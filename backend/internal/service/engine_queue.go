package service

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

type engineQueueWaiter struct {
	jobID        int64
	limit        int
	storages     []string
	storageLimit int
	ch           chan struct{}
}

type engineQueueBucket struct {
	active  map[int64]struct{}
	waiters []*engineQueueWaiter
}

// EngineQueueManager enforces per-engine and optional per-storage concurrency
// limits with a FIFO cancellable wait queue while tracking active and queued
// counts both per engine and per top-level storage name (e.g. "本地", "oss").
type EngineQueueManager struct {
	mu            sync.Mutex
	buckets       map[int64]*engineQueueBucket
	storageActive map[string]map[int64]struct{}
	storageQueued map[string]map[int64]struct{}
	queuedJobs    map[int64]int64 // jobID -> alistID
	queuedSince   map[int64]int64 // jobID -> unix timestamp
}

func newEngineQueueManager() *EngineQueueManager {
	return &EngineQueueManager{
		buckets:       make(map[int64]*engineQueueBucket),
		storageActive: make(map[string]map[int64]struct{}),
		storageQueued: make(map[string]map[int64]struct{}),
		queuedJobs:    make(map[int64]int64),
		queuedSince:   make(map[int64]int64),
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

func normalizeQueueStorages(storages []string) []string {
	if len(storages) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(storages))
	out := make([]string, 0, len(storages))
	for _, s := range storages {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func (m *EngineQueueManager) storagesAvailableLocked(storages []string, storageLimit int) bool {
	if storageLimit <= 0 || len(storages) == 0 {
		return true
	}
	for _, st := range storages {
		if len(m.storageActive[st]) >= storageLimit {
			return false
		}
	}
	return true
}

func (m *EngineQueueManager) addStorageActiveLocked(jobID int64, storages []string) {
	for _, st := range storages {
		set := m.storageActive[st]
		if set == nil {
			set = make(map[int64]struct{})
			m.storageActive[st] = set
		}
		set[jobID] = struct{}{}
	}
}

func (m *EngineQueueManager) removeStorageActiveLocked(jobID int64, storages []string) {
	for _, st := range storages {
		if set := m.storageActive[st]; set != nil {
			delete(set, jobID)
			if len(set) == 0 {
				delete(m.storageActive, st)
			}
		}
	}
}

func (m *EngineQueueManager) addStorageQueuedLocked(jobID int64, storages []string) {
	for _, st := range storages {
		set := m.storageQueued[st]
		if set == nil {
			set = make(map[int64]struct{})
			m.storageQueued[st] = set
		}
		set[jobID] = struct{}{}
	}
}

func (m *EngineQueueManager) removeStorageQueuedLocked(jobID int64, storages []string) {
	for _, st := range storages {
		if set := m.storageQueued[st]; set != nil {
			delete(set, jobID)
			if len(set) == 0 {
				delete(m.storageQueued, st)
			}
		}
	}
}

// Acquire claims an execution slot on alistID for jobID. When limit <= 0, no
// concurrency cap is enforced (the slot is granted immediately and still
// tracked for activeJobs reporting).
func (m *EngineQueueManager) Acquire(ctx context.Context, alistID, jobID int64, limit int) (func(), error) {
	return m.AcquireWithStorages(ctx, alistID, jobID, limit, nil, 0)
}

// AcquireWithStorages atomically claims an execution slot on alistID and all
// referenced storage buckets (deadlock-free single lock). When storageLimit > 0,
// each referenced storage is independently capped at storageLimit concurrent jobs.
func (m *EngineQueueManager) AcquireWithStorages(ctx context.Context, alistID, jobID int64, limit int, storages []string, storageLimit int) (func(), error) {
	normStorages := normalizeQueueStorages(storages)
	if alistID <= 0 && len(normStorages) == 0 {
		return func() {}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.Lock()
	b := m.bucketLocked(alistID)
	engineOK := limit <= 0 || len(b.active) < limit
	storageOK := m.storagesAvailableLocked(normStorages, storageLimit)
	if engineOK && storageOK {
		b.active[jobID] = struct{}{}
		m.addStorageActiveLocked(jobID, normStorages)
		m.mu.Unlock()
		return m.makeRelease(alistID, jobID, normStorages), nil
	}

	waiter := &engineQueueWaiter{
		jobID:        jobID,
		limit:        limit,
		storages:     normStorages,
		storageLimit: storageLimit,
		ch:           make(chan struct{}),
	}
	b.waiters = append(b.waiters, waiter)
	m.queuedJobs[jobID] = alistID
	m.queuedSince[jobID] = time.Now().Unix()
	m.addStorageQueuedLocked(jobID, normStorages)
	m.mu.Unlock()

	select {
	case <-waiter.ch:
		return m.makeRelease(alistID, jobID, normStorages), nil
	case <-ctx.Done():
		m.mu.Lock()
		select {
		case <-waiter.ch:
			m.releaseLocked(alistID, jobID, normStorages, limit)
			m.mu.Unlock()
			return nil, ctx.Err()
		default:
			m.removeWaiterLocked(alistID, waiter)
			delete(m.queuedJobs, jobID)
			delete(m.queuedSince, jobID)
			m.removeStorageQueuedLocked(jobID, normStorages)
			m.mu.Unlock()
			return nil, ctx.Err()
		}
	}
}

func (m *EngineQueueManager) makeRelease(alistID, jobID int64, storages []string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			m.releaseLocked(alistID, jobID, storages, -1)
			m.mu.Unlock()
		})
	}
}

func (m *EngineQueueManager) releaseLocked(alistID, jobID int64, storages []string, overrideLimit int) {
	m.removeStorageActiveLocked(jobID, storages)
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
	i := 0
	for i < len(b.waiters) {
		next := b.waiters[i]
		effectiveLimit := next.limit
		if overrideLimit >= 0 {
			effectiveLimit = overrideLimit
		}
		if effectiveLimit > 0 && len(b.active) >= effectiveLimit {
			// Engine itself is at capacity; no further waiter on this engine can start.
			return
		}
		if !m.storagesAvailableLocked(next.storages, next.storageLimit) {
			// This waiter's remote storage is busy; allow independent jobs on other storages to proceed.
			i++
			continue
		}
		b.waiters = append(b.waiters[:i], b.waiters[i+1:]...)
		delete(m.queuedJobs, next.jobID)
		delete(m.queuedSince, next.jobID)
		m.removeStorageQueuedLocked(next.jobID, next.storages)
		b.active[next.jobID] = struct{}{}
		m.addStorageActiveLocked(next.jobID, next.storages)
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

// StorageStats returns current active and queued job counts for a top-level
// storage name (e.g. "本地" or a configured rclone remote name).
func (m *EngineQueueManager) StorageStats(storageName string) (active int, queued int) {
	storageName = strings.TrimSpace(storageName)
	if storageName == "" {
		return 0, 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.storageActive[storageName]), len(m.storageQueued[storageName])
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
