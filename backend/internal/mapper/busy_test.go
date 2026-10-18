package mapper

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// busyCodeError mimics modernc.org/sqlite's *Error.
type busyCodeError struct{ code int }

func (e busyCodeError) Error() string { return fmt.Sprintf("sqlite error code %d", e.code) }
func (e busyCodeError) Code() int     { return e.code }

func withFastWriteRetry(t *testing.T, attempts int) {
	t.Helper()
	old := writeBusyRetryDelays
	writeBusyRetryDelays = make([]time.Duration, attempts)
	for i := range writeBusyRetryDelays {
		writeBusyRetryDelays[i] = time.Millisecond
	}
	t.Cleanup(func() { writeBusyRetryDelays = old })
}

func TestIsBusyErrorRecognizesLockContention(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("database is locked (5) (SQLITE_BUSY)"), true},
		{busyCodeError{5 | 2<<8}, true}, // SQLITE_BUSY_SNAPSHOT
		{busyCodeError{6}, true},        // SQLITE_LOCKED
		{fmt.Errorf("save: %w", busyCodeError{5}), true},
		{busyCodeError{19}, false}, // SQLITE_CONSTRAINT
		{errors.New("sql: database is closed"), false},
	}
	for _, tc := range cases {
		if got := IsBusyError(tc.err); got != tc.want {
			t.Errorf("IsBusyError(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestRetryOnBusyRetriesOnlyContention(t *testing.T) {
	withFastWriteRetry(t, 2)

	calls := 0
	err := retryOnBusy(func() error {
		calls++
		if calls < 3 {
			return busyCodeError{5}
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("busy: err=%v calls=%d, want success on the 3rd call", err, calls)
	}

	calls = 0
	ioErr := errors.New("disk I/O error")
	if err := retryOnBusy(func() error { calls++; return ioErr }); !errors.Is(err, ioErr) || calls != 1 {
		t.Fatalf("non-busy: err=%v calls=%d, want the error after one call", err, calls)
	}
}

func TestSQLiteDSNUsesImmediateTransactions(t *testing.T) {
	dsn := sqliteDSN("/tmp/x.db")
	if !strings.Contains(dsn, "_txlock=immediate") {
		t.Fatalf("dsn %q lacks _txlock=immediate", dsn)
	}
	if !strings.Contains(dsn, fmt.Sprintf("busy_timeout%%28%d%%29", sqliteBusyTimeoutMS)) {
		t.Fatalf("dsn %q lacks busy_timeout(%d)", dsn, sqliteBusyTimeoutMS)
	}
}

// openFileDB opens a file database through sqliteDSN with a two-connection pool.
func openFileDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	handle, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	t.Cleanup(func() { handle.Close() })
	handle.SetMaxOpenConns(2)
	if _, err := handle.Exec("CREATE TABLE IF NOT EXISTS t(id integer primary key, v integer)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	return handle
}

// A deferred transaction took no lock at BEGIN; a read-then-write transaction
// could then be refused with SQLITE_BUSY_SNAPSHOT without waiting at all.
// withTx must hold the write lock from its first statement.
func TestWithTxTakesWriteLockAtBegin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock.db")
	testDB := openFileDB(t, path)
	restore := SetDBForTest(testDB)
	defer restore()

	// A second handle that never waits for the lock.
	other, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatalf("open other: %v", err)
	}
	defer other.Close()

	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- runTx(nil, func(tx *sql.Tx) error {
			// Only a read: the lock must already be held.
			var n int
			if err := tx.QueryRow("SELECT COUNT(*) FROM t").Scan(&n); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	_, writeErr := other.Exec("INSERT INTO t(v) VALUES (1)")
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("runTx() error: %v", err)
	}
	if !IsBusyError(writeErr) {
		t.Fatalf("write during withTx = %v, want SQLITE_BUSY: the transaction did not take the write lock at BEGIN", writeErr)
	}
}

// Read-only transactions must not queue for the write lock.
func TestWithReadTxDoesNotWaitForWriter(t *testing.T) {
	testDB := openFileDB(t, filepath.Join(t.TempDir(), "read.db"))
	restore := SetDBForTest(testDB)
	defer restore()

	writer, err := testDB.Begin()
	if err != nil {
		t.Fatalf("begin writer: %v", err)
	}
	defer writer.Rollback()
	if _, err := writer.Exec("INSERT INTO t(v) VALUES (1)"); err != nil {
		t.Fatalf("writer insert: %v", err)
	}

	start := time.Now()
	var n int
	if err := withReadTx(func(tx *sql.Tx) error {
		return tx.QueryRow("SELECT COUNT(*) FROM t").Scan(&n)
	}); err != nil {
		t.Fatalf("withReadTx() error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("read transaction waited %v behind the writer", elapsed)
	}
	if n != 0 {
		t.Fatalf("read saw %d uncommitted rows, want 0", n)
	}
}

// A write arriving while another connection holds the lock waits for it
// (busy_timeout) instead of failing with "database is locked".
func TestExecuteUpdateWaitsForWriteLock(t *testing.T) {
	testDB := openFileDB(t, filepath.Join(t.TempDir(), "wait.db"))
	restore := SetDBForTest(testDB)
	defer restore()

	writer, err := testDB.Begin()
	if err != nil {
		t.Fatalf("begin writer: %v", err)
	}
	if _, err := writer.Exec("INSERT INTO t(v) VALUES (1)"); err != nil {
		t.Fatalf("writer insert: %v", err)
	}
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = writer.Commit()
	}()

	if err := ExecuteUpdate("INSERT INTO t(v) VALUES (2)"); err != nil {
		t.Fatalf("ExecuteUpdate() while locked = %v, want it to wait and succeed", err)
	}
	if n := countRows(t, testDB, "SELECT COUNT(*) FROM t"); n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
}

func newDeleteTestDB(t *testing.T) *sql.DB {
	t.Helper()
	testDB := newRetentionTestDB(t, ":memory:")
	if _, err := testDB.Exec("CREATE TABLE job(id integer primary key, remark text)"); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := testDB.Exec("INSERT INTO job(id, remark) VALUES (1, 'a'), (2, 'b')"); err != nil {
		t.Fatalf("insert job: %v", err)
	}
	insertTaskWithItems(t, testDB, 1, 100, 2, 23)
	insertTaskWithItems(t, testDB, 2, 100, 2, 15)
	// Task 3 belongs to job 2 and must survive.
	insertTaskWithItems(t, testDB, 3, 100, 2, 4)
	if _, err := testDB.Exec("UPDATE job_task SET jobId=2 WHERE id=3"); err != nil {
		t.Fatalf("move task 3: %v", err)
	}
	return testDB
}

func withSmallDeleteBatch(t *testing.T) {
	t.Helper()
	old := jobTaskItemDeleteBatchSize
	jobTaskItemDeleteBatchSize = 5
	t.Cleanup(func() { jobTaskItemDeleteBatchSize = old })
}

func TestDeleteJobRemovesItemsInBatches(t *testing.T) {
	testDB := newDeleteTestDB(t)
	restore := SetDBForTest(testDB)
	defer restore()
	withSmallDeleteBatch(t)

	if err := DeleteJob(1); err != nil {
		t.Fatalf("DeleteJob() error: %v", err)
	}
	if n := countRows(t, testDB, "SELECT COUNT(*) FROM job WHERE id=1"); n != 0 {
		t.Fatalf("job rows = %d, want 0", n)
	}
	if n := countRows(t, testDB, "SELECT COUNT(*) FROM job_task WHERE jobId=1"); n != 0 {
		t.Fatalf("task rows = %d, want 0", n)
	}
	if n := countRows(t, testDB, "SELECT COUNT(*) FROM job_task_item WHERE taskId IN (1, 2)"); n != 0 {
		t.Fatalf("item rows = %d, want 0", n)
	}
	if n := countRows(t, testDB, "SELECT COUNT(*) FROM job_task_item WHERE taskId=3"); n != 4 {
		t.Fatalf("items of the other job = %d, want 4", n)
	}
	if n := countRows(t, testDB, "SELECT COUNT(*) FROM job_task_item_fts WHERE job_task_item_fts MATCH 'file'"); n != 4 {
		t.Fatalf("fts rows = %d, want only the other job's 4", n)
	}
}

func TestDeleteJobTaskByTaskIDRemovesItemsInBatches(t *testing.T) {
	testDB := newDeleteTestDB(t)
	restore := SetDBForTest(testDB)
	defer restore()
	withSmallDeleteBatch(t)

	if err := DeleteJobTaskByTaskID(1); err != nil {
		t.Fatalf("DeleteJobTaskByTaskID() error: %v", err)
	}
	if n := countRows(t, testDB, "SELECT COUNT(*) FROM job_task WHERE id=1"); n != 0 {
		t.Fatalf("task rows = %d, want 0", n)
	}
	if n := countRows(t, testDB, "SELECT COUNT(*) FROM job_task_item WHERE taskId=1"); n != 0 {
		t.Fatalf("item rows = %d, want 0", n)
	}
	if n := countRows(t, testDB, "SELECT COUNT(*) FROM job_task_item WHERE taskId=2"); n != 15 {
		t.Fatalf("items of task 2 = %d, want 15", n)
	}
}

func TestDeleteJobTaskItemsInBatchesStopsWhenCancelled(t *testing.T) {
	testDB := newDeleteTestDB(t)
	restore := SetDBForTest(testDB)
	defer restore()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := deleteJobTaskItemsInBatches(ctx, "taskId=?", int64(1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if n := countRows(t, testDB, "SELECT COUNT(*) FROM job_task_item WHERE taskId=1"); n != 23 {
		t.Fatalf("items = %d, want untouched 23", n)
	}
}
