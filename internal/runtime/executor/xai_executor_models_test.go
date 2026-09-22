package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	xaiauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/xai"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestXAIExecutorListModels(t *testing.T) {
	var gotAuthorization string
	var gotTokenAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %s, want /v1/models", r.URL.Path)
		}
		gotAuthorization = r.Header.Get("Authorization")
		gotTokenAuth = r.Header.Get(xaiTokenAuthHeader)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"grok-4.7","object":"model","created":1790000000,"owned_by":"xai"},{"id":"grok-4.7-build-fast","object":"model","owned_by":"xai"},{"id":"   ","object":"model"}]}`))
	}))
	t.Cleanup(server.Close)

	auth := &cliproxyauth.Auth{
		Provider: "xai",
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"base_url":  server.URL + "/v1",
		},
		Metadata: map[string]any{"access_token": "oauth-token"},
	}

	models, err := NewXAIExecutor(&config.Config{}).ListModels(context.Background(), auth)
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	if gotAuthorization != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q, want Bearer oauth-token", gotAuthorization)
	}
	if gotTokenAuth != "" {
		t.Fatalf("%s = %q, want empty for custom endpoint", xaiTokenAuthHeader, gotTokenAuth)
	}
	if len(models) != 2 {
		t.Fatalf("model count = %d, want 2", len(models))
	}
	if models[0].ID != "grok-4.7" || models[0].Object != "model" || models[0].OwnedBy != "xai" || models[0].Type != "xai" {
		t.Fatalf("first model = %+v", models[0])
	}
	if models[0].Created != 1790000000 {
		t.Fatalf("first model created = %d, want 1790000000", models[0].Created)
	}
	if models[1].ID != "grok-4.7-build-fast" || models[1].Name != "grok-4.7-build-fast" {
		t.Fatalf("second model = %+v", models[1])
	}
}

func TestApplyXAIModelDiscoveryHeaders(t *testing.T) {
	t.Run("cli proxy receives session identity headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, xaiauth.CLIChatProxyBaseURL+"/models", nil)
		auth := &cliproxyauth.Auth{
			Provider: "xai",
			Attributes: map[string]string{
				"auth_kind": "oauth",
				"base_url":  xaiauth.DefaultAPIBaseURL,
			},
			Metadata: map[string]any{
				"access_token": "oauth-token",
				"sub":          "user-123",
				"email":        "member@example.com",
			},
		}

		applyXAIModelDiscoveryHeaders(req, auth)

		want := map[string]string{
			"Authorization":           "Bearer oauth-token",
			xaiTokenAuthHeader:        xaiTokenAuthValue,
			xaiClientVersionHeader:    xaiClientVersionValue,
			xaiClientIdentifierHeader: xaiClientIdentifierValue,
			"x-grok-client-mode":      "headless",
			"x-userid":                "user-123",
			"x-email":                 "member@example.com",
		}
		for header, value := range want {
			if got := req.Header.Get(header); got != value {
				t.Fatalf("%s = %q, want %q", header, got, value)
			}
		}
	})

	t.Run("custom endpoint receives no cli session headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "https://gateway.example.com/v1/models", nil)
		auth := &cliproxyauth.Auth{
			Provider: "xai",
			Attributes: map[string]string{
				"auth_kind": "oauth",
				"base_url":  "https://gateway.example.com/v1",
			},
			Metadata: map[string]any{
				"access_token": "oauth-token",
				"sub":          "user-123",
				"email":        "member@example.com",
			},
		}

		applyXAIModelDiscoveryHeaders(req, auth)

		for _, header := range []string{xaiTokenAuthHeader, xaiClientVersionHeader, xaiClientIdentifierHeader, "x-grok-client-mode", "x-userid", "x-email"} {
			if got := req.Header.Get(header); got != "" {
				t.Fatalf("%s = %q, want empty for custom endpoint", header, got)
			}
		}
	})
}

func TestXAIExecutorListModelsRejectsInvalidResponses(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{name: "upstream error", statusCode: http.StatusUnauthorized, body: `{"error":"unauthorized"}`},
		{name: "malformed json", statusCode: http.StatusOK, body: `{"data":`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.statusCode)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)

			auth := &cliproxyauth.Auth{
				Provider: "xai",
				Attributes: map[string]string{
					"auth_kind": "oauth",
					"base_url":  server.URL + "/v1",
				},
				Metadata: map[string]any{"access_token": "oauth-token"},
			}

			if _, err := NewXAIExecutor(&config.Config{}).ListModels(context.Background(), auth); err == nil {
				t.Fatal("ListModels() error = nil, want error")
			}
		})
	}
}
