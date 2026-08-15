package helps

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	codexWeeklyOverdraftAccountRetention = 6 * time.Hour
	// The cap prevents unbounded memory growth when the management endpoint is
	// never polled. Existing account entries remain lock-free and keep updating.
	codexWeeklyOverdraftAccountLimit int64 = 8192
)

// CodexWeeklyOverdraftActionStatus contains request and terminal outcome
// counters for one experiment action. Observe and inject remain separate so a
// hot mode change cannot relabel earlier account results.
type CodexWeeklyOverdraftActionStatus struct {
	Requests uint64                              `json:"requests"`
	Outcomes CodexWeeklyOverdraftOutcomeSnapshot `json:"outcomes"`
}

// CodexWeeklyOverdraftAccountStatus is a six-hour, process-local operational
// summary keyed by the stable management-visible CPA auth ID.
type CodexWeeklyOverdraftAccountStatus struct {
	AuthID      string                           `json:"auth-id"`
	FirstSeenAt time.Time                        `json:"first-seen-at"`
	LastSeenAt  time.Time                        `json:"last-seen-at"`
	Observed    CodexWeeklyOverdraftActionStatus `json:"observed"`
	Injected    CodexWeeklyOverdraftActionStatus `json:"injected"`
}

type codexWeeklyOverdraftActionMetrics struct {
	requests     atomic.Uint64
	success      atomic.Uint64
	usageLimit   atomic.Uint64
	hardStop     atomic.Uint64
	canceled     atomic.Uint64
	otherFailure atomic.Uint64
}

type codexWeeklyOverdraftAccountMetric struct {
	authID      string
	firstSeenAt atomic.Int64
	lastSeenAt  atomic.Int64
	observed    codexWeeklyOverdraftActionMetrics
	injected    codexWeeklyOverdraftActionMetrics
}

type codexWeeklyOverdraftAccountMetricSet struct {
	entries sync.Map
	count   atomic.Int64
}

func newCodexWeeklyOverdraftAccountMetricSet() *codexWeeklyOverdraftAccountMetricSet {
	return &codexWeeklyOverdraftAccountMetricSet{}
}

// track records an eligible account request without a global mutex. The only
// shared structure operation is sync.Map lookup/insert; steady-state counters
// are atomics scoped to the selected auth entry.
func (m *codexWeeklyOverdraftAccountMetricSet) track(authID, action string, now time.Time) *codexWeeklyOverdraftAccountMetric {
	if m == nil {
		return nil
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return nil
	}
	if loaded, ok := m.entries.Load(authID); ok {
		metric := loaded.(*codexWeeklyOverdraftAccountMetric)
		metric.recordRequest(action, now)
		return metric
	}

	// Reserve capacity with CAS so concurrent first-seen accounts cannot turn a
	// burst into unbounded memory. Dropping account-only observability never
	// changes the global counters or upstream request behavior.
	for {
		count := m.count.Load()
		if count >= codexWeeklyOverdraftAccountLimit {
			return nil
		}
		if m.count.CompareAndSwap(count, count+1) {
			break
		}
	}

	timestamp := now.UTC().UnixNano()
	candidate := &codexWeeklyOverdraftAccountMetric{authID: authID}
	candidate.firstSeenAt.Store(timestamp)
	candidate.lastSeenAt.Store(timestamp)
	loaded, existed := m.entries.LoadOrStore(authID, candidate)
	metric := candidate
	if existed {
		m.count.Add(-1)
		metric = loaded.(*codexWeeklyOverdraftAccountMetric)
	}
	metric.recordRequest(action, now)
	return metric
}

func (m *codexWeeklyOverdraftAccountMetric) recordRequest(action string, now time.Time) {
	if m == nil {
		return
	}
	m.lastSeenAt.Store(now.UTC().UnixNano())
	switch action {
	case CodexWeeklyOverdraftActionObserved:
		m.observed.requests.Add(1)
	case CodexWeeklyOverdraftActionInjected:
		m.injected.requests.Add(1)
	}
}

func (m *codexWeeklyOverdraftAccountMetric) recordOutcome(action, outcome string, now time.Time) {
	if m == nil {
		return
	}
	m.lastSeenAt.Store(now.UTC().UnixNano())
	metrics := &m.observed
	if action == CodexWeeklyOverdraftActionInjected {
		metrics = &m.injected
	}
	metrics.addOutcome(outcome)
}

func (m *codexWeeklyOverdraftActionMetrics) addOutcome(outcome string) {
	switch outcome {
	case codexWeeklyOverdraftOutcomeSuccess:
		m.success.Add(1)
	case codexWeeklyOverdraftOutcomeUsageLimit:
		m.usageLimit.Add(1)
	case codexWeeklyOverdraftOutcomeHardStop:
		m.hardStop.Add(1)
	case codexWeeklyOverdraftOutcomeCanceled:
		m.canceled.Add(1)
	case codexWeeklyOverdraftOutcomeOtherFailure:
		m.otherFailure.Add(1)
	}
}

func (m *codexWeeklyOverdraftActionMetrics) snapshot() CodexWeeklyOverdraftActionStatus {
	return CodexWeeklyOverdraftActionStatus{
		Requests: m.requests.Load(),
		Outcomes: CodexWeeklyOverdraftOutcomeSnapshot{
			Success:      m.success.Load(),
			UsageLimit:   m.usageLimit.Load(),
			HardStop:     m.hardStop.Load(),
			Canceled:     m.canceled.Load(),
			OtherFailure: m.otherFailure.Load(),
		},
	}
}

func (m *codexWeeklyOverdraftAccountMetric) snapshot() CodexWeeklyOverdraftAccountStatus {
	return CodexWeeklyOverdraftAccountStatus{
		AuthID:      m.authID,
		FirstSeenAt: time.Unix(0, m.firstSeenAt.Load()).UTC(),
		LastSeenAt:  time.Unix(0, m.lastSeenAt.Load()).UTC(),
		Observed:    m.observed.snapshot(),
		Injected:    m.injected.snapshot(),
	}
}

func (m *codexWeeklyOverdraftAccountMetricSet) snapshot(now time.Time, authIDs []string) []CodexWeeklyOverdraftAccountStatus {
	accounts := make([]CodexWeeklyOverdraftAccountStatus, 0)
	if m == nil {
		return accounts
	}
	filter := codexWeeklyOverdraftAuthIDFilter(authIDs)
	cutoff := now.UTC().Add(-codexWeeklyOverdraftAccountRetention)
	m.entries.Range(func(key, value any) bool {
		authID := key.(string)
		metric := value.(*codexWeeklyOverdraftAccountMetric)
		lastSeenAt := time.Unix(0, metric.lastSeenAt.Load()).UTC()
		if lastSeenAt.Before(cutoff) {
			if m.entries.CompareAndDelete(authID, metric) {
				m.count.Add(-1)
			}
			return true
		}
		if len(filter) > 0 {
			if _, ok := filter[authID]; !ok {
				return true
			}
		}
		accounts = append(accounts, metric.snapshot())
		return true
	})
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].LastSeenAt.Equal(accounts[j].LastSeenAt) {
			return accounts[i].AuthID < accounts[j].AuthID
		}
		return accounts[i].LastSeenAt.After(accounts[j].LastSeenAt)
	})
	return accounts
}

func codexWeeklyOverdraftAuthIDFilter(authIDs []string) map[string]struct{} {
	filter := make(map[string]struct{}, len(authIDs))
	for _, authID := range authIDs {
		if authID = strings.TrimSpace(authID); authID != "" {
			filter[authID] = struct{}{}
		}
	}
	return filter
}
