package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Sending serially made a finished task wait for the sum of every webhook
// timeout: notifyHTTPClient allows 30s per request, so a handful of unreachable
// destinations held the task for minutes. This drives the real fan-out against
// a server that parks every request until released — if the sends were still
// serial, the second one would never arrive and the test times out.
func TestSendNotifyFanOutSendsConcurrently(t *testing.T) {
	const configs = notifySendConcurrency

	var peakMu sync.Mutex
	var inFlight, peak int64
	release := make(chan struct{})
	var arrived sync.WaitGroup
	arrived.Add(configs)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := atomic.AddInt64(&inFlight, 1)
		peakMu.Lock()
		if current > peak {
			peak = current
		}
		peakMu.Unlock()
		arrived.Done()
		<-release
		atomic.AddInt64(&inFlight, -1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	notifyList := make([]map[string]interface{}, 0, configs)
	for i := 0; i < configs; i++ {
		notifyList = append(notifyList, map[string]interface{}{
			"method": 0,
			"params": fmt.Sprintf(
				`{"url":%q,"httpMethod":"POST","contentType":"application/json","needContent":true,"titleName":"title","contentName":"content"}`,
				fmt.Sprintf("%s/hook/%d", server.URL, i),
			),
		})
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		sendNotifyFanOut(notifyList, "title", "content", false)
	}()

	allArrived := make(chan struct{})
	go func() {
		arrived.Wait()
		close(allArrived)
	}()
	select {
	case <-allArrived:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("not every send was in flight at once: the fan-out is still serial")
	}

	peakMu.Lock()
	got := peak
	peakMu.Unlock()
	if got < 2 {
		t.Errorf("peak concurrent sends = %d, want at least 2", got)
	}

	close(release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sendNotifyFanOut() did not wait for its sends to finish")
	}
}

// The bound must actually hold: a long config list should not open one
// connection per config at once.
func TestSendNotifyFanOutRespectsConcurrencyBound(t *testing.T) {
	const configs = notifySendConcurrency * 3

	var peakMu sync.Mutex
	var inFlight, peak int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := atomic.AddInt64(&inFlight, 1)
		peakMu.Lock()
		if current > peak {
			peak = current
		}
		peakMu.Unlock()
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt64(&inFlight, -1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	notifyList := make([]map[string]interface{}, 0, configs)
	for i := 0; i < configs; i++ {
		notifyList = append(notifyList, map[string]interface{}{
			"method": 0,
			"params": fmt.Sprintf(
				`{"url":%q,"httpMethod":"POST","contentType":"application/json","needContent":true,"titleName":"title","contentName":"content"}`,
				fmt.Sprintf("%s/hook/%d", server.URL, i),
			),
		})
	}
	sendNotifyFanOut(notifyList, "title", "content", false)

	peakMu.Lock()
	got := peak
	peakMu.Unlock()
	if got > notifySendConcurrency {
		t.Fatalf("peak concurrent sends = %d, want at most %d", got, notifySendConcurrency)
	}
}

// A panic from one bad config must not prevent the others from being attempted,
// and must not escape into the task completion path.
func TestSendNotifyFanOutIsolatesPanics(t *testing.T) {
	var delivered int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&delivered, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	notifyList := []map[string]interface{}{
		{"method": 0, "params": `{"url":"not a url"}`},
		{"method": 99, "params": `{}`},
		{"method": -1, "params": `not json`},
		{
			"method": 0,
			"params": fmt.Sprintf(
				`{"url":%q,"httpMethod":"POST","contentType":"application/json","needContent":true,"titleName":"title","contentName":"content"}`,
				server.URL+"/hook",
			),
		},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("sendNotifyFanOut() leaked a panic: %v", r)
			}
		}()
		sendNotifyFanOut(notifyList, "title", "content", false)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("sendNotifyFanOut() did not return")
	}

	// The three broken configs panic; the healthy one must still be delivered.
	if got := atomic.LoadInt64(&delivered); got != 1 {
		t.Fatalf("delivered = %d, want the one healthy config to be sent", got)
	}
}

// Bounding the fan-out keeps a large config list from opening one connection
// per config at once.
func TestNotifySendConcurrencyIsBounded(t *testing.T) {
	if notifySendConcurrency < 2 || notifySendConcurrency > 16 {
		t.Fatalf("notifySendConcurrency = %d, want a small bound above 1", notifySendConcurrency)
	}
}
