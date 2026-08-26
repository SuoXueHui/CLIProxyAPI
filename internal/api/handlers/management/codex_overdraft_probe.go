package management

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type codexOverdraftProbeRequest struct {
	AuthID    string `json:"auth_id"`
	AuthIndex string `json:"auth_index"`
	Model     string `json:"model"`
	Window    string `json:"window"`
	CycleKey  string `json:"cycle_key"`
}

type codexOverdraftConfigRequest struct {
	Enabled               bool   `json:"enabled"`
	Mode                  string `json:"mode"`
	CanaryPercent         int    `json:"canary_percent"`
	PairCount             int    `json:"pair_count"`
	TailPolicy            string `json:"tail_policy"`
	OAuthOnly             bool   `json:"oauth_only"`
	MaxBodyBytes          int    `json:"max_body_bytes"`
	GateMode              string `json:"gate_mode"`
	QuotaThresholdPercent int    `json:"quota_threshold_percent"`
	AccountDeviceIdentity string `json:"account_device_identity"`
}

type codexOverdraftGateRequest struct {
	AuthID      string  `json:"auth_id"`
	Window      string  `json:"window"`
	CycleKey    string  `json:"cycle_key"`
	ResetAtMS   int64   `json:"reset_at_ms"`
	UsedPercent float64 `json:"used_percent"`
	Remaining   float64 `json:"remaining_percent"`
	Threshold   int     `json:"threshold_percent"`
	Verified    bool    `json:"verified"`
}

// GetCodexOverdraftConfig returns only the data-plane controls managed by CPA Manager.
func (h *Handler) GetCodexOverdraftConfig(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "management handler unavailable"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config unavailable"})
		return
	}
	c.JSON(http.StatusOK, codexOverdraftConfigResponse(h.cfg))
}

// PutCodexOverdraftConfig applies a bounded control-plane configuration update.
// The normal config reload hook is used so all existing runtime components see
// the same validated snapshot without exposing the full YAML config to Manager.
func (h *Handler) PutCodexOverdraftConfig(c *gin.Context) {
	if h == nil || strings.TrimSpace(h.configFilePath) == "" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config persistence unavailable"})
		return
	}
	var input codexOverdraftConfigRequest
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid overdraft config"})
		return
	}
	h.mu.Lock()
	if h.cfg == nil {
		h.mu.Unlock()
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config unavailable"})
		return
	}
	next := h.cfg.CloneForRuntime()
	next.Codex.WeeklyOverdraft = config.CodexWeeklyOverdraftConfig{
		Enabled: input.Enabled, Mode: input.Mode, CanaryPercent: input.CanaryPercent,
		PairCount: input.PairCount, TailPolicy: input.TailPolicy, OAuthOnly: input.OAuthOnly,
		MaxBodyBytes: input.MaxBodyBytes, GateMode: input.GateMode,
		QuotaThresholdPercent: input.QuotaThresholdPercent,
	}
	next.Codex.WeeklyOverdraft.Normalize()
	if err := next.Codex.WeeklyOverdraft.Validate(); err != nil {
		h.mu.Unlock()
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	next.Codex.AccountDeviceIdentity = config.NormalizeCodexAccountDeviceIdentityMode(input.AccountDeviceIdentity)
	if err := config.ValidateCodexAccountDeviceIdentityMode(next.Codex.AccountDeviceIdentity); err != nil {
		h.mu.Unlock()
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	// Persist the clone before publishing it to the running process. A write
	// failure therefore leaves both the in-memory and previous file state intact.
	if errSave := config.SaveConfigPreserveComments(h.configFilePath, next); errSave != nil {
		h.mu.Unlock()
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to save overdraft config"})
		return
	}
	h.cfg = next
	snapshot := h.reloadSnapshotConfigLocked()
	h.mu.Unlock()
	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), snapshot)
	c.JSON(http.StatusOK, codexOverdraftConfigResponse(next))
}

// PostCodexOverdraftGate accepts only Manager-normalized quota snapshot evidence.
func (h *Handler) PostCodexOverdraftGate(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "management handler unavailable"})
		return
	}
	var input codexOverdraftGateRequest
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || strings.TrimSpace(input.AuthID) == "" || !input.Verified {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid gate evidence"})
		return
	}
	window := normalizeCodexGateWindow(input.Window)
	if window == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "window must be 5h or 7d"})
		return
	}
	helperThreshold := input.Threshold
	if helperThreshold < 1 || helperThreshold > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "threshold_percent must be between 1 and 100"})
		return
	}
	if input.UsedPercent < 0 || input.UsedPercent > 100 || input.Remaining < 0 || input.Remaining > 100 || (input.UsedPercent == 0 && input.Remaining == 0) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "quota evidence percentages are invalid"})
		return
	}
	// Manager sends the measured used percentage as the authoritative threshold
	// signal; remaining_percent is retained for observability only and cannot
	// override a below-threshold used value.
	if input.UsedPercent < float64(helperThreshold) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "quota evidence is below threshold"})
		return
	}
	helps.RecordCodexWeeklyOverdraftQuotaEvidenceWithThresholdAndCycle(
		input.AuthID, window, helperThreshold, input.CycleKey, input.ResetAtMS,
		http.StatusTooManyRequests, nil, []byte(`{"error":{"code":"quota_snapshot_threshold"}}`),
	)
	c.JSON(http.StatusOK, gin.H{"ok": true, "window": window})
}

func normalizeCodexGateWindow(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "5h", "five_hour", "five-hour":
		return "5h"
	case "7d", "weekly", "seven_day", "seven-day":
		return "7d"
	default:
		return ""
	}
}

func codexOverdraftConfigResponse(cfg *config.Config) gin.H {
	return gin.H{
		"enabled":                 cfg.Codex.WeeklyOverdraft.Enabled,
		"mode":                    cfg.Codex.WeeklyOverdraft.Mode,
		"canary_percent":          cfg.Codex.WeeklyOverdraft.CanaryPercent,
		"pair_count":              cfg.Codex.WeeklyOverdraft.PairCount,
		"tail_policy":             cfg.Codex.WeeklyOverdraft.TailPolicy,
		"oauth_only":              cfg.Codex.WeeklyOverdraft.OAuthOnly,
		"max_body_bytes":          cfg.Codex.WeeklyOverdraft.MaxBodyBytes,
		"gate_mode":               cfg.Codex.WeeklyOverdraft.GateMode,
		"quota_threshold_percent": cfg.Codex.WeeklyOverdraft.QuotaThresholdPercent,
		"account_device_identity": cfg.Codex.AccountDeviceIdentity,
	}
}

// PostCodexOverdraftProbe performs one bounded, Manager-owned probe through the
// normal Codex executor. The request is marked in metadata so it cannot trigger
// a second weekly-overdraft injection.
func (h *Handler) PostCodexOverdraftProbe(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "codex auth manager unavailable"})
		return
	}
	var input codexOverdraftProbeRequest
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid probe request"})
		return
	}
	auth := h.resolveCodexProbeAuth(input)
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") || auth.AuthKind() != cliproxyauth.AuthKindOAuth {
		c.JSON(http.StatusBadRequest, gin.H{"error": "a Codex OAuth auth_id or auth_index is required"})
		return
	}
	model := strings.TrimSpace(input.Model)
	if model == "" {
		model = "gpt-5-codex"
	}
	body, _ := json.Marshal(map[string]any{
		"model": model,
		"input": []any{map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": "probe"}},
		}},
		"stream": false,
	})
	metadata := map[string]any{
		helps.CodexWeeklyOverdraftProbeMetadataKey: true,
		"codex_overdraft_window":                   strings.TrimSpace(input.Window),
		"codex_overdraft_cycle_key":                strings.TrimSpace(input.CycleKey),
	}
	request := cliproxyexecutor.Request{Model: model, Payload: body, Format: sdktranslator.FormatOpenAIResponse, Metadata: metadata}
	options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Metadata: metadata}
	response, errExecute := executor.NewCodexExecutor(h.currentConfig()).Execute(c.Request.Context(), auth, request, options)
	statusCode := http.StatusOK
	if errExecute != nil {
		statusCode = statusCodeFromProbeError(errExecute)
		c.JSON(http.StatusOK, gin.H{"ok": false, "status_code": statusCode, "window": strings.TrimSpace(input.Window), "error": sanitizeProbeError(errExecute)})
		return
	}
	// 探测只返回判定所需的状态，避免把上游响应头中的令牌、追踪信息或内部实现细节暴露给管理面。
	_ = response
	c.JSON(http.StatusOK, gin.H{"ok": true, "status_code": statusCode, "window": strings.TrimSpace(input.Window)})
}

func (h *Handler) resolveCodexProbeAuth(input codexOverdraftProbeRequest) *cliproxyauth.Auth {
	if h == nil || h.authManager == nil {
		return nil
	}
	if id := strings.TrimSpace(input.AuthID); id != "" {
		if auth, ok := h.authManager.GetByID(id); ok {
			return auth
		}
	}
	return h.authByIndex(strings.TrimSpace(input.AuthIndex))
}

func (h *Handler) currentConfig() *config.Config {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cfg == nil {
		return &config.Config{}
	}
	return h.cfg.CloneForRuntime()
}

func statusCodeFromProbeError(err error) int {
	var statusErr cliproxyexecutor.StatusError
	if errors.As(err, &statusErr) && statusErr.StatusCode() > 0 {
		return statusErr.StatusCode()
	}
	return http.StatusBadGateway
}

func sanitizeProbeError(err error) string {
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(err.Error())
	if len(message) > 512 {
		message = message[:512]
	}
	return message
}
