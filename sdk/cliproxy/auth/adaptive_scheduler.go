package auth

import (
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type adaptiveAuthKey struct {
	provider string
	model    string
	authID   string
}

type adaptiveLoadKey struct {
	provider string
	authID   string
}

type adaptiveAuthStats struct {
	firstEventEWMA float64
	durationEWMA   float64
	sampleCount    int
	slowStreak     int
	penaltyLevel   int
	penaltyUntil   time.Time
	lastSeen       time.Time
}

// adaptiveSchedulerRuntime stores volatile health and load state. It is kept
// separate from Auth so auth files and persisted cooldown records remain stable.
type adaptiveSchedulerRuntime struct {
	config internalconfig.AdaptiveAuthConfig
	health map[adaptiveAuthKey]*adaptiveAuthStats
	load   map[adaptiveLoadKey]int
	last   map[string]string
	weight map[string]*smoothWeightedState
	mu     sync.Mutex
}

type adaptiveAuthLease struct {
	scheduler *authScheduler
	key       adaptiveLoadKey
	authKey   adaptiveAuthKey
	once      sync.Once
}

func newAdaptiveSchedulerRuntime() adaptiveSchedulerRuntime {
	return adaptiveSchedulerRuntime{
		config: internalconfig.DefaultAdaptiveAuthConfig(),
		health: make(map[adaptiveAuthKey]*adaptiveAuthStats),
		load:   make(map[adaptiveLoadKey]int),
		last:   make(map[string]string),
		weight: make(map[string]*smoothWeightedState),
	}
}

func normalizeAdaptiveKey(provider, model, authID string) (adaptiveAuthKey, adaptiveLoadKey, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	authID = strings.TrimSpace(authID)
	if provider == "" || authID == "" {
		return adaptiveAuthKey{}, adaptiveLoadKey{}, false
	}
	modelKey := canonicalModelKey(model)
	return adaptiveAuthKey{provider: provider, model: modelKey, authID: authID}, adaptiveLoadKey{provider: provider, authID: authID}, true
}

func (s *authScheduler) setAdaptiveConfig(cfg internalconfig.AdaptiveAuthConfig) {
	if s == nil {
		return
	}
	cfg = cfg.WithDefaults()
	if errValidate := cfg.Validate(); errValidate != nil {
		return
	}
	s.mu.Lock()
	s.adaptive.mu.Lock()
	s.adaptive.config = cfg
	if !cfg.Enabled {
		clear(s.adaptive.health)
		clear(s.adaptive.load)
		clear(s.adaptive.last)
		clear(s.adaptive.weight)
	}
	s.adaptive.mu.Unlock()
	s.mu.Unlock()
}

func (s *authScheduler) adaptiveConfig() internalconfig.AdaptiveAuthConfig {
	if s == nil {
		return internalconfig.DefaultAdaptiveAuthConfig()
	}
	s.adaptive.mu.Lock()
	defer s.adaptive.mu.Unlock()
	return s.adaptive.config
}

func (s *authScheduler) pruneAdaptiveLocked(now time.Time) {
	cfg := s.adaptive.config
	if cfg.StateTTL > 0 {
		cutoff := now.Add(-cfg.StateTTL)
		for key, stats := range s.adaptive.health {
			if stats == nil || (!stats.lastSeen.IsZero() && stats.lastSeen.Before(cutoff)) {
				delete(s.adaptive.health, key)
			}
		}
	}
	if cfg.MaxStateEntries <= 0 || len(s.adaptive.health) <= cfg.MaxStateEntries {
		return
	}
	type stateAge struct {
		key  adaptiveAuthKey
		seen time.Time
	}
	ages := make([]stateAge, 0, len(s.adaptive.health))
	for key, stats := range s.adaptive.health {
		seen := time.Time{}
		if stats != nil {
			seen = stats.lastSeen
		}
		ages = append(ages, stateAge{key: key, seen: seen})
	}
	sort.Slice(ages, func(i, j int) bool { return ages[i].seen.Before(ages[j].seen) })
	for len(s.adaptive.health) > cfg.MaxStateEntries && len(ages) > 0 {
		delete(s.adaptive.health, ages[0].key)
		ages = ages[1:]
	}
	if len(s.adaptive.last) > cfg.MaxStateEntries*2 {
		clear(s.adaptive.last)
		clear(s.adaptive.weight)
	}
}

func ewma(previous, sample, alpha float64) float64 {
	if previous <= 0 {
		return sample
	}
	return previous + alpha*(sample-previous)
}

func (s *authScheduler) reportAdaptiveObservation(provider, model, authID string, firstEvent, duration time.Duration, success bool) {
	if s == nil {
		return
	}
	authKey, _, okKey := normalizeAdaptiveKey(provider, model, authID)
	if !okKey {
		return
	}
	now := time.Now()
	s.mu.Lock()
	s.adaptive.mu.Lock()
	defer s.adaptive.mu.Unlock()
	defer s.mu.Unlock()
	if !s.adaptive.config.Enabled {
		return
	}
	s.pruneAdaptiveLocked(now)
	stats := s.adaptive.health[authKey]
	if stats == nil {
		stats = &adaptiveAuthStats{}
		s.adaptive.health[authKey] = stats
	}
	alpha := s.adaptive.config.EWMAAlpha
	if alpha <= 0 || alpha > 1 {
		alpha = 0.2
	}
	stats.lastSeen = now
	if firstEvent > 0 {
		stats.firstEventEWMA = ewma(stats.firstEventEWMA, float64(firstEvent), alpha)
		stats.sampleCount++
		if firstEvent >= s.adaptive.config.FirstEventThreshold {
			stats.slowStreak++
		} else {
			stats.slowStreak = 0
			stats.penaltyLevel = 0
		}
		if !s.adaptive.config.ObserveOnly && stats.sampleCount >= s.adaptive.config.MinSamples && stats.slowStreak >= s.adaptive.config.SlowStreak && !stats.penaltyUntil.After(now) {
			stats.penaltyLevel++
			penalty := s.adaptive.config.Penalty
			for level := 1; level < stats.penaltyLevel && penalty < s.adaptive.config.MaxPenalty; level++ {
				if penalty > s.adaptive.config.MaxPenalty/2 {
					penalty = s.adaptive.config.MaxPenalty
					break
				}
				penalty *= 2
			}
			if penalty > s.adaptive.config.MaxPenalty {
				penalty = s.adaptive.config.MaxPenalty
			}
			stats.penaltyUntil = now.Add(penalty)
			stats.slowStreak = 0
		}
	}
	if duration > 0 {
		stats.durationEWMA = ewma(stats.durationEWMA, float64(duration), alpha)
	}
	if success && firstEvent > 0 && firstEvent < s.adaptive.config.FirstEventThreshold {
		stats.slowStreak = 0
	}
}

func (s *authScheduler) acquireAdaptive(provider, model, authID string) *adaptiveAuthLease {
	if s == nil {
		return nil
	}
	authKey, loadKey, okKey := normalizeAdaptiveKey(provider, model, authID)
	if !okKey {
		return nil
	}
	s.mu.Lock()
	s.adaptive.mu.Lock()
	defer s.adaptive.mu.Unlock()
	defer s.mu.Unlock()
	if !s.adaptive.config.Enabled {
		return nil
	}
	s.adaptive.load[loadKey]++
	if stats := s.adaptive.health[authKey]; stats != nil {
		stats.lastSeen = time.Now()
	}
	return &adaptiveAuthLease{scheduler: s, key: loadKey, authKey: authKey}
}

func (l *adaptiveAuthLease) release(firstEvent, duration time.Duration, success bool) {
	if l == nil || l.scheduler == nil {
		return
	}
	l.once.Do(func() {
		s := l.scheduler
		s.mu.Lock()
		s.adaptive.mu.Lock()
		if current := s.adaptive.load[l.key]; current > 1 {
			s.adaptive.load[l.key] = current - 1
		} else {
			delete(s.adaptive.load, l.key)
		}
		s.adaptive.mu.Unlock()
		s.mu.Unlock()
		s.reportAdaptiveObservation(l.authKey.provider, l.authKey.model, l.authKey.authID, firstEvent, duration, success)
	})
}

func (s *authScheduler) adaptivePenaltyActiveLocked(provider, model, authID string, now time.Time) bool {
	if !s.adaptive.config.Enabled || s.adaptive.config.ObserveOnly {
		return false
	}
	key, _, okKey := normalizeAdaptiveKey(provider, model, authID)
	if !okKey {
		return false
	}
	stats := s.adaptive.health[key]
	return stats != nil && stats.penaltyUntil.After(now)
}

func (s *authScheduler) adaptiveLoadLocked(provider, model, authID string) float64 {
	if !s.adaptive.config.Enabled {
		return 0
	}
	key, loadKey, okKey := normalizeAdaptiveKey(provider, model, authID)
	if !okKey {
		return 0
	}
	stats := s.adaptive.health[key]
	duration := float64(s.adaptive.config.LoadFloor)
	if stats != nil && stats.durationEWMA > duration {
		duration = stats.durationEWMA
	}
	weight := float64(1)
	if entry := s.authEntryWeightLocked(loadKey.authID, provider); entry > 0 {
		weight = float64(entry)
	}
	return float64(s.adaptive.load[loadKey]) * duration / weight
}

func (s *authScheduler) adaptiveHasSignalLocked(provider, model string, entries []*scheduledAuth) bool {
	if s == nil || !s.adaptive.config.Enabled {
		return false
	}
	for _, entry := range entries {
		if entry == nil || entry.auth == nil {
			continue
		}
		if s.adaptiveLoadLocked(provider, model, entry.auth.ID) > 0 {
			return true
		}
		key, _, okKey := normalizeAdaptiveKey(provider, model, entry.auth.ID)
		if !okKey {
			continue
		}
		if stats := s.adaptive.health[key]; stats != nil && (stats.sampleCount > 0 || stats.penaltyUntil.After(time.Now())) {
			return true
		}
	}
	return false
}

func (s *authScheduler) adaptiveCandidateMetadata(provider, model, authID string) map[string]any {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.adaptive.mu.Lock()
	defer s.adaptive.mu.Unlock()
	defer s.mu.Unlock()
	if !s.adaptive.config.Enabled {
		return nil
	}
	key, loadKey, okKey := normalizeAdaptiveKey(provider, model, authID)
	if !okKey {
		return nil
	}
	stats := s.adaptive.health[key]
	if s.adaptive.load[loadKey] == 0 && (stats == nil || (stats.sampleCount == 0 && !stats.penaltyUntil.After(time.Now()))) {
		return nil
	}
	metadata := map[string]any{
		"adaptive_load": s.adaptiveLoadLocked(provider, model, authID),
	}
	if stats != nil && stats.penaltyUntil.After(time.Now()) {
		metadata["adaptive_penalty_until"] = stats.penaltyUntil.UTC().Format(time.RFC3339Nano)
	}
	return metadata
}

func (s *authScheduler) authEntryWeightLocked(authID, provider string) int64 {
	providerState := s.providers[strings.ToLower(strings.TrimSpace(provider))]
	if providerState == nil {
		return 1
	}
	meta := providerState.auths[authID]
	if meta == nil || meta.weight <= 0 {
		return 1
	}
	return meta.weight
}

// filterAdaptiveCandidates narrows plugin/legacy candidates to the healthiest
// set. If every candidate is penalized, it fails open and returns all candidates.
func (s *authScheduler) filterAdaptiveCandidates(provider, model string, candidates []*Auth, pinnedAuthID string) []*Auth {
	if s == nil || len(candidates) == 0 {
		return candidates
	}
	s.mu.Lock()
	s.adaptive.mu.Lock()
	defer s.adaptive.mu.Unlock()
	defer s.mu.Unlock()
	if !s.adaptive.config.Enabled || s.adaptive.config.ObserveOnly {
		return candidates
	}
	now := time.Now()
	healthy := make([]*Auth, 0, len(candidates))
	for _, auth := range candidates {
		if auth == nil {
			continue
		}
		if strings.TrimSpace(pinnedAuthID) != "" && auth.ID == pinnedAuthID {
			healthy = append(healthy, auth)
			continue
		}
		candidateProvider := provider
		if strings.EqualFold(strings.TrimSpace(provider), "mixed") {
			candidateProvider = executorKeyFromAuth(auth)
		}
		if !s.adaptivePenaltyActiveLocked(candidateProvider, model, auth.ID, now) {
			healthy = append(healthy, auth)
		}
	}
	if len(healthy) == 0 {
		return candidates
	}
	return healthy
}

func (s *authScheduler) pickAdaptiveFromViewLocked(view *readyView, provider, model string, strategy schedulerStrategy, predicate func(*scheduledAuth) bool) *scheduledAuth {
	if view == nil || len(view.flat) == 0 {
		return nil
	}
	now := time.Now()
	all := make([]*scheduledAuth, 0, len(view.flat))
	healthy := make([]*scheduledAuth, 0, len(view.flat))
	for _, entry := range view.flat {
		if entry == nil || entry.auth == nil || (predicate != nil && !predicate(entry)) {
			continue
		}
		all = append(all, entry)
		if !s.adaptivePenaltyActiveLocked(provider, model, entry.auth.ID, now) {
			healthy = append(healthy, entry)
		}
	}
	if len(all) == 0 {
		return nil
	}
	if !s.adaptiveHasSignalLocked(provider, model, all) {
		return nil
	}
	if len(healthy) > 0 {
		all = healthy
	}
	allowed := make(map[string]struct{}, len(all))
	for _, entry := range all {
		if entry != nil && entry.auth != nil {
			allowed[entry.auth.ID] = struct{}{}
		}
	}
	minLoad := math.Inf(1)
	for _, entry := range all {
		load := s.adaptiveLoadLocked(provider, model, entry.auth.ID)
		if load < minLoad {
			minLoad = load
		}
	}
	loadPredicate := func(entry *scheduledAuth) bool {
		if entry == nil || entry.auth == nil || (predicate != nil && !predicate(entry)) {
			return false
		}
		if _, ok := allowed[entry.auth.ID]; !ok {
			return false
		}
		return math.Abs(s.adaptiveLoadLocked(provider, model, entry.auth.ID)-minLoad) < 0.000001
	}
	switch strategy {
	case schedulerStrategyFillFirst:
		return view.pickFirst(loadPredicate)
	case schedulerStrategyWeightedRoundRobin:
		return view.pickWeighted(loadPredicate)
	default:
		return view.pickRoundRobin(loadPredicate)
	}
}

func (s *authScheduler) pickAdaptiveSingleLocked(shard *modelScheduler, preferWebsocket bool, provider, model string, strategy schedulerStrategy, predicate func(*scheduledAuth) bool) *Auth {
	if shard == nil || !s.adaptive.config.Enabled {
		return nil
	}
	priority, okPriority := shard.highestReadyPriorityLocked(preferWebsocket, predicate)
	if !okPriority {
		return nil
	}
	bucket := shard.readyByPriority[priority]
	if bucket == nil {
		return nil
	}
	view := &bucket.all
	if preferWebsocket && bucket.ws.pickFirst(predicate) != nil {
		view = &bucket.ws
	}
	picked := s.pickAdaptiveFromViewLocked(view, provider, model, strategy, predicate)
	if picked == nil {
		return nil
	}
	return picked.auth
}

// pickAdaptiveMixedLocked applies the same soft penalty and virtual load score
// across provider shards. The caller already selected the highest ready priority.
func (s *authScheduler) pickAdaptiveMixedLocked(shards []*modelScheduler, providers []string, model string, priority int, strategy schedulerStrategy, predicate func(*scheduledAuth) bool) (*scheduledAuth, string) {
	type candidate struct {
		entry    *scheduledAuth
		provider string
	}
	candidates := make([]candidate, 0)
	for index, shard := range shards {
		if shard == nil || index >= len(providers) {
			continue
		}
		bucket := shard.readyByPriority[priority]
		if bucket == nil {
			continue
		}
		for _, entry := range bucket.all.flat {
			if entry == nil || entry.auth == nil || (predicate != nil && !predicate(entry)) {
				continue
			}
			candidates = append(candidates, candidate{entry: entry, provider: providers[index]})
		}
	}
	if len(candidates) == 0 {
		return nil, ""
	}
	now := time.Now()
	hasSignal := false
	for _, item := range candidates {
		if s.adaptiveLoadLocked(item.provider, model, item.entry.auth.ID) > 0 {
			hasSignal = true
			break
		}
		key, _, okKey := normalizeAdaptiveKey(item.provider, model, item.entry.auth.ID)
		if okKey {
			if stats := s.adaptive.health[key]; stats != nil && (stats.sampleCount > 0 || stats.penaltyUntil.After(now)) {
				hasSignal = true
				break
			}
		}
	}
	if !hasSignal {
		return nil, ""
	}
	healthy := make([]candidate, 0, len(candidates))
	for _, item := range candidates {
		if !s.adaptivePenaltyActiveLocked(item.provider, model, item.entry.auth.ID, now) {
			healthy = append(healthy, item)
		}
	}
	if len(healthy) > 0 {
		candidates = healthy
	}
	minLoad := math.Inf(1)
	for _, item := range candidates {
		load := s.adaptiveLoadLocked(item.provider, model, item.entry.auth.ID)
		if load < minLoad {
			minLoad = load
		}
	}
	best := make([]candidate, 0, len(candidates))
	for _, item := range candidates {
		if math.Abs(s.adaptiveLoadLocked(item.provider, model, item.entry.auth.ID)-minLoad) < 0.000001 {
			best = append(best, item)
		}
	}
	if len(best) == 0 {
		return nil, ""
	}
	sort.SliceStable(best, func(i, j int) bool {
		left := best[i].entry.auth.ID
		right := best[j].entry.auth.ID
		if left == right {
			return best[i].provider < best[j].provider
		}
		return left < right
	})
	key := strings.Join(providers, ",") + ":" + canonicalModelKey(model)
	if strategy == schedulerStrategyWeightedRoundRobin {
		entries := make([]*scheduledAuth, 0, len(best))
		for _, item := range best {
			entries = append(entries, item.entry)
		}
		state := s.adaptive.weight[key]
		if state == nil {
			state = &smoothWeightedState{}
			s.adaptive.weight[key] = state
		}
		state.prepare(scheduledWeightVector(entries))
		if picked := pickSmoothWeightedScheduled(entries, state.current, func(entry *scheduledAuth) bool {
			for _, item := range best {
				if item.entry == entry {
					return true
				}
			}
			return false
		}); picked != nil {
			for _, item := range best {
				if item.entry == picked {
					s.adaptive.last[key] = picked.auth.ID
					return picked, item.provider
				}
			}
		}
	}
	last := s.adaptive.last[key]
	start := 0
	if last != "" {
		for index, item := range best {
			if item.entry.auth.ID > last {
				start = index
				break
			}
			if index == len(best)-1 {
				start = 0
			}
		}
	}
	picked := best[start]
	s.adaptive.last[key] = picked.entry.auth.ID
	return picked.entry, picked.provider
}
