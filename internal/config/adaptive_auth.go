package config

import (
	"fmt"
	"time"
)

const (
	defaultAdaptiveFirstEventThreshold = 15 * time.Second
	defaultAdaptiveSlowStreak          = 2
	defaultAdaptivePenalty             = 30 * time.Second
	defaultAdaptiveMaxPenalty          = 5 * time.Minute
	defaultAdaptiveEWMAAlpha           = 0.2
	defaultAdaptiveMinSamples          = 3
	defaultAdaptiveLoadFloor           = 10 * time.Second
	defaultAdaptiveStateTTL            = 6 * time.Hour
	defaultAdaptiveMaxStateEntries     = 4096
)

// AdaptiveAuthConfig controls in-memory slow-auth avoidance and fair scheduling.
// It deliberately has no hard concurrency limit, so enabling it cannot reduce
// the total request concurrency accepted by the proxy.
type AdaptiveAuthConfig struct {
	Enabled             bool          `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	ObserveOnly         bool          `yaml:"observe-only,omitempty" json:"observe-only,omitempty"`
	FirstEventThreshold time.Duration `yaml:"first-event-threshold,omitempty" json:"first-event-threshold,omitempty"`
	SlowStreak          int           `yaml:"slow-streak,omitempty" json:"slow-streak,omitempty"`
	Penalty             time.Duration `yaml:"penalty,omitempty" json:"penalty,omitempty"`
	MaxPenalty          time.Duration `yaml:"max-penalty,omitempty" json:"max-penalty,omitempty"`
	EWMAAlpha           float64       `yaml:"ewma-alpha,omitempty" json:"ewma-alpha,omitempty"`
	MinSamples          int           `yaml:"min-samples,omitempty" json:"min-samples,omitempty"`
	LoadFloor           time.Duration `yaml:"load-floor,omitempty" json:"load-floor,omitempty"`
	StateTTL            time.Duration `yaml:"state-ttl,omitempty" json:"state-ttl,omitempty"`
	MaxStateEntries     int           `yaml:"max-state-entries,omitempty" json:"max-state-entries,omitempty"`
}

// WithDefaults applies conservative defaults while preserving explicit values.
func (c AdaptiveAuthConfig) WithDefaults() AdaptiveAuthConfig {
	if c.FirstEventThreshold == 0 {
		c.FirstEventThreshold = defaultAdaptiveFirstEventThreshold
	}
	if c.SlowStreak == 0 {
		c.SlowStreak = defaultAdaptiveSlowStreak
	}
	if c.Penalty == 0 {
		c.Penalty = defaultAdaptivePenalty
	}
	if c.MaxPenalty == 0 {
		c.MaxPenalty = defaultAdaptiveMaxPenalty
	}
	if c.EWMAAlpha == 0 {
		c.EWMAAlpha = defaultAdaptiveEWMAAlpha
	}
	if c.MinSamples == 0 {
		c.MinSamples = defaultAdaptiveMinSamples
	}
	if c.LoadFloor == 0 {
		c.LoadFloor = defaultAdaptiveLoadFloor
	}
	if c.StateTTL == 0 {
		c.StateTTL = defaultAdaptiveStateTTL
	}
	if c.MaxStateEntries == 0 {
		c.MaxStateEntries = defaultAdaptiveMaxStateEntries
	}
	return c
}

// Validate checks adaptive scheduling bounds before a config snapshot is applied.
func (c AdaptiveAuthConfig) Validate() error {
	if c.FirstEventThreshold <= 0 {
		return fmt.Errorf("adaptive auth first-event threshold must be positive")
	}
	if c.SlowStreak < 2 {
		return fmt.Errorf("adaptive auth slow streak must be at least 2")
	}
	if c.Penalty <= 0 {
		return fmt.Errorf("adaptive auth penalty must be positive")
	}
	if c.MaxPenalty < c.Penalty {
		return fmt.Errorf("adaptive auth max penalty must be greater than or equal to penalty")
	}
	if c.EWMAAlpha <= 0 || c.EWMAAlpha > 1 {
		return fmt.Errorf("adaptive auth EWMA alpha must be greater than 0 and at most 1")
	}
	if c.MinSamples < 1 {
		return fmt.Errorf("adaptive auth minimum samples must be positive")
	}
	if c.LoadFloor <= 0 {
		return fmt.Errorf("adaptive auth load floor must be positive")
	}
	if c.StateTTL <= 0 {
		return fmt.Errorf("adaptive auth state TTL must be positive")
	}
	if c.MaxStateEntries < 1 {
		return fmt.Errorf("adaptive auth maximum state entries must be positive")
	}
	return nil
}
