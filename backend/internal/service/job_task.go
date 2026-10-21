package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"opensync/internal/config"
	"opensync/pkg/util"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// JobTask represents a running task with sync engine
type JobTask struct {
	TaskID      int64
	JobClient   *JobClient
	Job         map[string]interface{}
	AlistClient *AlistClient
	CreateTime  float64

	FinishMu       sync.Mutex
	FinishedCounts map[taskStatus]int
	FinishedSizes  map[taskStatus]int64
	Doing          map[int64]*CopyItem
	DoingMu        sync.RWMutex
	Waiting        *copyQueue

	LastWatching atomic.Int64
	// QueueNum is incremented by both the submit executor and the full-sync
	// relocation path, so it has to be atomic: a plain ++ let two items share a
	// DoingKey and silently overwrite each other in Doing.
	QueueNum      atomic.Int64
	ScanFinish    atomic.Bool
	cachedLimits  *taskRuntimeLimits
	FirstSync     atomic.Int64
	BreakFlag     atomic.Bool
	scanSem       chan struct{}
	scanBranchSem chan struct{}
	lastMeter     transferMeter
	ctx           context.Context
	cancel        context.CancelFunc
	runtimeMu     sync.Mutex
	copyWG        sync.WaitGroup
	// copySlotMu guards copyInFlight, the number of admitted copies. Admission
	// happens under runtimeMu; the counter is what keeps the submit executor and
	// full-sync relocations from overshooting CopyConcurrency when both pass
	// their own "is there room" check at the same time.
	copySlotMu   sync.Mutex
	copyInFlight int
	// copyAdmissionClosed is set (under runtimeMu) once the submit executor
	// has begun its final copyWG.Wait. Later copyWG.Add calls would race that
	// Wait and start AList transfers after the task was reported finished.
	copyAdmissionClosed bool
	// scanDone is closed when the scan worker started by Start has returned.
	// The executor waits for it before the final flush: a scan goroutine still
	// running would append task items and re-arm the flush timer after the
	// task was finished. Nil for tasks not started through Start (tests).
	scanDone chan struct{}
	// drained records that the executor exited because every queued item
	// finished — not because the task was stopped or its deadline hit.
	drained       atomic.Bool
	ScanTotalDirs atomic.Int64
	ScanDoneDirs  atomic.Int64

	// CurrentMu guards the transfer meter below (speed bookkeeping).
	CurrentMu sync.RWMutex

	RetrySourceTaskID     int64
	RetryStatuses         []taskStatus
	FatalMu               sync.Mutex
	FatalErr              *string
	PersistMu             sync.Mutex
	PersistErr            error
	persistBufMu          sync.Mutex
	persistBuffer         []JobTaskItem
	persistFlushMu        sync.Mutex
	persistFlushScheduled bool
	// persistFlushTimer is the pending debounced flush. It is retained so the
	// task can cancel it on completion: an AfterFunc that outlives the task
	// would still write task items (and reach GetDB) after the run finished,
	// and after CloseDB during shutdown.
	persistFlushTimer *time.Timer
	// persistFlushInFlight is held for the duration of a flush's DB write. The
	// final flush takes it too, so a timer flush that already started (and can
	// therefore no longer be cancelled) is awaited instead of being raced: its
	// failure must be visible before the task reports success.
	persistFlushInFlight      sync.Mutex
	copyMonitor               *copyTaskMonitor
	copyMonitorClientOverride copyItemClient
}

func newJobTask(taskID int64, jc *JobClient) (*JobTask, error) {
	job := jc.jobSnapshot()
	limits := runtimeTaskLimits()
	jt := &JobTask{
		TaskID:         taskID,
		JobClient:      jc,
		Job:            job,
		CreateTime:     float64(time.Now().Unix()),
		FinishedCounts: make(map[taskStatus]int),
		FinishedSizes:  make(map[taskStatus]int64),
		Doing:          make(map[int64]*CopyItem),
		Waiting:        newCopyQueueWithCapacity(defaultCopyQueueCapacity),
		scanSem:        make(chan struct{}, scanConcurrencyLimit()),
		scanBranchSem:  make(chan struct{}, scanBranchLimit()),
		cachedLimits:   &limits,
	}
	jt.ctx, jt.cancel = newTaskContext(config.GetConfig().Server.Timeout)
	client, err := GetClientByIDContext(jt.ctx, util.ToInt64(job["alistId"]))
	if err != nil {
		jt.cancel()
		return nil, err
	}
	jt.AlistClient = client
	return jt, nil
}

// taskLimits returns the cached runtime limits for this task, avoiding
// repeated config reads on hot paths (copy executor loop, retry decisions).
func (jt *JobTask) taskLimits() taskRuntimeLimits {
	if jt.cachedLimits != nil {
		return *jt.cachedLimits
	}
	return runtimeTaskLimits()
}

func newTaskContext(timeoutHours int) (context.Context, context.CancelFunc) {
	if timeoutHours <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), time.Duration(timeoutHours)*time.Hour)
}

func (jt *JobTask) Start() {
	scanDone := make(chan struct{})
	jt.runtimeMu.Lock()
	jt.scanDone = scanDone
	jt.runtimeMu.Unlock()
	go func() {
		// Closed last, after the panic handler below has run, so the executor
		// never proceeds while the scan side is still recording items.
		defer close(scanDone)
		defer func() {
			if r := recover(); r != nil {
				jt.handleWorkerPanic("scan", r)
			}
		}()
		jt.sync()
	}()
	jt.startWorker("submit", jt.taskSubmit)
}

// waitForScanExit blocks until the scan worker has returned. Stop and timeout
// both cancel the task context, which every scan path observes, so this wait
// is bounded by the scan's own reaction to cancellation.
func (jt *JobTask) waitForScanExit() {
	jt.runtimeMu.Lock()
	done := jt.scanDone
	jt.runtimeMu.Unlock()
	if done != nil {
		<-done
	}
}

func (jt *JobTask) startWorker(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				jt.handleWorkerPanic(name, r)
			}
		}()
		fn()
	}()
}

func (jt *JobTask) handleWorkerPanic(name string, recovered interface{}) {
	errMsg := workerPanicMessage(name, recovered)
	log.Printf("Task %d %s", jt.TaskID, errMsg)
	jt.setFatalError(errMsg)
	jt.ScanFinish.Store(true)
	jt.requestBreak()
	if name == "submit" {
		// A panic here means the submit worker unwound past its normal shutdown
		// (the executor loop has its own recovery). In-flight copies must still be
		// awaited before the task is reported finished, otherwise their item
		// records are written after the final status/counters and can even re-arm
		// the flush timer after the run ended.
		jt.copyWG.Wait()
		// The drain or final flush is what panicked, so retry the cleanup it
		// skipped once, best effort: stop the remote-task monitor and save
		// the buffered items. A second panic is logged, not propagated.
		jt.bestEffortSubmitCleanup()
		jt.finishFailedTask(errMsg)
	}
}

func (jt *JobTask) bestEffortSubmitCleanup() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Task %d cleanup after submit panic failed: %v", jt.TaskID, r)
		}
	}()
	jt.stopCopyMonitor()
	if err := jt.persistRemainingTaskItems(); err != nil {
		log.Printf("Task %d could not save remaining items after submit panic: %v", jt.TaskID, err)
	}
}

func workerPanicMessage(name string, recovered interface{}) string {
	return fmt.Sprintf("%s worker panic: %v", name, recovered)
}

func (jt *JobTask) recoverWorkerPanic(name string, errTarget *error) {
	if r := recover(); r != nil {
		errMsg := workerPanicMessage(name, r)
		if errTarget != nil {
			*errTarget = errors.New(errMsg)
		}
		jt.handleWorkerPanic(name, r)
	}
}

func (jt *JobTask) setFatalError(errMsg string) {
	jt.FatalMu.Lock()
	defer jt.FatalMu.Unlock()
	if jt.FatalErr != nil {
		return
	}
	msg := errMsg
	jt.FatalErr = &msg
}

func (jt *JobTask) fatalError() *string {
	jt.FatalMu.Lock()
	defer jt.FatalMu.Unlock()
	if jt.FatalErr == nil {
		return nil
	}
	msg := *jt.FatalErr
	return &msg
}

func (jt *JobTask) copyMonitorClient() copyItemClient {
	if jt.copyMonitorClientOverride != nil {
		return jt.copyMonitorClientOverride
	}
	return jt.AlistClient
}

func scanConcurrencyLimit() int {
	limit := runtimeTaskLimits().ScanConcurrency
	if limit <= 0 {
		limit = runtime.NumCPU()
	}
	return intInRangeOrDefault(limit, config.MinScanConcurrency, config.MaxScanConcurrency, config.DefaultScanConcurrency)
}

func scanBranchLimit() int {
	return scanConcurrencyLimit() * 3
}

func (jt *JobTask) initRuntime() {
	jt.runtimeMu.Lock()
	defer jt.runtimeMu.Unlock()
	jt.ensureRuntimeLocked()
}

func (jt *JobTask) ensureRuntimeLocked() {
	if jt.Waiting == nil {
		jt.Waiting = newCopyQueueWithCapacity(defaultCopyQueueCapacity)
	}
	if jt.Doing == nil {
		jt.Doing = make(map[int64]*CopyItem)
	}
	if jt.FinishedCounts == nil {
		jt.FinishedCounts = make(map[taskStatus]int)
	}
	if jt.FinishedSizes == nil {
		jt.FinishedSizes = make(map[taskStatus]int64)
	}
	if jt.scanSem == nil {
		jt.scanSem = make(chan struct{}, scanConcurrencyLimit())
	}
	if jt.scanBranchSem == nil {
		jt.scanBranchSem = make(chan struct{}, scanBranchLimit())
	}
	if jt.ctx == nil || jt.cancel == nil {
		jt.ctx, jt.cancel = newTaskContext(config.GetConfig().Server.Timeout)
	}
	if jt.BreakFlag.Load() && jt.cancel != nil {
		jt.cancel()
	}
}

func (jt *JobTask) context() context.Context {
	jt.runtimeMu.Lock()
	defer jt.runtimeMu.Unlock()
	jt.ensureRuntimeLocked()
	return jt.ctx
}

const cleanupTimeout = 30 * time.Second

func (jt *JobTask) cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), cleanupTimeout)
}

func (jt *JobTask) isBreak() bool {
	return jt.BreakFlag.Load()
}

func (jt *JobTask) requestBreak() {
	if !jt.BreakFlag.Swap(true) {
		jt.runtimeMu.Lock()
		jt.ensureRuntimeLocked()
		cancel := jt.cancel
		jt.runtimeMu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
}

func (jt *JobTask) waitForBreak(d time.Duration) bool {
	ctx := jt.context()
	if d <= 0 {
		select {
		case <-ctx.Done():
			return false
		default:
			return true
		}
	}

	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (jt *JobTask) jobConfig() map[string]interface{} {
	return jt.Job
}

func (jt *JobTask) lastWatchingUnix() int64 {
	return jt.LastWatching.Load()
}

func (jt *JobTask) finishCopyItem(item *CopyItem) {
	item.mu.RLock()
	srcPath := item.SrcPath
	dstPath := item.DstPath
	fileName := item.FileName
	fileSize := item.FileSize
	alistTaskID := item.AlistTaskID
	status := item.Status
	errMsg := item.ErrMsg
	copyType := item.CopyType
	isPath := item.IsPath
	createTime := item.CreateTime
	item.mu.RUnlock()

	// Remove the item from Doing before recording its final state: the hook
	// takes the progress snapshot, and an item that is still in Doing while its
	// terminal record is appended is counted twice (running + success) for one
	// snapshot.
	jt.DoingMu.Lock()
	delete(jt.Doing, item.DoingKey)
	jt.DoingMu.Unlock()

	if copyType == taskItemTypeDelete {
		jt.DelHook(dstPath, fileName, fileSize, status, errMsg, isPath, createTime)
	} else {
		jt.CopyHook(srcPath, dstPath, fileName, fileSize, alistTaskID,
			status, errMsg, isPath, copyType, createTime)
	}
}
