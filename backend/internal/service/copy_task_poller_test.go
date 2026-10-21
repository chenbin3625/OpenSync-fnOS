package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func newCopyMonitorTestJobTask(client copyItemClient) *JobTask {
	ctx, cancel := context.WithCancel(context.Background())
	return &JobTask{
		TaskID:                    1,
		copyMonitorClientOverride: client,
		ctx:                       ctx,
		cancel:                    cancel,
	}
}

func newCopyMonitorWatch(client copyItemClient, opts ...func(*CopyItem)) (*copyTaskMonitor, *copyTaskWatch) {
	jt := newCopyMonitorTestJobTask(client)
	item := newCopyItem(jt, client, "/src", "/dst", "file.txt", int64(1), taskItemTypeCopy)
	item.setTaskID("task-1")
	for _, opt := range opts {
		opt(item)
	}
	monitor := &copyTaskMonitor{
		jt:      jt,
		watches: make(map[string]*copyTaskWatch),
		stopCh:  make(chan struct{}),
	}
	watch := &copyTaskWatch{
		ci:       item,
		taskID:   "task-1",
		copyType: taskItemTypeCopy,
		done:     make(chan struct{}),
	}
	monitor.watches[monitor.watchKey(watch.taskID, watch.copyType)] = watch
	return monitor, watch
}

func TestCopyMonitorPollTaskInfo404MarksSuccessWhenDstExists(t *testing.T) {
	client := &copyItemTestClient{
		fileExists: true,
		taskInfoFn: func(int) (map[string]interface{}, error) {
			return nil, &alistStatusError{httpStatus: 404}
		},
	}
	monitor, watch := newCopyMonitorWatch(client)

	if !monitor.pollTaskInfo(watch) {
		t.Fatal("pollTaskInfo() = false, want true")
	}
	if status := watch.ci.status(); status != taskStatusSuccess {
		t.Fatalf("status = %d, want success after 404 with dst present", status)
	}
	if client.existsCalls != 1 {
		t.Fatalf("existsCalls = %d, want 1", client.existsCalls)
	}
}

func TestCopyMonitorPollTaskInfo404MarksFailedWhenDstMissing(t *testing.T) {
	client := &copyItemTestClient{
		fileExists: false,
		taskInfoFn: func(int) (map[string]interface{}, error) {
			return nil, &alistStatusError{httpStatus: 404}
		},
	}
	monitor, watch := newCopyMonitorWatch(client)

	if !monitor.pollTaskInfo(watch) {
		t.Fatal("pollTaskInfo() = false, want true")
	}
	if status := watch.ci.status(); status != taskStatusFailed {
		t.Fatalf("status = %d, want failed after 404 with dst missing", status)
	}
}

// fakePollClock is a manually advanced clock for the copy poller.
type fakePollClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakePollClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakePollClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func withFakePollClock(t *testing.T, window time.Duration) *fakePollClock {
	t.Helper()
	clock := &fakePollClock{now: time.Unix(1700000000, 0)}
	d := *jobDeps
	d.Now = clock.Now
	d.CopyPollErrorWindow = window
	d.CopyPollMaxBackoff = 10 * time.Second
	restore := SetJobDepsForTest(&d)
	t.Cleanup(restore)
	return clock
}

func TestCopyMonitorPollTaskInfoTransientErrorsRetryThenSucceed(t *testing.T) {
	clock := withFakePollClock(t, 2*time.Minute)
	client := &copyItemTestClient{
		taskInfoFn: func(call int) (map[string]interface{}, error) {
			if call < 10 {
				return nil, errors.New("connection refused")
			}
			return map[string]interface{}{"state": taskStatusSuccess.Int(), "progress": 100}, nil
		},
	}
	monitor, watch := newCopyMonitorWatch(client)

	// Nine failures spread over ~90s (an AList restart) stay inside the window.
	for call := 1; call < 10; call++ {
		if monitor.pollTaskInfo(watch) {
			t.Fatalf("pollTaskInfo call %d finished early", call)
		}
		clock.Advance(10 * time.Second)
	}
	if !monitor.pollTaskInfo(watch) {
		t.Fatal("pollTaskInfo() = false, want true after transient errors recovered")
	}
	if status := watch.ci.status(); status != taskStatusSuccess {
		t.Fatalf("status = %d, want success after transient blips recovered", status)
	}
	if !watch.firstErrAt.IsZero() || watch.transientErrs != 0 {
		t.Fatalf("transient state not reset after success: %v/%d", watch.firstErrAt, watch.transientErrs)
	}
}

func TestCopyMonitorPollTaskInfoTransientErrorsFailOnlyAfterWindow(t *testing.T) {
	clock := withFakePollClock(t, 2*time.Minute)
	client := &copyItemTestClient{
		taskInfoFn: func(int) (map[string]interface{}, error) {
			return nil, errors.New("connection refused")
		},
	}
	monitor, watch := newCopyMonitorWatch(client)

	// Many fast consecutive errors must not fail the item on their own.
	for call := 1; call <= 20; call++ {
		if monitor.pollTaskInfo(watch) {
			t.Fatalf("pollTaskInfo call %d failed the item inside the tolerance window", call)
		}
		clock.Advance(time.Second)
	}
	clock.Advance(2 * time.Minute)
	if !monitor.pollTaskInfo(watch) {
		t.Fatal("pollTaskInfo() = false, want true once errors outlasted the window")
	}
	if status := watch.ci.status(); status != taskStatusFailed {
		t.Fatalf("status = %d, want failed after the window elapsed", status)
	}
}

func TestTransientPollBackoffIsExponentialAndCapped(t *testing.T) {
	withFakePollClock(t, time.Minute)
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 10 * time.Second, 10 * time.Second}
	for i, w := range want {
		if got := transientPollBackoff(i + 1); got != w {
			t.Fatalf("transientPollBackoff(%d) = %v, want %v", i+1, got, w)
		}
	}
	if got := transientPollBackoff(1000); got != 10*time.Second {
		t.Fatalf("transientPollBackoff(1000) = %v, want cap", got)
	}
}

func TestCopyMonitorPollTaskInfoSetsBackoffDeadline(t *testing.T) {
	clock := withFakePollClock(t, time.Minute)
	client := &copyItemTestClient{
		taskInfoFn: func(int) (map[string]interface{}, error) {
			return nil, errors.New("connection reset by peer")
		},
	}
	monitor, watch := newCopyMonitorWatch(client)
	monitor.pollTaskInfo(watch)
	monitor.pollTaskInfo(watch)
	if want := clock.Now().Add(2 * time.Second); !watch.nextPollAt.Equal(want) {
		t.Fatalf("nextPollAt = %v, want %v", watch.nextPollAt, want)
	}
}

func TestCopyMonitorPollTaskInfo404RejectsSizeMismatch(t *testing.T) {
	// Overwrite:true copies leave the old version at the destination; a vanished
	// task must not be reported successful just because that old file exists.
	client := &copyItemTestClient{
		fileExists: true,
		stat:       &FileStat{Exists: true, SizeKnown: true, Size: 5},
		taskInfoFn: func(int) (map[string]interface{}, error) {
			return nil, &alistStatusError{httpStatus: 404}
		},
	}
	monitor, watch := newCopyMonitorWatch(statCopyItemTestClient{copyItemTestClient: client}, func(ci *CopyItem) { ci.FileSize = int64(1024) })

	if !monitor.pollTaskInfo(watch) {
		t.Fatal("pollTaskInfo() = false, want true")
	}
	if status := watch.ci.status(); status != taskStatusFailed {
		t.Fatalf("status = %d, want failed when destination size differs", status)
	}
}

func TestCopyMonitorPollTaskInfo404AcceptsMatchingSize(t *testing.T) {
	client := &copyItemTestClient{
		stat: &FileStat{Exists: true, SizeKnown: true, Size: 1024},
		taskInfoFn: func(int) (map[string]interface{}, error) {
			return nil, &alistStatusError{httpStatus: 404}
		},
	}
	monitor, watch := newCopyMonitorWatch(statCopyItemTestClient{copyItemTestClient: client}, func(ci *CopyItem) { ci.FileSize = int64(1024) })

	monitor.pollTaskInfo(watch)
	if status := watch.ci.status(); status != taskStatusSuccess {
		t.Fatalf("status = %d, want success when destination size matches", status)
	}
}

func TestCopyMonitorApplyTaskInfoDoesNotOverwriteStopped(t *testing.T) {
	client := &copyItemTestClient{}
	monitor, watch := newCopyMonitorWatch(client)
	watch.ci.setStatus(taskStatusStopped)

	monitor.applyTaskInfo(watch, map[string]interface{}{"state": 2, "progress": 100})

	if status := watch.ci.status(); status != taskStatusStopped {
		t.Fatalf("status = %d, want stopped to stay terminal", status)
	}
}

func TestCopyMonitorAbortRacesWithApplyTaskInfo(t *testing.T) {
	for i := 0; i < 50; i++ {
		client := &copyItemTestClient{}
		monitor, watch := newCopyMonitorWatch(client)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			monitor.abortWatch(watch, nil)
		}()
		go func() {
			defer wg.Done()
			monitor.applyTaskInfo(watch, map[string]interface{}{"state": 1, "progress": 50})
		}()
		wg.Wait()
		// After abort completed, any later poll result must not revive it.
		monitor.applyTaskInfo(watch, map[string]interface{}{"state": 2, "progress": 100})
		if status := watch.ci.status(); status != taskStatusStopped {
			t.Fatalf("iteration %d: status = %d, want stopped", i, status)
		}
	}
}

func TestCopyMonitorAbortWatchKeepsStoppedStatusWhenCancelFails(t *testing.T) {
	client := &copyItemTestClient{cancelErr: errors.New("cancel failed")}
	monitor, watch := newCopyMonitorWatch(client)

	monitor.abortWatch(watch, nil)

	if status := watch.ci.status(); status != taskStatusStopped {
		t.Fatalf("status = %d, want stopped", status)
	}
	if watch.ci.ErrMsg == nil || *watch.ci.ErrMsg != "cancel failed" {
		t.Fatalf("ErrMsg = %#v, want cancel failure recorded", watch.ci.ErrMsg)
	}
}

func TestCopyMonitorAbortAllStopsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := &copyItemTestClient{}
	jt := &JobTask{
		TaskID:                    1,
		copyMonitorClientOverride: client,
		ctx:                       ctx,
		cancel:                    cancel,
	}
	item := newCopyItem(jt, client, "/src", "/dst", "file.txt", int64(1), taskItemTypeCopy)
	item.setTaskID("copy-task")
	monitor := &copyTaskMonitor{
		jt:      jt,
		watches: make(map[string]*copyTaskWatch),
		stopCh:  make(chan struct{}),
	}
	watch := &copyTaskWatch{
		ci:       item,
		taskID:   "copy-task",
		copyType: taskItemTypeCopy,
		done:     make(chan struct{}),
	}
	monitor.watches[monitor.watchKey(watch.taskID, watch.copyType)] = watch

	cancel()
	monitor.abortAll(ctx.Err())

	if status := item.status(); status != taskStatusStopped {
		t.Fatalf("status = %d, want stopped", status)
	}
	if client.cancelCalls != 1 || client.deleteCalls != 1 {
		t.Fatalf("cancel/delete calls = %d/%d, want 1/1", client.cancelCalls, client.deleteCalls)
	}
}

type blockingDeleteClient struct {
	*copyItemTestClient
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockingDeleteClient) TaskDeleteContext(ctx context.Context, _ string, _ taskItemType) error {
	c.once.Do(func() { close(c.started) })
	select {
	case <-c.release:
	case <-ctx.Done():
	}
	return nil
}

// finishWatch used to close the watch's done channel only after the remote task
// deletion returned, so a finished copy kept its executor slot (and blocked a
// copy goroutine) for up to the 30s cleanup timeout. The cleanup goroutine is
// still tracked so stop() waits for it.
func TestCopyMonitorFinishWatchUnblocksBeforeRemoteDelete(t *testing.T) {
	client := &blockingDeleteClient{
		copyItemTestClient: &copyItemTestClient{},
		started:            make(chan struct{}),
		release:            make(chan struct{}),
	}
	monitor, watch := newCopyMonitorWatch(client)

	monitor.finishWatch(watch)

	select {
	case <-watch.done:
	case <-time.After(2 * time.Second):
		t.Fatal("watch.done stayed open until the remote task deletion finished")
	}
	select {
	case <-client.started:
	case <-time.After(2 * time.Second):
		t.Fatal("remote task deletion was not started")
	}

	stopped := make(chan struct{})
	go func() {
		monitor.stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("stop() returned while the remote task deletion was still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(client.release)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stop() did not return after the remote task deletion finished")
	}
}

// The mapping is hand-written against an external enum (tache.State), so pin
// every value up to the retry states: a silent default change would make a
// terminal state look like Running and poll forever.
func TestMapAlistTaskState(t *testing.T) {
	cases := map[int]taskStatus{
		0:  taskStatusWaiting,  // pending
		1:  taskStatusRunning,  // running
		2:  taskStatusSuccess,  // succeeded
		3:  taskStatusRunning,  // canceling
		4:  taskStatusStopped,  // canceled
		5:  taskStatusRunning,  // errored (will retry)
		6:  taskStatusRunning,  // failing (hook running)
		7:  taskStatusFailed,   // failed
		8:  taskStatusRunning,  // waiting retry
		9:  taskStatusRunning,  // before retry
		42: taskStatusRunning,  // unknown state keeps polling
	}
	for state, want := range cases {
		if got := mapAlistTaskState(state); got != want {
			t.Errorf("mapAlistTaskState(%d) = %d, want %d", state, got, want)
		}
	}
}
