package cliproxy

import (
	"context"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/egress"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/lifecycle"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const lifecycleAutoRefreshInterval = 15 * time.Minute

type lifecycleWriteToggle interface {
	SetWritesEnabled(bool)
}

type lifecycleWriteStatus interface {
	WritesEnabled() bool
}

type lifecyclePluginWriteToggle interface {
	SetAuthWritesEnabled(bool)
}

func (s *Service) configureLifecycleControl() {
	if s == nil || s.lifecycleController == nil {
		return
	}
	s.lifecycleController.SetHooks(lifecycle.Hooks{
		PrepareReadOnly: s.prepareLifecycleReadOnly,
		Activate:        s.activateLifecycle,
		BeginDrain:      s.beginLifecycleDrain,
		ResumeActive:    s.resumeLifecycleActive,
		Deactivate:      s.deactivateLifecycle,
	})
	s.setLifecycleWritesEnabled(s.lifecycleController.Status().CredentialWriter)
	s.lifecycleController.SetComponents(s.snapshotLifecycleComponents(false))
}

// prepareLifecycleReadOnly loads current credentials before proxy admission is enabled.
// Writer gates remain closed and no background refresh, plugin, watcher, or IPv6 work starts.
func (s *Service) prepareLifecycleReadOnly(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.setLifecycleWritesEnabled(false)
	return s.reloadLifecycleAuthState(ctx)
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
	return s.activateLifecycleLocked(ctx)
}

func (s *Service) activateLifecycleLocked(ctx context.Context) (components lifecycle.Components, err error) {
	if s.writerLease == nil {
		lease, errLease := lifecycle.AcquireWriterLease(s.writerLeasePath)
		if errLease != nil {
			return lifecycle.Components{}, errLease
		}
		s.writerLease = lease
	}
	// Keep write gates closed until the latest auth snapshot and all runtime
	// components are ready. Read-only requests are drained before this hook runs.
	s.setLifecycleWritesEnabled(false)
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
		components = s.snapshotLifecycleComponents(false)
	}()
	if !s.syncPluginRuntimeConfig(ctx) {
		return lifecycle.Components{}, fmt.Errorf("activate plugin runtime during promotion")
	}
	if _, errPlugin := s.lifecyclePluginRuntimeState(); errPlugin != nil {
		return lifecycle.Components{}, errPlugin
	}
	if errLoad := s.reloadLifecycleAuthState(ctx); errLoad != nil {
		return lifecycle.Components{}, fmt.Errorf("reload auth store during promotion: %w", errLoad)
	}
	if errEgress := s.initializeLoadedAuthEgress(ctx); errEgress != nil {
		return lifecycle.Components{}, fmt.Errorf("activate IPv6 egress during promotion: %w", errEgress)
	}
	if errWatcher := s.startFileWatcher(context.Background()); errWatcher != nil {
		return lifecycle.Components{}, errWatcher
	}
	s.setLifecycleWritesEnabled(true)
	if s.coreManager != nil {
		s.coreManager.StartAutoRefresh(context.Background(), lifecycleAutoRefreshInterval)
	}
	rollback = false
	return s.snapshotLifecycleComponents(s.coreManager != nil), nil
}

func (s *Service) reloadLifecycleAuthState(ctx context.Context) error {
	if s == nil || s.coreManager == nil {
		return nil
	}
	if errLoad := s.coreManager.Load(ctx); errLoad != nil {
		return fmt.Errorf("load auth store: %w", errLoad)
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	if cfg != nil {
		// Read-only preparation must not allocate account IPv6 addresses. Active
		// promotion initializes egress after the previous owner has stopped.
		s.registerConfigAPIKeyAuthsWithEgress(coreauth.WithSkipPersist(ctx), cfg, false)
	}
	auths := s.coreManager.List()
	s.registerAvailableExecutors(ctx, executorRegistrationOptions{auths: auths})
	s.registerModelsForAuthBatch(ctx, auths)
	if errContext := ctx.Err(); errContext != nil {
		return fmt.Errorf("register lifecycle auth runtime: %w", errContext)
	}
	return nil
}

func (s *Service) beginLifecycleDrain() {
	if s == nil {
		return
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.stopLifecycleBackground()
}

func (s *Service) resumeLifecycleActive(ctx context.Context) (lifecycle.Components, error) {
	if s == nil {
		return lifecycle.Components{}, nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	return s.activateLifecycleLocked(ctx)
}

func (s *Service) setLifecycleComponents(autoRefresh bool) {
	if s == nil || s.lifecycleController == nil {
		return
	}
	s.lifecycleController.SetComponents(s.snapshotLifecycleComponents(autoRefresh))
}

func (s *Service) snapshotLifecycleComponents(autoRefresh bool) lifecycle.Components {
	if s == nil {
		return lifecycle.Components{}
	}
	writesEnabled := false
	if status, ok := any(s.coreManager).(lifecycleWriteStatus); ok {
		writesEnabled = status.WritesEnabled()
	}
	pluginRuntime, _ := s.lifecyclePluginRuntimeState()
	return lifecycle.Components{
		CredentialWriter: writesEnabled,
		WriterLeaseHeld:  s.writerLease != nil,
		AutoRefresh:      autoRefresh,
		IPv6Enabled:      s.lifecycleEgressEnabled(),
		PluginRuntime:    pluginRuntime,
	}
}

func (s *Service) lifecycleEgressEnabled() bool {
	if s == nil {
		return false
	}
	s.egressMu.Lock()
	defer s.egressMu.Unlock()
	if s.egressState == nil || s.egressState.controller == nil {
		return false
	}
	cfg := s.currentEgressConfigLocked()
	return cfg.Enabled && s.egressState.controller.Equivalent(cfg)
}

func (s *Service) lifecyclePluginRuntimeState() (bool, error) {
	if s == nil {
		return false, nil
	}
	s.cfgMu.RLock()
	cfg := s.cfg
	s.cfgMu.RUnlock()
	if cfg == nil || !cfg.Plugins.Enabled {
		return false, nil
	}
	expected := 0
	for id, item := range cfg.Plugins.Configs {
		if item.Enabled == nil || !*item.Enabled {
			continue
		}
		expected++
		if s.pluginHost == nil || !s.pluginHost.PluginRegistered(id) {
			return false, fmt.Errorf("activate plugin runtime: required plugin %q is not registered", id)
		}
	}
	return expected > 0, nil
}

func (s *Service) deactivateLifecycle(ctx context.Context) (lifecycle.Components, error) {
	if s == nil {
		return lifecycle.Components{}, nil
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	// Close every credential write gate before tearing down any dependency that
	// an in-flight refresh or persistence operation could still reference.
	s.setLifecycleWritesEnabled(false)
	s.stopLifecycleBackground()
	if errEgress := s.transitionEgress(ctx, egress.Config{}); errEgress != nil {
		return s.snapshotLifecycleComponents(false), fmt.Errorf("release IPv6 egress during demotion: %w", errEgress)
	}
	s.stopLifecyclePlugins(ctx)
	if errRelease := s.releaseWriterLease(); errRelease != nil {
		return s.snapshotLifecycleComponents(false), errRelease
	}
	return s.snapshotLifecycleComponents(false), nil
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
