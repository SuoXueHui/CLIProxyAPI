package cliproxy

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	xaiOAuthModelDiscoveryInterval = 15 * time.Minute
	xaiOAuthModelDiscoveryTimeout  = 30 * time.Second
	xaiOAuthModelDiscoveryWorkers  = 5
)

var errXAIDiscoveryCredentialChanged = errors.New("xai model discovery: credential changed during refresh")

type xaiOAuthModelFetcher func(context.Context, *coreauth.Auth) ([]*ModelInfo, error)

type xaiOAuthModelSnapshot struct {
	credentialFingerprint [sha256.Size]byte
	models                []*ModelInfo
}

// xaiOAuthModelDiscovery owns process-local, per-auth upstream model snapshots.
// Static catalog entries remain outside this cache and provide metadata or fallback.
type xaiOAuthModelDiscovery struct {
	mu           sync.RWMutex
	modelsByAuth map[string]xaiOAuthModelSnapshot
	fetch        xaiOAuthModelFetcher
	onChanged    func(*coreauth.Auth)
	isCurrent    func(*coreauth.Auth) bool
	fetchTimeout time.Duration
}

func newXAIOAuthModelDiscovery(fetch xaiOAuthModelFetcher, onChanged func(*coreauth.Auth)) *xaiOAuthModelDiscovery {
	return &xaiOAuthModelDiscovery{
		modelsByAuth: make(map[string]xaiOAuthModelSnapshot),
		fetch:        fetch,
		onChanged:    onChanged,
		fetchTimeout: xaiOAuthModelDiscoveryTimeout,
	}
}

func (s *Service) initializeXAIOAuthModelDiscovery() {
	if s == nil {
		return
	}
	discovery := newXAIOAuthModelDiscovery(
		func(ctx context.Context, auth *coreauth.Auth) ([]*ModelInfo, error) {
			s.cfgMu.RLock()
			cfg := s.cfg
			s.cfgMu.RUnlock()
			return executor.NewXAIExecutor(cfg).ListModels(ctx, auth)
		},
		func(auth *coreauth.Auth) {
			current, ok := s.coreManager.GetByID(auth.ID)
			if !ok || !sameXAIDiscoveryCredential(auth, current) {
				return
			}
			if s.refreshModelRegistrationForAuth(current) {
				log.WithField("auth_id", auth.ID).Info("xai OAuth model discovery updated model registration")
			}
		},
	)
	discovery.isCurrent = func(auth *coreauth.Auth) bool {
		current, ok := s.coreManager.GetByID(auth.ID)
		return ok && sameXAIDiscoveryCredential(auth, current)
	}
	s.xaiModelDiscovery = discovery
}

func (s *Service) startXAIOAuthModelDiscovery(ctx context.Context) {
	if s == nil || s.xaiModelDiscovery == nil || s.coreManager == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.xaiModelDiscoveryMu.Lock()
	if s.xaiModelDiscoveryCancel != nil {
		s.xaiModelDiscoveryMu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.xaiModelDiscoveryCancel = cancel
	s.xaiModelDiscoveryDone = done
	s.xaiModelDiscoveryMu.Unlock()

	go func() {
		defer close(done)
		s.xaiModelDiscovery.run(runCtx, s.coreManager.List)
		s.xaiModelDiscoveryMu.Lock()
		if s.xaiModelDiscoveryDone == done {
			s.xaiModelDiscoveryCancel = nil
			s.xaiModelDiscoveryDone = nil
		}
		s.xaiModelDiscoveryMu.Unlock()
	}()
}

func (s *Service) stopXAIOAuthModelDiscovery() {
	if s == nil {
		return
	}
	s.xaiModelDiscoveryMu.Lock()
	cancel := s.xaiModelDiscoveryCancel
	done := s.xaiModelDiscoveryDone
	s.xaiModelDiscoveryCancel = nil
	s.xaiModelDiscoveryDone = nil
	s.xaiModelDiscoveryMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done != nil {
		<-done
	}
}

func (d *xaiOAuthModelDiscovery) modelsForAuth(auth *coreauth.Auth) ([]*ModelInfo, bool) {
	if d == nil || auth == nil || strings.TrimSpace(auth.ID) == "" {
		return nil, false
	}
	fingerprint := xaiDiscoveryCredentialFingerprint(auth)
	d.mu.RLock()
	snapshot, ok := d.modelsByAuth[auth.ID]
	if ok && snapshot.credentialFingerprint != fingerprint {
		ok = false
	}
	var models []*ModelInfo
	if ok {
		models = cloneXAIOAuthModels(snapshot.models)
	}
	d.mu.RUnlock()
	return models, ok
}

func (d *xaiOAuthModelDiscovery) forgetAuth(authID string) {
	if d == nil || strings.TrimSpace(authID) == "" {
		return
	}
	d.mu.Lock()
	delete(d.modelsByAuth, authID)
	d.mu.Unlock()
}

func (d *xaiOAuthModelDiscovery) refreshAuth(ctx context.Context, auth *coreauth.Auth) (bool, error) {
	if d == nil || d.fetch == nil || auth == nil || strings.TrimSpace(auth.ID) == "" {
		return false, fmt.Errorf("xai model discovery: auth and fetcher are required")
	}
	auth = auth.Clone()
	// Model discovery is credential capability acquisition rather than inference,
	// so it is deliberately bounded to prevent one auth from freezing all cycles.
	fetchCtx := ctx
	cancel := func() {}
	if d.fetchTimeout > 0 {
		fetchCtx, cancel = context.WithTimeout(ctx, d.fetchTimeout)
	}
	defer cancel()
	models, errFetch := d.fetch(fetchCtx, auth)
	if errFetch != nil {
		return false, errFetch
	}
	models = normalizeXAIOAuthModels(models)
	if len(models) == 0 {
		return false, fmt.Errorf("xai model discovery: upstream returned no usable models")
	}
	if d.isCurrent != nil && !d.isCurrent(auth) {
		return false, errXAIDiscoveryCredentialChanged
	}
	fingerprint := xaiDiscoveryCredentialFingerprint(auth)

	d.mu.Lock()
	previous, exists := d.modelsByAuth[auth.ID]
	if exists && previous.credentialFingerprint == fingerprint && reflect.DeepEqual(previous.models, models) {
		d.mu.Unlock()
		return false, nil
	}
	d.modelsByAuth[auth.ID] = xaiOAuthModelSnapshot{
		credentialFingerprint: fingerprint,
		models:                cloneXAIOAuthModels(models),
	}
	d.mu.Unlock()

	if d.onChanged != nil {
		d.onChanged(auth.Clone())
	}
	return true, nil
}

func (d *xaiOAuthModelDiscovery) run(ctx context.Context, listAuths func() []*coreauth.Auth) {
	if d == nil || listAuths == nil {
		return
	}
	d.refreshEligibleAuths(ctx, listAuths())
	ticker := time.NewTicker(xaiOAuthModelDiscoveryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.refreshEligibleAuths(ctx, listAuths())
		}
	}
}

func (d *xaiOAuthModelDiscovery) refreshEligibleAuths(ctx context.Context, auths []*coreauth.Auth) {
	eligible := make([]*coreauth.Auth, 0, len(auths))
	for _, auth := range auths {
		if isXAIOAuthModelDiscoveryEligible(auth) {
			eligible = append(eligible, auth.Clone())
		}
	}
	if len(eligible) == 0 {
		return
	}

	workers := xaiOAuthModelDiscoveryWorkers
	if workers > len(eligible) {
		workers = len(eligible)
	}
	tasks := make(chan *coreauth.Auth)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for auth := range tasks {
				if ctx.Err() != nil {
					return
				}
				if _, errRefresh := d.refreshAuth(ctx, auth); errRefresh != nil && ctx.Err() == nil {
					log.WithError(errRefresh).WithField("auth_id", auth.ID).Debug("xai OAuth model discovery refresh failed; keeping the last successful snapshot")
				}
			}
		}()
	}
	for _, auth := range eligible {
		select {
		case <-ctx.Done():
			close(tasks)
			wg.Wait()
			return
		case tasks <- auth:
		}
	}
	close(tasks)
	wg.Wait()
}

func isXAIOAuthModelDiscoveryEligible(auth *coreauth.Auth) bool {
	return auth != nil && auth.ID != "" && !auth.Disabled && strings.EqualFold(strings.TrimSpace(auth.Provider), "xai") && auth.AuthKind() == coreauth.AuthKindOAuth
}

// resolveXAIOAuthModels treats a successful chat discovery as authoritative.
// Static media models use separate upstream APIs and remain available.
func resolveXAIOAuthModels(staticModels, discoveredModels []*ModelInfo, discovered bool) []*ModelInfo {
	if !discovered {
		return cloneXAIOAuthModels(staticModels)
	}
	staticByID := make(map[string]*ModelInfo, len(staticModels))
	for _, model := range staticModels {
		if model != nil && strings.TrimSpace(model.ID) != "" {
			staticByID[model.ID] = model
		}
	}
	resolved := make([]*ModelInfo, 0, len(discoveredModels)+2)
	seen := make(map[string]struct{}, len(discoveredModels)+2)
	for _, model := range discoveredModels {
		if model == nil || strings.TrimSpace(model.ID) == "" {
			continue
		}
		if _, exists := seen[model.ID]; exists {
			continue
		}
		seen[model.ID] = struct{}{}
		if staticModel := staticByID[model.ID]; staticModel != nil {
			resolved = append(resolved, cloneXAIOAuthModel(staticModel))
			continue
		}
		resolved = append(resolved, cloneXAIOAuthModel(model))
	}
	for _, model := range staticModels {
		if model == nil || !isXAIStaticMediaModel(model.ID) {
			continue
		}
		if _, exists := seen[model.ID]; exists {
			continue
		}
		seen[model.ID] = struct{}{}
		resolved = append(resolved, cloneXAIOAuthModel(model))
	}
	return resolved
}

func isXAIStaticMediaModel(modelID string) bool {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	return strings.HasPrefix(modelID, "grok-imagine-image") || strings.HasPrefix(modelID, "grok-imagine-video")
}

func sameXAIDiscoveryCredential(left, right *coreauth.Auth) bool {
	if left == nil || right == nil {
		return false
	}
	return xaiDiscoveryCredentialFingerprint(left) == xaiDiscoveryCredentialFingerprint(right)
}

// xaiDiscoveryCredentialFingerprint identifies the logical account without
// invalidating snapshots for routine access-token rotation.
func xaiDiscoveryCredentialFingerprint(auth *coreauth.Auth) [sha256.Size]byte {
	if auth == nil {
		return [sha256.Size]byte{}
	}
	metadataString := func(key string) string {
		if auth.Metadata == nil {
			return ""
		}
		value, _ := auth.Metadata[key].(string)
		return strings.TrimSpace(value)
	}
	attribute := func(key string) string {
		if auth.Attributes == nil {
			return ""
		}
		return strings.TrimSpace(auth.Attributes[key])
	}
	identity := strings.Join([]string{metadataString("sub"), metadataString("email")}, "\x00")
	if strings.Trim(identity, "\x00") == "" {
		identity = metadataString("access_token")
	}
	fields := []string{
		strings.TrimSpace(auth.ID),
		strings.ToLower(strings.TrimSpace(auth.Provider)),
		strings.TrimSpace(auth.FileName),
		strings.TrimSpace(auth.Index),
		auth.AuthKind(),
		strings.TrimSpace(auth.ProxyURL),
		strings.TrimSpace(auth.EgressIPv6),
		attribute("base_url"),
		metadataString("base_url"),
		identity,
	}
	return sha256.Sum256([]byte(strings.Join(fields, "\x00")))
}

func normalizeXAIOAuthModels(models []*ModelInfo) []*ModelInfo {
	byID := make(map[string]*ModelInfo, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" {
			continue
		}
		clone := cloneXAIOAuthModel(model)
		clone.ID = modelID
		if clone.Object == "" {
			clone.Object = "model"
		}
		if clone.OwnedBy == "" {
			clone.OwnedBy = "xai"
		}
		if clone.Type == "" {
			clone.Type = "xai"
		}
		if clone.Name == "" {
			clone.Name = modelID
		}
		if clone.DisplayName == "" {
			clone.DisplayName = modelID
		}
		if _, exists := byID[modelID]; !exists {
			byID[modelID] = clone
		}
	}
	ids := make([]string, 0, len(byID))
	for modelID := range byID {
		ids = append(ids, modelID)
	}
	sort.Strings(ids)
	out := make([]*ModelInfo, 0, len(ids))
	for _, modelID := range ids {
		out = append(out, byID[modelID])
	}
	return out
}

func cloneXAIOAuthModels(models []*ModelInfo) []*ModelInfo {
	if len(models) == 0 {
		return nil
	}
	out := make([]*ModelInfo, 0, len(models))
	for _, model := range models {
		if model != nil {
			out = append(out, cloneXAIOAuthModel(model))
		}
	}
	return out
}

func cloneXAIOAuthModel(model *ModelInfo) *ModelInfo {
	if model == nil {
		return nil
	}
	clone := *model
	clone.SupportedGenerationMethods = append([]string(nil), model.SupportedGenerationMethods...)
	clone.SupportedParameters = append([]string(nil), model.SupportedParameters...)
	clone.SupportedInputModalities = append([]string(nil), model.SupportedInputModalities...)
	clone.SupportedOutputModalities = append([]string(nil), model.SupportedOutputModalities...)
	if model.Thinking != nil {
		thinking := *model.Thinking
		thinking.Levels = append([]string(nil), model.Thinking.Levels...)
		clone.Thinking = &thinking
	}
	if model.Config != nil {
		modelConfig := *model.Config
		if model.Config.OverrideHeader != nil {
			modelConfig.OverrideHeader = make(map[string]string, len(model.Config.OverrideHeader))
			for key, value := range model.Config.OverrideHeader {
				modelConfig.OverrideHeader[key] = value
			}
		}
		clone.Config = &modelConfig
	}
	return &clone
}
