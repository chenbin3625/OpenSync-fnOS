package service

import (
	"context"
	"opensync/internal/config"
	"opensync/internal/native"
	"opensync/pkg/util"
	"sync"
	"sync/atomic"
	"time"
)

// JobTask represents a running sync task executed by the built-in rclone engine.
type JobTask struct {
	TaskID       int64
	JobClient    *JobClient
	Job          map[string]interface{}
	nativeConfig native.Config
	nativeStats  atomic.Pointer[nativeProgressSnapshot]
	CreateTime   float64

	FinishMu       sync.Mutex
	FinishedCounts map[taskStatus]int
	FinishedSizes  map[taskStatus]int64
	Doing          map[int64]*CopyItem
	DoingMu        sync.RWMutex
	Waiting        *copyQueue

	LastWatching               atomic.Int64
	QueueNum                   atomic.Int64
	ScanFinish                 atomic.Bool
	cachedLimits               *taskRuntimeLimits
	FirstSync                  atomic.Int64
	BreakFlag                  atomic.Bool
	ctx                        context.Context
	cancel                     context.CancelFunc
	runtimeMu                  sync.Mutex
	RequireFullSyncApproval    bool
	fullSyncDeleteGuardSkipped atomic.Bool
	drained                    atomic.Bool
	ScanTotalDirs              atomic.Int64
	ScanDoneDirs               atomic.Int64
	ScanTotalFiles             atomic.Int64
	ScanCheckedFiles           atomic.Int64

	RetrySourceTaskID int64
	RetryStatuses     []taskStatus
	RetryItemIDs      []int64
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
	jt := &JobTask{
		TaskID:         taskID,
		JobClient:      jc,
		Job:            job,
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
		jt.runtimeMu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
}

func (jt *JobTask) approvalJobID() int64 {
	if id := jt.jobIDSnapshot(); id > 0 {
		return id
	}
	if jt != nil && jt.Job != nil {
		return util.ToInt64(jt.Job["id"])
	}
	return 0
}

const defaultFullSyncApprovalWait = 30 * time.Minute

// fullSyncApprovalWait bounds how long a pending deletion approval stays valid
// before expiring automatically without deleting destination files.
// Tests may override it directly; in production it follows SystemSettings.FullSyncApprovalTimeout.
var fullSyncApprovalWait = defaultFullSyncApprovalWait

func effectiveFullSyncApprovalWait() time.Duration {
	if fullSyncApprovalWait != defaultFullSyncApprovalWait {
		return fullSyncApprovalWait
	}
	minutes := config.GetSystemSettings().FullSyncApprovalTimeout
	if minutes <= 0 {
		return 0
	}
	return time.Duration(minutes) * time.Minute
}
