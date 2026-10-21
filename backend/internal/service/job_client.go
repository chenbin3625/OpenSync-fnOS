package service

import (
	"context"
	"fmt"
	"log"
	"opensync/internal/mapper"
	"opensync/internal/msg"
	"opensync/pkg/util"
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

func (jc *JobClient) runMarkedJob() {
	jc.runMarkedJobConfig(0, nil)
}

// runMarkedJobConfig starts a task. With sourceTaskID > 0 it replays the source
// task's items whose status is in statuses (retry-failed); otherwise it runs a
// fresh full scan.
func (jc *JobClient) runMarkedJobConfig(sourceTaskID int64, statuses []taskStatus) {
	taskID := int64(0)
	defer func() {
		if r := recover(); r != nil {
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

	var err error
	taskID, err = mapper.AddJobTask(jc.idSnapshot(), time.Now().Unix())
	if err != nil {
		log.Printf("Failed to create task for job %d: %v", jc.idSnapshot(), err)
		jc.markDone()
		return
	}
	if !jc.enabled() || jc.isClosing() {
		if err := UpdateJobTaskStatusSimple(taskID, taskStatusStopped, nil); err != nil {
			log.Printf("Failed to mark disabled task %d as stopped: %v", taskID, err)
		}
		jc.markDone()
		return
	}
	task, err := newJobTask(taskID, jc)
	if err != nil {
		errMsg := err.Error()
		log.Printf("Failed to init task for job %d: %v", jc.idSnapshot(), err)
		if err := UpdateJobTaskStatusSimple(taskID, taskStatusSystemFailed, &errMsg); err != nil {
			log.Printf("Failed to mark task %d as failed: %v", taskID, err)
		}
		jc.markDone()
		return
	}
	if jc.isClosing() {
		// The job is being deleted: record the task as stopped instead of
		// starting transfers that nobody will ever see or be able to stop.
		if err := UpdateJobTaskStatusSimple(taskID, taskStatusStopped, nil); err != nil {
			log.Printf("Failed to mark closing task %d as stopped: %v", taskID, err)
		}
		jc.markDone()
		return
	}
	if sourceTaskID > 0 {
		task.RetrySourceTaskID = sourceTaskID
		task.RetryStatuses = append([]taskStatus(nil), statuses...)
	}
	jc.setCurrentTask(task)
	task.Start()
}

// DoJob executes the job, waiting until any current run has finished.
// Gives up after 30 attempts (~5 minutes) to avoid blocking a goroutine forever.
func (jc *JobClient) DoJob() {
	const maxAttempts = 30
	for attempt := 0; !jc.tryMarkDoing(); attempt++ {
		if !jc.enabled() || attempt >= maxAttempts {
			if attempt >= maxAttempts {
				log.Printf("DoJob: gave up waiting for job %d after %d attempts", jc.idSnapshot(), maxAttempts)
			}
			return
		}
		jc.waitUntilIdle(10 * time.Second)
	}
	jc.runMarkedJob()
}

// DoScheduled executes a scheduled job once, skipping if the previous run is still active.
func (jc *JobClient) DoScheduled() bool {
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
	if !jc.tryMarkDoing() {
		return publicError(msg.T(msg.JobRunning))
	}
	go jc.runMarkedJob()
	return nil
}

// DoRetryFailedTaskItems triggers a manual execution that replays the non-success
// items of a historical task.
func (jc *JobClient) DoRetryFailedTaskItems(sourceTaskID int64) error {
	if jc.isClosing() {
		return publicError(msg.T(msg.JobNotFound))
	}
	if !jc.tryMarkDoing() {
		return publicError(msg.T(msg.JobRunning))
	}
	// Claim the source task before the goroutine starts: RemoveTask checks this
	// claim, so the task cannot disappear while the replay is reading it.
	jc.setRetrySource(sourceTaskID)
	go jc.runMarkedJobConfig(sourceTaskID, retryableTaskStatuses)
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

// AbortJob aborts the current running task
func (jc *JobClient) AbortJob() {
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
	jobID := jc.idSnapshot()
	scheduler := jc.schedulerSnapshot()
	if remove {
		jc.setEnable(0)
		if task := jc.currentTask(); task != nil {
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
	jc.setEnable(0)
	// Break the task before rewriting history. The reverse order marked the
	// row aborted while the task was still running, so the task's own final
	// write could land afterwards and resurrect it as running.
	if task := jc.currentTask(); task != nil {
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
