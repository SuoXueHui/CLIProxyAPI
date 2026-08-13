package management

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	defaultCodexQuotaUsageURL = "https://chatgpt.com/backend-api/wham/usage"
	codexQuotaUpstreamTimeout = 8 * time.Second
	maxCodexQuotaBodyBytes    = int64(256 << 10)
)

// codexQuotaRawWindow is the minimum window projection accepted from ChatGPT.
// Both duration forms are supported because upstream variants have used each.
type codexQuotaRawWindow struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	LimitWindowMinutes int64   `json:"limit_window_minutes"`
	WindowSeconds      int64   `json:"window_seconds"`
	WindowMinutes      int64   `json:"window_minutes"`
	ResetAfterSeconds  int64   `json:"reset_after_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

type codexQuotaRateLimit struct {
	PrimaryWindow   *codexQuotaRawWindow `json:"primary_window"`
	SecondaryWindow *codexQuotaRawWindow `json:"secondary_window"`
}

type codexQuotaUpstreamResponse struct {
	PlanType  string               `json:"plan_type"`
	RateLimit *codexQuotaRateLimit `json:"rate_limit"`
}

// codexQuotaWindowResponse is the stable read-only DTO exposed to management clients.
type codexQuotaWindowResponse struct {
	UsedPercent   float64    `json:"used_percent"`
	WindowSeconds int64      `json:"window_seconds"`
	ResetAt       *time.Time `json:"reset_at,omitempty"`
}

type codexQuotaUsageResponse struct {
	PlanType  string                    `json:"plan_type"`
	FetchedAt time.Time                 `json:"fetched_at"`
	FiveHour  *codexQuotaWindowResponse `json:"five_hour"`
	SevenDay  *codexQuotaWindowResponse `json:"seven_day"`
}

// GetCodexQuotaUsage fetches official ChatGPT quota for one Codex auth_index.
// The caller controls only auth_index; target, method, headers, and DTO are fixed.
func (h *Handler) GetCodexQuotaUsage(c *gin.Context) {
	authIndex := strings.TrimSpace(c.Query("auth_index"))
	if authIndex == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid codex auth_index is required"})
		return
	}
	auth := h.authByIndex(authIndex)
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "valid codex auth_index is required"})
		return
	}

	accessToken := strings.TrimSpace(tokenValueForAuth(auth))
	accountID := stringValue(auth.Metadata, "account_id")
	if accessToken == "" || accountID == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "codex quota credential is unavailable"})
		return
	}

	fetchedAt := time.Now().UTC()
	requestCtx, cancel := context.WithTimeout(c.Request.Context(), codexQuotaUpstreamTimeout)
	defer cancel()
	req, errRequest := http.NewRequestWithContext(requestCtx, http.MethodGet, h.codexQuotaTarget(), nil)
	if errRequest != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "codex quota request is invalid"})
		return
	}
	setCodexQuotaHeaders(req.Header, accessToken, accountID)

	client := h.codexQuotaClient(auth)
	resp, errDo := client.Do(req)
	if errDo != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "codex quota request failed"})
		return
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("management Codex quota response body close failed")
		}
	}()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		c.JSON(codexQuotaUpstreamStatus(resp.StatusCode), gin.H{"error": "codex quota upstream rejected request"})
		return
	}

	payload, errRead := io.ReadAll(io.LimitReader(resp.Body, maxCodexQuotaBodyBytes+1))
	if errRead != nil || int64(len(payload)) > maxCodexQuotaBodyBytes {
		c.JSON(http.StatusBadGateway, gin.H{"error": "codex quota response is invalid"})
		return
	}
	var upstream codexQuotaUpstreamResponse
	if errDecode := json.Unmarshal(payload, &upstream); errDecode != nil || upstream.RateLimit == nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "codex quota response is invalid"})
		return
	}
	fiveHour, sevenDay := normalizeCodexQuotaWindowsAt(upstream.RateLimit, fetchedAt)
	if fiveHour == nil && sevenDay == nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "codex quota windows are unavailable"})
		return
	}

	c.JSON(http.StatusOK, codexQuotaUsageResponse{
		PlanType:  sanitizeCodexPlanType(upstream.PlanType),
		FetchedAt: fetchedAt,
		FiveHour:  fiveHour,
		SevenDay:  sevenDay,
	})
}

func (h *Handler) codexQuotaTarget() string {
	if h != nil && strings.TrimSpace(h.codexQuotaUsageURL) != "" {
		return strings.TrimSpace(h.codexQuotaUsageURL)
	}
	return defaultCodexQuotaUsageURL
}

func (h *Handler) codexQuotaClient(auth *coreauth.Auth) *http.Client {
	if h != nil && h.codexQuotaHTTPClient != nil {
		return h.codexQuotaHTTPClient
	}
	return &http.Client{
		Timeout:   codexQuotaUpstreamTimeout,
		Transport: h.apiCallTransport(auth, ""),
		// 官方 quota 目标固定；拒绝跟随任何重定向，避免 Authorization 被带到非预期主机。
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func setCodexQuotaHeaders(header http.Header, accessToken, accountID string) {
	header.Set("Authorization", "Bearer "+accessToken)
	header.Set("ChatGPT-Account-ID", accountID)
	header.Set("OpenAI-Beta", "codex-1")
	header.Set("OAI-Language", "zh-CN")
	header.Set("Originator", "Codex Desktop")
	header.Set("Accept", "application/json")
	header.Set("Sec-Fetch-Site", "none")
	header.Set("Sec-Fetch-Mode", "no-cors")
	header.Set("Sec-Fetch-Dest", "empty")
	header.Set("Priority", "u=4, i")
}

func codexQuotaUpstreamStatus(status int) int {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
		return status
	default:
		return http.StatusBadGateway
	}
}

func normalizeCodexQuotaWindows(limit *codexQuotaRateLimit) (*codexQuotaWindowResponse, *codexQuotaWindowResponse) {
	return normalizeCodexQuotaWindowsAt(limit, time.Now().UTC())
}

// normalizeCodexQuotaWindowsAt classifies by actual duration, not slot order.
func normalizeCodexQuotaWindowsAt(limit *codexQuotaRateLimit, fetchedAt time.Time) (*codexQuotaWindowResponse, *codexQuotaWindowResponse) {
	if limit == nil {
		return nil, nil
	}
	primary := normalizeCodexQuotaWindow(limit.PrimaryWindow, fetchedAt)
	secondary := normalizeCodexQuotaWindow(limit.SecondaryWindow, fetchedAt)
	var fiveHour *codexQuotaWindowResponse
	var sevenDay *codexQuotaWindowResponse
	for _, window := range []*codexQuotaWindowResponse{primary, secondary} {
		if window == nil {
			continue
		}
		if window.WindowSeconds <= 6*60*60 {
			// When upstream unexpectedly returns two short slots, retain the one
			// closest to the canonical 5h duration instead of inventing a 7d slot.
			if fiveHour == nil || durationDistance(window.WindowSeconds, 5*60*60) < durationDistance(fiveHour.WindowSeconds, 5*60*60) {
				fiveHour = window
			}
			continue
		}
		// The longest long-duration slot is the best available weekly window.
		if sevenDay == nil || window.WindowSeconds > sevenDay.WindowSeconds {
			sevenDay = window
		}
	}
	return fiveHour, sevenDay
}

func durationDistance(left, right int64) int64 {
	if left >= right {
		return left - right
	}
	return right - left
}

func normalizeCodexQuotaWindow(raw *codexQuotaRawWindow, fetchedAt time.Time) *codexQuotaWindowResponse {
	if raw == nil || math.IsNaN(raw.UsedPercent) || math.IsInf(raw.UsedPercent, 0) || raw.UsedPercent < 0 || raw.UsedPercent > 1000 || raw.ResetAfterSeconds < 0 {
		return nil
	}
	windowSeconds := raw.LimitWindowSeconds
	if windowSeconds <= 0 {
		windowSeconds = raw.WindowSeconds
	}
	if windowSeconds <= 0 {
		windowMinutes := raw.LimitWindowMinutes
		if windowMinutes <= 0 {
			windowMinutes = raw.WindowMinutes
		}
		if windowMinutes > 0 && windowMinutes <= 31*24*60 {
			windowSeconds = windowMinutes * 60
		}
	}
	if windowSeconds <= 0 || windowSeconds > 31*24*60*60 {
		return nil
	}

	result := &codexQuotaWindowResponse{UsedPercent: raw.UsedPercent, WindowSeconds: windowSeconds}
	if raw.ResetAt > 0 {
		resetAt := time.Unix(raw.ResetAt, 0).UTC()
		if resetAt.Year() >= 2000 && resetAt.Year() <= 2200 {
			result.ResetAt = &resetAt
		}
	} else if raw.ResetAfterSeconds > 0 {
		const maxResetAfterSeconds = int64((31 * 24 * time.Hour) / time.Second)
		if raw.ResetAfterSeconds <= maxResetAfterSeconds {
			resetAt := fetchedAt.UTC().Add(time.Duration(raw.ResetAfterSeconds) * time.Second)
			result.ResetAt = &resetAt
		}
	}
	return result
}

func sanitizeCodexPlanType(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 32 {
		return ""
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return ""
	}
	return value
}
