package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func adaptiveSchedulerTestConfig() internalconfig.AdaptiveAuthConfig {
	return internalconfig.AdaptiveAuthConfig{
		Enabled:             true,
		FirstEventThreshold: 15 * time.Second,
		SlowStreak:          2,
		Penalty:             30 * time.Second,
		MaxPenalty:          5 * time.Minute,
		EWMAAlpha:           0.2,
		MinSamples:          3,
		LoadFloor:           10 * time.Second,
		StateTTL:            6 * time.Hour,
		MaxStateEntries:     4096,
	}.WithDefaults()
}

func TestAdaptiveSchedulerSoftPenaltyAvoidsSlowAuthAndFailsOpen(t *testing.T) {
	scheduler := newAuthScheduler(&RoundRobinSelector{})
	scheduler.setAdaptiveConfig(adaptiveSchedulerTestConfig())
	scheduler.rebuild([]*Auth{
		{ID: "slow", Provider: "gemini"},
		{ID: "healthy", Provider: "gemini"},
	})

	for i := 0; i < 3; i++ {
		scheduler.reportAdaptiveObservation("gemini", "", "slow", 20*time.Second, 30*time.Second, true)
	}
	got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() error = %v", errPick)
	}
	if got == nil || got.ID != "healthy" {
		t.Fatalf("pickSingle() auth = %#v, want healthy auth", got)
	}

	for i := 0; i < 3; i++ {
		scheduler.reportAdaptiveObservation("gemini", "", "healthy", 20*time.Second, 30*time.Second, true)
	}
	got, errPick = scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() with all soft-penalized auths error = %v", errPick)
	}
	if got == nil {
		t.Fatal("pickSingle() with all soft-penalized auths returned nil")
	}
}

func TestAdaptiveSchedulerPrefersLowerVirtualLoad(t *testing.T) {
	scheduler := newAuthScheduler(&RoundRobinSelector{})
	scheduler.setAdaptiveConfig(adaptiveSchedulerTestConfig())
	scheduler.rebuild([]*Auth{
		{ID: "busy", Provider: "gemini"},
		{ID: "free", Provider: "gemini"},
	})

	busyOne := scheduler.acquireAdaptive("gemini", "", "busy")
	busyTwo := scheduler.acquireAdaptive("gemini", "", "busy")
	free := scheduler.acquireAdaptive("gemini", "", "free")
	t.Cleanup(func() {
		busyOne.release(0, 10*time.Second, true)
		busyTwo.release(0, 10*time.Second, true)
		free.release(0, 10*time.Second, true)
	})

	got, errPick := scheduler.pickSingle(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() error = %v", errPick)
	}
	if got == nil || got.ID != "free" {
		t.Fatalf("pickSingle() auth = %#v, want free auth", got)
	}
}

func TestAdaptiveSchedulerFiltersPluginCandidates(t *testing.T) {
	scheduler := newAuthScheduler(&RoundRobinSelector{})
	scheduler.setAdaptiveConfig(adaptiveSchedulerTestConfig())
	for i := 0; i < 3; i++ {
		scheduler.reportAdaptiveObservation("gemini", "", "slow", 20*time.Second, 30*time.Second, true)
	}

	candidates := []*Auth{{ID: "slow", Provider: "gemini"}, {ID: "healthy", Provider: "gemini"}}
	filtered := scheduler.filterAdaptiveCandidates("gemini", "", candidates, "")
	if len(filtered) != 1 || filtered[0].ID != "healthy" {
		t.Fatalf("filterAdaptiveCandidates() = %#v, want healthy only", filtered)
	}
}

func TestAdaptiveSchedulerMixedSelectionAvoidsSlowProvider(t *testing.T) {
	scheduler := newAuthScheduler(&RoundRobinSelector{})
	scheduler.setAdaptiveConfig(adaptiveSchedulerTestConfig())
	scheduler.rebuild([]*Auth{
		{ID: "gemini-slow", Provider: "gemini"},
		{ID: "claude-healthy", Provider: "claude"},
	})
	for i := 0; i < 3; i++ {
		scheduler.reportAdaptiveObservation("gemini", "", "gemini-slow", 20*time.Second, 30*time.Second, true)
	}
	got, provider, errPick := scheduler.pickMixed(context.Background(), []string{"gemini", "claude"}, "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickMixed() error = %v", errPick)
	}
	if got == nil || got.ID != "claude-healthy" || provider != "claude" {
		t.Fatalf("pickMixed() = auth %#v provider %q, want claude-healthy/claude", got, provider)
	}
}

func TestAdaptiveSchedulerStreamLeaseReleasesAfterCompletion(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetConfig(&internalconfig.Config{Routing: internalconfig.RoutingConfig{AdaptiveAuth: adaptiveSchedulerTestConfig()}})
	auth := &Auth{ID: "stream-auth", Provider: "gemini"}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	lease := manager.scheduler.acquireAdaptive("gemini", "", auth.ID)
	remaining := make(chan cliproxyexecutor.StreamChunk, 1)
	remaining <- cliproxyexecutor.StreamChunk{Payload: []byte("data")}
	close(remaining)
	wrapped := manager.wrapStreamResult(context.Background(), auth, "gemini", "", http.Header{}, nil, remaining, OAuthModelAliasResult{}, false, cliproxyexecutor.Options{}, time.Now().Add(-time.Second), time.Second, lease)
	for range wrapped.Chunks {
	}

	manager.scheduler.mu.Lock()
	if got := manager.scheduler.adaptive.load[adaptiveLoadKey{provider: "gemini", authID: auth.ID}]; got != 0 {
		t.Fatalf("adaptive in-flight load = %d, want 0", got)
	}
	manager.scheduler.mu.Unlock()
}
