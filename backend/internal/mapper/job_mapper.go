package mapper

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"strconv"
	"strings"
	"time"
)

// List queries order by createTime DESC, id DESC. createTime is second-grained
// and a flush can write several items with the same value, so ties are the norm
// rather than an edge case; without the unique id tiebreaker LIMIT/OFFSET
// paging can repeat or skip rows between pages.
const (
	jobTaskItemListColumns     = "id, taskId, srcPath, dstPath, isPath, fileName, fileSize, type, alistTaskId, status, progress, errMsg, createTime"
	jobTaskItemRuntimeColumns  = "id, taskId, srcPath, dstPath, isPath, fileName, fileSize, type, alistTaskId, status, errMsg, createTime"
	expiredTaskDeleteBatchSize = 500
)

// GetJobList gets paginated job list
func GetJobList(params map[string]interface{}) (map[string]interface{}, error) {
	return FetchAllToPage("SELECT * FROM job ORDER BY createTime DESC, id DESC", params)
}

// GetJobListAll gets all jobs
func GetJobListAll() ([]map[string]interface{}, error) {
	return FetchAllToTable("SELECT * FROM job ORDER BY createTime DESC, id DESC")
}

// GetEnableJobList gets all enabled jobs
func GetEnableJobList() ([]map[string]interface{}, error) {
	return FetchAllToTable("SELECT * FROM job WHERE enable=1")
}

// GetJobByID gets job by ID
func GetJobByID(jobID int64) (map[string]interface{}, error) {
	rst, err := FetchAllToTable("SELECT * FROM job WHERE id=?", jobID)
	if err != nil {
		return nil, err
	}
	if len(rst) == 0 {
		return nil, notFound(ErrJobNotFound, msg.JobNotFound)
	}
	return rst[0], nil
}

// GetLastJobTaskCreateTime returns the start time of the most recent task of a
// job, or 0 when it has never run. Used to anchor fixed-interval scheduling to
// the last run instead of process start.
func GetLastJobTaskCreateTime(jobID int64) (int64, error) {
	var createTime int64
	err := GetDB().QueryRow(
		`SELECT IFNULL(MAX(createTime), 0) FROM job_task WHERE jobId=?`,
		jobID,
	).Scan(&createTime)
	if err != nil {
		return 0, err
	}
	return createTime, nil
}

// GetJobByTaskID gets job by task ID
func GetJobByTaskID(taskID int64) (map[string]interface{}, error) {
	rst, err := FetchAllToTable(
		`SELECT j.* FROM job AS j
		 INNER JOIN job_task AS jt ON jt.jobId=j.id
		 WHERE jt.id=?`,
		taskID,
	)
	if err != nil {
		return nil, err
	}
	if len(rst) == 0 {
		return nil, notFound(ErrJobNotFound, msg.JobNotFound)
	}
	return rst[0], nil
}

func jobExtVal(job map[string]interface{}, key string, fallback interface{}) interface{} {
	if v, ok := job[key]; ok && v != nil {
		return v
	}
	return fallback
}

// AddJob inserts a new job
func AddJob(job map[string]interface{}) (int64, error) {
	hasNativeOpts, _ := tableHasColumnDBErr(GetDB(), "job", "bwLimit")
	hasExt := hasNativeOpts
	if !hasExt {
		hasExt, _ = tableHasColumnDBErr(GetDB(), "job", "copyConcurrency")
	}
	var (
		id  int64
		err error
	)
	if hasNativeOpts {
		id, err = ExecuteInsert(
			`INSERT INTO job (enable, remark, srcPath, dstPath, alistId, useCacheT, scanIntervalT, useCacheS, scanIntervalS,
			 method, interval, isCron, month, day, day_of_week, hour, minute, second, exclude,
			 minFileSize, maxFileSize, dstMap,
			 copyConcurrency, scanConcurrency, maxRetries, compareMode, conflictPolicy, deleteEmptySrcDirs,
			 trashEnable, trashDir, trashRetentionDays, filterMode, include, minFileAgeMinutes, olderThanDays, newerThanDays,
			 windowEnable, windowStart, windowEnd, windowStopOutside, notifyMode, notifyIds,
			 bwLimit, tpsLimit, tpsLimitBurst, modifyWindow, fastList, multiThreadStreams, createEmptySrcDirs, followSymlinks, extraFlags)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			job["enable"], job["remark"], job["srcPath"], job["dstPath"], job["alistId"],
			job["useCacheT"], job["scanIntervalT"], job["useCacheS"], job["scanIntervalS"],
			job["method"], job["interval"], job["isCron"],
			job["month"], job["day"], job["day_of_week"],
			job["hour"], job["minute"], job["second"], job["exclude"],
			job["minFileSize"], job["maxFileSize"], job["dstMap"],
			jobExtVal(job, "copyConcurrency", 0),
			jobExtVal(job, "scanConcurrency", 0),
			jobExtVal(job, "maxRetries", -1),
			jobExtVal(job, "compareMode", 0),
			jobExtVal(job, "conflictPolicy", 0),
			jobExtVal(job, "deleteEmptySrcDirs", 0),
			jobExtVal(job, "trashEnable", 0),
			jobExtVal(job, "trashDir", ".opensync-trash"),
			jobExtVal(job, "trashRetentionDays", 30),
			jobExtVal(job, "filterMode", 0),
			job["include"],
			jobExtVal(job, "minFileAgeMinutes", 0),
			jobExtVal(job, "olderThanDays", 0),
			jobExtVal(job, "newerThanDays", 0),
			jobExtVal(job, "windowEnable", 0),
			job["windowStart"],
			job["windowEnd"],
			jobExtVal(job, "windowStopOutside", 0),
			jobExtVal(job, "notifyMode", 0),
			job["notifyIds"],
			job["bwLimit"],
			jobExtVal(job, "tpsLimit", 0.0),
			jobExtVal(job, "tpsLimitBurst", 0),
			job["modifyWindow"],
			jobExtVal(job, "fastList", 0),
			jobExtVal(job, "multiThreadStreams", 0),
			jobExtVal(job, "createEmptySrcDirs", 0),
			jobExtVal(job, "followSymlinks", 0),
			job["extraFlags"],
		)
	} else if hasExt {
		id, err = ExecuteInsert(
			`INSERT INTO job (enable, remark, srcPath, dstPath, alistId, useCacheT, scanIntervalT, useCacheS, scanIntervalS,
			 method, interval, isCron, month, day, day_of_week, hour, minute, second, exclude,
			 minFileSize, maxFileSize, dstMap,
			 copyConcurrency, scanConcurrency, maxRetries, compareMode, conflictPolicy, deleteEmptySrcDirs,
			 trashEnable, trashDir, trashRetentionDays, filterMode, include, minFileAgeMinutes, olderThanDays, newerThanDays,
			 windowEnable, windowStart, windowEnd, windowStopOutside, notifyMode, notifyIds)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			job["enable"], job["remark"], job["srcPath"], job["dstPath"], job["alistId"],
			job["useCacheT"], job["scanIntervalT"], job["useCacheS"], job["scanIntervalS"],
			job["method"], job["interval"], job["isCron"],
			job["month"], job["day"], job["day_of_week"],
			job["hour"], job["minute"], job["second"], job["exclude"],
			job["minFileSize"], job["maxFileSize"], job["dstMap"],
			jobExtVal(job, "copyConcurrency", 0),
			jobExtVal(job, "scanConcurrency", 0),
			jobExtVal(job, "maxRetries", -1),
			jobExtVal(job, "compareMode", 0),
			jobExtVal(job, "conflictPolicy", 0),
			jobExtVal(job, "deleteEmptySrcDirs", 0),
			jobExtVal(job, "trashEnable", 0),
			jobExtVal(job, "trashDir", ".opensync-trash"),
			jobExtVal(job, "trashRetentionDays", 30),
			jobExtVal(job, "filterMode", 0),
			job["include"],
			jobExtVal(job, "minFileAgeMinutes", 0),
			jobExtVal(job, "olderThanDays", 0),
			jobExtVal(job, "newerThanDays", 0),
			jobExtVal(job, "windowEnable", 0),
			job["windowStart"],
			job["windowEnd"],
			jobExtVal(job, "windowStopOutside", 0),
			jobExtVal(job, "notifyMode", 0),
			job["notifyIds"],
		)
	} else {
		id, err = ExecuteInsert(
			`INSERT INTO job (enable, remark, srcPath, dstPath, alistId, useCacheT, scanIntervalT, useCacheS, scanIntervalS,
			 method, interval, isCron, month, day, day_of_week, hour, minute, second, exclude,
			 minFileSize, maxFileSize, dstMap)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			job["enable"], job["remark"], job["srcPath"], job["dstPath"], job["alistId"],
			job["useCacheT"], job["scanIntervalT"], job["useCacheS"], job["scanIntervalS"],
			job["method"], job["interval"], job["isCron"],
			job["month"], job["day"], job["day_of_week"],
			job["hour"], job["minute"], job["second"], job["exclude"],
			job["minFileSize"], job["maxFileSize"], job["dstMap"],
		)
	}
	if isUniqueConstraintError(err) {
		// job has UNIQUE(srcPath, dstPath, alistId); surface the duplicate as a
		// user-facing reason instead of an opaque 500.
		return 0, jobExistsError()
	}
	return id, err
}

// UpdateJob updates a job. It overwrites every mutable column, so the caller
// must pass the complete normalized definition: a missing key is written as
// NULL (that is how a user clears an optional field, e.g. exclude or remark).
// The id is validated here because an absent/nil id used to make the statement
// match no row and still report success.
func UpdateJob(job map[string]interface{}) error {
	jobID := util.ToInt64(job["id"])
	if jobID <= 0 {
		return fmt.Errorf("update job: missing or invalid id")
	}
	hasNativeOpts, _ := tableHasColumnDBErr(GetDB(), "job", "bwLimit")
	hasExt := hasNativeOpts
	if !hasExt {
		hasExt, _ = tableHasColumnDBErr(GetDB(), "job", "copyConcurrency")
	}
	var (
		affected int64
		err      error
	)
	if hasNativeOpts {
		affected, err = executeUpdateAffected(
			`UPDATE job SET enable=?, remark=?, srcPath=?, dstPath=?, alistId=?, useCacheT=?, scanIntervalT=?,
			 useCacheS=?, scanIntervalS=?, method=?, interval=?, isCron=?, month=?, day=?,
			 day_of_week=?, hour=?, minute=?, second=?, exclude=?, minFileSize=?, maxFileSize=?, dstMap=?,
			 copyConcurrency=?, scanConcurrency=?, maxRetries=?, compareMode=?, conflictPolicy=?, deleteEmptySrcDirs=?,
			 trashEnable=?, trashDir=?, trashRetentionDays=?, filterMode=?, include=?, minFileAgeMinutes=?, olderThanDays=?, newerThanDays=?,
			 windowEnable=?, windowStart=?, windowEnd=?, windowStopOutside=?, notifyMode=?, notifyIds=?,
			 bwLimit=?, tpsLimit=?, tpsLimitBurst=?, modifyWindow=?, fastList=?, multiThreadStreams=?, createEmptySrcDirs=?, followSymlinks=?, extraFlags=? WHERE id=?`,
			job["enable"], job["remark"], job["srcPath"], job["dstPath"], job["alistId"],
			job["useCacheT"], job["scanIntervalT"], job["useCacheS"], job["scanIntervalS"],
			job["method"], job["interval"], job["isCron"],
			job["month"], job["day"], job["day_of_week"],
			job["hour"], job["minute"], job["second"], job["exclude"],
			job["minFileSize"], job["maxFileSize"], job["dstMap"],
			jobExtVal(job, "copyConcurrency", 0),
			jobExtVal(job, "scanConcurrency", 0),
			jobExtVal(job, "maxRetries", -1),
			jobExtVal(job, "compareMode", 0),
			jobExtVal(job, "conflictPolicy", 0),
			jobExtVal(job, "deleteEmptySrcDirs", 0),
			jobExtVal(job, "trashEnable", 0),
			jobExtVal(job, "trashDir", ".opensync-trash"),
			jobExtVal(job, "trashRetentionDays", 30),
			jobExtVal(job, "filterMode", 0),
			job["include"],
			jobExtVal(job, "minFileAgeMinutes", 0),
			jobExtVal(job, "olderThanDays", 0),
			jobExtVal(job, "newerThanDays", 0),
			jobExtVal(job, "windowEnable", 0),
			job["windowStart"],
			job["windowEnd"],
			jobExtVal(job, "windowStopOutside", 0),
			jobExtVal(job, "notifyMode", 0),
			job["notifyIds"],
			job["bwLimit"],
			jobExtVal(job, "tpsLimit", 0.0),
			jobExtVal(job, "tpsLimitBurst", 0),
			job["modifyWindow"],
			jobExtVal(job, "fastList", 0),
			jobExtVal(job, "multiThreadStreams", 0),
			jobExtVal(job, "createEmptySrcDirs", 0),
			jobExtVal(job, "followSymlinks", 0),
			job["extraFlags"],
			jobID,
		)
	} else if hasExt {
		affected, err = executeUpdateAffected(
			`UPDATE job SET enable=?, remark=?, srcPath=?, dstPath=?, alistId=?, useCacheT=?, scanIntervalT=?,
			 useCacheS=?, scanIntervalS=?, method=?, interval=?, isCron=?, month=?, day=?,
			 day_of_week=?, hour=?, minute=?, second=?, exclude=?, minFileSize=?, maxFileSize=?, dstMap=?,
			 copyConcurrency=?, scanConcurrency=?, maxRetries=?, compareMode=?, conflictPolicy=?, deleteEmptySrcDirs=?,
			 trashEnable=?, trashDir=?, trashRetentionDays=?, filterMode=?, include=?, minFileAgeMinutes=?, olderThanDays=?, newerThanDays=?,
			 windowEnable=?, windowStart=?, windowEnd=?, windowStopOutside=?, notifyMode=?, notifyIds=? WHERE id=?`,
			job["enable"], job["remark"], job["srcPath"], job["dstPath"], job["alistId"],
			job["useCacheT"], job["scanIntervalT"], job["useCacheS"], job["scanIntervalS"],
			job["method"], job["interval"], job["isCron"],
			job["month"], job["day"], job["day_of_week"],
			job["hour"], job["minute"], job["second"], job["exclude"],
			job["minFileSize"], job["maxFileSize"], job["dstMap"],
			jobExtVal(job, "copyConcurrency", 0),
			jobExtVal(job, "scanConcurrency", 0),
			jobExtVal(job, "maxRetries", -1),
			jobExtVal(job, "compareMode", 0),
			jobExtVal(job, "conflictPolicy", 0),
			jobExtVal(job, "deleteEmptySrcDirs", 0),
			jobExtVal(job, "trashEnable", 0),
			jobExtVal(job, "trashDir", ".opensync-trash"),
			jobExtVal(job, "trashRetentionDays", 30),
			jobExtVal(job, "filterMode", 0),
			job["include"],
			jobExtVal(job, "minFileAgeMinutes", 0),
			jobExtVal(job, "olderThanDays", 0),
			jobExtVal(job, "newerThanDays", 0),
			jobExtVal(job, "windowEnable", 0),
			job["windowStart"],
			job["windowEnd"],
			jobExtVal(job, "windowStopOutside", 0),
			jobExtVal(job, "notifyMode", 0),
			job["notifyIds"],
			jobID,
		)
	} else {
		affected, err = executeUpdateAffected(
			`UPDATE job SET enable=?, remark=?, srcPath=?, dstPath=?, alistId=?, useCacheT=?, scanIntervalT=?,
			 useCacheS=?, scanIntervalS=?, method=?, interval=?, isCron=?, month=?, day=?,
			 day_of_week=?, hour=?, minute=?, second=?, exclude=?, minFileSize=?, maxFileSize=?, dstMap=? WHERE id=?`,
			job["enable"], job["remark"], job["srcPath"], job["dstPath"], job["alistId"],
			job["useCacheT"], job["scanIntervalT"], job["useCacheS"], job["scanIntervalS"],
			job["method"], job["interval"], job["isCron"],
			job["month"], job["day"], job["day_of_week"],
			job["hour"], job["minute"], job["second"], job["exclude"],
			job["minFileSize"], job["maxFileSize"], job["dstMap"],
			jobID,
		)
	}
	if isUniqueConstraintError(err) {
		return jobExistsError()
	}
	if err != nil {
		return err
	}
	if affected == 0 {
		return notFound(ErrJobNotFound, msg.JobNotFound)
	}
	return nil
}

// clearJobTaskCounts drops the cached item counters for the tasks matched by
// where. Deletion removes items in separately committed batches before the
// parent rows, so a failure can leave the task row behind with part of its
// history gone; keeping the cached counters would then show counts for items
// that no longer exist, with no path that recomputes them.
func clearJobTaskCounts(where string, args ...interface{}) error {
	return ExecuteUpdate("UPDATE job_task SET taskNum=NULL WHERE "+where, args...)
}

// UpdateJobEnable updates job enable status
func UpdateJobEnable(jobID int64, enable int) error {
	affected, err := executeUpdateAffected("UPDATE job SET enable=? WHERE id=?", enable, jobID)
	if err != nil {
		return err
	}
	if affected == 0 {
		return notFound(ErrJobNotFound, msg.JobNotFound)
	}
	return nil
}

// DeleteJob deletes a job and its tasks.
//
// The items go first, in short separately committed batches: one statement
// for every item of a long-lived job (plus the FTS trigger per row) held the
// write lock long enough for other jobs' writes to fail with "database is
// locked". An interruption leaves the job and its tasks in place with fewer
// items, so deleting again finishes the job.
func DeleteJob(jobID int64) error {
	// Invalidate cached counters first (P2-1): the batches below commit
	// separately, so a later failure leaves the tasks with a shorter history.
	if err := clearJobTaskCounts("jobId=?", jobID); err != nil {
		return err
	}
	if err := deleteJobTaskItemsInBatches(context.Background(),
		"taskId IN (SELECT id FROM job_task WHERE jobId=?)", jobID); err != nil {
		return err
	}
	return withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM job_task_item WHERE taskId IN (SELECT id FROM job_task WHERE jobId=?)", jobID); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM job_task WHERE jobId=?", jobID); err != nil {
			return err
		}
		for _, table := range []string{"sync_file_state", "sync_state", "bisync_entry", "bisync_root_meta", "bisync_conflict", "oneway_baseline"} {
			if exists, err := txTableExists(tx, table); err != nil {
				return err
			} else if exists {
				if _, err := tx.Exec("DELETE FROM "+table+" WHERE jobId=?", jobID); err != nil {
					return err
				}
			}
		}
		if hasApprovalTable, err := txTableExists(tx, "pending_delete_approval"); err == nil && hasApprovalTable {
			if _, err := tx.Exec("DELETE FROM pending_delete_approval WHERE jobId=?", jobID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec("DELETE FROM job WHERE id=?", jobID); err != nil {
			return err
		}
		return nil
	})
}

// --- Job Task ---

// GetJobTaskList gets paginated task list for a job
func GetJobTaskList(params map[string]interface{}) (map[string]interface{}, error) {
	jobID := params["id"]
	where := newWhereBuilder("WHERE jobId=?", jobID)

	if status, ok := params["status"]; ok {
		where.and("status=?", util.ToInt(status))
	} else if statuses := parseStatusList(params["statusIn"]); len(statuses) > 0 {
		clause, statusArgs := statusInClause(statuses)
		where.and(fmt.Sprintf("status IN (%s)", clause), statusArgs...)
	}
	if startTime, ok := params["startTime"]; ok {
		start := util.ToInt(startTime)
		if start > 0 {
			where.and("COALESCE(NULLIF(runTime, 0), createTime) >= ?", start)
		}
	}
	if endTime, ok := params["endTime"]; ok {
		end := util.ToInt(endTime)
		if end > 0 {
			where.and("COALESCE(NULLIF(runTime, 0), createTime) <= ?", end)
		}
	}
	if endTime, ok := params["endTimeExclusive"]; ok {
		end := util.ToInt(endTime)
		if end > 0 {
			where.and("COALESCE(NULLIF(runTime, 0), createTime) < ?", end)
		}
	}
	if keyword, ok := params["keyword"]; ok {
		kw := strings.TrimSpace(fmt.Sprintf("%v", keyword))
		if kw != "" {
			where.and("CAST(id AS TEXT) LIKE ? ESCAPE '\\'", "%"+escapeLike(kw)+"%")
		}
	}

	baseSQL := fmt.Sprintf("SELECT * FROM job_task %s ORDER BY createTime DESC, id DESC", where.clause)
	return FetchAllToPage(baseSQL, params, where.args...)
}

type whereBuilder struct {
	clause string
	args   []interface{}
}

func newWhereBuilder(clause string, args ...interface{}) whereBuilder {
	return whereBuilder{clause: clause, args: args}
}

func (b *whereBuilder) and(condition string, args ...interface{}) {
	b.clause += " AND " + condition
	b.args = append(b.args, args...)
}

func parseStatusList(value interface{}) []int {
	statuses := parseStatusListRaw(value)
	if statuses == nil {
		return nil
	}
	return uniqueKnownStatuses(statuses)
}

// knownStatuses are the status codes stored in job_task / job_task_item.
var knownStatuses = map[int]struct{}{0: {}, 1: {}, 2: {}, 3: {}, 4: {}, 5: {}, 6: {}, 7: {}, 8: {}, 10: {}}

// uniqueKnownStatuses drops unknown codes and duplicates while keeping order,
// so a client-supplied list never grows the IN (...) clause beyond the handful
// of real states (an unbounded list could exceed SQLite's variable limit).
func uniqueKnownStatuses(statuses []int) []int {
	seen := make(map[int]struct{}, len(knownStatuses))
	out := make([]int, 0, len(statuses))
	for _, s := range statuses {
		if _, ok := knownStatuses[s]; !ok {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func parseStatusListRaw(value interface{}) []int {
	switch v := value.(type) {
	case nil:
		return nil
	case []int:
		return v
	case []string:
		// Each entry may itself be comma-separated: Gin delivers
		// "?statusIn=2,7" as []string{"2,7"}, and ToInt("2,7") is 0 — which
		// silently degraded the filter to "status IN (0)", i.e. waiting-only.
		statuses := make([]int, 0, len(v))
		for _, item := range v {
			statuses = append(statuses, parseStatusCSV(item)...)
		}
		return statuses
	case []interface{}:
		// A JSON-decoded array. Each element goes through the same strict
		// parser as the string form: util.ToInt("abc") is 0, which would
		// silently turn a malformed filter into "waiting only".
		statuses := make([]int, 0, len(v))
		for _, item := range v {
			statuses = append(statuses, parseStatusCSV(fmt.Sprint(item))...)
		}
		return statuses
	case string:
		return parseStatusCSV(v)
	default:
		// Unknown scalar type (e.g. float64 from JSON): format and parse it
		// strictly; "true"/"1.5" are dropped instead of becoming status 0.
		return parseStatusCSV(fmt.Sprint(v))
	}
}

// parseStatusCSV splits a comma-separated status list, skipping blanks and
// values that are not plain integers. A malformed entry is dropped rather than
// silently read as status 0 (waiting).
func parseStatusCSV(value string) []int {
	parts := strings.Split(value, ",")
	statuses := make([]int, 0, len(parts))
	for _, item := range parts {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		n, err := strconv.Atoi(item)
		if err != nil {
			continue
		}
		statuses = append(statuses, n)
	}
	return statuses
}

// GetJobTaskByID gets task by ID
func GetJobTaskByID(taskID int64) (map[string]interface{}, error) {
	rst, err := FetchAllToTable("SELECT * FROM job_task WHERE id=?", taskID)
	if err != nil {
		return nil, err
	}
	if len(rst) == 0 {
		return nil, notFound(ErrTaskNotFound, msg.TaskNotFound)
	}
	return rst[0], nil
}

// AddJobTask inserts a new job task
func AddJobTask(jobID int64, runTime int64) (int64, error) {
	return ExecuteInsert("INSERT INTO job_task (jobId, runTime) VALUES (?, ?)", jobID, runTime)
}

// ListRunningJobTasks returns task/job pairs currently marked waiting or running
// (used on startup to optionally resume tasks interrupted by an unclean shutdown).
func ListRunningJobTasks() ([]map[string]interface{}, error) {
	return FetchAllToTable("SELECT id, jobId FROM job_task WHERE status IN (0, 1) ORDER BY id ASC")
}

// UpdateJobTaskStatusByStatus updates incomplete tasks to aborted (for restart)
func UpdateJobTaskStatusByStatus() error {
	return ExecuteUpdate("UPDATE job_task SET status=4 WHERE status IN (0, 1)")
}

// UpdateJobTaskStatusByStatusAndJobID updates incomplete tasks to aborted for a job
func UpdateJobTaskStatusByStatusAndJobID(jobID int64) error {
	return ExecuteUpdate("UPDATE job_task SET status=4 WHERE status IN (0, 1) AND jobId=?", jobID)
}

// DeleteJobTaskByTaskID deletes a task and its items. The items are removed in
// batches first for the same reason as DeleteJob.
func DeleteJobTaskByTaskID(taskID int64) error {
	if err := clearJobTaskCounts("id=?", taskID); err != nil {
		return err
	}
	if err := deleteJobTaskItemsInBatches(context.Background(), "taskId=?", taskID); err != nil {
		return err
	}
	return withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM job_task_item WHERE taskId=?", taskID); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM job_task WHERE id=?", taskID); err != nil {
			return err
		}
		return nil
	})
}

// DeleteJobTasksByIDs deletes a batch of finished tasks and their items.
func DeleteJobTasksByIDs(taskIDs []int64) error {
	if len(taskIDs) == 0 {
		return nil
	}
	for start := 0; start < len(taskIDs); start += expiredTaskDeleteBatchSize {
		end := start + expiredTaskDeleteBatchSize
		if end > len(taskIDs) {
			end = len(taskIDs)
		}
		if err := deleteJobTasksByIDs(context.Background(), taskIDs[start:end]); err != nil {
			return err
		}
	}
	return nil
}

// GetFinishedTaskIDsByJobID returns all finished (non-running) task IDs for a job.
func GetFinishedTaskIDsByJobID(jobID int64) ([]int64, error) {
	rows, err := GetDB().Query(
		`SELECT id FROM job_task WHERE jobId=? AND status NOT IN (0, 1) ORDER BY id ASC`,
		jobID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

// GetJobTasksByIDs returns task rows matching the supplied IDs.
func GetJobTasksByIDs(taskIDs []int64) ([]map[string]interface{}, error) {
	if len(taskIDs) == 0 {
		return nil, nil
	}
	var out []map[string]interface{}
	for start := 0; start < len(taskIDs); start += 400 {
		end := start + 400
		if end > len(taskIDs) {
			end = len(taskIDs)
		}
		clause, args := int64InClause(taskIDs[start:end])
		rows, err := FetchAllToTable(fmt.Sprintf("SELECT * FROM job_task WHERE id IN (%s)", clause), args...)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	return out, nil
}

// GetLatestTaskPerJob returns the most recent job_task record for every jobId.
func GetLatestTaskPerJob(jobIDs ...int64) (map[int64]map[string]interface{}, error) {
	query := `
		SELECT t.id, t.jobId, t.status, t.errMsg, t.runTime, t.taskNum, t.createTime
		FROM job AS j JOIN job_task AS t ON t.id = (
			SELECT id FROM job_task WHERE jobId = j.id
			ORDER BY createTime DESC, id DESC LIMIT 1
		)`
	var args []interface{}
	if len(jobIDs) > 0 {
		var clause string
		clause, args = int64InClause(jobIDs)
		query += " WHERE j.id IN (" + clause + ")"
	}
	rows, err := FetchAllToTable(query, args...)
	if err != nil {
		return nil, err
	}
	result := make(map[int64]map[string]interface{}, len(rows))
	for _, row := range rows {
		jobID := util.ToInt64(row["jobId"])
		if jobID > 0 {
			result[jobID] = row
		}
	}
	return result, nil
}

// GetRecentTasks returns tasks whose effective time is >= sinceUnix.
func GetRecentTasks(sinceUnix int64) ([]map[string]interface{}, error) {
	return fetchAllToTableWithLimit(
		GetDB(),
		0,
		`SELECT id, jobId, status, runTime, taskNum, createTime
		 FROM job_task
		 WHERE COALESCE(NULLIF(runTime, 0), createTime) >= ?`,
		sinceUnix,
	)
}

// ForEachRecentTask streams status and taskNum for every task whose effective
// time is >= sinceUnix, without materializing all rows or tripping maxFetchAllRows.
func ForEachRecentTask(sinceUnix int64, fn func(status int, taskNum string) error) error {
	rows, err := GetDB().Query(
		`SELECT COALESCE(status, 0), COALESCE(taskNum, '')
		 FROM job_task
		 WHERE COALESCE(NULLIF(runTime, 0), createTime) >= ?`,
		sinceUnix,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var status int
		var taskNum string
		if err := rows.Scan(&status, &taskNum); err != nil {
			return err
		}
		if err := fn(status, taskNum); err != nil {
			return err
		}
	}
	return rows.Err()
}

// TaskHistoryCleanupGuard coordinates retention cleanup with running sync/retry
// tasks so an expired task currently being replayed is never deleted mid-retry.
type TaskHistoryCleanupGuard struct {
	Blocked          func() bool
	ProtectedTaskIDs func() []int64
	BeginDeleteBatch func(taskIDs []int64) []int64
	EndDeleteBatch   func(taskIDs []int64)
}

// DeleteJobTaskByRunTimeContext deletes finished tasks older than runTime, then
// reclaims space. ctx is checked between every short delete batch, so shutdown
// can stop a cleanup of years of history without waiting for it to finish.
//
// maintenanceBlocked reports whether a sync is running. Both expired-task
// deletion and database maintenance (WAL truncate, VACUUM) are deferred while
// it returns true so an in-progress retry never loses its source task mid-replay
// and running item writes are never starved by a write lock.
// A nil maintenanceBlocked never blocks.
func DeleteJobTaskByRunTimeContext(ctx context.Context, runTime int64, maintenanceBlocked func() bool) error {
	return DeleteJobTaskByRunTimeWithGuardContext(ctx, runTime, TaskHistoryCleanupGuard{
		Blocked: maintenanceBlocked,
	})
}

func DeleteJobTaskByRunTimeWithGuardContext(ctx context.Context, runTime int64, guard TaskHistoryCleanupGuard) error {
	maintenanceBlocked := guard.Blocked
	if maintenanceBlocked == nil {
		maintenanceBlocked = func() bool { return false }
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if maintenanceBlocked() {
			log.Printf("Deferring task history cleanup: a sync task is running")
			return nil
		}
		var excludeIDs []int64
		if guard.ProtectedTaskIDs != nil {
			excludeIDs = guard.ProtectedTaskIDs()
		}
		taskIDs, err := expiredJobTaskIDsExcluding(runTime, expiredTaskDeleteBatchSize, excludeIDs)
		if err != nil {
			return err
		}
		if len(taskIDs) == 0 {
			break
		}
		if guard.BeginDeleteBatch != nil {
			taskIDs = guard.BeginDeleteBatch(taskIDs)
			if len(taskIDs) == 0 {
				return nil
			}
		}
		deleteErr := deleteJobTasksByIDs(ctx, taskIDs)
		if guard.EndDeleteBatch != nil {
			guard.EndDeleteBatch(taskIDs)
		}
		if deleteErr != nil {
			return deleteErr
		}
	}
	if maintenanceBlocked() {
		log.Printf("Skipping database maintenance after task history cleanup: a sync task is running")
		return nil
	}
	// incremental_vacuum only does anything when the database was created
	// with auto_vacuum=INCREMENTAL, which this schema never set — so deleting
	// years of history returned no pages to the filesystem. TRUNCATE actually
	// shrinks the WAL, and the freed pages are reclaimed by an explicit VACUUM
	// below.
	if err := walCheckpointTruncate(ctx); err != nil {
		// Not fatal (the cleanup itself succeeded), but no longer silent: a
		// WAL that cannot be truncated is how a fixed-size disk fills up.
		if ctx.Err() == nil {
			log.Printf("WAL checkpoint after task history cleanup failed: %v", err)
		}
	}
	if _, err := GetDB().ExecContext(ctx, "PRAGMA optimize"); err != nil && ctx.Err() == nil {
		log.Printf("PRAGMA optimize after task history cleanup failed: %v", err)
	}
	if maintenanceBlocked() {
		// A sync task started after the check above (e.g. while the checkpoint
		// ran); do not take the write lock for a full VACUUM now.
		log.Printf("Skipping database VACUUM: a sync task just started")
		return nil
	}
	if err := vacuumIfWorthwhile(ctx, maintenanceBlocked); err != nil {
		log.Printf("Failed to reclaim free database pages: %v", err)
	}
	if ctx.Err() == nil && !maintenanceBlocked() {
		if err := walCheckpointTruncate(ctx); err != nil && ctx.Err() == nil {
			log.Printf("WAL checkpoint after database maintenance failed: %v", err)
		}
	}
	return nil
}

// walCheckpointTruncate runs PRAGMA wal_checkpoint(TRUNCATE) and reports
// whether it completed. SQLite answers with a row even when it only made
// partial progress because a reader still pins the WAL (busy=1), so the old
// "discard the result" call could not tell a shrunken WAL from a failed one.
func walCheckpointTruncate(ctx context.Context) error {
	var busy, logFrames, checkpointed int64
	if err := GetDB().QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return fmt.Errorf("checkpoint busy: %d of %d WAL frames not checkpointed", logFrames-checkpointed, logFrames)
	}
	return nil
}

// freePageVacuumThreshold is the number of free pages that justifies a VACUUM.
// VACUUM rewrites the whole database and needs room for a copy, so it is only
// worth doing once a meaningful amount of space is actually reclaimable.
const freePageVacuumThreshold = 4096

// freePageVacuumMinPercent is the share of the file that has to be free as
// well. The absolute threshold alone (16 MiB at 4 KiB pages) is reached by
// ordinary churn on a multi-GB database, and each such VACUUM rewrote every
// page of it to give back a rounding error.
const freePageVacuumMinPercent = 20

// vacuumMaintenancePollInterval is how often a running VACUUM checks whether a
// sync task has started and it should give way.
const vacuumMaintenancePollInterval = 500 * time.Millisecond

// shouldVacuum decides whether reclaiming freePages out of pageCount is worth
// a full rewrite: enough pages in absolute terms and as a share of the file.
func shouldVacuum(freePages, pageCount, minFreePages int64) bool {
	if freePages <= 0 || pageCount <= 0 || freePages < minFreePages {
		return false
	}
	return freePages*100 >= pageCount*freePageVacuumMinPercent
}

// vacuumIfWorthwhile reclaims disk space when shouldVacuum says so. VACUUM
// cannot run inside a transaction and is skipped (not an error) when the
// database is busy.
//
// It runs on a dedicated connection switched to temp_store=FILE. The pool opens
// every connection with temp_store=MEMORY, and VACUUM builds its full copy of
// the database in temp storage: on a NAS with a multi-GB history that copy was
// built in RAM and the process was OOM-killed mid-rewrite.
func vacuumIfWorthwhile(ctx context.Context, maintenanceBlocked func() bool) error {
	if maintenanceBlocked != nil && maintenanceBlocked() {
		log.Printf("Skipping database VACUUM: a sync task is running")
		return nil
	}
	conn, err := GetDB().Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	var freePages, pageCount int64
	if err := conn.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&freePages); err != nil {
		return err
	}
	if err := conn.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pageCount); err != nil {
		return err
	}
	if !shouldVacuum(freePages, pageCount, freePageVacuumThreshold) {
		return nil
	}
	return vacuumOnConn(ctx, conn, freePages, pageCount, maintenanceBlocked)
}

// vacuumOnConn runs VACUUM with file-backed temp storage on conn and restores
// the pool's temp_store afterwards, so the connection goes back to the pool
// configured like its siblings.
func vacuumOnConn(ctx context.Context, conn *sql.Conn, freePages, pageCount int64, maintenanceBlocked func() bool) error {
	if _, err := conn.ExecContext(ctx, "PRAGMA temp_store=FILE"); err != nil {
		return err
	}
	defer func() {
		// Background, not ctx: an interrupted VACUUM cancelled ctx, and the
		// connection must still leave with the pool's setting restored.
		if _, err := conn.ExecContext(context.Background(), "PRAGMA temp_store=MEMORY"); err != nil {
			log.Printf("Failed to restore sqlite temp_store after VACUUM: %v", err)
		}
	}()

	vacuumCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopWatch := watchMaintenanceBlocked(vacuumCtx, cancel, maintenanceBlocked, vacuumMaintenancePollInterval)
	defer stopWatch()

	log.Printf("Reclaiming %d of %d database pages", freePages, pageCount)
	_, err := conn.ExecContext(vacuumCtx, "VACUUM")
	return err
}

// watchMaintenanceBlocked cancels ctx (interrupting the VACUUM running under
// it) as soon as blocked reports a sync task. VACUUM holds the write lock for
// its whole run, so a task that starts meanwhile would otherwise see its item
// writes time out and abort. The returned function stops the watcher and must
// be called exactly once.
func watchMaintenanceBlocked(ctx context.Context, cancel context.CancelFunc, blocked func() bool, interval time.Duration) func() {
	if blocked == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if blocked() {
					log.Printf("Interrupting VACUUM: a sync task started")
					cancel()
					return
				}
			}
		}
	}()
	return func() { close(done) }
}

func expiredJobTaskIDs(cutoff int64, limit int) ([]int64, error) {
	return expiredJobTaskIDsExcluding(cutoff, limit, nil)
}

func expiredJobTaskIDsExcluding(cutoff int64, limit int, excludeIDs []int64) ([]int64, error) {
	if limit <= 0 {
		limit = expiredTaskDeleteBatchSize
	}
	args := []interface{}{cutoff}
	excludeSQL := ""
	if len(excludeIDs) > 0 {
		clause, exArgs := int64InClause(excludeIDs)
		excludeSQL = " AND id NOT IN (" + clause + ")"
		args = append(args, exArgs...)
	}
	args = append(args, limit)
	rows, err := GetDB().Query(
		`SELECT id FROM job_task
		 WHERE COALESCE(NULLIF(runTime, 0), createTime) < ?
		   AND status NOT IN (0, 1)`+excludeSQL+`
		 ORDER BY COALESCE(NULLIF(runTime, 0), createTime), id
		 LIMIT ?`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var taskIDs []int64
	for rows.Next() {
		var taskID int64
		if err := rows.Scan(&taskID); err != nil {
			return nil, err
		}
		taskIDs = append(taskIDs, taskID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return taskIDs, nil
}

// jobTaskItemDeleteBatchSize bounds the rows removed per write transaction.
// Each deleted item also fires the FTS delete trigger, so one statement for
// every item of 500 tasks held the write lock long enough for a running sync's
// item writes to exceed busy_timeout and abort the task.
var jobTaskItemDeleteBatchSize = 2000

// deleteJobTasksByIDs removes the tasks' items in short, separately committed
// batches, then the task rows themselves. An interruption leaves the task rows
// in place with fewer items; they are still expired, so the next cleanup picks
// them up again.
func deleteJobTasksByIDs(ctx context.Context, taskIDs []int64) error {
	if len(taskIDs) == 0 {
		return nil
	}
	clause, args := int64InClause(taskIDs)
	if err := clearJobTaskCounts("id IN ("+clause+")", args...); err != nil {
		return err
	}
	if err := deleteJobTaskItemsInBatches(ctx, "taskId IN ("+clause+")", args...); err != nil {
		return err
	}
	return retryOnBusyContext(ctx, func() error {
		_, err := GetDB().ExecContext(ctx, "DELETE FROM job_task WHERE id IN ("+clause+")", args...)
		return err
	})
}

// deleteJobTaskItemsInBatches deletes the job_task_item rows matching where,
// jobTaskItemDeleteBatchSize at a time, each batch its own short write so other
// writers get the lock in between.
func deleteJobTaskItemsInBatches(ctx context.Context, where string, args ...interface{}) error {
	query := "DELETE FROM job_task_item WHERE id IN (SELECT id FROM job_task_item WHERE " + where + " LIMIT ?)"
	batchArgs := appendSQLArgs(args, jobTaskItemDeleteBatchSize)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var n int64
		err := retryOnBusyContext(ctx, func() error {
			result, err := GetDB().ExecContext(ctx, query, batchArgs...)
			if err != nil {
				return err
			}
			n, err = result.RowsAffected()
			return err
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
	}
}

// withTx runs fn in a write transaction (BEGIN IMMEDIATE, see sqliteDSN) and
// repeats the whole transaction when it fails on lock contention. fn must
// therefore only touch state through tx, or overwrite its outputs each run.
func withTx(fn func(*sql.Tx) error) error {
	return retryOnBusy(func() error { return runTxOn(GetDB(), nil, fn) })
}

// withReadTx runs fn in a read-only transaction: one consistent snapshot that
// begins deferred and so never queues for the write lock.
func withReadTx(fn func(*sql.Tx) error) error {
	return runTxOn(GetDB(), &sql.TxOptions{ReadOnly: true}, fn)
}

func runTx(opts *sql.TxOptions, fn func(*sql.Tx) error) error {
	return runTxOn(GetDB(), opts, fn)
}

// runTxOn is runTx against an explicit handle, for maintenance work that was
// handed its database (e.g. the startup FTS repair) instead of using the global.
func runTxOn(handle *sql.DB, opts *sql.TxOptions, fn func(*sql.Tx) error) error {
	tx, err := handle.BeginTx(context.Background(), opts)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// UpdateJobTaskNumMany batch updates task result counts
func UpdateJobTaskNumMany(taskNums []map[string]interface{}) error {
	if len(taskNums) == 0 {
		return nil
	}
	argsList := make([][]interface{}, 0, len(taskNums))
	for _, tn := range taskNums {
		argsList = append(argsList, []interface{}{tn["taskNum"], tn["taskId"]})
	}
	return ExecuteMany("UPDATE job_task SET taskNum=? WHERE id=?", argsList)
}

// UpdateJobTaskStatusAndNum updates final status and cached counts in one write.
func UpdateJobTaskStatusAndNum(taskID int64, status int, errMsg *string, taskNum string) error {
	return ExecuteUpdate("UPDATE job_task SET status=?, errMsg=?, taskNum=?, runTime=strftime('%s','now') WHERE id=?", status, errMsg, taskNum, taskID)
}

// UpdateJobTaskStatusClearNum records a final status without counters. taskNum
// is left NULL so the task list recomputes it on the next read instead of
// caching counters that could not be read.
func UpdateJobTaskStatusClearNum(taskID int64, status int, errMsg *string) error {
	return ExecuteUpdate("UPDATE job_task SET status=?, errMsg=?, taskNum=NULL, runTime=strftime('%s','now') WHERE id=?", status, errMsg, taskID)
}

// --- Job Task Item ---

// AddJobTaskItemMany batch inserts task items
func AddJobTaskItemMany(items []map[string]interface{}) error {
	if len(items) == 0 {
		return nil
	}
	argsList := make([][]interface{}, 0, len(items))
	for _, item := range items {
		createTime := item["createTime"]
		if createTime == nil {
			createTime = time.Now().Unix()
		}
		argsList = append(argsList, []interface{}{
			item["taskId"], item["srcPath"], item["dstPath"], item["isPath"], item["fileName"],
			item["fileSize"], item["type"], item["alistTaskId"], item["status"], item["errMsg"],
			// progress is optional: live progress is streamed over SSE and the
			// finalized items have no meaningful value, so absent stays NULL.
			// It is persisted when a caller does supply it (see P3-10).
			item["progress"],
			createTime,
		})
	}
	return ExecuteMany(
		`INSERT INTO job_task_item (taskId, srcPath, dstPath, isPath, fileName, fileSize, type, alistTaskId, status, errMsg, progress, createTime)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		argsList,
	)
}

func buildJobTaskItemWhere(params map[string]interface{}) whereBuilder {
	taskID := params["taskId"]
	where := newWhereBuilder("WHERE taskId=?", taskID)

	if status, ok := params["status"]; ok {
		if util.ToInt(status) == -1 {
			where.and("status NOT IN (0,1,2,7)")
		} else {
			where.and("status=?", status)
		}
	}
	if typ, ok := params["type"]; ok {
		where.and("type=?", typ)
	}
	if isPath, ok := params["isPath"]; ok {
		where.and("isPath=?", isPath)
	}
	if hasError, ok := params["hasError"]; ok {
		if util.ToInt(hasError) == 1 {
			where.and("errMsg IS NOT NULL AND errMsg<>''")
		} else {
			where.and("(errMsg IS NULL OR errMsg='')")
		}
	}
	if keyword, ok := params["keyword"]; ok {
		kw := strings.TrimSpace(fmt.Sprintf("%v", keyword))
		if kw != "" {
			filterSQL, filterArgs := taskItemKeywordFilter(kw)
			where.clause += filterSQL
			where.args = append(where.args, filterArgs...)
		}
	}
	return where
}

// GetJobTaskItemList gets paginated task item list
func GetJobTaskItemList(params map[string]interface{}) (map[string]interface{}, error) {
	where := buildJobTaskItemWhere(params)

	baseSQL := fmt.Sprintf("SELECT %s FROM job_task_item %s ORDER BY createTime DESC, id DESC", jobTaskItemListColumns, where.clause)
	// A separate COUNT(*) instead of FetchAllToPage's window count: the window
	// forces SQLite to materialize and sort every matching item on each page,
	// which for a million-item task is a full scan per click.
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM job_task_item %s", where.clause)
	return FetchAllToPageWithCount(baseSQL, countSQL, params, where.args...)
}

const recentJobTaskItemSelectColumns = `i.id, i.taskId, jt.jobId, COALESCE(NULLIF(TRIM(j.remark), ''), '同步任务 #' || j.id) AS jobName, j.method AS jobMethod, i.srcPath, i.dstPath, i.isPath, i.fileName, i.fileSize, i.type, i.alistTaskId, i.status, i.progress, i.errMsg, i.createTime`

const recentJobTaskItemJoinFrom = `FROM job_task_item AS i INNER JOIN job_task AS jt ON jt.id = i.taskId INNER JOIN job AS j ON j.id = jt.jobId`

// EmptyRecentTaskItemStats returns zeroed summary counters for recent task items.
func EmptyRecentTaskItemStats() map[string]interface{} {
	return map[string]interface{}{
		"allNum":     int64(0),
		"waitNum":    int64(0),
		"runningNum": int64(0),
		"successNum": int64(0),
		"failNum":    int64(0),
		"otherNum":   int64(0),
		"deleteNum":  int64(0),
		"errorNum":   int64(0),
		"sumSize":    int64(0),
	}
}

func buildRecentJobTaskItemWhere(params map[string]interface{}) (whereBuilder, bool) {
	where := newWhereBuilder("WHERE 1=1")

	if jobID, ok := params["jobId"]; ok {
		id := util.ToInt64(jobID)
		if id <= 0 {
			return where, true
		}
		where.and("jt.jobId=?", id)
	} else if rawIDs, hasIDs := params["jobIds"]; hasIDs {
		jobIDs, _ := rawIDs.([]int64)
		if len(jobIDs) == 0 {
			return where, true
		}
		clause, idArgs := int64InClause(jobIDs)
		where.and(fmt.Sprintf("jt.jobId IN (%s)", clause), idArgs...)
	}

	if status, ok := params["status"]; ok {
		if util.ToInt(status) == -1 {
			where.and("i.status NOT IN (0,1,2,7)")
		} else {
			where.and("i.status=?", util.ToInt(status))
		}
	}
	if typ, ok := params["type"]; ok {
		where.and("i.type=?", util.ToInt(typ))
	}
	if isPath, ok := params["isPath"]; ok {
		where.and("i.isPath=?", util.ToInt(isPath))
	}
	if hasError, ok := params["hasError"]; ok {
		if util.ToInt(hasError) == 1 {
			where.and("i.errMsg IS NOT NULL AND i.errMsg<>''")
		} else {
			where.and("(i.errMsg IS NULL OR i.errMsg='')")
		}
	}
	if keyword, ok := params["keyword"]; ok {
		kw := strings.TrimSpace(fmt.Sprintf("%v", keyword))
		if kw != "" {
			like := "%" + escapeLike(kw) + "%"
			where.and(
				"(i.fileName LIKE ? ESCAPE '\\' OR i.srcPath LIKE ? ESCAPE '\\' OR i.dstPath LIKE ? ESCAPE '\\' OR i.alistTaskId LIKE ? ESCAPE '\\' OR i.errMsg LIKE ? ESCAPE '\\' OR j.remark LIKE ? ESCAPE '\\')",
				like, like, like, like, like, like,
			)
		}
	}
	return where, false
}

// GetRecentJobTaskItemList gets paginated cross-task recent task items enriched with
// jobName/srcPath/dstPath and matching summary statistics.
func GetRecentJobTaskItemList(params map[string]interface{}) (map[string]interface{}, error) {
	pageParams := make(map[string]interface{}, 2)
	if params != nil {
		if ps, ok := params["pageSize"]; ok {
			pageParams["pageSize"] = ps
		}
		if pn, ok := params["pageNum"]; ok {
			pageParams["pageNum"] = pn
		}
	}
	if _, hasPS := pageParams["pageSize"]; !hasPS {
		pageParams["pageSize"] = 20
	}
	if _, hasPN := pageParams["pageNum"]; !hasPN {
		pageParams["pageNum"] = 1
	}
	ps, pn, _, err := parsePageParams(pageParams)
	if err != nil {
		return nil, err
	}
	offset, err := pageOffset(ps, pn)
	if err != nil {
		return nil, err
	}

	where, forceEmpty := buildRecentJobTaskItemWhere(params)
	if forceEmpty {
		return map[string]interface{}{
			"dataList": make([]map[string]interface{}, 0),
			"count":    int64(0),
			"pageNum":  pn,
			"pageSize": ps,
			"stats":    EmptyRecentTaskItemStats(),
		}, nil
	}

	statsSQL := fmt.Sprintf(
		`SELECT
			COUNT(i.id) AS allNum,
			COALESCE(SUM(CASE WHEN i.status=0 THEN 1 ELSE 0 END), 0) AS waitNum,
			COALESCE(SUM(CASE WHEN i.status=1 THEN 1 ELSE 0 END), 0) AS runningNum,
			COALESCE(SUM(CASE WHEN i.status=2 THEN 1 ELSE 0 END), 0) AS successNum,
			COALESCE(SUM(CASE WHEN i.status=7 THEN 1 ELSE 0 END), 0) AS failNum,
			COALESCE(SUM(CASE WHEN i.status NOT IN (0,1,2,7) THEN 1 ELSE 0 END), 0) AS otherNum,
			COALESCE(SUM(CASE WHEN i.type=1 THEN 1 ELSE 0 END), 0) AS deleteNum,
			COALESCE(SUM(CASE WHEN i.errMsg IS NOT NULL AND i.errMsg<>'' THEN 1 ELSE 0 END), 0) AS errorNum,
			COALESCE(SUM(CASE WHEN i.status=2 AND i.type<>1 AND i.fileSize IS NOT NULL THEN i.fileSize ELSE 0 END), 0) AS sumSize
		 %s %s`,
		recentJobTaskItemJoinFrom,
		where.clause,
	)
	dataSQL := fmt.Sprintf(
		`SELECT %s %s %s ORDER BY i.createTime DESC, i.id DESC LIMIT ? OFFSET ?`,
		recentJobTaskItemSelectColumns,
		recentJobTaskItemJoinFrom,
		where.clause,
	)

	var result map[string]interface{}
	err = withReadTx(func(tx *sql.Tx) error {
		var counts jobTaskCounts
		var deleteNum, errorNum int64
		if err := tx.QueryRow(statsSQL, where.args...).Scan(
			&counts.allNum,
			&counts.waitNum,
			&counts.runningNum,
			&counts.successNum,
			&counts.failNum,
			&counts.otherNum,
			&deleteNum,
			&errorNum,
			&counts.sumSize,
		); err != nil {
			return err
		}

		dataList := make([]map[string]interface{}, 0)
		if counts.allNum > 0 && offset < counts.allNum {
			dataArgs := appendSQLArgs(where.args, ps, offset)
			rows, err := fetchAllToTable(tx, dataSQL, dataArgs...)
			if err != nil {
				return err
			}
			dataList = rows
		}
		stats := counts.toMap()
		stats["deleteNum"] = deleteNum
		stats["errorNum"] = errorNum
		result = map[string]interface{}{
			"dataList": dataList,
			"count":    counts.allNum,
			"pageNum":  pn,
			"pageSize": ps,
			"stats":    stats,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

const maxExportTaskItemRows = 50000

// GetJobTaskItemsForExport fetches filtered task items bounded by limit for CSV export.
func GetJobTaskItemsForExport(params map[string]interface{}, limit int) ([]map[string]interface{}, error) {
	if limit <= 0 || limit > maxExportTaskItemRows {
		limit = maxExportTaskItemRows
	}
	where := buildJobTaskItemWhere(params)
	query := fmt.Sprintf("SELECT %s FROM job_task_item %s ORDER BY createTime DESC, id DESC LIMIT ?", jobTaskItemListColumns, where.clause)
	args := appendSQLArgs(where.args, limit)
	return fetchAllToTableWithLimit(GetDB(), limit, query, args...)
}

// GetTopFailedTaskItems returns up to limit failed items for notification error summaries.
func GetTopFailedTaskItems(taskID int64, limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 3
	}
	return FetchAllToTable(
		fmt.Sprintf("SELECT %s FROM job_task_item WHERE taskId=? AND (status=7 OR (errMsg IS NOT NULL AND errMsg<>'')) ORDER BY id ASC LIMIT ?", jobTaskItemRuntimeColumns),
		taskID, limit,
	)
}

// CountJobTaskItemsByIDs counts selected task items matching any of the given statuses.
func CountJobTaskItemsByIDs(taskID int64, itemIDs []int64, statuses []int) (int64, error) {
	if len(itemIDs) == 0 || len(statuses) == 0 {
		return 0, nil
	}
	statusClause, statusArgs := statusInClause(statuses)
	var total int64
	for start := 0; start < len(itemIDs); start += 400 {
		end := start + 400
		if end > len(itemIDs) {
			end = len(itemIDs)
		}
		idClause, idArgs := int64InClause(itemIDs[start:end])
		query := fmt.Sprintf("SELECT COUNT(id) FROM job_task_item WHERE taskId=? AND status IN (%s) AND id IN (%s)", statusClause, idClause)
		args := make([]interface{}, 0, 1+len(statusArgs)+len(idArgs))
		args = append(args, taskID)
		args = append(args, statusArgs...)
		args = append(args, idArgs...)
		var count int64
		if err := GetDB().QueryRow(query, args...).Scan(&count); err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

// ForEachJobTaskItemsByIDs reads selected task items in id order matching statuses.
func ForEachJobTaskItemsByIDs(taskID int64, itemIDs []int64, statuses []int, batchSize int, fn func([]map[string]interface{}) error) error {
	if len(itemIDs) == 0 || len(statuses) == 0 {
		return nil
	}
	if batchSize <= 0 || batchSize > 400 {
		batchSize = 400
	}
	statusClause, statusArgs := statusInClause(statuses)
	for start := 0; start < len(itemIDs); start += batchSize {
		end := start + batchSize
		if end > len(itemIDs) {
			end = len(itemIDs)
		}
		idClause, idArgs := int64InClause(itemIDs[start:end])
		query := fmt.Sprintf(
			`SELECT %s FROM job_task_item
			 WHERE taskId=? AND status IN (%s) AND id IN (%s)
			 ORDER BY id ASC`,
			jobTaskItemRuntimeColumns,
			statusClause,
			idClause,
		)
		args := make([]interface{}, 0, 1+len(statusArgs)+len(idArgs))
		args = append(args, taskID)
		args = append(args, statusArgs...)
		args = append(args, idArgs...)
		items, err := FetchAllToTable(query, args...)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			continue
		}
		if err := fn(items); err != nil {
			return err
		}
	}
	return nil
}

// CountJobTaskItemsByStatuses counts task items matching any of the given statuses.
func CountJobTaskItemsByStatuses(taskID int64, statuses []int) (int64, error) {
	if len(statuses) == 0 {
		return 0, nil
	}
	clause, args := statusInClause(statuses)
	query := fmt.Sprintf("SELECT COUNT(id) FROM job_task_item WHERE taskId=? AND status IN (%s)", clause)
	queryArgs := make([]interface{}, 1, len(args)+1)
	queryArgs[0] = taskID
	queryArgs = append(queryArgs, args...)
	var count int64
	if err := GetDB().QueryRow(query, queryArgs...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// ForEachJobTaskItemsByStatuses reads task items in bounded batches, in insert
// (id) order.
//
// The cursor is the id alone. The previous (createTime, id) keyset needed an OR
// that no index could serve, so every batch re-scanned the task's items, and a
// row with a NULL createTime failed both comparisons and was silently never
// replayed. ids are assigned monotonically (AUTOINCREMENT), so the cursor never
// skips or repeats a row. Insert order is the order items finished, not the
// order they were queued (createTime); for the retry replay that is still the
// order that matters, since a directory's record is written when it is created,
// before anything inside it is queued.
func ForEachJobTaskItemsByStatuses(taskID int64, statuses []int, batchSize int, fn func([]map[string]interface{}) error) error {
	if len(statuses) == 0 {
		return nil
	}
	if batchSize <= 0 {
		batchSize = 500
	}
	clause, statusArgs := statusInClause(statuses)
	query := fmt.Sprintf(
		`SELECT %s FROM job_task_item
		 WHERE taskId=? AND status IN (%s) AND id > ?
		 ORDER BY id ASC
		 LIMIT ?`,
		jobTaskItemRuntimeColumns,
		clause,
	)
	lastID := int64(0)

	for {
		args := append([]interface{}{taskID}, statusArgs...)
		args = append(args, lastID, batchSize)
		items, err := FetchAllToTable(query, args...)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		if err := fn(items); err != nil {
			return err
		}
		lastID = util.ToInt64(items[len(items)-1]["id"])
	}
}

const jobTaskCountSelect = `
			COUNT(id) AS allNum,
			COALESCE(SUM(CASE WHEN status=0 THEN 1 ELSE 0 END), 0) AS waitNum,
			COALESCE(SUM(CASE WHEN status=1 THEN 1 ELSE 0 END), 0) AS runningNum,
			COALESCE(SUM(CASE WHEN status=2 THEN 1 ELSE 0 END), 0) AS successNum,
			COALESCE(SUM(CASE WHEN status=7 THEN 1 ELSE 0 END), 0) AS failNum,
			COALESCE(SUM(CASE WHEN status NOT IN (0,1,2,7) THEN 1 ELSE 0 END), 0) AS otherNum,
			COALESCE(SUM(CASE WHEN status=2 AND type<>1 AND fileSize IS NOT NULL THEN fileSize ELSE 0 END), 0) AS sumSize`

func inClause(values []interface{}) (string, []interface{}) {
	placeholders := make([]string, len(values))
	for i := range values {
		placeholders[i] = "?"
	}
	return strings.Join(placeholders, ","), values
}

func statusInClause(statuses []int) (string, []interface{}) {
	args := make([]interface{}, 0, len(statuses))
	for _, status := range statuses {
		args = append(args, status)
	}
	return inClause(args)
}

func int64InClause(values []int64) (string, []interface{}) {
	args := make([]interface{}, 0, len(values))
	for _, value := range values {
		args = append(args, value)
	}
	return inClause(args)
}

// QueryJobTaskCounts returns all task item status counters in one query.
//
// A failed read is returned, not folded into zero counts: callers cache the
// result into job_task.taskNum and derive the final status from allNum, so a
// transient "database is locked" used to mark a finished task "nothing synced"
// and keep showing zeros for it forever.
func QueryJobTaskCounts(taskID int64) (map[string]interface{}, error) {
	var counts jobTaskCounts
	if err := GetDB().QueryRow(
		fmt.Sprintf(`SELECT%s FROM job_task_item WHERE taskId=?`, jobTaskCountSelect),
		taskID,
	).Scan(
		&counts.allNum,
		&counts.waitNum,
		&counts.runningNum,
		&counts.successNum,
		&counts.failNum,
		&counts.otherNum,
		&counts.sumSize,
	); err != nil {
		return nil, err
	}
	return counts.toMap(), nil
}

// GetJobTaskCountsByTaskIDs returns task item counters for many tasks in one
// query. Tasks without items get zero counters. On error the result is nil:
// a partial map would be indistinguishable from real zeros to a caller about to
// cache it.
func GetJobTaskCountsByTaskIDs(taskIDs []int64) (map[int64]map[string]interface{}, error) {
	results := make(map[int64]map[string]interface{}, len(taskIDs))
	uniqueIDs := make([]int64, 0, len(taskIDs))
	seen := make(map[int64]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		if taskID <= 0 {
			continue
		}
		if _, ok := seen[taskID]; ok {
			continue
		}
		seen[taskID] = struct{}{}
		uniqueIDs = append(uniqueIDs, taskID)
		results[taskID] = EmptyJobTaskCounts()
	}
	if len(uniqueIDs) == 0 {
		return results, nil
	}

	clause, args := int64InClause(uniqueIDs)
	rows, err := GetDB().Query(
		fmt.Sprintf(`SELECT
					taskId,%s
				FROM job_task_item
				WHERE taskId IN (%s)
				GROUP BY taskId`, jobTaskCountSelect, clause),
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var taskID int64
		var counts jobTaskCounts
		if err := rows.Scan(
			&taskID,
			&counts.allNum,
			&counts.waitNum,
			&counts.runningNum,
			&counts.successNum,
			&counts.failNum,
			&counts.otherNum,
			&counts.sumSize,
		); err != nil {
			return nil, err
		}
		results[taskID] = counts.toMap()
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

type jobTaskCounts struct {
	allNum     int64
	waitNum    int64
	runningNum int64
	successNum int64
	failNum    int64
	otherNum   int64
	sumSize    int64
}

func (c jobTaskCounts) toMap() map[string]interface{} {
	return map[string]interface{}{
		"waitNum":    c.waitNum,
		"runningNum": c.runningNum,
		"successNum": c.successNum,
		"failNum":    c.failNum,
		"otherNum":   c.otherNum,
		"allNum":     c.allNum,
		"sumSize":    c.sumSize,
	}
}

func EmptyJobTaskCounts() map[string]interface{} {
	return map[string]interface{}{
		"waitNum":    int64(0),
		"runningNum": int64(0),
		"successNum": int64(0),
		"failNum":    int64(0),
		"otherNum":   int64(0),
		"allNum":     int64(0),
		"sumSize":    int64(0),
	}
}

// taskItemKeywordFilter builds the keyword predicate for a task-scoped item
// query. It intentionally uses LIKE only: the previous FTS5 trigram index was
// not scoped to the task, and the OR with the LIKE terms forced SQLite to scan
// every item of the task anyway, so the index cost ~45% of the database and a
// trigger write on every insert without making common searches faster.
func taskItemKeywordFilter(keyword string) (string, []interface{}) {
	like := "%" + escapeLike(keyword) + "%"
	return " AND (fileName LIKE ? ESCAPE '\\' OR srcPath LIKE ? ESCAPE '\\' OR dstPath LIKE ? ESCAPE '\\' OR alistTaskId LIKE ? ESCAPE '\\' OR errMsg LIKE ? ESCAPE '\\')",
		[]interface{}{like, like, like, like, like}
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// ForEachRecentTaskWithTime streams recent tasks since unix timestamp including
// each task's effective timestamp (runTime fallback createTime), status, and taskNum.
func ForEachRecentTaskWithTime(since int64, fn func(ts int64, status int, taskNum string) error) error {
	rows, err := GetDB().Query(
		`SELECT COALESCE(NULLIF(runTime, 0), createTime), status, COALESCE(taskNum, '')
		 FROM job_task
		 WHERE COALESCE(NULLIF(runTime, 0), createTime) >= ?`,
		since,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ts int64
		var status int
		var taskNum string
		if err := rows.Scan(&ts, &status, &taskNum); err != nil {
			return err
		}
		if err := fn(ts, status, taskNum); err != nil {
			return err
		}
	}
	return rows.Err()
}

// GetTaskErrorGroups returns top grouped failure messages for a historical task.
func GetTaskErrorGroups(taskID int64, limit int) ([]map[string]interface{}, error) {
	if taskID <= 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	rows, err := GetDB().Query(
		`SELECT TRIM(errMsg) AS reason, COUNT(id) AS cnt
		 FROM job_task_item
		 WHERE taskId=? AND errMsg IS NOT NULL AND TRIM(errMsg) <> ''
		 GROUP BY TRIM(errMsg)
		 ORDER BY cnt DESC
		 LIMIT ?`,
		taskID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]interface{}, 0)
	for rows.Next() {
		var reason string
		var cnt int64
		if err := rows.Scan(&reason, &cnt); err != nil {
			return nil, err
		}
		out = append(out, map[string]interface{}{
			"reason": reason,
			"count":  cnt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
