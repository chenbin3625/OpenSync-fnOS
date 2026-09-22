package service

import (
	"testing"
	"time"
)

// Saving system config used to run the retention cleanup inline, so a large
// history made the request appear to hang. The async entry point must return
// without waiting for the cleanup to finish.
func TestStartTaskRetentionCleanupAsyncReturnsImmediately(t *testing.T) {
	waitForIdleRetention(t)

	done := make(chan bool, 1)
	go func() { done <- StartTaskRetentionCleanupAsync() }()

	select {
	case started := <-done:
		if !started {
			t.Fatal("StartTaskRetentionCleanupAsync() = false, want it to start a cleanup")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StartTaskRetentionCleanupAsync() blocked instead of returning")
	}
	waitForIdleRetention(t)
}

// Two concurrent cleanups would contend on the same rows and could both decide
// to VACUUM, so a second call while one is running is a no-op.
func TestStartTaskRetentionCleanupAsyncIsSingleFlight(t *testing.T) {
	waitForIdleRetention(t)

	// Hold the flag as a running cleanup would, then confirm a caller is turned
	// away rather than starting a second pass.
	if !taskRetentionRunning.CompareAndSwap(false, true) {
		t.Fatal("retention flag was not idle")
	}
	if StartTaskRetentionCleanupAsync() {
		t.Error("StartTaskRetentionCleanupAsync() started a second concurrent cleanup")
	}
	taskRetentionRunning.Store(false)

	// Once idle again, a later call is accepted.
	if !StartTaskRetentionCleanupAsync() {
		t.Error("StartTaskRetentionCleanupAsync() refused to run after the previous one finished")
	}
	waitForIdleRetention(t)
}

func waitForIdleRetention(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !taskRetentionRunning.Load() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a retention cleanup is still running")
}
