package mapper

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// List queries order by createTime DESC, id DESC. createTime is second-grained
// and AddJobTaskItemMany stamps a whole batch with the same value, so ties are
// the norm rather than an edge case; without the unique id tiebreaker LIMIT/
// OFFSET paging can repeat or skip rows between pages.
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
		return nil, errors.New(msg.T(msg.JobNotFound))
	}
	return rst[0], nil
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
		return nil, errors.New(msg.T(msg.JobNotFound))
	}
	return rst[0], nil
}

// AddJob inserts a new job
func AddJob(job map[string]interface{}) (int64, error) {
	return ExecuteInsert(
		`INSERT INTO job (enable, remark, srcPath, dstPath, alistId, useCacheT, scanIntervalT, useCacheS, scanIntervalS,
		 method, interval, isCron, month, day, day_of_week, hour, minute, second, exclude,
		 minFileSize, maxFileSize)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job["enable"], job["remark"], job["srcPath"], job["dstPath"], job["alistId"],
		job["useCacheT"], job["scanIntervalT"], job["useCacheS"], job["scanIntervalS"],
		job["method"], job["interval"], job["isCron"],
		job["month"], job["day"], job["day_of_week"],
		job["hour"], job["minute"], job["second"], job["exclude"],
		job["minFileSize"], job["maxFileSize"],
	)
}

// UpdateJob updates a job
func UpdateJob(job map[string]interface{}) error {
	return ExecuteUpdate(
		`UPDATE job SET enable=?, remark=?, srcPath=?, dstPath=?, alistId=?, useCacheT=?, scanIntervalT=?,
		 useCacheS=?, scanIntervalS=?, method=?, interval=?, isCron=?, month=?, day=?,
		 day_of_week=?, hour=?, minute=?, second=?, exclude=?, minFileSize=?, maxFileSize=? WHERE id=?`,
		job["enable"], job["remark"], job["srcPath"], job["dstPath"], job["alistId"],
		job["useCacheT"], job["scanIntervalT"], job["useCacheS"], job["scanIntervalS"],
		job["method"], job["interval"], job["isCron"],
		job["month"], job["day"], job["day_of_week"],
		job["hour"], job["minute"], job["second"], job["exclude"],
		job["minFileSize"], job["maxFileSize"],
		job["id"],
	)
}

// UpdateJobEnable updates job enable status
func UpdateJobEnable(jobID int64, enable int) error {
	return ExecuteUpdate("UPDATE job SET enable=? WHERE id=?", enable, jobID)
}

// DeleteJob deletes a job and its tasks
func DeleteJob(jobID int64) error {
	return withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM job_task_item WHERE taskId IN (SELECT id FROM job_task WHERE jobId=?)", jobID); err != nil {
			return err
		}
		if _, err := tx.Exec("DELETE FROM job_task WHERE jobId=?", jobID); err != nil {
			return err
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
		statuses := make([]int, 0, len(v))
		for _, item := range v {
			statuses = append(statuses, util.ToInt(item))
		}
		return statuses
	case string:
		return parseStatusCSV(v)
	default:
		return []int{util.ToInt(v)}
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
		return nil, errors.New(msg.T(msg.TaskNotFound))
	}
	return rst[0], nil
}

// AddJobTask inserts a new job task
func AddJobTask(jobID int64, runTime int64) (int64, error) {
	return ExecuteInsert("INSERT INTO job_task (jobId, runTime) VALUES (?, ?)", jobID, runTime)
}

// UpdateJobTaskStatusByStatus updates incomplete tasks to aborted (for restart)
func UpdateJobTaskStatusByStatus() error {
	return ExecuteUpdate("UPDATE job_task SET status=4 WHERE status IN (0, 1)")
}

// UpdateJobTaskStatusByStatusAndJobID updates incomplete tasks to aborted for a job
func UpdateJobTaskStatusByStatusAndJobID(jobID int64) error {
	return ExecuteUpdate("UPDATE job_task SET status=4 WHERE status IN (0, 1) AND jobId=?", jobID)
}

// DeleteJobTaskByTaskID deletes a task and its items
func DeleteJobTaskByTaskID(taskID int64) error {
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

// DeleteJobTaskByRunTime deletes old tasks. It is the context-free form of
// DeleteJobTaskByRunTimeContext and never skips maintenance.
func DeleteJobTaskByRunTime(runTime int64) error {
	return DeleteJobTaskByRunTimeContext(context.Background(), runTime, nil)
}

// DeleteJobTaskByRunTimeContext deletes finished tasks older than runTime, then
// reclaims space. ctx is checked between every short delete batch, so shutdown
// can stop a cleanup of years of history without waiting for it to finish.
//
// maintenanceBlocked reports whether a sync is running. The WAL truncate and
// VACUUM hold the write lock for as long as they take, and a running task's
// item writes give up after busy_timeout; both are skipped while it returns
// true, and a VACUUM already under way is interrupted when it turns true.
// A nil maintenanceBlocked never blocks.
func DeleteJobTaskByRunTimeContext(ctx context.Context, runTime int64, maintenanceBlocked func() bool) error {
	if maintenanceBlocked == nil {
		maintenanceBlocked = func() bool { return false }
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		taskIDs, err := expiredJobTaskIDs(runTime, expiredTaskDeleteBatchSize)
		if err != nil {
			return err
		}
		if len(taskIDs) == 0 {
			break
		}
		if err := deleteJobTasksByIDs(ctx, taskIDs); err != nil {
			return err
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
	_, _ = GetDB().ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	_, _ = GetDB().ExecContext(ctx, "PRAGMA optimize")
	if err := vacuumIfWorthwhile(ctx, maintenanceBlocked); err != nil {
		log.Printf("Failed to reclaim free database pages: %v", err)
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
	if limit <= 0 {
		limit = expiredTaskDeleteBatchSize
	}
	rows, err := GetDB().Query(
		`SELECT id FROM job_task
		 WHERE COALESCE(NULLIF(runTime, 0), createTime) < ?
		   AND status NOT IN (0, 1)
		 ORDER BY COALESCE(NULLIF(runTime, 0), createTime), id
		 LIMIT ?`,
		cutoff,
		limit,
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
	itemArgs := appendSQLArgs(args, jobTaskItemDeleteBatchSize)
	deleteItems := "DELETE FROM job_task_item WHERE id IN (SELECT id FROM job_task_item WHERE taskId IN (" + clause + ") LIMIT ?)"
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := GetDB().ExecContext(ctx, deleteItems, itemArgs...)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			break
		}
	}
	_, err := GetDB().ExecContext(ctx, "DELETE FROM job_task WHERE id IN ("+clause+")", args...)
	return err
}

func withTx(fn func(*sql.Tx) error) error {
	tx, err := GetDB().Begin()
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
			createTime,
		})
	}
	return ExecuteMany(
		`INSERT INTO job_task_item (taskId, srcPath, dstPath, isPath, fileName, fileSize, type, alistTaskId, status, errMsg, createTime)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		argsList,
	)
}

// GetJobTaskItemList gets paginated task item list
func GetJobTaskItemList(params map[string]interface{}) (map[string]interface{}, error) {
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

	baseSQL := fmt.Sprintf("SELECT %s FROM job_task_item %s ORDER BY createTime DESC, id DESC", jobTaskItemListColumns, where.clause)
	// A separate COUNT(*) instead of FetchAllToPage's window count: the window
	// forces SQLite to materialize and sort every matching item on each page,
	// which for a million-item task is a full scan per click.
	countSQL := fmt.Sprintf("SELECT COUNT(*) FROM job_task_item %s", where.clause)
	return FetchAllToPageWithCount(baseSQL, countSQL, params, where.args...)
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

// GetJobTaskCounts is the legacy, error-swallowing form of QueryJobTaskCounts:
// on failure it logs and returns zero counters. New callers should use
// QueryJobTaskCounts, which lets them avoid caching or acting on counts that
// were never actually read.
func GetJobTaskCounts(taskID int64) map[string]interface{} {
	counts, err := QueryJobTaskCounts(taskID)
	if err != nil {
		log.Printf("Failed to count items of task %d: %v", taskID, err)
		return EmptyJobTaskCounts()
	}
	return counts
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

func taskItemKeywordFilter(keyword string) (string, []interface{}) {
	like := "%" + escapeLike(keyword) + "%"
	if taskItemFTSAvailable() && utf8.RuneCountInString(keyword) >= 3 {
		return ` AND (
			id IN (SELECT rowid FROM job_task_item_fts WHERE job_task_item_fts MATCH ?)
			OR alistTaskId LIKE ? ESCAPE '\'
			OR errMsg LIKE ? ESCAPE '\'
		)`, []interface{}{fts5Phrase(keyword), like, like}
	}
	return " AND (fileName LIKE ? ESCAPE '\\' OR srcPath LIKE ? ESCAPE '\\' OR dstPath LIKE ? ESCAPE '\\' OR alistTaskId LIKE ? ESCAPE '\\' OR errMsg LIKE ? ESCAPE '\\')",
		[]interface{}{like, like, like, like, like}
}

func taskItemFTSAvailable() bool {
	handle := GetDB()
	ftsAvailability.mu.Lock()
	defer ftsAvailability.mu.Unlock()
	if ftsAvailability.known && ftsAvailability.db == handle {
		return ftsAvailability.ok
	}
	var name string
	err := handle.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='job_task_item_fts'").Scan(&name)
	ok := err == nil && name == "job_task_item_fts"
	ftsAvailability.db = handle
	ftsAvailability.known = true
	ftsAvailability.ok = ok
	return ok
}

var ftsAvailability struct {
	mu    sync.Mutex
	db    *sql.DB
	known bool
	ok    bool
}

func fts5Phrase(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
