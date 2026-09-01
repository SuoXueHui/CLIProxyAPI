package cliproxy

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/lifecycle"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const lifecycleAutoRefreshInterval = 15 * time.Minute

type lifecycleWriteToggle interface {
	SetWritesEnabled(bool)
}

type lifecyclePluginWriteToggle interface {
	SetAuthWritesEnabled(bool)
}

func (s *Service) configureLifecycleControl() {
	if s == nil || s.lifecycleController == nil {
		return
	}
	s.lifecycleController.SetHooks(lifecycle.Hooks{
		Activate:     s.activateLifecycle,
		BeginDrain:   s.beginLifecycleDrain,
		ResumeActive: s.resumeLifecycleActive,
		Deactivate:   s.deactivateLifecycle,
	})
	s.setLifecycleWritesEnabled(s.lifecycleController.Status().CredentialWriter)
	s.lifecycleController.SetComponents(lifecycle.Components{
		WriterLeaseHeld: s.writerLease != nil,
		AutoRefresh:     false,
		IPv6Enabled:     false,
		PluginRuntime:   s.lifecycleController.Status().Mode == lifecycle.ModeActive && s.pluginHost != nil,
	})
}

func (s *Service) lifecycleActive() bool {
	if s == nil || s.lifecycleController == nil {
		return true
	}
	mode := s.lifecycleController.Status().Mode
	return mode == lifecycle.ModeActive || mode == lifecycle.ModeDraining
}

func (s *Service) setLifecycleWritesEnabled(enabled bool) {
	if s == nil {
		return
	}
	if toggle, ok := any(s.coreManager).(lifecycleWriteToggle); ok {
		toggle.SetWritesEnabled(enabled)
	}
	if toggle, ok := any(sdkAuth.GetTokenStore()).(lifecycleWriteToggle); ok {
		toggle.SetWritesEnabled(enabled)
	}
	if toggle, ok := any(s.pluginHost).(lifecyclePluginWriteToggle); ok {
		toggle.SetAuthWritesEnabled(enabled)
	}
}

func (s *Service) activateLifecycle(ctx context.Context) (lifecycle.Components, error) {
	if s == nil {
		return lifecycle.Components{}, fmt.Errorf("activate lifecycle: service is nil")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.writerLease == nil {
		lease, errLease := lifecycle.AcquireWriterLease(s.writerLeasePath)
		if errLease != nil {
			return lifecycle.Components{}, errLease
		}
		s.writerLease = lease
	}
	s.setLifecycleWritesEnabled(true)
	rollback := true
	defer func() {
		if !rollback {
			return
		}
		s.stopLifecycleBackground()
		_ = s.transitionEgress(context.Background(), egress.Config{})
		s.stopLifecyclePlugins(context.Background())
		s.setLifecycleWritesEnabled(false)
		_ = s.releaseWriterLease()
	}()
	if !s.syncPluginRuntimeConfig(ctx) {
		return lifecycle.Components{}, fmt.Errorf("activate plugin runtime during promotion")
	}
	if s.coreManager != nil {
		if errLoad := s.coreManager.Load(ctx); errLoad != nil {
			return lifecycle.Components{}, fmt.Errorf("reload auth store during promotion: %w", errLoad)
		}
	}
	if errEgress := s.initializeLoadedAuthEgress(ctx); errEgress != nil {
		return lifecycle.Components{}, fmt.Errorf("activate IPv6 egress during promotion: %w", errEgress)
	}
	if errWatcher := s.startFileWatcher(context.Background()); errWatcher != nil {
		return lifecycle.Components{}, errWatcher
	}
	if s.coreManager != nil {
		s.coreManager.StartAutoRefresh(context.Background(), lifecycleAutoRefreshInterval)
	}
	rollback = false
	s.cfgMu.RLock()
	ipv6Enabled := s.cfg != nil && s.cfg.IPv6Egress.Enabled
	pluginRuntime := s.cfg != nil && s.cfg.Plugins.Enabled
	s.cfgMu.RUnlock()
	return lifecycle.Components{WriterLeaseHeld: true, AutoRefresh: s.coreManager != nil, IPv6Enabled: ipv6Enabled, PluginRuntime: pluginRuntime}, nil
}

func (s *Service) beginLifecycleDrain() {
	if s == nil {
		return
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.stopLifecycleBackground()
}

func (s *Service) resumeLifecycleActive(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if errWatcher := s.startFileWatcher(ctx); errWatcher != nil {
		return errWatcher
	}
	if s.coreManager != nil {
		s.coreManager.StartAutoRefresh(context.Background(), lifecycleAutoRefreshInterval)
	}
	s.setLifecycleComponents(true)
	return nil
}

func (s *Service) setLifecycleComponents(autoRefresh bool) {
	if s == nil || s.lifecycleController == nil {
		return
	}
	s.cfgMu.RLock()
	ipv6Enabled := s.cfg != nil && s.cfg.IPv6Egress.Enabled
	pluginRuntime := s.cfg != nil && s.cfg.Plugins.Enabled
	s.cfgMu.RUnlock()
	s.lifecycleController.SetComponents(lifecycle.Components{
		WriterLeaseHeld: s.writerLease != nil,
		AutoRefresh:     autoRefresh,
		IPv6Enabled:     ipv6Enabled,
		PluginRuntime:   pluginRuntime,
	})
}

func (s *Service) deactivateLifecycle(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.stopLifecycleBackground()
	if errEgress := s.transitionEgress(ctx, egress.Config{}); errEgress != nil {
		return fmt.Errorf("release IPv6 egress during demotion: %w", errEgress)
	}
	s.stopLifecyclePlugins(ctx)
	s.setLifecycleWritesEnabled(false)
	return s.releaseWriterLease()
}

func (s *Service) stopLifecycleBackground() {
	if s.watcherCancel != nil {
		s.watcherCancel()
		s.watcherCancel = nil
	}
	if s.watcher != nil {
		if errStop := s.watcher.Stop(); errStop != nil {
			log.WithError(errStop).Warn("failed to stop lifecycle watcher")
		}
		s.watcher = nil
	}
	if s.authQueueStop != nil {
		s.authQueueStop()
		s.authQueueStop = nil
	}
	if s.coreManager != nil {
		s.coreManager.StopAutoRefresh()
	}
}

func (s *Service) stopLifecyclePlugins(ctx context.Context) {
	if s.pluginHost == nil {
		return
	}
	sdktranslator.SetPluginHooks(nil)
	s.pluginHost.ApplyConfig(ctx, &config.Config{})
	s.pluginHost.ShutdownAllContext(ctx)
	if s.coreManager != nil {
		s.coreManager.SetPluginScheduler(nil)
	}
}

func (s *Service) releaseWriterLease() error {
	if s.writerLease == nil {
		return nil
	}
	errRelease := s.writerLease.Release()
	s.writerLease = nil
	return errRelease
}

func (s *Service) startFileWatcher(ctx context.Context) error {
	if s == nil || s.lifecycleController == nil {
		return nil
	}
	if s.watcher != nil {
		return nil
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	if cfg == nil {
		return fmt.Errorf("start lifecycle watcher: config is unavailable")
	}
	reloadCallback := func(newCfg *config.Config) { s.applyWatcherConfigUpdate(newCfg) }
	watcherWrapper, errCreate := s.watcherFactory(s.configPath, cfg.AuthDir, reloadCallback)
	if errCreate != nil {
		return fmt.Errorf("cliproxy: failed to create watcher: %w", errCreate)
	}
	s.watcher = watcherWrapper
	s.ensureAuthUpdateQueue(context.Background())
	if s.authUpdates != nil {
		watcherWrapper.SetAuthUpdateQueue(s.authUpdates)
	}
	watcherWrapper.SetConfig(cfg)
	s.registerPluginAuthParser()
	watcherCtx, watcherCancel := context.WithCancel(context.Background())
	s.watcherCancel = watcherCancel
	if errStart := watcherWrapper.Start(watcherCtx); errStart != nil {
		watcherCancel()
		s.watcherCancel = nil
		s.watcher = nil
		return fmt.Errorf("cliproxy: failed to start watcher: %w", errStart)
	}
	log.Info("file watcher started for config and auth directory changes")
	s.syncPluginModelRuntime(ctx)
	return nil
}

func (s *Service) lifecycleCleanup() {
	if s == nil || s.lifecycleController == nil {
		return
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.setLifecycleWritesEnabled(false)
	if errEgress := s.transitionEgress(context.Background(), egress.Config{}); errEgress != nil {
		log.WithError(errEgress).Warn("failed to release lifecycle IPv6 egress during shutdown")
	}
	if errRelease := s.releaseWriterLease(); errRelease != nil {
		log.WithError(errRelease).Warn("failed to release lifecycle writer lease during shutdown")
	}
}
