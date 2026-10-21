package service

import (
	"context"
	"log"
	"testing"
	"time"
)

// stubRetentionCleanup replaces the cleanup entry point so these lifecycle tests
// never open the real database (the real cleanup uses GetDB, which would
// initialize the on-disk database from a unit test).
func stubRetentionCleanup(t *testing.T, ran chan<- struct{}) {
	t.Helper()
	old := cleanupExpiredTasksRun
	cleanupExpiredTasksRun = func(ctx context.Context, logger *log.Logger, taskSaveDays int, now time.Time) {
		if ran == nil {
			return
		}
		select {
		case ran <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() { cleanupExpiredTasksRun = old })
}

// Saving system config used to run the retention cleanup inline, so a large
// history made the request appear to hang. The async entry point must return
// without waiting for the cleanup to finish.
func TestStartTaskRetentionCleanupAsyncReturnsImmediately(t *testing.T) {
	waitForIdleRetention(t)
	ran := make(chan struct{}, 1)
	stubRetentionCleanup(t, ran)

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
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("the started cleanup never ran")
	}
	waitForIdleRetention(t)
}

// Two concurrent cleanups would contend on the same rows and could both decide
// to VACUUM, so a second call while one is running is a no-op.
func TestStartTaskRetentionCleanupAsyncIsSingleFlight(t *testing.T) {
	waitForIdleRetention(t)
	stubRetentionCleanup(t, nil)

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
