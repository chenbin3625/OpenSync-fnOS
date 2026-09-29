package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// removeRecorder is an AList stub that answers mkdir/remove and records every
// remove request as dir+name. failRemove makes every remove fail.
type removeRecorder struct {
	mu         sync.Mutex
	removes    []string
	failRemove bool
}

func (rec *removeRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
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

			rec := &removeRecorder{}
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

			// The unrelated extra file is still deleted; the colliding ones are not.
			if calls := rec.calls(); len(calls) != 1 || calls[0] != "/dst/stale.txt" {
				t.Fatalf("remove calls = %#v, want only /dst/stale.txt", calls)
			}
			waiting := jt.Waiting.snapshot()
			if len(waiting) != 2 {
				t.Fatalf("queued = %d, want both copies of %q", len(waiting), tc.srcName)
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
