package service

import (
	"context"
	"errors"
	"fmt"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
)

// CopyItem represents a single file copy operation
type copyItemRuntime interface {
	context() context.Context
	cleanupContext() (context.Context, context.CancelFunc)
	isBreak() bool
	waitForBreak(time.Duration) bool
	jobConfig() map[string]interface{}
	lastWatchingUnix() int64
	finishCopyItem(*CopyItem)
	waitForRemoteCopyCompletion(*CopyItem)
	notifyProgressChange()
}

type copyItemClient interface {
	CopyFileContext(context.Context, string, string, string) (string, error)
	MoveFileContext(context.Context, string, string, string) (string, error)
	TaskCancelContext(context.Context, string, taskItemType) error
	TaskDeleteContext(context.Context, string, taskItemType) error
	TaskInfoContext(context.Context, string, taskItemType) (map[string]interface{}, error)
	TaskUndoneListContext(context.Context, taskItemType) ([]map[string]interface{}, error)
	DeleteFileContext(context.Context, string, []string, int) error
	FileExistsContext(context.Context, string, string) (bool, error)
}

// copyItemStatClient is optionally implemented by clients that can return
// destination metadata (AlistClient via /api/fs/get). It is kept out of
// copyItemClient so simple fakes only need the existence probe.
type copyItemStatClient interface {
	FileStatContext(context.Context, string, string) (FileStat, error)
}

// maxTransientPollErrors bounds the destination probes confirmSynchronousCopy
// makes when the probe itself keeps erroring.
const maxTransientPollErrors = 3

type CopyItem struct {
	mu          sync.RWMutex
	SrcPath     string
	DstPath     string
	FileName    string
	FileSize    interface{}
	CopyType    taskItemType
	IsPath      taskItemObject
	AlistTaskID string
	Status      taskStatus
	Progress    float64
	ErrMsg      *string
	CreateTime  int64
	DoingKey    int64

	runtime copyItemRuntime
	client  copyItemClient
}

func newCopyItem(runtime copyItemRuntime, client copyItemClient, srcPath, dstPath, fileName string, fileSize interface{}, copyType taskItemType) *CopyItem {
	return &CopyItem{
		SrcPath:    srcPath,
		DstPath:    dstPath,
		FileName:   fileName,
		FileSize:   fileSize,
		CopyType:   copyType,
		Status:     taskStatusWaiting,
		Progress:   0,
		CreateTime: time.Now().Unix(),
		runtime:    runtime,
		client:     client,
	}
}

func (ci *CopyItem) copyRuntime() copyItemRuntime {
	return ci.runtime
}

func (ci *CopyItem) copyClient() copyItemClient {
	return ci.client
}

func (ci *CopyItem) setStatus(status taskStatus) {
	ci.mu.Lock()
	ci.Status = status
	ci.mu.Unlock()
}

func (ci *CopyItem) setTaskID(taskID string) {
	ci.mu.Lock()
	ci.AlistTaskID = taskID
	ci.mu.Unlock()
}

func (ci *CopyItem) setFailure(err error) {
	errMsg := err.Error()
	ci.mu.Lock()
	ci.Status = taskStatusFailed
	ci.Progress = 0
	ci.ErrMsg = &errMsg
	ci.mu.Unlock()
}

func (ci *CopyItem) setRunning() {
	ci.mu.Lock()
	ci.Status = taskStatusRunning
	ci.Progress = 0
	ci.ErrMsg = nil
	ci.AlistTaskID = ""
	ci.mu.Unlock()
}

func (ci *CopyItem) setRetrying(err error) {
	errMsg := err.Error()
	ci.mu.Lock()
	ci.Status = taskStatusRetrying
	ci.Progress = 0
	ci.ErrMsg = &errMsg
	ci.AlistTaskID = ""
	ci.mu.Unlock()
}

func (ci *CopyItem) setProgress(status taskStatus, progress float64, errMsg *string) {
	ci.mu.Lock()
	if ci.Status == taskStatusStopped && status != taskStatusStopped {
		// Stopped is terminal for a watch: abortWatch (user stop / task
		// cancel) can race with the monitor loop applying a poll result it
		// fetched just before. Letting that stale Running/Success overwrite
		// Stopped would report a cancelled transfer as done. DoIt resets the
		// status through setRunning/setRetrying, not through here.
		ci.mu.Unlock()
		return
	}
	ci.Status = status
	ci.Progress = progress
	ci.ErrMsg = errMsg
	ci.mu.Unlock()
	if runtime := ci.copyRuntime(); runtime != nil {
		runtime.notifyProgressChange()
	}
}

func (ci *CopyItem) status() taskStatus {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	return ci.Status
}

func (ci *CopyItem) taskID() string {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	return ci.AlistTaskID
}

func (ci *CopyItem) progress() float64 {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	return ci.Progress
}

func (ci *CopyItem) countableWaitSize() int64 {
	if ci.CopyType == taskItemTypeDelete || ci.FileSize == nil {
		return 0
	}
	return util.ToInt64(ci.FileSize)
}

func (ci *CopyItem) toStreamItem() streamDoingItem {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	return streamDoingItem{
		AlistTaskID: ci.AlistTaskID,
		FileName:    ci.FileName,
		SrcPath:     ci.SrcPath,
		DstPath:     ci.DstPath,
		FileSize:    util.ToInt64(ci.FileSize),
		Type:        ci.CopyType.Int(),
		Status:      ci.Status.Int(),
		Progress:    ci.Progress,
		CreateTime:  ci.CreateTime,
	}
}

func (ci *CopyItem) ToMap(taskID int64) map[string]interface{} {
	ci.mu.RLock()
	itemMap := ci.toJobTaskItemLocked(taskID).ToMap()
	itemMap["progress"] = ci.Progress
	ci.mu.RUnlock()
	return itemMap
}

func (ci *CopyItem) ToJobTaskItem(taskID int64) JobTaskItem {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	return ci.toJobTaskItemLocked(taskID)
}

func (ci *CopyItem) toJobTaskItemLocked(taskID int64) JobTaskItem {
	if ci.CopyType == taskItemTypeDelete {
		return NewDeleteJobTaskItem(taskID, ci.DstPath, ci.FileName, ci.FileSize,
			ci.Status, ci.ErrMsg, ci.IsPath, ci.CreateTime)
	}
	return NewCopyJobTaskItem(taskID, ci.SrcPath, ci.DstPath, ci.FileName, ci.FileSize,
		ci.AlistTaskID, ci.Status, ci.ErrMsg, ci.IsPath, ci.CopyType, ci.CreateTime)
}

// DoIt executes the copy operation in a goroutine.
func (ci *CopyItem) DoIt() {
	runtime := ci.copyRuntime()
	client := ci.copyClient()
	maxRetries := runtimeTaskLimits().MaxRetries
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if runtime.isBreak() {
			ci.setStatus(taskStatusStopped)
			break
		}

		ci.setRunning()
		submittedAt := time.Now().Unix()
		taskID, err := ci.startTransfer(runtime.context(), client)
		if err != nil && ci.CopyType != taskItemTypeDelete && isAmbiguousTransferError(err) && !runtime.isBreak() {
			// The request may have reached AList even though no answer came
			// back (header timeout, connection reset). Copy/move are not
			// idempotent: resubmitting a completed move fails because the
			// source is gone, and resubmitting a copy starts a duplicate task.
			// Look at the server state before deciding to retry.
			adoptedTaskID, completed := ci.reconcileAmbiguousTransfer(runtime, client, submittedAt)
			if completed {
				ci.setProgress(taskStatusSuccess, 100, nil)
				break
			}
			if adoptedTaskID != "" {
				taskID, err = adoptedTaskID, nil
			}
		}
		if err != nil {
			if errors.Is(err, context.Canceled) && runtime.isBreak() {
				ci.setStatus(taskStatusStopped)
				break
			}
			if attempt < maxRetries {
				ci.setRetrying(err)
				if completed := runtime.waitForBreak(jobDeps.CopyRetryDelay(attempt)); !completed {
					ci.setStatus(taskStatusStopped)
					break
				}
				continue
			}
			ci.setFailure(err)
			break
		}

		ci.setTaskID(taskID)
		// Delete operations are synchronous API calls — success means done.
		if ci.CopyType == taskItemTypeDelete {
			ci.setProgress(taskStatusSuccess, 100, nil)
			break
		}
		if taskID == "" && !ci.confirmSynchronousCopy(runtime, client) {
			// AList accepted the request but returned no task id, and the file
			// never arrived at the destination. Treat the attempt as failed so
			// the bounded retry loop can resubmit it.
			emptyTaskErr := errors.New(msg.T(msg.AlistNoCopyTask))
			if attempt < maxRetries {
				ci.setRetrying(emptyTaskErr)
				if completed := runtime.waitForBreak(jobDeps.CopyRetryDelay(attempt)); !completed {
					ci.setStatus(taskStatusStopped)
					break
				}
				continue
			}
			ci.setFailure(emptyTaskErr)
			break
		}
		if taskID == "" {
			ci.setProgress(taskStatusSuccess, 100, nil)
		} else if ci.status() != taskStatusStopped {
			runtime.waitForRemoteCopyCompletion(ci)
		}
		if ci.status() == taskStatusFailed && attempt < maxRetries {
			ci.setRetrying(errors.New(ci.errorMessage()))
			if completed := runtime.waitForBreak(jobDeps.CopyRetryDelay(attempt)); !completed {
				ci.setStatus(taskStatusStopped)
				break
			}
			continue
		}
		break
	}
	ci.endIt()
}

func (ci *CopyItem) startTransfer(ctx context.Context, client copyItemClient) (string, error) {
	switch ci.CopyType {
	case taskItemTypeDelete:
		scanIntervalT := 0
		if cfg := ci.copyRuntime().jobConfig(); cfg != nil {
			scanIntervalT = util.ToInt(cfg["scanIntervalT"])
		}
		return "", client.DeleteFileContext(ctx, ci.DstPath, []string{ci.FileName}, scanIntervalT)
	case taskItemTypeMove:
		return client.MoveFileContext(ctx, ci.SrcPath, ci.DstPath, ci.FileName)
	default:
		return client.CopyFileContext(ctx, ci.SrcPath, ci.DstPath, ci.FileName)
	}
}

// confirmSynchronousCopy handles a copy/move response with no task id: some
// backends complete the operation synchronously. The destination is verified so
// "no task" is only treated as success when the file actually arrived.
//
// A definitive "absent" answer fails the attempt. An erroring check is retried a
// few times so a network blip does not decide the outcome; if it keeps failing
// the legacy assume-success behavior is kept, because some drivers do not
// support the existence probe at all and every such copy would otherwise be
// reported as failed.
func (ci *CopyItem) confirmSynchronousCopy(runtime copyItemRuntime, client copyItemClient) bool {
	for attempt := 0; attempt < maxTransientPollErrors; attempt++ {
		exists, err := ci.verifyDstComplete(runtime, client)
		if err == nil {
			return exists
		}
		if runtime.isBreak() {
			return false
		}
		if attempt < maxTransientPollErrors-1 && !runtime.waitForBreak(jobDeps.CopyRetryDelay(attempt)) {
			return false
		}
	}
	return true
}

// isAmbiguousTransferError reports whether a copy/move error leaves it unknown
// whether AList executed the request. Clear server answers (HTTP status or
// AList business code), auth/address errors and connection refusals mean the
// operation did not run, so the normal retry applies. Everything else
// (timeouts, resets, undecodable 200 responses) may follow a server-side
// success.
func isAmbiguousTransferError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var statusErr *alistStatusError
	if errors.As(err, &statusErr) {
		return false
	}
	for _, definite := range []string{msg.T(msg.AlistUnAuth), msg.T(msg.AddressIncorrect), msg.T(msg.AlistConnectFail)} {
		if strings.Contains(err.Error(), definite) {
			return false
		}
	}
	return true
}

// reconcileAmbiguousTransfer inspects AList after an ambiguous copy/move
// failure. It returns the id of an undone AList task that already carries this
// exact transfer (so the caller polls it instead of submitting a duplicate),
// or completed=true when the destination already holds the file (and, for a
// move, the source is gone). Any probe error yields ("", false): the caller
// then falls back to the normal bounded retry.
//
// For a copy the destination may already have held an older version of the
// same size (Overwrite:true replaces it), so a matching size alone does not
// prove this submission ran. The destination must also have been written at or
// after submittedAt; an unknown mtime falls back to the normal retry.
func (ci *CopyItem) reconcileAmbiguousTransfer(runtime copyItemRuntime, client copyItemClient, submittedAt int64) (string, bool) {
	ctx, cancel := runtime.cleanupContext()
	tasks, err := client.TaskUndoneListContext(ctx, ci.CopyType)
	cancel()
	if err == nil {
		if taskID := ci.matchUndoneTask(tasks); taskID != "" {
			return taskID, false
		}
	}

	if ci.CopyType == taskItemTypeMove {
		srcCtx, srcCancel := runtime.cleanupContext()
		srcExists, err := client.FileExistsContext(srcCtx, ci.SrcPath, ci.FileName)
		srcCancel()
		if err != nil || srcExists {
			return "", false
		}
	}
	if ci.CopyType == taskItemTypeMove {
		// The source is gone, so the destination can only be this move's result.
		done, err := ci.verifyDstComplete(runtime, client)
		return "", err == nil && done
	}
	done, err := ci.verifyDstWrittenSince(runtime, client, submittedAt)
	return "", err == nil && done
}

// verifyDstWrittenSince is verifyDstComplete plus a freshness check: the
// destination's modification time must not predate since. Storage clocks can
// round to the second, hence the one-second allowance.
func (ci *CopyItem) verifyDstWrittenSince(runtime copyItemRuntime, client copyItemClient, since int64) (bool, error) {
	statClient, canStat := client.(copyItemStatClient)
	if !canStat {
		return false, nil
	}
	done, err := ci.verifyDstComplete(runtime, client)
	if err != nil || !done {
		return false, err
	}
	ctx, cancel := runtime.cleanupContext()
	defer cancel()
	stat, err := statClient.FileStatContext(ctx, ci.DstPath, ci.FileName)
	if err != nil {
		return false, err
	}
	modified := stat.Modified
	if modified > 1e12 {
		// Some drivers report milliseconds.
		modified /= 1000
	}
	return modified > 0 && modified >= since-1, nil
}

// alistTaskNamePattern matches AList's copy/move task names:
// "copy [<src mount>](<src path in storage>) to [<dst mount>](<dst dir in storage>)".
var alistTaskNamePattern = regexp.MustCompile(`^\w+ \[([^\]]*)\]\((.*)\) to \[([^\]]*)\]\((.*)\)$`)

// matchUndoneTask returns the id of the single undone task whose name encodes
// exactly this item's source file and destination directory. Unknown name
// formats and multiple matches return "" so an unrelated task is never
// adopted.
func (ci *CopyItem) matchUndoneTask(tasks []map[string]interface{}) string {
	wantSrc := path.Clean("/" + ci.SrcPath + "/" + ci.FileName)
	wantDst := path.Clean("/" + ci.DstPath)
	match := ""
	for _, task := range tasks {
		name, _ := task["name"].(string)
		parts := alistTaskNamePattern.FindStringSubmatch(name)
		if parts == nil {
			continue
		}
		src := path.Clean("/" + parts[1] + "/" + parts[2])
		dst := path.Clean("/" + parts[3] + "/" + parts[4])
		if src != wantSrc || dst != wantDst {
			continue
		}
		id := fmt.Sprintf("%v", task["id"])
		if id == "" || id == "<nil>" {
			continue
		}
		if match != "" && match != id {
			return ""
		}
		match = id
	}
	return match
}

func defaultCopyRetryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := time.Duration(attempt+1) * time.Second
	if delay > 5*time.Second {
		return 5 * time.Second
	}
	return delay
}

func (ci *CopyItem) errorMessage() string {
	ci.mu.RLock()
	defer ci.mu.RUnlock()
	if ci.ErrMsg == nil {
		return "copy failed"
	}
	return *ci.ErrMsg
}

// verifyDstComplete checks whether the destination holds the transferred file.
// Used when the AList task record has vanished (404) or the copy/move response
// was lost, to decide whether the transfer actually completed. Copies run with
// Overwrite:true, so the old version of the file usually already exists at the
// destination; existence alone would turn a failed update into a success.
// When the source size is known and the client can report metadata, the
// destination size must match. Without a known size the historical existence
// check is kept. Runs on an independent cleanup context so it is not cut short
// by a cancelling task context.
func (ci *CopyItem) verifyDstComplete(runtime copyItemRuntime, client copyItemClient) (bool, error) {
	ctx, cancel := runtime.cleanupContext()
	defer cancel()
	statClient, canStat := client.(copyItemStatClient)
	wantSize, sizeKnown := ci.expectedSize()
	if !canStat || !sizeKnown {
		return client.FileExistsContext(ctx, ci.DstPath, ci.FileName)
	}
	stat, err := statClient.FileStatContext(ctx, ci.DstPath, ci.FileName)
	if err != nil {
		return false, err
	}
	if !stat.Exists {
		return false, nil
	}
	if stat.IsDir || ci.IsPath == taskItemPath || !stat.SizeKnown {
		return true, nil
	}
	return stat.Size == wantSize, nil
}

// expectedSize returns the source size recorded when the item was queued.
func (ci *CopyItem) expectedSize() (int64, bool) {
	if ci.FileSize == nil {
		return 0, false
	}
	size := util.ToInt64(ci.FileSize)
	if size < 0 {
		return 0, false
	}
	return size, true
}

func (ci *CopyItem) stopRemoteTask(client copyItemClient, cause error) {
	ci.setStatus(taskStatusStopped)
	if cause != nil {
		errMsg := cause.Error()
		ci.setProgress(taskStatusStopped, ci.progress(), &errMsg)
	}
	if taskID := ci.taskID(); taskID != "" {
		// Cancel and delete each get their own cleanup context so a slow cancel
		// cannot exhaust the budget and silently skip the delete.
		cancelCtx, cancelCancel := ci.copyRuntime().cleanupContext()
		if err := client.TaskCancelContext(cancelCtx, taskID, ci.CopyType); err != nil {
			errMsg := err.Error()
			ci.setProgress(taskStatusStopped, ci.progress(), &errMsg)
		}
		cancelCancel()
		deleteCtx, cancelDelete := ci.copyRuntime().cleanupContext()
		_ = client.TaskDeleteContext(deleteCtx, taskID, ci.CopyType)
		cancelDelete()
	}
}

func (ci *CopyItem) endIt() {
	runtime := ci.copyRuntime()
	runtime.finishCopyItem(ci)
}
