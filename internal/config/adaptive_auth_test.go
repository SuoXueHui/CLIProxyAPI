package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestParseConfigBytesAdaptiveAuthDefaults(t *testing.T) {
	parsed, errParse := ParseConfigBytes([]byte("{}"))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	cfg := parsed.Routing.AdaptiveAuth
	if !cfg.Enabled {
		t.Fatal("adaptive auth scheduling must be enabled by default")
	}
	if cfg.FirstEventThreshold != 15*time.Second {
		t.Fatalf("FirstEventThreshold = %v, want 15s", cfg.FirstEventThreshold)
	}
	if cfg.SlowStreak != 2 || cfg.Penalty != 30*time.Second || cfg.MaxPenalty != 5*time.Minute {
		t.Fatalf("unexpected penalty defaults: %#v", cfg)
	}
	if cfg.EWMAAlpha != 0.2 || cfg.MinSamples != 3 || cfg.LoadFloor != 10*time.Second || cfg.StateTTL != 6*time.Hour || cfg.MaxStateEntries != 4096 {
		t.Fatalf("unexpected runtime defaults: %#v", cfg)
	}
}

func TestLoadConfigOptionalAdaptiveAuthDefaults(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(t *testing.T) string
	}{
		{
			name: "missing file",
			prepare: func(t *testing.T) string {
				return filepath.Join(t.TempDir(), "missing.yaml")
			},
		},
		{
			name: "empty file",
			prepare: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "config.yaml")
				if errWrite := os.WriteFile(path, nil, 0o600); errWrite != nil {
					t.Fatalf("os.WriteFile() error = %v", errWrite)
				}
				return path
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg, errLoad := LoadConfigOptional(test.prepare(t), true)
			if errLoad != nil {
				t.Fatalf("LoadConfigOptional() error = %v", errLoad)
			}
			if !cfg.Routing.AdaptiveAuth.Enabled {
				t.Fatal("optional config must retain the active adaptive auth default")
			}
		})
	}
}

func TestAdaptiveAuthConfigProgrammaticDisable(t *testing.T) {
	cfg := (AdaptiveAuthConfig{Enabled: false}).WithDefaults()
	if cfg.Enabled {
		t.Fatal("programmatic adaptive auth Enabled=false must remain disabled")
	}
}

func TestAdaptiveAuthConfigJSONEnabledCompatibility(t *testing.T) {
	tests := []struct {
		name        string
		payload     string
		wantEnabled bool
	}{
		{name: "omitted enabled uses active default", payload: `{"penalty":30000000000}`, wantEnabled: true},
		{name: "explicit false disables", payload: `{"enabled":false}`, wantEnabled: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var cfg AdaptiveAuthConfig
			if errDecode := json.Unmarshal([]byte(test.payload), &cfg); errDecode != nil {
				t.Fatalf("json.Unmarshal() error = %v", errDecode)
			}
			cfg = cfg.WithDefaults()
			if cfg.Enabled != test.wantEnabled {
				t.Fatalf("Enabled = %t, want %t", cfg.Enabled, test.wantEnabled)
			}
		})
	}
}

func TestAdaptiveAuthConfigProgrammaticDisableRoundTrip(t *testing.T) {
	cfg := (AdaptiveAuthConfig{Enabled: false}).WithDefaults()
	payload, errMarshal := yaml.Marshal(cfg)
	if errMarshal != nil {
		t.Fatalf("yaml.Marshal() error = %v", errMarshal)
	}
	var decoded AdaptiveAuthConfig
	if errDecode := yaml.Unmarshal(payload, &decoded); errDecode != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", errDecode)
	}
	if decoded.WithDefaults().Enabled {
		t.Fatalf("round-trip config Enabled = true, payload = %s", payload)
	}
}

func TestAdaptiveAuthConfigYAMLAndValidation(t *testing.T) {
	var routing RoutingConfig
	if errDecode := yaml.Unmarshal([]byte(`adaptive-auth:
  enabled: true
  observe-only: true
  first-event-threshold: 20s
  slow-streak: 3
  penalty: 45s
  max-penalty: 6m
  ewma-alpha: 0.3
  min-samples: 5
  load-floor: 12s
  state-ttl: 2h
  max-state-entries: 128
`), &routing); errDecode != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", errDecode)
	}
	cfg := routing.AdaptiveAuth.WithDefaults()
	if errValidate := cfg.Validate(); errValidate != nil {
		t.Fatalf("Validate() error = %v", errValidate)
	}
	if !cfg.Enabled || !cfg.ObserveOnly || cfg.FirstEventThreshold != 20*time.Second || cfg.SlowStreak != 3 || cfg.Penalty != 45*time.Second || cfg.MaxPenalty != 6*time.Minute || cfg.EWMAAlpha != 0.3 || cfg.MinSamples != 5 || cfg.LoadFloor != 12*time.Second || cfg.StateTTL != 2*time.Hour || cfg.MaxStateEntries != 128 {
		t.Fatalf("decoded adaptive config = %#v", cfg)
	}
}

func TestAdaptiveAuthConfigExplicitDisable(t *testing.T) {
	var routing RoutingConfig
	if errDecode := yaml.Unmarshal([]byte("adaptive-auth:\n  enabled: false\n"), &routing); errDecode != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", errDecode)
	}
	if cfg := routing.AdaptiveAuth.WithDefaults(); cfg.Enabled {
		t.Fatal("explicit adaptive-auth.enabled=false must remain disabled")
	}
}

func TestAdaptiveAuthConfigRejectsInvalidValues(t *testing.T) {
	tests := []AdaptiveAuthConfig{
		{FirstEventThreshold: -time.Second},
		{SlowStreak: 1},
		{Penalty: time.Second, MaxPenalty: 500 * time.Millisecond},
		{EWMAAlpha: 1.1},
		{MinSamples: -1},
		{LoadFloor: -time.Second},
		{StateTTL: -time.Minute},
		{MaxStateEntries: -1},
	}
	for index, cfg := range tests {
		if errValidate := cfg.WithDefaults().Validate(); errValidate == nil {
			t.Errorf("case %d: Validate() error = nil, want failure", index)
		}
	}
}
