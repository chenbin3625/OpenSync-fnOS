package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// removeRecorder is an AList stub that answers mkdir/remove and records every
// remove request as dir+name. failRemove makes every remove fail.
type removeRecorder struct {
	mu         sync.Mutex
	removes    []string
	failRemove bool
	listings   map[string]string
	copySize   int64
	failCopy   bool
}

func (rec *removeRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/me":
			_, _ = w.Write([]byte(`{"code":200,"data":{"username":"admin"}}`))
		case "/api/fs/list":
			var req alistListRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode list request: %v", err)
				return
			}
			if data, ok := rec.listings[req.Path]; ok {
				_, _ = fmt.Fprintf(w, `{"code":200,"data":%s}`, data)
				return
			}
			t.Errorf("unexpected list path %q", req.Path)
		case "/api/fs/copy":
			if rec.copySize == 0 {
				t.Error("unexpected copy request")
			} else if rec.failCopy {
				_, _ = w.Write([]byte(`{"code":401,"message":"unauthorized","data":null}`))
			} else {
				_, _ = w.Write([]byte(`{"code":200,"data":{"tasks":[]}}`))
			}
		case "/api/fs/get":
			if rec.copySize == 0 {
				t.Error("unexpected get request")
			} else {
				_, _ = fmt.Fprintf(w, `{"code":200,"data":{"size":%d}}`, rec.copySize)
			}
		case "/api/fs/mkdir":
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{}}`))
		case "/api/fs/remove":
			var req alistRemoveRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode remove request: %v", err)
				return
			}
			rec.mu.Lock()
			for _, name := range req.Names {
				rec.removes = append(rec.removes, req.Dir+name)
			}
			fail := rec.failRemove
			rec.mu.Unlock()
			if fail {
				_, _ = w.Write([]byte(`{"code":500,"message":"remove failed","data":null}`))
				return
			}
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{}}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
}

func (rec *removeRecorder) calls() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]string(nil), rec.removes...)
}

func deleteSafetyTask(t *testing.T, serverURL string, client *http.Client) *JobTask {
	t.Helper()
	return scanTestTask(t, serverURL, client, map[string]interface{}{
		"method":        1,
		"scanIntervalT": 0,
	})
}

// filesUnder returns a snapshot directory listing of n plain files.
func filesUnder(prefix string, n int) FileListResult {
	items := make(FileListResult, n)
	for i := 0; i < n; i++ {
		items[fmt.Sprintf("%s%d.txt", prefix, i)] = FileMetadata{Size: 1}
	}
	return items
}

// The guard record must never be replayable as a delete: retrying it would
// send /api/fs/remove {dir: root, names: [""]}, which some servers resolve to
// the destination root itself.
func TestRetryOfMirrorDeleteGuardRecordNeverDeletes(t *testing.T) {
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	rec := &removeRecorder{}
	server := rec.server(t)
	defer server.Close()

	// Produce the guard record exactly as the executor does: a whole missing
	// source tree against a large destination.
	src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{"": {}}}
	dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{"": filesUnder("f", 200)}}
	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{"method": 1})

	jt := deleteSafetyTask(t, server.URL, server.Client())
	jt.executeFullSyncPlan(plan)
	if err := jt.flushPersistBuffer(); err != nil {
		t.Fatalf("flushPersistBuffer() error: %v", err)
	}
	if calls := rec.calls(); len(calls) != 0 {
		t.Fatalf("remove calls = %#v, want none while the valve is tripped", calls)
	}
	if len(persisted) != 1 {
		t.Fatalf("persisted = %#v, want exactly the guard record", persisted)
	}
	guard := persisted[0]
	if guard["type"] != taskItemTypeDelete.Int() || guard["status"] != taskStatusFailed.Int() || guard["fileName"] != "" {
		t.Fatalf("guard record = %#v, want failed delete with empty name", guard)
	}
	guardMsg := ""
	if p, ok := guard["errMsg"].(*string); ok && p != nil {
		guardMsg = *p
	}
	if guardMsg == "" {
		t.Fatal("guard record has no explanation")
	}

	// Retry the record as "retry failed items" would. Nothing may reach the
	// server and nothing may be queued for the executor to delete later.
	var retried []map[string]interface{}
	restoreRetryPersist := stubPersistJobTaskItems(t, &retried, nil)
	defer restoreRetryPersist()
	retry := deleteSafetyTask(t, server.URL, server.Client())
	retry.retryTaskItem(guard)
	if waiting := retry.Waiting.snapshot(); len(waiting) != 0 {
		t.Fatalf("retry queued %#v, want nothing", waiting)
	}
	if calls := rec.calls(); len(calls) != 0 {
		t.Fatalf("remove calls after retry = %#v, want none", calls)
	}
	if err := retry.flushPersistBuffer(); err != nil {
		t.Fatalf("flushPersistBuffer() error: %v", err)
	}
	if len(retried) != 1 || retried[0]["status"] != taskStatusFailed.Int() {
		t.Fatalf("retry records = %#v, want one failed record", retried)
	}
	if p, ok := retried[0]["errMsg"].(*string); !ok || p == nil || *p != guardMsg {
		t.Fatalf("retry errMsg = %#v, want the original guard explanation %q", retried[0]["errMsg"], guardMsg)
	}
}

// Database rows carry errMsg as a plain string, and any delete whose name does
// not address exactly one entry is refused at every delete entry point.
func TestUnsafeDeleteNamesAreRefusedEverywhere(t *testing.T) {
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	rec := &removeRecorder{}
	server := rec.server(t)
	defer server.Close()
	jt := deleteSafetyTask(t, server.URL, server.Client())

	for _, name := range []string{"", "/", ".", "..", "../", "a/b", "a/b/"} {
		jt.retryTaskItem(map[string]interface{}{
			"type":     taskItemTypeDelete.Int(),
			"isPath":   taskItemPath.Int(),
			"dstPath":  "/dst/",
			"fileName": name,
			"errMsg":   "stored reason",
		})
		jt.queueDelFile("/dst/", name, nil, true)
		if status := jt.delFile("/dst/", name, nil); status != taskStatusFailed {
			t.Fatalf("delFile(%q) = %d, want failed", name, status)
		}
	}
	if waiting := jt.Waiting.snapshot(); len(waiting) != 0 {
		t.Fatalf("queued deletes = %d, want none", len(waiting))
	}
	if calls := rec.calls(); len(calls) != 0 {
		t.Fatalf("remove calls = %#v, want none", calls)
	}
	if err := jt.flushPersistBuffer(); err != nil {
		t.Fatalf("flushPersistBuffer() error: %v", err)
	}
	if p, ok := persisted[0]["errMsg"].(*string); !ok || p == nil || *p != "stored reason" {
		t.Fatalf("retry errMsg = %#v, want the stored reason", persisted[0]["errMsg"])
	}

	// A normal name still deletes.
	if status := jt.delFile("/dst/", "keep-working.txt", int64(1)); status != taskStatusSuccess {
		t.Fatalf("delFile(normal) = %d, want success", status)
	}
	if calls := rec.calls(); len(calls) != 1 || calls[0] != "/dst/keep-working.txt" {
		t.Fatalf("remove calls = %#v, want the normal delete", calls)
	}
}

// A missing source subtree is one delete root but removes every entry below
// it. Counting it as one would let any size of tree past the 100-entry floor.
func TestSafetyValveWeighsDirectoryDeletesBySubtree(t *testing.T) {
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	rec := &removeRecorder{}
	server := rec.server(t)
	defer server.Close()

	src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{
		"": {"keep.txt": {Size: 1}},
	}}
	dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{
		"":            {"keep.txt": {Size: 1}, "mount/": {}},
		"mount/":      {"deep/": {}},
		"mount/deep/": filesUnder("m", 150),
	}}
	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{"method": 1})
	if len(plan.extraDeletes) != 1 {
		t.Fatalf("extraDeletes = %#v, want the single mount/ root", plan.extraDeletes)
	}

	jt := deleteSafetyTask(t, server.URL, server.Client())
	jt.executeFullSyncPlan(plan)
	if calls := rec.calls(); len(calls) != 0 {
		t.Fatalf("remove calls = %#v, want none: the subtree is most of the destination", calls)
	}
	if err := jt.flushPersistBuffer(); err != nil {
		t.Fatalf("flushPersistBuffer() error: %v", err)
	}
	if len(persisted) != 1 || persisted[0]["fileName"] != "" {
		t.Fatalf("persisted = %#v, want only the guard record", persisted)
	}
}

func TestSafetyValveAllowsSmallDirectoryDelete(t *testing.T) {
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	rec := &removeRecorder{}
	server := rec.server(t)
	defer server.Close()

	src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{
		"":      {"keep/": {}},
		"keep/": filesUnder("k", 300),
	}}
	dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{
		"":      {"keep/": {}, "old/": {}},
		"keep/": filesUnder("k", 300),
		"old/":  filesUnder("o", 150),
	}}
	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{"method": 1})

	jt := deleteSafetyTask(t, server.URL, server.Client())
	jt.executeFullSyncPlan(plan)
	jt.runFullSyncDeletes()
	if calls := rec.calls(); len(calls) != 1 || calls[0] != "/dst/old" {
		t.Fatalf("remove calls = %#v, want /dst/old (151 of 452 entries is under the ratio)", calls)
	}
}

func TestSubtreeEntryCountIncludesNestedDirectories(t *testing.T) {
	snapshot := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{
		"":     {"a/": {}, "ab/": {}},
		"a/":   {"b/": {}, "x": {}},
		"a/b/": {"y": {}, "z": {}},
		"ab/":  {"w": {}},
	}}
	cases := map[string]int{"a/": 4, "a/b/": 2, "ab/": 1, "missing/": 0}
	for dir, want := range cases {
		if got := snapshot.subtreeEntryCount(dir); got != want {
			t.Errorf("subtreeEntryCount(%q) = %d, want %d", dir, got, want)
		}
	}
	if got := snapshot.deleteWeight(fullSyncDelete{name: "a/", subtree: "a/"}); got != 5 {
		t.Errorf("deleteWeight(a/) = %d, want 5 (the directory plus 4 entries)", got)
	}
	if got := snapshot.deleteWeight(fullSyncDelete{name: "x"}); got != 1 {
		t.Errorf("deleteWeight(file) = %d, want 1", got)
	}
}

// Type-conflict removals are recursive deletes too. When the valve trips they
// must be skipped, and the blocked source item must not be written over them.
func TestSafetyValveCoversTypeConflictDeletes(t *testing.T) {
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	rec := &removeRecorder{}
	server := rec.server(t)
	defer server.Close()

	// Source has a file "media" where the destination has a large directory.
	src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{
		"": {"media": {Size: 5}},
	}}
	dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{
		"":       {"media/": {}},
		"media/": filesUnder("p", 200),
	}}
	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{"method": 1})
	if len(plan.blockers) != 1 || len(plan.extraDeletes) != 0 {
		t.Fatalf("blockers=%#v extraDeletes=%#v, want one blocker only", plan.blockers, plan.extraDeletes)
	}

	jt := deleteSafetyTask(t, server.URL, server.Client())
	jt.executeFullSyncPlan(plan)
	if calls := rec.calls(); len(calls) != 0 {
		t.Fatalf("remove calls = %#v, want none: the conflict delete is most of the destination", calls)
	}
	if waiting := jt.Waiting.snapshot(); len(waiting) != 0 {
		t.Fatalf("queued = %#v, want no copy over the kept directory", waiting)
	}
	if err := jt.flushPersistBuffer(); err != nil {
		t.Fatalf("flushPersistBuffer() error: %v", err)
	}
	if len(persisted) != 1 || persisted[0]["fileName"] != "" {
		t.Fatalf("persisted = %#v, want only the guard record", persisted)
	}
}

// Type-conflict removals share the consecutive-failure cap with the delete
// phase, so a destination rejecting deletes is not hit once per conflict.
func TestTypeConflictDeletesHonorConsecutiveFailureCap(t *testing.T) {
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	rec := &removeRecorder{failRemove: true}
	server := rec.server(t)
	defer server.Close()

	srcRoot := FileListResult{}
	dstRoot := FileListResult{}
	for i := 0; i < 30; i++ {
		name := fmt.Sprintf("c%02d", i)
		srcRoot[name] = FileMetadata{Size: 1}
		dstRoot[name+"/"] = FileMetadata{}
	}
	// Plenty of untouched entries keep the plan well under the valve.
	for name, meta := range filesUnder("steady", 500) {
		srcRoot[name] = meta
		dstRoot[name] = meta
	}
	src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{"": srcRoot}}
	dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{"": dstRoot}}
	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{"method": 1})
	if len(plan.blockers) != 30 {
		t.Fatalf("blockers = %d, want 30", len(plan.blockers))
	}

	jt := deleteSafetyTask(t, server.URL, server.Client())
	jt.executeFullSyncPlan(plan)
	if calls := rec.calls(); len(calls) != maxConsecutiveDeleteErrors {
		t.Fatalf("remove calls = %d, want %d before the cap stops them", len(calls), maxConsecutiveDeleteErrors)
	}
	if waiting := jt.Waiting.snapshot(); len(waiting) != 0 {
		t.Fatalf("queued = %d, want no copies over conflicts that were not removed", len(waiting))
	}
}

// On case-insensitive or Unicode-normalizing destinations the entry being
// deleted can be the same object the plan is writing. Deleting it would race
// the queued copy, so it is kept.
func TestFullSyncKeepsDeleteThatCollidesWithWrittenName(t *testing.T) {
	nfc := "caf\u00e9.jpg"  // precomposed e-acute (NFC)
	nfd := "cafe\u0301.jpg" // e + combining acute accent (NFD)
	cases := []struct {
		name    string
		srcName string
		dstName string
	}{
		{"case fold", "Photo.jpg", "photo.jpg"},
		{"NFC source over NFD destination", nfc, nfd},
		{"NFD source over NFC destination", nfd, nfc},
		{"case fold and normalization together", "CAF\u00c9.jpg", nfd},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var persisted []map[string]interface{}
			restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
			defer restorePersist()

			rec := &removeRecorder{copySize: 10}
			server := rec.server(t)
			defer server.Close()

			src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{
				"":     {tc.srcName: {Size: 10}, "sub/": {}},
				"sub/": {tc.srcName: {Size: 10}},
			}}
			dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{
				"":     {tc.dstName: {Size: 3}, "sub/": {}, "stale.txt": {Size: 1}},
				"sub/": {tc.dstName: {Size: 3}},
			}}
			plan := newFullSyncPlan(src, dst, nil)
			plan.build(map[string]interface{}{"method": 1})

			jt := deleteSafetyTask(t, server.URL, server.Client())
			jt.executeFullSyncPlan(plan)

			waiting := jt.Waiting.snapshot()
			if len(waiting) != 2 {
				t.Fatalf("queued = %d, want both copies of %q", len(waiting), tc.srcName)
			}
			jt.markScanFinished()
			jt.runCopyExecutor()
			jt.drainCopyExecutor()
			jt.runFullSyncDeletes()
			if calls := rec.calls(); len(calls) != 1 || calls[0] != "/dst/stale.txt" {
				t.Fatalf("remove calls = %#v, want only /dst/stale.txt", calls)
			}
		})
	}
}

// The collision check only compares names within one directory, and an exact
// same-name type conflict is not a collision.
func TestFullSyncWriteIndexCollision(t *testing.T) {
	index := make(fullSyncWriteIndex)
	index.track("/dst/a/")
	index.track("/dst/")
	index.add("/dst/a/", "Photo.jpg")
	index.add("/dst/", "media")
	// Writes into directories without delete candidates are not tracked.
	index.add("/dst/b/", "Photo.jpg")

	if written, ok := index.collision("/dst/a", "photo.jpg"); !ok || written != "Photo.jpg" {
		t.Fatalf("collision(photo.jpg) = %q %v, want Photo.jpg", written, ok)
	}
	if _, ok := index.collision("/dst/b/", "photo.jpg"); ok {
		t.Fatal("collision matched across directories")
	}
	if _, ok := index.collision("/dst/", "media/"); ok {
		t.Fatal("same-name type conflict treated as a collision")
	}
	if _, ok := index.collision("/dst/a/", "other.jpg"); ok {
		t.Fatal("unrelated name treated as a collision")
	}
	if foldedEntryName("A\u0301") != foldedEntryName("\u00e1") {
		t.Fatal("NFD and NFC forms fold differently")
	}
}

// A matched directory whose source listing came back empty while its destination
// subtree is large is the signature of a listing anomaly (unmounted share, driver
// returning an empty page). Its deletes must be skipped with a visible record
// instead of trusting the empty list and emptying the directory.
func TestFullSyncSkipsDeletesUnderEmptySourceSubtree(t *testing.T) {
	t.Run("large subtree is protected", func(t *testing.T) {
		var persisted []map[string]interface{}
		restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
		defer restorePersist()

		rec := &removeRecorder{}
		server := rec.server(t)
		defer server.Close()

		// 500 matched files keep the global valve under its ratio; only the
		// 150 destinations below the anomalous directory are at risk.
		srcRoot := filesUnder("keep", 500)
		dstRoot := filesUnder("keep", 500)
		srcRoot["media/"] = FileMetadata{}
		dstRoot["media/"] = FileMetadata{}
		src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{
			"":       srcRoot,
			"media/": {},
		}}
		dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{
			"":       dstRoot,
			"media/": filesUnder("m", 150),
		}}
		plan := newFullSyncPlan(src, dst, nil)
		plan.build(map[string]interface{}{"method": 1})
		if len(plan.extraDeletes) != 150 {
			t.Fatalf("extraDeletes = %d, want 150 candidates below media/", len(plan.extraDeletes))
		}

		jt := deleteSafetyTask(t, server.URL, server.Client())
		jt.executeFullSyncPlan(plan)
		jt.runFullSyncDeletes()
		if calls := rec.calls(); len(calls) != 0 {
			t.Fatalf("remove calls = %#v, want none under a suspicious empty source directory", calls)
		}
		if err := jt.flushPersistBuffer(); err != nil {
			t.Fatalf("flushPersistBuffer() error: %v", err)
		}
		if len(persisted) != 1 {
			t.Fatalf("persisted = %#v, want exactly the guard record", persisted)
		}
		guard := persisted[0]
		if guard["type"] != taskItemTypeDelete.Int() || guard["status"] != taskStatusFailed.Int() || guard["fileName"] != "" {
			t.Fatalf("guard record = %#v, want a failed delete with an empty name", guard)
		}
	})

	t.Run("small subtree still mirrors", func(t *testing.T) {
		var persisted []map[string]interface{}
		restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
		defer restorePersist()

		rec := &removeRecorder{}
		server := rec.server(t)
		defer server.Close()

		srcRoot := filesUnder("keep", 500)
		dstRoot := filesUnder("keep", 500)
		srcRoot["media/"] = FileMetadata{}
		dstRoot["media/"] = FileMetadata{}
		src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{
			"":       srcRoot,
			"media/": {},
		}}
		dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{
			"":       dstRoot,
			"media/": filesUnder("m", 99),
		}}
		plan := newFullSyncPlan(src, dst, nil)
		plan.build(map[string]interface{}{"method": 1})

		jt := deleteSafetyTask(t, server.URL, server.Client())
		jt.executeFullSyncPlan(plan)
		jt.runFullSyncDeletes()
		if calls := rec.calls(); len(calls) != 99 {
			t.Fatalf("remove calls = %d, want 99: a small directory is ordinary mirror cleanup", len(calls))
		}
	})
}

func TestFullSyncCleanupRequiresSuccessfulWrites(t *testing.T) {
	for _, test := range []struct {
		name     string
		failCopy bool
		cancel   bool
		want     []string
	}{
		{name: "success", want: []string{"/dst/old.txt"}},
		{name: "failed copy", failCopy: true},
		{name: "canceled before cleanup", cancel: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			pinCopyTestDeps(t)
			rec := &removeRecorder{copySize: 1, failCopy: test.failCopy}
			server := rec.server(t)
			defer server.Close()
			var persisted []map[string]interface{}
			defer stubPersistJobTaskItems(t, &persisted, nil)()
			jt := deleteSafetyTask(t, server.URL, server.Client())
			src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{"": {"new.txt": {Size: 1}}}}
			dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{"": {"old.txt": {Size: 1}}}}
			plan := newFullSyncPlan(src, dst, nil)
			plan.build(jt.Job)
			jt.executeFullSyncPlan(plan)
			if calls := rec.calls(); len(calls) != 0 {
				t.Fatalf("removed while copy was still queued: %v", calls)
			}
			jt.markScanFinished()
			jt.runCopyExecutor()
			jt.drainCopyExecutor()
			if test.cancel {
				jt.requestBreak()
			}
			jt.runFullSyncDeletes()
			if calls := rec.calls(); !slices.Equal(calls, test.want) {
				t.Fatalf("remove calls = %v, want %v", calls, test.want)
			}
			if test.failCopy {
				if err := jt.flushPersistBuffer(); err != nil {
					t.Fatal(err)
				}
				if len(persisted) != 2 || persisted[1]["type"] != taskItemTypeDelete.Int() || persisted[1]["status"] != taskStatusFailed.Int() {
					t.Fatalf("expected failed copy and skipped cleanup, got %v", persisted)
				}
			}
		})
	}
}

func TestFullSyncPreservesExcludedTargetDescendants(t *testing.T) {
	for _, test := range []struct {
		name    string
		source  string
		removes []string
		writes  []string
	}{
		{"extra directory", `{"content":[{"name":"keep.txt","size":2}],"total":1}`, []string{"/dst/old/nested/junk.txt", "/dst/old/junk.bin"}, nil},
		{"type conflict", `{"content":[{"name":"old","size":7},{"name":"keep.txt","size":2}],"total":2}`, nil, nil},
		{"type conflict under a case alias", `{"content":[{"name":"OLD","size":7},{"name":"keep.txt","size":2}],"total":2}`, nil, nil},
		// The destination copy is outside the size range, but the source file is
		// in range: the filter decides what is copied, so it replaces it. The
		// excluded descendants below old/ are still kept.
		{"excluded file under a case alias", `{"content":[{"name":"TINY.DAT","size":7},{"name":"keep.txt","size":2}],"total":2}`, []string{"/dst/old/nested/junk.txt", "/dst/old/junk.bin"}, []string{"TINY.DAT"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := &removeRecorder{listings: map[string]string{
				"/src/":            test.source,
				"/dst/":            `{"content":[{"name":"old","is_dir":true},{"name":"tiny.dat","size":1},{"name":"keep.txt","size":2}],"total":3}`,
				"/dst/old/":        `{"content":[{"name":"nested","is_dir":true},{"name":"private","is_dir":true},{"name":"tiny.bin","size":1},{"name":"junk.bin","size":7}],"total":4}`,
				"/dst/old/nested/": `{"content":[{"name":"keep.tmp","size":7},{"name":"junk.txt","size":7}],"total":2}`,
			}}
			server := rec.server(t)
			defer server.Close()
			var persisted []map[string]interface{}
			defer stubPersistJobTaskItems(t, &persisted, nil)()
			jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{
				"method": 1, "srcPath": "/src/", "dstPath": "/dst/", "exclude": "*.tmp\nprivate/", "minFileSize": 2,
			})
			jt.sync()
			jt.runFullSyncDeletes()
			if calls := rec.calls(); !slices.Equal(calls, test.removes) {
				t.Fatalf("remove calls = %v, want %v", calls, test.removes)
			}
			waiting := jt.Waiting.snapshot()
			names := make([]string, 0, len(waiting))
			for _, item := range waiting {
				names = append(names, item.FileName)
			}
			if !slices.Equal(names, test.writes) {
				t.Fatalf("queued writes = %v, want %v", names, test.writes)
			}
		})
	}
}

// The reverse type conflict (source directory, destination file) must remove the
// file, create the directory and queue the files inside it.
func TestFullSyncReplacesDestinationFileWithSourceDirectory(t *testing.T) {
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	rec := &removeRecorder{}
	server := rec.server(t)
	defer server.Close()

	src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{
		"":       {"media/": {}},
		"media/": {"f.txt": {Size: 1}},
	}}
	dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{
		"": {"media": {Size: 5}},
	}}
	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{"method": 1})
	if len(plan.blockers) != 1 {
		t.Fatalf("blockers = %d, want the destination file removed for the directory", len(plan.blockers))
	}

	jt := deleteSafetyTask(t, server.URL, server.Client())
	jt.executeFullSyncPlan(plan)
	if calls := rec.calls(); len(calls) != 1 || calls[0] != "/dst/media" {
		t.Fatalf("remove calls = %#v, want only /dst/media", calls)
	}
	waiting := jt.Waiting.snapshot()
	if len(waiting) != 1 || waiting[0].DstPath != "/dst/media/" || waiting[0].FileName != "f.txt" {
		t.Fatalf("queued copies = %#v, want the file copied into the new directory", waiting)
	}
}

// A failed listing must no longer abort the whole mirror run. Entries proven by
// the listings that succeeded are still copied, but every destructive action is
// deferred to a complete run: an entry that looks "extra" may live in the part
// of the tree that could not be read.
func TestFullSyncIncompleteScanCopiesWithoutDeleting(t *testing.T) {
	deps := *jobDeps
	deps.ScanListRetryDelay = func(int) time.Duration { return 0 }
	restoreDeps := SetJobDepsForTest(&deps)
	defer restoreDeps()
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	var mu sync.Mutex
	var removes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/api/me":
			_, _ = w.Write([]byte(`{"code":200,"data":{"username":"admin"}}`))
		case "/api/fs/list":
			switch body.Path {
			case "/src/":
				_, _ = w.Write([]byte(`{"code":200,"data":{"total":2,"content":[{"name":"keep.txt","size":1},{"name":"sub","is_dir":true}]}}`))
			case "/src/sub/":
				_, _ = w.Write([]byte(`{"code":500,"message":"driver exploded","data":null}`))
			case "/dst/":
				_, _ = w.Write([]byte(`{"code":200,"data":{"total":1,"content":[{"name":"old.txt","size":1}]}}`))
			default:
				t.Errorf("unexpected list path %q", body.Path)
				_, _ = w.Write([]byte(`{"code":200,"data":{"total":0,"content":[]}}`))
			}
		case "/api/fs/mkdir":
			_, _ = w.Write([]byte(`{"code":200,"data":{}}`))
		case "/api/fs/remove":
			mu.Lock()
			removes = append(removes, body.Path)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":200,"data":{}}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()

	jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{
		"method": 1, "srcPath": "/src/", "dstPath": "/dst/", "scanIntervalT": 0,
	})
	jt.sync()
	jt.runFullSyncDeletes()

	mu.Lock()
	defer mu.Unlock()
	if len(removes) != 0 {
		t.Fatalf("remove calls = %v, want none for an incomplete scan", removes)
	}
	waiting := jt.Waiting.snapshot()
	if len(waiting) != 1 || waiting[0].FileName != "keep.txt" || waiting[0].DstPath != "/dst/" {
		t.Fatalf("queued copies = %#v, want the scanned file copied", waiting)
	}
	if err := jt.flushPersistBuffer(); err != nil {
		t.Fatal(err)
	}
	guardFound := false
	for _, item := range persisted {
		if item["type"] == taskItemTypeDelete.Int() && item["status"] == taskStatusFailed.Int() && item["fileName"] == "" {
			guardFound = true
		}
	}
	if !guardFound {
		t.Fatalf("persisted = %#v, want a guard record for the skipped cleanup", persisted)
	}
}

// One failed write must not freeze cleanup for the whole destination root, but
// it must still protect every delete that could remove the last copy of what
// that write was meant to produce.
func TestFullSyncWriteFailureBlocksOnlyRelatedDeletes(t *testing.T) {
	failures := &fullSyncWriteFailures{}
	failures.record("/dst/a/", "Report.PDF", int64(42), taskStatusFailed)

	for _, test := range []struct {
		name string
		item fullSyncDelete
		want bool
	}{
		{"same directory", fullSyncDelete{dir: "/dst/a", name: "other.txt", metadata: FileMetadata{Size: 1}}, true},
		{"subdirectory", fullSyncDelete{dir: "/dst/a/sub", name: "other.txt", metadata: FileMetadata{Size: 1}}, true},
		{"the failed directory itself", fullSyncDelete{dir: "/dst", name: "a", subtree: "a/"}, true},
		{"same name under another directory", fullSyncDelete{dir: "/dst/b", name: "report.pdf", metadata: FileMetadata{Size: 1}}, true},
		{"same size under another directory", fullSyncDelete{dir: "/dst/b", name: "other.bin", metadata: FileMetadata{Size: 42}}, true},
		{"unrelated entry", fullSyncDelete{dir: "/dst/b", name: "other.bin", metadata: FileMetadata{Size: 7}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := failures.blocks(test.item); got != test.want {
				t.Fatalf("blocks(%+v) = %v, want %v", test.item, got, test.want)
			}
		})
	}

	var empty fullSyncWriteFailures
	if empty.blocks(fullSyncDelete{dir: "/dst/a", name: "Report.PDF"}) {
		t.Fatal("no recorded failure must not block any delete")
	}
}

// When the destination root itself is "/", fullSyncPathBlocked must still check
// "/" before terminating the ancestor walk so empty-source protection and
// write-failure guards are never bypassed.
func TestFullSyncPathBlockedHandlesRootPath(t *testing.T) {
	blocked := map[string]struct{}{"/": {}}
	for _, path := range []string{"/", "/file.txt", "/a/b/file.txt"} {
		if !fullSyncPathBlocked(path, blocked) {
			t.Fatalf(`fullSyncPathBlocked(%q, {"/": {}}) = false, want true`, path)
		}
	}
	if fullSyncPathBlocked("relative.txt", blocked) {
		t.Fatal(`fullSyncPathBlocked(relative.txt, {"/": {}}) = true, want false`)
	}

	failures := &fullSyncWriteFailures{}
	failures.record("/", "fail.txt", int64(5), taskStatusFailed)
	if !failures.blocks(fullSyncDelete{dir: "/sub", name: "other.txt", metadata: FileMetadata{Size: 1}}) {
		t.Fatal(`write failure at root "/" failed to block delete in "/sub"`)
	}
}

func TestFullSyncRootDestinationGuardsDeletesOnEmptySource(t *testing.T) {
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	rec := &removeRecorder{}
	server := rec.server(t)
	defer server.Close()

	src := &fullSyncSnapshot{root: "/", dirs: map[string]FileListResult{"": {}}}
	dst := &fullSyncSnapshot{root: "/", dirs: map[string]FileListResult{
		"": {"keep.txt": {Size: 1}, "sub/": {}},
		"sub/": {"nested.txt": {Size: 2}},
	}}
	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{"method": 1})

	suspicious := plan.suspiciousEmptySourceDirs()
	if _, ok := suspicious["/"]; !ok {
		t.Fatalf(`suspiciousEmptySourceDirs = %#v, want root "/" flagged`, suspicious)
	}

	jt := deleteSafetyTask(t, server.URL, server.Client())
	jt.executeFullSyncPlan(plan)
	jt.runFullSyncDeletes()
	if calls := rec.calls(); len(calls) != 0 {
		t.Fatalf(`remove calls = %#v, want none when root "/" source listed empty`, calls)
	}
}

func TestFullSyncIncompleteScanGuardDistinguishesSrcAndDstFailure(t *testing.T) {
	cases := []struct {
		name           string
		srcFailed      bool
		dstFailed      bool
		wantSubstring  string
		forbidSubstr   string
	}{
		{
			name:          "destination scan failed only",
			dstFailed:     true,
			wantSubstring: "full-sync destination scan was incomplete",
			forbidSubstr:  "请确认来源目录可正常列举后重试",
		},
		{
			name:          "source scan failed only",
			srcFailed:     true,
			wantSubstring: "full-sync source scan was incomplete",
			forbidSubstr:  "请确认来源目录可正常列举后重试",
		},
		{
			name:          "both scans failed",
			srcFailed:     true,
			dstFailed:     true,
			wantSubstring: "full-sync source and destination scans were incomplete",
			forbidSubstr:  "请确认来源目录可正常列举后重试",
		},
		{
			name:          "generic scanIncomplete fallback",
			wantSubstring: "full-sync scan was incomplete",
			forbidSubstr:  "请确认来源目录可正常列举后重试",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var persisted []map[string]interface{}
			restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
			defer restorePersist()

			rec := &removeRecorder{}
			server := rec.server(t)
			defer server.Close()

			src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{"": {"a.txt": {Size: 1}}}}
			dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{"": {"a.txt": {Size: 1}}}}
			plan := newFullSyncPlan(src, dst, nil)
			plan.scanIncomplete = true
			plan.srcScanFailed = tc.srcFailed
			plan.dstScanFailed = tc.dstFailed

			jt := deleteSafetyTask(t, server.URL, server.Client())
			jt.executeFullSyncPlan(plan)
			if err := jt.flushPersistBuffer(); err != nil {
				t.Fatalf("flushPersistBuffer() error: %v", err)
			}
			if len(persisted) != 1 {
				t.Fatalf("persisted = %#v, want 1 guard record", persisted)
			}
			gotMsg := taskItemErrMsg(persisted[0]["errMsg"])
			if !strings.Contains(gotMsg, tc.wantSubstring) {
				t.Fatalf("guard errMsg = %q, want substring %q", gotMsg, tc.wantSubstring)
			}
			if strings.Contains(gotMsg, tc.forbidSubstr) {
				t.Fatalf("guard errMsg = %q, must not contain misleading text %q", gotMsg, tc.forbidSubstr)
			}
		})
	}
}
