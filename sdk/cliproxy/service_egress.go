package cliproxy

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type egressRuntimeState struct {
	controller *egress.Controller
}

// configureEgressLocked initializes the active controller. Configuration
// transitions use transitionEgressLocked so incremental updates cannot replace
// the active allocation domain with a stale snapshot.
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
	if !s.egressState.controller.Equivalent(cfg) {
		return nil, fmt.Errorf("ipv6 egress controller does not match active configuration")
	}
	return s.egressState.controller, nil
}

func (s *Service) currentEgressConfigLocked() egress.Config {
	if s == nil {
		return egress.Config{}
	}
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	if s.cfg == nil {
		return egress.Config{}
	}
	return s.cfg.IPv6Egress
}

func (s *Service) activeEgressControllerLocked() (*egress.Controller, error) {
	if s.egressState != nil && s.egressState.controller != nil {
		return s.egressState.controller, nil
	}
	return s.configureEgressLocked(s.currentEgressConfigLocked())
}

func (s *Service) assignEgressIPv6(authID string) (net.IP, error) {
	if s == nil {
		return nil, nil
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return nil, fmt.Errorf("ipv6 egress auth ID is empty")
	}
	s.egressMu.Lock()
	defer s.egressMu.Unlock()
	controller, err := s.activeEgressControllerLocked()
	if err != nil {
		return nil, err
	}
	return controller.Assign(authID)
}

// prepareAuthUpdateWithEgress keeps address selection and runtime registration
// in one egress critical section. A config transition therefore runs either
// before the auth is registered or after it is visible to candidate synthesis.
func (s *Service) prepareAuthUpdateWithEgress(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	if s == nil || auth == nil || strings.TrimSpace(auth.ID) == "" {
		return nil, nil
	}
	s.egressMu.Lock()
	defer s.egressMu.Unlock()
	controller, errController := s.activeEgressControllerLocked()
	if errController != nil {
		return nil, errController
	}
	updated := auth.Clone()
	updated.EgressIPv6 = ""
	ip, errAssign := controller.Assign(updated.ID)
	if errAssign != nil {
		return nil, errAssign
	}
	if ip != nil {
		updated.EgressIPv6 = ip.String()
	}
	return s.prepareCoreAuthForModelRegistration(ctx, updated), nil
}

func (s *Service) removeAuthWithEgress(ctx context.Context, authID string) error {
	if s == nil || strings.TrimSpace(authID) == "" {
		return nil
	}
	s.egressMu.Lock()
	defer s.egressMu.Unlock()
	controller, errController := s.activeEgressControllerLocked()
	if errController != nil {
		return errController
	}
	s.applyCoreAuthRemoval(ctx, authID)
	return controller.Release(authID)
}

func sortedRuntimeAuths(auths []*coreauth.Auth) []*coreauth.Auth {
	result := make([]*coreauth.Auth, 0, len(auths))
	for _, auth := range auths {
		if auth != nil && strings.TrimSpace(auth.ID) != "" {
			result = append(result, auth)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func rollbackRuntimeEgress(ctx context.Context, manager *coreauth.Manager, originals []*coreauth.Auth) {
	if manager == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	} else {
		ctx = context.WithoutCancel(ctx)
	}
	for index := len(originals) - 1; index >= 0; index-- {
		if originals[index] != nil {
			_, _ = manager.Update(coreauth.WithSkipPersist(ctx), originals[index])
		}
	}
}

// transitionEgressLocked builds all candidate addresses and runtime bindings
// before publishing the replacement controller. Candidate failure leaves the
// previous controller, addresses, and runtime auth projection untouched.
func (s *Service) transitionEgressLocked(ctx context.Context, cfg egress.Config, commit *configCommit) error {
	if s == nil || s.coreManager == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	oldController := (*egress.Controller)(nil)
	if s.egressState != nil {
		oldController = s.egressState.controller
	}
	if oldController != nil && oldController.Equivalent(cfg) {
		return nil
	}
	candidate, errCandidate := egress.NewController(cfg)
	if errCandidate != nil {
		return errCandidate
	}
	oldManaged := map[string]net.IP{}
	if oldController != nil {
		oldManaged = oldController.ManagedAssignments()
	}
	auths := sortedRuntimeAuths(s.coreManager.List())
	targets := make(map[string]string, len(auths))
	for _, current := range auths {
		if errContext := ctx.Err(); errContext != nil {
			_ = candidate.CloseExcept(oldManaged)
			return errContext
		}
		if cfg.Enabled {
			ip, errAssign := candidate.Assign(current.ID)
			if errAssign != nil {
				_ = candidate.CloseExcept(oldManaged)
				return fmt.Errorf("assign IPv6 egress for auth %q: %w", current.ID, errAssign)
			}
			if ip != nil {
				targets[current.ID] = ip.String()
			}
		}
	}
	commitLocked := false
	if commit != nil {
		s.configUpdateMu.Lock()
		if s.configSequence != commit.sequence {
			s.configUpdateMu.Unlock()
			_ = candidate.CloseExcept(oldManaged)
			return fmt.Errorf("ipv6 egress config commit is stale")
		}
		commitLocked = true
		defer func() {
			if commitLocked {
				s.configUpdateMu.Unlock()
			}
		}()
	}
	originals := make([]*coreauth.Auth, 0, len(auths))
	for _, current := range auths {
		if errContext := ctx.Err(); errContext != nil {
			rollbackRuntimeEgress(ctx, s.coreManager, originals)
			_ = candidate.CloseExcept(oldManaged)
			return errContext
		}
		updated := current.Clone()
		updated.EgressIPv6 = targets[updated.ID]
		if updated.EgressIPv6 == current.EgressIPv6 {
			continue
		}
		if _, errUpdate := s.coreManager.Update(coreauth.WithSkipPersist(ctx), updated); errUpdate != nil {
			rollbackRuntimeEgress(ctx, s.coreManager, originals)
			_ = candidate.CloseExcept(oldManaged)
			return fmt.Errorf("update IPv6 egress for auth %q: %w", updated.ID, errUpdate)
		}
		originals = append(originals, current.Clone())
	}
	s.egressState = &egressRuntimeState{controller: candidate}
	if commitLocked {
		s.configUpdateMu.Unlock()
		commitLocked = false
	}
	if oldController != nil {
		if errClose := oldController.CloseExcept(candidate.ManagedAssignments()); errClose != nil {
			log.WithError(errClose).Warn("failed to remove retired IPv6 egress addresses")
		}
	}
	return nil
}

func (s *Service) transitionEgress(ctx context.Context, cfg egress.Config) error {
	if s == nil {
		return nil
	}
	s.egressMu.Lock()
	defer s.egressMu.Unlock()
	return s.transitionEgressLocked(ctx, cfg, nil)
}

func (s *Service) transitionEgressCommit(ctx context.Context, commit configCommit) error {
	if s == nil || commit.cfg == nil {
		return nil
	}
	s.egressMu.Lock()
	defer s.egressMu.Unlock()
	return s.transitionEgressLocked(ctx, commit.cfg.IPv6Egress, &commit)
}

func (s *Service) initializeLoadedAuthEgress(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	if cfg == nil {
		return nil
	}
	return s.transitionEgress(ctx, cfg.IPv6Egress)
}
