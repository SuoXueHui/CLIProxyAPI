package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type fileStoreRoundTripperFunc func(*http.Request) (*http.Response, error)

var _ cliproxyauth.CredentialWriteGate = (*FileTokenStore)(nil)

func (f fileStoreRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestFileTokenStoreCredentialWritesDefaultToEnabledAndCanToggle(t *testing.T) {
	store := NewFileTokenStore()
	if !store.WritesEnabled() {
		t.Fatal("WritesEnabled() = false, want backward-compatible true default")
	}
	store.SetWritesEnabled(false)
	if store.WritesEnabled() {
		t.Fatal("WritesEnabled() = true after disabling writes")
	}
}

func TestFileTokenStoreReadOnlyRejectsSaveAndDelete(t *testing.T) {
	baseDir := t.TempDir()
	deletePath := filepath.Join(baseDir, "delete-me.json")
	if errWrite := os.WriteFile(deletePath, []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
		t.Fatalf("seed delete file: %v", errWrite)
	}
	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	store.SetWritesEnabled(false)

	auth := &cliproxyauth.Auth{
		ID:       "save-me.json",
		FileName: "save-me.json",
		Metadata: map[string]any{"type": "codex"},
	}
	if _, errSave := store.Save(context.Background(), auth); !errors.Is(errSave, ErrCredentialWritesDisabled) {
		t.Fatalf("Save() error = %v, want read-only rejection", errSave)
	}
	if _, errStat := os.Stat(filepath.Join(baseDir, auth.FileName)); !os.IsNotExist(errStat) {
		t.Fatalf("Save() created a credential file in read-only mode: %v", errStat)
	}

	if errDelete := store.Delete(context.Background(), filepath.Base(deletePath)); !errors.Is(errDelete, ErrCredentialWritesDisabled) {
		t.Fatalf("Delete() error = %v, want read-only rejection", errDelete)
	}
	if _, errStat := os.Stat(deletePath); errStat != nil {
		t.Fatalf("Delete() removed the credential file in read-only mode: %v", errStat)
	}
}

func TestFileTokenStoreReadOnlyListSkipsAntigravityProjectRepair(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "antigravity.json")
	original := []byte(`{"type":"antigravity","access_token":"access-token"}`)
	if errWrite := os.WriteFile(path, original, 0o600); errWrite != nil {
		t.Fatalf("seed auth file: %v", errWrite)
	}

	var requests atomic.Int32
	originalTransport := http.DefaultClient.Transport
	http.DefaultClient.Transport = fileStoreRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"cloudaicompanionProject":"repaired-project"}`)),
		}, nil
	})
	t.Cleanup(func() { http.DefaultClient.Transport = originalTransport })

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	store.SetWritesEnabled(false)
	auths, errList := store.List(context.Background())
	if errList != nil {
		t.Fatalf("List() error = %v", errList)
	}
	if len(auths) != 1 {
		t.Fatalf("List() len = %d, want 1", len(auths))
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("Antigravity project discovery requests = %d, want 0 in read-only mode", got)
	}
	if got := strings.TrimSpace(stringValue(auths[0].Metadata["project_id"])); got != "" {
		t.Fatalf("listed project_id = %q, want no load-time repair", got)
	}
	persisted, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read auth file: %v", errRead)
	}
	if string(persisted) != string(original) {
		t.Fatalf("auth file changed in read-only mode: got %s want %s", persisted, original)
	}
}

func stringValue(value any) string {
	valueString, _ := value.(string)
	return valueString
}
