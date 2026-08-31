package redisqueue

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStartupOutboxMaintenanceRequiresDisabledQueue(t *testing.T) {
	previous := snapshotGlobalOutboxForTest()
	previousEnabled := Enabled()
	t.Cleanup(func() {
		SetEnabled(false)
		restoreGlobalOutboxForTest(previous)
		SetEnabled(previousEnabled)
	})

	path := filepath.Join(t.TempDir(), "usage-outbox.sqlite")
	if errConfigure := ConfigureOutbox(path); errConfigure != nil {
		t.Fatalf("ConfigureOutbox() error = %v", errConfigure)
	}
	SetEnabled(true)

	_, errMaintain := maintainOutboxAtStartup(context.Background(), 1, 0)
	if errMaintain == nil || !strings.Contains(errMaintain.Error(), "disabled") {
		t.Fatalf("maintainOutboxAtStartup() error = %v, want disabled queue requirement", errMaintain)
	}
}

func TestStartupOutboxMaintenanceReclaimsAckedPagesAndPreservesPendingData(t *testing.T) {
	previous := snapshotGlobalOutboxForTest()
	previousEnabled := Enabled()
	t.Cleanup(func() {
		SetEnabled(false)
		restoreGlobalOutboxForTest(previous)
		SetEnabled(previousEnabled)
	})

	path := filepath.Join(t.TempDir(), "usage-outbox.sqlite")
	SetEnabled(false)
	if errConfigure := ConfigureOutbox(path); errConfigure != nil {
		t.Fatalf("ConfigureOutbox() error = %v", errConfigure)
	}
	SetEnabled(true)
	for index := 0; index < 40; index++ {
		payload := []byte(`{"id":` + strings.Repeat("x", 64*1024) + `}`)
		if errEnqueue := EnqueueWithError(payload); errEnqueue != nil {
			t.Fatalf("EnqueueWithError(%d) error = %v", index, errEnqueue)
		}
	}
	claim, errClaim := Claim(40, time.Minute)
	if errClaim != nil {
		t.Fatalf("Claim() error = %v", errClaim)
	}
	if len(claim.Items) != 40 {
		t.Fatalf("Claim() items = %d, want 40", len(claim.Items))
	}
	ackedIDs := make([]string, 0, len(claim.Items)-1)
	for _, item := range claim.Items[:len(claim.Items)-1] {
		ackedIDs = append(ackedIDs, item.DeliveryID)
	}
	if acked, errAck := Ack(claim.LeaseID, ackedIDs); errAck != nil || acked != 39 {
		t.Fatalf("Ack() = %d, err=%v, want 39", acked, errAck)
	}
	SetEnabled(false)

	before := Status()
	if before.StorageBytes == 0 || before.ReclaimableBytes == 0 {
		t.Fatalf("Status() before compaction = %+v, want allocated and reclaimable bytes", before)
	}
	result, errMaintain := maintainOutboxAtStartup(context.Background(), 1, 0)
	if errMaintain != nil {
		t.Fatalf("maintainOutboxAtStartup() error = %v", errMaintain)
	}
	if !result.Performed || result.BeforeBytes != before.StorageBytes || result.AfterBytes >= result.BeforeBytes || result.ReclaimedBytes != result.BeforeBytes-result.AfterBytes {
		t.Fatalf("maintainOutboxAtStartup() = %+v, want a smaller consistent storage size", result)
	}
	physicalBytes := existingFileSize(t, path) + existingFileSize(t, path+"-wal") + existingFileSize(t, path+"-shm")
	if physicalBytes != result.AfterBytes {
		t.Fatalf("physical database size after compaction = %d, want %d", physicalBytes, result.AfterBytes)
	}
	if walBytes := existingFileSize(t, path+"-wal"); walBytes != 0 {
		t.Fatalf("WAL file size after compaction = %d, want 0", walBytes)
	}
	after := Status()
	if after.StorageBytes != result.AfterBytes || after.ReclaimableBytes >= before.ReclaimableBytes {
		t.Fatalf("Status() after compaction = %+v, result=%+v, want reclaimed storage", after, result)
	}
	if after.Pending != 0 || after.Inflight != 1 || after.Produced != 40 || after.Acked != 39 {
		t.Fatalf("Status() after compaction = %+v, want pending data and counters preserved", after)
	}

	SetEnabled(true)
	remaining, errRemaining := Claim(1, time.Minute)
	if errRemaining != nil || len(remaining.Items) != 0 {
		t.Fatalf("Claim() before lease expiry = %+v, err=%v, want preserved inflight record", remaining, errRemaining)
	}
}

func TestStartupOutboxMaintenanceSkipsBelowThreshold(t *testing.T) {
	previous := snapshotGlobalOutboxForTest()
	previousEnabled := Enabled()
	t.Cleanup(func() {
		SetEnabled(false)
		restoreGlobalOutboxForTest(previous)
		SetEnabled(previousEnabled)
	})

	SetEnabled(false)
	if errConfigure := ConfigureOutbox(filepath.Join(t.TempDir(), "usage-outbox.sqlite")); errConfigure != nil {
		t.Fatal(errConfigure)
	}
	result, errMaintain := maintainOutboxAtStartup(context.Background(), 1<<30, 0.5)
	if errMaintain != nil {
		t.Fatal(errMaintain)
	}
	if result.Performed {
		t.Fatalf("maintenance performed below threshold: %+v", result)
	}
}

func TestOutboxPhysicalStorageBytesIncludesWALAndSHM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage-outbox.sqlite")
	for name, size := range map[string]int{
		path: 11, path + "-wal": 13, path + "-shm": 17,
	} {
		if errWrite := os.WriteFile(name, make([]byte, size), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	bytes, errBytes := outboxPhysicalStorageBytes(path)
	if errBytes != nil {
		t.Fatal(errBytes)
	}
	if bytes != 41 {
		t.Fatalf("physical storage bytes = %d, want 41", bytes)
	}
}

func existingFileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, errStat := os.Stat(path)
	if os.IsNotExist(errStat) {
		return 0
	}
	if errStat != nil {
		t.Fatalf("stat %q: %v", path, errStat)
	}
	return info.Size()
}

func TestEnqueueDoesNotWriteAfterQueueIsDisabledWhileWaitingForRuntimeLock(t *testing.T) {
	previous := snapshotGlobalOutboxForTest()
	previousEnabled := Enabled()
	previousMaxProcs := runtime.GOMAXPROCS(1)
	t.Cleanup(func() {
		SetEnabled(false)
		restoreGlobalOutboxForTest(previous)
		SetEnabled(previousEnabled)
		runtime.GOMAXPROCS(previousMaxProcs)
	})

	SetEnabled(false)
	if errConfigure := ConfigureOutbox(filepath.Join(t.TempDir(), "usage-outbox.sqlite")); errConfigure != nil {
		t.Fatalf("ConfigureOutbox() error = %v", errConfigure)
	}
	SetEnabled(true)
	runtimeOutbox.mu.Lock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- EnqueueWithError([]byte(`{"id":1}`))
	}()
	<-started
	runtime.Gosched()
	// This is the intermediate state of SetEnabled(false) before it waits on the runtime lock.
	enabled.Store(false)
	runtimeOutbox.mu.Unlock()
	if errEnqueue := <-done; errEnqueue != nil {
		t.Fatalf("EnqueueWithError() error = %v", errEnqueue)
	}
	if status := Status(); status.Pending != 0 || status.Produced != 0 {
		t.Fatalf("Status() = %+v, want no write after disable", status)
	}
}

func TestMemoryEnqueueDoesNotWriteAfterQueueIsDisabledWhileWaitingForQueueLock(t *testing.T) {
	previous := snapshotGlobalOutboxForTest()
	previousEnabled := Enabled()
	previousMaxProcs := runtime.GOMAXPROCS(1)
	t.Cleanup(func() {
		SetEnabled(false)
		restoreGlobalOutboxForTest(previous)
		SetEnabled(previousEnabled)
		runtime.GOMAXPROCS(previousMaxProcs)
	})

	SetEnabled(false)
	if errConfigure := ConfigureOutbox(disabledOutboxPath); errConfigure != nil {
		t.Fatalf("ConfigureOutbox() error = %v", errConfigure)
	}
	SetEnabled(true)
	subscriber, unsubscribe := SubscribeUsage()
	defer unsubscribe()
	requireUsageSubscriberPayload(t, subscriber, usageSupportRefreshPayload)
	global.mu.Lock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- EnqueueWithError([]byte(`{"id":1}`))
	}()
	<-started
	runtime.Gosched()
	// Acquiring this lock confirms that the producer has already left the runtime section.
	runtimeOutbox.mu.Lock()
	runtimeOutbox.mu.Unlock()
	enabled.Store(false)
	global.mu.Unlock()
	if errEnqueue := <-done; errEnqueue != nil {
		t.Fatalf("EnqueueWithError() error = %v", errEnqueue)
	}
	if status := Status(); status.Pending != 0 || status.Produced != 0 {
		t.Fatalf("Status() = %+v, want no memory write after disable", status)
	}
	select {
	case payload := <-subscriber:
		t.Fatalf("subscriber payload after disable = %s, want none", payload)
	case <-time.After(25 * time.Millisecond):
	}
}

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
