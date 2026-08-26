package config

import (
	"fmt"
	"strings"
)

const (
	CodexWeeklyOverdraftModeObserve = "observe"
	CodexWeeklyOverdraftModeInject  = "inject"

	CodexWeeklyOverdraftTailUserOnly          = "user-only"
	CodexWeeklyOverdraftTailUserAndToolOutput = "user-and-tool-output"
	CodexWeeklyOverdraftGateModeOff           = "off"
	CodexWeeklyOverdraftGateModeHeaderOr429   = "header-or-429"

	defaultCodexWeeklyOverdraftCanaryPercent = 10
	defaultCodexWeeklyOverdraftPairCount     = 1
	defaultCodexWeeklyOverdraftMaxBodyBytes  = 8 * 1024 * 1024
	maxCodexWeeklyOverdraftBodyBytes         = 32 * 1024 * 1024
)

// CodexWeeklyOverdraftConfig controls the experimental request transform used to
// test limited Codex generation after a five-hour or seven-day quota is exhausted.
type CodexWeeklyOverdraftConfig struct {
	Enabled               bool   `yaml:"enabled" json:"enabled"`
	Mode                  string `yaml:"mode" json:"mode"`
	CanaryPercent         int    `yaml:"canary-percent" json:"canary-percent"`
	PairCount             int    `yaml:"pair-count" json:"pair-count"`
	TailPolicy            string `yaml:"tail-policy" json:"tail-policy"`
	OAuthOnly             bool   `yaml:"oauth-only" json:"oauth-only"`
	MaxBodyBytes          int    `yaml:"max-body-bytes" json:"max-body-bytes"`
	GateMode              string `yaml:"gate-mode" json:"gate-mode"`
	QuotaThresholdPercent int    `yaml:"quota-threshold-percent" json:"quota-threshold-percent"`
}

// DefaultCodexWeeklyOverdraftConfig returns conservative defaults. The feature
// remains disabled until an operator explicitly enables it.
func DefaultCodexWeeklyOverdraftConfig() CodexWeeklyOverdraftConfig {
	return CodexWeeklyOverdraftConfig{
		Mode:                  CodexWeeklyOverdraftModeObserve,
		CanaryPercent:         defaultCodexWeeklyOverdraftCanaryPercent,
		PairCount:             defaultCodexWeeklyOverdraftPairCount,
		TailPolicy:            CodexWeeklyOverdraftTailUserAndToolOutput,
		OAuthOnly:             true,
		MaxBodyBytes:          defaultCodexWeeklyOverdraftMaxBodyBytes,
		GateMode:              CodexWeeklyOverdraftGateModeOff,
		QuotaThresholdPercent: 95,
	}
}

// Normalize restores conservative defaults for legacy zero-value Config
// marshals, then canonicalizes enum-like fields before validation and runtime use.
func (c *CodexWeeklyOverdraftConfig) Normalize() {
	if c == nil {
		return
	}
	if *c == (CodexWeeklyOverdraftConfig{}) {
		*c = DefaultCodexWeeklyOverdraftConfig()
		return
	}
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	c.TailPolicy = strings.ToLower(strings.TrimSpace(c.TailPolicy))
	c.GateMode = strings.ToLower(strings.TrimSpace(c.GateMode))
}

// Validate rejects configurations that could silently broaden the experiment.
func (c CodexWeeklyOverdraftConfig) Validate() error {
	switch c.Mode {
	case CodexWeeklyOverdraftModeObserve, CodexWeeklyOverdraftModeInject:
	default:
		return fmt.Errorf("codex.weekly-overdraft.mode must be %q or %q", CodexWeeklyOverdraftModeObserve, CodexWeeklyOverdraftModeInject)
	}
	if c.CanaryPercent < 1 || c.CanaryPercent > 100 {
		return fmt.Errorf("codex.weekly-overdraft.canary-percent must be between 1 and 100")
	}
	switch c.PairCount {
	case 1, 2, 4:
	default:
		return fmt.Errorf("codex.weekly-overdraft.pair-count must be 1, 2, or 4")
	}
	switch c.TailPolicy {
	case CodexWeeklyOverdraftTailUserOnly, CodexWeeklyOverdraftTailUserAndToolOutput:
	default:
		return fmt.Errorf("codex.weekly-overdraft.tail-policy must be %q or %q", CodexWeeklyOverdraftTailUserOnly, CodexWeeklyOverdraftTailUserAndToolOutput)
	}
	if c.MaxBodyBytes < 1 || c.MaxBodyBytes > maxCodexWeeklyOverdraftBodyBytes {
		return fmt.Errorf("codex.weekly-overdraft.max-body-bytes must be between 1 and %d", maxCodexWeeklyOverdraftBodyBytes)
	}
	if c.GateMode != CodexWeeklyOverdraftGateModeOff && c.GateMode != CodexWeeklyOverdraftGateModeHeaderOr429 {
		return fmt.Errorf("codex.weekly-overdraft.gate-mode must be %q or %q", CodexWeeklyOverdraftGateModeOff, CodexWeeklyOverdraftGateModeHeaderOr429)
	}
	if c.QuotaThresholdPercent < 1 || c.QuotaThresholdPercent > 100 {
		return fmt.Errorf("codex.weekly-overdraft.quota-threshold-percent must be between 1 and 100")
	}
	return nil
}
