package cliproxy

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type egressRuntimeState struct {
	controller *egress.Controller
}

// configureEgressLocked validates and reconciles process-owned addresses.
// Callers must hold egressMu so no incremental assignment can race a prefix,
// mode, interface, or enabled-state transition.
func (s *Service) configureEgressLocked(cfg egress.Config) (*egress.Controller, error) {
	if s.egressState == nil {
		controller, err := egress.NewController(cfg)
		if err != nil {
			return nil, err
		}
		s.egressState = &egressRuntimeState{controller: controller}
		return controller, nil
	}
	if s.egressState.controller == nil {
		controller, err := egress.NewController(cfg)
		if err != nil {
			return nil, err
		}
		s.egressState.controller = controller
		return controller, nil
	}
	if err := s.egressState.controller.Configure(cfg); err != nil {
		return nil, err
	}
	return s.egressState.controller, nil
}

func (s *Service) configureEgress(cfg egress.Config) error {
	if s == nil {
		return nil
	}
	s.egressMu.Lock()
	defer s.egressMu.Unlock()
	_, err := s.configureEgressLocked(cfg)
	return err
}

func (s *Service) assignEgressIPv6(cfg egress.Config, authID string) (net.IP, error) {
	if s == nil {
		return nil, nil
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return nil, fmt.Errorf("ipv6 egress auth ID is empty")
	}
	s.egressMu.Lock()
	defer s.egressMu.Unlock()
	controller, err := s.configureEgressLocked(cfg)
	if err != nil {
		return nil, err
	}
	return controller.Assign(authID)
}

func (s *Service) releaseEgressIPv6(cfg egress.Config, authID string) error {
	if s == nil {
		return nil
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return nil
	}
	s.egressMu.Lock()
	defer s.egressMu.Unlock()
	controller, err := s.configureEgressLocked(cfg)
	if err != nil {
		return err
	}
	return controller.Release(authID)
}

// reconcileRuntimeAuthEgress makes the runtime auth projection match the
// addresses owned after a config transition. This clears stale source bindings
// when disabled and immediately reassigns active auths after a prefix change.
func (s *Service) reconcileRuntimeAuthEgress(ctx context.Context, cfg egress.Config) error {
	if s == nil || s.coreManager == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for _, current := range s.coreManager.List() {
		if current == nil || strings.TrimSpace(current.ID) == "" {
			continue
		}
		updated := current.Clone()
		updated.EgressIPv6 = ""
		if cfg.Enabled {
			ip, errAssign := s.assignEgressIPv6(cfg, updated.ID)
			if errAssign != nil {
				return fmt.Errorf("assign IPv6 egress for auth %q: %w", updated.ID, errAssign)
			}
			if ip != nil {
				updated.EgressIPv6 = ip.String()
			}
		}
		if updated.EgressIPv6 == current.EgressIPv6 {
			continue
		}
		if _, errUpdate := s.coreManager.Update(coreauth.WithSkipPersist(ctx), updated); errUpdate != nil {
			return fmt.Errorf("update IPv6 egress for auth %q: %w", updated.ID, errUpdate)
		}
	}
	return nil
}
