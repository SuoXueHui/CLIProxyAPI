package cliproxy

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type startupEgressStore struct {
	auths []*coreauth.Auth
}

func (s *startupEgressStore) List(context.Context) ([]*coreauth.Auth, error) {
	return s.auths, nil
}

func (*startupEgressStore) Save(context.Context, *coreauth.Auth) (string, error) {
	return "", nil
}

func (*startupEgressStore) Delete(context.Context, string) error { return nil }

func TestServiceInitializesLoadedAuthEgressBeforeServerStart(t *testing.T) {
	statePath := installFakeIPCommand(t)
	cfg := &config.Config{
		IPv6Egress: egress.Config{Enabled: true, Prefix: "2001:db8::/120", Interface: "eth0"},
	}
	manager := coreauth.NewManager(&startupEgressStore{auths: []*coreauth.Auth{{ID: "loaded", Provider: "unknown", Status: coreauth.StatusActive}}}, nil, nil)
	service := &Service{cfg: cfg, coreManager: manager}
	if errLoad := manager.Load(context.Background()); errLoad != nil {
		t.Fatal(errLoad)
	}
	if errEgress := service.initializeLoadedAuthEgress(context.Background()); errEgress != nil {
		t.Fatal(errEgress)
	}
	auth, _ := manager.GetByID("loaded")
	if auth == nil || auth.EgressIPv6 == "" {
		t.Fatal("loaded auth had no IPv6 egress before server initialization")
	}
	if got := readFakeIPState(t, statePath); len(got) != 1 {
		t.Fatalf("startup IPv6 addresses = %#v, want one adopted address", got)
	}
}

func TestServiceIPv6EgressKeepsAllocatorAcrossIncrementalAuthBatches(t *testing.T) {
	installFakeIPCommand(t)
	cfg := &config.Config{IPv6Egress: egress.Config{Enabled: true, Prefix: "2001:db8::/124", Interface: "eth0"}}
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: cfg, coreManager: manager}

	for _, authID := range []string{"auth-1", "auth-2"} {
		service.handleAuthUpdate(context.Background(), watcher.AuthUpdate{
			Action: watcher.AuthUpdateActionAdd,
			ID:     authID,
			Auth:   &coreauth.Auth{ID: authID, Provider: "unknown", Status: coreauth.StatusActive},
		})
	}
	first, okFirst := manager.GetByID("auth-1")
	second, okSecond := manager.GetByID("auth-2")
	if !okFirst || !okSecond {
		t.Fatalf("runtime auths missing: first=%v second=%v", okFirst, okSecond)
	}
	if first.EgressIPv6 == "" || first.EgressIPv6 == second.EgressIPv6 {
		t.Fatalf("incremental auths received colliding IPv6 addresses: %q and %q", first.EgressIPv6, second.EgressIPv6)
	}
}

func TestServiceIPv6EgressReleasesDeletedAndOldPrefixAddresses(t *testing.T) {
	statePath := installFakeIPCommand(t)
	firstCfg := &config.Config{IPv6Egress: egress.Config{Enabled: true, Prefix: "2001:db8:1::/120", Interface: "eth0"}}
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: firstCfg, coreManager: manager}

	service.handleAuthUpdate(context.Background(), watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionAdd,
		ID:     "deleted",
		Auth:   &coreauth.Auth{ID: "deleted", Provider: "unknown", Status: coreauth.StatusActive},
	})
	service.handleAuthUpdate(context.Background(), watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionDelete,
		ID:     "deleted",
	})
	if got := readFakeIPState(t, statePath); len(got) != 0 {
		t.Fatalf("deleted auth left addresses in namespace: %#v", got)
	}

	service.handleAuthUpdate(context.Background(), watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionAdd,
		ID:     "retained",
		Auth:   &coreauth.Auth{ID: "retained", Provider: "unknown", Status: coreauth.StatusActive},
	})
	commit := service.commitConfigUpdate(&config.Config{IPv6Egress: egress.Config{Enabled: true, Prefix: "2001:db8:2::/120", Interface: "eth0"}})
	if !service.applyConfigRuntime(context.Background(), commit, false) {
		t.Fatal("applyConfigRuntime() = false")
	}
	addresses := readFakeIPState(t, statePath)
	if len(addresses) != 1 || !strings.HasPrefix(addresses[0], "2001:db8:2:") {
		t.Fatalf("prefix change left stale or missing addresses: %#v", addresses)
	}
}

func TestServiceIPv6EgressIgnoresStaleConfigDuringDelayedAssignment(t *testing.T) {
	statePath := installFakeIPCommand(t)
	oldCfg := egress.Config{Enabled: true, Prefix: "2001:db8:1::/120", Interface: "eth0"}
	newCfg := egress.Config{Enabled: true, Prefix: "2001:db8:2::/120", Interface: "eth0"}
	service := &Service{cfg: &config.Config{IPv6Egress: newCfg}, coreManager: coreauth.NewManager(nil, nil, nil)}
	if _, errAssign := service.assignEgressIPv6("seed-auth"); errAssign != nil {
		t.Fatal(errAssign)
	}
	// Simulate a delayed auth path retaining an older service config snapshot.
	// The already activated controller remains authoritative for assignment.
	service.cfgMu.Lock()
	service.cfg = &config.Config{IPv6Egress: oldCfg}
	service.cfgMu.Unlock()
	ip, errAssign := service.assignEgressIPv6("late-auth")
	if errAssign != nil {
		t.Fatal(errAssign)
	}
	if ip == nil || !strings.HasPrefix(ip.String(), "2001:db8:2:") {
		t.Fatalf("delayed assignment used stale config: %v", ip)
	}
	addresses := readFakeIPState(t, statePath)
	if len(addresses) != 2 {
		t.Fatalf("delayed assignment addresses = %#v, want seed and late auth", addresses)
	}
	for _, address := range addresses {
		if !strings.HasPrefix(address, "2001:db8:2:") {
			t.Fatalf("delayed assignment changed active prefix: %#v", addresses)
		}
	}
}

func TestServiceIPv6EgressRejectsStaleCommitBeforePublication(t *testing.T) {
	statePath := installFakeIPCommand(t)
	oldCfg := &config.Config{IPv6Egress: egress.Config{Enabled: true, Prefix: "2001:db8:1::/120", Interface: "eth0"}}
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: oldCfg, coreManager: manager}
	service.handleAuthUpdate(context.Background(), watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionAdd,
		ID:     "auth",
		Auth:   &coreauth.Auth{ID: "auth", Provider: "unknown", Status: coreauth.StatusActive},
	})
	before, _ := manager.GetByID("auth")

	service.configUpdateMu.Lock()
	service.configSequence = 2
	service.configUpdateMu.Unlock()
	stale := configCommit{
		cfg:      &config.Config{IPv6Egress: egress.Config{Enabled: true, Prefix: "2001:db8:2::/120", Interface: "eth0"}},
		sequence: 1,
	}
	if errTransition := service.transitionEgressCommit(context.Background(), stale); errTransition == nil || !strings.Contains(errTransition.Error(), "stale") {
		t.Fatalf("transitionEgressCommit() error = %v, want stale commit rejection", errTransition)
	}
	after, _ := manager.GetByID("auth")
	if before == nil || after == nil || after.EgressIPv6 != before.EgressIPv6 {
		t.Fatalf("stale commit changed runtime binding: before=%#v after=%#v", before, after)
	}
	addresses := readFakeIPState(t, statePath)
	if len(addresses) != 1 || !strings.HasPrefix(addresses[0], "2001:db8:1:") {
		t.Fatalf("stale commit changed active addresses: %#v", addresses)
	}
}

func TestServiceIPv6EgressCandidateFailureRollsBackConfigAndBindings(t *testing.T) {
	statePath := installFakeIPCommand(t)
	oldConfig := &config.Config{IPv6Egress: egress.Config{Enabled: true, Prefix: "2001:db8:1::/120", Interface: "eth0"}}
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: oldConfig, coreManager: manager}
	for _, authID := range []string{"auth-a", "auth-b"} {
		service.handleAuthUpdate(context.Background(), watcher.AuthUpdate{
			Action: watcher.AuthUpdateActionAdd,
			ID:     authID,
			Auth:   &coreauth.Auth{ID: authID, Provider: "unknown", Status: coreauth.StatusActive},
		})
	}
	beforeBindings := map[string]string{}
	for _, auth := range manager.List() {
		beforeBindings[auth.ID] = auth.EgressIPv6
	}
	beforeAddresses := readFakeIPState(t, statePath)

	newConfig := &config.Config{IPv6Egress: egress.Config{
		Enabled: true,
		Mode:    egress.ModeManual,
		Prefix:  oldConfig.IPv6Egress.Prefix,
		Manual:  map[string]string{"auth-a": beforeBindings["auth-a"]},
	}}
	commit := service.commitConfigUpdate(newConfig)
	if service.applyConfigRuntime(context.Background(), commit, false) {
		t.Fatal("applyConfigRuntime() = true, want candidate failure")
	}
	service.cfgMu.RLock()
	activeConfig := service.cfg
	service.cfgMu.RUnlock()
	if activeConfig != oldConfig {
		t.Fatalf("active config = %#v, want previous config restored", activeConfig.IPv6Egress)
	}
	for _, auth := range manager.List() {
		if auth.EgressIPv6 != beforeBindings[auth.ID] {
			t.Fatalf("auth %q binding = %q, want %q", auth.ID, auth.EgressIPv6, beforeBindings[auth.ID])
		}
	}
	if got := readFakeIPState(t, statePath); strings.Join(got, ",") != strings.Join(beforeAddresses, ",") {
		t.Fatalf("addresses after failed candidate = %#v, want %#v", got, beforeAddresses)
	}
}

func TestServiceConfigRuntimeDisablesIPv6EgressWithoutAuthUpdate(t *testing.T) {
	statePath := installFakeIPCommand(t)
	firstCfg := &config.Config{IPv6Egress: egress.Config{Enabled: true, Prefix: "2001:db8::/120", Interface: "eth0"}}
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: firstCfg, coreManager: manager}
	assigned, err := service.assignEgressIPv6("auth")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Register(context.Background(), &coreauth.Auth{
		ID: "auth", Provider: "unknown", Status: coreauth.StatusActive, EgressIPv6: assigned.String(),
	}); err != nil {
		t.Fatal(err)
	}
	commit := service.commitConfigUpdate(&config.Config{})
	if !service.applyConfigRuntime(context.Background(), commit, false) {
		t.Fatal("applyConfigRuntime() = false")
	}
	if got := readFakeIPState(t, statePath); len(got) != 0 {
		t.Fatalf("disabled config left process-owned addresses: %#v", got)
	}
	updated, ok := manager.GetByID("auth")
	if !ok || updated.EgressIPv6 != "" {
		t.Fatalf("disabled config retained runtime egress IPv6: %#v", updated)
	}
}

func TestServiceDisabledIPv6EgressClearsStaleIncrementalField(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: &config.Config{}, coreManager: manager}
	service.handleAuthUpdate(context.Background(), watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionAdd,
		ID:     "auth",
		Auth: &coreauth.Auth{
			ID: "auth", Provider: "unknown", Status: coreauth.StatusActive, EgressIPv6: "2001:db8::42",
		},
	})
	updated, ok := manager.GetByID("auth")
	if !ok || updated.EgressIPv6 != "" {
		t.Fatalf("disabled config accepted stale runtime egress IPv6: %#v", updated)
	}
}

func installFakeIPCommand(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	statePath := filepath.Join(dir, "addresses")
	script := `#!/bin/sh
state="$IP_STATE_FILE"
case "$*" in
  *"addr show"*)
    if [ -f "$state" ]; then
      while IFS= read -r address; do
        [ -n "$address" ] && printf '2: eth0 inet6 %s scope global nodad\n' "$address"
      done < "$state"
    fi
    ;;
  *"addr replace"*|*"addr add"*)
    address="$4"
    touch "$state"
    if ! grep -qxF "$address" "$state"; then printf '%s\n' "$address" >> "$state"; fi
    ;;
  *"addr del"*)
    address="$4"
    if [ -f "$state" ]; then
      grep -vxF "$address" "$state" > "$state.tmp" || true
      mv "$state.tmp" "$state"
    fi
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "ip"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("IP_STATE_FILE", statePath)
	return statePath
}

func readFakeIPState(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Fields(string(data))
	for index, address := range lines {
		ip, _, errCIDR := net.ParseCIDR(address)
		if errCIDR != nil {
			t.Fatalf("invalid fake IP state %q: %v", address, errCIDR)
		}
		lines[index] = ip.String()
	}
	return lines
}
