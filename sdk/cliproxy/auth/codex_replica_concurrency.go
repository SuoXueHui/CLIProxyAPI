package auth

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

const ErrorCodeCodexReplicaConcurrency = "codex_replica_concurrency_exceeded"

// CodexReplicaConcurrencyState is the log-safe runtime occupancy of one replica.
type CodexReplicaConcurrencyState struct {
	Active int `json:"active"`
	Limit  int `json:"limit"`
}

type codexReplicaConcurrencyTracker struct {
	mu     sync.Mutex
	active map[string]int
	limits map[string]int
}

var globalCodexReplicaConcurrency = codexReplicaConcurrencyTracker{
	active: make(map[string]int),
	limits: make(map[string]int),
}

// CodexReplicaConcurrencyLease owns one per-replica execution slot.
type CodexReplicaConcurrencyLease struct {
	authID  string
	once    sync.Once
	tracker *codexReplicaConcurrencyTracker
}

// AcquireCodexReplicaConcurrency atomically reserves one configured runtime slot.
func AcquireCodexReplicaConcurrency(auth *Auth) (*CodexReplicaConcurrencyLease, error) {
	limit, enabled := codexReplicaConcurrencyLimit(auth)
	if !enabled {
		return nil, nil
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return nil, &Error{Code: ErrorCodeCodexReplicaConcurrency, Message: "Codex replica auth ID is empty", HTTPStatus: http.StatusServiceUnavailable}
	}

	tracker := &globalCodexReplicaConcurrency
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.limits[authID] = limit
	if tracker.active[authID] >= limit {
		return nil, newCodexReplicaConcurrencyError(limit)
	}
	tracker.active[authID]++
	return &CodexReplicaConcurrencyLease{authID: authID, tracker: tracker}, nil
}

// CodexReplicaConcurrencyAvailable reports whether selection may admit another request.
// AcquireCodexReplicaConcurrency remains the atomic authority because occupancy can change
// after selection and before execution starts.
func CodexReplicaConcurrencyAvailable(auth *Auth) bool {
	limit, enabled := codexReplicaConcurrencyLimit(auth)
	if !enabled {
		return true
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return false
	}
	tracker := &globalCodexReplicaConcurrency
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	tracker.limits[authID] = limit
	return tracker.active[authID] < limit
}

// Release returns a slot exactly once.
func (l *CodexReplicaConcurrencyLease) Release() {
	if l == nil || l.tracker == nil || strings.TrimSpace(l.authID) == "" {
		return
	}
	l.once.Do(func() {
		l.tracker.mu.Lock()
		defer l.tracker.mu.Unlock()
		if l.tracker.active[l.authID] <= 1 {
			delete(l.tracker.active, l.authID)
			return
		}
		l.tracker.active[l.authID]--
	})
}

// CodexReplicaConcurrencySnapshot returns the current occupancy without exposing account data.
func CodexReplicaConcurrencySnapshot(authID string) CodexReplicaConcurrencyState {
	authID = strings.TrimSpace(authID)
	tracker := &globalCodexReplicaConcurrency
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return CodexReplicaConcurrencyState{Active: tracker.active[authID], Limit: tracker.limits[authID]}
}

// ForgetCodexReplicaConcurrency removes inactive observability state when an auth is unregistered.
func ForgetCodexReplicaConcurrency(authID string) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	tracker := &globalCodexReplicaConcurrency
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.active[authID] == 0 {
		delete(tracker.active, authID)
		delete(tracker.limits, authID)
	}
}

// IsCodexReplicaConcurrencyError identifies local admission failures that must not cool down auths.
func IsCodexReplicaConcurrencyError(err error) bool {
	var authErr *Error
	return errors.As(err, &authErr) && authErr != nil && authErr.Code == ErrorCodeCodexReplicaConcurrency
}

func newCodexReplicaConcurrencyError(limit int) error {
	message := "All eligible Codex replicas reached their concurrency limits"
	if limit > 0 {
		message = fmt.Sprintf("Codex replica reached its concurrency limit of %d", limit)
	}
	return &Error{
		Code:       ErrorCodeCodexReplicaConcurrency,
		Message:    message,
		Retryable:  true,
		HTTPStatus: http.StatusTooManyRequests,
	}
}

func codexReplicaConcurrencyLimit(auth *Auth) (int, bool) {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") || len(auth.Attributes) == 0 {
		return 0, false
	}
	if strings.TrimSpace(auth.Attributes[AttributeCodexReplicaGroup]) == "" {
		return 0, false
	}
	limit, errParse := strconv.Atoi(strings.TrimSpace(auth.Attributes[AttributeCodexReplicaConcurrency]))
	if errParse != nil || limit < 1 || limit > MaxCodexReplicaConcurrency {
		return 0, false
	}
	return limit, true
}

func resetCodexReplicaConcurrencyForTest() {
	tracker := &globalCodexReplicaConcurrency
	tracker.mu.Lock()
	tracker.active = make(map[string]int)
	tracker.limits = make(map[string]int)
	tracker.mu.Unlock()
}
