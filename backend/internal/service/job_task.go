package service

import (
	"context"
	"errors"
	"opensync/internal/config"
	"opensync/internal/msg"
	"opensync/internal/native"
	"opensync/pkg/util"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// JobTask represents a running sync task executed by the built-in rclone engine.
type JobTask struct {
	TaskID                int64
	JobClient             *JobClient
	Job                   map[string]interface{}
	nativeConfig          native.Config
	nativeStats           atomic.Pointer[nativeProgressSnapshot]
	nativePlannedMu       sync.Mutex
	nativePlanned         *nativePlannedProgress
	rcController          *native.RCController
	bwLimitUserOverridden atomic.Bool
	CreateTime            float64

	FinishMu       sync.Mutex
	FinishedCounts map[taskStatus]int
	FinishedSizes  map[taskStatus]int64
	Doing          map[int64]*CopyItem
	DoingMu        sync.RWMutex
	Waiting        *copyQueue

	LastWatching               atomic.Int64
	QueueNum                   atomic.Int64
	ScanFinish                 atomic.Bool
	nativePendingOverflow      atomic.Bool
	interruptedRecorded        atomic.Bool
	cachedLimits               *taskRuntimeLimits
	FirstSync                  atomic.Int64
	BreakFlag                  atomic.Bool
	ctx                        context.Context
	cancel                     context.CancelFunc
	runtimeMu                  sync.Mutex
	fullSyncDeleteGuardSkipped atomic.Bool
	drained                    atomic.Bool
	ScanTotalDirs              atomic.Int64
	ScanDoneDirs               atomic.Int64
	ScanTotalFiles             atomic.Int64
	ScanCheckedFiles           atomic.Int64

	RetrySourceTaskID int64
	RetryJobItems     bool
	RetryStatuses     []taskStatus
	RetryItemIDs      []int64
	OnlyFiles         []string
	ForceOverwrite    bool
	ChainDepth        int
	SyncGeneration    int64
	SyncChangedFiles  []string
	SyncPartial       bool
	SyncFullScan      bool
	releaseEngineSlot func()
	trashDirMu        sync.Mutex
	trashRunBucket    string
	trashItemCounts   map[string]int
	FatalMu           sync.Mutex
	FatalErr          *string
	PersistMu         sync.Mutex
	PersistErr        error
	persistBufMu      sync.Mutex
	persistBuffer     []JobTaskItem
	persistFlushMu    sync.Mutex
	// persistFlushScheduled indicates a debounced persist timer is pending.
	persistFlushScheduled bool
	persistFlushTimer     *time.Timer
	persistFlushInFlight  sync.Mutex
}

func newJobTask(taskID int64, jc *JobClient) (*JobTask, error) {
	job := jc.jobSnapshot()
	limits := taskRuntimeLimitsForJob(job)
	var rcCtrl *native.RCController
	if jobBw := strings.TrimSpace(util.StringValue(job["bwLimit"])); jobBw != "" {
		rcCtrl = native.NewRCController(jobBw)
	} else {
		globalBw := globalDefaultBwLimit()
		if native.GetGlobalBwLimit() != globalBw {
			_ = native.SetGlobalBwLimit(globalBw)
		}
		rcCtrl = native.NewGlobalRCController()
	}
	jt := &JobTask{
		TaskID:         taskID,
		JobClient:      jc,
		Job:            job,
		rcController:   rcCtrl,
		CreateTime:     float64(time.Now().Unix()),
		FinishedCounts: make(map[taskStatus]int),
		FinishedSizes:  make(map[taskStatus]int64),
		Doing:          make(map[int64]*CopyItem),
		Waiting:        newCopyQueueWithCapacity(defaultCopyQueueCapacity),
		cachedLimits:   &limits,
	}
	jt.ctx, jt.cancel = newTaskContext(config.GetConfig().Server.Timeout)
	id := util.ToInt64(job["alistId"])
	cfg, err := loadNative(id)
	if err != nil {
		jt.cancel()
		return nil, err
	}
	if cfg == nil {
		jt.cancel()
		return nil, publicError(legacyOpenListJobDisabledMsg)
	}
	jt.nativeConfig = cfg
	return jt, nil
}

// taskLimits returns the cached runtime limits for this task.
func (jt *JobTask) taskLimits() taskRuntimeLimits {
	if jt.cachedLimits != nil {
		return *jt.cachedLimits
	}
	return taskRuntimeLimitsForJob(jt.Job)
}

func newTaskContext(timeoutHours int) (context.Context, context.CancelFunc) {
	if timeoutHours <= 0 {
		return context.WithCancel(context.Background())
	}
	return context.WithTimeout(context.Background(), time.Duration(timeoutHours)*time.Hour)
}

func (jt *JobTask) Start() {
	go jt.runNative()
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
		rc := jt.rcController
		jt.runtimeMu.Unlock()
		if rc != nil {
			rc.ResumeAllProcesses()
		}
		if cancel != nil {
			cancel()
		}
	}
}

func (jt *JobTask) controller() *native.RCController {
	if jt == nil {
		return nil
	}
	return jt.rcController
}

func (jt *JobTask) isPaused() bool {
	if jt == nil || jt.rcController == nil {
		return false
	}
	return jt.rcController.IsPaused()
}

func (jt *JobTask) pausedDuration() time.Duration {
	if jt == nil || jt.rcController == nil {
		return 0
	}
	return jt.rcController.PausedDuration()
}

func (jt *JobTask) liveBwLimit() string {
	if jt == nil || jt.rcController == nil {
		return ""
	}
	return jt.rcController.BwLimit()
}

// PauseTransfer pauses the running task's rclone / resumable transfer workers.
func (jt *JobTask) PauseTransfer() error {
	if jt == nil || jt.rcController == nil {
		return publicError("当前任务不支持暂停")
	}
	if jt.isBreak() {
		return publicError(msg.T(msg.TaskNotRunningStop))
	}
	if err := jt.rcController.Pause(); err != nil {
		return err
	}
	jt.notifyProgressNow()
	return nil
}

// ResumeTransfer resumes a previously paused task's transfer workers.
func (jt *JobTask) ResumeTransfer() error {
	if jt == nil || jt.rcController == nil {
		return publicError("当前任务不支持恢复")
	}
	if jt.isBreak() {
		return publicError(msg.T(msg.TaskNotRunningStop))
	}
	if err := jt.rcController.Resume(); err != nil {
		return err
	}
	jt.notifyProgressNow()
	return nil
}

// SetLiveBwLimit updates bandwidth throttling for an actively running task via rclone RC.
func (jt *JobTask) SetLiveBwLimit(ctx context.Context, rate string) error {
	if jt == nil || jt.rcController == nil {
		return publicError("当前任务不支持动态限速")
	}
	if err := jt.rcController.SetBwLimit(ctx, rate); err != nil {
		return publicError(err.Error())
	}
	jt.bwLimitUserOverridden.Store(true)
	jt.notifyProgressNow()
	return nil
}

// ApplyGlobalBwLimit propagates an updated global bandwidth limit to a
// running task when the task inherits the global cap and has not been
// manually overridden during this run.
func (jt *JobTask) ApplyGlobalBwLimit(ctx context.Context, rate string) error {
	if jt == nil || jt.rcController == nil || jt.bwLimitUserOverridden.Load() {
		return nil
	}
	if jt.Job != nil && strings.TrimSpace(util.StringValue(jt.Job["bwLimit"])) != "" {
		return nil
	}
	if jt.rcController.InheritsGlobal() {
		if err := native.SetGlobalBwLimit(rate); err != nil {
			return err
		}
		jt.notifyProgressNow()
		return nil
	}
	if err := jt.rcController.SetBwLimit(ctx, rate); err != nil {
		return err
	}
	jt.notifyProgressNow()
	return nil
}

// recordInterruptedPendingItems persists in-flight and still-waiting items when
// a task is stopped or times out so "重试未完成项" can resume from the exact
// remaining file set instead of finding zero retryable items.
func (jt *JobTask) recordInterruptedPendingItems(fallbackPairs [][2]string) {
	if jt == nil || jt.isDrained() {
		return
	}
	ctxErr := jt.context().Err()
	if !jt.isBreak() && ctxErr == nil {
		return
	}
	if !jt.interruptedRecorded.CompareAndSwap(false, true) {
		return
	}
	status := taskStatusStopped
	reason := "任务已中断，可点击“重试未完成项”继续同步"
	if !jt.isBreak() && errors.Is(ctxErr, context.DeadlineExceeded) {
		status = taskStatusTimeout
		reason = "任务超时中断，可点击“重试未完成项”继续同步"
	}
	itemType := taskItemTypeCopy
	if util.ToInt(jt.Job["method"]) == 2 {
		itemType = taskItemTypeTransfer
	}

	now := time.Now().Unix()
	type itemKey struct{ src, dst, name string }
	seen := make(map[itemKey]struct{})
	var items []JobTaskItem

	if snap := jt.nativeStats.Load(); snap != nil {
		for _, act := range snap.activeTransferring() {
			rel := path.Clean(act.Name)
			if rel == "" || rel == "." {
				continue
			}
			srcDir := path.Join(act.srcBase, path.Dir(rel))
			dstDir := path.Join(act.dstBase, path.Dir(rel))
			base := path.Base(rel)
			k := itemKey{srcDir, dstDir, base}
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			msgCopy := reason
			items = append(items, NewCopyJobTaskItem(jt.TaskID, srcDir, dstDir, base, act.Size, "", status, &msgCopy, taskItemFile, itemType, now))
		}
	}
	if jt.Waiting != nil {
		for _, w := range jt.Waiting.snapshot() {
			if w == nil {
				continue
			}
			k := itemKey{w.SrcPath, w.DstPath, w.FileName}
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			msgCopy := reason
			items = append(items, NewCopyJobTaskItem(jt.TaskID, w.SrcPath, w.DstPath, w.FileName, w.FileSize, "", status, &msgCopy, w.IsPath, itemType, now))
		}
	}
	if jt.nativePendingOverflow.Load() || (len(items) == 0 && !jt.ScanFinish.Load()) {
		for _, pair := range fallbackPairs {
			if pair[0] == "" || pair[1] == "" {
				continue
			}
			msgCopy := reason
			items = append(items, NewCopyJobTaskItem(jt.TaskID, pair[0], pair[1], "", nil, "", status, &msgCopy, taskItemPath, itemType, now))
		}
	}
	if len(items) > 0 {
		jt.appendFinishMany(items)
	}
}
