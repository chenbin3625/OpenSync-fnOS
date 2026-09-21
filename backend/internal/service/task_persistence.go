package service

import (
	"fmt"
	"log"
	"time"
)

func (jt *JobTask) persistRemainingTaskItems() error {
	// Cancel first: a timer that fires during/after the final flush would write
	// again behind the task's back.
	jt.cancelPersistFlush()
	if err := jt.flushPersistBuffer(); err != nil {
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

	if needFlush {
		if err := jt.flushPersistBuffer(); err != nil {
			jt.recordTaskPersistenceError(err)
			jt.requestBreak()
		}
	} else {
		jt.schedulePersistFlush()
	}
	jt.notifyProgressChange()
}

func (jt *JobTask) schedulePersistFlush() {
	jt.persistFlushMu.Lock()
	defer jt.persistFlushMu.Unlock()
	if jt.persistFlushScheduled {
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
		jt.persistFlushScheduled = false
		jt.persistFlushTimer = nil
		jt.persistFlushMu.Unlock()
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
// buffered is lost by cancelling here.
func (jt *JobTask) cancelPersistFlush() {
	jt.persistFlushMu.Lock()
	timer := jt.persistFlushTimer
	jt.persistFlushTimer = nil
	jt.persistFlushScheduled = false
	jt.persistFlushMu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

func (jt *JobTask) flushPersistBuffer() error {
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

	if err := persistJobTaskItems(jobTaskItemsToMaps(items)); err != nil {
		jt.persistBufMu.Lock()
		jt.persistBuffer = append(items, jt.persistBuffer...)
		jt.persistBufMu.Unlock()

		jobID := int64(0)
		if jt.JobClient != nil {
			jobID = jt.JobClient.JobID
		}
		first := items[0]
		log.Printf(
			"Failed to save %d task items for task %d (job %d) first file=%q: %v",
			len(items), jt.TaskID, jobID, first.FileName, err,
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
