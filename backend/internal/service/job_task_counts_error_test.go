package service

import (
	"database/sql"
	"errors"
	"opensync/internal/mapper"
	"testing"

	_ "modernc.org/sqlite"
)

// A failed count used to come back as zeros, which GetTaskList then wrote into
// job_task.taskNum — and a cached value is never recomputed, so the task showed
// "0 items" for good.
func TestGetTaskListDoesNotCacheCountsWhenCountQueryFails(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()
	if _, err := testDB.Exec(`CREATE TABLE job_task(
		id integer primary key autoincrement,
		jobId integer,
		status integer,
		runTime integer,
		taskNum text,
		createTime integer
	)`); err != nil {
		t.Fatalf("create job_task: %v", err)
	}
	// Finished (status 2) with no cached taskNum: exactly the rows GetTaskList
	// computes and caches.
	if _, err := testDB.Exec("INSERT INTO job_task(id, jobId, status, runTime, createTime) VALUES (10, 1, 2, 100, 100)"); err != nil {
		t.Fatalf("insert job_task: %v", err)
	}
	restoreDB := mapper.SetDBForTest(testDB)
	defer restoreDB()

	countErr := errors.New("database is locked")
	oldCounts := jobTaskCountsByTaskIDs
	jobTaskCountsByTaskIDs = func([]int64) (map[int64]map[string]interface{}, error) { return nil, countErr }
	defer func() { jobTaskCountsByTaskIDs = oldCounts }()

	var scheduled [][]map[string]interface{}
	oldSchedule := scheduleTaskNumUpdateFn
	scheduleTaskNumUpdateFn = func(rows []map[string]interface{}) { scheduled = append(scheduled, rows) }
	defer func() { scheduleTaskNumUpdateFn = oldSchedule }()

	result, err := GetTaskList(map[string]interface{}{"id": int64(1)})
	if !errors.Is(err, countErr) {
		t.Fatalf("GetTaskList() = %#v, %v; want the count error", result, err)
	}
	if len(scheduled) != 0 {
		t.Fatalf("scheduled taskNum updates = %#v, want none on a failed count", scheduled)
	}
	var taskNum sql.NullString
	if err := testDB.QueryRow("SELECT taskNum FROM job_task WHERE id=10").Scan(&taskNum); err != nil {
		t.Fatalf("read taskNum: %v", err)
	}
	if taskNum.Valid {
		t.Fatalf("taskNum = %q, want it left NULL", taskNum.String)
	}
}

// The success path still caches, through the same hook.
func TestGetTaskListCachesCountsOfFinishedTasks(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()
	for _, stmt := range []string{
		`CREATE TABLE job_task(id integer primary key, jobId integer, status integer, runTime integer, taskNum text, createTime integer)`,
		`CREATE TABLE job_task_item(id integer primary key, taskId integer, fileSize integer, type integer, status integer)`,
		"INSERT INTO job_task(id, jobId, status, runTime, createTime) VALUES (10, 1, 2, 100, 100)",
		"INSERT INTO job_task_item(taskId, status, type, fileSize) VALUES (10, 2, 0, 5)",
	} {
		if _, err := testDB.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	restoreDB := mapper.SetDBForTest(testDB)
	defer restoreDB()

	var scheduled []map[string]interface{}
	oldSchedule := scheduleTaskNumUpdateFn
	scheduleTaskNumUpdateFn = func(rows []map[string]interface{}) { scheduled = append(scheduled, rows...) }
	defer func() { scheduleTaskNumUpdateFn = oldSchedule }()

	if _, err := GetTaskList(map[string]interface{}{"id": int64(1)}); err != nil {
		t.Fatalf("GetTaskList() error: %v", err)
	}
	if len(scheduled) != 1 || scheduled[0]["taskId"] != int64(10) {
		t.Fatalf("scheduled = %#v, want one update for task 10", scheduled)
	}
}

// finishing a task derives its final status from the item counters, so a failed
// count must fail the status update instead of reading as "zero items" (which
// maps to taskStatusNoSync and would be cached into taskNum).
func TestUpdateTaskStatusPropagatesCountError(t *testing.T) {
	countErr := errors.New("database is locked")
	oldQuery := queryJobTaskCounts
	queryJobTaskCounts = func(int64) (map[string]interface{}, error) { return nil, countErr }
	defer func() { queryJobTaskCounts = oldQuery }()

	jt := &JobTask{TaskID: 42}
	if _, _, _, err := jt.updateTaskStatus(); !errors.Is(err, countErr) {
		t.Fatalf("updateTaskStatus() error = %v, want the count error", err)
	}
}

// A task that failed or was stopped still needs its status recorded when the
// counters cannot be read; taskNum is cleared so the list recomputes it.
func TestUpdateJobTaskStatusSimpleClearsTaskNumWhenCountFails(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()
	if _, err := testDB.Exec(`CREATE TABLE job_task(
		id integer primary key autoincrement,
		status integer,
		errMsg text,
		taskNum text,
		runTime integer
	)`); err != nil {
		t.Fatalf("create job_task: %v", err)
	}
	if _, err := testDB.Exec(`INSERT INTO job_task(id, status, taskNum) VALUES (7, 1, '{"allNum":0}')`); err != nil {
		t.Fatalf("insert job_task: %v", err)
	}
	restoreDB := mapper.SetDBForTest(testDB)
	defer restoreDB()

	oldQuery := queryJobTaskCounts
	queryJobTaskCounts = func(int64) (map[string]interface{}, error) { return nil, errors.New("database is locked") }
	defer func() { queryJobTaskCounts = oldQuery }()

	errMsg := "boom"
	if err := UpdateJobTaskStatusSimple(7, taskStatusSystemFailed, &errMsg); err != nil {
		t.Fatalf("UpdateJobTaskStatusSimple() error = %v", err)
	}
	var status int
	var taskNum sql.NullString
	if err := testDB.QueryRow("SELECT status, taskNum FROM job_task WHERE id=7").Scan(&status, &taskNum); err != nil {
		t.Fatalf("read job_task: %v", err)
	}
	if status != taskStatusSystemFailed.Int() {
		t.Fatalf("status = %d, want %d", status, taskStatusSystemFailed.Int())
	}
	if taskNum.Valid {
		t.Fatalf("taskNum = %q, want NULL so it is recomputed", taskNum.String)
	}
}
