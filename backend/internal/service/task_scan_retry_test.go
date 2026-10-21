package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"opensync/internal/msg"
)

func TestListDirRetriesTransientAListFailures(t *testing.T) {
	d := *jobDeps
	d.ScanListRetryDelay = func(int) time.Duration { return 0 }
	restore := SetJobDepsForTest(&d)
	defer restore()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if attempts.Add(1) <= maxScanListRetries {
			// A network-style failure: retryable, unlike "object not found".
			_, _ = w.Write([]byte(`{"code":500,"message":"connection reset by peer","data":null}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"total":1,"content":[{"name":"file.txt","is_dir":false,"size":10}]}}`))
	}))
	defer server.Close()

	jt := newScanRetryTestTask(server)
	files, err := jt.listDir("/dst/", true, nil, "/dst/", false)
	if err != nil {
		t.Fatalf("listDir() error: %v", err)
	}
	if attempts.Load() != maxScanListRetries+1 {
		t.Fatalf("list attempts = %d, want %d", attempts.Load(), maxScanListRetries+1)
	}
	metadata, ok := files["file.txt"]
	if !ok || metadata.Size != 10 {
		t.Fatalf("listed file metadata = %#v, want size 10", files["file.txt"])
	}
}

// "The object does not exist" is repaired by creating the destination directory,
// not by waiting: retrying only added the whole backoff to a first sync, and
// the failure item it eventually recorded made the run report partial failure.
func TestListDirDoesNotRetryMissingObject(t *testing.T) {
	d := *jobDeps
	var delays atomic.Int32
	d.ScanListRetryDelay = func(int) time.Duration {
		delays.Add(1)
		return 0
	}
	restore := SetJobDepsForTest(&d)
	defer restore()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":500,"message":"failed get objs: failed get dir: object not found","data":null}`))
	}))
	defer server.Close()

	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	jt := newScanRetryTestTask(server)
	if _, err := jt.listDir("/dst/", true, nil, "/dst/", false); err == nil {
		t.Fatal("listDir() error = nil, want the missing-object error")
	}
	if attempts.Load() != 1 {
		t.Fatalf("list attempts = %d, want 1 (no retry backoff for a missing object)", attempts.Load())
	}
	if delays.Load() != 0 {
		t.Fatalf("retry delays = %d, want 0", delays.Load())
	}
	if err := jt.flushPersistBuffer(); err != nil {
		t.Fatalf("flushPersistBuffer() error: %v", err)
	}
	// The caller (listSrcAndDst / syncFull) owns this error: it creates the
	// directory and only records a failure when that repair fails.
	if len(persisted) != 0 {
		t.Fatalf("persisted = %#v, want no failure item for a repairable missing root", persisted)
	}
}

// A cancel during the retry backoff must not replace the root failure with a
// bare context.Canceled: errors.Is has to keep working for both causes.
func TestListDirCancelDuringRetryKeepsOriginalError(t *testing.T) {
	var once atomic.Bool
	jt := &JobTask{TaskID: 42, Job: map[string]interface{}{}}
	jt.initRuntime()
	d := *jobDeps
	d.ScanListRetryDelay = func(int) time.Duration {
		if once.CompareAndSwap(false, true) {
			jt.requestBreak()
		}
		return time.Millisecond
	}
	restore := SetJobDepsForTest(&d)
	defer restore()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":500,"message":"connection reset by peer","data":null}`))
	}))
	defer server.Close()
	jt.AlistClient = &AlistClient{URL: server.URL, client: server.Client()}

	_, err := jt.listDir("/dst/", true, nil, "/dst/", false)
	if err == nil {
		t.Fatal("listDir() error = nil, want the cancelled retry")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want it to wrap context.Canceled", err)
	}
	if !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("error = %v, want the original AList failure preserved", err)
	}
}

func TestListDirDoesNotRetryAuthenticationFailure(t *testing.T) {
	d := *jobDeps
	d.ScanListRetryDelay = func(int) time.Duration { return 0 }
	restore := SetJobDepsForTest(&d)
	defer restore()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":401,"message":"unauthorized","data":null}`))
	}))
	defer server.Close()

	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	jt := newScanRetryTestTask(server)
	if _, err := jt.listDir("/dst/", true, nil, "/dst/", false); err == nil {
		t.Fatal("listDir() error = nil, want authentication error")
	}
	if err := jt.flushPersistBuffer(); err != nil {
		t.Fatalf("flushPersistBuffer() error: %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("list attempts = %d, want 1", attempts.Load())
	}
	if len(persisted) != 1 {
		t.Fatalf("persisted failures = %d, want 1", len(persisted))
	}
}

func TestShouldRetryScanListHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if shouldRetryScanList(ctx, errors.New("temporary failure")) {
		t.Fatal("shouldRetryScanList() = true for canceled context")
	}
	if shouldRetryScanList(context.Background(), errors.New(msg.T(msg.AlistUnAuth))) {
		t.Fatal("shouldRetryScanList() = true for authentication failure")
	}
	if !shouldRetryScanList(context.Background(), errors.New("temporary failure")) {
		t.Fatal("shouldRetryScanList() = false for transient failure")
	}
	// "object not found" is repaired by creating the directory, never by
	// retrying, so it must not be classified as transient.
	missing := &alistStatusError{httpStatus: http.StatusOK, alistCode: 500, message: "failed get dir: object not found"}
	if shouldRetryScanList(context.Background(), missing) {
		t.Fatal("shouldRetryScanList() = true for a missing object")
	}
}

func newScanRetryTestTask(server *httptest.Server) *JobTask {
	client := server.Client()
	client.Timeout = time.Second
	jt := &JobTask{
		TaskID: 42,
		Job:    map[string]interface{}{},
		AlistClient: &AlistClient{
			URL:    server.URL,
			client: client,
		},
	}
	jt.initRuntime()
	return jt
}
