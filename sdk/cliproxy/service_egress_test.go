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
	service.cfgMu.Lock()
	service.cfg = &config.Config{IPv6Egress: egress.Config{Enabled: true, Prefix: "2001:db8:2::/120", Interface: "eth0"}}
	service.cfgMu.Unlock()
	service.handleAuthUpdate(context.Background(), watcher.AuthUpdate{
		Action: watcher.AuthUpdateActionModify,
		ID:     "retained",
		Auth:   &coreauth.Auth{ID: "retained", Provider: "unknown", Status: coreauth.StatusActive},
	})
	addresses := readFakeIPState(t, statePath)
	if len(addresses) != 1 || !strings.HasPrefix(addresses[0], "2001:db8:2:") {
		t.Fatalf("prefix change left stale or missing addresses: %#v", addresses)
	}
}

func TestServiceConfigRuntimeDisablesIPv6EgressWithoutAuthUpdate(t *testing.T) {
	statePath := installFakeIPCommand(t)
	firstCfg := &config.Config{IPv6Egress: egress.Config{Enabled: true, Prefix: "2001:db8::/120", Interface: "eth0"}}
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: firstCfg, coreManager: manager}
	assigned, err := service.assignEgressIPv6(firstCfg.IPv6Egress, "auth")
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
