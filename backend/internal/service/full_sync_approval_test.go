package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"opensync/internal/mapper"
	"strings"
	"sync"
	"testing"
	"time"
)

func waitForPendingApprovalCleared(t *testing.T, jobID int64) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !pendingDeleteApprovals.Has(jobID) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pending delete approval for job %d was not cleared in time (current=%+v)", jobID, pendingDeleteApprovals.GetByJobID(jobID))
}

func waitForPendingApprovalPlanChanged(t *testing.T, jobID int64) *FullSyncApproval {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if a := pendingDeleteApprovals.GetByJobID(jobID); a != nil && a.Status == pendingApprovalStatusPending && a.PlanChanged {
			return a
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("pending delete approval for job %d did not return to pending with PlanChanged=true (current=%+v)", jobID, pendingDeleteApprovals.GetByJobID(jobID))
	return nil
}

func waitForSyncExit(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("full sync blocked instead of completing without waiting for approval")
	}
}

func approvalTestTask(t *testing.T, jobID, taskID int64, rec *removeRecorder) *JobTask {
	t.Helper()
	pendingDeleteApprovals.ResetForTest()
	t.Cleanup(pendingDeleteApprovals.ResetForTest)

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
		"id":            jobID,
		"method":        1,
		"srcPath":       "/src/",
		"dstPath":       "/dst/",
		"scanIntervalS": 0,
		"scanIntervalT": 0,
	})
	jt.TaskID = taskID
	jt.RequireFullSyncApproval = true
	return jt
}

// Core requirement: file deletion confirmation is post-positioned and MUST NOT
// block the sync task. Copies/updates run and finish immediately; deletions
// stay queued in pending approval until the operator confirms.
func TestFullSyncCompletesWithoutBlockingAndDeletesOnlyAfterApproval(t *testing.T) {
	rec := &removeRecorder{
		listings: map[string]string{
			"/src/": `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"new.txt","is_dir":false,"size":5}]}`,
			"/dst/": `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"old.txt","is_dir":false,"size":12}]}`,
		},
		copySize: 5,
	}
	jt := approvalTestTask(t, 101, 1001, rec)
	done := make(chan struct{})
	go func() {
		defer close(done)
		jt.sync()
	}()

	// Sync finishes immediately without blocking on approval.
	waitForSyncExit(t, done)

	// New file was queued for copy immediately during sync.
	waiting := jt.Waiting.snapshot()
	if len(waiting) != 1 || waiting[0].FileName != "new.txt" {
		t.Fatalf("queued copies = %#v, want new.txt queued immediately", waiting)
	}

	// Post-copy delete phase stages pending deletion approval without blocking
	// and without deleting yet.
	jt.runFullSyncDeletes()
	if calls := rec.calls(); len(calls) != 0 {
		t.Fatalf("remove calls before approval = %#v, want none", calls)
	}
	approval := GetPendingApproval(101)
	if approval == nil {
		t.Fatal("expected pending deletion approval after full sync")
	}
	if approval.TaskID != 1001 || approval.DeleteCount != 1 || approval.DeleteSize != 12 {
		t.Fatalf("approval = %+v, want taskId=1001, 1 delete, 12 bytes", approval)
	}
	if approval.Status != pendingApprovalStatusPending {
		t.Fatalf("approval.Status = %q, want %q", approval.Status, pendingApprovalStatusPending)
	}

	// Simulate destination after copy completed (new.txt now exists on dst).
	rec.listings["/dst/"] = `{"total":3,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"new.txt","is_dir":false,"size":5},{"name":"old.txt","is_dir":false,"size":12}]}`

	// Approve asynchronously and wait for background deletion worker to finish.
	if err := ApproveFullSyncTask(1001); err != nil {
		t.Fatalf("ApproveFullSyncTask() error: %v", err)
	}
	waitForPendingApprovalCleared(t, 101)

	if calls := rec.calls(); len(calls) != 1 || calls[0] != "/dst/old.txt" {
		t.Fatalf("remove calls after approval = %#v, want [/dst/old.txt]", calls)
	}
}

// Rejecting/skipping a pending deletion approval keeps destination untouched
// and NEVER aborts or breaks an active sync task.
func TestFullSyncRejectKeepsDestinationAndDoesNotBreakSyncTask(t *testing.T) {
	rec := &removeRecorder{listings: map[string]string{
		"/src/": `{"total":1,"content":[{"name":"keep.txt","is_dir":false,"size":1}]}`,
		"/dst/": `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"old.txt","is_dir":false,"size":12}]}`,
	}}
	jt := approvalTestTask(t, 102, 1002, rec)
	jt.sync()
	jt.runFullSyncDeletes()

	if !pendingDeleteApprovals.Has(102) {
		t.Fatal("expected pending deletion approval")
	}
	if err := RejectFullSyncTask(1002); err != nil {
		t.Fatalf("RejectFullSyncTask() error: %v", err)
	}
	if pendingDeleteApprovals.Has(102) {
		t.Fatal("pending approval still present after reject")
	}
	if jt.isBreak() {
		t.Fatal("rejecting pending deletion must not break the sync task")
	}
	if calls := rec.calls(); len(calls) != 0 {
		t.Fatalf("remove calls after rejection = %#v, want none", calls)
	}
}

// If a file scheduled for deletion is restored on the source before approval,
// live verification skips deleting it so user data is never lost.
func TestPendingDeleteSkipsFileRestoredOnSourceBeforeApproval(t *testing.T) {
	rec := &removeRecorder{listings: map[string]string{
		"/src/": `{"total":1,"content":[{"name":"keep.txt","is_dir":false,"size":1}]}`,
		"/dst/": `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"old.txt","is_dir":false,"size":12}]}`,
	}}
	jt := approvalTestTask(t, 103, 1003, rec)
	jt.sync()
	jt.runFullSyncDeletes()

	// Source regains old.txt before the operator approves deletion.
	rec.listings["/src/"] = `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"old.txt","is_dir":false,"size":12}]}`

	if err := ApproveFullSyncTask(1003); err != nil {
		t.Fatalf("ApproveFullSyncTask() error: %v", err)
	}
	waitForPendingApprovalCleared(t, 103)

	if calls := rec.calls(); len(calls) != 0 {
		t.Fatalf("remove calls = %#v, want none after file reappeared on source", calls)
	}
}

// If a directory scheduled for recursive deletion receives a new file before
// the operator approves, verification refuses to delete and re-prompts with
// the updated count and size.
func TestPendingDeleteRepromptsWhenApprovedDirectoryChanges(t *testing.T) {
	rec := &removeRecorder{listings: map[string]string{
		"/src/":          `{"total":1,"content":[{"name":"keep.txt","is_dir":false,"size":1}]}`,
		"/dst/":          `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"old_dir","is_dir":true,"size":0}]}`,
		"/dst/old_dir/":  `{"total":1,"content":[{"name":"a.txt","is_dir":false,"size":10}]}`,
		"/src/old_dir/":  `{"total":0,"content":[]}`, // not queried because /src/ doesn't have old_dir
	}}
	jt := approvalTestTask(t, 104, 1004, rec)
	jt.sync()
	jt.runFullSyncDeletes()

	first := GetPendingApproval(104)
	if first == nil || first.DeleteCount != 2 || first.DeleteSize != 10 {
		t.Fatalf("first approval = %+v, want DeleteCount=2 DeleteSize=10", first)
	}

	// Someone drops an important file into /dst/old_dir/ before approval!
	rec.listings["/dst/old_dir/"] = `{"total":2,"content":[{"name":"a.txt","is_dir":false,"size":10},{"name":"important.doc","is_dir":false,"size":50}]}`

	if err := ApproveFullSyncTask(1004); err != nil {
		t.Fatalf("first ApproveFullSyncTask() error: %v", err)
	}
	second := waitForPendingApprovalPlanChanged(t, 104)
	if calls := rec.calls(); len(calls) != 0 {
		t.Fatalf("remove calls when directory changed = %#v, want none", calls)
	}
	if second.DeleteCount != 3 || second.DeleteSize != 60 {
		t.Fatalf("updated approval = %+v, want DeleteCount=3 DeleteSize=60", second)
	}

	// Operator reviews the updated list and confirms again -> now deletes cleanly.
	if err := ApproveFullSyncTask(1004); err != nil {
		t.Fatalf("second ApproveFullSyncTask() error: %v", err)
	}
	waitForPendingApprovalCleared(t, 104)
	if calls := rec.calls(); len(calls) != 1 || calls[0] != "/dst/old_dir" {
		t.Fatalf("remove calls after re-approval = %#v, want [/dst/old_dir]", calls)
	}
}

// If the source root suddenly lists empty during verification, no files may be
// deleted and the error must be surfaced on the pending approval.
func TestPendingDeleteAbortsWhenSourceRootTurnsEmpty(t *testing.T) {
	rec := &removeRecorder{listings: map[string]string{
		"/src/": `{"total":1,"content":[{"name":"keep.txt","is_dir":false,"size":1}]}`,
		"/dst/": `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"old.txt","is_dir":false,"size":12}]}`,
	}}
	jt := approvalTestTask(t, 105, 1005, rec)
	jt.sync()
	jt.runFullSyncDeletes()

	// Source mount disappears / returns empty root before approval.
	rec.listings["/src/"] = `{"total":0,"content":[]}`

	if err := ApproveFullSyncTask(1005); err != nil {
		t.Fatalf("ApproveFullSyncTask() error: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a := GetPendingApproval(105)
		if a != nil && a.Status == pendingApprovalStatusPending && a.LastError != "" {
			if calls := rec.calls(); len(calls) != 0 {
				t.Fatalf("remove calls when source root empty = %#v, want none", calls)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("expected approval to return to pending with LastError, got %+v", GetPendingApproval(105))
}

func TestFullSyncApprovalCombinesAllDestinationRoots(t *testing.T) {
	pendingDeleteApprovals.ResetForTest()
	defer pendingDeleteApprovals.ResetForTest()

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
		"id":            int64(106),
		"method":        1,
		"srcPath":       []string{"/src/"},
		"dstPath":       []string{"/one-dst/", "/two-dst/"},
		"scanIntervalS": 0,
		"scanIntervalT": 0,
	})
	jt.TaskID = 1006
	jt.RequireFullSyncApproval = true

	jt.sync()
	jt.runFullSyncDeletes()

	approval := GetPendingApproval(106)
	if approval == nil || approval.DeleteCount != 2 || approval.PathCount != 2 || approval.DeleteSize != 3 {
		t.Fatalf("combined approval = %+v, want both roots in one pending approval", approval)
	}
	if err := ApproveFullSyncTask(1006); err != nil {
		t.Fatalf("ApproveFullSyncTask() error: %v", err)
	}
	waitForPendingApprovalCleared(t, 106)
	if calls := rec.calls(); len(calls) != 2 {
		t.Fatalf("remove calls = %#v, want one per destination", calls)
	}
}

func TestApproveWithoutPendingReturnsFriendlyError(t *testing.T) {
	pendingDeleteApprovals.ResetForTest()
	defer pendingDeleteApprovals.ResetForTest()

	if err := ApproveFullSyncTask(7777); err == nil || err.Error() != "当前没有待确认的删除项" {
		t.Fatalf("ApproveFullSyncTask() error = %v", err)
	}
	if err := RejectFullSyncTask(7777); err == nil || err.Error() != "当前没有待确认的删除项" {
		t.Fatalf("RejectFullSyncTask() error = %v", err)
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

// A pending deletion approval expires automatically after FullSyncApprovalTimeout
// without ever blocking the sync task and without deleting destination files.
func TestPendingDeleteApprovalExpiresWithoutBlockingSync(t *testing.T) {
	oldWait := fullSyncApprovalWait
	fullSyncApprovalWait = 40 * time.Millisecond
	defer func() { fullSyncApprovalWait = oldWait }()

	rec := &removeRecorder{listings: map[string]string{
		"/src/": `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"new.txt","is_dir":false,"size":3}]}`,
		"/dst/": `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"old.txt","is_dir":false,"size":12}]}`,
	}}
	jt := approvalTestTask(t, 107, 1007, rec)
	jt.sync()
	jt.runFullSyncDeletes()

	// Copies queued immediately without waiting for timeout.
	waiting := jt.Waiting.snapshot()
	if len(waiting) != 1 || waiting[0].FileName != "new.txt" {
		t.Fatalf("queued copies = %#v, want new.txt queued immediately", waiting)
	}
	if !pendingDeleteApprovals.Has(107) {
		t.Fatal("expected pending approval immediately after sync")
	}

	// Wait for approval to expire cleanly.
	waitForPendingApprovalCleared(t, 107)

	if calls := rec.calls(); len(calls) != 0 {
		t.Fatalf("remove calls after approval expiry = %#v, want none", calls)
	}
	if err := ApproveFullSyncTask(1007); err == nil {
		t.Fatal("ApproveFullSyncTask() succeeded after expiry, want error")
	}
}

// When one file inside a recursively deleted directory is relocated while other
// files remain in that directory, approvalPreview reports only the remaining
// deleted entries (relocations run non-destructively during sync).
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
		t.Fatal("approvalPreview() = nil, want remaining directory deletion")
	}
	// 500 remaining files + dir/ itself = 501; 500 * 10 = 5000 bytes.
	if approval.DeleteCount != 501 || approval.DeleteSize != 5000 {
		t.Fatalf("approval = %+v, want DeleteCount=501 DeleteSize=5000", approval)
	}
	if approval.PathCount != 1 {
		t.Fatalf("PathCount = %d, want 1 (remaining directory delete only)", approval.PathCount)
	}
	if len(approval.Paths) != 1 || approval.Paths[0].Path != "/dst/dir" || approval.Paths[0].Action != "删除" || !approval.Paths[0].IsPath || approval.Paths[0].Count != 501 || approval.Paths[0].Size != 5000 {
		t.Fatalf("approval.Paths = %+v, want /dst/dir delete (501/5000)", approval.Paths)
	}
}

// Type-conflict blockers must also be deferred when RequireFullSyncApproval is
// enabled so zero destination deletions happen before confirmation.
func TestFullSyncDefersTypeConflictBlockerUntilApproval(t *testing.T) {
	rec := &removeRecorder{listings: map[string]string{
		"/src/":      `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"item","is_dir":true,"size":0}]}`,
		"/src/item/": `{"total":1,"content":[{"name":"child.txt","is_dir":false,"size":4}]}`,
		"/dst/":      `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"item","is_dir":false,"size":9}]}`,
	}}
	jt := approvalTestTask(t, 108, 1008, rec)
	jt.sync()
	jt.runFullSyncDeletes()

	if calls := rec.calls(); len(calls) != 0 {
		t.Fatalf("blocker deleted before approval: %#v", calls)
	}
	approval := GetPendingApproval(108)
	if approval == nil || approval.DeleteCount != 1 || approval.DeleteSize != 9 {
		t.Fatalf("approval = %+v, want blocker /dst/item (9 bytes)", approval)
	}
	if err := ApproveFullSyncTask(1008); err != nil {
		t.Fatalf("ApproveFullSyncTask() error: %v", err)
	}
	waitForPendingApprovalCleared(t, 108)
	if calls := rec.calls(); len(calls) != 1 || calls[0] != "/dst/item" {
		t.Fatalf("remove calls after blocker approval = %#v, want [/dst/item]", calls)
	}
}

// Pending deletion approvals persist in SQLite across restarts and refresh the
// historical task record counters when confirmed.
func TestPendingDeleteApprovalPersistsAcrossRestartAndUpdatesTaskHistory(t *testing.T) {
	_, cleanup := setupFullFeatureTestDB(t)
	defer cleanup()
	pendingDeleteApprovals.ResetForTest()
	defer pendingDeleteApprovals.ResetForTest()

	rec := &removeRecorder{listings: map[string]string{
		"/src/": `{"total":1,"content":[{"name":"keep.txt","is_dir":false,"size":1}]}`,
		"/dst/": `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"old.txt","is_dir":false,"size":12}]}`,
	}}
	server := rec.server(t)
	defer server.Close()

	alistID, err := mapper.AddAlistWithConcurrency("E1", server.URL, "admin", "tok", 0)
	if err != nil {
		t.Fatalf("AddAlistWithConcurrency: %v", err)
	}
	jobID, err := mapper.AddJob(map[string]interface{}{
		"enable": 1, "remark": "持久化审批测试", "srcPath": `["/src/"]`, "dstPath": `["/dst/"]`,
		"alistId": alistID, "method": 1, "isCron": 2,
	})
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	origRunTime := time.Now().Unix() - 300
	taskID, err := mapper.AddJobTask(jobID, origRunTime)
	if err != nil {
		t.Fatalf("AddJobTask: %v", err)
	}
	if err := mapper.UpdateJobTaskStatusAndNumPreserveRunTime(taskID, taskStatusNoSync.Int(), nil, `{"allNum":0,"successNum":0,"failNum":0,"otherNum":0,"waitNum":0,"runningNum":0,"sumSize":0,"duration":5}`); err != nil {
		t.Fatalf("seed task status: %v", err)
	}

	jobRow, err := mapper.GetJobByID(jobID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	jt := scanTestTask(t, server.URL, server.Client(), jobRow)
	jt.TaskID = taskID
	jt.RequireFullSyncApproval = true
	jt.sync()
	jt.runFullSyncDeletes()

	if !pendingDeleteApprovals.Has(jobID) {
		t.Fatal("expected pending approval before simulated restart")
	}

	// Simulate process restart: wipe in-memory map and reload from SQLite.
	pendingDeleteApprovals.ResetForTest()
	if pendingDeleteApprovals.Has(jobID) {
		t.Fatal("in-memory map should be empty after reset")
	}
	pendingDeleteApprovals.LoadFromDB()
	restored := GetPendingApproval(jobID)
	if restored == nil || restored.TaskID != taskID || restored.DeleteCount != 1 {
		t.Fatalf("restored approval = %+v, want taskId=%d deleteCount=1", restored, taskID)
	}

	// Approve after restart -> uses persisted alistId to connect and delete.
	if err := ApproveFullSyncTask(taskID); err != nil {
		t.Fatalf("ApproveFullSyncTask after restart error: %v", err)
	}
	waitForPendingApprovalCleared(t, jobID)

	if calls := rec.calls(); len(calls) != 1 || calls[0] != "/dst/old.txt" {
		t.Fatalf("remove calls after restart approval = %#v, want [/dst/old.txt]", calls)
	}

	// Verify history task status upgraded from NoSync (8) -> Success (2) and
	// preserved original runTime & duration.
	updatedTask, err := mapper.GetJobTaskByID(taskID)
	if err != nil {
		t.Fatalf("GetJobTaskByID: %v", err)
	}
	if got := taskStatusFromValue(updatedTask["status"]); got != taskStatusSuccess {
		t.Fatalf("updated task status = %d, want %d (Success)", got, taskStatusSuccess)
	}
	if gotRunTime := updatedTask["runTime"].(int64); gotRunTime != origRunTime {
		t.Fatalf("runTime = %d, want preserved %d", gotRunTime, origRunTime)
	}
	parsedNum := parseTaskNumJSON(updatedTask["taskNum"])
	if parsedNum == nil || parsedNum["successNum"].(float64) != 1 || parsedNum["duration"].(float64) != 5 {
		t.Fatalf("updated taskNum = %+v, want successNum=1 and preserved duration=5", parsedNum)
	}
}

// Pending deletion approvals must never block subsequent sync runs on the same
// job, and a later clean sync (or non-confirm scheduled sync that deletes) must
// clear stale pending approvals automatically.
func TestPendingDeleteApprovalDoesNotBlockSubsequentFullSyncRun(t *testing.T) {
	pendingDeleteApprovals.ResetForTest()
	defer pendingDeleteApprovals.ResetForTest()

	deps := *jobDeps
	deps.ScanListRetryDelay = func(int) time.Duration { return 0 }
	restoreDeps := SetJobDepsForTest(&deps)
	defer restoreDeps()
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	rec := &removeRecorder{
		listings: map[string]string{
			"/src/": `{"total":1,"content":[{"name":"keep.txt","is_dir":false,"size":1}]}`,
			"/dst/": `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"old.txt","is_dir":false,"size":12}]}`,
		},
		copySize: 4,
	}
	server := rec.server(t)
	defer server.Close()
	jobCfg := map[string]interface{}{
		"id":            int64(109),
		"method":        1,
		"srcPath":       "/src/",
		"dstPath":       "/dst/",
		"scanIntervalS": 0,
		"scanIntervalT": 0,
	}

	jt1 := scanTestTask(t, server.URL, server.Client(), jobCfg)
	jt1.TaskID = 1009
	jt1.RequireFullSyncApproval = true
	jt1.sync()
	jt1.runFullSyncDeletes()

	a1 := GetPendingApproval(109)
	if a1 == nil || a1.TaskID != 1009 || a1.DeleteCount != 1 {
		t.Fatalf("first pending approval = %+v, want taskId=1009 deleteCount=1", a1)
	}

	// Run a second sync immediately while the first approval is still pending.
	rec.listings["/src/"] = `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"second.txt","is_dir":false,"size":4}]}`
	jt2 := scanTestTask(t, server.URL, server.Client(), jobCfg)
	jt2.TaskID = 1010
	jt2.RequireFullSyncApproval = true
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		jt2.sync()
		jt2.runFullSyncDeletes()
	}()
	waitForSyncExit(t, done2)

	if waiting := jt2.Waiting.snapshot(); len(waiting) != 1 || waiting[0].FileName != "second.txt" {
		t.Fatalf("second run queued copies = %#v, want second.txt", waiting)
	}
	a2 := GetPendingApproval(109)
	if a2 == nil || a2.TaskID != 1010 || a2.DeleteCount != 1 {
		t.Fatalf("second pending approval = %+v, want refreshed taskId=1010 deleteCount=1", a2)
	}

	// A third clean sync with no extra destination files auto-clears the stale approval.
	rec.listings["/dst/"] = `{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"second.txt","is_dir":false,"size":4}]}`
	jt3 := scanTestTask(t, server.URL, server.Client(), jobCfg)
	jt3.TaskID = 1011
	jt3.RequireFullSyncApproval = true
	jt3.sync()
	jt3.runFullSyncDeletes()
	if pendingDeleteApprovals.Has(109) {
		t.Fatalf("expected clean sync to auto-clear stale pending approval, still got %+v", GetPendingApproval(109))
	}

	// Re-stage a manual pending approval, then run a scheduled non-confirm sync
	// (RequireFullSyncApproval=false): it deletes directly and clears the pending approval.
	rec.listings["/dst/"] = `{"total":3,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"second.txt","is_dir":false,"size":4},{"name":"stale.txt","is_dir":false,"size":7}]}`
	jt4 := scanTestTask(t, server.URL, server.Client(), jobCfg)
	jt4.TaskID = 1012
	jt4.RequireFullSyncApproval = true
	jt4.sync()
	jt4.runFullSyncDeletes()
	if !pendingDeleteApprovals.Has(109) {
		t.Fatal("expected pending approval from manual run jt4")
	}

	jt5 := scanTestTask(t, server.URL, server.Client(), jobCfg)
	jt5.TaskID = 1013
	jt5.RequireFullSyncApproval = false
	jt5.sync()
	jt5.runFullSyncDeletes()
	if calls := rec.calls(); len(calls) != 1 || calls[0] != "/dst/stale.txt" {
		t.Fatalf("scheduled non-confirm remove calls = %#v, want [/dst/stale.txt]", calls)
	}
	if pendingDeleteApprovals.Has(109) {
		t.Fatalf("expected non-confirm sync to clear stale pending approval, still got %+v", GetPendingApproval(109))
	}
}

// Approved deferred deletions must honor trashEnable and move entries into the
// configured trash directory instead of hard-deleting them.
func TestPendingDeleteApprovalHonorsTrashEnable(t *testing.T) {
	pendingDeleteApprovals.ResetForTest()
	defer pendingDeleteApprovals.ResetForTest()

	deps := *jobDeps
	deps.ScanListRetryDelay = func(int) time.Duration { return 0 }
	restoreDeps := SetJobDepsForTest(&deps)
	defer restoreDeps()
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	var mu sync.Mutex
	var movedTo []string
	var removes []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/fs/list":
			var req alistListRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			switch req.Path {
			case "/src/":
				_, _ = w.Write([]byte(`{"code":200,"data":{"total":1,"content":[{"name":"keep.txt","is_dir":false,"size":1}]}}`))
			case "/dst/":
				_, _ = w.Write([]byte(`{"code":200,"data":{"total":2,"content":[{"name":"keep.txt","is_dir":false,"size":1},{"name":"old.txt","is_dir":false,"size":15}]}}`))
			default:
				t.Errorf("unexpected list path %q", req.Path)
			}
		case "/api/fs/mkdir":
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{}}`))
		case "/api/fs/move":
			var req struct {
				SrcDir string   `json:"src_dir"`
				DstDir string   `json:"dst_dir"`
				Names  []string `json:"names"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			for _, name := range req.Names {
				movedTo = append(movedTo, req.DstDir+name)
			}
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"tasks":[]}}`))
		case "/api/fs/remove":
			var req alistRemoveRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			mu.Lock()
			removes = append(removes, req.Names...)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{}}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()

	jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{
		"id":            int64(110),
		"method":        1,
		"srcPath":       "/src/",
		"dstPath":       "/dst/",
		"trashEnable":   1,
		"trashDir":      ".opensync_trash",
		"scanIntervalS": 0,
		"scanIntervalT": 0,
	})
	jt.TaskID = 1014
	jt.RequireFullSyncApproval = true
	jt.sync()
	jt.runFullSyncDeletes()

	mu.Lock()
	if len(movedTo) != 0 || len(removes) != 0 {
		mu.Unlock()
		t.Fatalf("unexpected mutations before approval: movedTo=%v removes=%v", movedTo, removes)
	}
	mu.Unlock()

	if err := ApproveFullSyncTask(1014); err != nil {
		t.Fatalf("ApproveFullSyncTask() error: %v", err)
	}
	waitForPendingApprovalCleared(t, 110)

	mu.Lock()
	defer mu.Unlock()
	if len(removes) != 0 {
		t.Fatalf("hard removes with trashEnable=1 = %v, want none", removes)
	}
	if len(movedTo) != 1 || !strings.HasPrefix(movedTo[0], "/dst/.opensync_trash/") || !strings.HasSuffix(movedTo[0], "/old.txt") {
		t.Fatalf("trash moves = %v, want single move under /dst/.opensync_trash/.../old.txt", movedTo)
	}
}
