package service

import (
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
