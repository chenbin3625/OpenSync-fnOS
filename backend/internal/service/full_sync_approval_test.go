package service

import (
	"fmt"
	"testing"
	"time"
)

func waitForFullSyncApproval(t *testing.T, jt *JobTask) *FullSyncApproval {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if approval := jt.fullSyncApprovalSnapshot(); approval != nil {
			return approval
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("full sync did not offer a deletion preview")
	return nil
}

func waitForSyncExit(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("full sync did not exit after approval")
	}
}

func approvalTestTask(t *testing.T, rec *removeRecorder) *JobTask {
	t.Helper()
	deps := *jobDeps
	deps.ScanListRetryDelay = func(int) time.Duration { return 0 }
	restoreDeps := SetJobDepsForTest(&deps)
	t.Cleanup(restoreDeps)
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	t.Cleanup(restorePersist)

	server := rec.server(t)
	t.Cleanup(server.Close)
	jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{
		"method":        1,
		"srcPath":       "/src/",
		"dstPath":       "/dst/",
		"scanIntervalT": 0,
	})
	jt.RequireFullSyncApproval = true
	return jt
}

func TestFullSyncWaitsForApprovalBeforeDeleting(t *testing.T) {
	rec := &removeRecorder{listings: map[string]string{
		"/src/": `{"total":1,"content":[{"name":"keep.txt","is_dir":false,"size":1}]}`,
		"/dst/": `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"old.txt","is_dir":false,"size":12}]}`,
	}}
	jt := approvalTestTask(t, rec)
	done := make(chan struct{})
	go func() {
		defer close(done)
		jt.sync()
	}()

	approval := waitForFullSyncApproval(t, jt)
	if approval.DeleteCount != 1 || approval.DeleteSize != 12 {
		t.Fatalf("approval = %+v, want one 12-byte delete", approval)
	}
	if calls := rec.calls(); len(calls) != 0 {
		t.Fatalf("remove calls before approval = %#v", calls)
	}
	if err := jt.approveFullSync(); err != nil {
		t.Fatalf("approveFullSync() error: %v", err)
	}
	waitForSyncExit(t, done)
	jt.runFullSyncDeletes()
	if calls := rec.calls(); len(calls) != 1 || calls[0] != "/dst/old.txt" {
		t.Fatalf("remove calls after approval = %#v, want /dst/old.txt", calls)
	}
}

func TestFullSyncRejectKeepsDestinationUntouched(t *testing.T) {
	rec := &removeRecorder{listings: map[string]string{
		"/src/": `{"total":1,"content":[{"name":"keep.txt","is_dir":false,"size":1}]}`,
		"/dst/": `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"old.txt","is_dir":false,"size":12}]}`,
	}}
	jt := approvalTestTask(t, rec)
	done := make(chan struct{})
	go func() {
		defer close(done)
		jt.sync()
	}()

	waitForFullSyncApproval(t, jt)
	jt.requestBreak()
	waitForSyncExit(t, done)
	jt.runFullSyncDeletes()
	if calls := rec.calls(); len(calls) != 0 {
		t.Fatalf("remove calls after rejection = %#v", calls)
	}
}

func TestFullSyncRepromptsWhenPlanChangesAfterApproval(t *testing.T) {
	rec := &removeRecorder{listings: map[string]string{
		"/src/": `{"total":1,"content":[{"name":"keep.txt","is_dir":false,"size":1}]}`,
		"/dst/": `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"old.txt","is_dir":false,"size":12}]}`,
	}}
	jt := approvalTestTask(t, rec)
	done := make(chan struct{})
	go func() {
		defer close(done)
		jt.sync()
	}()

	first := waitForFullSyncApproval(t, jt)
	if first.DeleteCount != 1 {
		t.Fatalf("first approval = %+v, want one delete", first)
	}
	rec.listings["/dst/"] = `{"total":3,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"old.txt","is_dir":false,"size":12},{"name":"new.txt","is_dir":false,"size":8}]}`
	if err := jt.approveFullSync(); err != nil {
		t.Fatalf("first approveFullSync() error: %v", err)
	}

	second := waitForFullSyncApproval(t, jt)
	if second.DeleteCount != 2 || second.DeleteSize != 20 {
		t.Fatalf("second approval = %+v, want both destination files", second)
	}
	if err := jt.approveFullSync(); err != nil {
		t.Fatalf("second approveFullSync() error: %v", err)
	}
	waitForSyncExit(t, done)
	jt.runFullSyncDeletes()
	if calls := rec.calls(); len(calls) != 2 {
		t.Fatalf("remove calls = %#v, want both files", calls)
	}
}

func TestFullSyncApprovalCombinesAllDestinationRoots(t *testing.T) {
	rec := &removeRecorder{listings: map[string]string{
		"/src/":     `{"total":1,"content":[{"name":"keep.txt","is_dir":false,"size":1}]}`,
		"/one-dst/": `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"a.txt","is_dir":false,"size":1}]}`,
		"/two-dst/": `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"b.txt","is_dir":false,"size":2}]}`,
	}}
	deps := *jobDeps
	deps.ScanListRetryDelay = func(int) time.Duration { return 0 }
	restoreDeps := SetJobDepsForTest(&deps)
	defer restoreDeps()
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()
	server := rec.server(t)
	defer server.Close()
	jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{
		"method":        1,
		"srcPath":       []string{"/src/"},
		"dstPath":       []string{"/one-dst/", "/two-dst/"},
		"scanIntervalT": 0,
	})
	jt.RequireFullSyncApproval = true
	done := make(chan struct{})
	go func() {
		defer close(done)
		jt.sync()
	}()

	approval := waitForFullSyncApproval(t, jt)
	if approval.DeleteCount != 2 || approval.PathCount != 2 {
		t.Fatalf("combined approval = %+v, want both roots in one preview", approval)
	}
	if err := jt.approveFullSync(); err != nil {
		t.Fatalf("approveFullSync() error: %v", err)
	}
	waitForSyncExit(t, done)
	jt.runFullSyncDeletes()
	if calls := rec.calls(); len(calls) != 2 {
		t.Fatalf("remove calls = %#v, want one per destination", calls)
	}
}

func TestFullSyncApprovalErrorDoesNotExposeDetachedClient(t *testing.T) {
	jt := &JobTask{TaskID: 7}
	if err := jt.approveFullSync(); err == nil || err.Error() != "全量同步当前不在待确认状态" {
		t.Fatalf("approveFullSync() error = %v", err)
	}
}

func TestFullSyncApprovalPreviewBoundedPaths(t *testing.T) {
	src := make(FileListResult)
	dst := make(FileListResult)
	for i := 0; i < 200; i++ {
		name := fmt.Sprintf("kept-%d.txt", i)
		src[name] = FileMetadata{Size: 1}
		dst[name] = FileMetadata{Size: 1}
	}
	for i := 0; i < fullSyncApprovalPathLimit+5; i++ {
		dst[fmt.Sprintf("file-%d.txt", i)] = FileMetadata{Size: 1}
	}
	plan := newFullSyncPlan(
		&fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{"": src}},
		&fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{"": dst}},
		nil,
	)
	plan.build(map[string]interface{}{"method": 1})
	approval := plan.approvalPreview()
	if approval == nil {
		t.Fatal("approvalPreview() = nil")
	}
	want := fullSyncApprovalPathLimit + 5
	if approval.PathCount != want || len(approval.Paths) != fullSyncApprovalPathLimit {
		t.Fatalf("preview paths = %d/%d, want %d of %d", len(approval.Paths), approval.PathCount, fullSyncApprovalPathLimit, want)
	}
}

// A forgotten confirmation must not block the job forever. On timeout the safe
// half of the plan still runs (copies and new directories), while every
// destructive action is skipped for a later, explicitly approved run.
func TestFullSyncApprovalTimeoutRunsCopiesWithoutDeleting(t *testing.T) {
	oldWait := fullSyncApprovalWait
	fullSyncApprovalWait = 20 * time.Millisecond
	defer func() { fullSyncApprovalWait = oldWait }()

	rec := &removeRecorder{listings: map[string]string{
		"/src/": `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"new.txt","is_dir":false,"size":3}]}`,
		"/dst/": `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"old.txt","is_dir":false,"size":12}]}`,
	}}
	jt := approvalTestTask(t, rec)
	done := make(chan struct{})
	go func() {
		defer close(done)
		jt.sync()
	}()
	waitForSyncExit(t, done)
	jt.runFullSyncDeletes()

	if calls := rec.calls(); len(calls) != 0 {
		t.Fatalf("remove calls after an approval timeout = %#v, want none", calls)
	}
	waiting := jt.Waiting.snapshot()
	if len(waiting) != 1 || waiting[0].FileName != "new.txt" {
		t.Fatalf("queued copies = %#v, want new.txt copied despite the timeout", waiting)
	}
	if approval := jt.fullSyncApprovalSnapshot(); approval != nil {
		t.Fatalf("approval still pending after timeout: %+v", approval)
	}
}

// When one file inside a recursively deleted directory is relocated while other
// files remain in that directory, approvalPreview must still report the
// remaining deleted entries instead of hiding the whole directory.
func TestFullSyncApprovalPreviewAccountsForPartialDirectoryRelocation(t *testing.T) {
	relocatedMeta := FileMetadata{
		Size: 100,
		MD5:  "0123456789abcdef0123456789abcdef",
	}
	dstDir := FileListResult{"moved.bin": relocatedMeta}
	for i := 0; i < 500; i++ {
		dstDir[fmt.Sprintf("stay-%d.txt", i)] = FileMetadata{Size: 10}
	}

	srcRoot := FileListResult{"new/": {}}
	dstRoot := FileListResult{"dir/": {}}
	for i := 0; i < 1000; i++ {
		name := fmt.Sprintf("keep-%d.txt", i)
		srcRoot[name] = FileMetadata{Size: 1}
		dstRoot[name] = FileMetadata{Size: 1}
	}
	src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{
		"":     srcRoot,
		"new/": {"moved.bin": relocatedMeta},
	}}
	dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{
		"":     dstRoot,
		"dir/": dstDir,
	}}

	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{"method": 1})

	approval := plan.approvalPreview()
	if approval == nil {
		t.Fatal("approvalPreview() = nil, want remaining directory deletion + move")
	}
	// 500 remaining files + dir/ itself = 501; 500 * 10 = 5000 bytes.
	if approval.DeleteCount != 501 || approval.DeleteSize != 5000 {
		t.Fatalf("approval = %+v, want DeleteCount=501 DeleteSize=5000", approval)
	}
	if approval.PathCount != 2 {
		t.Fatalf("PathCount = %d, want 2 (directory delete + file move)", approval.PathCount)
	}
	var foundDirDelete, foundMove bool
	for _, p := range approval.Paths {
		if p.Path == "/dst/dir" && p.Action == "删除" && p.IsPath && p.Count == 501 && p.Size == 5000 {
			foundDirDelete = true
		}
		if p.Path == "/dst/dir/moved.bin" && p.Action == "移动" && !p.IsPath {
			foundMove = true
		}
	}
	if !foundDirDelete || !foundMove {
		t.Fatalf("approval.Paths = %+v, want /dst/dir delete (501/5000) and /dst/dir/moved.bin move", approval.Paths)
	}
}
