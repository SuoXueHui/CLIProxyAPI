package cliproxy

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/lifecycle"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
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
