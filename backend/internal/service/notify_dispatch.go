package service

import (
	"context"
	"log"
	"sync"
)

const (
	// notifyQueueCapacity bounds the backlog. Task completion used to call
	// delivery inline, so one unreachable webhook held the finishing goroutine
	// for the full 30s client timeout (60s for WeCom, which needs two
	// round-trips) times the number of configs. The queue decouples the two;
	// the cap keeps a wedged provider from growing the backlog without limit.
	notifyQueueCapacity = 64
	// notifyWorkerCount is deliberately small: notifications are not latency
	// critical, and more workers would only multiply concurrent outbound
	// requests to the same provider.
	notifyWorkerCount = 2
	// notifySendConcurrency bounds the fan-out across configs within one
	// notification, so N configs cost about one timeout instead of N. The
	// process-wide ceiling is notifyWorkerCount * notifySendConcurrency; this
	// constant alone is not a global limit.
	notifySendConcurrency = 4
)

// notifyJob is one task-completion notification, already detached from the task
// runtime so the worker never reads task state that has moved on.
type notifyJob struct {
	taskID     int64
	status     int
	taskNum    map[string]interface{}
	duration   int
	createTime float64
}

type notifyDispatcher struct {
	mu    sync.Mutex
	queue chan notifyJob
	// handle is a seam: tests drive the queue and shutdown mechanics without
	// reaching the database or a real webhook.
	handle  func(notifyJob)
	wg      sync.WaitGroup
	started bool
	closed  bool
	// ctx scopes every outbound notification request. shutdown cancels it
	// when its wait expires, so in-flight HTTP calls abort instead of running
	// on (and recording their outcome) after the database has been closed.
	ctx    context.Context
	cancel context.CancelFunc
}

func newNotifyDispatcher(handle func(notifyJob)) *notifyDispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	return &notifyDispatcher{
		queue:  make(chan notifyJob, notifyQueueCapacity),
		handle: handle,
		ctx:    ctx,
		cancel: cancel,
	}
}

// The handler is installed in init rather than in the variable initializer:
// referencing deliverTaskNotification here would form an initialization cycle,
// because the delivery path reaches notifyBaseContext, which reads
// notifyDispatch.
var notifyDispatch = newNotifyDispatcher(nil)

func init() {
	notifyDispatch.handle = deliverTaskNotification
}

// notifyBaseContext is the parent context of every notification request.
func notifyBaseContext() context.Context {
	return notifyDispatch.context()
}

func (d *notifyDispatcher) context() context.Context {
	if d == nil || d.ctx == nil {
		return context.Background()
	}
	return d.ctx
}

// StartNotifyDispatcher launches the delivery workers. It is idempotent so the
// lazy start in enqueue and the explicit start at boot cannot double-spawn.
func StartNotifyDispatcher() {
	notifyDispatch.start()
}

// ShutdownNotifyDispatcher stops accepting notifications and waits for the
// queued ones, giving up when ctx expires rather than holding up process exit.
func ShutdownNotifyDispatcher(ctx context.Context) {
	notifyDispatch.shutdown(ctx)
}

// QueueTaskNotification hands a finished task's notification to the background
// workers. It never blocks the caller: a full queue drops the notification with
// a log line, which is the lesser evil compared to stalling task completion.
func QueueTaskNotification(taskID int64, status int, taskNum map[string]interface{}, duration int, createTime float64) {
	queued := notifyDispatch.enqueue(notifyJob{
		taskID:     taskID,
		status:     status,
		taskNum:    copyTaskNum(taskNum),
		duration:   duration,
		createTime: createTime,
	})
	if !queued {
		log.Printf("Dropped completion notification for task %d: notify queue is full or shutting down", taskID)
	}
}

// copyTaskNum detaches the counts map from the task runtime. The caller keeps
// using its map after handing the job over, and the worker reads it later.
func copyTaskNum(taskNum map[string]interface{}) map[string]interface{} {
	if taskNum == nil {
		return nil
	}
	copied := make(map[string]interface{}, len(taskNum))
	for k, v := range taskNum {
		copied[k] = v
	}
	return copied
}

func (d *notifyDispatcher) start() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.startLocked()
}

func (d *notifyDispatcher) startLocked() {
	if d.started || d.closed {
		return
	}
	d.started = true
	for i := 0; i < notifyWorkerCount; i++ {
		d.wg.Add(1)
		go d.work()
	}
}

func (d *notifyDispatcher) work() {
	defer d.wg.Done()
	for job := range d.queue {
		d.runJob(job)
	}
}

// runJob isolates one notification: delivery panics on malformed configs, and a
// panic escaping here would take down a worker (and with it every later
// notification) instead of just this one.
func (d *notifyDispatcher) runJob(job notifyJob) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Notification for task %d failed: %v", job.taskID, r)
		}
	}()
	d.handle(job)
}

func (d *notifyDispatcher) enqueue(job notifyJob) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return false
	}
	d.startLocked()
	select {
	case d.queue <- job:
		return true
	default:
		return false
	}
}

func (d *notifyDispatcher) shutdown(ctx context.Context) {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	started := d.started
	close(d.queue)
	d.mu.Unlock()

	if !started {
		return
	}
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		log.Printf("Gave up waiting for pending notifications to finish; cancelling in-flight requests")
		if d.cancel != nil {
			d.cancel()
		}
	}
}
