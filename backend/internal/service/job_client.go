package service

import (
	"context"
	"fmt"
	"log"
	"opensync/internal/config"
	"opensync/internal/mapper"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// JobClient manages a single job's lifecycle
type JobClient struct {
	JobID          int64
	Job            map[string]interface{}
	Scheduler      *Scheduler
	JobDoing       bool
	CurrentJobTask *JobTask
	mu             sync.Mutex
	stateCh        chan struct{}
	// opMu serializes configuration changes (edit, delete, pause, resume) so
	// they cannot interleave: an edit racing a delete used to start a new
	// scheduler for a job whose row was already gone. It is held across slow
	// steps (waiting for a task to stop), so it must never be taken under mu.
	opMu sync.Mutex
	// deleted is set, under opMu, once the job row is removed. Operations that
	// were waiting on opMu for this client then fail instead of reviving it.
	deleted bool
	// closing is set while a delete attempt is in progress (and stays set once
	// the row is gone). runMarkedJobConfig checks it before creating or starting
	// a task, so a cron tick racing a delete cannot create a task for a job that
	// is about to disappear.
	closing atomic.Bool
	// retrySourceID is the historical task whose items the current run replays,
	// guarded by mu. RemoveTask refuses to delete it while the replay is
	// running: the replay reads the source task's items in id-ordered batches,
	// so deleting them mid-run silently drops the remainder.
	retrySourceID int64
	// queueCancel cancels an in-flight wait on engineQueue.Acquire so stop,
	// pause, delete, and shutdown abort queued jobs immediately.
	queueCancel context.CancelFunc
	// autoRetryTimer schedules delayed automatic retries after a failed run.
	autoRetryMu    sync.Mutex
	autoRetryCount int
	autoRetryTimer *time.Timer
}

const maxPostSyncChainDepth = 5

func (jc *JobClient) cancelAutoRetry() {
	jc.autoRetryMu.Lock()
	if jc.autoRetryTimer != nil {
		jc.autoRetryTimer.Stop()
		jc.autoRetryTimer = nil
	}
	jc.autoRetryMu.Unlock()
}

func (jc *JobClient) resetAutoRetry() {
	jc.autoRetryMu.Lock()
	if jc.autoRetryTimer != nil {
		jc.autoRetryTimer.Stop()
		jc.autoRetryTimer = nil
	}
	jc.autoRetryCount = 0
	jc.autoRetryMu.Unlock()
}

func (jc *JobClient) scheduleAutoRetry(failedTaskID int64) {
	if jc == nil || jc.isClosing() || !jc.enabled() {
		return
	}
	job := jc.jobSnapshot()
	if job == nil {
		return
	}
	extra, _, err := parseNativeExtraFlags(job["extraFlags"])
	if err != nil || extra.AutoRetryMax <= 0 {
		return
	}
	delayMin := extra.AutoRetryDelayMin
	if delayMin <= 0 {
		delayMin = 5
	}
	jc.autoRetryMu.Lock()
	if jc.autoRetryCount >= extra.AutoRetryMax {
		jc.autoRetryMu.Unlock()
		return
	}
	jc.autoRetryCount++
	attempt := jc.autoRetryCount
	if jc.autoRetryTimer != nil {
		jc.autoRetryTimer.Stop()
	}
	jobID := jc.idSnapshot()
	jc.autoRetryTimer = time.AfterFunc(time.Duration(delayMin)*time.Minute, func() {
		jc.autoRetryMu.Lock()
		jc.autoRetryTimer = nil
		jc.autoRetryMu.Unlock()
		if jc.isClosing() || !jc.enabled() {
			return
		}
		if !JobInTimeWindow(jc.jobSnapshot(), time.Now()) {
			log.Printf("Auto-retry #%d for job %d skipped: outside run window", attempt, jobID)
			return
		}
		if failedTaskID > 0 && util.ToInt(jc.jobSnapshot()["method"]) != 3 {
			if retryErr := jc.DoRetryFailedTaskItems(failedTaskID); retryErr == nil {
				log.Printf("Auto-retry #%d/%d started for job %d (replaying failed items of task %d)", attempt, extra.AutoRetryMax, jobID, failedTaskID)
				return
			}
		}
		if jc.tryMarkDoing() {
			log.Printf("Auto-retry #%d/%d started full rerun for job %d", attempt, extra.AutoRetryMax, jobID)
			go jc.runMarkedJob()
		}
	})
	jc.autoRetryMu.Unlock()
	log.Printf("Scheduled auto-retry #%d/%d for job %d in %d minute(s)", attempt, extra.AutoRetryMax, jobID, delayMin)
}

func (jc *JobClient) setQueueCancel(cancel context.CancelFunc) {
	jc.mu.Lock()
	jc.queueCancel = cancel
	jc.mu.Unlock()
}

func (jc *JobClient) cancelQueueWait() {
	jc.mu.Lock()
	cancel := jc.queueCancel
	jc.queueCancel = nil
	jc.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// lockOp takes the configuration lock and reports whether the job still
// exists. The caller must call opMu.Unlock, whether ok is true or false.
func (jc *JobClient) lockOp() bool {
	jc.opMu.Lock()
	return jc.lockOpLocked()
}

// lockOpLocked is lockOp without taking the lock; the caller already holds opMu.
func (jc *JobClient) lockOpLocked() bool {
	return !jc.deleted && !jc.closing.Load()
}

// beginClose marks the job as being torn down and returns false when it was
// already closing/deleted.
func (jc *JobClient) beginCloseLocked() bool {
	if jc.deleted || jc.closing.Load() {
		return false
	}
	jc.closing.Store(true)
	return true
}

func (jc *JobClient) isClosing() bool {
	return jc.closing.Load()
}

// setRetrySource records the task the current run replays (0 clears it).
func (jc *JobClient) setRetrySource(taskID int64) {
	jc.mu.Lock()
	jc.retrySourceID = taskID
	jc.mu.Unlock()
}

// retrySource is the historical task the current run replays, or 0.
func (jc *JobClient) retrySource() int64 {
	jc.mu.Lock()
	defer jc.mu.Unlock()
	return jc.retrySourceID
}

func (jc *JobClient) tryMarkDoing() bool {
	jc.mu.Lock()
	defer jc.mu.Unlock()
	if jc.JobDoing {
		return false
	}
	jc.JobDoing = true
	jc.signalStateChangeLocked()
	return true
}

func (jc *JobClient) isDoing() bool {
	jc.mu.Lock()
	defer jc.mu.Unlock()
	return jc.JobDoing
}

func (jc *JobClient) isBusy() bool {
	jc.mu.Lock()
	defer jc.mu.Unlock()
	return jc.JobDoing || jc.CurrentJobTask != nil
}

func (jc *JobClient) markDone() {
	jc.mu.Lock()
	jc.JobDoing = false
	// A run that failed before a task was registered never calls
	// clearCurrentTask, so clear the retry claim here too. When a task is
	// still current, clearCurrentTask releases it (it must stay claimed until
	// the task itself is finished).
	if jc.CurrentJobTask == nil {
		jc.retrySourceID = 0
	}
	jc.signalStateChangeLocked()
	jc.mu.Unlock()
}

func (jc *JobClient) setCurrentTask(task *JobTask) {
	jc.mu.Lock()
	jc.CurrentJobTask = task
	jc.signalStateChangeLocked()
	jc.mu.Unlock()
}

func (jc *JobClient) trySetCurrentTaskIfActive(task *JobTask) bool {
	jc.mu.Lock()
	defer jc.mu.Unlock()
	if jc.Job == nil || util.ToInt(jc.Job["enable"]) != 1 || jc.isClosing() {
		return false
	}
	jc.CurrentJobTask = task
	jc.signalStateChangeLocked()
	return true
}

func (jc *JobClient) currentTask() *JobTask {
	jc.mu.Lock()
	defer jc.mu.Unlock()
	return jc.CurrentJobTask
}

func (jc *JobClient) clearCurrentTask(task *JobTask) {
	jc.mu.Lock()
	if task == nil || jc.CurrentJobTask == task {
		jc.CurrentJobTask = nil
		jc.retrySourceID = 0
		jc.signalStateChangeLocked()
	}
	jc.mu.Unlock()
}

func (jc *JobClient) waitUntilIdle(timeout time.Duration) bool {
	if timeout <= 0 {
		jc.mu.Lock()
		idle := jc.isIdleLocked()
		jc.mu.Unlock()
		return idle
	}
	return jc.waitUntilIdleContext(context.Background(), timeout)
}

func (jc *JobClient) waitUntilIdleContext(ctx context.Context, timeout time.Duration) bool {
	var deadline <-chan time.Time
	var timer *time.Timer
	if timeout > 0 {
		timer = time.NewTimer(timeout)
		defer timer.Stop()
		deadline = timer.C
	}

	for {
		jc.mu.Lock()
		if jc.isIdleLocked() {
			jc.mu.Unlock()
			return true
		}
		stateCh := jc.stateChangeChLocked()
		jc.mu.Unlock()

		select {
		case <-ctx.Done():
			jc.mu.Lock()
			idle := jc.isIdleLocked()
			jc.mu.Unlock()
			return idle
		case <-deadline:
			jc.mu.Lock()
			idle := jc.isIdleLocked()
			jc.mu.Unlock()
			return idle
		case <-stateCh:
		}
	}
}

func (jc *JobClient) isIdleLocked() bool {
	return !jc.JobDoing && jc.CurrentJobTask == nil
}

func (jc *JobClient) stateChangeChLocked() chan struct{} {
	if jc.stateCh == nil {
		jc.stateCh = make(chan struct{})
	}
	return jc.stateCh
}

func (jc *JobClient) signalStateChangeLocked() {
	if jc.stateCh == nil {
		jc.stateCh = make(chan struct{})
		return
	}
	close(jc.stateCh)
	jc.stateCh = make(chan struct{})
}

func (jc *JobClient) setEnable(enable int) {
	jc.mu.Lock()
	if jc.Job != nil {
		jc.Job["enable"] = enable
	}
	jc.mu.Unlock()
}

func (jc *JobClient) enabled() bool {
	jc.mu.Lock()
	defer jc.mu.Unlock()
	return jc.Job != nil && util.ToInt(jc.Job["enable"]) == 1
}

func (jc *JobClient) jobSnapshot() map[string]interface{} {
	jc.mu.Lock()
	defer jc.mu.Unlock()
	return cloneJobConfig(jc.Job)
}

func (jc *JobClient) idSnapshot() int64 {
	jc.mu.Lock()
	defer jc.mu.Unlock()
	return jc.JobID
}

func (jc *JobClient) configSnapshot() (int64, map[string]interface{}, *Scheduler) {
	jc.mu.Lock()
	defer jc.mu.Unlock()
	return jc.JobID, cloneJobConfig(jc.Job), jc.Scheduler
}

// nextRunTime reports when the scheduler will next fire this job. ok is false
// when the job is manual-only, disabled, paused, or has no scheduler.
func (jc *JobClient) nextRunTime() (time.Time, bool) {
	scheduler := jc.schedulerSnapshot()
	if scheduler == nil {
		return time.Time{}, false
	}
	return scheduler.Next()
}

func (jc *JobClient) schedulerSnapshot() *Scheduler {
	jc.mu.Lock()
	defer jc.mu.Unlock()
	return jc.Scheduler
}

func (jc *JobClient) replaceJobConfig(job map[string]interface{}, scheduler *Scheduler) *Scheduler {
	jc.mu.Lock()
	oldScheduler := jc.Scheduler
	jc.JobID = util.ToInt64(job["id"])
	jc.Job = cloneJobConfig(job)
	jc.Scheduler = scheduler
	jc.mu.Unlock()
	return oldScheduler
}

func cloneJobConfig(job map[string]interface{}) map[string]interface{} {
	if job == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(job))
	for key, value := range job {
		switch v := value.(type) {
		case map[string]interface{}:
			cloned[key] = cloneJobConfig(v)
		case []string:
			cloned[key] = append([]string(nil), v...)
		case []interface{}:
			cp := make([]interface{}, len(v))
			for i, elem := range v {
				switch e := elem.(type) {
				case map[string]interface{}:
					cp[i] = cloneJobConfig(e)
				case []interface{}:
					cp[i] = deepCopySlice(e)
				default:
					cp[i] = elem
				}
			}
			cloned[key] = cp
		default:
			cloned[key] = value
		}
	}
	return cloned
}

func deepCopySlice(src []interface{}) []interface{} {
	cp := make([]interface{}, len(src))
	for i, elem := range src {
		switch e := elem.(type) {
		case map[string]interface{}:
			cp[i] = cloneJobConfig(e)
		case []interface{}:
			cp[i] = deepCopySlice(e)
		default:
			cp[i] = elem
		}
	}
	return cp
}

func cloneTaskRows(rows []map[string]interface{}) []map[string]interface{} {
	cloned := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		item := make(map[string]interface{}, len(row))
		for key, value := range row {
			item[key] = value
		}
		cloned = append(cloned, item)
	}
	return cloned
}

func jobTaskHasStatus(taskID int64, statuses ...taskStatus) bool {
	task, err := mapper.GetJobTaskByID(taskID)
	if err != nil {
		return false
	}
	current := taskStatusFromValue(task["status"])
	for _, status := range statuses {
		if current == status {
			return true
		}
	}
	return false
}

// NewJobClient creates a new job client
func NewJobClient(job map[string]interface{}, isInit bool) (*JobClient, error) {
	// Clone first: the defaults below must not mutate the caller's map.
	job = cloneJobConfig(job)
	jc := &JobClient{
		Job: cloneJobConfig(job),
	}

	addJobID := int64(0)
	if _, ok := job["enable"]; !ok {
		job["enable"] = 1
	}
	if _, ok := job["method"]; !ok {
		job["method"] = 0
	}

	// Every construction path runs the same safety validation as job creation.
	// Without this, GetJobClientByID could revive a stored job that startup
	// deliberately refused to load (overlapping source/destination, nested
	// sources) and arm a cron scheduler for it, defeating exactly the checks
	// that keep a mirror sync from deleting outside its destination.
	// rejectNestedDst is relaxed for startup loading, matching InitJobs.
	if err := validateJobInput(job, !isInit); err != nil {
		return nil, err
	}

	if _, ok := job["id"]; !ok {
		id, err := mapper.AddJob(job)
		if err != nil {
			return nil, fmt.Errorf("add job: %w", err)
		}
		addJobID = id
		var err2 error
		job, err2 = mapper.GetJobByID(id)
		if err2 != nil {
			// Do not leave a scheduled-enabled row behind for a request that
			// failed: it would be loaded at the next startup or fail again.
			if delErr := mapper.DeleteJob(id); delErr != nil {
				log.Printf("Failed to roll back job %d after read failure: %v", id, delErr)
			}
			return nil, fmt.Errorf("get job after add: %w", err2)
		}
	}

	jc.JobID = util.ToInt64(job["id"])
	jc.Job = cloneJobConfig(job)

	sched := NewScheduler()
	jc.Scheduler = sched

	err := sched.AddJob(util.ToInt(job["isCron"]), job, func() {
		jc.DoScheduled()
	})
	if err != nil {
		sched.Stop()
		if addJobID != 0 {
			// Log the identifier and the cause, not the whole config: the config
			// carries source/destination paths and dumping it loses the error.
			log.Printf("Error during job setup, deleting job %d: %v", jc.JobID, err)
			if delErr := mapper.DeleteJob(jc.JobID); delErr != nil {
				log.Printf("Failed to delete job %d after setup error: %v", jc.JobID, delErr)
			}
		} else if isInit {
			log.Printf("Error during job setup, disabling job %d: %v", jc.JobID, err)
			if updateErr := mapper.UpdateJobEnable(jc.JobID, 0); updateErr != nil {
				log.Printf("Failed to disable invalid job %d: %v", jc.JobID, updateErr)
			}
			jc.setEnable(0)
		}
		return nil, publicError(err.Error())
	}

	return jc, nil
}

type markedJobRunOptions struct {
	sourceTaskID     int64
	statuses         []taskStatus
	itemIDs          []int64
	manual           bool
	onlyFiles        []string
	forceOverwrite   bool
	chainDepth       int
	syncGeneration   int64
	syncChangedFiles []string
	syncPartial      bool
}

func (jc *JobClient) runMarkedJob() {
	jc.runMarkedJobWithOptions(markedJobRunOptions{})
}

func (jc *JobClient) runMarkedManualJob() {
	jc.runMarkedJobWithOptions(markedJobRunOptions{manual: true})
}

// runMarkedJobConfig starts a task. With sourceTaskID > 0 it replays the source
// task's items whose status is in statuses (or matching itemIDs when provided);
// otherwise it runs a fresh full scan.
func (jc *JobClient) runMarkedJobConfig(sourceTaskID int64, statuses []taskStatus, itemIDs []int64, manual bool) {
	jc.runMarkedJobWithOptions(markedJobRunOptions{
		sourceTaskID: sourceTaskID,
		statuses:     statuses,
		itemIDs:      itemIDs,
		manual:       manual,
	})
}

func (jc *JobClient) runMarkedJobWithOptions(opts markedJobRunOptions) {
	// A manual or scheduled full run can drain the same durable inbox. Repair
	// and historical retries must never acknowledge unrelated pending changes.
	if opts.syncGeneration == 0 && opts.sourceTaskID == 0 && len(opts.onlyFiles) == 0 {
		job := jc.jobSnapshot()
		extra, _, _ := parseNativeExtraFlags(job["extraFlags"])
		if util.ToInt(job["isCron"]) == 3 || extra.WatchEnable {
			if state, err := mapper.GetSyncState(jc.idSnapshot()); err == nil {
				opts.syncGeneration = state.Generation
				opts.syncChangedFiles = state.Paths
			}
		}
	}
	started := false
	defer func() {
		if opts.syncGeneration > 0 && !started {
			_ = mapper.RecordSyncError(jc.idSnapshot(), "同步启动中断，待处理变更已保留，将自动重试")
		}
	}()
	sourceTaskID := opts.sourceTaskID
	statuses := opts.statuses
	itemIDs := opts.itemIDs
	manual := opts.manual
	taskID := int64(0)
	var task *JobTask
	var releaseSlot func()
	defer func() {
		if r := recover(); r != nil {
			if task != nil {
				task.requestBreak()
				task.cancelTaskRuntime()
			} else if releaseSlot != nil {
				releaseSlot()
			}
			jc.markDone()
			jc.clearCurrentTask(nil)
			errMsg := fmt.Sprintf("%v", r)
			log.Printf("Job execution error: %s", errMsg)
			if taskID > 0 && !jobTaskHasStatus(taskID, taskStatusStopped) {
				if err := UpdateJobTaskStatusSimple(taskID, taskStatusSystemFailed, &errMsg); err != nil {
					log.Printf("Failed to mark task %d as failed after execution error: %v", taskID, err)
				}
			}
		}
	}()

	if !jc.enabled() || jc.isClosing() {
		// tryMarkDoing already set JobDoing=true; the async task.Start() below
		// is never reached on this path, so we must clear it here or the job is
		// permanently stuck "doing" (cannot rerun/delete without a restart).
		jc.markDone()
		return
	}

	// Enforce per-engine and per-storage maxConcurrentJobs queues before creating the task row.
	jobSnap := jc.jobSnapshot()
	alistID := util.ToInt64(jobSnap["alistId"])
	maxConcurrent := 0
	var storageLimits map[string]int
	storages := extractJobStorageNames(jobSnap)
	if alistID > 0 && alistDeps != nil && alistDeps.GetAlistByID != nil {
		if alistRow, err := alistDeps.GetAlistByID(alistID); err == nil && alistRow != nil {
			maxConcurrent = util.ToInt(alistRow["maxConcurrentJobs"])
			if cfg, cfgErr := nativeConfig(alistRow); cfgErr == nil && cfg != nil {
				storages = extractJobStorageNamesWithDeps(jobSnap, cfg)
				storageLimits = nativeStorageConcurrencyLimits(cfg)
			}
		}
	}
	if storageLimits == nil {
		if localCap := config.GetSystemSettings().LocalMaxConcurrentJobs; localCap > 0 {
			storageLimits = map[string]int{"本地": localCap}
		}
	}
	queueCtx, queueCancel := context.WithCancel(context.Background())
	jc.setQueueCancel(queueCancel)
	slotRelease, acquireErr := engineQueue.AcquireWithStorageLimits(queueCtx, alistID, jc.idSnapshot(), maxConcurrent, storages, storageLimits)
	jc.setQueueCancel(nil)
	queueCancel()
	if acquireErr != nil {
		jc.markDone()
		return
	}
	releaseSlot = slotRelease

	if !jc.enabled() || jc.isClosing() {
		releaseSlot()
		jc.markDone()
		return
	}

	var err error
	taskID, err = mapper.AddJobTask(jc.idSnapshot(), time.Now().Unix())
	if err != nil {
		log.Printf("Failed to create task for job %d: %v", jc.idSnapshot(), err)
		releaseSlot()
		jc.markDone()
		return
	}
	if !jc.enabled() || jc.isClosing() {
		if err := UpdateJobTaskStatusSimple(taskID, taskStatusStopped, nil); err != nil {
			log.Printf("Failed to mark disabled task %d as stopped: %v", taskID, err)
		}
		releaseSlot()
		jc.markDone()
		return
	}
	task, err = newJobTask(taskID, jc)
	if err != nil {
		errMsg := err.Error()
		log.Printf("Failed to init task for job %d: %v", jc.idSnapshot(), err)
		if err := UpdateJobTaskStatusSimple(taskID, taskStatusSystemFailed, &errMsg); err != nil {
			log.Printf("Failed to mark task %d as failed: %v", taskID, err)
		}
		releaseSlot()
		jc.markDone()
		return
	}
	task.releaseEngineSlot = releaseSlot
	releaseSlot = nil
	if sourceTaskID > 0 {
		task.RetrySourceTaskID = sourceTaskID
		task.RetryStatuses = append([]taskStatus(nil), statuses...)
		if len(itemIDs) > 0 {
			task.RetryItemIDs = append([]int64(nil), itemIDs...)
		}
	}
	if len(opts.onlyFiles) > 0 {
		task.OnlyFiles = append([]string(nil), opts.onlyFiles...)
	}
	task.ForceOverwrite = opts.forceOverwrite
	task.ChainDepth = opts.chainDepth
	task.SyncGeneration = opts.syncGeneration
	task.SyncChangedFiles = append([]string(nil), opts.syncChangedFiles...)
	task.SyncPartial = opts.syncPartial
	task.SyncFullScan = opts.syncGeneration > 0 && !opts.syncPartial
	if !jc.trySetCurrentTaskIfActive(task) {
		// The job was paused or deleted while newJobTask initialized: record the
		// task as stopped instead of starting transfers that pause/delete
		// missed before CurrentJobTask was published.
		if err := UpdateJobTaskStatusSimple(taskID, taskStatusStopped, nil); err != nil {
			log.Printf("Failed to mark closing/paused task %d as stopped: %v", taskID, err)
		}
		task.requestBreak()
		task.cancelTaskRuntime()
		jc.markDone()
		return
	}
	if !manual && util.ToInt(task.Job["windowEnable"]) == 1 && util.ToInt(task.Job["windowStopOutside"]) == 1 {
		go watchTaskTimeWindow(task)
	}
	started = true
	task.Start()
}

func extractJobStorageNames(job map[string]interface{}) []string {
	seen := make(map[string]struct{})
	for _, raw := range append(parsePathList(job["srcPath"]), parsePathList(job["dstPath"])...) {
		seg, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(raw), "/"), "/")
		if seg != "" {
			seen[seg] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func extractJobStorageNamesWithDeps(job map[string]interface{}, cfg interface {
	ExpandRemoteDependencies(string) []string
}) []string {
	base := extractJobStorageNames(job)
	if cfg == nil || len(base) == 0 {
		return base
	}
	seen := make(map[string]struct{}, len(base))
	for _, name := range base {
		for _, dep := range cfg.ExpandRemoteDependencies(name) {
			if dep != "" {
				seen[dep] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func watchTaskTimeWindow(task *JobTask) {
	ctx := task.context()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if task.isBreak() {
				return
			}
			if !JobInTimeWindow(task.Job, time.Now()) {
				log.Printf("Task %d (job %d) left configured run window; stopping task", task.TaskID, task.jobIDSnapshot())
				task.requestBreak()
				return
			}
		}
	}
}

// DoJob executes the job, waiting until any current run has finished.
// Gives up after 30 attempts (~5 minutes) to avoid blocking a goroutine forever.
func (jc *JobClient) DoJob() {
	const maxAttempts = 30
	for attempt := 0; !jc.tryMarkDoing(); attempt++ {
		if !jc.enabled() || jc.isClosing() || attempt >= maxAttempts {
			if attempt >= maxAttempts {
				log.Printf("DoJob: gave up waiting for job %d after %d attempts", jc.idSnapshot(), maxAttempts)
			}
			return
		}
		jc.waitUntilIdle(10 * time.Second)
	}
	jc.runMarkedManualJob()
}

// DoScheduled executes a scheduled job once, skipping if outside its allowed
// time window or if the previous run is still active.
func (jc *JobClient) DoScheduled() bool {
	if !JobInTimeWindow(jc.jobSnapshot(), time.Now()) {
		log.Printf("Skipping scheduled run of job %d: outside allowed time window", jc.idSnapshot())
		return false
	}
	if !jc.tryMarkDoing() {
		log.Printf("Skipping job %d because previous run is still active", jc.idSnapshot())
		return false
	}
	jc.runMarkedJob()
	return true
}

// DoManual triggers manual execution
func (jc *JobClient) DoManual() error {
	if jc.isClosing() {
		return publicError(msg.T(msg.JobNotFound))
	}
	jc.resetAutoRetry()
	if !jc.tryMarkDoing() {
		return publicError(msg.T(msg.JobRunning))
	}
	if jc.isClosing() {
		jc.markDone()
		return publicError(msg.T(msg.JobNotFound))
	}
	go jc.runMarkedManualJob()
	return nil
}

// DoRepairFiles launches a targeted force-overwrite copy for specific relative
// paths identified by a consistency check.
func (jc *JobClient) DoRepairFiles(relPaths []string) error {
	if jc.isClosing() {
		return publicError(msg.T(msg.JobNotFound))
	}
	if len(relPaths) == 0 {
		return publicError("没有需要修复的差异或缺失文件")
	}
	if !jc.enabled() {
		return publicError(msg.T(msg.DisabledJobCannotRun))
	}
	jc.resetAutoRetry()
	if !jc.tryMarkDoing() {
		return publicError(msg.T(msg.JobRunning))
	}
	if jc.isClosing() {
		jc.markDone()
		return publicError(msg.T(msg.JobNotFound))
	}
	go jc.runMarkedJobWithOptions(markedJobRunOptions{
		manual:         true,
		onlyFiles:      relPaths,
		forceOverwrite: true,
	})
	return nil
}

// DoChainedRun triggers a downstream job chained via extraFlags.postSyncJobId.
func (jc *JobClient) DoChainedRun(chainDepth int) error {
	if chainDepth > maxPostSyncChainDepth {
		return nil
	}
	if jc.isClosing() || !jc.enabled() {
		return nil
	}
	if !jc.tryMarkDoing() {
		return publicError(msg.T(msg.JobRunning))
	}
	if jc.isClosing() {
		jc.markDone()
		return nil
	}
	go jc.runMarkedJobWithOptions(markedJobRunOptions{
		chainDepth: chainDepth,
	})
	return nil
}

// DoRetryFailedTaskItems triggers a manual execution that replays all
// non-success items of a historical task.
func (jc *JobClient) DoRetryFailedTaskItems(sourceTaskID int64) error {
	return jc.DoRetrySelectedTaskItems(sourceTaskID, nil)
}

// DoRetrySelectedTaskItems replays either all non-success items (when itemIDs
// is empty) or only the specified retryable item IDs of a historical task.
func (jc *JobClient) DoRetrySelectedTaskItems(sourceTaskID int64, itemIDs []int64) error {
	if jc.isClosing() {
		return publicError(msg.T(msg.JobNotFound))
	}
	// Coordinate with retention history cleanup under historyCleanupMu so a
	// cleanup batch cannot be mid-deletion of sourceTaskID when the retry
	// starts, and cleanup cannot select sourceTaskID once claimed here.
	historyCleanupMu.Lock()
	if _, deleting := historyDeletingTasks[sourceTaskID]; deleting {
		historyCleanupMu.Unlock()
		return publicError(msg.T(msg.TaskNotFound))
	}
	if !jc.tryMarkDoing() {
		historyCleanupMu.Unlock()
		return publicError(msg.T(msg.JobRunning))
	}
	if jc.isClosing() {
		jc.markDone()
		historyCleanupMu.Unlock()
		return publicError(msg.T(msg.JobNotFound))
	}
	if !jc.enabled() {
		jc.markDone()
		historyCleanupMu.Unlock()
		return publicError(msg.T(msg.DisabledJobCannotRun))
	}
	// Claim the source task before releasing historyCleanupMu and before
	// counting or starting the goroutine: RemoveTask and retention cleanup both
	// honor this claim, so the source cannot disappear mid-replay.
	jc.setRetrySource(sourceTaskID)
	historyCleanupMu.Unlock()
	var (
		count int64
		err   error
	)
	if len(itemIDs) > 0 {
		count, err = mapper.CountJobTaskItemsByIDs(sourceTaskID, itemIDs, retryableStatusValues())
	} else {
		count, err = jobDeps.CountJobTaskItemsByStatuses(sourceTaskID, retryableStatusValues())
	}
	if err != nil {
		jc.markDone()
		return fmt.Errorf("count retryable items: %w", err)
	}
	if count == 0 {
		jc.markDone()
		if len(itemIDs) > 0 {
			return publicError(msg.T(msg.NoRetryItemSelected))
		}
		return publicError(msg.T(msg.NoFailedTaskItems))
	}
	go jc.runMarkedJobConfig(sourceTaskID, retryableTaskStatuses, itemIDs, false)
	return nil
}

// ResumeJob enables and resumes the job
func (jc *JobClient) ResumeJob() error {
	// Serialize against edit/delete: a resume that snapshots the scheduler while
	// an edit replaces it would leave the job enabled in the database but with
	// the old (stopped) scheduler, i.e. enabled and never scheduled.
	if !jc.lockOp() {
		jc.opMu.Unlock()
		return publicError(msg.T(msg.JobNotFound))
	}
	defer jc.opMu.Unlock()

	jobID, job, scheduler := jc.configSnapshot()
	isCron := util.ToInt(job["isCron"])
	if isCron == 2 {
		// Manual only, just enable
		if err := mapper.UpdateJobEnable(jobID, 1); err != nil {
			return fmt.Errorf("enable job: %w", err)
		}
		jc.setEnable(1)
		return nil
	}

	if scheduler == nil {
		return publicError(msg.T(msg.CannotResumeLostJob))
	}
	err := scheduler.Resume(isCron, job, func() {
		jc.DoScheduled()
	})
	if err != nil {
		return publicError(err.Error())
	}
	if err := mapper.UpdateJobEnable(jobID, 1); err != nil {
		scheduler.Pause()
		return fmt.Errorf("enable job: %w", err)
	}
	jc.setEnable(1)
	return nil
}

// AbortJob aborts the current running or queued task
func (jc *JobClient) AbortJob() {
	jc.cancelQueueWait()
	if task := jc.currentTask(); task != nil {
		task.requestBreak()
	}
}

// StopJob stops the job (for disable or delete). Configuration changes are
// serialized through opMu so a pause/resume/edit/delete cannot interleave.
func (jc *JobClient) StopJob(remove bool) error {
	if !jc.lockOp() {
		jc.opMu.Unlock()
		if remove {
			// Already deleted (or being deleted): the teardown is in progress.
			return nil
		}
		return publicError(msg.T(msg.JobNotFound))
	}
	defer jc.opMu.Unlock()
	return jc.stopJobLocked(remove)
}

// stopJobLocked is StopJob's body; the caller holds opMu.
func (jc *JobClient) stopJobLocked(remove bool) error {
	jc.cancelAutoRetry()
	jobID := jc.idSnapshot()
	scheduler := jc.schedulerSnapshot()
	if remove {
		jc.closing.Store(true)
		jc.cancelQueueWait()
		jc.mu.Lock()
		if jc.Job != nil {
			jc.Job["enable"] = 0
		}
		task := jc.CurrentJobTask
		jc.mu.Unlock()
		if task != nil {
			task.requestBreak()
		}
		if scheduler != nil {
			scheduler.Stop()
		}
		return nil
	}
	if err := mapper.UpdateJobEnable(jobID, 0); err != nil {
		return fmt.Errorf("disable job: %w", err)
	}
	jc.cancelQueueWait()
	// Atomically disable and read CurrentJobTask under jc.mu so a starting task
	// either sees enable=0 in trySetCurrentTaskIfActive or is seen here and broken.
	jc.mu.Lock()
	if jc.Job != nil {
		jc.Job["enable"] = 0
	}
	task := jc.CurrentJobTask
	jc.mu.Unlock()
	// Break the task before rewriting history. The reverse order marked the
	// row aborted while the task was still running, so the task's own final
	// write could land afterwards and resurrect it as running.
	if task != nil {
		task.requestBreak()
	}
	idle := jc.waitUntilIdle(pauseJobIdleTimeout)
	if scheduler != nil {
		scheduler.Pause()
	}
	if !idle {
		// The task was told to stop but is still finishing. Rewriting its
		// history row now is exactly the race the ordering above avoids: its
		// own final write would land afterwards. It records its terminal
		// status itself, so leave the row to it.
		log.Printf("Job %d disabled; its running task has not stopped yet and will record its own final status", jobID)
		return nil
	}
	if err := mapper.UpdateJobTaskStatusByStatusAndJobID(jobID); err != nil {
		return fmt.Errorf("mark tasks aborted: %w", err)
	}
	return nil
}

// pauseJobIdleTimeout bounds how long a pause waits for the running task to
// stop before leaving its history to it; swapped by tests.
var pauseJobIdleTimeout = 30 * time.Second

// pauseForRemoval stops new runs and asks the current one to stop, but keeps
// the scheduler alive so a delete that cannot complete can be rolled back. It
// returns the previous in-memory enable value. The caller holds opMu.
func (jc *JobClient) pauseForRemoval() int {
	jc.cancelQueueWait()
	jc.mu.Lock()
	previous := 0
	if jc.Job != nil {
		previous = util.ToInt(jc.Job["enable"])
		jc.Job["enable"] = 0
	}
	scheduler := jc.Scheduler
	task := jc.CurrentJobTask
	jc.mu.Unlock()
	if scheduler != nil {
		scheduler.Pause()
	}
	if task != nil {
		task.requestBreak()
	}
	return previous
}

// restoreAfterFailedRemoval undoes pauseForRemoval: the row still exists, so
// the job must keep its enable flag and schedule. The caller holds opMu.
func (jc *JobClient) restoreAfterFailedRemoval(previousEnable int) {
	jc.setEnable(previousEnable)
	if previousEnable != 1 {
		return
	}
	_, job, scheduler := jc.configSnapshot()
	if scheduler == nil {
		return
	}
	if err := scheduler.Resume(util.ToInt(job["isCron"]), job, func() {
		jc.DoScheduled()
	}); err != nil {
		log.Printf("Job %d: failed to restore its schedule after an aborted delete: %v", jc.idSnapshot(), err)
	}
}
