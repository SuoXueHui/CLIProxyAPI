package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
)

func TestGetCodexWeeklyOverdraftStatus(t *testing.T) {
	overdraft := config.DefaultCodexWeeklyOverdraftConfig()
	overdraft.Enabled = true
	overdraft.Mode = config.CodexWeeklyOverdraftModeInject
	overdraft.CanaryPercent = 25
	overdraft.PairCount = 2
	body := []byte(`{"input":[{"type":"message","role":"user","content":"hello"}]}`)
	trackingConfig := overdraft
	trackingConfig.CanaryPercent = 100
	_, decision := helps.ApplyCodexWeeklyOverdraft(helps.CodexWeeklyOverdraftRequest{
		Config: trackingConfig, AuthID: "management-account", SessionID: "management-session", OAuth: true, Body: body,
	})
	helps.RecordCodexWeeklyOverdraftOutcome(decision, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{Codex: config.CodexConfig{WeeklyOverdraft: overdraft}}, nil)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/codex-weekly-overdraft?auth-id=management-account", nil)
	h.GetCodexWeeklyOverdraftStatus(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var payload struct {
		Config config.CodexWeeklyOverdraftConfig `json:"config"`
		Status struct {
			Evaluated               uint64                                    `json:"evaluated"`
			Skipped                 map[string]uint64                         `json:"skipped"`
			Outcomes                map[string]uint64                         `json:"outcomes"`
			AccountRetentionSeconds int64                                     `json:"account-retention-seconds"`
			Accounts                []helps.CodexWeeklyOverdraftAccountStatus `json:"accounts"`
		} `json:"status"`
	}
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if !payload.Config.Enabled || payload.Config.Mode != config.CodexWeeklyOverdraftModeInject || payload.Config.CanaryPercent != 25 || payload.Config.PairCount != 2 {
		t.Fatalf("config = %#v", payload.Config)
	}
	if payload.Status.Skipped == nil || payload.Status.Outcomes == nil {
		t.Fatalf("status = %#v", payload.Status)
	}
	if payload.Status.AccountRetentionSeconds != 6*60*60 || len(payload.Status.Accounts) != 1 || payload.Status.Accounts[0].AuthID != "management-account" {
		t.Fatalf("account status = %#v", payload.Status)
	}

	responseBody := recorder.Body.String()
	for _, forbidden := range []string{`"session-id"`, `"request-body"`, `"token"`, `"account-label"`} {
		if strings.Contains(responseBody, forbidden) {
			t.Fatalf("response leaked forbidden field %s: %s", forbidden, responseBody)
		}
	}
}
