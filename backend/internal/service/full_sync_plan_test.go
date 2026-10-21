package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"opensync/internal/config"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestFullSyncCreatesMissingDestinationRoot(t *testing.T) {
	var mu sync.Mutex
	created := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/fs/list":
			if body["path"] == "/dst/" {
				mu.Lock()
				exists := created
				mu.Unlock()
				if !exists {
					_, _ = w.Write([]byte(`{"code":500,"message":"failed get dir: object not found","data":null}`))
					return
				}
			}
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"content":[]}}`))
		case "/api/fs/mkdir":
			mu.Lock()
			created = true
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{}}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	d := *jobDeps
	d.ScanListRetryDelay = func(int) time.Duration { return 0 }
	restore := SetJobDepsForTest(&d)
	defer restore()
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()
	jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{
		"method": 1, "srcPath": "/src/", "dstPath": "/dst/",
	})
	jt.sync()
	mu.Lock()
	defer mu.Unlock()
	if !created || jt.isBreak() {
		t.Fatalf("created=%v stopped=%v, want created and running", created, jt.isBreak())
	}
}

func TestFullSyncScanFailureDoesNotMarkTaskStopped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":401,"message":"unauthorized","data":null}`))
	}))
	defer server.Close()
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()
	jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{
		"method": 1, "srcPath": "/src/", "dstPath": "/dst/",
	})
	jt.sync()
	if jt.isBreak() {
		t.Fatal("scan failure marked task as user-stopped")
	}
}

func TestFullSyncPlanMatchesMovedFileByStrongFingerprint(t *testing.T) {
	plan := movedFileFullSyncPlan(FileMetadata{
		Size: 8 << 30,
		MD5:  "0123456789abcdef0123456789abcdef",
	})

	relocations := plan.relocations()
	if len(relocations) != 1 {
		t.Fatalf("relocations len = %d, want 1", len(relocations))
	}
	got := relocations[0]
	if got.source.dir != "/dst/old/archive/" || got.target.dstDir != "/dst/new/archive/" {
		t.Fatalf("relocation = %#v, want destination-side old/archive -> new/archive", got)
	}
	if got.source.name != "movie.mkv" || got.target.name != "movie.mkv" {
		t.Fatalf("relocation names = %q -> %q, want movie.mkv", got.source.name, got.target.name)
	}
}

func TestFullSyncPlanDoesNotInferMoveWithoutMD5(t *testing.T) {
	plan := movedFileFullSyncPlan(FileMetadata{Size: 8 << 30})

	if relocations := plan.relocations(); len(relocations) != 0 {
		t.Fatalf("relocations = %#v, want none when only file size matches", relocations)
	}
}

func TestFullSyncPlanExecutesDestinationMoveBeforeDeletingOldTree(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode %s request: %v", r.URL.Path, err)
		}
		mu.Lock()
		switch r.URL.Path {
		case "/api/fs/list":
			mu.Unlock()
			writeMovedFileListResponse(t, w, body["path"].(string))
			return
		case "/api/fs/mkdir":
			calls = append(calls, "mkdir:"+body["path"].(string))
		case "/api/fs/move":
			names := body["names"].([]interface{})
			calls = append(calls, "move:"+body["src_dir"].(string)+"->"+body["dst_dir"].(string)+names[0].(string))
		case "/api/fs/get":
			calls = append(calls, "get:"+body["path"].(string))
		case "/api/fs/remove":
			names := body["names"].([]interface{})
			calls = append(calls, "remove:"+body["dir"].(string)+names[0].(string))
		default:
			mu.Unlock()
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		mu.Unlock()

		if r.URL.Path == "/api/fs/move" {
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"tasks":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{}}`))
	}))
	defer server.Close()

	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

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
	if err := jt.flushPersistBuffer(); err != nil {
		t.Fatalf("flushPersistBuffer() error: %v", err)
	}
	if !jt.ScanFinish.Load() {
		t.Fatal("sync did not mark scanning complete")
	}

	// Extra deletes are executed synchronously (consecutive failure threshold);
	// the waiting queue should be empty and the remove call should appear in the
	// server calls list.
	if waiting := jt.Waiting.snapshot(); len(waiting) != 0 {
		t.Fatalf("waiting items = %d, want 0 (deletes are synchronous now)", len(waiting))
	}
	wantCalls := []string{
		"mkdir:/dst/new/",
		"mkdir:/dst/new/archive/",
		"move:/dst/old/archive/->/dst/new/archive/movie.mkv",
		"get:/dst/new/archive/movie.mkv",
		"remove:/dst/old",
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != len(wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
	for i := range wantCalls {
		if calls[i] != wantCalls[i] {
			t.Fatalf("calls[%d] = %q, want %q (all calls: %#v)", i, calls[i], wantCalls[i], calls)
		}
	}

	moveRecords := 0
	for _, item := range persisted {
		if item["type"] == taskItemTypeMove.Int() {
			moveRecords++
			if item["srcPath"] != "/dst/old/archive/" || item["dstPath"] != "/dst/new/archive/" {
				t.Fatalf("persisted move = %#v", item)
			}
		}
	}
	if moveRecords != 1 {
		t.Fatalf("move records = %d, want 1 (all records: %#v)", moveRecords, persisted)
	}
}

func TestFullSyncPlanPreservesOldTreeWhenDestinationMoveFails(t *testing.T) {
	oldConfig := config.GetConfig()
	defer config.SetConfigForTest(oldConfig)
	config.SetConfigForTest(&config.Config{Server: config.ServerConfig{
		CopyConcurrency: 1,
		ScanConcurrency: 1,
		MaxRetries:      0,
	}})

	removeCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/fs/mkdir":
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{}}`))
		case "/api/fs/move":
			_, _ = w.Write([]byte(`{"code":500,"message":"move failed","data":null}`))
		case "/api/fs/remove":
			removeCalled = true
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{
		"method":        1,
		"scanIntervalT": 0,
	})
	plan := movedFileFullSyncPlan(FileMetadata{
		Size: 8 << 30,
		MD5:  "0123456789abcdef0123456789abcdef",
	})

	jt.executeFullSyncPlan(plan)
	if err := jt.flushPersistBuffer(); err != nil {
		t.Fatalf("flushPersistBuffer() error: %v", err)
	}
	if removeCalled {
		t.Fatal("old destination tree was deleted after its relocation failed")
	}
	waiting := jt.Waiting.snapshot()
	if len(waiting) != 1 || waiting[0].SrcPath != "/src/new/archive/" || waiting[0].DstPath != "/dst/new/archive/" {
		t.Fatalf("fallback copy queue = %#v, want source new/archive -> destination new/archive", waiting)
	}
	moveFailures := 0
	for _, item := range persisted {
		if item["type"] == taskItemTypeMove.Int() && item["status"] == taskStatusFailed.Int() {
			moveFailures++
		}
	}
	if moveFailures != 1 {
		t.Fatalf("failed move records = %d, want 1 (all records: %#v)", moveFailures, persisted)
	}
}

func writeMovedFileListResponse(t *testing.T, w http.ResponseWriter, dir string) {
	t.Helper()
	responses := map[string]string{
		"/src/":             `{"code":200,"message":"ok","data":{"content":[{"name":"new","is_dir":true}]}}`,
		"/src/new/":         `{"code":200,"message":"ok","data":{"content":[{"name":"archive","is_dir":true}]}}`,
		"/src/new/archive/": `{"code":200,"message":"ok","data":{"content":[{"name":"movie.mkv","size":8589934592,"hash_info":{"md5":"0123456789abcdef0123456789abcdef"}}]}}`,
		"/dst/":             `{"code":200,"message":"ok","data":{"content":[{"name":"old","is_dir":true}]}}`,
		"/dst/old/":         `{"code":200,"message":"ok","data":{"content":[{"name":"archive","is_dir":true}]}}`,
		"/dst/old/archive/": `{"code":200,"message":"ok","data":{"content":[{"name":"movie.mkv","size":8589934592,"hash_info":{"md5":"0123456789abcdef0123456789abcdef"}}]}}`,
	}
	response, ok := responses[dir]
	if !ok {
		t.Fatalf("unexpected list path %q", dir)
	}
	_, _ = w.Write([]byte(response))
}

func movedFileFullSyncPlan(metadata FileMetadata) *fullSyncPlan {
	src := &fullSyncSnapshot{
		root: "/src/",
		dirs: map[string]FileListResult{
			"":             {"new/": {}},
			"new/":         {"archive/": {}},
			"new/archive/": {"movie.mkv": metadata},
		},
	}
	dst := &fullSyncSnapshot{
		root: "/dst/",
		dirs: map[string]FileListResult{
			"":             {"old/": {}},
			"old/":         {"archive/": {}},
			"old/archive/": {"movie.mkv": metadata},
		},
	}
	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{"method": 1})
	return plan
}

// A source file excluded by the size filter must not be deleted from the
// destination: the filter decides what gets copied, never what gets deleted.
func TestFullSyncPlanKeepsSizeFilteredDestinationFile(t *testing.T) {
	small := FileMetadata{Size: 500 * 1024}
	src := &fullSyncSnapshot{
		root: "/src/",
		dirs: map[string]FileListResult{"": {"small.txt": small}},
	}
	dst := &fullSyncSnapshot{
		root: "/dst/",
		dirs: map[string]FileListResult{"": {"small.txt": small}},
	}
	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{
		"method":      1,
		"minFileSize": int64(1024 * 1024),
	})

	if len(plan.extraDeletes) != 0 {
		t.Fatalf("extraDeletes = %#v, want none: the file still exists at the source", plan.extraDeletes)
	}
	if len(plan.extraFiles) != 0 {
		t.Fatalf("extraFiles = %#v, want none", plan.extraFiles)
	}
	if len(plan.newFiles) != 0 || len(plan.changed) != 0 {
		t.Fatalf("newFiles=%#v changed=%#v, want none: the file is filtered out of copying", plan.newFiles, plan.changed)
	}
}

// A destination file with no source counterpart is still deleted when a size
// filter is configured, so the filter fix does not disable mirror deletes.
func TestFullSyncPlanStillDeletesUnmatchedDestinationFileWithSizeFilter(t *testing.T) {
	src := &fullSyncSnapshot{
		root: "/src/",
		dirs: map[string]FileListResult{"": {}},
	}
	dst := &fullSyncSnapshot{
		root: "/dst/",
		dirs: map[string]FileListResult{"": {"orphan.txt": {Size: 500 * 1024}}},
	}
	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{
		"method":      1,
		"minFileSize": int64(1024 * 1024),
	})

	if len(plan.extraDeletes) != 1 {
		t.Fatalf("extraDeletes = %#v, want the orphaned destination file", plan.extraDeletes)
	}
}

// A destination directory whose name collides with a source file that the size
// filter excludes must be kept: the filter only decides what is copied, and the
// type-conflict delete exists solely to make room for the replacement file.
func TestFullSyncPlanKeepsSizeFilteredDestinationDirectoryOfSameName(t *testing.T) {
	small := FileMetadata{Size: 500 * 1024}
	src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{
		"": {"small.bin": small},
	}}
	dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{
		"":           {"small.bin/": {}},
		"small.bin/": {"keep.txt": {Size: 1}},
	}}
	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{
		"method":      1,
		"minFileSize": int64(1024 * 1024),
	})

	if len(plan.extraDeletes) != 0 || len(plan.blockers) != 0 || len(plan.extraFiles) != 0 {
		t.Fatalf("extraDeletes=%#v blockers=%#v extraFiles=%#v, want none: the source entry is filtered out",
			plan.extraDeletes, plan.blockers, plan.extraFiles)
	}
	if len(plan.newFiles) != 0 || len(plan.changed) != 0 {
		t.Fatalf("newFiles=%#v changed=%#v, want none: the file is filtered out of copying", plan.newFiles, plan.changed)
	}
}

// planShape renders a plan as sorted descriptive lines so the concurrent build
// (non-nil branch gate) can be compared with the sequential one.
func planShape(plan *fullSyncPlan) []string {
	shape := make([]string, 0,
		len(plan.newDirs)+len(plan.newFiles)+len(plan.changed)+
			len(plan.extraFiles)+len(plan.extraDeletes)+len(plan.blockers)+len(plan.emptySrcDirs))
	for key, dir := range plan.newDirs {
		shape = append(shape, "dir:"+key+"->"+dir.srcDir+"|"+dir.dstDir)
	}
	for key := range plan.newFiles {
		shape = append(shape, "new:"+key)
	}
	for _, file := range plan.changed {
		shape = append(shape, "changed:"+file.target)
	}
	for key := range plan.extraFiles {
		shape = append(shape, "extra:"+key)
	}
	for key := range plan.extraDeletes {
		shape = append(shape, "delete:"+key)
	}
	for key := range plan.blockers {
		shape = append(shape, "block:"+key)
	}
	for _, dir := range plan.emptySrcDirs {
		shape = append(shape, "emptysrc:"+dir)
	}
	sort.Strings(shape)
	return shape
}

func branchyPlanSnapshots() (*fullSyncSnapshot, *fullSyncSnapshot) {
	changedSrc := FileMetadata{Size: 10, MD5: "0123456789abcdef0123456789abcdef"}
	changedDst := FileMetadata{Size: 11, MD5: "0123456789abcdef0123456789abcdef"}
	src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{
		"":     {"a/": {}, "b/": {}, "c.txt": {Size: 1}},
		"a/":   {"x/": {}, "same.txt": {Size: 1}, "changed.txt": changedSrc, "new-a.bin": {Size: 2}},
		"a/x/": {"deep.txt": {Size: 3}},
		"b/":   {"same.txt": {Size: 4}, "fresh.bin": {Size: 5}},
	}}
	dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{
		"":         {"a/": {}, "b/": {}, "stale-root.txt": {Size: 1}},
		"a/":       {"x/": {}, "same.txt": {Size: 1}, "changed.txt": changedDst, "old-a.bin": {Size: 2}},
		"a/x/":     {"deep.txt": {Size: 3}, "old-deep.bin": {Size: 6}},
		"b/":       {"same.txt": {Size: 4}, "old-b/": {}},
		"b/old-b/": {"stale.bin": {Size: 7}},
	}}
	return src, dst
}

// The concurrent plan walk must produce exactly the sequential plan: it is the
// production path (branchGate is the task's scan semaphore) and was previously
// untested.
func TestFullSyncPlanConcurrentBuildMatchesSequential(t *testing.T) {
	job := map[string]interface{}{"method": 1}
	src, dst := branchyPlanSnapshots()
	sequential := newFullSyncPlan(src, dst, nil)
	sequential.build(job)
	want := planShape(sequential)

	for i := 0; i < 20; i++ {
		gate := make(chan struct{}, 8)
		concurrent := newFullSyncPlan(src, dst, gate)
		concurrent.build(job)
		if err := concurrent.error(); err != nil {
			t.Fatalf("iteration %d: concurrent build failed: %v", i, err)
		}
		if len(gate) != 0 {
			t.Fatalf("iteration %d: %d branch slots leaked", i, len(gate))
		}
		if got := planShape(concurrent); !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d: concurrent plan differs from sequential\n got: %#v\nwant: %#v", i, got, want)
		}
	}
}

// A panic in a concurrent compareDir branch must be recovered and recorded; the
// caller then refuses to execute the partially built plan.
func TestFullSyncPlanBranchPanicIsContainedAndRecorded(t *testing.T) {
	src, dst := branchyPlanSnapshots()
	gate := make(chan struct{}, 8)

	oldHook := fullSyncPlanBranchHook
	fullSyncPlanBranchHook = func() { panic("branch exploded") }
	defer func() { fullSyncPlanBranchHook = oldHook }()

	plan := newFullSyncPlan(src, dst, gate)
	plan.build(map[string]interface{}{"method": 1})

	if err := plan.error(); err == nil {
		t.Fatal("branch panic was not recorded; a partial plan could be executed")
	}
	if len(gate) != 0 {
		t.Fatalf("%d branch slots leaked after panic", len(gate))
	}
}
