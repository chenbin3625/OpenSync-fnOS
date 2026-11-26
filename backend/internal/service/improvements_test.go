package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opensync/internal/config"
	"opensync/internal/mapper"
	"opensync/internal/native"
	"opensync/pkg/util"
)

func testVirtualLocalRoot(t *testing.T, absDir string) string {
	t.Helper()
	clean := filepath.Clean(absDir)
	for _, r := range native.AuthorizedRoots() {
		if filepath.Clean(r.Path) == clean {
			return "/" + native.Local + "/" + r.ID
		}
	}
	t.Fatalf("authorized root not found for %s (roots=%v)", absDir, native.AuthorizedRoots())
	return ""
}

func TestEngineQueuePerStorageConcurrencyLimits(t *testing.T) {
	q := newEngineQueueManager()
	limits := map[string]int{
		"oss":  1,
		"oned": 2,
	}

	rel1, err := q.AcquireWithStorageLimits(context.Background(), 1, 101, 10, []string{"oss"}, limits)
	if err != nil {
		t.Fatalf("job 101 acquire: %v", err)
	}
	defer rel1()

	// Unrelated storage "oned" runs concurrently without waiting.
	relOther, err := q.AcquireWithStorageLimits(context.Background(), 1, 102, 10, []string{"oned"}, limits)
	if err != nil {
		t.Fatalf("job 102 acquire: %v", err)
	}
	defer relOther()

	// Second job targeting "oss" (limit 1) must block until job 101 releases.
	ctxWait, cancelWait := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancelWait()
	if _, err := q.AcquireWithStorageLimits(ctxWait, 1, 103, 10, []string{"oss"}, limits); err == nil {
		t.Fatalf("expected job 103 on capped storage oss to block until timeout")
	}

	// Releasing job 101 lets job 104 acquire "oss" immediately.
	rel1()
	rel4, err := q.AcquireWithStorageLimits(context.Background(), 1, 104, 10, []string{"oss"}, limits)
	if err != nil {
		t.Fatalf("job 104 acquire after release: %v", err)
	}
	rel4()
}

func TestFullSyncPlanTrackRenamesPass2(t *testing.T) {
	md5Val := "0123456789abcdef0123456789abcdef"
	makePlan := func(trackRenames bool) *fullSyncPlan {
		return &fullSyncPlan{
			trackRenames: trackRenames,
			newFiles: map[string]*fullSyncFile{
				"/dst/renamed-photo.jpg": {
					srcDir:   "/src",
					dstDir:   "/dst",
					name:     "renamed-photo.jpg",
					metadata: FileMetadata{Size: 4096, MD5: md5Val},
					target:   "/dst/renamed-photo.jpg",
				},
				"/dst/zero-new.txt": {
					srcDir:   "/src",
					dstDir:   "/dst",
					name:     "zero-new.txt",
					metadata: FileMetadata{Size: 0, MD5: "d41d8cd98f00b204e9800998ecf8427e"},
					target:   "/dst/zero-new.txt",
				},
			},
			extraFiles: map[string]*fullSyncExtraFile{
				"/dst/old-photo.jpg": {
					dir:        "/dst",
					name:       "old-photo.jpg",
					metadata:   FileMetadata{Size: 4096, MD5: md5Val},
					object:     "/dst/old-photo.jpg",
					deleteRoot: "/dst/old-photo.jpg",
				},
				"/dst/zero-old.txt": {
					dir:        "/dst",
					name:       "zero-old.txt",
					metadata:   FileMetadata{Size: 0, MD5: "d41d8cd98f00b204e9800998ecf8427e"},
					object:     "/dst/zero-old.txt",
					deleteRoot: "/dst/zero-old.txt",
				},
			},
		}
	}

	if rels := makePlan(false).relocations(); len(rels) != 0 {
		t.Fatalf("expected 0 relocations when trackRenames is disabled, got %d", len(rels))
	}
	rels := makePlan(true).relocations()
	if len(rels) != 1 {
		t.Fatalf("expected exactly 1 non-zero rename relocation, got %d", len(rels))
	}
	if rels[0].source.object != "/dst/old-photo.jpg" || rels[0].target.target != "/dst/renamed-photo.jpg" {
		t.Fatalf("unexpected relocation pair: %s -> %s", rels[0].source.object, rels[0].target.target)
	}
}

func TestSelectiveExcludePendingDeleteApproval(t *testing.T) {
	defer setupNativeTestDB(t)()
	pendingDeleteApprovals.ResetForTest()
	defer pendingDeleteApprovals.ResetForTest()

	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	dstDir := filepath.Join(tmp, "dst")
	for _, p := range []string{srcDir, dstDir} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", srcDir+":"+dstDir)
	t.Setenv("TRIM_PKGTMP", "")

	engineID, err := EnsureNativeEngine()
	if err != nil {
		t.Fatalf("EnsureNativeEngine: %v", err)
	}
	srcV := testVirtualLocalRoot(t, srcDir)
	dstV := testVirtualLocalRoot(t, dstDir)
	jobID, err := mapper.AddJob(map[string]interface{}{
		"alistId": engineID,
		"srcPath": srcV,
		"dstPath": dstV,
		"method":  1,
		"isCron":  2,
		"enable":  1,
		"exclude": "*.tmp",
		"remark":  "selective-approval-test",
	})
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	jobCfg, err := mapper.GetJobByID(jobID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}

	jt := &JobTask{
		TaskID: 9001,
		Job:    jobCfg,
	}
	targets := []pendingDeleteTarget{
		{SrcRoot: srcV + "/", DstRoot: dstV + "/", SrcDir: srcV + "/", DstDir: dstV + "/", Name: "keep-me.txt", Size: 100, Modified: 1000, Count: 1},
		{SrcRoot: srcV + "/", DstRoot: dstV + "/", SrcDir: srcV + "/", DstDir: dstV + "/", Name: "remove-me.txt", Size: 200, Modified: 1000, Count: 1},
	}
	pendingDeleteApprovals.Stage(jt, targets)

	// Exclude "keep-me.txt": should append rule to job exclude and leave "remove-me.txt" pending.
	excludedCount, err := pendingDeleteApprovals.ExcludeByTaskIDPaths(9001, []string{dstV + "/keep-me.txt"})
	if err != nil {
		t.Fatalf("ExcludeByTaskIDPaths: %v", err)
	}
	if excludedCount != 1 {
		t.Fatalf("expected 1 excluded path, got %d", excludedCount)
	}
	updatedJob, err := mapper.GetJobByID(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(util.StringValue(updatedJob["exclude"]), "keep-me.txt") {
		t.Fatalf("expected job exclude to contain keep-me.txt, got %q", util.StringValue(updatedJob["exclude"]))
	}
	rem := pendingDeleteApprovals.GetByJobID(jobID)
	if rem == nil || len(rem.Paths) != 1 || rem.Paths[0].Path != dstV+"/remove-me.txt" {
		t.Fatalf("expected only remove-me.txt remaining in approval, got %#v", rem)
	}

	// Approving empty selection must fail cleanly.
	if err := pendingDeleteApprovals.ApproveByTaskIDPaths(9001, nil); err == nil {
		t.Fatalf("expected error on empty ApproveByTaskIDPaths")
	}
}

func TestCheckDestinationFreeSpaceGuard(t *testing.T) {
	defer setupNativeTestDB(t)()
	tmp := t.TempDir()
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", tmp)
	t.Setenv("TRIM_PKGTMP", "")

	dstV := testVirtualLocalRoot(t, tmp)
	job := map[string]interface{}{"dstPath": dstV}
	cfg := native.Config{}

	orig := config.GetSystemSettings()
	defer func() { _ = config.UpdateSystemSettings(orig) }()

	// MinFreeSpaceMB == 0 disables guard.
	s0 := orig
	s0.MinFreeSpaceMB = 0
	if err := config.UpdateSystemSettings(s0); err != nil {
		t.Fatal(err)
	}
	if err := checkDestinationFreeSpace(context.Background(), cfg, job); err != nil {
		t.Fatalf("expected MinFreeSpaceMB=0 to pass, got: %v", err)
	}

	// Set threshold to max allowed (10 TB = 10485760 MB) -> must trigger guard on test disk.
	sHigh := orig
	sHigh.MinFreeSpaceMB = 10485760
	if err := config.UpdateSystemSettings(sHigh); err != nil {
		t.Fatal(err)
	}
	if err := checkDestinationFreeSpace(context.Background(), cfg, job); err == nil || !strings.Contains(err.Error(), "剩余空间不足") {
		t.Fatalf("expected free-space guard error, got: %v", err)
	}
}

func TestExportJobCheckReportAndCSVFormulaSanitization(t *testing.T) {
	jobID := int64(77701)
	jobCheckMu.Lock()
	jobCheckSessions[jobID] = &jobCheckSession{
		result: JobCheckResult{
			JobID:             jobID,
			Status:            "completed",
			Mode:              "check",
			MatchedCount:      10,
			DifferCount:       1,
			MissingOnDstCount: 1,
			Differ:            []string{"=cmd|' /C calc'!A0"},
			MissingOnDst:      []string{"photos/2026/a.jpg"},
		},
	}
	jobCheckMu.Unlock()
	defer func() {
		jobCheckMu.Lock()
		delete(jobCheckSessions, jobID)
		jobCheckMu.Unlock()
	}()

	if IsJobCheckRunning(jobID) {
		t.Fatalf("completed check should not report running")
	}
	csvName, csvCT, csvData, err := ExportJobCheckReport(jobID, "csv")
	if err != nil {
		t.Fatalf("ExportJobCheckReport(csv): %v", err)
	}
	if !strings.HasSuffix(csvName, ".csv") || !strings.Contains(csvCT, "text/csv") {
		t.Fatalf("unexpected csv meta: %s %s", csvName, csvCT)
	}
	csvStr := string(csvData)
	// Formula cell must be prefixed with leading quote to neutralize Excel injection.
	if !strings.Contains(csvStr, "'=cmd") {
		t.Fatalf("expected formula neutralization in CSV output, got:\n%s", csvStr)
	}
	if !strings.Contains(csvStr, "photos/2026/a.jpg") {
		t.Fatalf("expected missing file in CSV output")
	}

	txtName, _, txtData, err := ExportJobCheckReport(jobID, "txt")
	if err != nil {
		t.Fatalf("ExportJobCheckReport(txt): %v", err)
	}
	if !strings.HasSuffix(txtName, ".txt") || !strings.Contains(string(txtData), "一致: 10") {
		t.Fatalf("unexpected txt report: %s\n%s", txtName, string(txtData))
	}
}

func TestFSWatcherRootConfinementAndLifecycle(t *testing.T) {
	jobFSWatchers.ResetForTest()
	defer jobFSWatchers.ResetForTest()

	tmp := t.TempDir()
	allowed := filepath.Join(tmp, "allowed")
	forbidden := filepath.Join(tmp, "forbidden")
	for _, d := range []string{allowed, forbidden} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", allowed)
	t.Setenv("TRIM_PKGTMP", "")

	allowedV := testVirtualLocalRoot(t, allowed)
	roots := resolveLocalWatchRoots([]string{
		allowedV,
		"/" + native.Local + "/nonexistent-root-id",
		"/oss/bucket",
	})
	if len(roots) != 1 || roots[0].absDir != allowed {
		t.Fatalf("expected only authorized local root %q, got %#v", allowed, roots)
	}

	jc := &JobClient{
		JobID: 88801,
		Job: map[string]interface{}{
			"id":         int64(88801),
			"enable":     1,
			"method":     0,
			"srcPath":    allowedV,
			"dstPath":    "/oss/backup",
			"extraFlags": `{"watchEnable":true,"watchDebounceSec":2}`,
		},
	}
	jobFSWatchers.SyncJob(jc)
	if !jobFSWatchers.IsWatching(88801) {
		t.Fatalf("expected job 88801 to be actively watched")
	}
	jobFSWatchers.Stop(88801)
	if jobFSWatchers.IsWatching(88801) {
		t.Fatalf("expected job 88801 watcher to stop cleanly")
	}
}

func TestDigestSummaryErrorGroupsAndDashboard30dTrend(t *testing.T) {
	defer setupNativeTestDB(t)()

	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	dstDir := filepath.Join(tmp, "dst")
	for _, p := range []string{srcDir, dstDir} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", srcDir+":"+dstDir)
	t.Setenv("TRIM_PKGTMP", "")

	engineID, err := EnsureNativeEngine()
	if err != nil {
		t.Fatalf("EnsureNativeEngine: %v", err)
	}
	jobID, err := mapper.AddJob(map[string]interface{}{
		"alistId": engineID,
		"srcPath": testVirtualLocalRoot(t, srcDir),
		"dstPath": testVirtualLocalRoot(t, dstDir),
		"method":  0,
		"isCron":  2,
		"enable":  1,
		"remark":  "digest-test-job",
	})
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	now := time.Now().Unix()
	taskID, err := mapper.AddJobTask(jobID, now)
	if err != nil {
		t.Fatalf("AddJobTask: %v", err)
	}
	if err := mapper.UpdateJobTaskStatusAndNum(taskID, 7, nil, `{"waitNum":0,"runningNum":0,"successNum":5,"failNum":2,"otherNum":0,"sumSize":2048}`); err != nil {
		t.Fatalf("UpdateJobTaskStatusAndNum: %v", err)
	}
	errMsgA := "403 AccessDenied"
	errMsgB := "i/o timeout"
	if err := mapper.AddJobTaskItemMany([]map[string]interface{}{
		{"taskId": taskID, "srcPath": "/a", "dstPath": "/b", "isPath": 0, "fileName": "1.bin", "fileSize": int64(100), "type": 0, "alistTaskId": "", "status": 3, "errMsg": errMsgA, "createTime": now},
		{"taskId": taskID, "srcPath": "/a", "dstPath": "/b", "isPath": 0, "fileName": "2.bin", "fileSize": int64(100), "type": 0, "alistTaskId": "", "status": 3, "errMsg": errMsgA, "createTime": now},
		{"taskId": taskID, "srcPath": "/a", "dstPath": "/b", "isPath": 0, "fileName": "3.bin", "fileSize": int64(100), "type": 0, "alistTaskId": "", "status": 3, "errMsg": errMsgB, "createTime": now},
	}); err != nil {
		t.Fatalf("AddJobTaskItemMany: %v", err)
	}

	groups, err := mapper.GetTaskErrorGroups(taskID, 20)
	if err != nil {
		t.Fatalf("GetTaskErrorGroups: %v", err)
	}
	if len(groups) != 2 || util.StringValue(groups[0]["reason"]) != errMsgA || util.ToInt64(groups[0]["count"]) != 2 {
		t.Fatalf("unexpected error groups: %#v", groups)
	}

	title, content, vars, err := BuildDigestSummary("daily")
	if err != nil {
		t.Fatalf("BuildDigestSummary(daily): %v", err)
	}
	if !strings.Contains(title, "每日备份摘要") || vars["totalRuns"] != "1" || !strings.Contains(content, "同步文件：5") {
		t.Fatalf("unexpected daily digest: title=%q vars=%#v content=%q", title, vars, content)
	}
	wTitle, _, _, err := BuildDigestSummary("weekly")
	if err != nil || !strings.Contains(wTitle, "每周备份摘要") {
		t.Fatalf("unexpected weekly digest: %q (%v)", wTitle, err)
	}

	overview, err := GetDashboardOverview()
	if err != nil {
		t.Fatalf("GetDashboardOverview: %v", err)
	}
	rawTrend, err := json.Marshal(overview["dailyTrend"])
	if err != nil {
		t.Fatal(err)
	}
	var trend []struct {
		Date string `json:"date"`
		Runs int64  `json:"runs"`
	}
	if err := json.Unmarshal(rawTrend, &trend); err != nil {
		t.Fatal(err)
	}
	if len(trend) != 30 {
		t.Fatalf("expected 30 dailyTrend buckets, got %d", len(trend))
	}
	if trend[29].Runs < 1 {
		t.Fatalf("expected today's bucket to record at least 1 run, got %#v", trend[29])
	}
}

func TestNativeTrashBrowseRestoreAndAsyncTransferQueue(t *testing.T) {
	defer setupNativeTestDB(t)()
	ClearCompletedNativeTransfers()

	base := t.TempDir()
	dstRoot := filepath.Join(base, "backup")
	trashSub := filepath.Join(dstRoot, defaultTrashDir, "2026-11-26", "v1", "docs")
	if err := os.MkdirAll(trashSub, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(trashSub, "notes.txt"), []byte("restorable-text"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", dstRoot)
	t.Setenv("TRIM_PKGTMP", "")

	if _, err := EnsureNativeEngine(); err != nil {
		t.Fatalf("EnsureNativeEngine: %v", err)
	}
	dstV := testVirtualLocalRoot(t, dstRoot)

	// 1. Browse .opensync-trash visually.
	items, err := BrowseNativeTrash(context.Background(), NativeTrashBrowseInput{DstRoot: dstV})
	if err != nil {
		t.Fatalf("BrowseNativeTrash: %v", err)
	}
	if len(items) != 1 || items[0].Name != "notes.txt" || items[0].BucketDate != "2026-11-26" || items[0].Version != "v1" {
		t.Fatalf("unexpected trash items: %#v", items)
	}
	if items[0].OriginalPath != dstV+"/docs/notes.txt" {
		t.Fatalf("unexpected originalPath: %s", items[0].OriginalPath)
	}

	// 2. Restore from trash back to original path.
	if err := RestoreNativeTrashItem(context.Background(), NativeTrashRestoreInput{
		TrashPath:    items[0].TrashPath,
		OriginalPath: items[0].OriginalPath,
	}); err != nil {
		t.Fatalf("RestoreNativeTrashItem: %v", err)
	}
	restoredData, err := os.ReadFile(filepath.Join(dstRoot, "docs", "notes.txt"))
	if err != nil || string(restoredData) != "restorable-text" {
		t.Fatalf("expected restored file content, got %q (%v)", string(restoredData), err)
	}

	// 3. Direct upload + download via service layer.
	if _, err := UploadNativeStorageFile(context.Background(), dstV, "hello.txt", strings.NewReader("world")); err != nil {
		t.Fatalf("UploadNativeStorageFile: %v", err)
	}
	rc, name, size, err := OpenNativeStorageDownload(context.Background(), dstV+"/hello.txt")
	if err != nil {
		t.Fatalf("OpenNativeStorageDownload: %v", err)
	}
	_ = rc.Close()
	if name != "hello.txt" || size != 5 {
		t.Fatalf("unexpected download result: name=%q size=%d", name, size)
	}

	// 4. Async batch delete via transfer queue.
	task, err := StartBatchNativeStorageAction(NativeBatchInput{
		Action: "delete",
		Items:  []NativeBatchItem{{Path: dstV + "/hello.txt", IsDir: false}},
	})
	if err != nil {
		t.Fatalf("StartBatchNativeStorageAction: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		list := ListNativeTransfers()
		done := false
		for _, item := range list {
			if item.ID == task.ID && item.Status == "completed" {
				done = true
				break
			}
		}
		if done {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dstRoot, "hello.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected batch delete task to remove hello.txt")
	}
	ClearCompletedNativeTransfers()
	if len(ListNativeTransfers()) != 0 {
		t.Fatalf("expected completed transfers to be cleared")
	}
}

func TestSystemSettingsNewFieldsAndLocale(t *testing.T) {
	defer setupNativeTestDB(t)()

	orig := config.GetSystemSettings()
	defer func() {
		_ = config.UpdateSystemSettings(orig)
		_ = config.UpdateLocale("")
	}()

	updated := orig
	updated.LocalMaxConcurrentJobs = 3
	updated.MinFreeSpaceMB = 512
	updated.AutoResumeInterrupted = 1
	updated.DigestEnable = 1
	updated.DigestSchedule = "weekly"
	updated.DigestHour = 9
	updated.DigestWeekday = 1
	if err := config.UpdateSystemSettings(updated); err != nil {
		t.Fatalf("UpdateSystemSettings: %v", err)
	}
	got := config.GetSystemSettings()
	if got.LocalMaxConcurrentJobs != 3 || got.MinFreeSpaceMB != 512 || got.AutoResumeInterrupted != 1 || got.DigestEnable != 1 || got.DigestSchedule != "weekly" || got.DigestHour != 9 || got.DigestWeekday != 1 {
		t.Fatalf("persisted system settings mismatch: %#v", got)
	}
	if err := config.UpdateLocale("en-US"); err != nil {
		t.Fatalf("UpdateLocale: %v", err)
	}
	if config.GetEffectiveLocale() != "en" {
		t.Fatalf("expected normalized effective locale en, got %q", config.GetEffectiveLocale())
	}
}
