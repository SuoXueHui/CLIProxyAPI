package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func codexOverdraftConfigTestBody() string {
	return `{
  "enabled": true,
  "mode": "inject",
  "canary_percent": 25,
  "pair_count": 2,
  "tail_policy": "user-and-tool-output",
  "oauth_only": true,
  "max_body_bytes": 8388608,
  "gate_mode": "header-or-429",
  "quota_threshold_percent": 95,
  "account_device_identity": "account_device"
}`
}

func newManagementJSONContext(method, path, body string) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	return ctx, recorder
}

func TestGetCodexOverdraftConfigReturnsDataPlaneControls(t *testing.T) {
	overdraft := config.DefaultCodexWeeklyOverdraftConfig()
	overdraft.Enabled = true
	overdraft.Mode = config.CodexWeeklyOverdraftModeInject
	overdraft.CanaryPercent = 25
	h := NewHandlerWithoutConfigFilePath(&config.Config{
		Codex: config.CodexConfig{
			AccountDeviceIdentity: config.CodexAccountDeviceIdentityModeAccountDevice,
			WeeklyOverdraft:       overdraft,
		},
	}, nil)

	ctx, recorder := newManagementJSONContext(http.MethodGet, "/v0/management/codex-overdraft/config", "")
	h.GetCodexOverdraftConfig(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var payload codexOverdraftConfigRequest
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if !payload.Enabled || payload.Mode != config.CodexWeeklyOverdraftModeInject || payload.CanaryPercent != 25 {
		t.Fatalf("weekly overdraft config = %#v", payload)
	}
	if payload.AccountDeviceIdentity != config.CodexAccountDeviceIdentityModeAccountDevice {
		t.Fatalf("account device identity = %q", payload.AccountDeviceIdentity)
	}
}

func TestPutCodexOverdraftConfigPersistsAndReloads(t *testing.T) {
	configPath := writeTestConfigFile(t)
	h := &Handler{
		cfg: &config.Config{Codex: config.CodexConfig{
			IdentityConfuse: true,
			WeeklyOverdraft: config.DefaultCodexWeeklyOverdraftConfig(),
		}},
		configFilePath: configPath,
	}
	reloads, reloadDone := captureConfigReload(h)

	ctx, recorder := newManagementJSONContext(http.MethodPut, "/v0/management/codex-overdraft/config", codexOverdraftConfigTestBody())
	h.PutCodexOverdraftConfig(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	reloaded := waitForAsyncReload(t, reloads)
	waitForReloadDone(t, reloadDone)
	if !reloaded.Codex.WeeklyOverdraft.Enabled || reloaded.Codex.WeeklyOverdraft.CanaryPercent != 25 {
		t.Fatalf("reload weekly overdraft config = %#v", reloaded.Codex.WeeklyOverdraft)
	}
	if reloaded.Codex.AccountDeviceIdentity != config.CodexAccountDeviceIdentityModeAccountDevice {
		t.Fatalf("reload account device identity = %q", reloaded.Codex.AccountDeviceIdentity)
	}
	if h.cfg.Codex.WeeklyOverdraft.PairCount != 2 {
		t.Fatalf("handler pair count = %d, want 2", h.cfg.Codex.WeeklyOverdraft.PairCount)
	}
	if !h.cfg.Codex.IdentityConfuse {
		t.Fatal("unrelated identity-confuse config was changed")
	}
	persisted, errLoad := config.LoadConfig(configPath)
	if errLoad != nil {
		t.Fatalf("load persisted config: %v", errLoad)
	}
	if persisted.Codex.WeeklyOverdraft.CanaryPercent != 25 || persisted.Codex.AccountDeviceIdentity != config.CodexAccountDeviceIdentityModeAccountDevice || !persisted.Codex.IdentityConfuse {
		t.Fatalf("persisted config = %#v", persisted.Codex)
	}
}

func TestPutCodexOverdraftConfigRejectsUnknownFieldWithoutMutation(t *testing.T) {
	overdraft := config.DefaultCodexWeeklyOverdraftConfig()
	h := &Handler{cfg: &config.Config{Codex: config.CodexConfig{WeeklyOverdraft: overdraft}}, configFilePath: writeTestConfigFile(t)}

	ctx, recorder := newManagementJSONContext(http.MethodPut, "/v0/management/codex-overdraft/config", strings.TrimSuffix(codexOverdraftConfigTestBody(), "\n}")+",\n  \"unexpected\": true\n}")
	h.PutCodexOverdraftConfig(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if h.cfg.Codex.WeeklyOverdraft != overdraft || h.cfg.Codex.AccountDeviceIdentity != "" {
		t.Fatalf("config mutated after unknown field: %#v", h.cfg.Codex)
	}
}

func TestPutCodexOverdraftConfigRejectsInvalidValueWithoutMutation(t *testing.T) {
	overdraft := config.DefaultCodexWeeklyOverdraftConfig()
	h := &Handler{cfg: &config.Config{Codex: config.CodexConfig{WeeklyOverdraft: overdraft}}, configFilePath: writeTestConfigFile(t)}

	ctx, recorder := newManagementJSONContext(http.MethodPut, "/v0/management/codex-overdraft/config", strings.Replace(codexOverdraftConfigTestBody(), `"mode": "inject"`, `"mode": "invalid"`, 1))
	h.PutCodexOverdraftConfig(ctx)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}
	if h.cfg.Codex.WeeklyOverdraft != overdraft {
		t.Fatalf("config mutated after invalid value: %#v", h.cfg.Codex.WeeklyOverdraft)
	}
}

func TestPutCodexOverdraftConfigFailsSafeWhenPersistenceUnavailable(t *testing.T) {
	h := &Handler{cfg: &config.Config{Codex: config.CodexConfig{WeeklyOverdraft: config.DefaultCodexWeeklyOverdraftConfig()}}}
	ctx, recorder := newManagementJSONContext(http.MethodPut, "/v0/management/codex-overdraft/config", codexOverdraftConfigTestBody())
	h.PutCodexOverdraftConfig(ctx)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusServiceUnavailable, recorder.Body.String())
	}
}

func TestPutCodexOverdraftConfigLeavesMemoryUnchangedOnSaveFailure(t *testing.T) {
	overdraft := config.DefaultCodexWeeklyOverdraftConfig()
	h := &Handler{
		cfg:            &config.Config{Codex: config.CodexConfig{WeeklyOverdraft: overdraft}},
		configFilePath: filepath.Join(t.TempDir(), "missing", "config.yaml"),
	}
	ctx, recorder := newManagementJSONContext(http.MethodPut, "/v0/management/codex-overdraft/config", codexOverdraftConfigTestBody())
	h.PutCodexOverdraftConfig(ctx)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusInternalServerError, recorder.Body.String())
	}
	if h.cfg.Codex.WeeklyOverdraft != overdraft || h.cfg.Codex.AccountDeviceIdentity != "" {
		t.Fatalf("config mutated after save failure: %#v", h.cfg.Codex)
	}
	if _, errStat := os.Stat(h.configFilePath); !os.IsNotExist(errStat) {
		t.Fatalf("unexpected config file state: %v", errStat)
	}
}

func TestPostCodexOverdraftGateRequiresUsedPercentThreshold(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	ctx, recorder := newManagementJSONContext(http.MethodPost, "/v0/management/codex-overdraft/gate", `{"auth_id":"auth-1","window":"5h","used_percent":94,"remaining_percent":0,"threshold_percent":95,"verified":true}`)
	h.PostCodexOverdraftGate(ctx)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
	}

	ctx, recorder = newManagementJSONContext(http.MethodPost, "/v0/management/codex-overdraft/gate", `{"auth_id":"auth-1","window":"5h","used_percent":95,"remaining_percent":5,"threshold_percent":95,"verified":true}`)
	h.PostCodexOverdraftGate(ctx)
	if recorder.Code != http.StatusOK {
		t.Fatalf("valid status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
}
