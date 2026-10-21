package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFullSyncDeletesConflictingDestinationDirectoryBeforeQueueingFile(t *testing.T) {
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	var removeCalls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/fs/list":
			var req struct {
				Path string `json:"path"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode list request: %v", err)
			}
			switch req.Path {
			case "/src/":
				_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"total":1,"content":[{"name":"foo","is_dir":false,"size":10}]}}`))
			case "/dst/":
				_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"total":1,"content":[{"name":"foo","is_dir":true,"size":0}]}}`))
			case "/dst/foo/":
				// Full sync walks the destination tree, so the conflicting
				// directory is listed too. It is empty.
				_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"total":0,"content":[]}}`))
			default:
				t.Fatalf("unexpected list path %q", req.Path)
			}
		case "/api/fs/remove":
			var req struct {
				Dir   string   `json:"dir"`
				Names []string `json:"names"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode remove request: %v", err)
			}
			if len(req.Names) != 1 {
				t.Fatalf("remove names = %#v, want one name", req.Names)
			}
			removeCalls = append(removeCalls, req.Dir+req.Names[0]+"/")
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	// Mirror (method=1) jobs run through syncFull, so drive the job entry point
	// rather than the incremental scanner: the dir-vs-file conflict is resolved
	// by the full-sync plan's blockers, which run before anything is queued.
	jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{
		"method":        1,
		"srcPath":       "/src/",
		"dstPath":       "/dst/",
		"useCacheS":     0,
		"useCacheT":     0,
		"scanIntervalS": 0,
		"scanIntervalT": 0,
	})

	jt.sync()

	if len(removeCalls) != 1 || removeCalls[0] != "/dst/foo/" {
		t.Fatalf("removeCalls = %#v, want /dst/foo/", removeCalls)
	}
	if err := jt.flushPersistBuffer(); err != nil {
		t.Fatalf("flushPersistBuffer() error: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("persisted len = %d, want one delete record", len(persisted))
	}
	if persisted[0]["type"] != taskItemTypeDelete.Int() || persisted[0]["isPath"] != taskItemPath.Int() {
		t.Fatalf("persisted delete item = %#v, want directory delete", persisted[0])
	}
	waiting := jt.Waiting.snapshot()
	if len(waiting) != 1 {
		t.Fatalf("waiting len = %d, want one queued copy", len(waiting))
	}
	if waiting[0].FileName != "foo" {
		t.Fatalf("queued file = %q, want foo", waiting[0].FileName)
	}
}

func TestRetryDeleteRecordsRefusalWithoutQueuing(t *testing.T) {
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	jt := &JobTask{
		TaskID: 7,
		Job: map[string]interface{}{
			"scanIntervalT": 0,
		},
	}
	jt.initRuntime()

	// A well-formed single-entry delete record: it used to be replayed, which
	// deletes a destination entry the source may have regained since. The retry
	// refuses it and records why; the next mirror run re-decides deletions from
	// a fresh source snapshot.
	jt.retryTaskItem(map[string]interface{}{
		"type":     taskItemTypeDelete.Int(),
		"isPath":   taskItemPath.Int(),
		"dstPath":  "/dst/",
		"fileName": "old-dir",
	})

	if waiting := jt.Waiting.snapshot(); len(waiting) != 0 {
		t.Fatalf("waiting len = %d, want no queued delete", len(waiting))
	}
	if err := jt.persistRemainingTaskItems(); err != nil {
		t.Fatalf("persistRemainingTaskItems() error: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("persisted len = %d, want one refused-delete record", len(persisted))
	}
	record := persisted[0]
	if record["type"] != taskItemTypeDelete.Int() || record["status"] != taskStatusFailed.Int() {
		t.Fatalf("persisted = %#v, want a failed delete record", record)
	}
	if record["isPath"] != taskItemPath.Int() {
		t.Fatalf("persisted isPath = %#v, want path", record["isPath"])
	}
	errMsg, _ := record["errMsg"].(*string)
	if errMsg == nil || !strings.Contains(*errMsg, "not replayed") {
		t.Fatalf("persisted errMsg = %v, want an explicit refusal", record["errMsg"])
	}
}

func TestFullSyncSkipsEquivalentEscapedDestinationFileNameWithSameSize(t *testing.T) {
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	var removeCalls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/fs/list":
			var req struct {
				Path string `json:"path"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode list request: %v", err)
			}
			switch req.Path {
			case "/src/":
				_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"total":1,"content":[{"name":"Q&A.mp4","is_dir":false,"size":10}]}}`))
			case "/dst/":
				_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"total":1,"content":[{"name":"Q&amp;A.mp4","is_dir":false,"size":10}]}}`))
			default:
				t.Fatalf("unexpected list path %q", req.Path)
			}
		case "/api/fs/remove":
			var req struct {
				Dir   string   `json:"dir"`
				Names []string `json:"names"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode remove request: %v", err)
			}
			removeCalls = append(removeCalls, req.Dir+req.Names[0])
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{"method": 1})
	jt.syncWithHave(scanWork{
		SrcPath:     "/src/",
		DstPath:     "/dst/",
		SrcRootPath: "/src/",
		DstRootPath: "/dst/",
		FirstDst:    true,
		Mode:        scanWorkCompare,
	}, nil)

	if len(removeCalls) != 0 {
		t.Fatalf("removeCalls = %#v, want none", removeCalls)
	}
	if len(persisted) != 0 {
		t.Fatalf("persisted len = %d, want no delete records", len(persisted))
	}
	if waiting := jt.Waiting.snapshot(); len(waiting) != 0 {
		t.Fatalf("waiting len = %d, want no queued copy", len(waiting))
	}
}

func TestFullSyncRecopiesEquivalentEscapedDestinationFileNameWhenSizeDiffers(t *testing.T) {
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	var removeCalls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/fs/list":
			var req struct {
				Path string `json:"path"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode list request: %v", err)
			}
			switch req.Path {
			case "/src/":
				_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"total":1,"content":[{"name":"Q&A.mp4","is_dir":false,"size":11}]}}`))
			case "/dst/":
				_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"total":1,"content":[{"name":"Q&amp;A.mp4","is_dir":false,"size":10}]}}`))
			default:
				t.Fatalf("unexpected list path %q", req.Path)
			}
		case "/api/fs/remove":
			var req struct {
				Dir   string   `json:"dir"`
				Names []string `json:"names"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode remove request: %v", err)
			}
			removeCalls = append(removeCalls, req.Dir+req.Names[0])
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{"method": 1})
	jt.syncWithHave(scanWork{
		SrcPath:     "/src/",
		DstPath:     "/dst/",
		SrcRootPath: "/src/",
		DstRootPath: "/dst/",
		FirstDst:    true,
		Mode:        scanWorkCompare,
	}, nil)

	if len(removeCalls) != 0 {
		t.Fatalf("removeCalls = %#v, want none", removeCalls)
	}
	if len(persisted) != 0 {
		t.Fatalf("persisted len = %d, want no delete records", len(persisted))
	}
	waiting := jt.Waiting.snapshot()
	if len(waiting) != 1 {
		t.Fatalf("waiting len = %d, want one queued copy", len(waiting))
	}
	if waiting[0].FileName != "Q&A.mp4" {
		t.Fatalf("queued file = %q, want Q&A.mp4", waiting[0].FileName)
	}
}

func TestFullSyncDoesNotFuzzyMatchWhenSourceHasCanonicalNameCollision(t *testing.T) {
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/fs/list":
			var req struct {
				Path string `json:"path"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode list request: %v", err)
			}
			switch req.Path {
			case "/src/":
				_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"total":2,"content":[{"name":"Q&A.mp4","is_dir":false,"size":10},{"name":"Q&amp;A.mp4","is_dir":false,"size":10}]}}`))
			case "/dst/":
				_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"total":1,"content":[{"name":"Q&amp;A.mp4","is_dir":false,"size":10}]}}`))
			default:
				t.Fatalf("unexpected list path %q", req.Path)
			}
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{"method": 1})
	jt.syncWithHave(scanWork{
		SrcPath:     "/src/",
		DstPath:     "/dst/",
		SrcRootPath: "/src/",
		DstRootPath: "/dst/",
		FirstDst:    true,
		Mode:        scanWorkCompare,
	}, nil)

	if len(persisted) != 0 {
		t.Fatalf("persisted len = %d, want no delete records", len(persisted))
	}
	waiting := jt.Waiting.snapshot()
	if len(waiting) != 1 {
		t.Fatalf("waiting len = %d, want one queued copy", len(waiting))
	}
	if waiting[0].FileName != "Q&A.mp4" {
		t.Fatalf("queued file = %q, want Q&A.mp4", waiting[0].FileName)
	}
}

func scanTestTask(t *testing.T, serverURL string, client *http.Client, job map[string]interface{}) *JobTask {
	t.Helper()
	jt := &JobTask{
		TaskID:  42,
		Job:     job,
		Waiting: newCopyQueue(),
		AlistClient: &AlistClient{
			URL:    serverURL,
			client: client,
		},
	}
	jt.initRuntime()
	// These tests drive jt.sync() directly, so taskSubmit — which cancels the
	// debounced flush in production — never runs. Cancel it here or the pending
	// timer fires after the test restored persistJobTaskItems.
	t.Cleanup(jt.cancelPersistFlush)
	return jt
}

// A destination root that does not exist yet must be created rather than
// failing the whole job with "destination scan failed". The repair must leave no
// failed item behind and must not pay the retry backoff: a first mirror run used
// to report partial failure with a misleading scan error for this normal case.
func TestSyncCreatesMissingDestinationRoot(t *testing.T) {
	d := *jobDeps
	d.ScanListRetryDelay = func(int) time.Duration {
		t.Error("scan retry backoff used for a missing destination root")
		return 0
	}
	restore := SetJobDepsForTest(&d)
	defer restore()

	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	var mkdirCalls []string
	var dstListCalls atomic.Int32
	dstCreated := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		switch r.URL.Path {
		case "/api/fs/list":
			switch req.Path {
			case "/src/":
				_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"total":1,"content":[{"name":"a.txt","is_dir":false,"size":10}]}}`))
			case "/dst/":
				dstListCalls.Add(1)
				if !dstCreated {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"code":500,"message":"failed get objs: failed get dir: object not found","data":null}`))
					return
				}
				_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"total":0,"content":[]}}`))
			default:
				t.Errorf("unexpected list path %q", req.Path)
			}
		case "/api/fs/mkdir":
			mkdirCalls = append(mkdirCalls, req.Path)
			dstCreated = true
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{}}`))
		case "/api/fs/copy":
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"tasks":[]}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	for _, method := range []int{0, 1} {
		mkdirCalls = nil
		dstCreated = false
		dstListCalls.Store(0)
		persisted = nil

		jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{
			"method":        method,
			"srcPath":       "/src/",
			"dstPath":       "/dst/",
			"scanIntervalS": 0,
			"scanIntervalT": 0,
		})

		jt.sync()

		if len(mkdirCalls) != 1 || mkdirCalls[0] != "/dst/" {
			t.Fatalf("method %d: mkdirCalls = %#v, want one /dst/", method, mkdirCalls)
		}
		if got := dstListCalls.Load(); got != 2 {
			t.Fatalf("method %d: destination list attempts = %d, want 2 (initial + retry after mkdir)", method, got)
		}
		// The full-sync plan executes its own queued copies, so only the
		// incremental path leaves the item in the waiting queue.
		if method == 0 {
			waiting := jt.Waiting.snapshot()
			if len(waiting) != 1 || waiting[0].FileName != "a.txt" {
				t.Fatalf("waiting = %#v, want the source file queued after the root was created", waiting)
			}
		}
		if err := jt.persistRemainingTaskItems(); err != nil {
			t.Fatalf("persistRemainingTaskItems() error: %v", err)
		}
		if len(persisted) != 0 {
			t.Fatalf("method %d: persisted = %#v, want no failure item for a repaired destination root", method, persisted)
		}
	}
}

// When the destination root cannot be created either, the missing-root failure
// must still be recorded (the list error itself is suppressed for the repair).
func TestSyncRecordsFailureWhenDestinationRootCannotBeCreated(t *testing.T) {
	d := *jobDeps
	d.ScanListRetryDelay = func(int) time.Duration { return 0 }
	restore := SetJobDepsForTest(&d)
	defer restore()

	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/fs/list":
			var req struct {
				Path string `json:"path"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Path == "/src/" {
				_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"total":1,"content":[{"name":"a.txt","is_dir":false,"size":10}]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":500,"message":"failed get objs: failed get dir: object not found","data":null}`))
		case "/api/fs/mkdir":
			_, _ = w.Write([]byte(`{"code":500,"message":"permission denied","data":null}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{
		"method":        0,
		"srcPath":       "/src/",
		"dstPath":       "/dst/",
		"scanIntervalS": 0,
		"scanIntervalT": 0,
	})

	jt.sync()

	if err := jt.persistRemainingTaskItems(); err != nil {
		t.Fatalf("persistRemainingTaskItems() error: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("persisted = %#v, want one destination failure item", persisted)
	}
	if persisted[0]["status"] != taskStatusFailed.Int() {
		t.Fatalf("persisted = %#v, want a failed item", persisted[0])
	}
}

// Incremental scanning has no delete authority, so a source directory whose
// destination path is occupied by a file cannot be repaired inline. It must be
// reported explicitly: the old path asked AList to mkdir over the file, failed,
// and then skipped the whole subtree with a single misleading mkdir error.
func TestIncrementalScanReportsDirectoryFileConflict(t *testing.T) {
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	var mu sync.Mutex
	var listed []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/fs/list":
			var req struct {
				Path string `json:"path"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode list request: %v", err)
				return
			}
			mu.Lock()
			listed = append(listed, req.Path)
			mu.Unlock()
			switch req.Path {
			case "/src/":
				_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"total":1,"content":[{"name":"foo","is_dir":true,"size":0}]}}`))
			case "/dst/":
				_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"total":1,"content":[{"name":"foo","is_dir":false,"size":10}]}}`))
			default:
				t.Errorf("unexpected list path %q", req.Path)
			}
		case "/api/fs/mkdir":
			t.Errorf("mkdir must not be attempted over a destination file")
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{
		"method":        0,
		"srcPath":       "/src/",
		"dstPath":       "/dst/",
		"scanIntervalS": 0,
		"scanIntervalT": 0,
	})
	jt.sync()

	if waiting := jt.Waiting.snapshot(); len(waiting) != 0 {
		t.Fatalf("waiting = %#v, want no copy queued onto a file", waiting)
	}
	if err := jt.persistRemainingTaskItems(); err != nil {
		t.Fatalf("persistRemainingTaskItems() error: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("persisted = %#v, want exactly one conflict report", persisted)
	}
	record := persisted[0]
	if record["status"] != taskStatusFailed.Int() || record["isPath"] != taskItemPath.Int() {
		t.Fatalf("persisted = %#v, want a failed path item", record)
	}
	errMsg, _ := record["errMsg"].(*string)
	if errMsg == nil || !strings.Contains(*errMsg, "scan conflict") || !strings.Contains(*errMsg, "is a file") {
		t.Fatalf("persisted errMsg = %v, want an explicit type-conflict report", record["errMsg"])
	}
	mu.Lock()
	defer mu.Unlock()
	for _, p := range listed {
		if p == "/src/foo/" {
			t.Fatalf("subtree under the conflicting directory was still scanned")
		}
	}
}

// The opposite conflict: a source file whose destination path is a directory.
// Queueing the copy would fail on the AList side; report the mismatch instead.
func TestIncrementalScanReportsFileDirectoryConflict(t *testing.T) {
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/fs/list":
			var req struct {
				Path string `json:"path"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			switch req.Path {
			case "/src/":
				_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"total":1,"content":[{"name":"a.txt","is_dir":false,"size":10}]}}`))
			case "/dst/":
				_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"total":1,"content":[{"name":"a.txt","is_dir":true,"size":0}]}}`))
			default:
				t.Errorf("unexpected list path %q", req.Path)
			}
		default:
			t.Errorf("unexpected path %s (a copy must not be submitted)", r.URL.Path)
		}
	}))
	defer server.Close()

	jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{
		"method":        0,
		"srcPath":       "/src/",
		"dstPath":       "/dst/",
		"scanIntervalS": 0,
		"scanIntervalT": 0,
	})
	jt.sync()

	if waiting := jt.Waiting.snapshot(); len(waiting) != 0 {
		t.Fatalf("waiting = %#v, want no copy queued onto a directory", waiting)
	}
	if err := jt.persistRemainingTaskItems(); err != nil {
		t.Fatalf("persistRemainingTaskItems() error: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("persisted = %#v, want exactly one conflict report", persisted)
	}
	record := persisted[0]
	if record["status"] != taskStatusFailed.Int() || record["isPath"] != taskItemFile.Int() {
		t.Fatalf("persisted = %#v, want a failed file item", record)
	}
	errMsg, _ := record["errMsg"].(*string)
	if errMsg == nil || !strings.Contains(*errMsg, "is a directory") {
		t.Fatalf("persisted errMsg = %v, want an explicit type-conflict report", record["errMsg"])
	}
}

// A mirror run deletes destination entries based on the source listing, so that
// listing must never come from AList's server-side cache: a stale one deletes
// newly added files or recreates removed ones.
func TestFullSyncSourceListingForcesRefresh(t *testing.T) {
	listRefresh := func(t *testing.T, job map[string]interface{}, firstDst bool) map[string]bool {
		t.Helper()
		var mu sync.Mutex
		refreshes := map[string]bool{}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path != "/api/fs/list" {
				t.Errorf("unexpected path %s", r.URL.Path)
				return
			}
			var req struct {
				Path    string `json:"path"`
				Refresh bool   `json:"refresh"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode list request: %v", err)
				return
			}
			mu.Lock()
			refreshes[req.Path] = req.Refresh
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"total":0,"content":[]}}`))
		}))
		defer server.Close()

		jt := scanTestTask(t, server.URL, server.Client(), job)
		if _, err := jt.listDir("/src/", firstDst, nil, "/src/", true); err != nil {
			t.Fatalf("listDir() error: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string]bool, len(refreshes))
		for k, v := range refreshes {
			out[k] = v
		}
		return out
	}

	t.Run("mirror ignores useCacheS", func(t *testing.T) {
		got := listRefresh(t, map[string]interface{}{
			"method": 1, "srcPath": "/src/", "dstPath": "/dst/", "useCacheS": 1,
		}, true)
		if !got["/src/"] {
			t.Fatal("mirror source listing was read from the AList cache")
		}
	})

	t.Run("incremental honours useCacheS", func(t *testing.T) {
		got := listRefresh(t, map[string]interface{}{
			"method": 0, "srcPath": "/src/", "dstPath": "/dst/", "useCacheS": 1,
		}, true)
		if got["/src/"] {
			t.Fatal("incremental sync with useCacheS=1 unexpectedly forced a refresh")
		}
	})
}

// Works for later destinations reuse the source listing the first destination
// refreshed, so they must not start before that refresh completed. Starting all
// (src,dst) pairs at once made the cache reuse a race that could delete live
// destination files in mirror mode.
func TestSyncScansFirstDestinationBeforeLaterDestinations(t *testing.T) {
	var mu sync.Mutex
	type listCall struct {
		path    string
		refresh bool
	}
	var calls []listCall
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/fs/list" {
			t.Errorf("unexpected path %s", r.URL.Path)
			return
		}
		var req struct {
			Path    string `json:"path"`
			Refresh bool   `json:"refresh"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode list request: %v", err)
			return
		}
		mu.Lock()
		calls = append(calls, listCall{path: req.Path, refresh: req.Refresh})
		mu.Unlock()
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"total":0,"content":[]}}`))
	}))
	defer server.Close()

	jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{
		"method":        0,
		"srcPath":       []string{"/src/"},
		"dstPath":       []string{"/dst1/", "/dst2/"},
		"scanIntervalS": 0,
		"scanIntervalT": 0,
	})
	jt.sync()

	mu.Lock()
	defer mu.Unlock()
	idx := map[string]int{}
	refresh := map[string]bool{}
	for i, call := range calls {
		if _, seen := idx[call.path]; !seen {
			idx[call.path] = i
		}
		// Keep the last refresh flag: the later destination's pass is the one
		// that must reuse the listing.
		refresh[call.path] = call.refresh
	}
	first, okFirst := idx["/dst1/"]
	later, okLater := idx["/dst2/"]
	if !okFirst || !okLater {
		t.Fatalf("list calls = %#v, want both destinations scanned", calls)
	}
	if first > later {
		t.Fatalf("list order = %#v: a later destination started before the first one finished refreshing the source", calls)
	}
	if refresh["/src/"] {
		t.Fatalf("source listing was refreshed again for the later destination; the first pass should be reused")
	}
}

// Removing the history of the task a "retry failed items" run is replaying used
// to make the replay stop silently: the replayed prefix looked exactly like the
// whole set. The loss must be reported.
func TestRetryReportsSourceTaskItemsLostWhileReplaying(t *testing.T) {
	d := *jobDeps
	d.CountJobTaskItemsByStatuses = func(int64, []int) (int64, error) { return 5, nil }
	d.ForEachJobTaskItemsByStatuses = func(_ int64, _ []int, _ int, fn func([]map[string]interface{}) error) error {
		return fn([]map[string]interface{}{
			{"type": taskItemTypeCopy.Int(), "srcPath": "/src/a.txt", "dstPath": "/dst/", "fileName": "a.txt", "fileSize": int64(1), "isPath": taskItemFile.Int()},
			{"type": taskItemTypeCopy.Int(), "srcPath": "/src/b.txt", "dstPath": "/dst/", "fileName": "b.txt", "fileSize": int64(2), "isPath": taskItemFile.Int()},
		})
	}
	restore := SetJobDepsForTest(&d)
	defer restore()

	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	jt := &JobTask{
		TaskID:            5,
		Job:               map[string]interface{}{},
		RetrySourceTaskID: 99,
		RetryStatuses:     []taskStatus{taskStatusFailed},
	}
	jt.initRuntime()
	jt.syncRetryItems()

	if waiting := jt.Waiting.snapshot(); len(waiting) != 2 {
		t.Fatalf("waiting len = %d, want the two replayed items queued", len(waiting))
	}
	if err := jt.persistRemainingTaskItems(); err != nil {
		t.Fatalf("persistRemainingTaskItems() error: %v", err)
	}
	if len(persisted) != 1 {
		t.Fatalf("persisted = %#v, want one lost-items report", persisted)
	}
	errMsg, _ := persisted[0]["errMsg"].(*string)
	if errMsg == nil || !strings.Contains(*errMsg, "lost items while this retry was running") {
		t.Fatalf("persisted errMsg = %v, want the lost-items report", persisted[0]["errMsg"])
	}
}
