package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestManagementCodexOverdraftConfigRouteIsRegisteredAndProtected(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")

	server := newTestServer(t)
	unauthorized := httptest.NewRequest(http.MethodGet, "/v0/management/codex-overdraft/config", nil)
	unauthorizedRecorder := httptest.NewRecorder()
	server.engine.ServeHTTP(unauthorizedRecorder, unauthorized)
	if unauthorizedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want %d; body=%s", unauthorizedRecorder.Code, http.StatusUnauthorized, unauthorizedRecorder.Body.String())
	}

	authorized := httptest.NewRequest(http.MethodGet, "/v0/management/codex-overdraft/config", nil)
	authorized.Header.Set("Authorization", "Bearer test-management-key")
	authorizedRecorder := httptest.NewRecorder()
	server.engine.ServeHTTP(authorizedRecorder, authorized)
	if authorizedRecorder.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, want %d; body=%s", authorizedRecorder.Code, http.StatusOK, authorizedRecorder.Body.String())
	}
}
