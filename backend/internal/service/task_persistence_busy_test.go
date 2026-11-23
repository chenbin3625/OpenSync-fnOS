package service

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// busyCodeError mimics modernc.org/sqlite's *Error for SQLITE_BUSY.
type busyCodeError struct{ code int }

func (e busyCodeError) Error() string { return fmt.Sprintf("sqlite error code %d", e.code) }
func (e busyCodeError) Code() int     { return e.code }

func withFastBusyRetry(t *testing.T) {
	t.Helper()
	old := persistBusyRetryDelays
	persistBusyRetryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(func() { persistBusyRetryDelays = old })
}

func TestIsSQLiteBusyRecognizesLockContention(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("database is locked (5) (SQLITE_BUSY)"), true},
		{errors.New("database table is locked"), true},
		{busyCodeError{5}, true},
		{busyCodeError{5 | 2<<8}, true}, // SQLITE_BUSY_SNAPSHOT
		{busyCodeError{6}, true},        // SQLITE_LOCKED
		{fmt.Errorf("save: %w", busyCodeError{5}), true},
		{busyCodeError{19}, false}, // SQLITE_CONSTRAINT
		{errors.New("disk I/O error"), false},
		{errors.New("sql: database is closed"), false},
	}
	for _, tc := range cases {
		if got := isSQLiteBusy(tc.err); got != tc.want {
			t.Errorf("isSQLiteBusy(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// A write lock held past busy_timeout (history cleanup, checkpoint) used to
// abort the running sync on the first failed flush.
func TestFlushRetriesBusyDatabaseBeforeFailingTask(t *testing.T) {
	withFastBusyRetry(t)
	var calls atomic.Int32
	d := *jobDeps
	d.PersistJobTaskItems = func([]map[string]interface{}) error {
		if calls.Add(1) <= 2 {
			return errors.New("database is locked (5) (SQLITE_BUSY)")
		}
		return nil
	}
	restore := SetJobDepsForTest(&d)
	defer restore()

	jt := &JobTask{TaskID: 42}
	jt.initRuntime()
	jt.persistBuffer = []JobTaskItem{{TaskID: 42, FileName: "a.txt", Status: taskStatusSuccess}}

	if err := jt.flushPersistBuffer(); err != nil {
		t.Fatalf("flushPersistBuffer() error = %v, want the busy write retried to success", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("persist calls = %d, want 3", calls.Load())
	}
	if jt.isBreak() {
		t.Fatal("task was broken although the write eventually succeeded")
	}
	if err := jt.taskPersistenceError(); err != nil {
		t.Fatalf("persistence error recorded: %v", err)
	}
}

func TestFlushGivesUpAfterBusyRetriesAreExhausted(t *testing.T) {
	withFastBusyRetry(t)
	var calls atomic.Int32
	busy := busyCodeError{5}
	d := *jobDeps
	d.PersistJobTaskItems = func([]map[string]interface{}) error {
		calls.Add(1)
		return busy
	}
	restore := SetJobDepsForTest(&d)
	defer restore()

	jt := &JobTask{TaskID: 42}
	jt.initRuntime()
	jt.persistBuffer = []JobTaskItem{{TaskID: 42, FileName: "a.txt", Status: taskStatusSuccess}}

	if err := jt.flushPersistBuffer(); !errors.Is(err, busy) {
		t.Fatalf("flushPersistBuffer() error = %v, want the busy error", err)
	}
	if want := int32(len(persistBusyRetryDelays) + 1); calls.Load() != want {
		t.Fatalf("persist calls = %d, want %d", calls.Load(), want)
	}
	if !jt.isBreak() {
		t.Fatal("task kept running after the write failed for good")
	}
	jt.persistBufMu.Lock()
	buffered := len(jt.persistBuffer)
	jt.persistBufMu.Unlock()
	if buffered != 1 {
		t.Fatalf("persistBuffer len = %d, want the item requeued", buffered)
	}
}

func TestFlushDoesNotRetryNonBusyErrors(t *testing.T) {
	withFastBusyRetry(t)
	var calls atomic.Int32
	d := *jobDeps
	d.PersistJobTaskItems = func([]map[string]interface{}) error {
		calls.Add(1)
		return errors.New("disk I/O error")
	}
	restore := SetJobDepsForTest(&d)
	defer restore()

	jt := &JobTask{TaskID: 42}
	jt.initRuntime()
	jt.persistBuffer = []JobTaskItem{{TaskID: 42, FileName: "a.txt"}}

	if err := jt.flushPersistBuffer(); err == nil {
		t.Fatal("flushPersistBuffer() = nil, want the I/O error")
	}
	if calls.Load() != 1 {
		t.Fatalf("persist calls = %d, want 1", calls.Load())
	}
}

// A stopped task must not sit out the whole retry budget.
func TestFlushStopsRetryingWhenTaskIsStopped(t *testing.T) {
	old := persistBusyRetryDelays
	persistBusyRetryDelays = []time.Duration{time.Hour}
	defer func() { persistBusyRetryDelays = old }()

	entered := make(chan struct{}, 1)
	d := *jobDeps
	d.PersistJobTaskItems = func([]map[string]interface{}) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		return errors.New("database is locked")
	}
	restore := SetJobDepsForTest(&d)
	defer restore()

	jt := &JobTask{TaskID: 42}
	jt.initRuntime()
	jt.persistBuffer = []JobTaskItem{{TaskID: 42, FileName: "a.txt"}}

	done := make(chan error, 1)
	go func() { done <- jt.flushPersistBuffer() }()
	<-entered
	jt.requestBreak()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("flushPersistBuffer() = nil after stop, want the busy error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("flush kept waiting for the retry backoff after the task was stopped")
	}
}

// Stopping a task with a large queued backlog triggers markWaitingAsAborted
// (>persistBatchSize) on an already-broken/cancelled task. Transient SQLITE_BUSY
// must be retried in bounded chunks during persistRemainingTaskItems instead of
// latching PersistErr and turning a clean Stop into SystemFailed.
func TestStoppedMarkWaitingAsAbortedRecoversFromTransientBusy(t *testing.T) {
	withFastBusyRetry(t)
	var calls atomic.Int32
	var persisted []map[string]interface{}
	var maxBatch int
	d := *jobDeps
	d.PersistJobTaskItems = func(items []map[string]interface{}) error {
		if len(items) > maxBatch {
			maxBatch = len(items)
		}
		if calls.Add(1) <= 2 {
			return busyCodeError{5}
		}
		persisted = append(persisted, items...)
		return nil
	}
	restore := SetJobDepsForTest(&d)
	defer restore()

	const total = 250
	jt := &JobTask{TaskID: 99, Waiting: newCopyQueueWithCapacity(total)}
	jt.initRuntime()
	for i := 0; i < total; i++ {
		jt.Waiting.pushWait(context.Background(), &CopyItem{
			SrcPath:  "/src/",
			DstPath:  "/dst/",
			FileName: fmt.Sprintf("file-%d.txt", i),
			FileSize: int64(1),
		})
	}

	jt.requestBreak()
	jt.markWaitingAsAborted()

	if err := jt.persistRemainingTaskItems(); err != nil {
		t.Fatalf("persistRemainingTaskItems() error = %v, want nil after busy recovery", err)
	}
	if len(persisted) != total {
		t.Fatalf("persisted len = %d, want %d", len(persisted), total)
	}
	if maxBatch > persistBatchSize {
		t.Fatalf("maxBatch = %d, want <= %d", maxBatch, persistBatchSize)
	}
}

// If a mid-run flush hits SQLITE_BUSY and is cut short by task timeout
// (DeadlineExceeded), persistRemainingTaskItems saves the requeued items and
// clears the transient busy error without marking BreakFlag as a user stop.
func TestTimedOutTaskRecoversWhenPreFinalFlushHitBusy(t *testing.T) {
	withFastBusyRetry(t)
	var calls atomic.Int32
	var persisted []map[string]interface{}
	d := *jobDeps
	d.PersistJobTaskItems = func(items []map[string]interface{}) error {
		if calls.Add(1) == 1 {
			return busyCodeError{5}
		}
		persisted = append(persisted, items...)
		return nil
	}
	restore := SetJobDepsForTest(&d)
	defer restore()

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	jt := &JobTask{TaskID: 100, ctx: ctx, cancel: cancel}
	jt.initRuntime()
	jt.persistBuffer = []JobTaskItem{{TaskID: 100, FileName: "a.txt", Status: taskStatusStopped}}

	if err := jt.flushPersistBuffer(); err == nil {
		t.Fatal("flushPersistBuffer() = nil on busy + expired context, want busy error")
	}
	if jt.isBreak() {
		t.Fatal("timeout flush failure unexpectedly set BreakFlag")
	}

	if err := jt.persistRemainingTaskItems(); err != nil {
		t.Fatalf("persistRemainingTaskItems() error = %v, want nil after final flush recovery", err)
	}
	if jt.isBreak() {
		t.Fatal("persistRemainingTaskItems unexpectedly set BreakFlag")
	}
	if len(persisted) != 1 {
		t.Fatalf("persisted len = %d, want 1", len(persisted))
	}
}
