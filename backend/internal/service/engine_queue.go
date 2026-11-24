package service

import (
	"context"
	"sync"
	"time"
)

type engineQueueWaiter struct {
	jobID int64
	limit int
	ch    chan struct{}
}

type engineQueueBucket struct {
	active  map[int64]struct{}
	waiters []*engineQueueWaiter
}

// EngineQueueManager enforces per-engine maxConcurrentJobs limits with a FIFO
// cancellable wait queue while tracking active and queued counts per engine.
type EngineQueueManager struct {
	mu          sync.Mutex
	buckets     map[int64]*engineQueueBucket
	queuedJobs  map[int64]int64 // jobID -> alistID
	queuedSince map[int64]int64 // jobID -> unix timestamp
}

func newEngineQueueManager() *EngineQueueManager {
	return &EngineQueueManager{
		buckets:     make(map[int64]*engineQueueBucket),
		queuedJobs:  make(map[int64]int64),
		queuedSince: make(map[int64]int64),
	}
}

var engineQueue = newEngineQueueManager()

func (m *EngineQueueManager) bucketLocked(alistID int64) *engineQueueBucket {
	b, ok := m.buckets[alistID]
	if !ok {
		b = &engineQueueBucket{
			active: make(map[int64]struct{}),
		}
		m.buckets[alistID] = b
	}
	return b
}

// Acquire claims an execution slot on alistID for jobID. When limit <= 0, no
// concurrency cap is enforced (the slot is granted immediately and still
// tracked for activeJobs reporting). When limit > 0 and active >= limit, the
// caller blocks in FIFO order until a slot opens or ctx is cancelled.
func (m *EngineQueueManager) Acquire(ctx context.Context, alistID, jobID int64, limit int) (func(), error) {
	if alistID <= 0 {
		return func() {}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.Lock()
	b := m.bucketLocked(alistID)
	if limit <= 0 || len(b.active) < limit {
		b.active[jobID] = struct{}{}
		m.mu.Unlock()
		return m.makeRelease(alistID, jobID), nil
	}

	waiter := &engineQueueWaiter{
		jobID: jobID,
		limit: limit,
		ch:    make(chan struct{}),
	}
	b.waiters = append(b.waiters, waiter)
	m.queuedJobs[jobID] = alistID
	m.queuedSince[jobID] = time.Now().Unix()
	m.mu.Unlock()

	select {
	case <-waiter.ch:
		return m.makeRelease(alistID, jobID), nil
	case <-ctx.Done():
		m.mu.Lock()
		// Check whether a releaser already granted us the slot while ctx was
		// being cancelled.
		select {
		case <-waiter.ch:
			// We were granted the slot; release it immediately for the next waiter.
			m.releaseLocked(alistID, jobID, limit)
			m.mu.Unlock()
			return nil, ctx.Err()
		default:
			m.removeWaiterLocked(alistID, waiter)
			delete(m.queuedJobs, jobID)
			delete(m.queuedSince, jobID)
			m.mu.Unlock()
			return nil, ctx.Err()
		}
	}
}

func (m *EngineQueueManager) makeRelease(alistID, jobID int64) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			m.releaseLocked(alistID, jobID, -1)
			m.mu.Unlock()
		})
	}
}

func (m *EngineQueueManager) releaseLocked(alistID, jobID int64, overrideLimit int) {
	b, ok := m.buckets[alistID]
	if !ok {
		return
	}
	delete(b.active, jobID)
	m.promoteWaitersLocked(alistID, b, overrideLimit)
	if len(b.active) == 0 && len(b.waiters) == 0 {
		delete(m.buckets, alistID)
	}
}

func (m *EngineQueueManager) promoteWaitersLocked(alistID int64, b *engineQueueBucket, overrideLimit int) {
	for len(b.waiters) > 0 {
		next := b.waiters[0]
		effectiveLimit := next.limit
		if overrideLimit >= 0 {
			effectiveLimit = overrideLimit
		}
		if effectiveLimit > 0 && len(b.active) >= effectiveLimit {
			return
		}
		b.waiters = b.waiters[1:]
		delete(m.queuedJobs, next.jobID)
		delete(m.queuedSince, next.jobID)
		b.active[next.jobID] = struct{}{}
		close(next.ch)
	}
}

func (m *EngineQueueManager) removeWaiterLocked(alistID int64, target *engineQueueWaiter) {
	b, ok := m.buckets[alistID]
	if !ok {
		return
	}
	for i, w := range b.waiters {
		if w == target {
			b.waiters = append(b.waiters[:i], b.waiters[i+1:]...)
			break
		}
	}
	if len(b.active) == 0 && len(b.waiters) == 0 {
		delete(m.buckets, alistID)
	}
}

// RefreshLimit updates waiters for alistID after an engine's maxConcurrentJobs
// setting changes, waking queued jobs if more slots are now available.
func (m *EngineQueueManager) RefreshLimit(alistID int64, newLimit int) {
	if alistID <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.buckets[alistID]
	if !ok {
		return
	}
	for _, w := range b.waiters {
		w.limit = newLimit
	}
	m.promoteWaitersLocked(alistID, b, newLimit)
}

// IsJobQueued reports whether jobID is currently waiting in an engine queue.
func (m *EngineQueueManager) IsJobQueued(jobID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.queuedJobs[jobID]
	return ok
}

// EngineStats returns current active and queued job counts for an engine.
func (m *EngineQueueManager) EngineStats(alistID int64) (active int, queued int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.buckets[alistID]
	if !ok {
		return 0, 0
	}
	return len(b.active), len(b.waiters)
}

// AllQueuedJobIDs returns a snapshot set of currently queued job IDs.
func (m *EngineQueueManager) AllQueuedJobIDs() map[int64]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[int64]bool, len(m.queuedJobs))
	for jobID := range m.queuedJobs {
		out[jobID] = true
	}
	return out
}
