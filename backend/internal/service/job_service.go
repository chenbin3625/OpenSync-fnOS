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
	jobClientList        = make(map[int64]*JobClient)
	jobClientListMu      sync.RWMutex
	jobClientLoadMu      sync.Mutex
	jobClientLoads       = make(map[int64]*sync.Mutex)
	historyCleanupMu     sync.Mutex
	historyDeletingTasks = make(map[int64]struct{})
)

var (
	taskNumUpdateMu     sync.Mutex
	taskNumUpdateLatest []map[string]interface{}
	taskNumUpdateActive bool
)

// jobTaskCountsByTaskIDs is swapped by tests to inject a count failure.
var jobTaskCountsByTaskIDs = mapper.GetJobTaskCountsByTaskIDs

// scheduleTaskNumUpdateFn is swapped by tests to observe what gets cached.
var scheduleTaskNumUpdateFn = scheduleTaskNumUpdate

// InitJobs loads and starts all enabled jobs on startup
func InitJobs() {
	logger := log.Default()
	if err := mapper.UpdateJobTaskStatusByStatus(); err != nil {
		logger.Printf("Failed to mark unfinished task history as aborted: %v", err)
	}
	// In the background: the cleanup deletes expired history batch by batch and
	// may VACUUM, which on a multi-GB database takes minutes. Run inline it held
	// startup before the socket was created, and the fnOS start script gives up
	// (and stops the app) after 20s without one — so large databases never
	// started at all. ShutdownJobs cancels it.
	StartTaskRetentionCleanupAsync()
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
	pendingDeleteApprovals.LoadFromDB()
}

func ShutdownJobs(ctx context.Context) {
	// Stop the retention cleanup first and wait for it within ctx: it may be
	// mid-VACUUM, and the caller closes the database right after this returns.
	if !stopTaskRetentionCleanup(ctx) {
		log.Printf("Warning: task history cleanup did not stop before the jobs shutdown deadline")
	}

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
	pendingDeleteApprovals.Shutdown(ctx)
	// Pending debounced progress publishes read the database; the caller closes
	// it right after ShutdownJobs returns, so their timers must be cancelled
	// here (same reason as the persist flush timers).
	StopJobProgressHub()
}

func waitJobClientIdleContext(ctx context.Context, client *JobClient) {
	client.waitUntilIdleContext(ctx, 0)
}

func CleanupExpiredTasks(logger *log.Logger, taskSaveDays int, now time.Time) {
	cleanupExpiredTasksContext(context.Background(), logger, taskSaveDays, now)
}

func cleanupExpiredTasksContext(ctx context.Context, logger *log.Logger, taskSaveDays int, now time.Time) {
	cutoff, ok := taskRetentionCutoff(now, taskSaveDays)
	if !ok {
		return
	}
	guard := mapper.TaskHistoryCleanupGuard{
		Blocked:          anyJobTaskRunning,
		ProtectedTaskIDs: activeProtectedHistoryTaskIDs,
		BeginDeleteBatch: beginHistoryDeleteBatch,
		EndDeleteBatch:   endHistoryDeleteBatch,
	}
	if err := mapper.DeleteJobTaskByRunTimeWithGuardContext(ctx, cutoff, guard); err != nil {
		if ctx.Err() != nil {
			logger.Printf("Task history cleanup stopped: %v", err)
			return
		}
		logger.Printf("Failed to delete expired task history: %v", err)
	}
}

// activeProtectedHistoryTaskIDs returns task IDs that retention cleanup must
// never delete: any currently running task and any historical task currently
// being replayed as a retry source.
func activeProtectedHistoryTaskIDs() []int64 {
	jobClientListMu.RLock()
	clients := make([]*JobClient, 0, len(jobClientList))
	for _, client := range jobClientList {
		clients = append(clients, client)
	}
	jobClientListMu.RUnlock()

	seen := make(map[int64]struct{})
	var ids []int64
	add := func(id int64) {
		if id <= 0 {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for _, client := range clients {
		if cur := client.currentTask(); cur != nil {
			add(cur.TaskID)
		}
		add(client.retrySource())
	}
	for _, approval := range pendingDeleteApprovals.ListAll() {
		add(approval.TaskID)
	}
	return ids
}

func beginHistoryDeleteBatch(taskIDs []int64) []int64 {
	historyCleanupMu.Lock()
	defer historyCleanupMu.Unlock()
	if anyJobTaskRunning() {
		return nil
	}
	protected := make(map[int64]struct{})
	for _, id := range activeProtectedHistoryTaskIDs() {
		protected[id] = struct{}{}
	}
	allowed := make([]int64, 0, len(taskIDs))
	for _, id := range taskIDs {
		if _, skip := protected[id]; skip {
			continue
		}
		historyDeletingTasks[id] = struct{}{}
		allowed = append(allowed, id)
	}
	return allowed
}

func endHistoryDeleteBatch(taskIDs []int64) {
	historyCleanupMu.Lock()
	for _, id := range taskIDs {
		delete(historyDeletingTasks, id)
	}
	historyCleanupMu.Unlock()
}

// anyJobTaskRunning reports whether any job is executing a task. Database
// maintenance that holds the write lock (WAL truncate, VACUUM) waits for this
// to be false: a running task's item writes fail after busy_timeout, and a
// failed write aborts the task.
func anyJobTaskRunning() bool {
	jobClientListMu.RLock()
	clients := make([]*JobClient, 0, len(jobClientList))
	for _, client := range jobClientList {
		clients = append(clients, client)
	}
	jobClientListMu.RUnlock()
	for _, client := range clients {
		if client.isBusy() {
			return true
		}
	}
	return false
}

func taskRetentionCutoff(now time.Time, taskSaveDays int) (int64, bool) {
	if taskSaveDays <= 0 {
		return 0, false
	}
	return now.Add(-time.Duration(taskSaveDays) * 24 * time.Hour).Unix(), true
}

// GetJobClientByID gets or creates a job client
func GetJobClientByID(jobID int64) (*JobClient, error) {
	loadMu := jobClientLoadMutex(jobID)
	loadMu.Lock()
	defer loadMu.Unlock()
	return getJobClientByIDLocked(jobID)
}

func jobClientLoadMutex(jobID int64) *sync.Mutex {
	jobClientLoadMu.Lock()
	defer jobClientLoadMu.Unlock()
	loadMu := jobClientLoads[jobID]
	if loadMu == nil {
		loadMu = &sync.Mutex{}
		jobClientLoads[jobID] = loadMu
	}
	return loadMu
}

func getJobClientByIDLocked(jobID int64) (*JobClient, error) {
	jobClientListMu.RLock()
	client, ok := jobClientList[jobID]
	jobClientListMu.RUnlock()
	if ok {
		return client, nil
	}

	// Load and build outside the global lock: the read hits the database and
	// NewJobClient starts a cron scheduler, and holding the write lock across
	// both stalled every other job lookup (anyJobTaskRunning, ShutdownJobs).
	job, err := mapper.GetJobByID(jobID)
	if err != nil {
		if err := publicErrorIf(err, msg.T(msg.JobNotFound)); err != nil {
			return nil, err
		}
	}
	// NewJobClient runs validateJobInput. A stored row is loaded under the same
	// rules as InitJobs (isInit): overlapping or nested sources are refused, a
	// legacy nested destination is accepted with a warning. Using the stricter
	// create-time rule here made a job that loaded at startup fail on the next
	// lazy lookup after it dropped out of the cache. The row is normalized
	// first, exactly as AddJobClient does at startup, so both paths judge the
	// same values.
	if err := CleanJobInput(job); err != nil {
		return nil, err
	}
	built, err := NewJobClient(job, true)
	if err != nil {
		return nil, err
	}

	jobClientListMu.Lock()
	if existing, ok := jobClientList[jobID]; ok {
		jobClientListMu.Unlock()
		// Another caller won the race; discard the duplicate and its cron.
		if built.Scheduler != nil {
			built.Scheduler.Stop()
		}
		return existing, nil
	}
	jobClientList[jobID] = built
	jobClientListMu.Unlock()
	return built, nil
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
	// A cron job without a second would be scheduled with "*" in that field and
	// fire every second. Store the "on the minute" value the UI sends instead.
	if util.ToInt(job["isCron"]) == 1 && job["second"] == nil {
		job["second"] = "0"
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
	if job["include"] != nil {
		includeStr := fmt.Sprintf("%v", job["include"])
		if invalid := invalidIncludeRules(includeStr); len(invalid) > 0 {
			return publicError(msg.IncludeRulesUnsupported(invalid))
		}
		normalized := normalizeInclude(includeStr)
		if normalized == "" {
			job["include"] = nil
		} else {
			job["include"] = normalized
		}
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
	if err := normalizeJobExtendedFields(job); err != nil {
		return err
	}
	// Persisted after srcPath/dstPath are normalized: this is what keeps the
	// destination layout stable when the source selection is edited later.
	ensureJobDstMap(job)
	return nil
}

func normalizeJobExtendedFields(job map[string]interface{}) error {
	// Scan intervals (0..300 seconds)
	for _, key := range []string{"scanIntervalS", "scanIntervalT"} {
		if raw, ok := job[key]; ok && raw != nil && raw != "" {
			n, valid := strictJobInt(raw)
			if !valid || n < 0 || n > 300 {
				return publicError(msg.T(msg.ScanIntervalInvalid))
			}
			job[key] = n
		}
	}
	// Per-job concurrency / retry overrides
	if raw, ok := job["copyConcurrency"]; ok && raw != nil && raw != "" {
		n, valid := strictJobInt(raw)
		if !valid || n < 0 || n > 100 {
			return publicError(msg.T(msg.CopyConcurrencyInvalid))
		}
		job["copyConcurrency"] = n
	} else {
		job["copyConcurrency"] = 0
	}
	if raw, ok := job["scanConcurrency"]; ok && raw != nil && raw != "" {
		n, valid := strictJobInt(raw)
		if !valid || n < 0 || n > 50 {
			return publicError(msg.T(msg.ScanConcurrencyInvalid))
		}
		job["scanConcurrency"] = n
	} else {
		job["scanConcurrency"] = 0
	}
	if raw, ok := job["maxRetries"]; ok && raw != nil && raw != "" {
		n, valid := strictJobInt(raw)
		if !valid || n < -1 || n > 10 {
			return publicError(msg.T(msg.MaxRetriesInvalid))
		}
		job["maxRetries"] = n
	} else {
		job["maxRetries"] = -1
	}
	// Compare mode (0..2) & conflict policy (0..2) & deleteEmptySrcDirs (0..1)
	if raw, ok := job["compareMode"]; ok && raw != nil && raw != "" {
		n, valid := strictJobInt(raw)
		if !valid || n < 0 || n > 2 {
			return publicError(msg.T(msg.CompareModeInvalid))
		}
		job["compareMode"] = n
	} else {
		job["compareMode"] = 0
	}
	if raw, ok := job["conflictPolicy"]; ok && raw != nil && raw != "" {
		n, valid := strictJobInt(raw)
		if !valid || n < 0 || n > 2 {
			return publicError(msg.T(msg.ConflictPolicyInvalid))
		}
		job["conflictPolicy"] = n
	} else {
		job["conflictPolicy"] = 0
	}
	if raw, ok := job["deleteEmptySrcDirs"]; ok && raw != nil && raw != "" {
		n, valid := strictJobEnable(raw)
		if !valid {
			return publicError(msg.T(msg.LostPart))
		}
		job["deleteEmptySrcDirs"] = n
	} else {
		job["deleteEmptySrcDirs"] = 0
	}
	// Target trash
	if raw, ok := job["trashEnable"]; ok && raw != nil && raw != "" {
		n, valid := strictJobEnable(raw)
		if !valid {
			return publicError(msg.T(msg.LostPart))
		}
		job["trashEnable"] = n
	} else {
		job["trashEnable"] = 0
	}
	trashDir, err := normalizeTrashDir(util.StringValue(job["trashDir"]))
	if err != nil {
		return err
	}
	job["trashDir"] = trashDir
	if raw, ok := job["trashRetentionDays"]; ok && raw != nil && raw != "" {
		n, valid := strictJobInt(raw)
		if !valid || n < 0 || n > maxTrashRetentionDays {
			return publicError(msg.T(msg.TrashRetentionInvalid))
		}
		job["trashRetentionDays"] = n
	} else {
		job["trashRetentionDays"] = defaultTrashRetention
	}
	// Filter mode & time filters
	if raw, ok := job["filterMode"]; ok && raw != nil && raw != "" {
		n, valid := strictJobInt(raw)
		if !valid || (n != 0 && n != 1) {
			return publicError(msg.T(msg.FilterModeInvalid))
		}
		job["filterMode"] = n
	} else {
		job["filterMode"] = 0
	}
	for _, key := range []string{"minFileAgeMinutes", "olderThanDays", "newerThanDays"} {
		if raw, ok := job[key]; ok && raw != nil && raw != "" {
			n, valid := strictJobInt(raw)
			if !valid || n < 0 || n > 36500 {
				return publicError(msg.T(msg.TimeFilterInvalid))
			}
			job[key] = n
		} else {
			job[key] = 0
		}
	}
	if older := util.ToInt(job["olderThanDays"]); older > 0 {
		if newer := util.ToInt(job["newerThanDays"]); newer > 0 && older >= newer {
			return publicError(msg.T(msg.TimeFilterRangeConflict))
		}
	}
	// Time window
	windowEnable := 0
	if raw, ok := job["windowEnable"]; ok && raw != nil && raw != "" {
		n, valid := strictJobEnable(raw)
		if !valid {
			return publicError(msg.T(msg.TimeWindowInvalid))
		}
		windowEnable = n
	}
	job["windowEnable"] = windowEnable
	winStart, winEnd, err := ValidateTimeWindow(windowEnable, util.StringValue(job["windowStart"]), util.StringValue(job["windowEnd"]))
	if err != nil {
		return err
	}
	if winStart == "" {
		job["windowStart"] = nil
	} else {
		job["windowStart"] = winStart
	}
	if winEnd == "" {
		job["windowEnd"] = nil
	} else {
		job["windowEnd"] = winEnd
	}
	if raw, ok := job["windowStopOutside"]; ok && raw != nil && raw != "" {
		n, valid := strictJobEnable(raw)
		if !valid {
			return publicError(msg.T(msg.TimeWindowInvalid))
		}
		job["windowStopOutside"] = n
	} else {
		job["windowStopOutside"] = 0
	}
	// Per-job notification binding
	notifyMode := 0
	if raw, ok := job["notifyMode"]; ok && raw != nil && raw != "" {
		n, valid := strictJobInt(raw)
		if !valid || n < 0 || n > 2 {
			return publicError(msg.T(msg.NotifyModeInvalid))
		}
		notifyMode = n
	}
	job["notifyMode"] = notifyMode
	if notifyMode == 1 {
		ids, err := parseNotifyIDList(job["notifyIds"])
		if err != nil {
			return err
		}
		if len(ids) > 0 {
			encoded, _ := json.Marshal(ids)
			job["notifyIds"] = string(encoded)
		} else {
			job["notifyIds"] = "[]"
		}
	} else {
		job["notifyIds"] = nil
	}
	return nil
}

func ValidateJobInput(job map[string]interface{}) error {
	return validateJobInput(job, true)
}

// validateJobInput checks a job definition. rejectNestedDst enables the
// nested-destination rule, which was added after jobs could already be saved
// with such layouts: it is enforced when a user creates or edits a job, but a
// stored job is still loaded at startup (with a warning) instead of silently
// dropping out of the scheduler while the task list keeps showing it.
func validateJobInput(job map[string]interface{}, rejectNestedDst bool) error {
	rawSrc := parseRawPathList(job["srcPath"])
	rawDst := parseRawPathList(job["dstPath"])
	if pathListHasParentSegment(rawSrc) || pathListHasParentSegment(rawDst) ||
		pathListHasUnsafeChars(rawSrc) || pathListHasUnsafeChars(rawDst) {
		return publicError(msg.T(msg.LostPart))
	}

	srcPaths := parsePathList(job["srcPath"])
	dstPaths := parsePathList(job["dstPath"])
	alistID, alistOK := strictJobInt64(job["alistId"])
	if len(srcPaths) == 0 ||
		len(dstPaths) == 0 ||
		!alistOK || alistID <= 0 {
		return publicError(msg.T(msg.LostPart))
	}
	for _, p := range srcPaths {
		if p == "." || (rejectNestedDst && !strings.HasPrefix(p, "/")) {
			return publicError(msg.T(msg.LostPart))
		}
	}
	for _, p := range dstPaths {
		if p == "." || (rejectNestedDst && !strings.HasPrefix(p, "/")) {
			return publicError(msg.T(msg.LostPart))
		}
	}
	if syncPathsOverlap(srcPaths, dstPaths) {
		return publicError(msg.T(msg.SyncPathOverlap))
	}
	if srcSelectionsNested(srcPaths) {
		return publicError(msg.T(msg.SrcPathNested))
	}
	if resolvedDstPathsNestedForJob(job) {
		if rejectNestedDst {
			return publicError(msg.T(msg.DstPathNested))
		}
		log.Printf("Warning: job %v has nested destination directories; edit it to separate them (%s)", job["id"], msg.T(msg.DstPathNested))
	}

	if enable, ok := job["enable"]; ok && enable != nil && enable != "" {
		if _, ok := strictJobEnable(enable); !ok {
			return publicError(msg.T(msg.LostPart))
		}
	}

	method := 0
	if rawMethod, exists := job["method"]; exists && rawMethod != nil && rawMethod != "" {
		parsed, ok := strictJobInt(rawMethod)
		if !ok {
			return publicError(msg.T(msg.LostPart))
		}
		method = parsed
	}
	if method < 0 || method > 2 {
		return publicError(msg.T(msg.LostPart))
	}
	if method == 2 && len(dstPaths) != 1 {
		return publicError("移动模式只允许一个目标目录")
	}

	isCron := 0
	if rawIsCron, exists := job["isCron"]; exists && rawIsCron != nil && rawIsCron != "" {
		parsed, ok := strictJobInt(rawIsCron)
		if !ok {
			return publicError(msg.T(msg.LostPart))
		}
		isCron = parsed
	}
	if isCron < 0 || isCron > 2 {
		return publicError(msg.T(msg.LostPart))
	}
	if isCron == 0 {
		interval, ok := strictJobInt64(job["interval"])
		if !ok || interval <= 0 || interval > maxJobIntervalMinutes {
			return publicError(msg.T(msg.IntervalLost))
		}
	} else if isCron == 1 {
		spec, err := buildCronSpec(job)
		if err != nil {
			return publicError(err.Error())
		}
		if spec != "" {
			if err := validateCronSpec(spec); err != nil {
				return publicError(msg.T(msg.CronLost))
			}
		}
	}
	if job["exclude"] != nil {
		if invalid := invalidExcludeRules(fmt.Sprintf("%v", job["exclude"])); len(invalid) > 0 {
			return publicError(msg.ExcludeRulesUnsupported(invalid))
		}
	}
	if job["include"] != nil {
		if invalid := invalidIncludeRules(fmt.Sprintf("%v", job["include"])); len(invalid) > 0 {
			return publicError(msg.IncludeRulesUnsupported(invalid))
		}
	}
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
	if err := normalizeJobExtendedFields(cloneJobConfig(job)); err != nil {
		return err
	}
	return nil
}

// validateJobDestinationConflicts rejects a job whose resolved destination
// roots overlap another job's on the same engine. Two jobs writing (and, in
// mirror mode, deleting) one destination subtree silently destroy each other's
// files; the per-job checks could not see this.
func validateJobDestinationConflicts(job map[string]interface{}) error {
	alistID := util.ToInt64(job["alistId"])
	if alistID <= 0 {
		return nil
	}
	jobID := util.ToInt64(job["id"])
	mine := resolvedDstPathsForJob(job)
	if len(mine) == 0 {
		return nil
	}
	loadJobs := mapper.GetJobListAll
	if jobDeps != nil && jobDeps.GetJobListAll != nil {
		loadJobs = jobDeps.GetJobListAll
	}
	jobs, err := loadJobs()
	if err != nil {
		return fmt.Errorf("load jobs for destination conflict check: %w", err)
	}
	for _, other := range jobs {
		if util.ToInt64(other["id"]) == jobID || util.ToInt64(other["alistId"]) != alistID {
			continue
		}
		for _, otherDst := range resolvedDstPathsForJob(other) {
			for _, dst := range mine {
				if syncPathContains(dst, otherDst) || syncPathContains(otherDst, dst) {
					return publicError(msg.JobDstConflict(otherDst))
				}
			}
		}
	}
	return nil
}

func strictJobInt64(value interface{}) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v || v < math.MinInt64 || v >= float64(1<<63) {
			return 0, false
		}
		return int64(v), true
	case json.Number:
		parsed, err := strconv.ParseInt(string(v), 10, 64)
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func strictJobInt(value interface{}) (int, bool) {
	parsed, ok := strictJobInt64(value)
	if !ok {
		return 0, false
	}
	asInt := int(parsed)
	return asInt, int64(asInt) == parsed
}

func strictJobEnable(value interface{}) (int, bool) {
	switch v := value.(type) {
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true":
			return 1, true
		case "0", "false":
			return 0, true
		default:
			return 0, false
		}
	default:
		parsed, ok := strictJobInt(value)
		if !ok || (parsed != 0 && parsed != 1) {
			return 0, false
		}
		return parsed, true
	}
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
	_, err := addJobClient(job, isInit)
	return err
}

// CreateJobClient creates a new interactive job client and returns its persisted ID.
func CreateJobClient(job map[string]interface{}) (int64, error) {
	return addJobClient(job, false)
}

func addJobClient(job map[string]interface{}, isInit bool) (int64, error) {
	// A row loaded from a pre-migration database has no stored mapping yet.
	// Freeze the layout it has been using before this run's computed mapping
	// could change on the first edit.
	needsPersistedDstMap := isInit && jobDstMapRaw(job["dstMap"]) == ""
	if err := CleanJobInput(job); err != nil {
		return 0, err
	}
	if needsPersistedDstMap {
		if err := mapper.UpdateJob(job); err != nil {
			log.Printf("Failed to persist the initial destination mapping of job %v: %v", job["id"], err)
		}
	}
	if err := validateJobInput(job, !isInit); err != nil {
		return 0, err
	}
	if !isInit {
		// Interactive add: hold alistRefMu across destination conflict
		// validation, engine existence check, and job row insertion so
		// concurrent creates/edits cannot bypass destination conflict checks
		// and a concurrent RemoveClient cannot delete the engine in between.
		alistRefMu.Lock()
		defer alistRefMu.Unlock()
		if err := validateJobDestinationConflicts(job); err != nil {
			return 0, err
		}
		if _, err := alistDeps.GetAlistByID(util.ToInt64(job["alistId"])); err != nil {
			if err := publicErrorIf(err, msg.T(msg.AlistNotFound)); err != nil {
				return 0, err
			}
		}
	}
	client, err := NewJobClient(job, isInit)
	if err != nil {
		return 0, err
	}
	jobClientListMu.Lock()
	jobClientList[client.JobID] = client
	jobClientListMu.Unlock()
	return client.JobID, nil
}

// EditJobClient updates an existing job client
func EditJobClient(job map[string]interface{}) error {
	jobID := util.ToInt64(job["id"])
	if job["dstMap"] == nil && jobID > 0 {
		// The edit form does not send the mapping. It must survive the edit, or
		// every remaining source would be re-mapped by the new source set.
		if previous, err := mapper.GetJobByID(jobID); err == nil {
			job["dstMap"] = previous["dstMap"]
		}
	}
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
	// Serialize the whole configuration swap with delete/pause/resume: two
	// concurrent edits used to leave the active scheduler and the database
	// holding different configurations, and an edit racing a delete could arm a
	// scheduler for a job whose row was already gone.
	if !client.lockOp() {
		client.opMu.Unlock()
		return publicError(msg.T(msg.JobNotFound))
	}
	defer client.opMu.Unlock()

	nextScheduler := newSchedulerStopped()
	if err := nextScheduler.AddJob(util.ToInt(job["isCron"]), job, func() {
		client.DoScheduled()
	}); err != nil {
		nextScheduler.Stop()
		return fmt.Errorf("schedule job: %w", err)
	}
	// Destination conflict validation, the engine reference check, and the row
	// update all commit under alistRefMu: concurrent creates/edits cannot both
	// pass conflict validation and persist overlapping destination roots, and
	// RemoveClient cannot delete the referenced engine in between.
	alistRefMu.Lock()
	conflictErr := validateJobDestinationConflicts(job)
	var alistErr error
	var updateErr error
	if conflictErr == nil {
		_, alistErr = alistDeps.GetAlistByID(util.ToInt64(job["alistId"]))
		if alistErr == nil {
			updateErr = mapper.UpdateJob(job)
		}
	}
	alistRefMu.Unlock()
	if conflictErr != nil {
		nextScheduler.Stop()
		return conflictErr
	}
	if alistErr != nil {
		nextScheduler.Stop()
		return publicErrorIf(alistErr, msg.T(msg.AlistNotFound))
	}
	if updateErr != nil {
		nextScheduler.Stop()
		return fmt.Errorf("update job: %w", updateErr)
	}
	prevConfig := client.jobSnapshot()
	nextScheduler.Start()
	oldScheduler := client.replaceJobConfig(job, nextScheduler)
	if oldScheduler != nil {
		oldScheduler.Stop()
	}
	if jobConfigInvalidatesPendingDeletes(prevConfig, job) {
		pendingDeleteApprovals.ClearForJob(jobID)
	}
	return nil
}

func jobConfigInvalidatesPendingDeletes(prev, next map[string]interface{}) bool {
	if util.ToInt(next["method"]) != 1 {
		return true
	}
	if prev == nil {
		return false
	}
	for _, key := range []string{
		"alistId", "method", "srcPath", "dstPath", "dstMap",
		"exclude", "include", "filterMode",
		"minFileSize", "maxFileSize", "minFileAgeMinutes", "olderThanDays", "newerThanDays",
		"compareMode", "conflictPolicy",
	} {
		if fmt.Sprint(prev[key]) != fmt.Sprint(next[key]) {
			return true
		}
	}
	return false
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
	loadMu := jobClientLoadMutex(jobID)
	loadMu.Lock()
	jobClientListMu.RLock()
	client := jobClientList[jobID]
	jobClientListMu.RUnlock()
	if client == nil {
		// A job absent from jobClientList (such as a legacy/invalid job that
		// failed startup validation) has no active scheduler or task. Delete the
		// DB row directly without loading or validating it so zombie jobs can
		// always be removed.
		defer loadMu.Unlock()
		if _, err := mapper.GetJobByID(jobID); err != nil {
			if err := publicErrorIf(err, msg.T(msg.JobNotFound)); err != nil {
				return err
			}
		}
		if err := removeJobRow(jobID); err != nil {
			return fmt.Errorf("delete job: %w", err)
		}
		pendingDeleteApprovals.ClearForJob(jobID)
		return nil
	}
	// Release loadMu before pausing and waiting for idle: holding loadMu across
	// pauseForRemoval / waitUntilIdle would deadlock with or stall callers such
	// as GetJobCurrent.
	loadMu.Unlock()

	// Stop accepting new configuration changes and new tasks for this job, then
	// release opMu while waiting: holding it across the idle wait would block
	// an unrelated shutdown for minutes.
	if !client.lockOp() {
		client.opMu.Unlock()
		if client.isClosing() {
			// A concurrent delete is already tearing this job down; report
			// success so a duplicated request is idempotent.
			return nil
		}
		return publicError(msg.T(msg.JobNotFound))
	}
	if !client.beginCloseLocked() {
		client.opMu.Unlock()
		return nil
	}
	// Pause instead of stopping: the scheduler stays usable so a delete that
	// cannot complete rolls back to the exact previous state.
	previousEnable := client.pauseForRemoval()
	client.opMu.Unlock()

	if !client.waitUntilIdle(removeJobIdleTimeout) {
		// The task is still draining and the row stays. Restore the previous
		// schedule and enable flag: leaving the client disabled with a dead
		// scheduler made it "enabled in the database, never scheduled", and a
		// later resume failed with CannotResumeLostJob until a restart.
		client.opMu.Lock()
		client.restoreAfterFailedRemoval(previousEnable)
		client.closing.Store(false)
		client.opMu.Unlock()
		return publicError(msg.T(msg.JobDeleteWaitTimeout))
	}

	// Lock order: loadMu -> opMu.
	loadMu.Lock()
	client.opMu.Lock()
	if client.deleted {
		client.opMu.Unlock()
		loadMu.Unlock()
		return nil
	}
	deleteErr := removeJobRow(jobID)
	if deleteErr == nil {
		client.deleted = true
		// Only now is the job really gone; stop its scheduler for good.
		client.stopJobLocked(true) //nolint:errcheck // remove=true never fails
		client.opMu.Unlock()
		jobClientListMu.Lock()
		if jobClientList[jobID] == client {
			delete(jobClientList, jobID)
		}
		jobClientListMu.Unlock()
		loadMu.Unlock()
		pendingDeleteApprovals.ClearForJob(jobID)
		return nil
	}
	// Row still exists: roll back to the previous state and allow a retry.
	client.restoreAfterFailedRemoval(previousEnable)
	client.closing.Store(false)
	client.opMu.Unlock()
	loadMu.Unlock()
	return fmt.Errorf("delete job: %w", deleteErr)
}

// removeJobIdleTimeout bounds how long a delete waits for the running task;
// removeJobRow deletes the stored job. Both are swapped by tests.
var (
	removeJobIdleTimeout = 2 * time.Minute
	removeJobRow         = mapper.DeleteJob
)

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

func ApproveFullSyncTask(taskID int64) error {
	return pendingDeleteApprovals.ApproveByTaskID(taskID)
}

func RejectFullSyncTask(taskID int64) error {
	return pendingDeleteApprovals.RejectByTaskID(taskID)
}

func GetPendingApproval(jobID int64) *FullSyncApproval {
	return pendingDeleteApprovals.GetByJobID(jobID)
}

func ListPendingApprovals() []*FullSyncApproval {
	return pendingDeleteApprovals.ListAll()
}

// RetryFailedTask replays all non-success items of a historical task.
func RetryFailedTask(taskID int64) error {
	return RetryFailedTaskItems(taskID, nil)
}

// RetryFailedTaskItems replays either all non-success items (when itemIDs is
// empty) or only the selected item IDs of a historical task.
func RetryFailedTaskItems(taskID int64, itemIDs []int64) error {
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
	return client.DoRetrySelectedTaskItems(taskID, itemIDs)
}

// GetJobList returns paginated job list
func GetJobList(params map[string]interface{}) (map[string]interface{}, error) {
	result, err := mapper.GetJobList(params)
	// An over-limit unpaginated request is the caller's to fix, so it reaches the
	// client as an actionable message instead of a generic 500.
	if err := publicErrorIf(err, msg.T(msg.ListTooLarge)); err != nil {
		return nil, err
	}
	addJobNextRunTimes(result)
	return result, nil
}

// addJobNextRunTimes adds the real next run time of every scheduled job. The
// list page used to show only the schedule rule ("every 60 minutes"), which
// hid when the job would actually run next, especially after a restart.
func addJobNextRunTimes(result map[string]interface{}) {
	if result == nil {
		return
	}
	rows, ok := result["dataList"].([]map[string]interface{})
	if !ok || len(rows) == 0 {
		return
	}
	clients := make(map[int64]*JobClient, len(rows))
	jobClientListMu.RLock()
	for _, row := range rows {
		jobID := util.ToInt64(row["id"])
		if client, exists := jobClientList[jobID]; exists {
			clients[jobID] = client
		}
	}
	jobClientListMu.RUnlock()
	queuedSet := engineQueue.AllQueuedJobIDs()
	latestTasks, _ := mapper.GetLatestTaskPerJob()
	for _, row := range rows {
		jobID := util.ToInt64(row["id"])
		next := int64(0)
		running := false
		if client := clients[jobID]; client != nil {
			if nextTime, scheduled := client.nextRunTime(); scheduled {
				next = nextTime.Unix()
			}
			if task := client.currentTask(); task != nil {
				running = true
			}
		}
		row["nextRunTime"] = next
		row["running"] = running
		row["queued"] = queuedSet[jobID]
		row["pendingApproval"] = pendingDeleteApprovals.Has(jobID)
		if latest, ok := latestTasks[jobID]; ok {
			row["lastStatus"] = util.ToInt(latest["status"])
		}
	}
}

// getJobCurrentFromCache returns live task progress purely from the in-memory
// jobClientList cache without acquiring loadMu or hitting the database.
func getJobCurrentFromCache(jobID int64) interface{} {
	jobClientListMu.RLock()
	client := jobClientList[jobID]
	jobClientListMu.RUnlock()
	if client == nil {
		return nil
	}
	taskClient := client.currentTask()
	if taskClient == nil {
		return nil
	}
	return taskClient.getCurrentPayload()
}

// GetJobCurrent returns real-time task progress
func GetJobCurrent(jobID int64, params map[string]interface{}) (interface{}, error) {
	jobClientListMu.RLock()
	client := jobClientList[jobID]
	jobClientListMu.RUnlock()
	if client == nil {
		var err error
		client, err = GetJobClientByID(jobID)
		if err != nil {
			return nil, err
		}
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
			// A parsed-but-incomplete cache entry (old schema, partial write) is
			// as useless as a missing one: a cached value is never recomputed, so
			// the task would show missing counters forever.
			if taskNum != nil && !taskNumCountsComplete(taskNum) {
				taskNum = nil
			}
		}
		if taskNum == nil {
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
		taskNumByID, err := jobTaskCountsByTaskIDs(missingTaskIDs)
		if err != nil {
			// Nothing is cached on failure: writing zeros into taskNum would
			// make these tasks show "0 items" permanently, since a cached value
			// is never recomputed.
			return nil, fmt.Errorf("count task items: %w", err)
		}
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
				if taskNumJSON, err := json.Marshal(taskNum); err == nil {
					needUpdateList = append(needUpdateList, map[string]interface{}{
						"taskId":  item["id"],
						"taskNum": string(taskNumJSON),
					})
				}
			}
		}
	}

	if len(needUpdateList) > 0 {
		scheduleTaskNumUpdateFn(needUpdateList)
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

// taskNumCounterKeys are the counters a cached job_task.taskNum must carry to
// be usable by the task list. A cache entry missing any of them is recomputed.
var taskNumCounterKeys = []string{"waitNum", "runningNum", "successNum", "failNum", "otherNum", "allNum", "sumSize"}

func taskNumCountsComplete(taskNum map[string]interface{}) bool {
	for _, key := range taskNumCounterKeys {
		if _, ok := taskNum[key]; !ok {
			return false
		}
	}
	return true
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
		// A "retry failed items" run replays this task's items in id-ordered
		// batches. Deleting the source task mid-run silently drops the remainder
		// of the replay, so it is locked while that run is active.
		if client.retrySource() == taskID {
			return publicError(msg.T(msg.JobRunningCannotDelete))
		}
	}

	if err := mapper.DeleteJobTaskByTaskID(taskID); err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	return nil
}
