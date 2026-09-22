package service

import (
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

// RunTaskRetentionCleanup deletes task history older than the configured retention window.
func RunTaskRetentionCleanup() {
	CleanupExpiredTasks(log.Default(), config.GetConfig().Server.TaskSave, time.Now())
}

// taskRetentionRunning admits one cleanup at a time. Two concurrent runs would
// contend on the same rows and could both decide to VACUUM.
var taskRetentionRunning atomic.Bool

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
func StartTaskRetentionCleanupAsync() bool {
	if !taskRetentionRunning.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		defer taskRetentionRunning.Store(false)
		// Without this recover a panic in the cleanup would take the whole
		// process down, since it no longer runs inside a request handler that
		// has one.
		defer func() {
			if r := recover(); r != nil {
				log.Printf("panic during task retention cleanup: %v", r)
			}
		}()
		RunTaskRetentionCleanup()
	}()
	return true
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
