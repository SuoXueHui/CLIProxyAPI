package auth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestCodexReplicaConcurrencyLeaseRejectsOnlyAtConfiguredLimit(t *testing.T) {
	resetCodexReplicaConcurrencyForTest()
	t.Cleanup(resetCodexReplicaConcurrencyForTest)
	auth := &Auth{
		ID:       "codex.json::replica:1",
		Provider: "codex",
		Attributes: map[string]string{
			AttributeCodexReplicaGroup:       "codex.json",
			AttributeCodexReplicaIndex:       "1",
			AttributeCodexReplicaCount:       "6",
			AttributeCodexReplicaConcurrency: "2",
		},
	}

	first, errFirst := AcquireCodexReplicaConcurrency(auth)
	if errFirst != nil || first == nil {
		t.Fatalf("first acquire = (%v, %v), want lease", first, errFirst)
	}
	second, errSecond := AcquireCodexReplicaConcurrency(auth)
	if errSecond != nil || second == nil {
		t.Fatalf("second acquire = (%v, %v), want lease", second, errSecond)
	}
	third, errThird := AcquireCodexReplicaConcurrency(auth)
	if third != nil || !IsCodexReplicaConcurrencyError(errThird) {
		t.Fatalf("third acquire = (%v, %v), want replica concurrency error", third, errThird)
	}
	var concurrencyErr *Error
	if !errors.As(errThird, &concurrencyErr) || concurrencyErr.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("third error = %#v, want HTTP 429", errThird)
	}
	if snapshot := CodexReplicaConcurrencySnapshot(auth.ID); snapshot.Active != 2 || snapshot.Limit != 2 {
		t.Fatalf("snapshot = %#v, want active=2 limit=2", snapshot)
	}

	first.Release()
	replacement, errReplacement := AcquireCodexReplicaConcurrency(auth)
	if errReplacement != nil || replacement == nil {
		t.Fatalf("replacement acquire = (%v, %v), want lease", replacement, errReplacement)
	}
	second.Release()
	replacement.Release()
	if snapshot := CodexReplicaConcurrencySnapshot(auth.ID); snapshot.Active != 0 || snapshot.Limit != 2 {
		t.Fatalf("released snapshot = %#v, want active=0 limit=2", snapshot)
	}
}

func TestCodexReplicaConcurrencyIsNoopForOrdinaryAuth(t *testing.T) {
	resetCodexReplicaConcurrencyForTest()
	t.Cleanup(resetCodexReplicaConcurrencyForTest)
	lease, errAcquire := AcquireCodexReplicaConcurrency(&Auth{ID: "ordinary", Provider: "codex"})
	if errAcquire != nil || lease != nil {
		t.Fatalf("ordinary acquire = (%v, %v), want no-op", lease, errAcquire)
	}
}

func TestCodexReplicaConcurrencyAvailabilityTracksActiveSlots(t *testing.T) {
	resetCodexReplicaConcurrencyForTest()
	t.Cleanup(resetCodexReplicaConcurrencyForTest)
	auth := codexReplicaConcurrencyTestAuth("replica-availability", "1")
	if !CodexReplicaConcurrencyAvailable(auth) {
		t.Fatal("unused replica reported unavailable")
	}
	lease, errAcquire := AcquireCodexReplicaConcurrency(auth)
	if errAcquire != nil || lease == nil {
		t.Fatalf("AcquireCodexReplicaConcurrency() = (%v, %v), want lease", lease, errAcquire)
	}
	if CodexReplicaConcurrencyAvailable(auth) {
		t.Fatal("full replica reported available")
	}
	lease.Release()
	if !CodexReplicaConcurrencyAvailable(auth) {
		t.Fatal("released replica remained unavailable")
	}
}

func codexReplicaConcurrencyTestAuth(authID, index string) *Auth {
	return &Auth{
		ID:       authID,
		Provider: "codex",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "test"},
		Attributes: map[string]string{
			AttributeCodexReplicaGroup:       "codex.json",
			AttributeCodexReplicaIndex:       index,
			AttributeCodexReplicaCount:       "6",
			AttributeCodexReplicaConcurrency: "1",
		},
	}
}

type replicaConcurrencyFallbackExecutor struct {
	firstCalls  atomic.Int32
	secondCalls atomic.Int32
}

func (*replicaConcurrencyFallbackExecutor) Identifier() string { return "codex" }

func (e *replicaConcurrencyFallbackExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if auth.ID == "replica-1" {
		e.firstCalls.Add(1)
		return cliproxyexecutor.Response{}, &Error{
			Code:       ErrorCodeCodexReplicaConcurrency,
			Message:    "replica busy",
			Retryable:  true,
			HTTPStatus: http.StatusTooManyRequests,
		}
	}
	e.secondCalls.Add(1)
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (*replicaConcurrencyFallbackExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (*replicaConcurrencyFallbackExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*replicaConcurrencyFallbackExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (*replicaConcurrencyFallbackExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestManagerRotatesPastReplicaConcurrencyWithoutCoolingAuth(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &replicaConcurrencyFallbackExecutor{}
	manager.RegisterExecutor(executor)
	for index := 1; index <= 2; index++ {
		authID := "replica-" + strconv.Itoa(index)
		registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "gpt-5.4"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
		auth := &Auth{
			ID:       authID,
			Provider: "codex",
			Status:   StatusActive,
			Metadata: map[string]any{"access_token": "test"},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register replica %d: %v", index, errRegister)
		}
	}

	response, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.4"}, cliproxyexecutor.Options{})
	if errExecute != nil || string(response.Payload) != `{"ok":true}` {
		t.Fatalf("Execute() = (%s, %v), want second replica success", response.Payload, errExecute)
	}
	if executor.firstCalls.Load() != 1 || executor.secondCalls.Load() != 1 {
		t.Fatalf("executor calls = (%d, %d), want one per replica", executor.firstCalls.Load(), executor.secondCalls.Load())
	}
	first, ok := manager.GetByID("replica-1")
	if !ok || first == nil {
		t.Fatal("first replica disappeared")
	}
	if first.Failed != 0 || first.Unavailable || !first.NextRetryAfter.IsZero() || first.Quota.Exceeded {
		t.Fatalf("local concurrency changed first replica availability: %#v", first)
	}
}

type replicaSelectionExecutor struct {
	calledAuthID string
}

func (*replicaSelectionExecutor) Identifier() string { return "codex" }

func (e *replicaSelectionExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calledAuthID = auth.ID
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (*replicaSelectionExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (*replicaSelectionExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*replicaSelectionExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (*replicaSelectionExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestManagerSkipsFiveFullReplicasAndUsesSixth(t *testing.T) {
	resetCodexReplicaConcurrencyForTest()
	t.Cleanup(resetCodexReplicaConcurrencyForTest)
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &replicaSelectionExecutor{}
	manager.RegisterExecutor(executor)
	leases := make([]*CodexReplicaConcurrencyLease, 0, 5)
	for index := 1; index <= 6; index++ {
		authID := "replica-selection-" + strconv.Itoa(index)
		auth := codexReplicaConcurrencyTestAuth(authID, strconv.Itoa(index))
		registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "gpt-5.4"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register replica %d: %v", index, errRegister)
		}
		if index <= 5 {
			lease, errAcquire := AcquireCodexReplicaConcurrency(auth)
			if errAcquire != nil {
				t.Fatalf("fill replica %d: %v", index, errAcquire)
			}
			leases = append(leases, lease)
		}
	}
	t.Cleanup(func() {
		for _, lease := range leases {
			lease.Release()
		}
	})

	response, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.4"}, cliproxyexecutor.Options{})
	if errExecute != nil || string(response.Payload) != `{"ok":true}` {
		t.Fatalf("Execute() = (%s, %v), want sixth replica success", response.Payload, errExecute)
	}
	if executor.calledAuthID != "replica-selection-6" {
		t.Fatalf("selected auth = %q, want replica-selection-6", executor.calledAuthID)
	}
}

type replicaParallelExecutor struct {
	started chan string
	release chan struct{}
	mu      sync.Mutex
	active  map[string]int
}

func (*replicaParallelExecutor) Identifier() string { return "codex" }

func (e *replicaParallelExecutor) Execute(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	lease, errAcquire := AcquireCodexReplicaConcurrency(auth)
	if errAcquire != nil {
		return cliproxyexecutor.Response{}, errAcquire
	}
	defer lease.Release()
	e.mu.Lock()
	e.active[auth.ID]++
	e.mu.Unlock()
	select {
	case e.started <- auth.ID:
	case <-ctx.Done():
		return cliproxyexecutor.Response{}, ctx.Err()
	}
	select {
	case <-e.release:
		return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
	case <-ctx.Done():
		return cliproxyexecutor.Response{}, ctx.Err()
	}
}

func (*replicaParallelExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (*replicaParallelExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*replicaParallelExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (*replicaParallelExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestSixCodexReplicasEachAcceptTenConcurrentRequests(t *testing.T) {
	resetCodexReplicaConcurrencyForTest()
	t.Cleanup(resetCodexReplicaConcurrencyForTest)
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &replicaParallelExecutor{
		started: make(chan string, 60),
		release: make(chan struct{}),
		active:  make(map[string]int),
	}
	manager.RegisterExecutor(executor)
	for index := 1; index <= 6; index++ {
		authID := "replica-parallel-" + strconv.Itoa(index)
		auth := codexReplicaConcurrencyTestAuth(authID, strconv.Itoa(index))
		auth.Attributes[AttributeCodexReplicaConcurrency] = "10"
		registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "gpt-5.4"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register replica %d: %v", index, errRegister)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errorsOut := make(chan error, 60)
	for requestIndex := 0; requestIndex < 60; requestIndex++ {
		go func() {
			response, errExecute := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.4"}, cliproxyexecutor.Options{})
			if errExecute == nil && string(response.Payload) != `{"ok":true}` {
				errExecute = errors.New("unexpected response")
			}
			errorsOut <- errExecute
		}()
	}

	for startedCount := 0; startedCount < 60; startedCount++ {
		select {
		case <-executor.started:
		case <-ctx.Done():
			close(executor.release)
			t.Fatalf("only %d requests started before timeout: %v", startedCount, ctx.Err())
		}
	}
	executor.mu.Lock()
	for index := 1; index <= 6; index++ {
		authID := "replica-parallel-" + strconv.Itoa(index)
		active := executor.active[authID]
		if active != 10 {
			executor.mu.Unlock()
			close(executor.release)
			t.Fatalf("%s active = %d, want 10", authID, active)
		}
	}
	executor.mu.Unlock()
	close(executor.release)
	for requestIndex := 0; requestIndex < 60; requestIndex++ {
		if errExecute := <-errorsOut; errExecute != nil {
			t.Fatalf("concurrent request failed: %v", errExecute)
		}
	}
}

type alwaysRefreshEvaluator struct{}

func (alwaysRefreshEvaluator) ShouldRefresh(time.Time, *Auth) bool { return true }

func TestCodexReplicaAutoRefreshUsesOnlyGroupLeader(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	makeReplica := func(index string) *Auth {
		return &Auth{
			ID:       "replica-" + index,
			Provider: "codex",
			Runtime:  alwaysRefreshEvaluator{},
			Attributes: map[string]string{
				AttributeCodexReplicaGroup:       "codex.json",
				AttributeCodexReplicaIndex:       index,
				AttributeCodexReplicaCount:       "6",
				AttributeCodexReplicaConcurrency: "10",
			},
		}
	}
	if !manager.shouldRefresh(makeReplica("1"), time.Now()) {
		t.Fatal("replica leader did not participate in automatic refresh")
	}
	if manager.shouldRefresh(makeReplica("2"), time.Now()) {
		t.Fatal("non-leader replica participated in automatic refresh")
	}
}

func TestManagerUpdateSynchronizesReplicaCredentialsWithoutReplacingRuntimeState(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	for index := 1; index <= 2; index++ {
		auth := &Auth{
			ID:         "replica-" + strconv.Itoa(index),
			Provider:   "codex",
			Status:     StatusActive,
			EgressIPv6: "2001:db8::" + strconv.Itoa(index),
			Metadata:   map[string]any{"access_token": "old", "refresh_token": "shared"},
			Attributes: map[string]string{
				AttributeCodexReplicaGroup:       "codex.json",
				AttributeCodexReplicaIndex:       strconv.Itoa(index),
				AttributeCodexReplicaCount:       "2",
				AttributeCodexReplicaConcurrency: "10",
			},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register replica %d: %v", index, errRegister)
		}
	}

	updated, _ := manager.GetByID("replica-2")
	updated.Metadata["access_token"] = "new"
	updated.Metadata["refresh_token"] = "rotated"
	if _, errUpdate := manager.Update(context.Background(), updated); errUpdate != nil {
		t.Fatalf("update replica: %v", errUpdate)
	}

	leader, _ := manager.GetByID("replica-1")
	if leader.Metadata["access_token"] != "new" || leader.Metadata["refresh_token"] != "rotated" {
		t.Fatalf("leader credentials = %#v, want synchronized tokens", leader.Metadata)
	}
	if leader.EgressIPv6 != "2001:db8::1" || leader.ID != "replica-1" {
		t.Fatalf("leader runtime identity changed: %#v", leader)
	}
}

func TestManagerUpdateReenablesEveryCodexReplica(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	for index := 1; index <= 2; index++ {
		auth := codexReplicaConcurrencyTestAuth("replica-enable-"+strconv.Itoa(index), strconv.Itoa(index))
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register replica %d: %v", index, errRegister)
		}
	}

	leader, _ := manager.GetByID("replica-enable-1")
	leader.Disabled = true
	leader.Status = StatusDisabled
	if _, errUpdate := manager.Update(context.Background(), leader); errUpdate != nil {
		t.Fatalf("disable group: %v", errUpdate)
	}
	for index := 1; index <= 2; index++ {
		replica, _ := manager.GetByID("replica-enable-" + strconv.Itoa(index))
		if !replica.Disabled || replica.Status != StatusDisabled {
			t.Fatalf("disabled replica %d = disabled %v status %s", index, replica.Disabled, replica.Status)
		}
	}

	leader, _ = manager.GetByID("replica-enable-1")
	leader.Disabled = false
	leader.Status = StatusActive
	leader.StatusMessage = ""
	if _, errUpdate := manager.Update(context.Background(), leader); errUpdate != nil {
		t.Fatalf("enable group: %v", errUpdate)
	}
	for index := 1; index <= 2; index++ {
		replica, _ := manager.GetByID("replica-enable-" + strconv.Itoa(index))
		if replica.Disabled || replica.Status != StatusActive {
			t.Fatalf("enabled replica %d = disabled %v status %s", index, replica.Disabled, replica.Status)
		}
	}
}
