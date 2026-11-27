package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"opensync/internal/mapper"
	"opensync/pkg/util"
	"time"
)

func (jt *JobTask) finishSubmittedTask(persistErr error) {
	if fatalErr := jt.fatalError(); fatalErr != nil {
		jt.finishFailedTask(*fatalErr)
		return
	}
	if persistErr != nil {
		jt.finishFailedTask(taskPersistenceErrorMessage(persistErr))
		return
	}
	jt.finishSuccessfulTask()
}

func (jt *JobTask) releaseSlotNow() {
	jt.runtimeMu.Lock()
	release := jt.releaseEngineSlot
	jt.releaseEngineSlot = nil
	jt.runtimeMu.Unlock()
	if release != nil {
		release()
	}
}

// cancelTaskRuntime releases the task-scoped resources that are not tied to a
// finished work item: the debounced persist flush, the engine concurrency slot,
// and the task context.
func (jt *JobTask) cancelTaskRuntime() {
	jt.cancelPersistFlush()
	jt.runtimeMu.Lock()
	cancel := jt.cancel
	releaseSlot := jt.releaseEngineSlot
	rc := jt.rcController
	jt.releaseEngineSlot = nil
	jt.runtimeMu.Unlock()
	if rc != nil {
		rc.Close()
	}
	if cancel != nil {
		cancel()
	}
	if releaseSlot != nil {
		releaseSlot()
	}
}

// isDrained reports that the task exited because all work completed before cancellation/deadline.
func (jt *JobTask) isDrained() bool {
	return jt.drained.Load()
}

func (jt *JobTask) finishSuccessfulTask() {
	status, taskNum, duration, err := jt.updateTaskStatus()
	if err != nil {
		jt.finishFailedTask(taskStatusUpdateErrorMessage(err))
		return
	}
	enqueue := queueTaskNotification
	jt.completeContinuousRun(status, "")
	jt.releaseSlotNow()
	// Mark the job idle after the DB update succeeds, before notification
	// delivery is handed off, so the next run is never gated on it.
	if jt.JobClient != nil {
		jt.JobClient.markDone()
		jt.JobClient.clearCurrentTask(jt)
		jt.notifyProgressNow()
	}
	// Queued rather than sent here: delivery costs one HTTP timeout per
	// unreachable destination, and the task is already finished.
	enqueue(jt.TaskID, status.Int(), taskNum, duration, jt.CreateTime)

	// Post-completion orchestration: auto-retry on failure/timeout, or trigger chained downstream job on success.
	if jt.JobClient != nil {
		switch status {
		case taskStatusSuccess, taskStatusNoSync:
			jt.JobClient.resetAutoRetry()
			if extra, _, extraErr := parseNativeExtraFlags(jt.Job["extraFlags"]); extraErr == nil && extra.PostSyncJobID > 0 {
				nextID := extra.PostSyncJobID
				if nextID != jt.jobIDSnapshot() && jt.ChainDepth < maxPostSyncChainDepth {
					go triggerPostSyncChain(nextID, jt.ChainDepth+1)
				}
			}
		case taskStatusFailed, taskStatusPartialFail, taskStatusTimeout:
			if jt.SyncGeneration == 0 {
				jt.JobClient.scheduleAutoRetry(jt.TaskID)
			}
		}
	}
}

func triggerPostSyncChain(jobID int64, depth int) {
	client, err := GetJobClientByID(jobID)
	if err != nil {
		log.Printf("Post-sync chain: failed to load job %d: %v", jobID, err)
		return
	}
	if err := client.DoChainedRun(depth); err != nil {
		log.Printf("Post-sync chain: skipped job %d (depth %d): %v", jobID, depth, err)
	} else {
		log.Printf("Post-sync chain: triggered job %d (depth %d)", jobID, depth)
	}
}

func taskPersistenceErrorMessage(err error) string {
	return fmt.Sprintf("failed to save task items: %v", err)
}

func taskStatusUpdateErrorMessage(err error) string {
	return fmt.Sprintf("failed to update task status: %v", err)
}

func (jt *JobTask) finishFailedTask(errMsg string) {
	errMsg = redactSensitiveError(errMsg)
	log.Printf("Task %d failed (job %d): %s", jt.TaskID, jt.jobIDSnapshot(), errMsg)
	if err := UpdateJobTaskStatusSimple(jt.TaskID, taskStatusSystemFailed, &errMsg); err != nil {
		log.Printf("Failed to mark task %d as failed: %v", jt.TaskID, err)
	}
	// A failed run is exactly what the user needs to hear about; the success
	// path was the only one that queued a notification. Counters are best
	// effort: the failure may itself be the database. Gathered before the job
	// is marked idle so nothing of this task runs after waiters are released.
	taskNum, err := GetCuTaskNum(jt.TaskID)
	if err != nil {
		taskNum = map[string]interface{}{}
	}
	duration := taskDuration(jt.CreateTime)
	taskNum["duration"] = duration
	enqueue := queueTaskNotification
	jt.completeContinuousRun(taskStatusSystemFailed, errMsg)
	jt.releaseSlotNow()
	if jt.JobClient != nil {
		jt.JobClient.markDone()
		jt.JobClient.clearCurrentTask(jt)
		jt.notifyProgressNow()
	}
	enqueue(jt.TaskID, taskStatusSystemFailed.Int(), taskNum, duration, jt.CreateTime)
	if jt.JobClient != nil && !jt.isBreak() && jt.SyncGeneration == 0 {
		jt.JobClient.scheduleAutoRetry(jt.TaskID)
	}
}

func (jt *JobTask) completeContinuousRun(status taskStatus, message string) {
	if jt.SyncGeneration <= 0 {
		return
	}
	if status != taskStatusSuccess && status != taskStatusNoSync && message == "" {
		message = "本轮同步未全部完成，待处理变更已保留，将自动重试"
	}
	if err := mapper.CompleteSyncRun(util.ToInt64(jt.Job["id"]), jt.SyncGeneration, redactSensitiveError(message)); err != nil {
		log.Printf("Task %d failed to checkpoint continuous sync: %v", jt.TaskID, err)
	}
}

// queueTaskNotification is swapped in tests to observe completion notifications.
var queueTaskNotification = QueueTaskNotification

// jobIDSnapshot reads the owning job's id under its lock; replaceJobConfig
// writes it concurrently.
func (jt *JobTask) jobIDSnapshot() int64 {
	if jt.JobClient == nil {
		return 0
	}
	return jt.JobClient.idSnapshot()
}

func (jt *JobTask) updateTaskStatus() (taskStatus, map[string]interface{}, int, error) {
	// A failed count must not be read as "zero items": allNum=0 derives the
	// "nothing to sync" status and the zeros would be cached into taskNum.
	taskNum, err := GetCuTaskNum(jt.TaskID)
	if err != nil {
		return 0, nil, 0, fmt.Errorf("count task items: %w", err)
	}
	failOrOtherNum := util.ToInt(taskNum["failNum"]) + util.ToInt(taskNum["otherNum"])
	successNum := util.ToInt(taskNum["successNum"])
	allNum := util.ToInt(taskNum["allNum"])
	ctxErr := jt.context().Err()
	if jt.isDrained() {
		// Every item finished before the deadline; a deadline that expired
		// during the final flush is not a timeout of the sync itself.
		ctxErr = nil
	}
	status := finalTaskStatus(jt.isBreak(), ctxErr, allNum, successNum, failOrOtherNum)
	duration := taskDuration(jt.CreateTime)
	taskNum["duration"] = duration
	taskNum["scanFinish"] = jt.ScanFinish.Load()
	taskNum["scan"] = jt.scanProgress()

	return status, taskNum, duration, finishJobTaskStatus(jt.TaskID, status, nil, taskNum)
}

func finalTaskStatus(isBreak bool, ctxErr error, allNum, successNum, failOrOtherNum int) taskStatus {
	if isBreak {
		return taskStatusStopped
	}
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		return taskStatusTimeout
	}
	if failOrOtherNum > 0 {
		// A run in which not a single item succeeded is a failure, not a
		// partial success; the notification title and the task list used to
		// report "Partial Success" for a task where everything failed.
		if successNum == 0 && failOrOtherNum == allNum {
			return taskStatusFailed
		}
		return taskStatusPartialFail
	}
	if allNum == 0 {
		return taskStatusNoSync
	}
	return taskStatusSuccess
}

func finishJobTaskStatus(taskID int64, status taskStatus, errMsg *string, taskNum map[string]interface{}) error {
	errMsg = redactSensitiveErrorPtr(errMsg)
	taskNumJSON, err := json.Marshal(taskNum)
	if err != nil {
		// Writing an empty counters blob would silently replace the task's
		// numbers with "no data"; fail the status update instead so the caller
		// reports the task as failed rather than half-recorded.
		return fmt.Errorf("marshal task counters: %w", err)
	}
	return mapper.UpdateJobTaskStatusAndNum(taskID, status.Int(), errMsg, string(taskNumJSON))
}

func taskDuration(createTime float64) int {
	if createTime <= 0 {
		return 0
	}
	d := int(float64(time.Now().Unix()) - createTime)
	if d < 0 {
		return 0
	}
	return d
}

// UpdateJobTaskStatusSimple updates task status with error message
func UpdateJobTaskStatusSimple(taskID int64, status taskStatus, errMsg *string) error {
	errMsg = redactSensitiveErrorPtr(errMsg)
	taskNum, err := GetCuTaskNum(taskID)
	if err != nil {
		// The status still has to be recorded; the counters are left for the
		// task list to recompute rather than persisted as zeros.
		log.Printf("Failed to count items of task %d, saving status without counters: %v", taskID, err)
		return mapper.UpdateJobTaskStatusClearNum(taskID, status.Int(), errMsg)
	}
	taskNumJSON, err := json.Marshal(taskNum)
	if err != nil {
		// The status still has to be recorded, but the counters must not be
		// persisted as an empty blob: leave them for the task list to recompute.
		log.Printf("Failed to marshal counters of task %d, saving status without counters: %v", taskID, err)
		return mapper.UpdateJobTaskStatusClearNum(taskID, status.Int(), errMsg)
	}
	return mapper.UpdateJobTaskStatusAndNum(taskID, status.Int(), errMsg, string(taskNumJSON))
}

// queryJobTaskCounts is swapped in tests to simulate a failed count.
var queryJobTaskCounts = mapper.QueryJobTaskCounts

// GetCuTaskNum gets current task counts from DB
func GetCuTaskNum(taskID int64) (map[string]interface{}, error) {
	return queryJobTaskCounts(taskID)
}
