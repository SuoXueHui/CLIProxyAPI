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
