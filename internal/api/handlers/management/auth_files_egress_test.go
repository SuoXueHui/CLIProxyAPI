package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestListAuthFilesIncludesRuntimeEgressIPv6(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "codex-user@example.com.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:         fileName,
		FileName:   fileName,
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		EgressIPv6: "2610:150:805f:f80e:100::42",
		Attributes: map[string]string{"path": filePath},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	h.ListAuthFiles(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	var payload struct {
		Files []map[string]any `json:"files"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("failed to decode list payload: %v", errDecode)
	}
	if len(payload.Files) != 1 {
		t.Fatalf("expected 1 auth entry, got %d", len(payload.Files))
	}
	if got := payload.Files[0]["egress_ipv6"]; got != record.EgressIPv6 {
		t.Fatalf("expected egress_ipv6 %q, got %#v", record.EgressIPv6, got)
	}
}

func TestListAuthFilesOmitsEmptyRuntimeEgressIPv6(t *testing.T) {
	auth := &coreauth.Auth{
		ID:         "auth-without-egress",
		FileName:   "auth-without-egress.json",
		Provider:   "codex",
		Attributes: map[string]string{"runtime_only": "true"},
	}

	entry := (&Handler{}).buildAuthFileEntry(auth)
	if _, ok := entry["egress_ipv6"]; ok {
		t.Fatalf("expected empty egress_ipv6 to be omitted, entry: %#v", entry)
	}
}
