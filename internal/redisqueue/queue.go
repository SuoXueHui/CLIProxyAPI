package redisqueue

import (
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	defaultRetentionSeconds int64 = 60
	maxRetentionSeconds     int64 = 3600
	usageSubscriberBuffer         = 256
	errorSubscriberBuffer         = 256

	usageSupportRefreshPayload = `{"support_refresh":true}`
	usageRefreshPayload        = `{"refresh":true}`
	outboxErrorLogInterval     = time.Minute
)

type queueItem struct {
	enqueuedAt time.Time
	payload    []byte
}

type queue struct {
	mu               sync.Mutex
	items            []queueItem
	head             int
	subscribers      map[uint64]chan []byte
	nextSubscriberID uint64
}

var (
	enabled          atomic.Bool
	retentionSeconds atomic.Int64
	global           queue
	errorGlobal      queue
	lastOutboxLogNS  atomic.Int64
)

func init() {
	retentionSeconds.Store(defaultRetentionSeconds)
}

func SetEnabled(value bool) {
	enabled.Store(value)
	if !value {
		if durableModeConfigured() {
			global.closeSubscribers()
		} else {
			global.clear()
		}
		errorGlobal.clear()
	}
}

func Enabled() bool {
	return enabled.Load()
}

func SetRetentionSeconds(value int) {
	normalized := int64(value)
	if normalized <= 0 {
		normalized = defaultRetentionSeconds
	} else if normalized > maxRetentionSeconds {
		normalized = maxRetentionSeconds
	}
	retentionSeconds.Store(normalized)
}

func Enqueue(payload []byte) {
	if errEnqueue := EnqueueWithError(payload); errEnqueue != nil {
		if shouldLogOutboxError(time.Now()) {
			log.WithError(errEnqueue).Error("usage record dropped because the durable outbox write failed")
		}
	}
}

func shouldLogOutboxError(now time.Time) bool {
	nowNS := now.UnixNano()
	for {
		previous := lastOutboxLogNS.Load()
		if previous > 0 && nowNS-previous < int64(outboxErrorLogInterval) {
			return false
		}
		if lastOutboxLogNS.CompareAndSwap(previous, nowNS) {
			return true
		}
	}
}

func EnqueueError(payload []byte) {
	if !Enabled() {
		return
	}
	if len(payload) == 0 {
		return
	}
	errorGlobal.publishToSubscribers(payload)
}

func PopOldest(count int) [][]byte {
	if !Enabled() {
		return nil
	}
	if count <= 0 {
		return nil
	}
	items, durable, errPop := popDurableOldest(count)
	if durable {
		if errPop != nil {
			log.WithError(errPop).Error("failed to pop usage records from the durable outbox")
			return nil
		}
		return items
	}
	items = global.popOldest(count)
	if len(items) > 0 {
		runtimeOutbox.acked.Add(int64(len(items)))
	}
	return items
}

func SubscribeUsage() (<-chan []byte, func()) {
	return global.subscribe(usageSubscriberBuffer, []byte(usageSupportRefreshPayload))
}

func SubscribeErrors() (<-chan []byte, func()) {
	return errorGlobal.subscribe(errorSubscriberBuffer, nil)
}

func NotifyUsageRefresh() {
	global.publishToSubscribers([]byte(usageRefreshPayload))
}

func (q *queue) clear() {
	q.mu.Lock()

	subscribers := make([]chan []byte, 0, len(q.subscribers))
	for _, subscriber := range q.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	q.items = nil
	q.head = 0
	q.subscribers = nil
	q.mu.Unlock()

	for _, subscriber := range subscribers {
		close(subscriber)
	}
}

func (q *queue) closeSubscribers() {
	q.mu.Lock()
	subscribers := make([]chan []byte, 0, len(q.subscribers))
	for _, subscriber := range q.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	q.subscribers = nil
	q.mu.Unlock()
	for _, subscriber := range subscribers {
		close(subscriber)
	}
}

func (q *queue) enqueue(payload []byte) bool {
	now := time.Now()

	q.mu.Lock()
	defer q.mu.Unlock()
	if !Enabled() {
		return false
	}

	q.pruneLocked(now)
	q.items = append(q.items, queueItem{
		enqueuedAt: now,
		payload:    append([]byte(nil), payload...),
	})
	q.maybeCompactLocked()
	return true
}

func (q *queue) publishToSubscribers(payload []byte) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.subscribers) == 0 {
		return false
	}

	for id, subscriber := range q.subscribers {
		cloned := append([]byte(nil), payload...)
		select {
		case subscriber <- cloned:
		default:
			delete(q.subscribers, id)
			close(subscriber)
		}
	}

	return true
}

func (q *queue) subscribe(buffer int, initialPayload []byte) (<-chan []byte, func()) {
	subscriber := make(chan []byte, buffer)
	if len(initialPayload) > 0 {
		subscriber <- append([]byte(nil), initialPayload...)
	}

	q.mu.Lock()
	if q.subscribers == nil {
		q.subscribers = make(map[uint64]chan []byte)
	}
	q.nextSubscriberID++
	id := q.nextSubscriberID
	q.subscribers[id] = subscriber
	q.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			q.unsubscribe(id)
		})
	}
	return subscriber, unsubscribe
}

func (q *queue) unsubscribe(id uint64) {
	q.mu.Lock()
	subscriber, ok := q.subscribers[id]
	if ok {
		delete(q.subscribers, id)
	}
	q.mu.Unlock()

	if ok {
		close(subscriber)
	}
}

func (q *queue) popOldest(count int) [][]byte {
	now := time.Now()

	q.mu.Lock()
	defer q.mu.Unlock()

	q.pruneLocked(now)
	available := len(q.items) - q.head
	if available <= 0 {
		q.items = nil
		q.head = 0
		return nil
	}
	if count > available {
		count = available
	}

	out := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		item := q.items[q.head+i]
		out = append(out, item.payload)
	}
	q.head += count
	q.maybeCompactLocked()
	return out
}

func (q *queue) pruneLocked(now time.Time) {
	if q.head >= len(q.items) {
		q.items = nil
		q.head = 0
		return
	}

	windowSeconds := retentionSeconds.Load()
	if windowSeconds <= 0 {
		windowSeconds = defaultRetentionSeconds
	}
	cutoff := now.Add(-time.Duration(windowSeconds) * time.Second)
	for q.head < len(q.items) && q.items[q.head].enqueuedAt.Before(cutoff) {
		q.head++
	}
}

func (q *queue) maybeCompactLocked() {
	if q.head == 0 {
		return
	}
	if q.head >= len(q.items) {
		q.items = nil
		q.head = 0
		return
	}
	if q.head < 1024 && q.head*2 < len(q.items) {
		return
	}
	q.items = append([]queueItem(nil), q.items[q.head:]...)
	q.head = 0
}

func (q *queue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pruneLocked(time.Now())
	return len(q.items) - q.head
}
