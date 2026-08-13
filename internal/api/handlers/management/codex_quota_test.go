package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCodexQuotaUsageProxiesWhitelistedSnapshot(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/backend-api/wham/usage" {
			t.Fatalf("unexpected upstream request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer access-secret" {
			t.Fatalf("missing bearer token")
		}
		if r.Header.Get("Chatgpt-Account-Id") != "acct-1" {
			t.Fatalf("chatgpt account header = %q", r.Header.Get("Chatgpt-Account-Id"))
		}
		if r.Header.Get("OpenAI-Beta") != "codex-1" || r.Header.Get("Originator") != "Codex Desktop" {
			t.Fatalf("missing codex quota headers: %#v", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"user_id":"must-not-leak","email":"secret@example.com","plan_type":"pro",
			"rate_limit":{"allowed":true,"limit_reached":false,
				"primary_window":{"used_percent":28.5,"limit_window_seconds":604800,"reset_after_seconds":601200,"reset_at":1786640400},
				"secondary_window":{"used_percent":16,"limit_window_minutes":300,"reset_after_seconds":14400,"reset_at":1786050000}},
			"rate_limit_reset_credits":{"available_count":9}
		}`))
	}))
	defer upstream.Close()

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: "codex-1", Provider: "codex", Metadata: map[string]any{
		"access_token": "access-secret", "account_id": "acct-1",
	}}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	h := &Handler{authManager: manager, codexQuotaUsageURL: upstream.URL + "/backend-api/wham/usage", codexQuotaHTTPClient: upstream.Client()}
	router := gin.New()
	router.GET("/", h.GetCodexQuotaUsage)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?auth_index="+auth.EnsureIndex(), nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var got codexQuotaUsageResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.PlanType != "pro" || got.FetchedAt.IsZero() || got.FiveHour == nil || got.SevenDay == nil {
		t.Fatalf("unexpected quota response: %+v", got)
	}
	if got.FiveHour.UsedPercent != 16 || got.FiveHour.WindowSeconds != 18000 || got.SevenDay.UsedPercent != 28.5 || got.SevenDay.WindowSeconds != 604800 {
		t.Fatalf("unexpected normalized windows: %+v", got)
	}
	for _, secret := range []string{"must-not-leak", "secret@example.com", "access-secret", "available_count"} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatalf("response leaked %q: %s", secret, rec.Body.String())
		}
	}
}

func TestCodexQuotaUsageResponseKeepsAllWhitelistKeys(t *testing.T) {
	payload, err := json.Marshal(codexQuotaUsageResponse{FetchedAt: time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"plan_type", "fetched_at", "five_hour", "seven_day"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("response is missing whitelist key %q: %s", key, payload)
		}
	}
	if len(got) != 4 {
		t.Fatalf("response has unexpected keys: %s", payload)
	}
}

func TestCodexQuotaUsageUsesFixedRequestSurface(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/backend-api/wham/usage" || r.URL.RawQuery != "" {
			t.Fatalf("unexpected upstream target: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		for _, forbidden := range []string{"X-Client-Injected", "X-Forwarded-Host"} {
			if value := r.Header.Get(forbidden); value != "" {
				t.Fatalf("client-controlled header %s leaked upstream: %q", forbidden, value)
			}
		}
		_, _ = w.Write([]byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":18000},"secondary_window":{"used_percent":2,"limit_window_seconds":604800}}}`))
	}))
	defer upstream.Close()

	auth, manager := newCodexQuotaTestAuth(t, map[string]any{"access_token": "access-secret", "account_id": "acct-1"})
	h := &Handler{authManager: manager, codexQuotaUsageURL: upstream.URL + "/backend-api/wham/usage", codexQuotaHTTPClient: upstream.Client()}
	router := gin.New()
	router.GET("/", h.GetCodexQuotaUsage)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?auth_index="+auth.EnsureIndex()+"&url=https://evil.invalid/private&method=POST", nil)
	req.Header.Set("X-Client-Injected", "must-not-forward")
	req.Header.Set("X-Forwarded-Host", "evil.invalid")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCodexQuotaUsageErrorsDoNotLeakSecretsOrRawBody(t *testing.T) {
	secretToken := "access-token-must-not-leak"
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"` + secretToken + `","private":"raw-upstream-body"}`))
	}))
	defer upstream.Close()

	auth, manager := newCodexQuotaTestAuth(t, map[string]any{"access_token": secretToken, "account_id": "acct-secret"})
	h := &Handler{authManager: manager, codexQuotaUsageURL: upstream.URL + "/backend-api/wham/usage", codexQuotaHTTPClient: upstream.Client()}
	router := gin.New()
	router.GET("/", h.GetCodexQuotaUsage)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?auth_index="+auth.EnsureIndex(), nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
	for _, value := range []string{secretToken, "acct-secret", "raw-upstream-body"} {
		if strings.Contains(rec.Body.String(), value) {
			t.Fatalf("error response leaked %q: %s", value, rec.Body.String())
		}
	}
}

func TestCodexQuotaUsageEnforcesResponseBodyLimit(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":1,"limit_window_seconds":18000}},"padding":"`))
		_, _ = w.Write([]byte(strings.Repeat("x", int(maxCodexQuotaBodyBytes))))
		_, _ = w.Write([]byte(`"}`))
	}))
	defer upstream.Close()

	auth, manager := newCodexQuotaTestAuth(t, map[string]any{"access_token": "access-secret", "account_id": "acct-1"})
	h := &Handler{authManager: manager, codexQuotaUsageURL: upstream.URL + "/backend-api/wham/usage", codexQuotaHTTPClient: upstream.Client()}
	router := gin.New()
	router.GET("/", h.GetCodexQuotaUsage)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?auth_index="+auth.EnsureIndex(), nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "response is invalid") {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCodexQuotaUsageHonorsShortTimeout(t *testing.T) {
	auth, manager := newCodexQuotaTestAuth(t, map[string]any{"access_token": "access-secret", "account_id": "acct-1"})
	var deadlineBudget time.Duration
	h := &Handler{
		authManager: manager,
		codexQuotaHTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			deadline, ok := req.Context().Deadline()
			if !ok {
				t.Fatal("quota request context has no deadline")
			}
			deadlineBudget = time.Until(deadline)
			return nil, context.DeadlineExceeded
		})},
	}
	router := gin.New()
	router.GET("/", h.GetCodexQuotaUsage)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/?auth_index="+auth.EnsureIndex(), nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if deadlineBudget <= 0 || deadlineBudget > codexQuotaUpstreamTimeout+time.Second {
		t.Fatalf("deadline budget = %s, want <= %s", deadlineBudget, codexQuotaUpstreamTimeout)
	}
}

func TestCodexQuotaUsageRejectsUnknownOrNonCodexAuth(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	other := &coreauth.Auth{ID: "xai-1", Provider: "xai", Metadata: map[string]any{"access_token": "secret"}}
	if _, err := manager.Register(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	h := &Handler{authManager: manager}
	router := gin.New()
	router.GET("/", h.GetCodexQuotaUsage)
	for _, raw := range []string{"", "missing", other.EnsureIndex()} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/?auth_index="+raw, nil)
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("auth_index=%q status=%d body=%s", raw, rec.Code, rec.Body.String())
		}
	}
}

func TestNormalizeCodexQuotaWindowsRejectsInvalidValues(t *testing.T) {
	bad := &codexQuotaRateLimit{PrimaryWindow: &codexQuotaRawWindow{UsedPercent: -1, LimitWindowSeconds: 18000}}
	if five, seven := normalizeCodexQuotaWindows(bad); five != nil || seven != nil {
		t.Fatalf("invalid window accepted: five=%+v seven=%+v", five, seven)
	}
}

func TestNormalizeCodexQuotaWindowsSupportsSecondsMinutesAndReversedOrder(t *testing.T) {
	tests := []struct {
		name  string
		limit *codexQuotaRateLimit
	}{
		{
			name: "primary 7d in seconds and secondary 5h in minutes",
			limit: &codexQuotaRateLimit{
				PrimaryWindow:   &codexQuotaRawWindow{UsedPercent: 70, LimitWindowSeconds: 7 * 24 * 60 * 60},
				SecondaryWindow: &codexQuotaRawWindow{UsedPercent: 5, LimitWindowMinutes: 5 * 60},
			},
		},
		{
			name: "primary 5h in minutes and secondary 7d in seconds",
			limit: &codexQuotaRateLimit{
				PrimaryWindow:   &codexQuotaRawWindow{UsedPercent: 5, LimitWindowMinutes: 5 * 60},
				SecondaryWindow: &codexQuotaRawWindow{UsedPercent: 70, LimitWindowSeconds: 7 * 24 * 60 * 60},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			five, seven := normalizeCodexQuotaWindows(tt.limit)
			if five == nil || seven == nil {
				t.Fatalf("missing normalized windows: five=%+v seven=%+v", five, seven)
			}
			if five.WindowSeconds != 5*60*60 || five.UsedPercent != 5 {
				t.Fatalf("five_hour = %+v", five)
			}
			if seven.WindowSeconds != 7*24*60*60 || seven.UsedPercent != 70 {
				t.Fatalf("seven_day = %+v", seven)
			}
		})
	}
}

func TestNormalizeCodexQuotaWindowsDoesNotMislabelTwoShortWindowsAsSevenDay(t *testing.T) {
	limit := &codexQuotaRateLimit{
		PrimaryWindow:   &codexQuotaRawWindow{UsedPercent: 10, LimitWindowSeconds: 60 * 60},
		SecondaryWindow: &codexQuotaRawWindow{UsedPercent: 20, LimitWindowSeconds: 5 * 60 * 60},
	}
	five, seven := normalizeCodexQuotaWindows(limit)
	if five == nil || five.WindowSeconds != 5*60*60 {
		t.Fatalf("five_hour = %+v", five)
	}
	if seven != nil {
		t.Fatalf("unexpected seven_day = %+v", seven)
	}
}

func TestCodexQuotaWindowUsesFetchedAtForResetAfter(t *testing.T) {
	fetchedAt := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	window := normalizeCodexQuotaWindow(&codexQuotaRawWindow{
		UsedPercent:        12.5,
		LimitWindowSeconds: 5 * 60 * 60,
		ResetAfterSeconds:  90,
	}, fetchedAt)
	if window == nil || window.ResetAt == nil {
		t.Fatalf("window = %+v", window)
	}
	if want := fetchedAt.Add(90 * time.Second); !window.ResetAt.Equal(want) {
		t.Fatalf("reset_at = %s, want %s", window.ResetAt, want)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func newCodexQuotaTestAuth(t *testing.T, metadata map[string]any) (*coreauth.Auth, *coreauth.Manager) {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{ID: "codex-" + strconv.FormatInt(time.Now().UnixNano(), 10), Provider: "codex", Metadata: metadata}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	return auth, manager
}
