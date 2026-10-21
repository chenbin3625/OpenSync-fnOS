package service

import (
	"context"
	"fmt"
	"log"
	"opensync/internal/mapper"
	"time"
)

// persistBusyRetryDelays are the waits between attempts when an item write
// fails because another connection holds the SQLite write lock. Each attempt
// already waits busy_timeout (15s) inside SQLite, and the mapper retries once
// more on its own, so five attempts cover well over a minute of contention —
// enough for a history cleanup batch, a checkpoint or a user's bulk delete to
// finish — before the task gives up.
var persistBusyRetryDelays = []time.Duration{
	250 * time.Millisecond,
	500 * time.Millisecond,
	time.Second,
	2 * time.Second,
}

// isSQLiteBusy reports whether err is lock contention rather than a real failure.
func isSQLiteBusy(err error) bool {
	return mapper.IsBusyError(err)
}

// persistWithBusyRetry writes items, retrying only lock contention. A busy
// database used to fail the flush on the first attempt, and a failed flush
// records a persistence error and breaks the task — so any writer holding the
// lock past busy_timeout aborted a healthy sync. Other errors are returned at
// once, and the retry stops early when the task is stopped or times out.
func (jt *JobTask) persistWithBusyRetry(items []map[string]interface{}) error {
	return jt.persistWithBusyRetryCtx(jt.context(), true, items)
}

// persistWithBusyRetryCtx is the retry loop. stopOnBreak ends it early when
// the task is stopped: right for writes made while the task runs, wrong for
// the final flush, which runs precisely because the task stopped (or timed
// out, cancelling ctx) and is the last chance to save those items. The final
// flush therefore passes stopOnBreak=false with a cleanup context.
func (jt *JobTask) persistWithBusyRetryCtx(ctx context.Context, stopOnBreak bool, items []map[string]interface{}) error {
	err := jobDeps.PersistJobTaskItems(items)
	for attempt := 0; attempt < len(persistBusyRetryDelays) && isSQLiteBusy(err); attempt++ {
		if stopOnBreak && jt.isBreak() {
			return err
		}
		log.Printf("Task %d: database busy while saving %d task items, retrying (%d/%d)",
			jt.TaskID, len(items), attempt+1, len(persistBusyRetryDelays))
		timer := time.NewTimer(persistBusyRetryDelays[attempt])
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
		err = jobDeps.PersistJobTaskItems(items)
	}
	return err
}

func (jt *JobTask) persistRemainingTaskItems() error {
	// Seal first: from here on no debounced flush may ever be armed for this
	// task. Cancelling alone was not enough - a writer that outlived the
	// executor (a scan goroutine recording one last item, a panic path) called
	// schedulePersistFlush again and wrote to the database after the task had
	// reached its terminal status, possibly after ShutdownDB closed the handle.
	jt.sealPersistFlush()
	// A stopped or timed-out task has a cancelled context and its break flag
	// set, which ended the busy retry at once: the burst of Stopped items
	// written on stop then failed on the first lock contention and the task
	// was reported as a system failure. The final flush keeps retrying,
	// bounded by its own cleanup deadline.
	ctx, cancel := jt.cleanupContext()
	defer cancel()
	if err := jt.flushPersistBufferWith(func(items []map[string]interface{}) error {
		return jt.persistWithBusyRetryCtx(ctx, false, items)
	}); err != nil {
		return err
	}
	return jt.taskPersistenceError()
}

func (jt *JobTask) appendFinish(item JobTaskItem) {
	jt.appendFinishMany([]JobTaskItem{item})
}

func (jt *JobTask) appendFinishMany(items []JobTaskItem) {
	if len(items) == 0 {
		return
	}
	jt.initRuntime()

	jt.FinishMu.Lock()
	for _, item := range items {
		status := item.Status
		jt.FinishedCounts[status]++
		if size := item.CountableFileSize(); size > 0 {
			jt.FinishedSizes[status] += size
		}
	}
	jt.FinishMu.Unlock()

	jt.persistBufMu.Lock()
	jt.persistBuffer = append(jt.persistBuffer, items...)
	needFlush := len(jt.persistBuffer) >= persistBatchSize
	jt.persistBufMu.Unlock()

	if needFlush || jt.persistFlushSealed() {
		// A sealed task already performed its final flush, so this record
		// arrived late (it should not happen: the executor waits for the scan
		// worker and every copy before sealing). Write it synchronously instead
		// of arming a timer that would outlive the task.
		if err := jt.flushPersistBuffer(); err != nil {
			jt.recordTaskPersistenceError(err)
			jt.requestBreak()
		}
	} else {
		jt.schedulePersistFlush()
	}
	jt.notifyProgressChange()
}

// Persist-flush lifecycle. persistFlushMu guards two fields that encode three
// states:
//
//	idle   (scheduled=false, timer=nil): nothing pending, a timer may be armed
//	armed  (scheduled=true,  timer!=nil): one debounced flush is pending
//	sealed (scheduled=true,  timer=nil): the final flush already ran; no timer
//	                                     may ever be armed again
//
// Sealing is what makes "the final flush is the last write" enforceable: a
// writer that outlives the executor used to re-arm the debounce and write after
// the task finished (and after ShutdownDB at shutdown). Sealed tasks write any
// late record synchronously instead, so nothing buffered is lost and no timer
// outlives the task.
func (jt *JobTask) sealPersistFlush() {
	jt.persistFlushMu.Lock()
	timer := jt.persistFlushTimer
	jt.persistFlushTimer = nil
	jt.persistFlushScheduled = true
	jt.persistFlushMu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

func (jt *JobTask) persistFlushSealed() bool {
	jt.persistFlushMu.Lock()
	defer jt.persistFlushMu.Unlock()
	return jt.persistFlushScheduled && jt.persistFlushTimer == nil
}

func (jt *JobTask) schedulePersistFlush() {
	jt.persistFlushMu.Lock()
	defer jt.persistFlushMu.Unlock()
	if jt.persistFlushScheduled {
		// Armed, or sealed: either way no second timer may be created.
		return
	}
	jt.persistFlushScheduled = true
	jt.persistFlushTimer = time.AfterFunc(persistFlushInterval, func() {
		// This runs on the timer's own goroutine, which no Gin middleware
		// covers: an unrecovered panic here takes the whole NAS app down. The
		// progress hub's debounced timer recovers for the same reason.
		defer func() {
			if r := recover(); r != nil {
				err := fmt.Errorf("persist flush panic: %v", r)
				log.Printf("Task %d %v", jt.TaskID, err)
				jt.recordTaskPersistenceError(err)
				jt.requestBreak()
			}
		}()
		jt.persistFlushMu.Lock()
		// Only the timer still registered here may clear the state: if the task
		// sealed (or cancelled) while this callback was already running, the
		// seal must survive or a later append could arm a new timer after the
		// final flush.
		ownsTimer := jt.persistFlushTimer != nil
		if ownsTimer {
			jt.persistFlushScheduled = false
			jt.persistFlushTimer = nil
		}
		sealed := jt.persistFlushScheduled && jt.persistFlushTimer == nil
		jt.persistFlushMu.Unlock()
		if !ownsTimer && !sealed {
			// Cancelled before this callback took the lock; the final flush that
			// cancelled it writes the buffer itself.
			return
		}
		if err := jt.flushPersistBuffer(); err != nil {
			jt.recordTaskPersistenceError(err)
			jt.requestBreak()
		}
	})
}

// cancelPersistFlush stops a pending debounced flush. Without it the timer
// outlives the task and fires a write after the run is finished — reaching
// GetDB even after CloseDB has torn the handle down at shutdown. The final
// flush is performed synchronously by persistRemainingTaskItems, so nothing
// buffered is lost by cancelling here. A sealed task keeps its sealed state:
// cancelling must never re-open the door for another timer.
func (jt *JobTask) cancelPersistFlush() {
	jt.persistFlushMu.Lock()
	sealed := jt.persistFlushScheduled && jt.persistFlushTimer == nil
	timer := jt.persistFlushTimer
	jt.persistFlushTimer = nil
	if !sealed {
		jt.persistFlushScheduled = false
	}
	jt.persistFlushMu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

func (jt *JobTask) flushPersistBuffer() error {
	return jt.flushPersistBufferWith(jt.persistWithBusyRetry)
}

func (jt *JobTask) flushPersistBufferWith(persist func([]map[string]interface{}) error) error {
	// Serialize the write itself. Without this the final flush can observe an
	// empty buffer while a timer flush is still writing, report success, and
	// only then have that write fail and push the items back — losing them
	// after the task was already marked successful.
	jt.persistFlushInFlight.Lock()
	defer jt.persistFlushInFlight.Unlock()
	jt.persistBufMu.Lock()
	if len(jt.persistBuffer) == 0 {
		jt.persistBufMu.Unlock()
		return jt.taskPersistenceError()
	}
	items := append([]JobTaskItem(nil), jt.persistBuffer...)
	jt.persistBuffer = jt.persistBuffer[:0]
	jt.persistBufMu.Unlock()

	if err := persist(jobTaskItemsToMaps(items)); err != nil {
		jt.persistBufMu.Lock()
		jt.persistBuffer = append(items, jt.persistBuffer...)
		jt.persistBufMu.Unlock()

		first := items[0]
		log.Printf(
			"Failed to save %d task items for task %d (job %d) first file=%q: %v",
			len(items), jt.TaskID, jt.jobIDSnapshot(), first.FileName, err,
		)
		jt.recordTaskPersistenceError(err)
		jt.requestBreak()
		return err
	}
	return jt.taskPersistenceError()
}

func (jt *JobTask) recordTaskPersistenceError(err error) {
	if err == nil {
		return
	}
	jt.PersistMu.Lock()
	defer jt.PersistMu.Unlock()
	if jt.PersistErr == nil {
		jt.PersistErr = err
	}
}

func (jt *JobTask) taskPersistenceError() error {
	jt.PersistMu.Lock()
	defer jt.PersistMu.Unlock()
	return jt.PersistErr
}
