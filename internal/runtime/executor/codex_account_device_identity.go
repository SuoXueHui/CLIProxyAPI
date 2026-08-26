package executor

import (
	"strings"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const codexAccountDeviceIdentityNamespace = "cli-proxy-api:codex:account-device:v1"

// codexAccountDeviceIdentityEnabled limits the feature to Codex OAuth auths.
// API keys and other providers retain their existing identity behavior.
func codexAccountDeviceIdentityEnabled(cfg *config.Config, auth *cliproxyauth.Auth) bool {
	if cfg == nil || auth == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return false
	}
	if auth.AuthKind() != cliproxyauth.AuthKindOAuth {
		return false
	}
	return config.NormalizeCodexAccountDeviceIdentityMode(cfg.Codex.AccountDeviceIdentity) == config.CodexAccountDeviceIdentityModeAccountDevice && strings.TrimSpace(auth.ID) != ""
}

// codexAccountDeviceIdentityUUID derives a stable, non-secret installation ID from auth ID.
func codexAccountDeviceIdentityUUID(authID string) string {
	name := codexAccountDeviceIdentityNamespace + ":" + strings.TrimSpace(authID)
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(name)).String()
}

// applyCodexAccountDeviceIdentityBody writes only the installation identity.
// A malformed client_metadata object is left unchanged to preserve fail-open behavior.
func applyCodexAccountDeviceIdentityBody(cfg *config.Config, auth *cliproxyauth.Auth, rawJSON []byte) ([]byte, bool) {
	if !codexAccountDeviceIdentityEnabled(cfg, auth) || len(rawJSON) == 0 || !gjson.ValidBytes(rawJSON) {
		return rawJSON, false
	}
	clientMetadata := gjson.GetBytes(rawJSON, "client_metadata")
	if clientMetadata.Exists() && !clientMetadata.IsObject() {
		return rawJSON, false
	}
	installationID := codexAccountDeviceIdentityUUID(auth.ID)
	updated, errSet := sjson.SetBytes(rawJSON, "client_metadata.x-codex-installation-id", installationID)
	if errSet != nil || len(updated) == 0 {
		return rawJSON, false
	}
	return updated, true
}
