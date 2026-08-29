package config

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestAdaptiveAuthConfigWithDefaults(t *testing.T) {
	cfg := (AdaptiveAuthConfig{}).WithDefaults()
	if cfg.Enabled {
		t.Fatal("adaptive auth scheduling must remain opt-in by default")
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
