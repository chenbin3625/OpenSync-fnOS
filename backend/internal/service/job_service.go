package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"opensync/internal/mapper"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	jobClientList    = make(map[int64]*JobClient)
	jobClientListMu  sync.RWMutex
)

var (
	taskNumUpdateMu      sync.Mutex
	taskNumUpdateLatest  []map[string]interface{}
	taskNumUpdateActive  bool
)

// InitJobs loads and starts all enabled jobs on startup
func InitJobs() {
	logger := log.Default()
	if err := mapper.UpdateJobTaskStatusByStatus(); err != nil {
		logger.Printf("Failed to mark unfinished task history as aborted: %v", err)
	}
	RunTaskRetentionCleanup()
	jobList, err := mapper.GetJobListAll()
	if err != nil {
		logger.Printf("Failed to get job list: %v", err)
		return
	}
	for _, item := range jobList {
		logger.Printf("Adding jobId %v", item["id"])
		if err := AddJobClient(item, true); err != nil {
			logger.Printf("Error adding job: %v", err)
		}
	}
}

func ShutdownJobs(ctx context.Context) {
	jobClientListMu.RLock()
	clients := make([]*JobClient, 0, len(jobClientList))
	for _, client := range jobClientList {
		clients = append(clients, client)
	}
	jobClientListMu.RUnlock()

	for _, client := range clients {
		client.StopJob(true) //nolint:errcheck // remove=true never fails
	}

	var wg sync.WaitGroup
	for _, client := range clients {
		wg.Add(1)
		go func(client *JobClient) {
			defer wg.Done()
			waitJobClientIdleContext(ctx, client)
		}(client)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}
}

func waitJobClientIdleContext(ctx context.Context, client *JobClient) {
	client.waitUntilIdleContext(ctx, 0)
}

func CleanupExpiredTasks(logger *log.Logger, taskSaveDays int, now time.Time) {
	cutoff, ok := taskRetentionCutoff(now, taskSaveDays)
	if !ok {
		return
	}
	if err := mapper.DeleteJobTaskByRunTime(cutoff); err != nil {
		logger.Printf("Failed to delete expired task history: %v", err)
	}
}

func taskRetentionCutoff(now time.Time, taskSaveDays int) (int64, bool) {
	if taskSaveDays <= 0 {
		return 0, false
	}
	return now.Add(-time.Duration(taskSaveDays) * 24 * time.Hour).Unix(), true
}

// GetJobClientByID gets or creates a job client
func GetJobClientByID(jobID int64) (*JobClient, error) {
	jobClientListMu.RLock()
	client, ok := jobClientList[jobID]
	jobClientListMu.RUnlock()
	if ok {
		return client, nil
	}

	jobClientListMu.Lock()
	defer jobClientListMu.Unlock()

	if client, ok := jobClientList[jobID]; ok {
		return client, nil
	}

	job, err := mapper.GetJobByID(jobID)
	if err != nil {
		if err := publicErrorIf(err, msg.T(msg.JobNotFound)); err != nil {
			return nil, err
		}
	}
	client, err = NewJobClient(job, false)
	if err != nil {
		return nil, err
	}
	jobClientList[jobID] = client
	return client, nil
}

// CleanJobInput sanitizes job input data
func CleanJobInput(job map[string]interface{}) error {
	if util.ToInt(job["isCron"]) == 2 && util.ToInt(job["enable"]) != 1 {
		job["enable"] = 1
	}
	for key, value := range job {
		if s, ok := value.(string); ok {
			trimmed := strings.TrimSpace(s)
			if trimmed == "" {
				job[key] = nil
			} else {
				job[key] = trimmed
			}
		}
	}
	if job["exclude"] != nil {
		excludeStr := fmt.Sprintf("%v", job["exclude"])
		// Rejected before normalization, because normalizeExclude is exactly the
		// step that would drop these lines without a trace.
		if invalid := invalidExcludeRules(excludeStr); len(invalid) > 0 {
			return publicError(msg.ExcludeRulesUnsupported(invalid))
		}
		job["exclude"] = normalizeExclude(excludeStr)
	}
	if job["srcPath"] != nil {
		job["srcPath"] = normalizePathListForStorage(job["srcPath"])
	}
	if job["dstPath"] != nil {
		job["dstPath"] = normalizePathListForStorage(job["dstPath"])
	}
	if err := normalizeJobFileSizeRange(job); err != nil {
		return err
	}
	return nil
}

func ValidateJobInput(job map[string]interface{}) error {
	if len(parsePathList(job["srcPath"])) == 0 ||
		len(parsePathList(job["dstPath"])) == 0 ||
		util.ToInt64(job["alistId"]) <= 0 {
		return publicError(msg.T(msg.LostPart))
	}
	if syncPathsOverlap(parsePathList(job["srcPath"]), parsePathList(job["dstPath"])) {
		return publicError(msg.T(msg.SyncPathOverlap))
	}
	if srcSelectionsNested(parsePathList(job["srcPath"])) {
		return publicError(msg.T(msg.SrcPathNested))
	}

	if enable, ok := job["enable"]; ok {
		enableInt := util.ToInt(enable)
		if enableInt != 0 && enableInt != 1 {
			return publicError(msg.T(msg.LostPart))
		}
	}

	method := util.ToInt(job["method"])
	if method < 0 || method > 2 {
		return publicError(msg.T(msg.LostPart))
	}

	isCron := util.ToInt(job["isCron"])
	if isCron < 0 || isCron > 2 {
		return publicError(msg.T(msg.LostPart))
	}
	if isCron == 0 && util.ToInt(job["interval"]) <= 0 {
		return publicError(msg.T(msg.IntervalLost))
	}
	return nil
}

func normalizeJobFileSizeRange(job map[string]interface{}) error {
	minSize, err := nonNegativeFileSize(job["minFileSize"])
	if err != nil {
		return publicError(msg.T(msg.MinFileSizeInvalid))
	}
	maxSize, err := nonNegativeFileSize(job["maxFileSize"])
	if err != nil {
		return publicError(msg.T(msg.MaxFileSizeInvalid))
	}
	if maxSize > 0 && minSize > maxSize {
		return publicError(msg.T(msg.MinFileSizeGtMax))
	}
	job["minFileSize"] = minSize
	job["maxFileSize"] = maxSize
	return nil
}

func nonNegativeFileSize(value interface{}) (int64, error) {
	if value == nil {
		return 0, nil
	}
	switch v := value.(type) {
	case int:
		if v < 0 {
			return 0, fmt.Errorf("negative file size")
		}
		return int64(v), nil
	case int64:
		if v < 0 {
			return 0, fmt.Errorf("negative file size")
		}
		return v, nil
	case float64:
		if v < 0 || math.Trunc(v) != v {
			return 0, fmt.Errorf("invalid file size")
		}
		parsed, err := strconv.ParseInt(strconv.FormatFloat(v, 'f', 0, 64), 10, 64)
		if err != nil || parsed < 0 {
			return 0, fmt.Errorf("invalid file size")
		}
		return parsed, nil
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return 0, nil
		}
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil || parsed < 0 {
			return 0, fmt.Errorf("invalid file size")
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("invalid file size")
	}
}

// AddJobClient creates a new job client
func AddJobClient(job map[string]interface{}, isInit bool) error {
	if err := CleanJobInput(job); err != nil {
		return err
	}
	if err := ValidateJobInput(job); err != nil {
		return err
	}
	if !isInit {
		// Interactive add: validate that the engine exists and hold the
		// reference lock until the job row is inserted, so a concurrent
		// RemoveClient cannot delete the engine in between and leave a job
		// pointing at nothing.
		alistRefMu.Lock()
		defer alistRefMu.Unlock()
		if _, err := alistDeps.GetAlistByID(util.ToInt64(job["alistId"])); err != nil {
			if err := publicErrorIf(err, msg.T(msg.AlistNotFound)); err != nil {
				return err
			}
		}
	}
	client, err := NewJobClient(job, isInit)
	if err != nil {
		return err
	}
	jobClientListMu.Lock()
	jobClientList[client.JobID] = client
	jobClientListMu.Unlock()
	return nil
}

// EditJobClient updates an existing job client
func EditJobClient(job map[string]interface{}) error {
	jobID := util.ToInt64(job["id"])
	if err := CleanJobInput(job); err != nil {
		return err
	}
	if err := ValidateJobInput(job); err != nil {
		return err
	}
	client, err := GetJobClientByID(jobID)
	if err != nil {
		return err
	}
	nextScheduler := newSchedulerStopped()
	if err := nextScheduler.AddJob(util.ToInt(job["isCron"]), job, func() {
		client.DoScheduled()
	}); err != nil {
		nextScheduler.Stop()
		return fmt.Errorf("schedule job: %w", err)
	}
	if err := mapper.UpdateJob(job); err != nil {
		nextScheduler.Stop()
		return fmt.Errorf("update job: %w", err)
	}
	nextScheduler.Start()
	oldScheduler := client.replaceJobConfig(job, nextScheduler)
	if oldScheduler != nil {
		oldScheduler.Stop()
	}
	return nil
}

// DoAllJobManual executes all enabled jobs manually
func DoAllJobManual() error {
	jobList, err := jobDeps.GetEnableJobList()
	if err != nil {
		return fmt.Errorf("get enabled jobs: %w", err)
	}
	if len(jobList) == 0 {
		return publicError(msg.T(msg.NoJobForRun))
	}
	for _, jobItem := range jobList {
		client, err := GetJobClientByID(util.ToInt64(jobItem["id"]))
		if err != nil {
			log.Printf("DoAllJobManual: job %v skipped: %v", jobItem["id"], err)
			continue
		}
		if client.enabled() {
			if err := client.DoManual(); err != nil {
				log.Printf("DoAllJobManual: job %v skipped: %v", jobItem["id"], err)
			}
		}
	}
	return nil
}

// DoJobManual executes a specific job manually
func DoJobManual(jobID int64) error {
	client, err := GetJobClientByID(jobID)
	if err != nil {
		return err
	}
	if !client.enabled() {
		return publicError(msg.T(msg.DisabledJobCannotRun))
	}
	return client.DoManual()
}

// RemoveJobClient deletes a job
func RemoveJobClient(jobID int64) error {
	client, err := GetJobClientByID(jobID)
	if err != nil {
		return err
	}
	client.StopJob(true)
	if !client.waitUntilIdle(2 * time.Minute) {
		return publicError(msg.T(msg.JobDeleteWaitTimeout))
	}
	if err := mapper.DeleteJob(jobID); err != nil {
		return fmt.Errorf("delete job: %w", err)
	}
	jobClientListMu.Lock()
	delete(jobClientList, jobID)
	jobClientListMu.Unlock()
	return nil
}

// ContinueJob enables a job
func ContinueJob(jobID int64) error {
	client, err := GetJobClientByID(jobID)
	if err != nil {
		return err
	}
	return client.ResumeJob()
}

// PauseJob disables a job
func PauseJob(jobID int64) error {
	client, err := GetJobClientByID(jobID)
	if err != nil {
		return err
	}
	if util.ToInt(client.jobSnapshot()["isCron"]) == 2 {
		return publicError(msg.T(msg.CannotDisableManualJob))
	}
	return client.StopJob(false)
}

// AbortJob aborts a running job
func AbortJob(jobID int64) error {
	client, err := GetJobClientByID(jobID)
	if err != nil {
		return err
	}
	client.AbortJob()
	return nil
}

// StopTask stops a currently running task without changing the job schedule.
func StopTask(taskID int64) error {
	job, err := mapper.GetJobByTaskID(taskID)
	if err != nil {
		if err := publicErrorIf(err, msg.T(msg.JobNotFound)); err != nil {
			return err
		}
	}
	client, err := GetJobClientByID(util.ToInt64(job["id"]))
	if err != nil {
		return err
	}
	task := client.currentTask()
	if task == nil || task.TaskID != taskID {
		return publicError(msg.T(msg.TaskNotRunningStop))
	}
	task.requestBreak()
	return nil
}

// RetryFailedTask replays the non-success items of a historical task.
func RetryFailedTask(taskID int64) error {
	job, err := mapper.GetJobByTaskID(taskID)
	if err != nil {
		if err := publicErrorIf(err, msg.T(msg.JobNotFound)); err != nil {
			return err
		}
	}
	client, err := GetJobClientByID(util.ToInt64(job["id"]))
	if err != nil {
		return err
	}
	if !client.enabled() {
		return publicError(msg.T(msg.DisabledJobCannotRun))
	}
	if client.isBusy() {
		return publicError(msg.T(msg.JobRunning))
	}
	count, err := jobDeps.CountJobTaskItemsByStatuses(taskID, retryableStatusValues())
	if err != nil {
		return fmt.Errorf("count retryable items: %w", err)
	}
	if count == 0 {
		return publicError(msg.T(msg.NoFailedTaskItems))
	}
	return client.DoRetryFailedTaskItems(taskID)
}

// GetJobList returns paginated job list
func GetJobList(params map[string]interface{}) (map[string]interface{}, error) {
	result, err := mapper.GetJobList(params)
	// An over-limit unpaginated request is the caller's to fix, so it reaches the
	// client as an actionable message instead of a generic 500.
	if err := publicErrorIf(err, msg.T(msg.ListTooLarge)); err != nil {
		return nil, err
	}
	return result, nil
}

// GetJobCurrent returns real-time task progress
func GetJobCurrent(jobID int64, params map[string]interface{}) (interface{}, error) {
	client, err := GetJobClientByID(jobID)
	if err != nil {
		return nil, err
	}
	taskClient := client.currentTask()
	if taskClient != nil {
		status, hasStatus := params["status"]
		if !hasStatus || fmt.Sprintf("%v", status) == "" {
			return taskClient.getCurrentPayload(), nil
		}
		statusInt := util.ToInt(status)
		pageSize := util.ToInt(params["pageSize"])
		pageNum := util.ToInt(params["pageNum"])
		if currentRequestStale(taskClient, params) {
			return taskClient.emptyCurrentTaskPage(statusInt, pageSize, pageNum, true), nil
		}
		if pageSize > 0 && pageNum > 0 {
			return taskClient.GetCurrentByStatusPage(statusInt, pageSize, pageNum), nil
		}
		return taskClient.GetCurrentByStatus(statusInt), nil
	}
	return nil, nil
}

func currentRequestStale(taskClient *JobTask, params map[string]interface{}) bool {
	expectedTaskID := util.ToInt64(params["expectedTaskId"])
	if expectedTaskID > 0 && expectedTaskID != taskClient.TaskID {
		return true
	}
	expectedCreateTime := util.ToInt64(params["expectedCreateTime"])
	return expectedCreateTime > 0 && expectedCreateTime != int64(taskClient.CreateTime)
}

// GetTaskList returns paginated task list with task num info
func GetTaskList(req map[string]interface{}) (map[string]interface{}, error) {
	jobTaskList, err := mapper.GetJobTaskList(req)
	if err := publicErrorIf(err, msg.T(msg.ListTooLarge)); err != nil {
		return nil, err
	}

	dataList, ok := jobTaskList["dataList"].([]map[string]interface{})
	if !ok {
		return jobTaskList, nil
	}

	var needUpdateList []map[string]interface{}
	missingTaskItems := make([]map[string]interface{}, 0)
	missingTaskIDs := make([]int64, 0)
	for _, item := range dataList {
		var taskNum map[string]interface{}
		taskNumStr, hasTaskNum := item["taskNum"]
		if hasTaskNum && taskNumStr != nil {
			taskNum = parseTaskNumJSON(taskNumStr)
			if taskNum == nil {
				taskID := util.ToInt64(item["id"])
				missingTaskIDs = append(missingTaskIDs, taskID)
				missingTaskItems = append(missingTaskItems, item)
			}
		} else {
			taskID := util.ToInt64(item["id"])
			missingTaskIDs = append(missingTaskIDs, taskID)
			missingTaskItems = append(missingTaskItems, item)
		}
		if taskNum != nil {
			for k, v := range taskNum {
				item[k] = v
			}
		}
	}

	if len(missingTaskItems) > 0 {
		taskNumByID := mapper.GetJobTaskCountsByTaskIDs(missingTaskIDs)
		for _, item := range missingTaskItems {
			taskID := util.ToInt64(item["id"])
			taskNum := taskNumByID[taskID]
			if taskNum == nil {
				taskNum = mapper.EmptyJobTaskCounts()
			}
			for k, v := range taskNum {
				item[k] = v
			}
			if util.ToInt(item["status"]) > 1 {
				taskNumJSON, _ := json.Marshal(taskNum)
				needUpdateList = append(needUpdateList, map[string]interface{}{
					"taskId":  item["id"],
					"taskNum": string(taskNumJSON),
				})
			}
		}
	}

	if len(needUpdateList) > 0 {
		scheduleTaskNumUpdate(needUpdateList)
	}

	return jobTaskList, nil
}

func parseTaskNumJSON(value interface{}) map[string]interface{} {
	var raw []byte
	switch v := value.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return nil
		}
		raw = []byte(v)
	case []byte:
		if len(v) == 0 {
			return nil
		}
		raw = v
	default:
		text := strings.TrimSpace(fmt.Sprintf("%v", value))
		if text == "" {
			return nil
		}
		raw = []byte(text)
	}
	var taskNum map[string]interface{}
	if err := json.Unmarshal(raw, &taskNum); err != nil {
		return nil
	}
	return taskNum
}

func scheduleTaskNumUpdate(taskNums []map[string]interface{}) {
	taskNums = cloneTaskRows(taskNums)
	taskNumUpdateMu.Lock()
	taskNumUpdateLatest = taskNums
	if taskNumUpdateActive {
		taskNumUpdateMu.Unlock()
		return
	}
	taskNumUpdateActive = true
	taskNumUpdateMu.Unlock()

	go func() {
		for {
			taskNumUpdateMu.Lock()
			pending := taskNumUpdateLatest
			taskNumUpdateLatest = nil
			taskNumUpdateMu.Unlock()

			if pending == nil {
				taskNumUpdateMu.Lock()
				if taskNumUpdateLatest == nil {
					taskNumUpdateActive = false
					taskNumUpdateMu.Unlock()
					return
				}
				taskNumUpdateMu.Unlock()
				continue
			}
			if err := mapper.UpdateJobTaskNumMany(pending); err != nil {
				log.Printf("Failed to update task counts: %v", err)
			}
		}
	}()
}

func GetTaskItemList(req map[string]interface{}) (map[string]interface{}, error) {
	result, err := mapper.GetJobTaskItemList(req)
	if err := publicErrorIf(err, msg.T(msg.ListTooLarge)); err != nil {
		return nil, err
	}
	return result, nil
}

// RemoveTask deletes a task
func RemoveTask(taskID int64) error {
	task, err := mapper.GetJobTaskByID(taskID)
	if err != nil {
		if err := publicErrorIf(err, msg.T(msg.TaskNotFound)); err != nil {
			return err
		}
	}
	status := taskStatusFromValue(task["status"])
	if status == taskStatusWaiting || status == taskStatusRunning {
		return publicError(msg.T(msg.JobRunningCannotDelete))
	}

	jobID := util.ToInt64(task["jobId"])
	jobClientListMu.RLock()
	client := jobClientList[jobID]
	jobClientListMu.RUnlock()
	if client != nil {
		if current := client.currentTask(); current != nil && current.TaskID == taskID {
			return publicError(msg.T(msg.JobRunningCannotDelete))
		}
	}

	if err := mapper.DeleteJobTaskByTaskID(taskID); err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	return nil
}
