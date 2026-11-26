package service

import (
	"context"
	"fmt"
	"opensync/internal/mapper"
	"opensync/internal/native"
	"os"
	"path/filepath"
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
	if approval.DeleteCount != 501 || approval.DeleteSize != 5000 {
		t.Fatalf("approval = %+v, want DeleteCount=501 DeleteSize=5000", approval)
	}
	if approval.PathCount != 1 {
		t.Fatalf("PathCount = %d, want 1", approval.PathCount)
	}
	if len(approval.Paths) != 1 || approval.Paths[0].Path != "/dst/dir" || approval.Paths[0].Action != "删除" || !approval.Paths[0].IsPath || approval.Paths[0].Count != 501 || approval.Paths[0].Size != 5000 {
		t.Fatalf("approval.Paths = %+v, want /dst/dir delete (501/5000)", approval.Paths)
	}
}

func TestVerifyPendingDeleteTargetsWithListerSafetyGuards(t *testing.T) {
	t.Run("skips file restored on source", func(t *testing.T) {
		listSrc := func(dir string) (FileListResult, bool, error) {
			return FileListResult{"keep.txt": {Size: 1}, "old.txt": {Size: 12}}, false, nil
		}
		listDst := func(dir string) (FileListResult, bool, error) {
			return FileListResult{"keep.txt": {Size: 1}, "old.txt": {Size: 12}}, false, nil
		}
		targets := []pendingDeleteTarget{{
			SrcRoot: "/src/", DstRoot: "/dst/", SrcDir: "/src/", DstDir: "/dst/",
			Name: "old.txt", Size: 12, Count: 1, SrcRootNonEmpty: true,
		}}
		toDelete, _, changed, err := verifyPendingDeleteTargetsWithLister(context.Background(), listSrc, listDst, map[string]interface{}{"method": 1}, targets)
		if err != nil || changed || len(toDelete) != 0 {
			t.Fatalf("got toDelete=%v changed=%v err=%v, want empty/false/nil", toDelete, changed, err)
		}
	})

	t.Run("aborts when source root turns empty", func(t *testing.T) {
		listSrc := func(dir string) (FileListResult, bool, error) {
			return FileListResult{}, false, nil
		}
		listDst := func(dir string) (FileListResult, bool, error) {
			return FileListResult{"old.txt": {Size: 12}}, false, nil
		}
		targets := []pendingDeleteTarget{{
			SrcRoot: "/src/", DstRoot: "/dst/", SrcDir: "/src/", DstDir: "/dst/",
			Name: "old.txt", Size: 12, Count: 1, SrcRootNonEmpty: true,
		}}
		_, _, _, err := verifyPendingDeleteTargetsWithLister(context.Background(), listSrc, listDst, map[string]interface{}{"method": 1}, targets)
		if err == nil {
			t.Fatal("expected mirror delete guard error when source root turns empty")
		}
	})

	t.Run("flags changed when directory gains new file before approval", func(t *testing.T) {
		listSrc := func(dir string) (FileListResult, bool, error) {
			if normalizeDirPath(dir) == "/src/" {
				return FileListResult{"keep.txt": {Size: 1}}, false, nil
			}
			return nil, true, nil
		}
		listDst := func(dir string) (FileListResult, bool, error) {
			switch normalizeDirPath(dir) {
			case "/dst/":
				return FileListResult{"keep.txt": {Size: 1}, "old_dir/": {}}, false, nil
			case "/dst/old_dir/":
				return FileListResult{"a.txt": {Size: 10}, "important.doc": {Size: 50}}, false, nil
			}
			return nil, true, nil
		}
		origCount, origSize, origDigest := summarizeSubtreeEntries([]subtreeDigestEntry{
			{relPath: "a.txt", size: 10},
		})
		targets := []pendingDeleteTarget{{
			SrcRoot: "/src/", DstRoot: "/dst/", SrcDir: "/src/", DstDir: "/dst/",
			Name: "old_dir/", IsDir: true, Count: origCount, Size: origSize, SubtreeDigest: origDigest, SrcRootNonEmpty: true,
		}}
		_, refreshed, changed, err := verifyPendingDeleteTargetsWithLister(context.Background(), listSrc, listDst, map[string]interface{}{"method": 1}, targets)
		if err != nil || !changed || len(refreshed) != 1 || refreshed[0].Count != 3 || refreshed[0].Size != 60 {
			t.Fatalf("got refreshed=%+v changed=%v err=%v, want changed=true Count=3 Size=60", refreshed, changed, err)
		}
	})
}

func TestNativePendingDeleteApprovalPersistsAcrossRestartAndExecutes(t *testing.T) {
	_, cleanup := setupFullFeatureTestDB(t)
	defer cleanup()
	pendingDeleteApprovals.ResetForTest()
	defer pendingDeleteApprovals.ResetForTest()

	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	dstDir := filepath.Join(tmp, "dst")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "keep.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstDir, "keep.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(dstDir, "old.txt")
	if err := os.WriteFile(oldPath, []byte("twelve-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldStat, err := os.Stat(oldPath)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", srcDir+":"+dstDir)
	t.Setenv("TRIM_PKGTMP", "")
	engineID, err := EnsureNativeEngine()
	if err != nil {
		t.Fatalf("EnsureNativeEngine: %v", err)
	}
	roots, err := GetChildPath(context.Background(), engineID, "/"+native.Local)
	if err != nil || len(roots) != 2 {
		t.Fatalf("GetChildPath local roots: %v %v", roots, err)
	}
	var srcVirtual, dstVirtual string
	for _, r := range roots {
		if r["name"] == srcDir {
			srcVirtual = r["path"]
		} else if r["name"] == dstDir {
			dstVirtual = r["path"]
		}
	}

	jobID, err := mapper.AddJob(map[string]interface{}{
		"enable": 1, "remark": "原生持久化审批", "srcPath": fmt.Sprintf("[%q]", srcVirtual), "dstPath": fmt.Sprintf("[%q]", dstVirtual),
		"alistId": engineID, "method": 1, "isCron": 2,
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
	jt := &JobTask{TaskID: taskID, Job: jobRow, RequireFullSyncApproval: true}
	jt.initRuntime()
	pendingDeleteApprovals.Stage(jt, []pendingDeleteTarget{{
		SrcRoot: normalizeDirPath(srcVirtual), DstRoot: normalizeDirPath(dstVirtual),
		SrcDir: normalizeDirPath(srcVirtual), DstDir: normalizeDirPath(dstVirtual),
		Name: "old.txt", Size: oldStat.Size(), Modified: oldStat.ModTime().Unix(),
		Count: 1, SrcRootNonEmpty: true,
	}})

	// Simulate restart: wipe in-memory map and reload from SQLite.
	pendingDeleteApprovals.ResetForTest()
	pendingDeleteApprovals.LoadFromDB()
	restored := GetPendingApproval(jobID)
	if restored == nil || restored.TaskID != taskID || restored.DeleteCount != 1 {
		t.Fatalf("restored approval = %+v, want taskId=%d deleteCount=1", restored, taskID)
	}

	if err := ApproveFullSyncTask(taskID); err != nil {
		t.Fatalf("ApproveFullSyncTask after restart error: %v", err)
	}
	waitForPendingApprovalCleared(t, jobID)

	if _, statErr := os.Stat(oldPath); !os.IsNotExist(statErr) {
		t.Fatalf("expected old.txt to be deleted after approval, statErr=%v", statErr)
	}
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
}

func TestPendingDeleteApprovalRejectAndExpiry(t *testing.T) {
	oldWait := fullSyncApprovalWait
	fullSyncApprovalWait = 40 * time.Millisecond
	defer func() { fullSyncApprovalWait = oldWait }()
	pendingDeleteApprovals.ResetForTest()
	defer pendingDeleteApprovals.ResetForTest()

	jt1 := &JobTask{TaskID: 2001, Job: map[string]interface{}{"id": int64(201), "method": 1}}
	pendingDeleteApprovals.Stage(jt1, []pendingDeleteTarget{{
		SrcRoot: "/src/", DstRoot: "/dst/", SrcDir: "/src/", DstDir: "/dst/", Name: "a.txt", Size: 10, Count: 1,
	}})
	if err := RejectFullSyncTask(2001); err != nil {
		t.Fatalf("RejectFullSyncTask() error: %v", err)
	}
	if pendingDeleteApprovals.Has(201) {
		t.Fatal("pending approval remained after reject")
	}

	jt2 := &JobTask{TaskID: 2002, Job: map[string]interface{}{"id": int64(202), "method": 1}}
	pendingDeleteApprovals.Stage(jt2, []pendingDeleteTarget{{
		SrcRoot: "/src/", DstRoot: "/dst/", SrcDir: "/src/", DstDir: "/dst/", Name: "b.txt", Size: 10, Count: 1,
	}})
	waitForPendingApprovalCleared(t, 202)
}
