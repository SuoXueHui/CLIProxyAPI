package auth

import (
	"context"
	"errors"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

var _ CredentialWriteGate = (*Manager)(nil)

func TestManagerCredentialWritesEnabledDefaultsToTrueAndCanToggle(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	if !manager.WritesEnabled() {
		t.Fatal("WritesEnabled() = false, want backward-compatible true default")
	}

	manager.SetWritesEnabled(false)
	if manager.WritesEnabled() {
		t.Fatal("WritesEnabled() = true after disabling writes")
	}

	manager.SetWritesEnabled(true)
	if !manager.WritesEnabled() {
		t.Fatal("WritesEnabled() = false after re-enabling writes")
	}
}

func TestManagerCredentialWriteGateRejectsRequestRefreshBeforeExecutor(t *testing.T) {
	manager, executor, primary, _, _ := newUnauthorizedRefreshFixture(t, false)
	manager.SetWritesEnabled(false)

	refreshed, errRefresh := manager.refreshAuthForRequest(context.Background(), primary.ID, "stale-access-token")
	if !errors.Is(errRefresh, ErrCredentialWritesDisabled) {
		t.Fatalf("refreshAuthForRequest() error = %v, want credential write gate rejection", errRefresh)
	}
	if refreshed != nil {
		t.Fatalf("refreshAuthForRequest() auth = %#v, want nil", refreshed)
	}
	if got := executor.RefreshCalls(); got != 0 {
		t.Fatalf("Refresh calls = %d, want 0 while credential writes are disabled", got)
	}
}

func TestManagerCredentialWriteGateSkipsUnauthorizedRequestRefresh(t *testing.T) {
	manager, executor, primary, backup, model := newUnauthorizedRefreshFixture(t, false)
	manager.SetWritesEnabled(false)

	resp, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v, want fallback credential success", errExecute)
	}
	if got := string(resp.Payload); got != backup.ID+":backup-access-token" {
		t.Fatalf("payload = %q, want backup credential response", got)
	}
	if got := executor.RefreshCalls(); got != 0 {
		t.Fatalf("Refresh calls = %d, want 0 while credential writes are disabled", got)
	}
	if calls := executor.ExecuteCalls(); len(calls) != 2 || calls[0] != primary.ID || calls[1] != backup.ID {
		t.Fatalf("Execute calls = %v, want [primary, backup]", calls)
	}
}

func TestManagerCredentialWriteGateSkipsHomeUnauthorizedRefresh(t *testing.T) {
	dispatcher := &homeUnauthorizedRefreshDispatcher{}
	executor := &homeUnauthorizedRefreshExecutor{}
	manager := newHomeUnauthorizedRefreshManager(dispatcher, executor)
	manager.SetWritesEnabled(false)

	_, errExecute := manager.Execute(context.Background(), []string{homeUnauthorizedRefreshProvider}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
	if statusCodeFromError(errExecute) != 401 {
		t.Fatalf("Execute() error = %v, want original unauthorized failure", errExecute)
	}
	if got := executor.refreshCalls.Load(); got != 0 {
		t.Fatalf("Home Refresh calls = %d, want 0 while credential writes are disabled", got)
	}
}

func TestManagerCredentialWriteGateSkipsRequestAuthPreparation(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	executor := &requestPrepareExecutor{}
	auth := &Auth{
		ID:       "readonly-request-auth",
		Provider: "antigravity",
		Metadata: map[string]any{"access_token": "token"},
	}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	manager.SetWritesEnabled(false)

	prepared, errPrepare := manager.prepareRequestAuth(context.Background(), executor, auth)
	if errPrepare != nil {
		t.Fatalf("prepareRequestAuth() error = %v", errPrepare)
	}
	if got := executor.prepareCalls.Load(); got != 0 {
		t.Fatalf("PrepareRequestAuth calls = %d, want 0 while credential writes are disabled", got)
	}
	if prepared == nil || testStringValue(prepared.Metadata["project_id"]) != "" {
		t.Fatalf("prepared auth = %#v, want original credential snapshot", prepared)
	}
}

func TestManagerCredentialWriteGateSkipsHomeRequestAuthPreparation(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	executor := &requestPrepareExecutor{}
	auth := &Auth{
		ID:       "readonly-home-request-auth",
		Provider: "antigravity",
		Metadata: map[string]any{"access_token": "token"},
	}
	manager.SetWritesEnabled(false)

	prepared, errPrepare := manager.prepareHomeAuthSnapshot(context.Background(), executor, auth)
	if errPrepare != nil {
		t.Fatalf("prepareHomeAuthSnapshot() error = %v", errPrepare)
	}
	if got := executor.prepareCalls.Load(); got != 0 {
		t.Fatalf("Home PrepareRequestAuth calls = %d, want 0 while credential writes are disabled", got)
	}
	if prepared == nil || testStringValue(prepared.Metadata["project_id"]) != "" {
		t.Fatalf("prepared Home auth = %#v, want original credential snapshot", prepared)
	}
}

func TestManagerCredentialWriteGateSuppressesAuthAndCooldownPersistence(t *testing.T) {
	authStore := &countingStore{}
	cooldownStore := &recordingCooldownStateStore{}
	manager := NewManager(authStore, nil, nil)
	manager.SetCooldownStateStore(cooldownStore)
	manager.SetWritesEnabled(false)

	auth := &Auth{
		ID:       "readonly-persistence",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex"},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	manager.PersistCooldownStates(context.Background())
	if got := authStore.saveCount.Load(); got != 0 {
		t.Fatalf("auth Save calls = %d, want 0 while credential writes are disabled", got)
	}
	if got := cooldownStore.saveCount.Load(); got != 0 {
		t.Fatalf("cooldown Save calls = %d, want 0 while credential writes are disabled", got)
	}

	manager.SetWritesEnabled(true)
	if _, errUpdate := manager.Update(context.Background(), auth); errUpdate != nil {
		t.Fatalf("Update() error = %v", errUpdate)
	}
	manager.PersistCooldownStates(context.Background())
	if got := authStore.saveCount.Load(); got != 1 {
		t.Fatalf("auth Save calls = %d, want 1 after re-enabling writes", got)
	}
	if got := cooldownStore.saveCount.Load(); got != 1 {
		t.Fatalf("cooldown Save calls = %d, want 1 after re-enabling writes", got)
	}
}
