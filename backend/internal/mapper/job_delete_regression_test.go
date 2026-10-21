package mapper

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// newDeletionFixture builds job/job_task/job_task_item (plus the FTS index and
// triggers the production schema has) with one job, one cached-count task and
// two items.
func newDeletionFixture(t *testing.T) *sql.DB {
	t.Helper()
	db := newFileTestDB(t)
	createJobSchema(t, db)
	if err := installJobTaskItemFTS(db, true); err != nil {
		t.Fatalf("install FTS: %v", err)
	}
	mustExec(t, db, `INSERT INTO job(id, remark, srcPath, dstPath, alistId) VALUES(1, 'job', '/src', '/dst', 1)`)
	mustExec(t, db, `INSERT INTO job_task(id, jobId, status, taskNum) VALUES(10, 1, 2, '{"allNum":2}')`)
	mustExec(t, db, `INSERT INTO job_task_item(id, taskId, srcPath, dstPath, isPath, fileName, type, status, createTime)
		VALUES(1, 10, '/src', '/dst', 0, 'a.bin', 0, 2, 1)`)
	mustExec(t, db, `INSERT INTO job_task_item(id, taskId, srcPath, dstPath, isPath, fileName, type, status, createTime)
		VALUES(2, 10, '/src', '/dst', 0, 'b.bin', 0, 2, 1)`)
	return db
}

// P2-4: the old "rollback" test never entered the transaction (it failed on the
// missing job_task_item table before withTx started). Inject a failure in the
// final statement instead so the rollback path is actually exercised.
func TestDeleteJobRollsBackTaskRowsWhenJobDeleteFails(t *testing.T) {
	db := newDeletionFixture(t)
	mustExec(t, db, `CREATE TRIGGER job_delete_block BEFORE DELETE ON job BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	restore := SetDBForTest(db)
	defer restore()

	if err := DeleteJob(1); err == nil {
		t.Fatal("DeleteJob() error = nil, want the injected failure")
	}
	// The task-row delete happened inside the transaction and must be rolled
	// back; the job row was never deleted.
	if got := mustCount(t, db, "SELECT COUNT(*) FROM job_task WHERE id=10"); got != 1 {
		t.Fatalf("job_task rows = %d, want the transaction rollback to keep 1", got)
	}
	if got := mustCount(t, db, "SELECT COUNT(*) FROM job WHERE id=1"); got != 1 {
		t.Fatalf("job rows = %d, want 1", got)
	}
	// P2-1: counters were invalidated before the separately committed batches,
	// so a half-finished delete can never show counts for items that are gone.
	var taskNum sql.NullString
	if err := db.QueryRow("SELECT taskNum FROM job_task WHERE id=10").Scan(&taskNum); err != nil {
		t.Fatalf("read taskNum: %v", err)
	}
	if taskNum.Valid {
		t.Fatalf("taskNum = %q, want NULL after the delete failed", taskNum.String)
	}
	// Documented trade-off of batching: items removed by the earlier batches are
	// not restored by the rollback, but the task row remains so retrying finishes
	// the delete.
	if got := mustCount(t, db, "SELECT COUNT(*) FROM job_task_item WHERE taskId=10"); got != 0 {
		t.Fatalf("item rows = %d, want the already-committed batches to be gone", got)
	}
}

// P2-1: cached counters are dropped before any batch runs, so a failure during
// the batched item delete leaves a task whose counters are recomputable.
func TestDeleteTaskClearsCachedCountsBeforeBatches(t *testing.T) {
	db := newDeletionFixture(t)
	mustExec(t, db, `CREATE TRIGGER item_delete_block BEFORE DELETE ON job_task_item BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	restore := SetDBForTest(db)
	defer restore()

	if err := DeleteJobTaskByTaskID(10); err == nil {
		t.Fatal("DeleteJobTaskByTaskID() error = nil, want the injected failure")
	}
	var taskNum sql.NullString
	if err := db.QueryRow("SELECT taskNum FROM job_task WHERE id=10").Scan(&taskNum); err != nil {
		t.Fatalf("read taskNum: %v", err)
	}
	if taskNum.Valid {
		t.Fatalf("taskNum = %q, want NULL: the cached counters must be invalidated before the batches", taskNum.String)
	}
	if got := mustCount(t, db, "SELECT COUNT(*) FROM job_task_item WHERE taskId=10"); got != 2 {
		t.Fatalf("item rows = %d, want the aborted batch to delete nothing", got)
	}
}

func TestWithTxRollsBackOnError(t *testing.T) {
	db := newFileTestDB(t)
	mustExec(t, db, "CREATE TABLE t(id integer primary key)")
	restore := SetDBForTest(db)
	defer restore()

	boom := errors.New("boom")
	err := withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO t(id) VALUES(1)"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("withTx() error = %v, want boom", err)
	}
	if got := mustCount(t, db, "SELECT COUNT(*) FROM t"); got != 0 {
		t.Fatalf("rows = %d, want the transaction to be rolled back", got)
	}
}

func TestWithTxCommitsOnSuccess(t *testing.T) {
	db := newFileTestDB(t)
	mustExec(t, db, "CREATE TABLE t(id integer primary key)")
	restore := SetDBForTest(db)
	defer restore()

	if err := withTx(func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO t(id) VALUES(1)")
		return err
	}); err != nil {
		t.Fatalf("withTx() error = %v", err)
	}
	if got := mustCount(t, db, "SELECT COUNT(*) FROM t"); got != 1 {
		t.Fatalf("rows = %d, want the committed insert", got)
	}
}

// P3-12: retention must keep draining batches until the expired set is empty,
// not stop after the first 500-task page.
func TestDeleteJobTaskByRunTimeContextDeletesMoreThanOneBatch(t *testing.T) {
	testDB := newRetentionTestDB(t, ":memory:")
	restore := SetDBForTest(testDB)
	defer restore()

	mustExec(t, testDB, `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x<501)
		INSERT INTO job_task(id, jobId, status, runTime, createTime)
		SELECT x, 1, 2, 100, 100 FROM c`)
	if got := mustCount(t, testDB, "SELECT COUNT(*) FROM job_task"); got != 501 {
		t.Fatalf("fixture task rows = %d, want 501", got)
	}
	if err := DeleteJobTaskByRunTimeContext(context.Background(), 1000, nil); err != nil {
		t.Fatalf("DeleteJobTaskByRunTimeContext() error = %v", err)
	}
	if got := mustCount(t, testDB, "SELECT COUNT(*) FROM job_task"); got != 0 {
		t.Fatalf("task rows after cleanup = %d, want 0 (more than one 500-task batch)", got)
	}
}

// A task with no timestamp at all is never considered expired: the retention
// predicate is conservative rather than treating unknown as 1970.
func TestRetentionKeepsTaskWithoutAnyTimestamp(t *testing.T) {
	testDB := newRetentionTestDB(t, ":memory:")
	restore := SetDBForTest(testDB)
	defer restore()

	mustExec(t, testDB, `INSERT INTO job_task(id, jobId, status, runTime, createTime) VALUES(1, 1, 2, 0, NULL)`)
	if err := DeleteJobTaskByRunTimeContext(context.Background(), 1000, nil); err != nil {
		t.Fatalf("DeleteJobTaskByRunTimeContext() error = %v", err)
	}
	if got := mustCount(t, testDB, "SELECT COUNT(*) FROM job_task"); got != 1 {
		t.Fatalf("task rows = %d, want the timestamp-less task kept", got)
	}
}

// P3-12: cancellation between batches, not only before the first one.
func TestDeleteJobTaskItemsInBatchesStopsMidWayWhenCancelled(t *testing.T) {
	testDB := newRetentionTestDB(t, ":memory:")
	restore := SetDBForTest(testDB)
	defer restore()

	mustExec(t, testDB, `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x<3000)
		INSERT INTO job_task_item(taskId, srcPath, dstPath, fileName, status)
		SELECT 1, '/src', '/dst', 'file-' || x || '.bin', 2 FROM c`)

	oldBatch := jobTaskItemDeleteBatchSize
	jobTaskItemDeleteBatchSize = 1
	defer func() { jobTaskItemDeleteBatchSize = oldBatch }()

	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(20*time.Millisecond, cancel)
	defer timer.Stop()

	err := deleteJobTaskItemsInBatches(ctx, "taskId=?", int64(1))
	if err == nil || ctx.Err() == nil {
		t.Fatalf("error = %v, ctx.Err() = %v; want the cleanup to stop when the context is cancelled", err, ctx.Err())
	}
	remaining := mustCount(t, testDB, "SELECT COUNT(*) FROM job_task_item")
	if remaining == 0 || remaining >= 3000 {
		t.Fatalf("remaining rows = %d, want a partial deletion stopped mid-way", remaining)
	}
}
