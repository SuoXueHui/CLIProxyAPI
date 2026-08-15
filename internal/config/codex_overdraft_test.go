package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseConfigBytesCodexWeeklyOverdraftDefaults(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`{}`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	got := cfg.Codex.WeeklyOverdraft
	if got.Enabled {
		t.Fatal("Enabled = true, want false")
	}
	if got.Mode != "observe" {
		t.Fatalf("Mode = %q, want observe", got.Mode)
	}
	if got.CanaryPercent != 10 {
		t.Fatalf("CanaryPercent = %d, want 10", got.CanaryPercent)
	}
	if got.PairCount != 1 {
		t.Fatalf("PairCount = %d, want 1", got.PairCount)
	}
	if got.TailPolicy != "user-and-tool-output" {
		t.Fatalf("TailPolicy = %q, want user-and-tool-output", got.TailPolicy)
	}
	if !got.OAuthOnly {
		t.Fatal("OAuthOnly = false, want true")
	}
	if got.MaxBodyBytes != 8*1024*1024 {
		t.Fatalf("MaxBodyBytes = %d, want %d", got.MaxBodyBytes, 8*1024*1024)
	}
}

func TestParseConfigBytesCodexWeeklyOverdraftExplicitValues(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
codex:
  weekly-overdraft:
    enabled: true
    mode: inject
    canary-percent: 25
    pair-count: 2
    tail-policy: user-only
    oauth-only: false
    max-body-bytes: 1048576
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}

	got := cfg.Codex.WeeklyOverdraft
	if !got.Enabled || got.Mode != "inject" || got.CanaryPercent != 25 || got.PairCount != 2 || got.TailPolicy != "user-only" || got.OAuthOnly || got.MaxBodyBytes != 1048576 {
		t.Fatalf("WeeklyOverdraft = %#v", got)
	}
}

func TestLoadConfigOptionalCodexWeeklyOverdraftDefaultsMatchParser(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if errWrite := os.WriteFile(configPath, []byte(`{}`), 0o600); errWrite != nil {
		t.Fatalf("write config: %v", errWrite)
	}

	cfg, errLoad := LoadConfigOptional(configPath, false)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	if got, want := cfg.Codex.WeeklyOverdraft, DefaultCodexWeeklyOverdraftConfig(); got != want {
		t.Fatalf("WeeklyOverdraft = %#v, want %#v", got, want)
	}
}

func TestParseConfigBytesRejectsInvalidCodexWeeklyOverdraft(t *testing.T) {
	tests := map[string]string{
		"mode":           "mode: adaptive",
		"canary low":     "canary-percent: 0",
		"canary high":    "canary-percent: 101",
		"pair count":     "pair-count: 3",
		"tail policy":    "tail-policy: any",
		"body size low":  "max-body-bytes: 0",
		"body size high": "max-body-bytes: 33554433",
	}
	for name, field := range tests {
		t.Run(name, func(t *testing.T) {
			_, errParse := ParseConfigBytes([]byte("codex:\n  weekly-overdraft:\n    " + field + "\n"))
			if errParse == nil {
				t.Fatalf("ParseConfigBytes(%q) error = nil", field)
			}
		})
	}
}
