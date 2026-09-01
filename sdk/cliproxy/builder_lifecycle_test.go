package cliproxy

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/lifecycle"
	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func lifecycleTestBuilder(t *testing.T) *Builder {
	t.Helper()
	dir := t.TempDir()
	if gate, ok := sdkAuth.GetTokenStore().(interface{ SetWritesEnabled(bool) }); ok {
		t.Cleanup(func() { gate.SetWritesEnabled(true) })
	}
	return NewBuilder().WithConfig(&config.Config{AuthDir: dir}).WithConfigPath(filepath.Join(dir, "config.yaml"))
}

func TestBuilderLifecycleControlIsOptIn(t *testing.T) {
	oldMode, hadMode := os.LookupEnv("CLIPROXY_LIFECYCLE_MODE")
	if errUnset := os.Unsetenv("CLIPROXY_LIFECYCLE_MODE"); errUnset != nil {
		t.Fatal(errUnset)
	}
	t.Cleanup(func() {
		if hadMode {
			_ = os.Setenv("CLIPROXY_LIFECYCLE_MODE", oldMode)
		} else {
			_ = os.Unsetenv("CLIPROXY_LIFECYCLE_MODE")
		}
	})
	service, errBuild := lifecycleTestBuilder(t).Build()
	if errBuild != nil {
		t.Fatalf("Build() error = %v", errBuild)
	}
	if service.lifecycleController != nil {
		t.Fatal("lifecycle controller enabled without environment opt-in")
	}
}

func TestBuilderStandbyDoesNotAcquireWriterLease(t *testing.T) {
	t.Setenv("CLIPROXY_LIFECYCLE_MODE", string(lifecycle.ModeStandby))
	t.Setenv("CLIPROXY_WRITER_LOCK_PATH", filepath.Join(t.TempDir(), "writer.lock"))
	service, errBuild := lifecycleTestBuilder(t).Build()
	if errBuild != nil {
		t.Fatalf("Build() error = %v", errBuild)
	}
	if got := service.lifecycleController.Status().Mode; got != lifecycle.ModeStandby {
		t.Fatalf("initial mode = %s", got)
	}
	if service.writerLease != nil {
		t.Fatal("standby acquired writer lease")
	}
}

func TestBuilderStandbyLoadsCredentialsBeforeServingReadOnly(t *testing.T) {
	authDir := t.TempDir()
	if gate, ok := sdkAuth.GetTokenStore().(interface{ SetWritesEnabled(bool) }); ok {
		t.Cleanup(func() { gate.SetWritesEnabled(true) })
	}
	if errWrite := os.WriteFile(filepath.Join(authDir, "codex.json"), []byte(`{"type":"codex","email":"test@example.com"}`), 0o600); errWrite != nil {
		t.Fatalf("write auth file: %v", errWrite)
	}
	t.Setenv("CLIPROXY_LIFECYCLE_MODE", string(lifecycle.ModeStandby))
	t.Setenv("CLIPROXY_WRITER_LOCK_PATH", filepath.Join(t.TempDir(), "writer.lock"))
	service, errBuild := NewBuilder().WithConfig(&config.Config{
		AuthDir:    authDir,
		IPv6Egress: egress.Config{Enabled: true, Prefix: "2001:db8::/120", Interface: "eth0"},
		CodexKey: []config.CodexKey{{
			APIKey: "configured-key",
		}},
	}).WithConfigPath(filepath.Join(authDir, "config.yaml")).Build()
	if errBuild != nil {
		t.Fatalf("Build() error = %v", errBuild)
	}
	if got := len(service.coreManager.List()); got != 0 {
		t.Fatalf("standby auth count before transition = %d, want 0", got)
	}
	status, errTransition := service.lifecycleController.Transition(context.Background(), lifecycle.ModeServingReadOnly, 0)
	if errTransition != nil {
		t.Fatalf("standby -> serving-readonly error = %v", errTransition)
	}
	if status.Mode != lifecycle.ModeServingReadOnly || !status.AcceptingNew || status.CredentialWriter || status.IPv6Enabled {
		t.Fatalf("serving-readonly status = %+v", status)
	}
	auths := service.coreManager.List()
	if got := len(auths); got != 2 {
		t.Fatalf("serving-readonly auth count = %d, want file and config auth", got)
	}
	modelRegistry := internalregistry.GetGlobalRegistry()
	configAuthFound := false
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		authID := auth.ID
		t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })
		if models := modelRegistry.GetModelsForClient(authID); len(models) == 0 {
			t.Fatalf("serving-readonly did not register models for auth %q", authID)
		}
		configAuthFound = configAuthFound || coreauth.IsConfigAPIKeyAuth(auth)
	}
	if !configAuthFound {
		t.Fatal("serving-readonly did not restore config API-key auth")
	}
	if _, ok := service.coreManager.Executor("codex"); !ok {
		t.Fatal("serving-readonly did not register the loaded auth executor")
	}
}

func TestBuilderActiveRequiresExclusiveWriterLease(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("writer lease is Unix-only")
	}
	lockPath := filepath.Join(t.TempDir(), "writer.lock")
	t.Setenv("CLIPROXY_LIFECYCLE_MODE", string(lifecycle.ModeActive))
	t.Setenv("CLIPROXY_WRITER_LOCK_PATH", lockPath)
	first, errBuild := lifecycleTestBuilder(t).Build()
	if errBuild != nil {
		t.Fatalf("first Build() error = %v", errBuild)
	}
	defer first.lifecycleCleanup()
	if first.writerLease == nil {
		t.Fatal("active did not acquire writer lease")
	}
	if _, errSecond := lifecycleTestBuilder(t).Build(); errSecond == nil {
		t.Fatal("second active Build() acquired the same writer lease")
	}
}
