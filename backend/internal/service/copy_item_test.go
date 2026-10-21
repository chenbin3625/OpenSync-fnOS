package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"opensync/internal/config"
)

type copyItemTestRuntime struct{}

func (copyItemTestRuntime) context() context.Context {
	return context.Background()
}

func (copyItemTestRuntime) cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

func (copyItemTestRuntime) isBreak() bool {
	return false
}

func (copyItemTestRuntime) waitForBreak(time.Duration) bool {
	return true
}

func (copyItemTestRuntime) jobConfig() map[string]interface{} {
	return map[string]interface{}{}
}

func (copyItemTestRuntime) lastWatchingUnix() int64 {
	return 0
}

func (copyItemTestRuntime) finishCopyItem(*CopyItem) {}

func (copyItemTestRuntime) waitForRemoteCopyCompletion(*CopyItem) {}

func (copyItemTestRuntime) notifyProgressChange() {}

type copyItemTestClient struct {
	copyCalls     int
	moveCalls     int
	deleteCalls   int
	cancelCalls   int
	cancelErr     error
	fileExists    bool
	fileExistsErr error
	existsCalls   int
	taskInfoCalls int
	taskInfoFn    func(call int) (map[string]interface{}, error)
	// transferErr is returned by the first copy/move call only.
	transferErr error
	// transferID is the remote task id the copy/move call reports ("" means
	// AList completed synchronously and returned no task).
	transferID string
	undone      []map[string]interface{}
	undoneErr   error
	undoneCalls int
	// existsFn overrides fileExists per probed directory when set.
	existsFn func(dir, name string) (bool, error)
	// stat is returned by statCopyItemTestClient.FileStatContext.
	stat *FileStat
}

func (c *copyItemTestClient) transferResult() (string, error) {
	if err := c.transferErr; err != nil {
		c.transferErr = nil
		return "", err
	}
	return c.transferID, nil
}

func (c *copyItemTestClient) CopyFileContext(context.Context, string, string, string) (string, error) {
	c.copyCalls++
	return c.transferResult()
}

func (c *copyItemTestClient) MoveFileContext(context.Context, string, string, string) (string, error) {
	c.moveCalls++
	return c.transferResult()
}

func (c *copyItemTestClient) TaskCancelContext(context.Context, string, taskItemType) error {
	c.cancelCalls++
	return c.cancelErr
}

func (c *copyItemTestClient) TaskDeleteContext(context.Context, string, taskItemType) error {
	c.deleteCalls++
	return nil
}

func (c *copyItemTestClient) TaskInfoContext(context.Context, string, taskItemType) (map[string]interface{}, error) {
	c.taskInfoCalls++
	if c.taskInfoFn != nil {
		return c.taskInfoFn(c.taskInfoCalls)
	}
	return map[string]interface{}{"state": taskStatusSuccess.Int(), "progress": 100}, nil
}

func (c *copyItemTestClient) TaskUndoneListContext(context.Context, taskItemType) ([]map[string]interface{}, error) {
	c.undoneCalls++
	if c.undoneErr != nil {
		return nil, c.undoneErr
	}
	if c.undone != nil {
		return c.undone, nil
	}
	return []map[string]interface{}{}, nil
}

func (c *copyItemTestClient) DeleteFileContext(context.Context, string, []string, int) error {
	c.deleteCalls++
	return nil
}

func (c *copyItemTestClient) FileExistsContext(_ context.Context, dir, name string) (bool, error) {
	c.existsCalls++
	if c.existsFn != nil {
		return c.existsFn(dir, name)
	}
	return c.fileExists, c.fileExistsErr
}

// statCopyItemTestClient adds destination metadata to copyItemTestClient.
type statCopyItemTestClient struct {
	*copyItemTestClient
	// statFn overrides stat per probed directory when set; stat is the fallback
	// for tests that do not need to distinguish source from destination.
	statFn func(dir, name string) (FileStat, error)
}

func (c statCopyItemTestClient) FileStatContext(_ context.Context, dir, name string) (FileStat, error) {
	c.existsCalls++
	if c.statFn != nil {
		return c.statFn(dir, name)
	}
	return *c.stat, nil
}

// pinCopyTestDeps makes copy-item tests hermetic: retry counts come from the
// process config (runtimeTaskLimits), so a developer's config.ini with
// max_retries=0 changed how many attempts these tests observe.
func pinCopyTestDeps(t *testing.T) {
	t.Helper()
	oldConfig := config.GetConfig()
	config.SetConfigForTest(&config.Config{Server: config.ServerConfig{
		CopyConcurrency: config.DefaultCopyConcurrency,
		ScanConcurrency: config.DefaultScanConcurrency,
		MaxRetries:      config.DefaultMaxRetries,
	}})
	oldJobDeps := *jobDeps
	deps := oldJobDeps
	deps.CopyRetryDelay = func(int) time.Duration { return 0 }
	SetJobDepsForTest(&deps)
	t.Cleanup(func() {
		SetJobDepsForTest(&oldJobDeps)
		config.SetConfigForTest(oldConfig)
	})
}

func TestCopyItemUsesMoveAPIForMoveItems(t *testing.T) {
	// The stub returns no remote task id, so DoIt verifies the destination;
	// fileExists=true represents the synchronous-completion path.
	client := &copyItemTestClient{fileExists: true}
	item := newCopyItem(copyItemTestRuntime{}, client, "/src", "/dst", "file.txt", int64(1), taskItemTypeMove)

	item.DoIt()

	if client.moveCalls != 1 {
		t.Fatalf("moveCalls = %d, want 1", client.moveCalls)
	}
	if client.copyCalls != 0 {
		t.Fatalf("copyCalls = %d, want 0 for move item", client.copyCalls)
	}
	if client.deleteCalls != 0 {
		t.Fatalf("deleteCalls = %d, want 0 because AList move already removes source", client.deleteCalls)
	}
}

type breakingCopyItemTestRuntime struct {
	copyItemTestRuntime
}

func (breakingCopyItemTestRuntime) isBreak() bool {
	return true
}

func TestCopyItemKeepsStoppedStatusWhenCancelFails(t *testing.T) {
	client := &copyItemTestClient{cancelErr: errors.New("cancel failed")}
	item := newCopyItem(breakingCopyItemTestRuntime{}, client, "/src", "/dst", "file.txt", int64(1), taskItemTypeCopy)
	item.setTaskID("copy-task")

	item.stopRemoteTask(client, nil)

	if status := item.status(); status != taskStatusStopped {
		t.Fatalf("status = %d, want stopped", status)
	}
	if item.ErrMsg == nil || *item.ErrMsg != "cancel failed" {
		t.Fatalf("ErrMsg = %#v, want cancel failure recorded", item.ErrMsg)
	}
}

// errAmbiguousTransfer mimics a response-header timeout: no HTTP status, so
// the request may or may not have been executed by AList.
var errAmbiguousTransfer = errors.New("/api/fs/move request failed: net/http: timeout awaiting response headers")

func TestCopyItemAmbiguousMoveTreatedAsSuccessWhenSourceGoneAndDstPresent(t *testing.T) {
	client := &copyItemTestClient{
		transferErr: errAmbiguousTransfer,
		existsFn: func(dir, name string) (bool, error) {
			return dir == "/dst", nil
		},
	}
	item := newCopyItem(copyItemTestRuntime{}, client, "/src", "/dst", "file.txt", int64(1), taskItemTypeMove)

	item.DoIt()

	if client.moveCalls != 1 {
		t.Fatalf("moveCalls = %d, want 1 (no resubmit after the move already happened)", client.moveCalls)
	}
	if status := item.status(); status != taskStatusSuccess {
		t.Fatalf("status = %d, want success", status)
	}
}

func TestCopyItemAmbiguousMoveRetriedWhenSourceStillExists(t *testing.T) {
	pinCopyTestDeps(t)
	client := &copyItemTestClient{transferErr: errAmbiguousTransfer, fileExists: true}
	item := newCopyItem(copyItemTestRuntime{}, client, "/src", "/dst", "file.txt", int64(1), taskItemTypeMove)

	item.DoIt()

	if client.moveCalls != 2 {
		t.Fatalf("moveCalls = %d, want 2 (source still present, so retry)", client.moveCalls)
	}
}

func TestCopyItemAmbiguousCopyAdoptsMatchingUndoneTask(t *testing.T) {
	client := &copyItemTestClient{
		transferErr: errAmbiguousTransfer,
		undone: []map[string]interface{}{
			{"id": "other", "name": "copy [/src](/other.txt) to [/dst](/)"},
			{"id": "t-42", "name": "copy [/src](/file.txt) to [/dst](/)"},
		},
	}
	rt := &trackingCopyItemRuntime{}
	item := newCopyItem(rt, client, "/src", "/dst", "file.txt", int64(1), taskItemTypeCopy)

	item.DoIt()

	if client.copyCalls != 1 {
		t.Fatalf("copyCalls = %d, want 1 (existing server task adopted)", client.copyCalls)
	}
	if rt.watched != "t-42" {
		t.Fatalf("watched task = %q, want t-42", rt.watched)
	}
	if status := item.status(); status != taskStatusSuccess {
		t.Fatalf("status = %d, want success from the adopted task", status)
	}
}

func TestCopyItemAmbiguousCopyWithCompleteDestinationIsSuccess(t *testing.T) {
	base := &copyItemTestClient{
		transferErr: errAmbiguousTransfer,
		stat:        &FileStat{Exists: true, SizeKnown: true, Size: 2048, Modified: time.Now().Unix()},
	}
	item := newCopyItem(copyItemTestRuntime{}, statCopyItemTestClient{copyItemTestClient: base}, "/src", "/dst", "file.txt", int64(2048), taskItemTypeCopy)

	item.DoIt()

	if base.copyCalls != 1 {
		t.Fatalf("copyCalls = %d, want 1", base.copyCalls)
	}
	if status := item.status(); status != taskStatusSuccess {
		t.Fatalf("status = %d, want success", status)
	}
}

// An in-place edit keeps the size: the previous version already at the
// destination matches it, so only a destination written after the submission
// may count as this copy's result.
func TestCopyItemAmbiguousCopyWithSameSizeOldDestinationIsRetried(t *testing.T) {
	pinCopyTestDeps(t)
	oldModified := time.Now().Add(-time.Hour).Unix()
	base := &copyItemTestClient{
		transferErr: errAmbiguousTransfer,
	}
	client := statCopyItemTestClient{base, func(dir, name string) (FileStat, error) {
		if dir == "/src" {
			// The source changed, so its mtime differs from the stale
			// destination's: the metadata-mirror criterion must not accept it.
			return FileStat{Exists: true, SizeKnown: true, Size: 2048, Modified: time.Now().Unix()}, nil
		}
		return FileStat{Exists: true, SizeKnown: true, Size: 2048, Modified: oldModified}, nil
	}}
	item := newCopyItem(copyItemTestRuntime{}, client, "/src", "/dst", "file.txt", int64(2048), taskItemTypeCopy)

	item.DoIt()

	if base.copyCalls < 2 {
		t.Fatalf("copyCalls = %d, want the copy resubmitted", base.copyCalls)
	}
}

func TestCopyItemAmbiguousCopyWithStaleDestinationIsRetried(t *testing.T) {
	pinCopyTestDeps(t)
	// Overwrite:true: the old, smaller file is still at the destination.
	base := &copyItemTestClient{
		transferErr: errAmbiguousTransfer,
		stat:        &FileStat{Exists: true, SizeKnown: true, Size: 10},
	}
	item := newCopyItem(copyItemTestRuntime{}, statCopyItemTestClient{copyItemTestClient: base}, "/src", "/dst", "file.txt", int64(2048), taskItemTypeCopy)

	item.DoIt()

	// The stale file never satisfies the size check, so neither the ambiguous
	// attempt nor any synchronous retry may be reported as success.
	if status := item.status(); status != taskStatusFailed {
		t.Fatalf("status = %d, want failed (stale destination must not count as success)", status)
	}
	if want := runtimeTaskLimits().MaxRetries + 1; base.copyCalls != want {
		t.Fatalf("copyCalls = %d, want %d (every attempt retried)", base.copyCalls, want)
	}
	if base.undoneCalls != 1 {
		t.Fatalf("undoneCalls = %d, want 1 (only the ambiguous attempt reconciles)", base.undoneCalls)
	}
}

func TestCopyItemDefiniteErrorSkipsReconciliation(t *testing.T) {
	pinCopyTestDeps(t)
	client := &copyItemTestClient{
		transferErr: &alistStatusError{alistCode: 500, message: "storage error"},
		fileExists:  true,
	}
	item := newCopyItem(copyItemTestRuntime{}, client, "/src", "/dst", "file.txt", int64(1), taskItemTypeCopy)

	item.DoIt()

	if client.undoneCalls != 0 {
		t.Fatalf("undoneCalls = %d, want 0 for a definite AList error", client.undoneCalls)
	}
	if client.copyCalls != 2 {
		t.Fatalf("copyCalls = %d, want 2 (normal retry)", client.copyCalls)
	}
}

func TestMatchUndoneTaskRejectsAmbiguousOrUnknown(t *testing.T) {
	item := newCopyItem(copyItemTestRuntime{}, &copyItemTestClient{}, "/mnt/a/dir", "/mnt/b/out", "f.bin", int64(1), taskItemTypeCopy)
	cases := []struct {
		name  string
		tasks []map[string]interface{}
		want  string
	}{
		{"nested mount", []map[string]interface{}{{"id": "1", "name": "copy [/mnt/a](/dir/f.bin) to [/mnt/b](/out)"}}, "1"},
		{"different dst", []map[string]interface{}{{"id": "1", "name": "copy [/mnt/a](/dir/f.bin) to [/mnt/b](/other)"}}, ""},
		{"unknown format", []map[string]interface{}{{"id": "1", "name": "something"}}, ""},
		{"two matches", []map[string]interface{}{
			{"id": "1", "name": "copy [/mnt/a](/dir/f.bin) to [/mnt/b](/out)"},
			{"id": "2", "name": "copy [/mnt/a](/dir/f.bin) to [/mnt/b](/out)"},
		}, ""},
	}
	for _, tc := range cases {
		if got := item.matchUndoneTask(tc.tasks); got != tc.want {
			t.Fatalf("%s: matchUndoneTask = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestIsAmbiguousTransferError(t *testing.T) {
	if isAmbiguousTransferError(&alistStatusError{httpStatus: 502}) {
		t.Fatal("HTTP status error must be definite")
	}
	if isAmbiguousTransferError(context.Canceled) {
		t.Fatal("cancellation must not trigger reconciliation")
	}
	if !isAmbiguousTransferError(errAmbiguousTransfer) {
		t.Fatal("header timeout must be ambiguous")
	}
}

func TestCopyItemSetProgressKeepsStoppedTerminal(t *testing.T) {
	item := newCopyItem(copyItemTestRuntime{}, &copyItemTestClient{}, "/src", "/dst", "f", int64(1), taskItemTypeCopy)
	item.setStatus(taskStatusStopped)
	item.setProgress(taskStatusRunning, 50, nil)
	if status := item.status(); status != taskStatusStopped {
		t.Fatalf("status = %d, want stopped", status)
	}
	// DoIt's own retry path still resets explicitly.
	item.setRunning()
	if status := item.status(); status != taskStatusRunning {
		t.Fatalf("status = %d, want running after setRunning", status)
	}
}

type trackingCopyItemRuntime struct {
	copyItemTestRuntime
	watched string
}

func (r *trackingCopyItemRuntime) waitForRemoteCopyCompletion(ci *CopyItem) {
	r.watched = ci.taskID()
	ci.setProgress(taskStatusSuccess, 100, nil)
}

// A same-storage AList copy calls the driver's Copy, and server-side copies
// commonly preserve the source mtime. The destination then carries the
// source's timestamp, not the submission time; the combined criterion (dst
// metadata mirrors the source and the sizes match) must accept that as the
// result of this submission instead of resubmitting a duplicate copy.
func TestCopyItemAmbiguousCopyWithPreservedMtimeIsSuccess(t *testing.T) {
	pinCopyTestDeps(t)
	preserved := time.Now().Add(-time.Hour).Unix()
	base := &copyItemTestClient{transferErr: errAmbiguousTransfer}
	client := statCopyItemTestClient{base, func(dir, name string) (FileStat, error) {
		return FileStat{Exists: true, SizeKnown: true, Size: 2048, Modified: preserved}, nil
	}}
	item := newCopyItem(copyItemTestRuntime{}, client, "/src", "/dst", "file.txt", int64(2048), taskItemTypeCopy)

	item.DoIt()

	if base.copyCalls != 1 {
		t.Fatalf("copyCalls = %d, want 1 (the completed copy must not be resubmitted)", base.copyCalls)
	}
	if status := item.status(); status != taskStatusSuccess {
		t.Fatalf("status = %d, want success", status)
	}
}

// A probe that keeps erroring is not evidence that the file arrived. Treating
// it as success marked never-transferred files as 100% successes, and success
// is terminal so they were never retried.
func TestCopyItemSynchronousCopyProbeFailureFails(t *testing.T) {
	pinCopyTestDeps(t)
	client := &copyItemTestClient{fileExistsErr: errors.New("probe down")}
	item := newCopyItem(copyItemTestRuntime{}, client, "/src", "/dst", "file.txt", int64(1), taskItemTypeCopy)

	item.DoIt()

	if status := item.status(); status != taskStatusFailed {
		t.Fatalf("status = %d, want failed when the destination probe cannot verify the copy", status)
	}
	if item.ErrMsg == nil || !strings.Contains(*item.ErrMsg, "probe down") {
		t.Fatalf("ErrMsg = %#v, want the probe failure recorded", item.ErrMsg)
	}
}

type noopWaitRuntime struct{ copyItemTestRuntime }

// Leaves the item Running, mimicking the monitor being aborted between removing
// the watch and applying the stopped status.
func (noopWaitRuntime) waitForRemoteCopyCompletion(*CopyItem) {}

// Never finish a copy item in a non-terminal state: a persisted Running row is
// counted neither as success nor as failure by the task statistics.
func TestCopyItemNeverFinishesInRunningState(t *testing.T) {
	pinCopyTestDeps(t)
	client := &copyItemTestClient{transferID: "task-1"}
	item := newCopyItem(noopWaitRuntime{}, client, "/src", "/dst", "file.txt", int64(1), taskItemTypeCopy)

	item.DoIt()

	if status := item.status(); status != taskStatusStopped {
		t.Fatalf("status = %d, want stopped for an item whose monitor never applied a terminal state", status)
	}
}
