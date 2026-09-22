package service

import (
	"errors"
	"sync"
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
