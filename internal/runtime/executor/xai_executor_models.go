package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	xaiModelsResponseLimit  = 4 << 20
	xaiClientModeHeader     = "x-grok-client-mode"
	xaiClientModeHeadless   = "headless"
	xaiModelUserIDHeader    = "x-userid"
	xaiModelUserEmailHeader = "x-email"
)

// ListModels reads the model IDs exposed to one xAI credential through its
// active HTTP execution surface. The caller decides how to cache and register
// the returned per-auth snapshot.
func (e *XAIExecutor) ListModels(ctx context.Context, auth *cliproxyauth.Auth) ([]*registry.ModelInfo, error) {
	baseURL := strings.TrimRight(xaiChatBaseURL(auth), "/")
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/models", nil)
	if errRequest != nil {
		return nil, fmt.Errorf("xai models: create request: %w", errRequest)
	}
	applyXAIModelDiscoveryHeaders(req, auth)

	resp, errRequest := e.HttpRequest(ctx, auth, req)
	if errRequest != nil {
		return nil, fmt.Errorf("xai models: request failed: %w", errRequest)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("xai models: close response body: %v", errClose)
		}
	}()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("xai models: upstream status %d", resp.StatusCode)
	}

	body, errRead := io.ReadAll(io.LimitReader(resp.Body, xaiModelsResponseLimit+1))
	if errRead != nil {
		return nil, fmt.Errorf("xai models: read response: %w", errRead)
	}
	if len(body) > xaiModelsResponseLimit {
		return nil, fmt.Errorf("xai models: response exceeds %d bytes", xaiModelsResponseLimit)
	}

	var payload struct {
		Data []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if errDecode := json.Unmarshal(body, &payload); errDecode != nil {
		return nil, fmt.Errorf("xai models: decode response: %w", errDecode)
	}

	models := make([]*registry.ModelInfo, 0, len(payload.Data))
	seen := make(map[string]struct{}, len(payload.Data))
	for _, item := range payload.Data {
		modelID := strings.TrimSpace(item.ID)
		if modelID == "" {
			continue
		}
		if _, exists := seen[modelID]; exists {
			continue
		}
		seen[modelID] = struct{}{}
		object := strings.TrimSpace(item.Object)
		if object == "" {
			object = "model"
		}
		ownedBy := strings.TrimSpace(item.OwnedBy)
		if ownedBy == "" {
			ownedBy = "xai"
		}
		models = append(models, &registry.ModelInfo{
			ID:          modelID,
			Object:      object,
			Created:     item.Created,
			OwnedBy:     ownedBy,
			Type:        "xai",
			DisplayName: modelID,
			Name:        modelID,
		})
	}
	return models, nil
}

// applyXAIModelDiscoveryHeaders mirrors the official Grok CLI session headers
// only for the CLI Chat Proxy endpoint. Official API and custom endpoints keep
// the standard bearer/custom-header behavior.
func applyXAIModelDiscoveryHeaders(req *http.Request, auth *cliproxyauth.Auth) {
	if req == nil {
		return
	}
	token, _ := xaiCreds(auth)
	applyXAIChatHeaders(req, auth, token, false, "")
	if !xaiIsCLIChatProxyBaseURL(xaiChatBaseURL(auth)) {
		return
	}
	req.Header.Set(xaiClientModeHeader, xaiClientModeHeadless)
	if auth != nil {
		if userID := xaiMetadataString(auth.Metadata, "sub"); userID != "" {
			req.Header.Set(xaiModelUserIDHeader, userID)
		}
		if email := xaiMetadataString(auth.Metadata, "email"); email != "" {
			req.Header.Set(xaiModelUserEmailHeader, email)
		}
	}
	// Preserve the project's custom-header precedence for deployments that need
	// to override a session identity header explicitly.
	applyXAICustomHeaders(req, auth)
}
