// Package lifecycle coordinates process-local release lifecycle state.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Mode is the release lifecycle role of one CPA process.
type Mode string

const (
	ModeActive          Mode = "active"
	ModeStandby         Mode = "standby"
	ModeServingReadOnly Mode = "serving-readonly"
	ModeDraining        Mode = "draining"
)

var (
	ErrGenerationConflict = errors.New("lifecycle generation conflict")
	ErrInvalidTransition  = errors.New("invalid lifecycle transition")
)

// Hooks apply service-owned side effects around lifecycle transitions.
type Hooks struct {
	PrepareReadOnly func(context.Context) error
	Activate        func(context.Context) (Components, error)
	BeginDrain      func()
	ResumeActive    func(context.Context) error
	Deactivate      func(context.Context) error
}

// Status is a safe lifecycle snapshot for management and release checks.
type Status struct {
	Mode             Mode   `json:"mode"`
	Generation       uint64 `json:"generation"`
	AcceptingNew     bool   `json:"accepting_new"`
	ActiveRequests   int64  `json:"active_requests"`
	CredentialWriter bool   `json:"credential_writer"`
	WriterLeaseHeld  bool   `json:"writer_lease_held"`
	AutoRefresh      bool   `json:"auto_refresh"`
	IPv6Enabled      bool   `json:"ipv6_enabled"`
	PluginRuntime    bool   `json:"plugin_runtime"`
}

// Components reports service prerequisites that are applied outside the state machine.
type Components struct {
	WriterLeaseHeld bool
	AutoRefresh     bool
	IPv6Enabled     bool
	PluginRuntime   bool
}

// Controller serializes role changes and tracks admitted proxy requests.
type Controller struct {
	transitionMu sync.Mutex
	mu           sync.Mutex
	changed      chan struct{}
	mode         Mode
	generation   uint64
	active       int64
	hooks        Hooks
	components   Components
}

func (c *Controller) SetComponents(components Components) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.components = components
	c.signalLocked()
	c.mu.Unlock()
}

// NewController builds a lifecycle controller with a validated initial role.
func NewController(mode Mode) *Controller {
	if !mode.Valid() {
		mode = ModeActive
	}
	return &Controller{mode: mode, generation: 1, changed: make(chan struct{})}
}

func ParseMode(raw string) (Mode, error) {
	mode := Mode(strings.ToLower(strings.TrimSpace(raw)))
	if mode == "" {
		mode = ModeActive
	}
	if !mode.Valid() {
		return "", fmt.Errorf("invalid lifecycle mode %q", raw)
	}
	return mode, nil
}

func (m Mode) Valid() bool {
	switch m {
	case ModeActive, ModeStandby, ModeServingReadOnly, ModeDraining:
		return true
	default:
		return false
	}
}

func (c *Controller) SetHooks(hooks Hooks) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.hooks = hooks
	c.mu.Unlock()
}

func (c *Controller) Status() Status {
	if c == nil {
		return Status{Mode: ModeActive, Generation: 1, AcceptingNew: true, CredentialWriter: true, WriterLeaseHeld: true, AutoRefresh: true}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.statusLocked()
}

// AllowsWrites reports whether the process currently owns credential writes.
func (c *Controller) AllowsWrites() bool {
	status := c.Status()
	return status.CredentialWriter
}

func (c *Controller) statusLocked() Status {
	return Status{
		Mode:             c.mode,
		Generation:       c.generation,
		AcceptingNew:     c.mode == ModeActive || c.mode == ModeServingReadOnly,
		ActiveRequests:   c.active,
		CredentialWriter: c.mode == ModeActive || c.mode == ModeDraining,
		WriterLeaseHeld:  c.components.WriterLeaseHeld,
		AutoRefresh:      c.components.AutoRefresh,
		IPv6Enabled:      c.components.IPv6Enabled,
		PluginRuntime:    c.components.PluginRuntime,
	}
}

// AdmitProxy reserves one proxy request when the current role accepts new work.
func (c *Controller) AdmitProxy() (func(), bool) {
	if c == nil {
		return func() {}, true
	}
	c.mu.Lock()
	if c.mode != ModeActive && c.mode != ModeServingReadOnly {
		c.mu.Unlock()
		return nil, false
	}
	c.active++
	c.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			if c.active > 0 {
				c.active--
			}
			c.signalLocked()
			c.mu.Unlock()
		})
	}, true
}

func (c *Controller) signalLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}

// Transition moves to target after applying the required service hooks.
func (c *Controller) Transition(ctx context.Context, target Mode, expectedGeneration uint64) (Status, error) {
	if c == nil {
		return Status{}, errors.New("lifecycle controller is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !target.Valid() {
		return c.Status(), fmt.Errorf("%w: target %q", ErrInvalidTransition, target)
	}
	c.transitionMu.Lock()
	defer c.transitionMu.Unlock()
	for {
		c.mu.Lock()
		if expectedGeneration != 0 && expectedGeneration != c.generation {
			status := c.statusLocked()
			c.mu.Unlock()
			return status, ErrGenerationConflict
		}
		if target == c.mode {
			status := c.statusLocked()
			c.mu.Unlock()
			return status, nil
		}
		current := c.mode
		hooks := c.hooks
		if !validTransition(current, target) {
			status := c.statusLocked()
			c.mu.Unlock()
			return status, fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, current, target)
		}
		if (current == ModeDraining || current == ModeServingReadOnly) && target == ModeStandby && c.active > 0 {
			changed := c.changed
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return c.Status(), ctx.Err()
			case <-changed:
				continue
			}
		}
		// These transitions do not have fallible preparation. Commit them while
		// holding the state lock so request admission changes atomically.
		if current == ModeServingReadOnly && target == ModeStandby {
			c.mode = target
			c.generation++
			c.signalLocked()
			status := c.statusLocked()
			c.mu.Unlock()
			return status, nil
		}
		if current == ModeActive && target == ModeDraining {
			c.mode = target
			c.generation++
			c.components.AutoRefresh = false
			c.signalLocked()
			c.mu.Unlock()
			if hooks.BeginDrain != nil {
				hooks.BeginDrain()
			}
			return c.Status(), nil
		}
		c.mu.Unlock()

		var components Components
		var errHook error
		switch {
		case current == ModeStandby && target == ModeServingReadOnly:
			if hooks.PrepareReadOnly != nil {
				errHook = hooks.PrepareReadOnly(ctx)
			}
		case current == ModeServingReadOnly && target == ModeActive:
			if hooks.Activate != nil {
				components, errHook = hooks.Activate(ctx)
			}
		case current == ModeDraining && target == ModeActive:
			if hooks.ResumeActive != nil {
				errHook = hooks.ResumeActive(ctx)
			}
		case current == ModeDraining && target == ModeStandby:
			if hooks.Deactivate != nil {
				errHook = hooks.Deactivate(ctx)
			}
		}
		if errHook != nil {
			return c.Status(), errHook
		}

		c.mu.Lock()
		switch {
		case current == ModeServingReadOnly && target == ModeActive:
			c.components = components
		case current == ModeDraining && target == ModeActive:
			c.components.AutoRefresh = true
		case current == ModeDraining && target == ModeStandby:
			c.components = Components{}
		}
		c.mode = target
		c.generation++
		c.signalLocked()
		status := c.statusLocked()
		c.mu.Unlock()
		return status, nil
	}
}

func validTransition(current, target Mode) bool {
	switch current {
	case ModeStandby:
		return target == ModeServingReadOnly
	case ModeServingReadOnly:
		return target == ModeStandby || target == ModeActive
	case ModeActive:
		return target == ModeDraining
	case ModeDraining:
		return target == ModeStandby || target == ModeActive
	default:
		return false
	}
}
