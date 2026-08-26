package config

import (
	"fmt"
	"strings"
)

const (
	// CodexAccountDeviceIdentityModeOff preserves the client-provided identity behavior.
	CodexAccountDeviceIdentityModeOff = "off"
	// CodexAccountDeviceIdentityModeAccountDevice derives one stable installation ID per OAuth auth.
	CodexAccountDeviceIdentityModeAccountDevice = "account_device"
)

// DefaultCodexAccountDeviceIdentityMode returns the backwards-compatible mode.
func DefaultCodexAccountDeviceIdentityMode() string {
	return CodexAccountDeviceIdentityModeOff
}

// NormalizeCodexAccountDeviceIdentityMode canonicalizes accepted config aliases.
func NormalizeCodexAccountDeviceIdentityMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", CodexAccountDeviceIdentityModeOff:
		return CodexAccountDeviceIdentityModeOff
	case CodexAccountDeviceIdentityModeAccountDevice, "account-device":
		return CodexAccountDeviceIdentityModeAccountDevice
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
}

// ValidateCodexAccountDeviceIdentityMode rejects unknown modes before runtime use.
func ValidateCodexAccountDeviceIdentityMode(mode string) error {
	switch NormalizeCodexAccountDeviceIdentityMode(mode) {
	case CodexAccountDeviceIdentityModeOff, CodexAccountDeviceIdentityModeAccountDevice:
		return nil
	default:
		return fmt.Errorf("codex.account-device-identity must be %q or %q", CodexAccountDeviceIdentityModeOff, CodexAccountDeviceIdentityModeAccountDevice)
	}
}
