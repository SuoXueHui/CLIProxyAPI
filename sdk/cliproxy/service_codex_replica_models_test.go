package cliproxy

import (
	"context"
	"strconv"
	"testing"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestRegisterLoadedAuthModelsBindsEveryCodexReplica(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	modelRegistry := internalregistry.GetGlobalRegistry()
	for index := 1; index <= 6; index++ {
		authID := "codex.json::replica:" + strconv.Itoa(index)
		auth := &coreauth.Auth{
			ID:       authID,
			Provider: "codex",
			Status:   coreauth.StatusActive,
			Metadata: map[string]any{"access_token": "test"},
			Attributes: map[string]string{
				coreauth.AttributeCodexReplicaGroup:       "codex.json",
				coreauth.AttributeCodexReplicaIndex:       strconv.Itoa(index),
				coreauth.AttributeCodexReplicaCount:       "6",
				coreauth.AttributeCodexReplicaConcurrency: "10",
			},
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register replica %d: %v", index, errRegister)
		}
		modelRegistry.UnregisterClient(authID)
		t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })
	}

	service := &Service{cfg: &config.Config{}, coreManager: manager}
	service.registerLoadedAuthModels(context.Background())
	for index := 1; index <= 6; index++ {
		authID := "codex.json::replica:" + strconv.Itoa(index)
		if models := modelRegistry.GetModelsForClient(authID); len(models) == 0 {
			t.Fatalf("replica %d registered no models", index)
		}
	}
}

func TestSyncPersistedCodexReplicaTopologyEnablesResizesAndDisablesGroup(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	physical := &coreauth.Auth{
		ID:       "codex.json",
		FileName: "codex.json",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"access_token": "test",
			coreauth.CodexReplicaMetadataKey: map[string]any{
				"enabled": true, "count": 3, "concurrency": 4,
			},
		},
		Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth},
	}
	if _, errRegister := manager.Register(context.Background(), physical); errRegister != nil {
		t.Fatalf("register physical auth: %v", errRegister)
	}
	service := &Service{cfg: &config.Config{}, coreManager: manager}

	managed, errSync := service.syncPersistedCodexReplicaTopology(context.Background(), physical)
	if errSync != nil || !managed {
		t.Fatalf("enable topology = (%v, %v)", managed, errSync)
	}
	assertCodexReplicaTopology(t, manager, 3, "4")

	leader, _ := manager.GetByID("codex.json::replica:1")
	for resultIndex := 0; resultIndex < 7; resultIndex++ {
		manager.MarkResult(context.Background(), coreauth.Result{
			AuthID: "codex.json::replica:1", Provider: "codex", Success: true,
		})
	}
	leader, _ = manager.GetByID("codex.json::replica:1")
	leader.Metadata[coreauth.CodexReplicaMetadataKey] = map[string]any{
		"enabled": true, "count": 2, "concurrency": 9,
	}
	if _, errUpdate := manager.Update(context.Background(), leader); errUpdate != nil {
		t.Fatalf("update leader: %v", errUpdate)
	}
	storedLeader, _ := manager.GetByID("codex.json::replica:1")
	if storedLeader.Success != 7 {
		t.Fatalf("stored leader success before resize = %d, want 7", storedLeader.Success)
	}
	managed, errSync = service.syncPersistedCodexReplicaTopology(context.Background(), leader)
	if errSync != nil || !managed {
		t.Fatalf("resize topology = (%v, %v)", managed, errSync)
	}
	assertCodexReplicaTopology(t, manager, 2, "9")
	resizedLeader, _ := manager.GetByID("codex.json::replica:1")
	if resizedLeader.Success != 7 {
		t.Fatalf("resized leader success = %d, want 7", resizedLeader.Success)
	}

	resizedLeader.Metadata[coreauth.CodexReplicaMetadataKey] = map[string]any{
		"enabled": false, "count": 2, "concurrency": 9,
	}
	if _, errUpdate := manager.Update(context.Background(), resizedLeader); errUpdate != nil {
		t.Fatalf("disable leader: %v", errUpdate)
	}
	managed, errSync = service.syncPersistedCodexReplicaTopology(context.Background(), resizedLeader)
	if errSync != nil || !managed {
		t.Fatalf("disable topology = (%v, %v)", managed, errSync)
	}
	auths := manager.List()
	if len(auths) != 1 || auths[0].ID != "codex.json" {
		t.Fatalf("disabled topology auths = %#v, want one physical auth", auths)
	}
}

func assertCodexReplicaTopology(t *testing.T, manager *coreauth.Manager, count int, concurrency string) {
	t.Helper()
	auths := manager.List()
	if len(auths) != count {
		t.Fatalf("auth count = %d, want %d", len(auths), count)
	}
	for index := 1; index <= count; index++ {
		auth, ok := manager.GetByID("codex.json::replica:" + strconv.Itoa(index))
		if !ok || auth == nil {
			t.Fatalf("replica %d missing", index)
		}
		if auth.Attributes[coreauth.AttributeCodexReplicaConcurrency] != concurrency {
			t.Fatalf("replica %d concurrency = %q", index, auth.Attributes[coreauth.AttributeCodexReplicaConcurrency])
		}
	}
}
