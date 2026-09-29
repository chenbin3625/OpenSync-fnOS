package service

import (
	"context"
	"log"
	"opensync/internal/config"
	"sync"
	"sync/atomic"
	"time"

	"github.com/robfig/cron/v3"
)

const taskRetentionCleanupCronSpec = "0 0 3 * * *"

var (
	taskRetentionSchedulerMu sync.Mutex
	taskRetentionCron        *cron.Cron
)

// Every cleanup — startup, config save, the daily cron — runs under one shared
// context and is counted in taskRetentionWG, so shutdown can cancel whichever
// is in flight and wait for it to leave the database before ShutdownDB closes
// the handle underneath it.
var (
	taskRetentionLifecycleMu sync.Mutex
	taskRetentionCtx         context.Context
	taskRetentionCancel      context.CancelFunc
	taskRetentionStopped     bool
	taskRetentionWG          sync.WaitGroup
)

func init() {
	taskRetentionCtx, taskRetentionCancel = context.WithCancel(context.Background())
}

// RunTaskRetentionCleanup deletes task history older than the configured
// retention window, inline. It is skipped when another cleanup is already
// running or shutdown has begun.
func RunTaskRetentionCleanup() {
	runTaskRetentionCleanup(false)
}

// taskRetentionRunning admits one cleanup at a time. Two concurrent runs would
// contend on the same rows and could both decide to VACUUM.
var taskRetentionRunning atomic.Bool

// runTaskRetentionCleanup admits a single cleanup and runs it inline or in the
// background. It reports whether a cleanup was started.
func runTaskRetentionCleanup(async bool) bool {
	if !taskRetentionRunning.CompareAndSwap(false, true) {
		return false
	}
	taskRetentionLifecycleMu.Lock()
	if taskRetentionStopped {
		taskRetentionLifecycleMu.Unlock()
		taskRetentionRunning.Store(false)
		return false
	}
	// Add under the lock that stopTaskRetentionCleanup takes before Wait, so
	// no Add can race a Wait that already started.
	taskRetentionWG.Add(1)
	ctx := taskRetentionCtx
	taskRetentionLifecycleMu.Unlock()

	run := func() {
		defer taskRetentionWG.Done()
		defer taskRetentionRunning.Store(false)
		// Without this recover a panic in the cleanup would take the whole
		// process down, since it no longer runs inside a request handler that
		// has one.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("panic during task retention cleanup: %v", r)
			}
		}()
		cleanupExpiredTasksContext(ctx, log.Default(), config.GetConfig().Server.TaskSave, time.Now())
	}
	if async {
		go run()
	} else {
		run()
	}
	return true
}

// stopTaskRetentionCleanup cancels any cleanup in flight, refuses new ones, and
// waits for the running one to return, bounded by ctx. The cleanup checks its
// context between delete batches and a running VACUUM is interrupted, so the
// wait is normally short; it is bounded anyway because the caller is about to
// close the database.
func stopTaskRetentionCleanup(ctx context.Context) bool {
	taskRetentionLifecycleMu.Lock()
	taskRetentionStopped = true
	taskRetentionCancel()
	taskRetentionLifecycleMu.Unlock()

	done := make(chan struct{})
	go func() {
		taskRetentionWG.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		log.Printf("Task history cleanup did not stop before the shutdown deadline")
		return false
	}
}

// resetTaskRetentionCleanupForTest re-arms the cleanup after a test stopped it.
func resetTaskRetentionCleanupForTest() {
	taskRetentionLifecycleMu.Lock()
	defer taskRetentionLifecycleMu.Unlock()
	taskRetentionStopped = false
	taskRetentionCtx, taskRetentionCancel = context.WithCancel(context.Background())
}

// StartTaskRetentionCleanupAsync runs the cleanup in the background and reports
// whether this call started it.
//
// The cleanup deletes expired history in batches, checkpoints the WAL and may
// VACUUM, which rewrites the entire database. Running it inline left the caller
// (PUT /svr/system/config) holding the HTTP request open for as long as that
// took, so saving a setting appeared to hang on a large history. Nothing in the
// response depends on the result: the new retention window is already saved,
// and the daily scheduler would apply it anyway.
//
// A cleanup already in progress is left to finish instead of being queued
// again — the work is idempotent, so a second pass would find nothing to do.
// After shutdown began no cleanup is started.
func StartTaskRetentionCleanupAsync() bool {
	return runTaskRetentionCleanup(true)
}

// StartTaskRetentionScheduler runs cleanup daily at 03:00 in the scheduler timezone.
// The returned stop function shuts down the scheduler and is safe to call multiple times.
func StartTaskRetentionScheduler() func() {
	taskRetentionSchedulerMu.Lock()
	defer taskRetentionSchedulerMu.Unlock()

	if taskRetentionCron != nil {
		return stopTaskRetentionScheduler
	}

	loc := schedulerLocation()
	c := cron.New(cron.WithSeconds(), cron.WithLocation(loc))
	if _, err := c.AddFunc(taskRetentionCleanupCronSpec, RunTaskRetentionCleanup); err != nil {
		log.Printf("Failed to schedule task retention cleanup: %v", err)
		return func() {}
	}
	c.Start()
	taskRetentionCron = c
	log.Printf("Task history cleanup scheduled daily at 03:00 (%s)", loc.String())

	return stopTaskRetentionScheduler
}

func stopTaskRetentionScheduler() {
	taskRetentionSchedulerMu.Lock()
	c := taskRetentionCron
	taskRetentionCron = nil
	taskRetentionSchedulerMu.Unlock()

	if c == nil {
		return
	}
	stopCronWithTimeout(c, "task retention scheduler")
}
