package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestAnyJobTaskRunningReflectsBusyClients(t *testing.T) {
	jobClientListMu.Lock()
	previous := jobClientList
	jobClientList = map[int64]*JobClient{1: {JobID: 1}, 2: {JobID: 2}}
	jobClientListMu.Unlock()
	defer func() {
		jobClientListMu.Lock()
		jobClientList = previous
		jobClientListMu.Unlock()
	}()

	if anyJobTaskRunning() {
		t.Fatal("anyJobTaskRunning() = true with only idle clients")
	}
	jobClientListMu.RLock()
	busy := jobClientList[2]
	jobClientListMu.RUnlock()
	if !busy.tryMarkDoing() {
		t.Fatal("tryMarkDoing() failed on an idle client")
	}
	if !anyJobTaskRunning() {
		t.Fatal("anyJobTaskRunning() = false while a client is running")
	}
	busy.markDone()
	if anyJobTaskRunning() {
		t.Fatal("anyJobTaskRunning() = true after the client finished")
	}
}

// Shutdown closes the database right after ShutdownJobs, so a cleanup still in
// flight has to be cancelled and waited for first, and none may start after.
func TestStopTaskRetentionCleanupCancelsAndWaitsForRunningCleanup(t *testing.T) {
	waitForIdleRetention(t)
	defer resetTaskRetentionCleanupForTest()

	// Stand in for a cleanup blocked mid-batch: it holds the running flag and
	// the wait group and returns only when its context is cancelled.
	if !taskRetentionRunning.CompareAndSwap(false, true) {
		t.Fatal("retention flag was not idle")
	}
	taskRetentionLifecycleMu.Lock()
	taskRetentionWG.Add(1)
	ctx := taskRetentionCtx
	taskRetentionLifecycleMu.Unlock()
	var returned atomic.Bool
	go func() {
		defer taskRetentionWG.Done()
		defer taskRetentionRunning.Store(false)
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond)
		returned.Store(true)
	}()

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !stopTaskRetentionCleanup(stopCtx) {
		t.Fatal("stopTaskRetentionCleanup() timed out")
	}
	if !returned.Load() {
		t.Fatal("stopTaskRetentionCleanup() returned before the cleanup left")
	}
	if StartTaskRetentionCleanupAsync() {
		t.Fatal("a cleanup started after shutdown began")
	}
}

func TestStopTaskRetentionCleanupIsBoundedByDeadline(t *testing.T) {
	waitForIdleRetention(t)
	defer resetTaskRetentionCleanupForTest()

	release := make(chan struct{})
	taskRetentionLifecycleMu.Lock()
	taskRetentionWG.Add(1)
	taskRetentionLifecycleMu.Unlock()
	go func() {
		defer taskRetentionWG.Done()
		<-release // ignores cancellation, like a wedged statement
	}()
	defer close(release)

	stopCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if stopTaskRetentionCleanup(stopCtx) {
		t.Fatal("stopTaskRetentionCleanup() reported success while the cleanup was still running")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("stopTaskRetentionCleanup() took %s, want it bounded by the deadline", elapsed)
	}
}
