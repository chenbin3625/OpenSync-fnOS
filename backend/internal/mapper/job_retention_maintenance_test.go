package mapper

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestShouldVacuumRequiresAbsoluteAndRelativeThreshold(t *testing.T) {
	cases := []struct {
		name      string
		freePages int64
		pageCount int64
		want      bool
	}{
		{"nothing free", 0, 100000, false},
		{"unknown page count", 5000, 0, false},
		{"below absolute threshold", 4095, 4100, false},
		{"exactly at both thresholds", 4096, 4096 * 5, true},
		{"above absolute but small share of a big file", 100000, 1000000, false},
		{"just under twenty percent", 199999, 1000000, false},
		{"twenty percent of a big file", 200000, 1000000, true},
		{"mostly free", 90000, 100000, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldVacuum(tc.freePages, tc.pageCount, freePageVacuumThreshold); got != tc.want {
				t.Fatalf("shouldVacuum(%d, %d) = %v, want %v", tc.freePages, tc.pageCount, got, tc.want)
			}
		})
	}
}

// newRetentionTestDB builds the job_task/job_task_item schema including the FTS
// table and its triggers, so deletes pay the same per-row trigger cost as in
// production.
func newRetentionTestDB(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	testDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	t.Cleanup(func() { testDB.Close() })
	testDB.SetMaxOpenConns(1)
	stmts := []string{
		`CREATE TABLE job_task(
			id integer primary key autoincrement,
			jobId integer,
			status integer DEFAULT 1,
			runTime integer,
			createTime integer
		)`,
		`CREATE TABLE job_task_item(
			id integer primary key autoincrement,
			taskId integer,
			srcPath text,
			dstPath text,
			fileName text,
			status integer
		)`,
	}
	stmts = append(stmts, jobTaskItemFTSStatements(false)...)
	for _, stmt := range stmts {
		if _, err := testDB.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	return testDB
}

func insertTaskWithItems(t *testing.T, testDB *sql.DB, taskID int64, runTime int64, status int, items int) {
	t.Helper()
	if _, err := testDB.Exec("INSERT INTO job_task(id, jobId, status, runTime, createTime) VALUES (?, 1, ?, ?, ?)",
		taskID, status, runTime, runTime); err != nil {
		t.Fatalf("insert job_task %d: %v", taskID, err)
	}
	if _, err := testDB.Exec(
		`WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < ?)
		 INSERT INTO job_task_item(taskId, srcPath, dstPath, fileName, status)
		 SELECT ?, '/src/', '/dst/', 'file-' || x || '.bin', 2 FROM c`,
		items, taskID,
	); err != nil {
		t.Fatalf("insert items for task %d: %v", taskID, err)
	}
}

func countRows(t *testing.T, testDB *sql.DB, query string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	if err := testDB.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestDeleteJobTasksByIDsRemovesAllItemsInSmallBatches(t *testing.T) {
	testDB := newRetentionTestDB(t, ":memory:")
	restore := SetDBForTest(testDB)
	defer restore()

	oldBatch := jobTaskItemDeleteBatchSize
	jobTaskItemDeleteBatchSize = 7
	defer func() { jobTaskItemDeleteBatchSize = oldBatch }()

	insertTaskWithItems(t, testDB, 1, 100, 2, 23)
	insertTaskWithItems(t, testDB, 2, 100, 2, 1)
	insertTaskWithItems(t, testDB, 3, 100, 2, 50)
	insertTaskWithItems(t, testDB, 4, 100, 2, 9) // not in the delete set

	if err := deleteJobTasksByIDs(context.Background(), []int64{1, 2, 3}); err != nil {
		t.Fatalf("deleteJobTasksByIDs() error: %v", err)
	}

	if n := countRows(t, testDB, "SELECT COUNT(*) FROM job_task_item WHERE taskId IN (1,2,3)"); n != 0 {
		t.Fatalf("remaining items of deleted tasks = %d, want 0", n)
	}
	if n := countRows(t, testDB, "SELECT COUNT(*) FROM job_task WHERE id IN (1,2,3)"); n != 0 {
		t.Fatalf("remaining deleted task rows = %d, want 0", n)
	}
	if n := countRows(t, testDB, "SELECT COUNT(*) FROM job_task_item WHERE taskId=4"); n != 9 {
		t.Fatalf("items of kept task = %d, want 9", n)
	}
	// The FTS delete trigger fired for every removed row.
	if n := countRows(t, testDB, "SELECT COUNT(*) FROM job_task_item_fts WHERE job_task_item_fts MATCH 'file'"); n != 9 {
		t.Fatalf("fts rows = %d, want only the kept task's 9", n)
	}
}

func TestDeleteJobTaskByRunTimeContextStopsWhenCancelled(t *testing.T) {
	testDB := newRetentionTestDB(t, ":memory:")
	restore := SetDBForTest(testDB)
	defer restore()

	insertTaskWithItems(t, testDB, 1, 100, 2, 5)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := DeleteJobTaskByRunTimeContext(ctx, 1000, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteJobTaskByRunTimeContext() error = %v, want context.Canceled", err)
	}
	if n := countRows(t, testDB, "SELECT COUNT(*) FROM job_task_item"); n != 5 {
		t.Fatalf("items = %d, want cleanup to stop before deleting anything", n)
	}
}

func TestDeleteJobTaskByRunTimeContextKeepsRunningAndRecentTasks(t *testing.T) {
	testDB := newRetentionTestDB(t, ":memory:")
	restore := SetDBForTest(testDB)
	defer restore()

	insertTaskWithItems(t, testDB, 1, 100, 2, 4)  // expired, finished
	insertTaskWithItems(t, testDB, 2, 100, 1, 4)  // expired but running
	insertTaskWithItems(t, testDB, 3, 5000, 2, 4) // recent

	if err := DeleteJobTaskByRunTimeContext(context.Background(), 1000, nil); err != nil {
		t.Fatalf("DeleteJobTaskByRunTimeContext() error: %v", err)
	}
	if n := countRows(t, testDB, "SELECT COUNT(*) FROM job_task"); n != 2 {
		t.Fatalf("task rows = %d, want the running and recent tasks kept", n)
	}
	if n := countRows(t, testDB, "SELECT COUNT(*) FROM job_task_item WHERE taskId=1"); n != 0 {
		t.Fatalf("items of expired task = %d, want 0", n)
	}
}

// fileRetentionDB returns a file-backed database with enough freed pages that
// shouldVacuum would say yes.
func fileRetentionDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := sqliteDSN(filepath.Join(t.TempDir(), "retention.db"))
	testDB := newRetentionTestDB(t, dsn)
	// Rows just over one page each (a leaf cell plus an overflow page), so the
	// freed page count clears freePageVacuumThreshold with a ~10 MiB fixture.
	if _, err := testDB.Exec(`CREATE TABLE filler(id integer primary key, payload blob)`); err != nil {
		t.Fatalf("create filler: %v", err)
	}
	if _, err := testDB.Exec(
		`WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 4000)
		 INSERT INTO filler(id, payload) SELECT x, zeroblob(4096) FROM c`,
	); err != nil {
		t.Fatalf("fill: %v", err)
	}
	if _, err := testDB.Exec("DELETE FROM filler"); err != nil {
		t.Fatalf("empty filler: %v", err)
	}
	if _, err := testDB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if free := countRows(t, testDB, "PRAGMA freelist_count"); free < freePageVacuumThreshold {
		t.Fatalf("freelist_count = %d, fixture needs at least %d", free, freePageVacuumThreshold)
	}
	return testDB
}

func TestVacuumIfWorthwhileReclaimsPagesAndRestoresTempStore(t *testing.T) {
	testDB := fileRetentionDB(t)
	restore := SetDBForTest(testDB)
	defer restore()

	if err := vacuumIfWorthwhile(context.Background(), func() bool { return false }); err != nil {
		t.Fatalf("vacuumIfWorthwhile() error: %v", err)
	}
	if free := countRows(t, testDB, "PRAGMA freelist_count"); free != 0 {
		t.Fatalf("freelist_count after VACUUM = %d, want 0", free)
	}
	// The pool holds a single connection, so this reads the one VACUUM used:
	// it must be back on the pool-wide MEMORY setting (2).
	if store := countRows(t, testDB, "PRAGMA temp_store"); store != 2 {
		t.Fatalf("temp_store after VACUUM = %d, want 2 (MEMORY)", store)
	}
}

func TestDeleteJobTaskByRunTimeContextSkipsMaintenanceWhileTaskRuns(t *testing.T) {
	testDB := fileRetentionDB(t)
	restore := SetDBForTest(testDB)
	defer restore()
	before := countRows(t, testDB, "PRAGMA freelist_count")

	if err := DeleteJobTaskByRunTimeContext(context.Background(), 1000, func() bool { return true }); err != nil {
		t.Fatalf("DeleteJobTaskByRunTimeContext() error: %v", err)
	}
	if free := countRows(t, testDB, "PRAGMA freelist_count"); free != before {
		t.Fatalf("freelist_count = %d, want %d: VACUUM must not run while a task is running", free, before)
	}
}

func TestWatchMaintenanceBlockedCancelsWhenTaskStarts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var running atomic.Bool
	stop := watchMaintenanceBlocked(ctx, cancel, running.Load, time.Millisecond)
	defer stop()

	select {
	case <-ctx.Done():
		t.Fatal("watcher cancelled while no task was running")
	case <-time.After(20 * time.Millisecond):
	}
	running.Store(true)
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not cancel after a task started")
	}
}

func TestVacuumIfWorthwhileSkipsSmallShareOfFreePages(t *testing.T) {
	testDB := fileRetentionDB(t)
	restore := SetDBForTest(testDB)
	defer restore()
	// Grow the live data so the freed pages drop well under 20% of the file.
	if _, err := testDB.Exec(
		`WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < 40000)
		 INSERT INTO filler(id, payload) SELECT x, zeroblob(4096) FROM c`,
	); err != nil {
		t.Fatalf("grow: %v", err)
	}
	if _, err := testDB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	before := countRows(t, testDB, "PRAGMA freelist_count")
	pages := countRows(t, testDB, "PRAGMA page_count")
	if shouldVacuum(before, pages, freePageVacuumThreshold) {
		t.Skipf("fixture still qualifies for VACUUM (%d of %d free)", before, pages)
	}

	if err := vacuumIfWorthwhile(context.Background(), nil); err != nil {
		t.Fatalf("vacuumIfWorthwhile() error: %v", err)
	}
	if free := countRows(t, testDB, "PRAGMA freelist_count"); free != before {
		t.Fatalf("freelist_count = %d, want unchanged %d", free, before)
	}
}
