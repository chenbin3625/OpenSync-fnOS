package service

import (
	"context"
	"fmt"
	"io"
	"log"
	"opensync/internal/mapper"
	"opensync/internal/native"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func isolateJobClientRegistryForTest(t *testing.T) {
	t.Helper()
	jobClientListMu.Lock()
	saved := jobClientList
	jobClientList = make(map[int64]*JobClient)
	jobClientListMu.Unlock()
	t.Cleanup(func() {
		jobClientListMu.Lock()
		created := jobClientList
		jobClientList = saved
		jobClientListMu.Unlock()
		for _, c := range created {
			if c.Scheduler != nil {
				c.Scheduler.Stop()
			}
		}
	})
}

// Mirror mode with conflictPolicy=2 (keep both) must never schedule its own
// *.conflict-YYYYMMDD-HHMMSS.* backups for deletion.
func TestMirrorModePreservesConflictBackupsUnderKeepBoth(t *testing.T) {
	srcSnap := emptyFullSyncSnapshot("/src/")
	srcSnap.dirs[""] = FileListResult{
		"report.pdf": {Size: 200, Modified: 2000},
	}

	dstSnap := emptyFullSyncSnapshot("/dst/")
	dstSnap.dirs[""] = FileListResult{
		"report.pdf":                          {Size: 200, Modified: 2000},
		"report.conflict-20260501-143005.pdf": {Size: 150, Modified: 1000},
		"removed/":                            {Size: 0, Modified: 1000},
		"extra.tmp":                           {Size: 10, Modified: 1000},
	}
	dstSnap.dirs["removed/"] = FileListResult{
		"note.conflict-20260501-143005.txt": {Size: 80, Modified: 1000},
		"obsolete.txt":                      {Size: 50, Modified: 1000},
	}

	job := map[string]interface{}{"method": 1, "conflictPolicy": 2}
	plan := newFullSyncPlan(srcSnap, dstSnap, nil)
	plan.build(job)

	for _, del := range plan.extraDeletes {
		if strings.Contains(del.name, ".conflict-") || del.object == "/dst/removed/" {
			t.Fatalf("mirror mode scheduled conflict backup or ancestor dir for deletion: %+v", del)
		}
	}
	if _, ok := plan.extraDeletes["/dst/extra.tmp"]; !ok {
		t.Fatalf("expected normal extra file /dst/extra.tmp in extraDeletes, got %+v", plan.extraDeletes)
	}
	if _, ok := plan.extraDeletes["/dst/removed/obsolete.txt"]; !ok {
		t.Fatalf("expected non-conflict file /dst/removed/obsolete.txt in extraDeletes, got %+v", plan.extraDeletes)
	}
}

// Legacy OpenList engines in backups are skipped with clear guidance warnings;
// valid native engine backups restore jobs safely and disable notifications
// (notifyMode=2) when specified channels were skipped.
func TestBackupRestoreLegacyEngineSkipAndNotifyScope(t *testing.T) {
	_, cleanup := setupFullFeatureTestDB(t)
	defer cleanup()
	isolateJobClientRegistryForTest(t)

	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "private")
	dstDir := filepath.Join(tmp, "backup")
	for _, d := range []string{srcDir, dstDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", srcDir+":"+dstDir)
	t.Setenv("TRIM_PKGTMP", "")

	targetNotifyID, err := mapper.AddNotify(map[string]interface{}{
		"enable": 1, "method": 1, "params": `{"sendKey":"unrelated-recipient"}`,
	})
	if err != nil {
		t.Fatalf("setup target notify: %v", err)
	}

	// Case 1: Legacy OpenList engine is skipped with WebDAV migration guidance; its job is skipped.
	backupLegacyEngine := SystemBackupPayload{
		Version: backupFormatVersion,
		Engines: []map[string]interface{}{
			{"id": int64(1), "remark": "LegacyOpenList", "url": "http://10.1.0.1:5244", "userName": "src", "token": "real-token"},
		},
		Jobs: []map[string]interface{}{
			{
				"remark": "旧版任务", "alistId": int64(1), "enable": 1, "method": 1, "isCron": 2,
				"srcPath": `["/src"]`, "dstPath": `["/dst"]`,
			},
		},
	}
	summary, err := ImportSystemBackup(backupLegacyEngine, "skip")
	if err != nil {
		t.Fatalf("ImportSystemBackup(legacyEngine) error: %v", err)
	}
	if summary["enginesSkipped"] != 1 || summary["jobsCreated"] != 0 || summary["jobsSkipped"] != 1 {
		t.Fatalf("expected legacy engine & job skipped, got summary=%+v", summary)
	}
	warnings, _ := summary["warnings"].([]string)
	hasWebDAVHint := false
	for _, w := range warnings {
		if strings.Contains(w, "WebDAV") {
			hasWebDAVHint = true
			break
		}
	}
	if !hasWebDAVHint {
		t.Fatalf("expected WebDAV migration guidance in warnings, got %+v", warnings)
	}

	// Case 2: Valid native engine imports, but specified notify channel #5 is redacted -> notifyMode=2.
	roots := native.AuthorizedRoots()
	var srcVirtual, dstVirtual string
	for _, r := range roots {
		if r.Path == srcDir {
			srcVirtual = "/" + native.Local + "/" + r.ID
		} else if r.Path == dstDir {
			dstVirtual = "/" + native.Local + "/" + r.ID
		}
	}
	backupSkippedNotify := SystemBackupPayload{
		Version: backupFormatVersion,
		Engines: []map[string]interface{}{
			{"id": int64(9), "remark": "内置存储", "url": native.EngineURL, "userName": "rclone", "token": "{}"},
		},
		Notifications: []map[string]interface{}{
			{"id": int64(5), "enable": 1, "method": 6, "params": `{"botToken":"****9999","chatId":"42"}`},
		},
		Jobs: []map[string]interface{}{
			{
				"remark": "私密同步", "alistId": int64(9), "enable": 1, "method": 0, "isCron": 2,
				"srcPath": fmt.Sprintf("[%q]", srcVirtual), "dstPath": fmt.Sprintf("[%q]", dstVirtual),
				"notifyMode": 1, "notifyIds": "[5]",
			},
		},
	}
	summary2, err := ImportSystemBackup(backupSkippedNotify, "skip")
	if err != nil {
		t.Fatalf("ImportSystemBackup(skippedNotify) error: %v", err)
	}
	if summary2["jobsCreated"] != 1 {
		t.Fatalf("expected job created, got summary=%+v", summary2)
	}
	jobs, _ := mapper.GetJobListAll()
	if len(jobs) != 1 {
		t.Fatalf("jobs count = %d, want 1", len(jobs))
	}
	if mode := int(jobs[0]["notifyMode"].(int64)); mode != 2 {
		t.Fatalf("imported notifyMode = %d, want 2 (disabled) instead of 0 (all channels)", mode)
	}
	allNotifies, _ := mapper.GetNotifyList(true)
	if len(allNotifies) == 0 || allNotifies[0]["id"].(int64) != targetNotifyID {
		t.Fatalf("expected existing enabled notify channel in DB, got %+v", allNotifies)
	}
	if delivered := filterNotifyListForJob(allNotifies, jobs[0]); len(delivered) != 0 {
		t.Fatalf("filterNotifyListForJob returned %+v, want empty so unrelated channel receives nothing", delivered)
	}
}

// Concurrent CreateJobClient and EditJobClient calls targeting the same
// destination directory must be serialized so only one succeeds.
func TestConcurrentJobSaveEnforcesDestinationConflict(t *testing.T) {
	_, cleanup := setupFullFeatureTestDB(t)
	defer cleanup()
	isolateJobClientRegistryForTest(t)

	tmp := t.TempDir()
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", tmp)
	t.Setenv("TRIM_PKGTMP", "")
	alistID, err := EnsureNativeEngine()
	if err != nil {
		t.Fatalf("EnsureNativeEngine: %v", err)
	}
	roots := native.AuthorizedRoots()
	if len(roots) != 1 {
		t.Fatalf("expected 1 local root, got %+v", roots)
	}
	rootV := "/" + native.Local + "/" + roots[0].ID

	const workers = 6
	for i := 0; i < workers; i++ {
		_ = os.MkdirAll(filepath.Join(tmp, fmt.Sprintf("src-%d", i)), 0o755)
	}
	for _, sub := range []string{"dst", "srcA", "srcB", "dstA", "dstB", "shared-dst"} {
		_ = os.MkdirAll(filepath.Join(tmp, sub), 0o755)
	}

	start := make(chan struct{})
	var created atomic.Int32
	var rejected atomic.Int32
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		idx := i
		go func() {
			defer wg.Done()
			<-start
			_, err := CreateJobClient(map[string]interface{}{
				"alistId": alistID,
				"remark":  fmt.Sprintf("并发镜像-%d", idx),
				"enable":  1,
				"method":  1,
				"isCron":  2,
				"srcPath": []string{fmt.Sprintf("%s/src-%d", rootV, idx)},
				"dstPath": []string{rootV + "/dst"},
			})
			if err == nil {
				created.Add(1)
			} else {
				rejected.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if created.Load() != 1 || rejected.Load() != workers-1 {
		t.Fatalf("concurrent create: created=%d rejected=%d, want 1/%d", created.Load(), rejected.Load(), workers-1)
	}

	idA, err := CreateJobClient(map[string]interface{}{
		"alistId": alistID, "remark": "任务A", "enable": 1, "method": 1, "isCron": 2,
		"srcPath": []string{rootV + "/srcA"}, "dstPath": []string{rootV + "/dstA"},
	})
	if err != nil {
		t.Fatalf("create job A: %v", err)
	}
	idB, err := CreateJobClient(map[string]interface{}{
		"alistId": alistID, "remark": "任务B", "enable": 1, "method": 1, "isCron": 2,
		"srcPath": []string{rootV + "/srcB"}, "dstPath": []string{rootV + "/dstB"},
	})
	if err != nil {
		t.Fatalf("create job B: %v", err)
	}

	startEdit := make(chan struct{})
	var editOK, editFail atomic.Int32
	var editWG sync.WaitGroup
	editWG.Add(2)
	for _, pair := range []struct {
		id  int64
		src string
	}{{idA, rootV + "/srcA"}, {idB, rootV + "/srcB"}} {
		pair := pair
		go func() {
			defer editWG.Done()
			<-startEdit
			err := EditJobClient(map[string]interface{}{
				"id": pair.id, "alistId": alistID, "remark": "编辑后", "enable": 1, "method": 1, "isCron": 2,
				"srcPath": []string{pair.src}, "dstPath": []string{rootV + "/shared-dst"},
			})
			if err == nil {
				editOK.Add(1)
			} else {
				editFail.Add(1)
			}
		}()
	}
	close(startEdit)
	editWG.Wait()

	if editOK.Load() != 1 || editFail.Load() != 1 {
		t.Fatalf("concurrent edit: ok=%d fail=%d, want 1/1", editOK.Load(), editFail.Load())
	}
}

func TestLargeDatasetExportAndDashboardOverviewExceeding10000Rows(t *testing.T) {
	testDB, cleanup := setupFullFeatureTestDB(t)
	defer cleanup()
	isolateJobClientRegistryForTest(t)

	alistID, err := EnsureNativeEngine()
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := mapper.AddJob(map[string]interface{}{
		"enable": 1, "remark": "大数据量测试", "srcPath": `["/src"]`, "dstPath": `["/dst"]`,
		"alistId": alistID, "method": 0, "isCron": 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	const count = 10005
	nowUnix := time.Now().Unix()
	if _, err := testDB.Exec(
		`WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < ?)
		 INSERT INTO job_task(jobId, status, runTime, taskNum, createTime)
		 SELECT ?, 2, ?, '{"allNum":1,"successNum":1,"failNum":0,"sumSize":10}', ? FROM c`,
		count, jobID, nowUnix, nowUnix,
	); err != nil {
		t.Fatalf("bulk insert job_task: %v", err)
	}
	var firstTaskID int64
	if err := testDB.QueryRow(`SELECT MIN(id) FROM job_task WHERE jobId = ?`, jobID).Scan(&firstTaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.Exec(
		`WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < ?)
		 INSERT INTO job_task_item(taskId, srcPath, dstPath, isPath, fileName, fileSize, type, status, createTime)
		 SELECT ?, '/src/', '/dst/', 0, 'f-' || x || '.bin', 10, 0, 2, ? FROM c`,
		count, firstTaskID, nowUnix,
	); err != nil {
		t.Fatalf("bulk insert job_task_item: %v", err)
	}

	_, _, txtData, err := ExportTaskItems(map[string]interface{}{"taskId": firstTaskID}, "txt")
	if err != nil {
		t.Fatalf("ExportTaskItems(txt, %d rows) error: %v", count, err)
	}
	lines := strings.Split(strings.TrimSpace(string(txtData)), "\n")
	if len(lines) != count {
		t.Fatalf("exported txt lines = %d, want %d", len(lines), count)
	}

	overview, err := GetDashboardOverview()
	if err != nil {
		t.Fatalf("GetDashboardOverview error: %v", err)
	}
	recent, _ := overview["recent7d"].(map[string]interface{})
	if got := recent["totalRuns"]; got != int64(count) {
		t.Fatalf("recent7d.totalRuns = %v, want %d", got, count)
	}
	if got := recent["successRuns"]; got != int64(count) {
		t.Fatalf("recent7d.successRuns = %v, want %d", got, count)
	}
	if got := recent["totalBytes"]; got != int64(count*10) {
		t.Fatalf("recent7d.totalBytes = %v, want %d", got, count*10)
	}
}

func TestCleanupExpiredTasksProtectsActiveRetrySource(t *testing.T) {
	testDB, cleanup := setupFullFeatureTestDB(t)
	defer cleanup()
	isolateJobClientRegistryForTest(t)

	expiredUnix := time.Now().AddDate(0, 0, -60).Unix()
	tProtected, err := mapper.AddJobTask(1, expiredUnix)
	if err != nil {
		t.Fatal(err)
	}
	_ = mapper.UpdateJobTaskStatusAndNum(tProtected, taskStatusFailed.Int(), nil, `{"allNum":1,"failNum":1}`)
	_, _ = testDB.Exec(`UPDATE job_task SET runTime = ?, createTime = ? WHERE id = ?`, expiredUnix, expiredUnix, tProtected)
	_ = mapper.AddJobTaskItemMany([]map[string]interface{}{
		{"taskId": tProtected, "srcPath": "/src/", "dstPath": "/dst/", "isPath": 0, "fileName": "a.bin", "fileSize": int64(1), "type": 0, "status": 3},
	})
	tExpired, err := mapper.AddJobTask(1, expiredUnix)
	if err != nil {
		t.Fatal(err)
	}
	_ = mapper.UpdateJobTaskStatusAndNum(tExpired, taskStatusSuccess.Int(), nil, `{"allNum":1,"successNum":1}`)
	_, _ = testDB.Exec(`UPDATE job_task SET runTime = ?, createTime = ? WHERE id = ?`, expiredUnix, expiredUnix, tExpired)

	jc := &JobClient{Job: map[string]interface{}{"id": int64(1)}, retrySourceID: tProtected}
	jobClientListMu.Lock()
	jobClientList[1] = jc
	jobClientListMu.Unlock()

	cleanupExpiredTasksContext(context.Background(), log.New(io.Discard, "", 0), 7, time.Now())

	var keptTask, keptItems, deletedTask int
	_ = testDB.QueryRow(`SELECT COUNT(*) FROM job_task WHERE id = ?`, tProtected).Scan(&keptTask)
	_ = testDB.QueryRow(`SELECT COUNT(*) FROM job_task_item WHERE taskId = ?`, tProtected).Scan(&keptItems)
	_ = testDB.QueryRow(`SELECT COUNT(*) FROM job_task WHERE id = ?`, tExpired).Scan(&deletedTask)
	if keptTask != 1 || keptItems != 1 {
		t.Fatalf("protected retry source task=%d items=%d, want 1/1", keptTask, keptItems)
	}
	if deletedTask != 0 {
		t.Fatalf("unprotected expired task=%d, want 0", deletedTask)
	}
}
