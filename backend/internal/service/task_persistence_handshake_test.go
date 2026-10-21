package service

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestP13FinalFlushWaitsForInFlightTimerFlush(t *testing.T) {
	var mu sync.Mutex
	entered := make(chan struct{})
	release := make(chan struct{})

	d := *jobDeps
	calls := 0

	d.PersistJobTaskItems = func(items []map[string]interface{}) error {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			close(entered)
			<-release
			return errors.New("simulated sqlite busy")
		}
		return nil
	}
	restore := SetJobDepsForTest(&d)
	defer restore()

	jt := &JobTask{TaskID: 7, Job: map[string]interface{}{}}
	jt.initRuntime()
	jt.appendFinish(JobTaskItem{TaskID: 7, FileName: "a.txt", Status: taskStatusSuccess})

	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("timer flush never started")
	}

	done := make(chan error, 1)
	go func() { done <- jt.persistRemainingTaskItems() }()

	select {
	case err := <-done:
		t.Fatalf("persistRemainingTaskItems returned err=%v before the in-flight write finished — items can still be lost", err)
	case <-time.After(500 * time.Millisecond):
		// correct: it is waiting on the in-flight write
	}

	close(release)
	err := <-done
	if err == nil {
		t.Fatal("final flush reported success even though the in-flight write failed")
	}
	t.Logf("final flush correctly surfaced the in-flight failure: %v", err)
}

// The final flush must close the debounce for good. A writer that outlived the
// executor (or a panic path) used to re-arm the timer and write to the database
// after the task, and — at shutdown — after ShutdownDB closed the handle.
func TestFinalFlushSealsDebounceTimer(t *testing.T) {
	var calls atomic.Int32
	d := *jobDeps
	d.PersistJobTaskItems = func([]map[string]interface{}) error {
		calls.Add(1)
		return nil
	}
	restore := SetJobDepsForTest(&d)
	defer restore()

	jt := &JobTask{TaskID: 7, Job: map[string]interface{}{}}
	jt.initRuntime()
	jt.appendFinish(JobTaskItem{TaskID: 7, FileName: "a.txt", Status: taskStatusSuccess})

	jt.persistFlushMu.Lock()
	armed := jt.persistFlushScheduled && jt.persistFlushTimer != nil
	jt.persistFlushMu.Unlock()
	if !armed {
		t.Fatal("appendFinish did not arm the debounced flush")
	}

	if err := jt.persistRemainingTaskItems(); err != nil {
		t.Fatalf("persistRemainingTaskItems() error = %v", err)
	}
	if !jt.persistFlushSealed() {
		t.Fatal("final flush did not seal the debounce")
	}

	// A late record is written synchronously and never arms a timer again.
	jt.appendFinish(JobTaskItem{TaskID: 7, FileName: "late.txt", Status: taskStatusFailed})
	if got := calls.Load(); got != 2 {
		t.Fatalf("persist calls = %d, want the late record written synchronously (2)", got)
	}
	jt.persistFlushMu.Lock()
	timer := jt.persistFlushTimer
	jt.persistFlushMu.Unlock()
	if timer != nil {
		t.Fatal("a debounce timer was armed after the final flush")
	}
	if !jt.persistFlushSealed() {
		t.Fatal("seal was lost after a late record")
	}

	// The taskSubmit defer calls this on every exit path; it must not re-open a
	// sealed task for another timer.
	jt.cancelPersistFlush()
	if !jt.persistFlushSealed() {
		t.Fatal("cancelPersistFlush re-opened the sealed task")
	}
}
