package management

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestBuildAuthFileEntryIncludesExplicitXAIPlanEvidence(t *testing.T) {
	t.Parallel()

	auth := testXAIAuthFile(t, map[string]any{
		"subscription_tier": " SuperGrok Heavy ",
		"access_token":      testXAIJWT(t, map[string]any{"tier": 0}),
	})

	entry := (&Handler{}).buildAuthFileEntry(auth)
	if got := entry["xai_plan_type"]; got != "supergrok_heavy" {
		t.Fatalf("xai_plan_type = %#v, want supergrok_heavy", got)
	}
	if got := entry["xai_plan_source"]; got != "explicit_metadata" {
		t.Fatalf("xai_plan_source = %#v, want explicit_metadata", got)
	}
}

func TestListAuthFilesIncludesXAIPlanEvidence(t *testing.T) {
	t.Parallel()

	authManager := coreauth.NewManager(nil, nil, nil)
	auth := testXAIAuthFile(t, map[string]any{
		"access_token": testXAIJWT(t, map[string]any{"tier": 5}),
	})
	if _, err := authManager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register xAI auth: %v", err)
	}

	h := &Handler{authManager: authManager}
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	h.ListAuthFiles(ctx)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	var body struct {
		Files []map[string]any `json:"files"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode auth-files response: %v", err)
	}
	if len(body.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(body.Files))
	}
	if got := body.Files[0]["xai_plan_type"]; got != "supergrok_heavy" {
		t.Fatalf("xai_plan_type = %#v, want supergrok_heavy", got)
	}
	if got := body.Files[0]["xai_plan_source"]; got != "access_token_tier" {
		t.Fatalf("xai_plan_source = %#v, want access_token_tier", got)
	}
}

func TestBuildAuthFileEntryMapsXAIAccessTokenTier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		claim any
		want  string
	}{
		{name: "free number", claim: 0, want: "free"},
		{name: "supergrok string", claim: "1", want: "supergrok"},
		{name: "heavy number", claim: 5, want: "supergrok_heavy"},
		{name: "heavy name", claim: "SuperGrok Heavy", want: "supergrok_heavy"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			auth := testXAIAuthFile(t, map[string]any{
				"access_token": testXAIJWT(t, map[string]any{"tier": test.claim}),
			})
			entry := (&Handler{}).buildAuthFileEntry(auth)

			if got := entry["xai_plan_type"]; got != test.want {
				t.Fatalf("xai_plan_type = %#v, want %q", got, test.want)
			}
			if got := entry["xai_plan_source"]; got != "access_token_tier" {
				t.Fatalf("xai_plan_source = %#v, want access_token_tier", got)
			}
		})
	}
}

func TestBuildAuthFileEntryOmitsXAIPlanWithoutKnownEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		metadata map[string]any
	}{
		{name: "missing tier", metadata: map[string]any{"monthlyLimit": 0}},
		{name: "malformed token", metadata: map[string]any{"access_token": "not-a-jwt", "monthly_limit": 0}},
		{name: "unsupported tier", metadata: map[string]any{"access_token": testXAIJWT(t, map[string]any{"tier": 6})}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			entry := (&Handler{}).buildAuthFileEntry(testXAIAuthFile(t, test.metadata))
			if got, ok := entry["xai_plan_type"]; ok {
				t.Fatalf("xai_plan_type = %#v, want omitted", got)
			}
			if got, ok := entry["xai_plan_source"]; ok {
				t.Fatalf("xai_plan_source = %#v, want omitted", got)
			}
		})
	}
}

func TestBuildAuthFileEntryKeepsCodexPlanClaimsUnchanged(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "codex-test.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write Codex auth fixture: %v", err)
	}
	idToken := testXAIJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": "acct-test",
			"chatgpt_plan_type":  "team",
		},
	})
	auth := &coreauth.Auth{
		ID:       "codex-test.json",
		Provider: "codex",
		FileName: "codex-test.json",
		Metadata: map[string]any{"id_token": idToken},
		Attributes: map[string]string{
			"path": path,
		},
	}

	entry := (&Handler{}).buildAuthFileEntry(auth)
	claims, ok := entry["id_token"].(gin.H)
	if !ok {
		t.Fatalf("id_token claims = %#v, want map", entry["id_token"])
	}
	if got := claims["plan_type"]; got != "team" {
		t.Fatalf("Codex plan_type = %#v, want team", got)
	}
	if _, ok := entry["xai_plan_type"]; ok {
		t.Fatalf("Codex entry unexpectedly contains xai_plan_type: %#v", entry)
	}
}

func TestBuildAuthFileEntryDoesNotExposeXAITokens(t *testing.T) {
	t.Parallel()

	auth := testXAIAuthFile(t, map[string]any{
		"access_token":  testXAIJWT(t, map[string]any{"tier": 5}),
		"refresh_token": "fixture-refresh-marker",
		"id_token":      "fixture-id-marker",
	})
	entry := (&Handler{}).buildAuthFileEntry(auth)
	for _, key := range []string{"access_token", "refresh_token", "id_token"} {
		if got, ok := entry[key]; ok {
			t.Fatalf("auth file entry exposed field %q = %#v", key, got)
		}
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal auth file entry: %v", err)
	}
	for _, secret := range []string{"fixture-refresh-marker", "fixture-id-marker"} {
		if response := string(raw); strings.Contains(response, secret) {
			t.Fatalf("auth file entry exposed %q: %s", secret, response)
		}
	}
}

func testXAIAuthFile(t *testing.T, metadata map[string]any) *coreauth.Auth {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "xai-test.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write xAI auth fixture: %v", err)
	}
	return &coreauth.Auth{
		ID:       "xai-test.json",
		Provider: "xai",
		FileName: "xai-test.json",
		Metadata: metadata,
		Attributes: map[string]string{
			"path": path,
		},
	}
}

func testXAIJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal xAI JWT claims: %v", err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
