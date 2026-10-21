package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// admissionTestClient is a copy client whose transfers always fail, so a copy
// item admitted by the executor reaches its terminal state quickly and without
// network I/O.
type admissionTestClient struct {
	mu       sync.Mutex
	denied   int
	wasAsked bool
}

var errAdmissionDenied = errors.New("admission test: transfer denied")

func (c *admissionTestClient) note() {
	c.mu.Lock()
	c.wasAsked = true
	c.denied++
	c.mu.Unlock()
}

func (c *admissionTestClient) CopyFileContext(context.Context, string, string, string) (string, error) {
	c.note()
	return "", errAdmissionDenied
}

func (c *admissionTestClient) MoveFileContext(context.Context, string, string, string) (string, error) {
	c.note()
	return "", errAdmissionDenied
}

func (c *admissionTestClient) TaskCancelContext(context.Context, string, taskItemType) error {
	return nil
}

func (c *admissionTestClient) TaskDeleteContext(context.Context, string, taskItemType) error {
	return nil
}

func (c *admissionTestClient) TaskInfoContext(context.Context, string, taskItemType) (map[string]interface{}, error) {
	return nil, errAdmissionDenied
}

func (c *admissionTestClient) TaskUndoneListContext(context.Context, taskItemType) ([]map[string]interface{}, error) {
	return nil, errAdmissionDenied
}

func (c *admissionTestClient) DeleteFileContext(context.Context, string, []string, int) error {
	return errAdmissionDenied
}

func (c *admissionTestClient) FileExistsContext(context.Context, string, string) (bool, error) {
	return false, errAdmissionDenied
}

// The final copyWG.Wait may only start after admission is closed: a copyWG.Add
// landing during that Wait is the WaitGroup misuse that panicked the submit
// worker ("reused before previous Wait has returned") and started AList
// transfers after the task had been reported finished.
func TestCopyAdmissionRejectsItemsAfterClose(t *testing.T) {
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	jt := &JobTask{TaskID: 3, Job: map[string]interface{}{}}
	jt.initRuntime()
	jt.closeCopyAdmission()

	item := newCopyItem(jt, &admissionTestClient{}, "/src/", "/dst/", "a.bin", int64(1), taskItemTypeCopy)
	if jt.startCopyItemTracked(item, nil) {
		t.Fatal("startCopyItemTracked() admitted a copy after admission was closed")
	}
	if got := item.status(); got != taskStatusStopped {
		t.Fatalf("item status = %d, want stopped", got)
	}
	if n := jt.doingLen(); n != 0 {
		t.Fatalf("Doing len = %d, want 0", n)
	}
	if err := jt.persistRemainingTaskItems(); err != nil {
		t.Fatalf("persistRemainingTaskItems() error = %v", err)
	}
	if len(persisted) != 1 || persisted[0]["status"] != taskStatusStopped.Int() {
		t.Fatalf("persisted = %#v, want one stopped item", persisted)
	}
}

// Every accepted Add happens-before the executor's copyWG.Wait, so concurrent
// starts racing the close must not panic and must leave nothing in Doing.
func TestCopyAdmissionAddAndWaitDoNotRace(t *testing.T) {
	d := *jobDeps
	d.CopyRetryDelay = func(int) time.Duration { return 0 }
	restore := SetJobDepsForTest(&d)
	defer restore()

	jt := &JobTask{TaskID: 3, Job: map[string]interface{}{}}
	jt.initRuntime()
	t.Cleanup(jt.cancelPersistFlush)

	const starters = 32
	var started sync.WaitGroup
	for i := 0; i < starters; i++ {
		started.Add(1)
		go func() {
			defer started.Done()
			item := newCopyItem(jt, &admissionTestClient{}, "/src/", "/dst/", "a.bin", int64(1), taskItemTypeCopy)
			jt.startCopyItemTracked(item, nil)
		}()
	}
	jt.closeCopyAdmission()
	// Panics with "sync: WaitGroup is reused before previous Wait has returned"
	// if an Add races this Wait, and leaks in-flight copies into the result.
	jt.copyWG.Wait()
	started.Wait()

	if n := jt.doingLen(); n != 0 {
		t.Fatalf("Doing len after the wait = %d, want every copy finished", n)
	}
}

// finishCopyItem records the item before removing it from Doing, so a panic path
// that re-enters the recovery must not record the same item twice.
func TestCopyPanicRecoveryRecordsItemOnce(t *testing.T) {
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	jt := &JobTask{TaskID: 3, Job: map[string]interface{}{}}
	jt.initRuntime()
	item := newCopyItem(jt, &admissionTestClient{}, "/src/", "/dst/", "a.bin", int64(1), taskItemTypeCopy)
	item.DoingKey = 1
	jt.DoingMu.Lock()
	jt.Doing[1] = item
	jt.DoingMu.Unlock()

	jt.failCopyItemIfStillDoing(item, "worker panic: boom")
	// A second recovery for the same item (the panic-unwind path) must be a
	// no-op: the item was already claimed and recorded.
	jt.failCopyItemIfStillDoing(item, "worker panic: boom")

	if n := jt.doingLen(); n != 0 {
		t.Fatalf("Doing len = %d, want the item claimed by the first recovery", n)
	}
	if err := jt.persistRemainingTaskItems(); err != nil {
		t.Fatalf("persistRemainingTaskItems() error = %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("persisted len = %d, want the item recorded exactly once", len(persisted))
	}
}

// The task-scoped context is cancelled when the task finishes, so its
// WithTimeout timer does not stay armed until the configured deadline (one per
// finished task).
func TestCancelTaskRuntimeCancelsTaskContext(t *testing.T) {
	jt := &JobTask{TaskID: 3, Job: map[string]interface{}{}}
	jt.initRuntime()
	ctx := jt.context()
	if ctx.Err() != nil {
		t.Fatal("fresh task context is already cancelled")
	}

	jt.cancelTaskRuntime()

	if ctx.Err() == nil {
		t.Fatal("cancelTaskRuntime() left the task context live")
	}
	jt.persistFlushMu.Lock()
	scheduled := jt.persistFlushScheduled
	jt.persistFlushMu.Unlock()
	if scheduled {
		t.Fatal("cancelTaskRuntime() left the persist flush scheduled")
	}
}

// The executor and the full-sync relocation path start copies from two
// goroutines; each used to check "is there room" separately and could overshoot
// CopyConcurrency. Admission now reserves a slot atomically.
func TestCopyAdmissionCapsConcurrentCopies(t *testing.T) {
	jt := &JobTask{cachedLimits: &taskRuntimeLimits{CopyConcurrency: 1}}
	if !jt.tryAdmitCopy(1) {
		t.Fatal("first admission was rejected")
	}
	if jt.tryAdmitCopy(1) {
		t.Fatal("second admission exceeded CopyConcurrency=1")
	}
	jt.finishCopySlot()
	if !jt.tryAdmitCopy(1) {
		t.Fatal("slot was not released by finishCopySlot")
	}
	jt.finishCopySlot()
}
