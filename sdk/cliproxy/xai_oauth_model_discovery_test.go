package cliproxy

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestResolveXAIOAuthModelsUsesDiscoveredChatSetAndPreservesStaticMetadata(t *testing.T) {
	static := []*ModelInfo{
		{ID: "grok-4.7", DisplayName: "Grok 4.7", ContextLength: 500000, OwnedBy: "xai", Type: "xai"},
		{ID: "grok-static-only", DisplayName: "Static only", ContextLength: 100000, OwnedBy: "xai", Type: "xai"},
		{ID: "grok-imagine-image", DisplayName: "Grok Imagine Image", OwnedBy: "xai", Type: "xai"},
	}
	discovered := []*ModelInfo{
		{ID: "grok-4.7", DisplayName: "upstream-name", ContextLength: 1},
		{ID: "grok-4.7-build-fast", DisplayName: "grok-4.7-build-fast", OwnedBy: "xai", Type: "xai"},
		{ID: "grok-4.7-build-fast"},
	}

	resolved := resolveXAIOAuthModels(static, discovered, true)
	if len(resolved) != 3 {
		t.Fatalf("resolved model count = %d, want 3", len(resolved))
	}
	if resolved[0].DisplayName != "Grok 4.7" || resolved[0].ContextLength != 500000 {
		t.Fatalf("static metadata was replaced: %+v", resolved[0])
	}
	if resolved[1].ID != "grok-4.7-build-fast" {
		t.Fatalf("dynamic model = %+v", resolved[1])
	}
	if resolved[2].ID != "grok-imagine-image" {
		t.Fatalf("static media model was removed: %+v", resolved[2])
	}
	for _, model := range resolved {
		if model.ID == "grok-static-only" {
			t.Fatal("static-only chat model remained after successful discovery")
		}
	}

	fallback := resolveXAIOAuthModels(static, nil, false)
	if len(fallback) != len(static) {
		t.Fatalf("fallback model count = %d, want %d", len(fallback), len(static))
	}
}

func TestXAIOAuthModelDiscoveryCachesPerAuthAndNotifiesOnChanges(t *testing.T) {
	responses := map[string][]*ModelInfo{
		"auth-a": {
			{ID: "grok-4.7-build-fast", OwnedBy: "xai", Type: "xai"},
			{ID: "grok-4.7"},
		},
		"auth-b": {{ID: "grok-private-b"}},
	}
	var changed []string
	discovery := newXAIOAuthModelDiscovery(
		func(_ context.Context, auth *coreauth.Auth) ([]*ModelInfo, error) {
			return responses[auth.ID], nil
		},
		func(auth *coreauth.Auth) { changed = append(changed, auth.ID) },
	)
	authA := &coreauth.Auth{ID: "auth-a", Provider: "xai", Metadata: map[string]any{"access_token": "a"}}
	authB := &coreauth.Auth{ID: "auth-b", Provider: "xai", Metadata: map[string]any{"access_token": "b"}}

	if updated, err := discovery.refreshAuth(context.Background(), authA); err != nil || !updated {
		t.Fatalf("refresh auth-a = (%t, %v), want (true, nil)", updated, err)
	}
	if updated, err := discovery.refreshAuth(context.Background(), authB); err != nil || !updated {
		t.Fatalf("refresh auth-b = (%t, %v), want (true, nil)", updated, err)
	}
	if got, ok := discovery.modelsForAuth(authA); !ok || len(got) != 2 || got[0].ID != "grok-4.7" || got[1].ID != "grok-4.7-build-fast" {
		t.Fatalf("auth-a models = %+v", got)
	}
	if got, ok := discovery.modelsForAuth(authB); !ok || len(got) != 1 || got[0].ID != "grok-private-b" {
		t.Fatalf("auth-b models = %+v", got)
	}
	if len(changed) != 2 || changed[0] != "auth-a" || changed[1] != "auth-b" {
		t.Fatalf("changed callbacks = %v", changed)
	}

	responses["auth-a"] = []*ModelInfo{
		{ID: "grok-4.7"},
		{ID: "grok-4.7-build-fast", OwnedBy: "xai", Type: "xai"},
	}
	if updated, err := discovery.refreshAuth(context.Background(), authA); err != nil || updated {
		t.Fatalf("unchanged refresh = (%t, %v), want (false, nil)", updated, err)
	}
	if len(changed) != 2 {
		t.Fatalf("unchanged refresh callbacks = %v", changed)
	}

	responses["auth-a"] = append(responses["auth-a"], &ModelInfo{ID: "grok-new"})
	if updated, err := discovery.refreshAuth(context.Background(), authA); err != nil || !updated {
		t.Fatalf("changed refresh = (%t, %v), want (true, nil)", updated, err)
	}
	if len(changed) != 3 || changed[2] != "auth-a" {
		t.Fatalf("changed refresh callbacks = %v", changed)
	}
}

func TestXAIOAuthModelDiscoveryRetainsLastSuccessOnFailureOrEmpty(t *testing.T) {
	mode := "success"
	discovery := newXAIOAuthModelDiscovery(
		func(_ context.Context, _ *coreauth.Auth) ([]*ModelInfo, error) {
			switch mode {
			case "failure":
				return nil, errors.New("temporary failure")
			case "empty":
				return nil, nil
			default:
				return []*ModelInfo{{ID: "grok-4.7-build-fast"}}, nil
			}
		},
		nil,
	)
	auth := &coreauth.Auth{ID: "auth-a", Provider: "xai", Metadata: map[string]any{"access_token": "a"}}

	if updated, err := discovery.refreshAuth(context.Background(), auth); err != nil || !updated {
		t.Fatalf("initial refresh = (%t, %v), want (true, nil)", updated, err)
	}
	mode = "failure"
	if updated, err := discovery.refreshAuth(context.Background(), auth); err == nil || updated {
		t.Fatalf("failed refresh = (%t, %v), want (false, error)", updated, err)
	}
	if got, ok := discovery.modelsForAuth(auth); !ok || len(got) != 1 || got[0].ID != "grok-4.7-build-fast" {
		t.Fatalf("models after failure = %+v", got)
	}

	mode = "empty"
	if updated, err := discovery.refreshAuth(context.Background(), auth); err == nil || updated {
		t.Fatalf("empty refresh = (%t, %v), want (false, error)", updated, err)
	}
	if got, ok := discovery.modelsForAuth(auth); !ok || len(got) != 1 || got[0].ID != "grok-4.7-build-fast" {
		t.Fatalf("models after empty response = %+v", got)
	}
}

func TestXAIOAuthModelDiscoveryRejectsStaleCredentialSnapshot(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var currentMu sync.RWMutex
	current := &coreauth.Auth{ID: "auth-a", Provider: "xai", Metadata: map[string]any{"access_token": "old"}}
	discovery := newXAIOAuthModelDiscovery(
		func(_ context.Context, _ *coreauth.Auth) ([]*ModelInfo, error) {
			close(started)
			<-release
			return []*ModelInfo{{ID: "grok-old-account"}}, nil
		},
		nil,
	)
	discovery.isCurrent = func(auth *coreauth.Auth) bool {
		currentMu.RLock()
		defer currentMu.RUnlock()
		return sameXAIDiscoveryCredential(auth, current)
	}

	done := make(chan error, 1)
	go func() {
		_, err := discovery.refreshAuth(context.Background(), current)
		done <- err
	}()
	<-started
	currentMu.Lock()
	current = &coreauth.Auth{ID: "auth-a", Provider: "xai", Metadata: map[string]any{"access_token": "new"}}
	currentMu.Unlock()
	close(release)

	if err := <-done; !errors.Is(err, errXAIDiscoveryCredentialChanged) {
		t.Fatalf("refresh error = %v, want %v", err, errXAIDiscoveryCredentialChanged)
	}
	if models, ok := discovery.modelsForAuth(current); ok || len(models) != 0 {
		t.Fatalf("stale models were cached: %+v", models)
	}
}

func TestXAIOAuthModelDiscoverySnapshotDoesNotMatchReplacementCredential(t *testing.T) {
	discovery := newXAIOAuthModelDiscovery(
		func(_ context.Context, _ *coreauth.Auth) ([]*ModelInfo, error) {
			return []*ModelInfo{{ID: "grok-old-account"}}, nil
		},
		nil,
	)
	oldAuth := &coreauth.Auth{ID: "auth-a", Provider: "xai", Metadata: map[string]any{"access_token": "old"}}
	newAuth := &coreauth.Auth{ID: "auth-a", Provider: "xai", Metadata: map[string]any{"access_token": "new"}}
	if _, err := discovery.refreshAuth(context.Background(), oldAuth); err != nil {
		t.Fatalf("refresh old auth: %v", err)
	}
	if models, ok := discovery.modelsForAuth(newAuth); ok || len(models) != 0 {
		t.Fatalf("replacement auth inherited stale models: %+v", models)
	}
}

func TestXAIOAuthModelDiscoveryBoundsCredentialCapabilityFetch(t *testing.T) {
	discovery := newXAIOAuthModelDiscovery(
		func(ctx context.Context, _ *coreauth.Auth) ([]*ModelInfo, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		nil,
	)
	discovery.fetchTimeout = 20 * time.Millisecond
	auth := &coreauth.Auth{ID: "auth-a", Provider: "xai", Metadata: map[string]any{"access_token": "token"}}

	startedAt := time.Now()
	if _, err := discovery.refreshAuth(context.Background(), auth); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("refresh error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("bounded refresh took %s", elapsed)
	}
}

func TestServiceRegistersDiscoveredXAIModelsOnlyForMatchingOAuthAuth(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	authA := &coreauth.Auth{
		ID:       "xai-discovery-auth-a",
		Provider: "xai",
		Metadata: map[string]any{"access_token": "a"},
	}
	authB := &coreauth.Auth{
		ID:       "xai-discovery-auth-b",
		Provider: "xai",
		Metadata: map[string]any{"access_token": "b"},
	}
	apiKeyAuth := &coreauth.Auth{
		ID:         "xai-discovery-api-key",
		Provider:   "xai",
		Attributes: map[string]string{"api_key": "key", "auth_kind": "apikey"},
	}
	for _, authID := range []string{authA.ID, authB.ID, apiKeyAuth.ID} {
		modelRegistry.UnregisterClient(authID)
		t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })
	}

	discovery := newXAIOAuthModelDiscovery(
		func(_ context.Context, auth *coreauth.Auth) ([]*ModelInfo, error) {
			if auth.ID == authA.ID {
				return []*ModelInfo{{ID: "grok-4.7-build-fast"}}, nil
			}
			return []*ModelInfo{{ID: "grok-4.7"}}, nil
		},
		nil,
	)
	if _, err := discovery.refreshAuth(context.Background(), authA); err != nil {
		t.Fatalf("refresh auth-a: %v", err)
	}
	if _, err := discovery.refreshAuth(context.Background(), authB); err != nil {
		t.Fatalf("refresh auth-b: %v", err)
	}

	service := &Service{cfg: &config.Config{}, xaiModelDiscovery: discovery}
	service.registerModelsForAuth(context.Background(), authA)
	service.registerModelsForAuth(context.Background(), authB)
	service.registerModelsForAuth(context.Background(), apiKeyAuth)

	if !modelRegistry.ClientSupportsModel(authA.ID, "grok-4.7-build-fast") {
		t.Fatal("auth-a does not support its discovered model")
	}
	if modelRegistry.ClientSupportsModel(authA.ID, "grok-4.6") {
		t.Fatal("auth-a inherited a static chat model absent from discovery")
	}
	if !modelRegistry.ClientSupportsModel(authA.ID, "grok-imagine-image") {
		t.Fatal("auth-a lost the static image model that uses a separate upstream API")
	}
	if modelRegistry.ClientSupportsModel(authB.ID, "grok-4.7-build-fast") {
		t.Fatal("auth-b inherited auth-a discovered model")
	}
	if modelRegistry.ClientSupportsModel(apiKeyAuth.ID, "grok-4.7-build-fast") {
		t.Fatal("API-key auth inherited OAuth discovered model")
	}
	if !modelRegistry.ClientSupportsModel(authB.ID, "grok-4.7") {
		t.Fatal("auth-b lost its discovered/static model")
	}
}

func TestXAIOAuthModelDiscoveryRefreshesOnlyEligibleAuths(t *testing.T) {
	var fetched []string
	discovery := newXAIOAuthModelDiscovery(
		func(_ context.Context, auth *coreauth.Auth) ([]*ModelInfo, error) {
			fetched = append(fetched, auth.ID)
			return []*ModelInfo{{ID: "grok-4.7"}}, nil
		},
		nil,
	)
	auths := []*coreauth.Auth{
		{ID: "eligible", Provider: "xai", Metadata: map[string]any{"access_token": "token"}},
		{ID: "disabled", Provider: "xai", Disabled: true, Metadata: map[string]any{"access_token": "token"}},
		{ID: "api-key", Provider: "xai", Attributes: map[string]string{"api_key": "key"}},
		{ID: "codex", Provider: "codex", Metadata: map[string]any{"access_token": "token"}},
	}

	discovery.refreshEligibleAuths(context.Background(), auths)
	if len(fetched) != 1 || fetched[0] != "eligible" {
		t.Fatalf("fetched auth IDs = %v, want [eligible]", fetched)
	}

	discovery.forgetAuth("eligible")
	if got, ok := discovery.modelsForAuth(auths[0]); ok || len(got) != 0 {
		t.Fatalf("models after forget = %+v", got)
	}
}

func TestXAIOAuthModelDiscoveryRunRefreshesImmediatelyAndStops(t *testing.T) {
	fetched := make(chan struct{}, 1)
	discovery := newXAIOAuthModelDiscovery(
		func(_ context.Context, _ *coreauth.Auth) ([]*ModelInfo, error) {
			fetched <- struct{}{}
			return []*ModelInfo{{ID: "grok-4.7"}}, nil
		},
		nil,
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		discovery.run(ctx, func() []*coreauth.Auth {
			return []*coreauth.Auth{{ID: "eligible", Provider: "xai", Metadata: map[string]any{"access_token": "token"}}}
		})
		close(done)
	}()

	select {
	case <-fetched:
	case <-time.After(2 * time.Second):
		t.Fatal("initial discovery did not run")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("discovery loop did not stop after cancellation")
	}
}

func TestServiceLifecycleStopCancelsXAIOAuthModelDiscovery(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: "eligible", Provider: "xai", Metadata: map[string]any{"access_token": "token"}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	started := make(chan struct{})
	cancelled := make(chan struct{})
	discovery := newXAIOAuthModelDiscovery(
		func(ctx context.Context, _ *coreauth.Auth) ([]*ModelInfo, error) {
			close(started)
			<-ctx.Done()
			close(cancelled)
			return nil, ctx.Err()
		},
		nil,
	)
	discovery.fetchTimeout = time.Minute
	service := &Service{coreManager: manager, xaiModelDiscovery: discovery}
	service.startXAIOAuthModelDiscovery(context.Background())

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("service discovery did not start")
	}
	service.stopLifecycleBackground()
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("lifecycle stop did not cancel discovery")
	}
	service.xaiModelDiscoveryMu.Lock()
	running := service.xaiModelDiscoveryCancel != nil || service.xaiModelDiscoveryDone != nil
	service.xaiModelDiscoveryMu.Unlock()
	if running {
		t.Fatal("service retained discovery lifecycle state after stop")
	}
}
