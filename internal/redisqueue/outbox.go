package redisqueue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const disabledOutboxPath = "disabled"

var errOutboxUnavailable = errors.New("usage outbox unavailable")

// ClaimItem carries a stable delivery ID beside the original unmodified usage payload.
type ClaimItem struct {
	DeliveryID string `json:"delivery_id"`
	Payload    []byte `json:"-"`
}

// ClaimResult identifies a leased batch. The lease token must accompany acknowledgements.
type ClaimResult struct {
	LeaseID string      `json:"lease_id"`
	Items   []ClaimItem `json:"items"`
}

// QueueStatus exposes aggregate delivery health without exposing usage payloads.
type QueueStatus struct {
	Mode             string  `json:"mode"`
	Healthy          bool    `json:"healthy"`
	Pending          int64   `json:"pending"`
	Inflight         int64   `json:"inflight"`
	Produced         int64   `json:"produced"`
	Acked            int64   `json:"acked"`
	LeaseExpired     int64   `json:"lease_expired"`
	Dropped          int64   `json:"dropped"`
	OldestAgeSeconds float64 `json:"oldest_age_seconds"`
}

type durableOutbox struct {
	db  *sql.DB
	now func() time.Time
}

type outboxRuntime struct {
	mu       sync.RWMutex
	durable  *durableOutbox
	path     string
	mode     string
	healthy  bool
	lastErr  error
	dropped  atomic.Int64
	produced atomic.Int64
	acked    atomic.Int64
}

type outboxTestSnapshot struct {
	durable  *durableOutbox
	path     string
	mode     string
	healthy  bool
	lastErr  error
	dropped  int64
	produced int64
	acked    int64
}

var runtimeOutbox = outboxRuntime{mode: "memory", healthy: true}

// ResolveOutboxPath resolves relative storage beside the active configuration file.
func ResolveOutboxPath(configFilePath, configuredPath string) string {
	configuredPath = strings.TrimSpace(configuredPath)
	if strings.EqualFold(configuredPath, disabledOutboxPath) {
		return disabledOutboxPath
	}
	if configuredPath == "" {
		configuredPath = "usage-outbox.sqlite"
	}
	if filepath.IsAbs(configuredPath) {
		return filepath.Clean(configuredPath)
	}
	base := filepath.Dir(configFilePath)
	if strings.TrimSpace(configFilePath) == "" {
		base = "."
	}
	return filepath.Clean(filepath.Join(base, configuredPath))
}

func openOutbox(path string, now func() time.Time) (*durableOutbox, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("usage outbox path is empty")
	}
	if now == nil {
		now = time.Now
	}
	parent := filepath.Dir(path)
	info, errStat := os.Stat(parent)
	if errStat != nil {
		return nil, fmt.Errorf("inspect usage outbox directory: %w", errStat)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("usage outbox parent is not a directory: %s", parent)
	}

	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(200)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)"
	db, errOpen := sql.Open("sqlite", dsn)
	if errOpen != nil {
		return nil, fmt.Errorf("open usage outbox: %w", errOpen)
	}
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if errPing := db.PingContext(ctx); errPing != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping usage outbox: %w", errPing)
	}
	if _, errSchema := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS usage_outbox (
    delivery_id TEXT PRIMARY KEY,
    payload BLOB NOT NULL,
    enqueued_at_ns INTEGER NOT NULL,
    lease_token TEXT NOT NULL DEFAULT '',
    lease_until_ns INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_usage_outbox_available ON usage_outbox(lease_until_ns, enqueued_at_ns);
CREATE TABLE IF NOT EXISTS usage_outbox_meta (
    key TEXT PRIMARY KEY,
    value INTEGER NOT NULL
);
INSERT OR IGNORE INTO usage_outbox_meta(key, value) VALUES ('produced', 0), ('acked', 0), ('lease_expired', 0);
`); errSchema != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize usage outbox: %w", errSchema)
	}
	return &durableOutbox{db: db, now: now}, nil
}

func (o *durableOutbox) close() error {
	if o == nil || o.db == nil {
		return nil
	}
	return o.db.Close()
}

func (o *durableOutbox) enqueue(payload []byte) (string, error) {
	if o == nil || o.db == nil {
		return "", errOutboxUnavailable
	}
	deliveryID := uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	tx, errBegin := o.db.BeginTx(ctx, nil)
	if errBegin != nil {
		return "", fmt.Errorf("begin usage outbox enqueue: %w", errBegin)
	}
	defer tx.Rollback()
	if _, errInsert := tx.ExecContext(ctx, `INSERT INTO usage_outbox(delivery_id, payload, enqueued_at_ns) VALUES (?, ?, ?)`, deliveryID, append([]byte(nil), payload...), o.now().UnixNano()); errInsert != nil {
		return "", fmt.Errorf("insert usage outbox record: %w", errInsert)
	}
	if _, errCount := tx.ExecContext(ctx, `UPDATE usage_outbox_meta SET value = value + 1 WHERE key = 'produced'`); errCount != nil {
		return "", fmt.Errorf("increment usage outbox produced count: %w", errCount)
	}
	if errCommit := tx.Commit(); errCommit != nil {
		return "", fmt.Errorf("commit usage outbox enqueue: %w", errCommit)
	}
	return deliveryID, nil
}

// shouldPersistPayload keeps queue control frames on the live subscriber path only.
func shouldPersistPayload(payload []byte) bool {
	trimmed := strings.TrimSpace(string(payload))
	return trimmed != usageSupportRefreshPayload && trimmed != usageRefreshPayload
}

func (o *durableOutbox) claim(count int, lease time.Duration) (ClaimResult, error) {
	result := ClaimResult{Items: make([]ClaimItem, 0)}
	if o == nil || o.db == nil {
		return result, errOutboxUnavailable
	}
	if count <= 0 {
		return result, errors.New("claim count must be positive")
	}
	if lease <= 0 {
		return result, errors.New("lease duration must be positive")
	}
	now := o.now()
	nowNS := now.UnixNano()
	tx, errBegin := o.db.Begin()
	if errBegin != nil {
		return result, fmt.Errorf("begin usage outbox claim: %w", errBegin)
	}
	defer tx.Rollback()

	var expired int64
	if errExpired := tx.QueryRow(`SELECT COUNT(*) FROM usage_outbox WHERE lease_token <> '' AND lease_until_ns <= ?`, nowNS).Scan(&expired); errExpired != nil {
		return result, fmt.Errorf("count expired usage leases: %w", errExpired)
	}
	if expired > 0 {
		if _, errCount := tx.Exec(`UPDATE usage_outbox_meta SET value = value + ? WHERE key = 'lease_expired'`, expired); errCount != nil {
			return result, fmt.Errorf("increment expired usage leases: %w", errCount)
		}
		if _, errRelease := tx.Exec(`UPDATE usage_outbox SET lease_token = '', lease_until_ns = 0 WHERE lease_token <> '' AND lease_until_ns <= ?`, nowNS); errRelease != nil {
			return result, fmt.Errorf("release expired usage leases: %w", errRelease)
		}
	}

	rows, errQuery := tx.Query(`SELECT delivery_id, payload FROM usage_outbox WHERE lease_until_ns <= ? ORDER BY enqueued_at_ns, delivery_id LIMIT ?`, nowNS, count)
	if errQuery != nil {
		return result, fmt.Errorf("select usage outbox claim: %w", errQuery)
	}
	for rows.Next() {
		var item ClaimItem
		if errScan := rows.Scan(&item.DeliveryID, &item.Payload); errScan != nil {
			rows.Close()
			return result, fmt.Errorf("scan usage outbox claim: %w", errScan)
		}
		item.Payload = append([]byte(nil), item.Payload...)
		result.Items = append(result.Items, item)
	}
	if errIter := rows.Err(); errIter != nil {
		rows.Close()
		return result, fmt.Errorf("iterate usage outbox claim: %w", errIter)
	}
	if errRows := rows.Close(); errRows != nil {
		return result, fmt.Errorf("close usage outbox claim rows: %w", errRows)
	}
	if len(result.Items) == 0 {
		if errCommit := tx.Commit(); errCommit != nil {
			return result, fmt.Errorf("commit empty usage outbox claim: %w", errCommit)
		}
		return result, nil
	}

	result.LeaseID = uuid.NewString()
	leaseUntil := now.Add(lease).UnixNano()
	for _, item := range result.Items {
		if _, errLease := tx.Exec(`UPDATE usage_outbox SET lease_token = ?, lease_until_ns = ? WHERE delivery_id = ? AND lease_until_ns <= ?`, result.LeaseID, leaseUntil, item.DeliveryID, nowNS); errLease != nil {
			return ClaimResult{}, fmt.Errorf("lease usage outbox record: %w", errLease)
		}
	}
	if errCommit := tx.Commit(); errCommit != nil {
		return ClaimResult{}, fmt.Errorf("commit usage outbox claim: %w", errCommit)
	}
	return result, nil
}

func (o *durableOutbox) ack(leaseToken string, deliveryIDs []string) (int64, error) {
	if o == nil || o.db == nil {
		return 0, errOutboxUnavailable
	}
	leaseToken = strings.TrimSpace(leaseToken)
	if leaseToken == "" || len(deliveryIDs) == 0 {
		return 0, nil
	}
	tx, errBegin := o.db.Begin()
	if errBegin != nil {
		return 0, fmt.Errorf("begin usage outbox ack: %w", errBegin)
	}
	defer tx.Rollback()
	var acked int64
	seen := make(map[string]struct{}, len(deliveryIDs))
	for _, deliveryID := range deliveryIDs {
		deliveryID = strings.TrimSpace(deliveryID)
		if deliveryID == "" {
			continue
		}
		if _, duplicate := seen[deliveryID]; duplicate {
			continue
		}
		seen[deliveryID] = struct{}{}
		result, errDelete := tx.Exec(`DELETE FROM usage_outbox WHERE delivery_id = ? AND lease_token = ?`, deliveryID, leaseToken)
		if errDelete != nil {
			return 0, fmt.Errorf("ack usage outbox record: %w", errDelete)
		}
		rows, errRows := result.RowsAffected()
		if errRows != nil {
			return 0, fmt.Errorf("read usage outbox ack result: %w", errRows)
		}
		acked += rows
	}
	if acked > 0 {
		if _, errCount := tx.Exec(`UPDATE usage_outbox_meta SET value = value + ? WHERE key = 'acked'`, acked); errCount != nil {
			return 0, fmt.Errorf("increment usage outbox acked count: %w", errCount)
		}
	}
	if errCommit := tx.Commit(); errCommit != nil {
		return 0, fmt.Errorf("commit usage outbox ack: %w", errCommit)
	}
	return acked, nil
}

func (o *durableOutbox) popOldest(count int) ([][]byte, error) {
	claim, errClaim := o.claim(count, time.Minute)
	if errClaim != nil {
		return nil, errClaim
	}
	if len(claim.Items) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(claim.Items))
	items := make([][]byte, 0, len(claim.Items))
	for _, item := range claim.Items {
		ids = append(ids, item.DeliveryID)
		items = append(items, append([]byte(nil), item.Payload...))
	}
	if _, errAck := o.ack(claim.LeaseID, ids); errAck != nil {
		return nil, errAck
	}
	return items, nil
}

func (o *durableOutbox) stats() (QueueStatus, error) {
	status := QueueStatus{Mode: "durable", Healthy: true}
	if o == nil || o.db == nil {
		return status, errOutboxUnavailable
	}
	now := o.now()
	var oldestPending int64
	if errCounts := o.db.QueryRow(`SELECT
COALESCE(SUM(CASE WHEN lease_until_ns <= ? THEN 1 ELSE 0 END), 0),
COALESCE(SUM(CASE WHEN lease_until_ns > ? THEN 1 ELSE 0 END), 0),
COALESCE(MIN(CASE WHEN lease_until_ns <= ? THEN enqueued_at_ns END), 0)
FROM usage_outbox`, now.UnixNano(), now.UnixNano(), now.UnixNano()).Scan(&status.Pending, &status.Inflight, &oldestPending); errCounts != nil {
		return status, fmt.Errorf("read usage outbox status: %w", errCounts)
	}
	if oldestPending > 0 {
		status.OldestAgeSeconds = max(0, now.Sub(time.Unix(0, oldestPending)).Seconds())
	}
	rows, errMeta := o.db.Query(`SELECT key, value FROM usage_outbox_meta`)
	if errMeta != nil {
		return status, fmt.Errorf("read usage outbox counters: %w", errMeta)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var value int64
		if errScan := rows.Scan(&key, &value); errScan != nil {
			return status, fmt.Errorf("scan usage outbox counter: %w", errScan)
		}
		switch key {
		case "produced":
			status.Produced = value
		case "acked":
			status.Acked = value
		case "lease_expired":
			status.LeaseExpired = value
		}
	}
	return status, rows.Err()
}

// ConfigureOutbox swaps the usage persistence backend without deleting existing data.
func ConfigureOutbox(path string) error {
	path = strings.TrimSpace(path)
	if strings.EqualFold(path, disabledOutboxPath) {
		runtimeOutbox.mu.Lock()
		old := runtimeOutbox.durable
		runtimeOutbox.durable = nil
		runtimeOutbox.path = disabledOutboxPath
		runtimeOutbox.mode = "memory"
		runtimeOutbox.healthy = true
		runtimeOutbox.lastErr = nil
		runtimeOutbox.mu.Unlock()
		if old != nil {
			return old.close()
		}
		return nil
	}
	runtimeOutbox.mu.RLock()
	if runtimeOutbox.durable != nil && runtimeOutbox.mode == "durable" && runtimeOutbox.healthy && runtimeOutbox.path == path {
		runtimeOutbox.mu.RUnlock()
		return nil
	}
	runtimeOutbox.mu.RUnlock()

	durable, errOpen := openOutbox(path, time.Now)
	runtimeOutbox.mu.Lock()
	old := runtimeOutbox.durable
	if errOpen == nil && old != nil && runtimeOutbox.mode == "durable" && runtimeOutbox.path == path {
		runtimeOutbox.healthy = true
		runtimeOutbox.lastErr = nil
		runtimeOutbox.mu.Unlock()
		_ = durable.close()
		return nil
	}
	if errOpen != nil {
		if old == nil {
			runtimeOutbox.durable = nil
			runtimeOutbox.path = path
			runtimeOutbox.mode = "durable"
			runtimeOutbox.healthy = false
			runtimeOutbox.lastErr = errOpen
		} else {
			// Keep the currently working durable backend when a hot-reload target cannot be opened.
			runtimeOutbox.healthy = true
			runtimeOutbox.lastErr = nil
			old = nil
		}
	} else {
		runtimeOutbox.durable = durable
		runtimeOutbox.path = path
		runtimeOutbox.mode = "durable"
		runtimeOutbox.healthy = true
		runtimeOutbox.lastErr = nil
	}
	runtimeOutbox.mu.Unlock()
	if old != nil {
		_ = old.close()
	}
	return errOpen
}

// CloseOutbox closes database handles but never deletes pending records.
func CloseOutbox() error {
	runtimeOutbox.mu.Lock()
	durable := runtimeOutbox.durable
	runtimeOutbox.durable = nil
	runtimeOutbox.path = ""
	runtimeOutbox.mode = "memory"
	runtimeOutbox.healthy = true
	runtimeOutbox.lastErr = nil
	runtimeOutbox.mu.Unlock()
	if durable == nil {
		return nil
	}
	return durable.close()
}

// EnqueueWithError persists a usage payload before subscriber notification.
func EnqueueWithError(payload []byte) error {
	if !Enabled() || len(payload) == 0 {
		return nil
	}
	if !shouldPersistPayload(payload) {
		global.publishToSubscribers(payload)
		return nil
	}
	runtimeOutbox.mu.RLock()
	durable := runtimeOutbox.durable
	mode := runtimeOutbox.mode
	lastErr := runtimeOutbox.lastErr
	runtimeOutbox.mu.RUnlock()
	if mode == "durable" {
		if durable == nil {
			runtimeOutbox.dropped.Add(1)
			if lastErr != nil {
				return fmt.Errorf("persist usage record: %w", lastErr)
			}
			return errOutboxUnavailable
		}
		if _, errEnqueue := durable.enqueue(payload); errEnqueue != nil {
			runtimeOutbox.dropped.Add(1)
			runtimeOutbox.mu.Lock()
			runtimeOutbox.healthy = false
			runtimeOutbox.lastErr = errEnqueue
			runtimeOutbox.mu.Unlock()
			return errEnqueue
		}
	} else {
		global.enqueue(payload)
		runtimeOutbox.produced.Add(1)
	}
	global.publishToSubscribers(payload)
	return nil
}

// Claim leases durable usage records without removing them.
func Claim(count int, lease time.Duration) (ClaimResult, error) {
	runtimeOutbox.mu.RLock()
	durable := runtimeOutbox.durable
	mode := runtimeOutbox.mode
	runtimeOutbox.mu.RUnlock()
	if mode != "durable" {
		return ClaimResult{}, errors.New("usage outbox claim requires durable mode")
	}
	if durable == nil {
		return ClaimResult{}, errOutboxUnavailable
	}
	return durable.claim(count, lease)
}

// Ack removes only records leased by the supplied token and is safe to repeat.
func Ack(leaseToken string, deliveryIDs []string) (int64, error) {
	runtimeOutbox.mu.RLock()
	durable := runtimeOutbox.durable
	mode := runtimeOutbox.mode
	runtimeOutbox.mu.RUnlock()
	if mode != "durable" {
		return 0, errors.New("usage outbox ack requires durable mode")
	}
	if durable == nil {
		return 0, errOutboxUnavailable
	}
	return durable.ack(leaseToken, deliveryIDs)
}

// Status returns queue counters and health without payload contents or file paths.
func Status() QueueStatus {
	runtimeOutbox.mu.RLock()
	durable := runtimeOutbox.durable
	mode := runtimeOutbox.mode
	healthy := runtimeOutbox.healthy
	runtimeOutbox.mu.RUnlock()
	if mode == "durable" && durable != nil {
		status, errStats := durable.stats()
		if errStats == nil {
			status.Dropped = runtimeOutbox.dropped.Load()
			return status
		}
		healthy = false
	}
	return QueueStatus{
		Mode:     mode,
		Healthy:  healthy,
		Pending:  int64(global.len()),
		Produced: runtimeOutbox.produced.Load(),
		Acked:    runtimeOutbox.acked.Load(),
		Dropped:  runtimeOutbox.dropped.Load(),
	}
}

func popDurableOldest(count int) ([][]byte, bool, error) {
	runtimeOutbox.mu.RLock()
	durable := runtimeOutbox.durable
	mode := runtimeOutbox.mode
	runtimeOutbox.mu.RUnlock()
	if mode != "durable" {
		return nil, false, nil
	}
	if durable == nil {
		return nil, true, errOutboxUnavailable
	}
	return func() ([][]byte, bool, error) {
		items, errPop := durable.popOldest(count)
		return items, true, errPop
	}()
}

func durableModeConfigured() bool {
	runtimeOutbox.mu.RLock()
	defer runtimeOutbox.mu.RUnlock()
	return runtimeOutbox.mode == "durable"
}

func snapshotGlobalOutboxForTest() outboxTestSnapshot {
	runtimeOutbox.mu.RLock()
	defer runtimeOutbox.mu.RUnlock()
	return outboxTestSnapshot{
		durable: runtimeOutbox.durable, path: runtimeOutbox.path, mode: runtimeOutbox.mode, healthy: runtimeOutbox.healthy, lastErr: runtimeOutbox.lastErr,
		dropped: runtimeOutbox.dropped.Load(), produced: runtimeOutbox.produced.Load(), acked: runtimeOutbox.acked.Load(),
	}
}

func restoreGlobalOutboxForTest(snapshot outboxTestSnapshot) {
	runtimeOutbox.mu.Lock()
	current := runtimeOutbox.durable
	runtimeOutbox.durable = snapshot.durable
	runtimeOutbox.path = snapshot.path
	runtimeOutbox.mode = snapshot.mode
	runtimeOutbox.healthy = snapshot.healthy
	runtimeOutbox.lastErr = snapshot.lastErr
	runtimeOutbox.dropped.Store(snapshot.dropped)
	runtimeOutbox.produced.Store(snapshot.produced)
	runtimeOutbox.acked.Store(snapshot.acked)
	runtimeOutbox.mu.Unlock()
	if current != nil && current != snapshot.durable {
		_ = current.close()
	}
}
