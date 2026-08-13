package redisqueue

import (
	"path/filepath"
	"testing"
	"time"
)

func TestOutboxReopenPreservesPendingRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-outbox.sqlite")
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

	first, errOpen := openOutbox(path, func() time.Time { return now })
	if errOpen != nil {
		t.Fatalf("openOutbox() error = %v", errOpen)
	}
	if _, errEnqueue := first.enqueue([]byte(`{"id":1}`)); errEnqueue != nil {
		t.Fatalf("enqueue() error = %v", errEnqueue)
	}
	if errClose := first.close(); errClose != nil {
		t.Fatalf("close() error = %v", errClose)
	}

	second, errReopen := openOutbox(path, func() time.Time { return now })
	if errReopen != nil {
		t.Fatalf("reopen outbox error = %v", errReopen)
	}
	defer second.close()

	claim, errClaim := second.claim(1, time.Minute)
	if errClaim != nil {
		t.Fatalf("claim() error = %v", errClaim)
	}
	if len(claim.Items) != 1 || string(claim.Items[0].Payload) != `{"id":1}` {
		t.Fatalf("claim items = %+v, want persisted record", claim.Items)
	}
}

func TestOutboxUnackedClaimReturnsAfterLeaseExpiry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-outbox.sqlite")
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	outbox, errOpen := openOutbox(path, func() time.Time { return now })
	if errOpen != nil {
		t.Fatalf("openOutbox() error = %v", errOpen)
	}
	defer outbox.close()

	deliveryID, errEnqueue := outbox.enqueue([]byte(`{"id":1}`))
	if errEnqueue != nil {
		t.Fatalf("enqueue() error = %v", errEnqueue)
	}
	first, errClaim := outbox.claim(1, time.Minute)
	if errClaim != nil {
		t.Fatalf("first claim error = %v", errClaim)
	}
	if len(first.Items) != 1 || first.Items[0].DeliveryID != deliveryID {
		t.Fatalf("first claim = %+v, want delivery %q", first, deliveryID)
	}
	if second, errSecond := outbox.claim(1, time.Minute); errSecond != nil || len(second.Items) != 0 {
		t.Fatalf("claim during lease = %+v, err=%v, want empty", second, errSecond)
	}

	now = now.Add(time.Minute + time.Nanosecond)
	retry, errRetry := outbox.claim(1, time.Minute)
	if errRetry != nil {
		t.Fatalf("retry claim error = %v", errRetry)
	}
	if len(retry.Items) != 1 || retry.Items[0].DeliveryID != deliveryID || retry.LeaseID == first.LeaseID {
		t.Fatalf("retry claim = %+v, want same delivery with a new lease", retry)
	}
	stats, errStats := outbox.stats()
	if errStats != nil {
		t.Fatalf("stats() error = %v", errStats)
	}
	if stats.LeaseExpired != 1 {
		t.Fatalf("lease_expired = %d, want 1", stats.LeaseExpired)
	}
}

func TestOutboxCountsEachExpiredLeaseOnceAcrossPartialRetries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-outbox.sqlite")
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	outbox, errOpen := openOutbox(path, func() time.Time { return now })
	if errOpen != nil {
		t.Fatalf("openOutbox() error = %v", errOpen)
	}
	defer outbox.close()

	_, _ = outbox.enqueue([]byte(`{"id":1}`))
	_, _ = outbox.enqueue([]byte(`{"id":2}`))
	if _, errClaim := outbox.claim(2, time.Minute); errClaim != nil {
		t.Fatalf("initial claim error = %v", errClaim)
	}
	now = now.Add(time.Minute + time.Nanosecond)
	if _, errRetry := outbox.claim(1, time.Minute); errRetry != nil {
		t.Fatalf("first partial retry error = %v", errRetry)
	}
	if _, errRetry := outbox.claim(1, time.Minute); errRetry != nil {
		t.Fatalf("second partial retry error = %v", errRetry)
	}

	stats, errStats := outbox.stats()
	if errStats != nil {
		t.Fatalf("stats() error = %v", errStats)
	}
	if stats.LeaseExpired != 2 {
		t.Fatalf("lease_expired = %d, want each of two records counted once", stats.LeaseExpired)
	}
}

func TestOutboxAckDeletesOnlyMatchingIDsAndIsIdempotent(t *testing.T) {
	outbox, errOpen := openOutbox(filepath.Join(t.TempDir(), "usage-outbox.sqlite"), time.Now)
	if errOpen != nil {
		t.Fatalf("openOutbox() error = %v", errOpen)
	}
	defer outbox.close()

	firstID, _ := outbox.enqueue([]byte(`{"id":1}`))
	secondID, _ := outbox.enqueue([]byte(`{"id":2}`))
	claim, errClaim := outbox.claim(2, time.Minute)
	if errClaim != nil {
		t.Fatalf("claim() error = %v", errClaim)
	}

	acked, errAck := outbox.ack(claim.LeaseID, []string{firstID})
	if errAck != nil || acked != 1 {
		t.Fatalf("ack() = %d, err=%v, want 1", acked, errAck)
	}
	ackedAgain, errAgain := outbox.ack(claim.LeaseID, []string{firstID})
	if errAgain != nil || ackedAgain != 0 {
		t.Fatalf("idempotent ack() = %d, err=%v, want 0", ackedAgain, errAgain)
	}

	stats, errStats := outbox.stats()
	if errStats != nil {
		t.Fatalf("stats() error = %v", errStats)
	}
	if stats.Inflight != 1 || stats.Pending != 0 || stats.Acked != 1 {
		t.Fatalf("stats = %+v, want one inflight and one acked", stats)
	}
	if secondID == "" {
		t.Fatal("second delivery ID is empty")
	}
}

func TestConfigureOutboxFailureMakesEnqueueErrorAndCountsDrop(t *testing.T) {
	previous := snapshotGlobalOutboxForTest()
	t.Cleanup(func() { restoreGlobalOutboxForTest(previous) })

	badPath := filepath.Join(t.TempDir(), "missing", "usage-outbox.sqlite")
	if errConfigure := ConfigureOutbox(badPath); errConfigure == nil {
		t.Fatal("ConfigureOutbox() error = nil, want open failure")
	}
	SetEnabled(true)

	if errEnqueue := EnqueueWithError([]byte(`{"id":1}`)); errEnqueue == nil {
		t.Fatal("EnqueueWithError() error = nil, want persistent write failure")
	}
	stats := Status()
	if stats.Dropped != 1 || stats.Healthy {
		t.Fatalf("Status() = %+v, want one explicit drop and unhealthy", stats)
	}
}

func TestDisableDoesNotDeleteDurablePendingRecords(t *testing.T) {
	previous := snapshotGlobalOutboxForTest()
	t.Cleanup(func() { restoreGlobalOutboxForTest(previous) })

	path := filepath.Join(t.TempDir(), "usage-outbox.sqlite")
	if errConfigure := ConfigureOutbox(path); errConfigure != nil {
		t.Fatalf("ConfigureOutbox() error = %v", errConfigure)
	}
	SetEnabled(true)
	if errEnqueue := EnqueueWithError([]byte(`{"id":1}`)); errEnqueue != nil {
		t.Fatalf("EnqueueWithError() error = %v", errEnqueue)
	}

	SetEnabled(false)
	SetEnabled(true)
	claim, errClaim := Claim(1, time.Minute)
	if errClaim != nil {
		t.Fatalf("Claim() error = %v", errClaim)
	}
	if len(claim.Items) != 1 || string(claim.Items[0].Payload) != `{"id":1}` {
		t.Fatalf("claim after disable = %+v, want pending durable record", claim)
	}
}

func TestResolveOutboxPathDefaultsBesideConfigAndSupportsExplicitModes(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if got, want := ResolveOutboxPath(configPath, ""), filepath.Join(filepath.Dir(configPath), "usage-outbox.sqlite"); got != want {
		t.Fatalf("default ResolveOutboxPath() = %q, want %q", got, want)
	}
	if got := ResolveOutboxPath(configPath, disabledOutboxPath); got != disabledOutboxPath {
		t.Fatalf("disabled ResolveOutboxPath() = %q, want disabled", got)
	}
	if got, want := ResolveOutboxPath(configPath, "state/usage.sqlite"), filepath.Join(filepath.Dir(configPath), "state/usage.sqlite"); got != want {
		t.Fatalf("relative ResolveOutboxPath() = %q, want %q", got, want)
	}
}

func TestConfigureOutboxSamePathDoesNotCloseActiveHandle(t *testing.T) {
	previous := snapshotGlobalOutboxForTest()
	t.Cleanup(func() { restoreGlobalOutboxForTest(previous) })

	path := filepath.Join(t.TempDir(), "usage-outbox.sqlite")
	if errConfigure := ConfigureOutbox(path); errConfigure != nil {
		t.Fatalf("first ConfigureOutbox() error = %v", errConfigure)
	}
	if errConfigure := ConfigureOutbox(path); errConfigure != nil {
		t.Fatalf("second ConfigureOutbox() error = %v", errConfigure)
	}
	SetEnabled(true)
	if errEnqueue := EnqueueWithError([]byte(`{"id":1}`)); errEnqueue != nil {
		t.Fatalf("enqueue after repeated configure error = %v", errEnqueue)
	}
}

func TestOutboxOldestAgeExcludesInflightRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-outbox.sqlite")
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	outbox, errOpen := openOutbox(path, func() time.Time { return now })
	if errOpen != nil {
		t.Fatalf("openOutbox() error = %v", errOpen)
	}
	defer outbox.close()

	if _, errEnqueue := outbox.enqueue([]byte(`{"id":1}`)); errEnqueue != nil {
		t.Fatalf("enqueue first record: %v", errEnqueue)
	}
	now = now.Add(time.Minute)
	if _, errEnqueue := outbox.enqueue([]byte(`{"id":2}`)); errEnqueue != nil {
		t.Fatalf("enqueue second record: %v", errEnqueue)
	}
	if _, errClaim := outbox.claim(1, time.Minute); errClaim != nil {
		t.Fatalf("claim first record: %v", errClaim)
	}
	now = now.Add(30 * time.Second)
	stats, errStats := outbox.stats()
	if errStats != nil {
		t.Fatalf("stats() error = %v", errStats)
	}
	if stats.Pending != 1 || stats.Inflight != 1 || stats.OldestAgeSeconds != 30 {
		t.Fatalf("stats = %+v, want pending age based only on the second record", stats)
	}
}

func TestConfigureOutboxFailureKeepsExistingDurableBackend(t *testing.T) {
	previous := snapshotGlobalOutboxForTest()
	t.Cleanup(func() { restoreGlobalOutboxForTest(previous) })

	path := filepath.Join(t.TempDir(), "usage-outbox.sqlite")
	if errConfigure := ConfigureOutbox(path); errConfigure != nil {
		t.Fatalf("first ConfigureOutbox() error = %v", errConfigure)
	}
	badPath := filepath.Join(t.TempDir(), "missing", "usage-outbox.sqlite")
	if errConfigure := ConfigureOutbox(badPath); errConfigure == nil {
		t.Fatal("second ConfigureOutbox() error = nil, want invalid target error")
	}
	SetEnabled(true)
	if errEnqueue := EnqueueWithError([]byte(`{"id":1}`)); errEnqueue != nil {
		t.Fatalf("existing durable backend stopped after failed reconfigure: %v", errEnqueue)
	}
	if status := Status(); !status.Healthy || status.Pending != 1 {
		t.Fatalf("Status() = %+v, want healthy original backend with one pending record", status)
	}
}

func TestControlMessagesNeverEnterDurableOutbox(t *testing.T) {
	previous := snapshotGlobalOutboxForTest()
	t.Cleanup(func() { restoreGlobalOutboxForTest(previous) })

	path := filepath.Join(t.TempDir(), "usage-outbox.sqlite")
	if errConfigure := ConfigureOutbox(path); errConfigure != nil {
		t.Fatalf("ConfigureOutbox() error = %v", errConfigure)
	}
	SetEnabled(true)
	subscriber, unsubscribe := SubscribeUsage()
	defer unsubscribe()
	requireUsageSubscriberPayload(t, subscriber, usageSupportRefreshPayload)

	NotifyUsageRefresh()
	requireUsageSubscriberPayload(t, subscriber, usageRefreshPayload)
	status := Status()
	if status.Pending != 0 || status.Produced != 0 {
		t.Fatalf("Status() = %+v, want no durable control messages", status)
	}
}

func TestEnqueueControlMessagesNeverEnterDurableOutbox(t *testing.T) {
	previous := snapshotGlobalOutboxForTest()
	t.Cleanup(func() { restoreGlobalOutboxForTest(previous) })

	path := filepath.Join(t.TempDir(), "usage-outbox.sqlite")
	if errConfigure := ConfigureOutbox(path); errConfigure != nil {
		t.Fatalf("ConfigureOutbox() error = %v", errConfigure)
	}
	SetEnabled(true)
	subscriber, unsubscribe := SubscribeUsage()
	defer unsubscribe()
	requireUsageSubscriberPayload(t, subscriber, usageSupportRefreshPayload)

	Enqueue([]byte(usageRefreshPayload))
	requireUsageSubscriberPayload(t, subscriber, usageRefreshPayload)
	status := Status()
	if status.Pending != 0 || status.Produced != 0 {
		t.Fatalf("Status() = %+v, want Enqueue control frame to bypass persistence", status)
	}
}
