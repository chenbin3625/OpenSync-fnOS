package service

import (
	"context"
	"encoding/json"
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

func TestSyncWithoutHaveKeepsDestinationOnSourceScanFailure(t *testing.T) {
	var persisted []map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/fs/mkdir":
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{}}`))
		case "/api/fs/list":
			_, _ = w.Write([]byte(`{"code":401,"message":"unauthorized","data":null}`))
		default:
			t.Errorf("unexpected request path %s", r.URL.Path)
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{}}`))
		}
	}))
	defer server.Close()

	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()
	jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{
		"scanIntervalS": 0,
		"scanIntervalT": 0,
	})
	jt.syncWithoutHave(scanWork{
		SrcPath:     "/src/dir/",
		DstPath:     "/dst/dir/",
		SrcRootPath: "/src/",
		DstRootPath: "/dst/",
		FirstDst:    true,
		Mode:        scanWorkMissingDst,
	}, nil)
	if err := jt.flushPersistBuffer(); err != nil {
		t.Fatalf("flushPersistBuffer() error: %v", err)
	}

	var failure map[string]interface{}
	for _, item := range persisted {
		if item["status"] == taskStatusFailed.Int() {
			failure = item
			break
		}
	}
	if failure == nil {
		t.Fatalf("persisted = %#v, want a source scan failure", persisted)
	}
	if failure["srcPath"] != "/src/dir/" || failure["dstPath"] != "/dst/dir/" {
		t.Fatalf("scan failure = %#v, want both source and destination paths", failure)
	}
}

func TestRetryDirectoryRescansConfiguredRoots(t *testing.T) {
	for _, test := range []struct {
		name      string
		method    int
		copyType  taskItemType
		srcPath   string
		dstPath   string
		multiDest bool
		listFails bool
		wantScans int32
		wantFile  bool
	}{
		{name: "recover paired directory", srcPath: "/src/sub/", dstPath: "/dst/sub/", wantScans: 1, wantFile: true},
		{name: "original listing still fails", srcPath: "/src/sub/", dstPath: "/dst/sub/", listFails: true, wantScans: 1},
		{name: "mirror repairs legacy single-sided failure without cleanup", method: 1, srcPath: "/src/sub/", wantScans: 1, wantFile: true},
		{name: "mirror repairs only the recorded destination", method: 1, srcPath: "/src/sub/", dstPath: "/dst/", multiDest: true, wantScans: 1, wantFile: true},
		{name: "same move mode queues verified transfer", method: 2, copyType: taskItemTypeTransfer, srcPath: "/src/sub/", dstPath: "/dst/sub/", wantScans: 1, wantFile: true},
		{name: "different configuration", srcPath: "/previous/sub/", dstPath: "/dst/sub/"},
		{name: "historical copy cannot turn into a move", method: 2, srcPath: "/src/sub/", dstPath: "/dst/sub/"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var sourceLists atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/fs/list" {
					t.Errorf("unexpected request: %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				var req alistListRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !req.Refresh {
					t.Errorf("expected a fresh listing, got %+v, err=%v", req, err)
				}
				switch req.Path {
				case "/src/":
					sourceLists.Add(1)
					if test.listFails {
						_, _ = w.Write([]byte(`{"code":401,"message":"unauthorized","data":null}`))
					} else {
						_, _ = w.Write([]byte(`{"code":200,"data":{"content":[{"name":"new.txt","size":7}],"total":1}}`))
					}
				case "/dst/":
					_, _ = w.Write([]byte(`{"code":200,"data":{"content":[{"name":"old.txt","size":7}],"total":1}}`))
				default:
					t.Errorf("unexpected list path: %s", req.Path)
				}
			}))
			defer server.Close()
			var persisted []map[string]interface{}
			defer stubPersistJobTaskItems(t, &persisted, nil)()
			directoryFailure := map[string]interface{}{
				"srcPath": test.srcPath, "dstPath": test.dstPath, "isPath": taskItemPath.Int(), "type": test.copyType.Int(),
			}
			rows := []map[string]interface{}{directoryFailure}
			if test.wantFile {
				rows = append(rows, directoryFailure, map[string]interface{}{
					"srcPath": "/src/", "dstPath": "/dst/", "fileName": "new.txt", "fileSize": int64(7), "type": test.copyType.Int(),
				})
			}
			deps := *jobDeps
			deps.CountJobTaskItemsByStatuses = func(int64, []int) (int64, error) { return int64(len(rows)), nil }
			deps.ForEachJobTaskItemsByStatuses = func(_ int64, _ []int, _ int, visit func([]map[string]interface{}) error) error {
				return visit(rows)
			}
			defer SetJobDepsForTest(&deps)()
			jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{
				"method": test.method, "srcPath": "/src/", "dstPath": "/dst/", "useCacheS": 1, "useCacheT": 1,
			})
			if test.multiDest {
				jt.Job["dstPath"] = []string{"/other/", "/dst/"}
			}
			jt.RetrySourceTaskID = 11
			jt.RetryStatuses = []taskStatus{taskStatusFailed}
			jt.sync()
			if err := jt.flushPersistBuffer(); err != nil {
				t.Fatal(err)
			}
			if got := sourceLists.Load(); got != test.wantScans {
				t.Fatalf("source listings = %d, want %d", got, test.wantScans)
			}
			if len(jt.fullSyncDeletes) != 0 {
				t.Fatal("directory retry staged mirror cleanup")
			}
			waiting := jt.Waiting.snapshot()
			if test.wantFile {
				if len(waiting) != 1 || waiting[0].FileName != "new.txt" || waiting[0].CopyType != test.copyType || len(persisted) != 0 {
					t.Fatalf("want one copy from the fresh scan, queued=%v persisted=%v", waiting, persisted)
				}
			} else if len(waiting) != 0 || len(persisted) != 1 || persisted[0]["status"] != taskStatusFailed.Int() {
				t.Fatalf("want a failed scan without copies, queued=%v persisted=%v", waiting, persisted)
			}
		})
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
