package search

import (
	"context"
	"sync"
	"time"
)

const (
	defaultBleveCoordinatorMaxActive     = 2
	defaultBleveCoordinatorMaxBytes      = 32 * 1024 * 1024
	defaultBleveCoordinatorAgingInterval = 30 * time.Second
	defaultBleveCoordinatorDiagnostic    = 5 * time.Second
)

type bleveWritePriority uint8

const (
	bleveWriteBackground bleveWritePriority = iota
	bleveWriteNormal
	bleveWriteForeground
)

func (p bleveWritePriority) String() string {
	switch p {
	case bleveWriteBackground:
		return "background"
	case bleveWriteForeground:
		return "foreground"
	default:
		return "normal"
	}
}

type bleveWriteRequest struct {
	Details  bleveErrorContext
	Priority bleveWritePriority
	Bytes    uint64
}

type bleveWriteWaitDiagnostic struct {
	Request        bleveWriteRequest
	Waited         time.Duration
	QueueDepth     int
	ActiveWrites   int
	ActiveBytes    uint64
	SameUserActive bool
}

type bleveWriteCoordinatorConfig struct {
	MaxActive       int
	MaxActiveBytes  uint64
	AgingInterval   time.Duration
	DiagnosticAfter time.Duration
	OnWait          func(bleveWriteWaitDiagnostic)
}

type bleveWriteCoordinator struct {
	mu sync.Mutex

	maxActive       int
	maxActiveBytes  uint64
	agingInterval   time.Duration
	diagnosticAfter time.Duration
	onWait          func(bleveWriteWaitDiagnostic)

	nextSequence uint64
	waiters      []*bleveWriteWaiter
	activeUsers  map[int64]int
	activeWrites int
	activeBytes  uint64
}

type bleveWriteWaiter struct {
	request  bleveWriteRequest
	queuedAt time.Time
	sequence uint64
	ready    chan *bleveWriteLease
	state    bleveWriteWaiterState
	lease    *bleveWriteLease
}

type bleveWriteWaiterState uint8

const (
	bleveWaiterQueued bleveWriteWaiterState = iota
	bleveWaiterGranted
	bleveWaiterCanceled
)

type bleveWriteLease struct {
	coordinator *bleveWriteCoordinator
	userID      int64
	bytes       uint64
	released    bool
	once        sync.Once
}

func newBleveWriteCoordinator(config bleveWriteCoordinatorConfig) *bleveWriteCoordinator {
	if config.MaxActive <= 0 {
		config.MaxActive = defaultBleveCoordinatorMaxActive
	}
	if config.MaxActiveBytes == 0 {
		config.MaxActiveBytes = defaultBleveCoordinatorMaxBytes
	}
	if config.AgingInterval <= 0 {
		config.AgingInterval = defaultBleveCoordinatorAgingInterval
	}
	if config.DiagnosticAfter <= 0 {
		config.DiagnosticAfter = defaultBleveCoordinatorDiagnostic
	}
	return &bleveWriteCoordinator{
		maxActive: config.MaxActive, maxActiveBytes: config.MaxActiveBytes,
		agingInterval: config.AgingInterval, diagnosticAfter: config.DiagnosticAfter,
		onWait: config.OnWait, activeUsers: make(map[int64]int),
	}
}

func (c *bleveWriteCoordinator) Acquire(ctx context.Context, request bleveWriteRequest) (*bleveWriteLease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.Priority > bleveWriteForeground {
		request.Priority = bleveWriteNormal
	}
	waiter := &bleveWriteWaiter{
		request: request, queuedAt: time.Now(), ready: make(chan *bleveWriteLease, 1),
		state: bleveWaiterQueued,
	}
	c.mu.Lock()
	waiter.sequence = c.nextSequence
	c.nextSequence++
	c.waiters = append(c.waiters, waiter)
	c.dispatchLocked(time.Now())
	c.mu.Unlock()

	timer := time.NewTimer(c.diagnosticAfter)
	defer timer.Stop()
	diagnosticC := timer.C
	for {
		select {
		case lease := <-waiter.ready:
			if err := ctx.Err(); err != nil {
				lease.Release()
				return nil, err
			}
			return lease, nil
		case <-ctx.Done():
			lease := c.cancelWaiter(waiter)
			if lease != nil {
				lease.Release()
			}
			return nil, ctx.Err()
		case <-diagnosticC:
			if diagnostic, ok := c.waitDiagnostic(waiter); ok && c.onWait != nil {
				go c.emitWaitDiagnostic(diagnostic)
			}
			diagnosticC = nil
		}
	}
}

func (c *bleveWriteCoordinator) emitWaitDiagnostic(diagnostic bleveWriteWaitDiagnostic) {
	defer func() {
		_ = recover()
	}()
	c.onWait(diagnostic)
}

func (c *bleveWriteCoordinator) cancelWaiter(waiter *bleveWriteWaiter) *bleveWriteLease {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch waiter.state {
	case bleveWaiterGranted:
		return waiter.lease
	case bleveWaiterQueued:
		for index, queued := range c.waiters {
			if queued != waiter {
				continue
			}
			copy(c.waiters[index:], c.waiters[index+1:])
			c.waiters = c.waiters[:len(c.waiters)-1]
			break
		}
		waiter.state = bleveWaiterCanceled
		c.dispatchLocked(time.Now())
	}
	return nil
}

func (c *bleveWriteCoordinator) waitDiagnostic(waiter *bleveWriteWaiter) (bleveWriteWaitDiagnostic, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if waiter.state != bleveWaiterQueued {
		return bleveWriteWaitDiagnostic{}, false
	}
	return bleveWriteWaitDiagnostic{
		Request: waiter.request, Waited: time.Since(waiter.queuedAt), QueueDepth: len(c.waiters),
		ActiveWrites: c.activeWrites, ActiveBytes: c.activeBytes,
		SameUserActive: c.activeUsers[waiter.request.Details.UserID] > 0,
	}, true
}

func (c *bleveWriteCoordinator) dispatchLocked(now time.Time) {
	for c.activeWrites < c.maxActive {
		candidate := c.bestCandidateLocked(now)
		if candidate < 0 {
			return
		}
		waiter := c.waiters[candidate]
		copy(c.waiters[candidate:], c.waiters[candidate+1:])
		c.waiters = c.waiters[:len(c.waiters)-1]
		lease := &bleveWriteLease{
			coordinator: c, userID: waiter.request.Details.UserID, bytes: waiter.request.Bytes,
		}
		waiter.state = bleveWaiterGranted
		waiter.lease = lease
		c.activeWrites++
		c.activeBytes = saturatingAdd(c.activeBytes, waiter.request.Bytes)
		c.activeUsers[lease.userID]++
		waiter.ready <- lease
	}
}

func (c *bleveWriteCoordinator) bestCandidateLocked(now time.Time) int {
	best := -1
	bestPriority := -1
	var bestSequence uint64
	for index, waiter := range c.waiters {
		if waiter.state != bleveWaiterQueued || c.activeUsers[waiter.request.Details.UserID] > 0 {
			continue
		}
		priority := int(waiter.request.Priority)
		if c.agingInterval > 0 {
			priority += int(now.Sub(waiter.queuedAt) / c.agingInterval)
		}
		if priority > int(bleveWriteForeground) {
			priority = int(bleveWriteForeground)
		}
		if best < 0 || priority > bestPriority || (priority == bestPriority && waiter.sequence < bestSequence) {
			best = index
			bestPriority = priority
			bestSequence = waiter.sequence
		}
	}
	if best >= 0 && !c.bytesAvailableLocked(c.waiters[best].request.Bytes) {
		// Let active leases drain for the highest-ranked runnable tenant. Letting
		// smaller later jobs fill the remaining bytes here could starve an older
		// or aged large batch forever under a steady indexing workload.
		return -1
	}
	return best
}

func (c *bleveWriteCoordinator) bytesAvailableLocked(bytes uint64) bool {
	if c.activeWrites == 0 {
		return true
	}
	if bytes > c.maxActiveBytes {
		return false
	}
	return c.activeBytes <= c.maxActiveBytes-bytes
}

func (l *bleveWriteLease) Release() {
	if l == nil || l.coordinator == nil {
		return
	}
	l.once.Do(func() {
		coordinator := l.coordinator
		coordinator.mu.Lock()
		l.released = true
		if coordinator.activeWrites > 0 {
			coordinator.activeWrites--
		}
		if coordinator.activeBytes >= l.bytes {
			coordinator.activeBytes -= l.bytes
		} else {
			coordinator.activeBytes = 0
		}
		if coordinator.activeUsers[l.userID] <= 1 {
			delete(coordinator.activeUsers, l.userID)
		} else {
			coordinator.activeUsers[l.userID]--
		}
		coordinator.dispatchLocked(time.Now())
		coordinator.mu.Unlock()
	})
}

// UpdateBytes reconciles a projection-based admission with Bleve's actual
// mapped document size. The lease remains admitted; an increase may temporarily
// exceed the soft byte target, but prevents any further dispatch until capacity
// is available again.
func (l *bleveWriteLease) UpdateBytes(bytes uint64) {
	if l == nil || l.coordinator == nil {
		return
	}
	coordinator := l.coordinator
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if l.released || l.bytes == bytes {
		return
	}
	if coordinator.activeBytes >= l.bytes {
		coordinator.activeBytes -= l.bytes
	} else {
		coordinator.activeBytes = 0
	}
	coordinator.activeBytes = saturatingAdd(coordinator.activeBytes, bytes)
	l.bytes = bytes
	coordinator.dispatchLocked(time.Now())
}

type bleveWritePriorityContextKey struct{}

// WithBackgroundIndexing labels derived attachment/search enrichment so direct
// user maintenance and current sync commits can be admitted first.
func WithBackgroundIndexing(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, bleveWritePriorityContextKey{}, bleveWriteBackground)
}

// WithForegroundIndexing labels an explicit user-requested rebuild or repair.
func WithForegroundIndexing(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, bleveWritePriorityContextKey{}, bleveWriteForeground)
}

func blevePriorityForOperation(ctx context.Context, operation string) bleveWritePriority {
	if ctx != nil {
		if priority, ok := ctx.Value(bleveWritePriorityContextKey{}).(bleveWritePriority); ok && priority <= bleveWriteForeground {
			return priority
		}
	}
	switch operation {
	case "delete-batch", "purge-mailbox-batch":
		return bleveWriteForeground
	default:
		return bleveWriteNormal
	}
}
