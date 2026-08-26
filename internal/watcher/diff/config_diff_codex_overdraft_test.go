package diff

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestBuildConfigChangeDetailsCodexWeeklyOverdraft(t *testing.T) {
	oldCfg := &config.Config{Codex: config.CodexConfig{WeeklyOverdraft: config.DefaultCodexWeeklyOverdraftConfig(), AccountDeviceIdentity: config.CodexAccountDeviceIdentityModeOff}}
	newCfg := &config.Config{Codex: config.CodexConfig{AccountDeviceIdentity: config.CodexAccountDeviceIdentityModeAccountDevice, WeeklyOverdraft: config.CodexWeeklyOverdraftConfig{
		Enabled:               true,
		Mode:                  "inject",
		CanaryPercent:         25,
		PairCount:             2,
		TailPolicy:            "user-only",
		OAuthOnly:             false,
		MaxBodyBytes:          1048576,
		GateMode:              config.CodexWeeklyOverdraftGateModeHeaderOr429,
		QuotaThresholdPercent: 100,
	}}}

	details := BuildConfigChangeDetails(oldCfg, newCfg)
	expectContains(t, details, "codex.weekly-overdraft.enabled: false -> true")
	expectContains(t, details, "codex.weekly-overdraft.mode: observe -> inject")
	expectContains(t, details, "codex.weekly-overdraft.canary-percent: 10 -> 25")
	expectContains(t, details, "codex.weekly-overdraft.pair-count: 1 -> 2")
	expectContains(t, details, "codex.weekly-overdraft.tail-policy: user-and-tool-output -> user-only")
	expectContains(t, details, "codex.weekly-overdraft.oauth-only: true -> false")
	expectContains(t, details, "codex.weekly-overdraft.max-body-bytes: 8388608 -> 1048576")
	expectContains(t, details, "codex.account-device-identity: off -> account_device")
	expectContains(t, details, "codex.weekly-overdraft.gate-mode: off -> header-or-429")
	expectContains(t, details, "codex.weekly-overdraft.quota-threshold-percent: 95 -> 100")
}
