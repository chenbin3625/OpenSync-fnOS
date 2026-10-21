package service

import (
	"log"
	"sync"
	"time"
)

func (jt *JobTask) taskSubmit() {
	// The task's runtime is released on every exit path, including a panic
	// outside runCopyExecutorRecovered (drainCopyExecutor, the final flush or
	// the terminal status write). A debounce timer or an un-cancelled task
	// context must not outlive the task: at shutdown they reach GetDB after
	// ShutdownDB, and a live timer can write a task item after its final status.
	defer jt.cancelTaskRuntime()
	jt.runCopyExecutorRecovered()
	// Everything below runs whether the executor returned or panicked: a
	// panic used to jump straight to finishFailedTask, leaving in-flight
	// copies, the remote-task monitor and the flush timer running past the
	// end of the task and dropping whatever was still buffered.
	jt.drainCopyExecutor()
	persistErr := jt.persistRemainingTaskItems()
	jt.finishSubmittedTask(persistErr)
}

// cancelTaskRuntime releases the task-scoped resources that are not tied to a
// finished work item: the debounced persist flush and the task context (whose
// WithTimeout timer otherwise stays armed until the configured deadline, one
// per finished task).
func (jt *JobTask) cancelTaskRuntime() {
	jt.cancelPersistFlush()
	jt.runtimeMu.Lock()
	cancel := jt.cancel
	jt.runtimeMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// runCopyExecutorRecovered runs the executor loop and converts a panic into a
// fatal task error, so the caller can still perform the normal shutdown.
func (jt *JobTask) runCopyExecutorRecovered() {
	defer func() {
		if r := recover(); r != nil {
			errMsg := workerPanicMessage("submit", r)
			log.Printf("Task %d %s", jt.TaskID, errMsg)
			jt.setFatalError(errMsg)
			jt.requestBreak()
		}
	}()
	jt.runCopyExecutor()
}

// drainCopyExecutor brings the task to rest before its final flush: the scan
// worker has exited (after a stop or timeout it may still be recording items
// or running relocations), no copy can start any more, every started copy has
// finished and the remote-task monitor is stopped.
func (jt *JobTask) drainCopyExecutor() {
	if jt.isBreak() || jt.context().Err() != nil {
		// Releases scan producers blocked on a full queue.
		jt.markWaitingAsAborted()
	}
	// Relocations started by the scan wait on their own batch, not on the
	// executor, so the scan can always run to completion here.
	jt.waitForScanExit()
	jt.closeCopyAdmission()
	jt.copyWG.Wait()
	jt.stopCopyMonitor()
}

func (jt *JobTask) closeCopyAdmission() {
	jt.runtimeMu.Lock()
	jt.copyAdmissionClosed = true
	jt.runtimeMu.Unlock()
}

func (jt *JobTask) runCopyExecutor() {
	jt.initRuntime()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		if jt.stopCopyExecutorIfBroken() {
			break
		}
		// Context cancelled (e.g. task timeout) without an explicit break flag:
		// drain waiting items and stop the executor. Without this, the select in
		// waitForCopyExecutorSignal returns immediately on the always-ready
		// ctx.Done() channel and the loop spins at 100% CPU until scan finishes.
		if jt.context().Err() != nil {
			jt.markWaitingAsAborted()
			break
		}

		started := jt.startAvailableCopyItems()
		if jt.copyExecutorDrained() {
			// Recorded only if the deadline had not passed when the last item
			// finished: a scan cut short by the deadline also "drains", and
			// must still be reported as a timeout.
			if jt.context().Err() == nil {
				jt.drained.Store(true)
			}
			break
		}
		if started {
			continue
		}

		jt.waitForCopyExecutorSignal(ticker.C)
	}

	if jt.isBreak() {
		jt.markWaitingAsAborted()
	}
}

func (jt *JobTask) stopCopyExecutorIfBroken() bool {
	if !jt.isBreak() {
		return false
	}
	jt.markWaitingAsAborted()
	return true
}

func (jt *JobTask) startAvailableCopyItems() bool {
	started := false
	limits := jt.taskLimits()
	for {
		if jt.isBreak() {
			jt.markWaitingAsAborted()
			break
		}
		if !jt.hasCopySlot(limits.CopyConcurrency) {
			break
		}
		item, ok := jt.Waiting.pop()
		if !ok {
			break
		}
		if jt.startCopyItemTracked(item, nil) {
			started = true
			continue
		}
		if jt.copyStopped() {
			// startCopyItemTracked already recorded the stopped item.
			break
		}
		// A full-sync relocation took the last slot between the capacity check
		// and admission. Put the item back instead of dropping or over-admitting
		// it; the queue has room because this executor just popped it.
		if !jt.Waiting.pushWait(jt.context(), item) {
			item.setStatus(taskStatusStopped)
			jt.appendFinish(item.ToJobTaskItem(jt.TaskID))
		}
		break
	}
	return started
}

func (jt *JobTask) copyExecutorDrained() bool {
	return jt.ScanFinish.Load() && jt.doingLen() == 0 && jt.Waiting.len() == 0
}

func (jt *JobTask) waitForCopyExecutorSignal(tick <-chan time.Time) {
	select {
	case <-jt.context().Done():
		jt.markWaitingAsAborted()
	case <-jt.Waiting.waitCh():
	case <-tick:
	}
}

func (jt *JobTask) doingLen() int {
	jt.DoingMu.RLock()
	defer jt.DoingMu.RUnlock()
	return len(jt.Doing)
}

func (jt *JobTask) startCopyItem(item *CopyItem) {
	jt.startCopyItemTracked(item, nil)
}

// hasCopySlot reports whether one more copy may be admitted. It is only an
// optimisation: adherence to CopyConcurrency is enforced by tryAdmitCopy.
func (jt *JobTask) hasCopySlot(limit int) bool {
	jt.copySlotMu.Lock()
	defer jt.copySlotMu.Unlock()
	return jt.copyInFlight < limit
}

// copyStopped reports whether copies must be refused for good (task stopped,
// timed out, or the executor already closed admission for its final wait).
func (jt *JobTask) copyStopped() bool {
	jt.runtimeMu.Lock()
	defer jt.runtimeMu.Unlock()
	return jt.copyAdmissionClosed || jt.BreakFlag.Load() || (jt.ctx != nil && jt.ctx.Err() != nil)
}

// tryAdmitCopy reserves one concurrency slot and a WaitGroup count under
// runtimeMu, the lock admission is closed under. Every copy starts through this
// function, so concurrent starters cannot overshoot CopyConcurrency.
func (jt *JobTask) tryAdmitCopy(limit int) bool {
	jt.runtimeMu.Lock()
	defer jt.runtimeMu.Unlock()
	if jt.copyAdmissionClosed || jt.BreakFlag.Load() || (jt.ctx != nil && jt.ctx.Err() != nil) {
		return false
	}
	jt.copySlotMu.Lock()
	defer jt.copySlotMu.Unlock()
	if jt.copyInFlight >= limit {
		return false
	}
	jt.copyInFlight++
	jt.copyWG.Add(1)
	return true
}

// finishCopySlot releases a slot reserved by tryAdmitCopy once the copy
// goroutine returned.
func (jt *JobTask) finishCopySlot() {
	jt.copyWG.Done()
	jt.copySlotMu.Lock()
	jt.copyInFlight--
	jt.copySlotMu.Unlock()
}

// isDrained reports that the executor exited because all work completed.
func (jt *JobTask) isDrained() bool {
	return jt.drained.Load()
}

// startCopyItemTracked starts item and, when batch is non-nil, also reports
// completion to it. Callers outside the submit executor use the batch handle to
// wait for their own items instead of every copy in flight.
func (jt *JobTask) startCopyItemTracked(item *CopyItem, batch *sync.WaitGroup) bool {
	if !jt.tryAdmitCopy(jt.taskLimits().CopyConcurrency) {
		if jt.copyStopped() {
			item.setStatus(taskStatusStopped)
			jt.appendFinish(item.ToJobTaskItem(jt.TaskID))
		}
		return false
	}
	jt.launchAdmittedCopy(item, batch)
	return true
}

// launchAdmittedCopy starts an item whose slot was already reserved by
// tryAdmitCopy (or waitForCopySlot). It never blocks.
func (jt *JobTask) launchAdmittedCopy(item *CopyItem, batch *sync.WaitGroup) {
	if jt.FirstSync.Load() == 0 {
		jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
	}
	key := jt.QueueNum.Add(1)
	item.DoingKey = key

	jt.DoingMu.Lock()
	jt.Doing[key] = item
	jt.DoingMu.Unlock()

	if batch != nil {
		batch.Add(1)
	}
	go func() {
		defer jt.finishCopySlot()
		if batch != nil {
			defer batch.Done()
		}
		jt.runCopyItem(item)
	}()
}

// waitForCopySlot blocks until the number of in-flight copies drops below limit.
// It reports false when the task was stopped or cancelled while waiting.
func (jt *JobTask) waitForCopySlot(limit int) bool {
	for {
		if jt.copyStopped() {
			return false
		}
		if jt.tryAdmitCopy(limit) {
			return true
		}
		if !jt.waitForBreak(50 * time.Millisecond) {
			return false
		}
	}
}

func (jt *JobTask) runCopyItem(item *CopyItem) {
	defer func() {
		if r := recover(); r != nil {
			errMsg := workerPanicMessage("copy", r)
			jt.failCopyItemIfStillDoing(item, errMsg)
			jt.handleWorkerPanic("copy", r)
		}
	}()
	item.DoIt()
}

func (jt *JobTask) failCopyItemIfStillDoing(item *CopyItem, errMsg string) {
	// Claim the item before recording it: finishCopyItem records the item and
	// only then removes it from Doing, so a panic inside finishCopyItem would
	// leave it "still doing" and this recovery path would record it a second
	// time (double count, duplicate job_task_item row).
	if !jt.claimDoingItem(item) {
		return
	}
	item.setProgress(taskStatusFailed, item.progress(), &errMsg)
	jt.finishCopyItem(item)
}

// claimDoingItem removes item from Doing and reports whether this caller is the
// one that did so. The later delete in finishCopyItem then becomes a no-op.
func (jt *JobTask) claimDoingItem(item *CopyItem) bool {
	if item == nil {
		return false
	}
	jt.DoingMu.Lock()
	defer jt.DoingMu.Unlock()
	if jt.Doing[item.DoingKey] != item {
		return false
	}
	delete(jt.Doing, item.DoingKey)
	return true
}

func (jt *JobTask) markWaitingAsAborted() {
	items := jt.Waiting.closeAndDrain()
	if len(items) == 0 {
		return
	}
	taskItems := make([]JobTaskItem, 0, len(items))
	for _, item := range items {
		item.setStatus(taskStatusStopped)
		taskItems = append(taskItems, item.ToJobTaskItem(jt.TaskID))
	}
	jt.appendFinishMany(taskItems)
}
