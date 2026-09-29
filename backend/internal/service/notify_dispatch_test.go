package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestDispatcher(capacity int, handle func(notifyJob)) *notifyDispatcher {
	return &notifyDispatcher{queue: make(chan notifyJob, capacity), handle: handle}
}

// Task completion used to call delivery inline, so an unreachable webhook held
// the finishing goroutine for a full client timeout. Enqueueing must return
// immediately even while a worker is parked inside a send.
func TestEnqueueDoesNotWaitForDelivery(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	dispatcher := newTestDispatcher(4, func(notifyJob) {
		started <- struct{}{}
		<-release
	})
	defer func() {
		close(release)
		dispatcher.shutdown(context.Background())
	}()

	if !dispatcher.enqueue(notifyJob{taskID: 1}) {
		t.Fatalf("enqueue() = false, want the first job accepted")
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatalf("worker never picked up the queued notification")
	}

	done := make(chan bool, 1)
	go func() { done <- dispatcher.enqueue(notifyJob{taskID: 2}) }()
	select {
	case accepted := <-done:
		if !accepted {
			t.Fatalf("enqueue() = false, want the second job queued behind the parked worker")
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("enqueue() blocked while a delivery was in flight")
	}
}

// A wedged provider must not grow the backlog without limit: once the queue is
// full, enqueue reports the drop instead of blocking task completion.
func TestEnqueueDropsWhenQueueIsFull(t *testing.T) {
	const capacity = 1
	release := make(chan struct{})
	started := make(chan struct{}, notifyWorkerCount)
	dispatcher := newTestDispatcher(capacity, func(notifyJob) {
		started <- struct{}{}
		<-release
	})
	defer func() {
		close(release)
		dispatcher.shutdown(context.Background())
	}()

	// Park every worker inside a delivery first. Enqueueing in a tight loop and
	// then asserting raced with the workers: one could still pull a job off the
	// queue after the loop, freeing the slot the final assertion expected full.
	for i := 0; i < notifyWorkerCount; i++ {
		if !dispatcher.enqueue(notifyJob{taskID: int64(i)}) {
			t.Fatalf("enqueue(%d) = false before any worker was busy", i)
		}
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatalf("worker %d never picked up its job", i)
		}
	}
	for i := 0; i < capacity; i++ {
		if !dispatcher.enqueue(notifyJob{taskID: int64(100 + i)}) {
			t.Fatalf("enqueue() = false with queue room left")
		}
	}
	for i := 0; i < 8; i++ {
		if dispatcher.enqueue(notifyJob{taskID: int64(200 + i)}) {
			t.Fatalf("enqueue() = true on a saturated dispatcher, want a drop")
		}
	}
}

// Queued notifications describe work that already finished; shutdown flushes
// them rather than discarding them.
func TestShutdownDrainsQueuedNotifications(t *testing.T) {
	var delivered int64
	var wg sync.WaitGroup
	wg.Add(1)
	blocked := make(chan struct{})
	dispatcher := newTestDispatcher(8, func(notifyJob) {
		<-blocked
		atomic.AddInt64(&delivered, 1)
	})

	const queued = 5
	for i := 0; i < queued; i++ {
		if !dispatcher.enqueue(notifyJob{taskID: int64(i)}) {
			t.Fatalf("enqueue(%d) = false, want it queued", i)
		}
	}
	go func() {
		defer wg.Done()
		dispatcher.shutdown(context.Background())
	}()
	close(blocked)
	wg.Wait()

	if got := atomic.LoadInt64(&delivered); got != queued {
		t.Fatalf("delivered %d notifications, want %d", got, queued)
	}
}

// Shutdown must not outlive its deadline: a webhook that never answers would
// otherwise hold up process exit.
func TestShutdownGivesUpOnDeadline(t *testing.T) {
	release := make(chan struct{})
	dispatcher := newTestDispatcher(2, func(notifyJob) { <-release })
	defer close(release)

	if !dispatcher.enqueue(notifyJob{taskID: 1}) {
		t.Fatalf("enqueue() = false, want the job queued")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		dispatcher.shutdown(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("shutdown() ignored its context deadline")
	}
}

// Delivery panics on malformed configs. A panic escaping a worker would kill it
// and silently stop every later notification.
func TestWorkerSurvivesDeliveryPanic(t *testing.T) {
	handled := make(chan int64, 2)
	dispatcher := newTestDispatcher(4, func(job notifyJob) {
		handled <- job.taskID
		if job.taskID == 1 {
			panic("delivery exploded")
		}
	})
	defer dispatcher.shutdown(context.Background())

	dispatcher.enqueue(notifyJob{taskID: 1})
	dispatcher.enqueue(notifyJob{taskID: 2})

	seen := map[int64]bool{}
	for i := 0; i < 2; i++ {
		select {
		case id := <-handled:
			seen[id] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("only saw %v, want both notifications handled after a panic", seen)
		}
	}
	if !seen[1] || !seen[2] {
		t.Fatalf("handled %v, want both task 1 and task 2", seen)
	}
}

func TestEnqueueRejectedAfterShutdown(t *testing.T) {
	dispatcher := newTestDispatcher(2, func(notifyJob) {})
	dispatcher.shutdown(context.Background())

	if dispatcher.enqueue(notifyJob{taskID: 1}) {
		t.Fatalf("enqueue() = true after shutdown, want rejection")
	}
	// Shutdown closes the queue, so a second call must not close it again.
	dispatcher.shutdown(context.Background())
}

// The counts map keeps being read by the caller after the job is handed over.
func TestQueuedTaskNumIsDetachedFromCaller(t *testing.T) {
	original := map[string]interface{}{"allNum": 3}
	copied := copyTaskNum(original)
	original["allNum"] = 99

	if copied["allNum"] != 3 {
		t.Fatalf("copied allNum = %v, want the value captured at enqueue time", copied["allNum"])
	}
	if copyTaskNum(nil) != nil {
		t.Fatalf("copyTaskNum(nil) != nil")
	}
}
