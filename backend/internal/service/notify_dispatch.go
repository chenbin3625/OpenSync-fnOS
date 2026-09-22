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
	// notification, so N configs cost about one timeout instead of N.
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
}

var notifyDispatch = &notifyDispatcher{
	queue:  make(chan notifyJob, notifyQueueCapacity),
	handle: deliverTaskNotification,
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
		log.Printf("Gave up waiting for pending notifications to finish")
	}
}
