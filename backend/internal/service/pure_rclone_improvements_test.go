package service

import (
	"context"
	"encoding/json"
	"opensync/internal/config"
	"opensync/internal/mapper"
	"opensync/internal/native"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func setupPureRcloneServiceTestDB(t *testing.T) func() {
	t.Helper()
	oldCfg := config.GetConfig()
	dbPath := filepath.Join(t.TempDir(), "pure-rclone.db")
	config.SetConfigForTest(&config.Config{
		DB: config.DBConfig{DBName: dbPath},
		Server: config.ServerConfig{
			Timeout:         0,
			CopyConcurrency: 2,
			ScanConcurrency: 4,
			PasswdStr:       "test-pure-rclone-secret",
		},
	})
	_ = mapper.CloseDB()
	mapper.InitSQL()
	resetJobClientsForTest()
	return func() {
		resetJobClientsForTest()
		_ = mapper.CloseDB()
		config.SetConfigForTest(oldCfg)
	}
}

func TestPureRcloneEngineLifecycleAndLegacyFiltering(t *testing.T) {
	defer setupPureRcloneServiceTestDB(t)()

	// EnsureNativeEngine creates the built-in rclone engine idempotently.
	id1, err := EnsureNativeEngine()
	if err != nil || id1 <= 0 {
		t.Fatalf("EnsureNativeEngine() #1 = (%d, %v)", id1, err)
	}
	id2, err := EnsureNativeEngine()
	if err != nil || id2 != id1 {
		t.Fatalf("EnsureNativeEngine() #2 = (%d, %v), want %d", id2, err, id1)
	}

	// Insert a legacy OpenList row directly in SQLite to confirm GetClientList filters it out.
	if _, err := mapper.AddAlist("legacy-openlist", "http://127.0.0.1:5244", "admin", "secret-token"); err != nil {
		t.Fatalf("AddAlist legacy row: %v", err)
	}

	list, err := GetClientList()
	if err != nil {
		t.Fatalf("GetClientList() error = %v", err)
	}
	if len(list) != 1 || list[0]["url"] != native.EngineURL || list[0]["kind"] != "rclone" {
		t.Fatalf("GetClientList() = %#v, want only built-in rclone engine", list)
	}

	// Built-in engine cannot be deleted.
	if err := RemoveClient(id1); err == nil {
		t.Fatal("RemoveClient() unexpectedly deleted built-in rclone engine")
	}

	// Updating built-in engine remark and maxConcurrentJobs succeeds.
	remark, maxConcurrent := "我的内置存储", 3
	if _, err := SaveNativeEngine(context.Background(), NativeEngineInput{
		Remark: &remark, MaxConcurrentJobs: &maxConcurrent,
	}); err != nil {
		t.Fatalf("SaveNativeEngine() error = %v", err)
	}
	row, err := mapper.GetAlistByID(id1)
	if err != nil {
		t.Fatalf("GetAlistByID() error = %v", err)
	}
	if row["remark"] != "我的内置存储" {
		t.Fatalf("updated remark = %v, want 我的内置存储", row["remark"])
	}

	// Backup import skips legacy non-native OpenList rows with clear WebDAV migration guidance.
	summary, err := ImportSystemBackup(SystemBackupPayload{
		Version: 1,
		Engines: []map[string]interface{}{
			{
				"id":       99,
				"remark":   "旧版OpenList",
				"url":      "http://192.168.1.10:5244",
				"userName": "admin",
				"token":    "old-token",
			},
		},
	}, "skip")
	if err != nil {
		t.Fatalf("ImportSystemBackup() error = %v", err)
	}
	if summary["enginesSkipped"] != 1 || summary["enginesCreated"] != 0 {
		t.Fatalf("unexpected import summary: %+v", summary)
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
}

func TestEngineQueuePerStorageConcurrencyAndNonBlocking(t *testing.T) {
	q := newEngineQueueManager()
	ctx := context.Background()

	// Job 1 holds storage "oss" with storageLimit=1, engineLimit=4.
	rel1, err := q.AcquireWithStorages(ctx, 1, 101, 4, []string{"本地", "oss"}, 1)
	if err != nil {
		t.Fatalf("AcquireWithStorages job 101: %v", err)
	}
	if act, qd := q.StorageStats("oss"); act != 1 || qd != 0 {
		t.Fatalf("oss stats = (%d,%d), want (1,0)", act, qd)
	}

	// Job 2 also targets "oss": must queue because oss storageLimit=1.
	job2Acquired := make(chan func(), 1)
	go func() {
		rel, acqErr := q.AcquireWithStorages(ctx, 1, 102, 4, []string{"oss"}, 1)
		if acqErr == nil {
			job2Acquired <- rel
		}
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, qd := q.StorageStats("oss")
		if qd == 1 && q.IsJobQueued(102) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, qd := q.StorageStats("oss"); qd != 1 {
		t.Fatalf("expected job 102 queued on oss, queued=%d", qd)
	}

	// Job 3 targets an independent storage "b2": must NOT be head-of-line blocked by job 102!
	rel3, err := q.AcquireWithStorages(ctx, 1, 103, 4, []string{"b2"}, 1)
	if err != nil {
		t.Fatalf("AcquireWithStorages independent storage b2 failed: %v", err)
	}
	if act, qd := q.StorageStats("b2"); act != 1 || qd != 0 {
		t.Fatalf("b2 stats = (%d,%d), want (1,0)", act, qd)
	}

	// Job 4 queued on "oss" then cancelled cleans up storageQueued cleanly.
	cancelCtx, cancel := context.WithCancel(ctx)
	job4Err := make(chan error, 1)
	go func() {
		_, e := q.AcquireWithStorages(cancelCtx, 1, 104, 4, []string{"oss"}, 1)
		job4Err <- e
	}()
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if q.IsJobQueued(104) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-job4Err; err == nil {
		t.Fatal("expected cancelled waiter 104 to return error")
	}
	if q.IsJobQueued(104) {
		t.Fatal("cancelled job 104 still marked queued")
	}

	// Releasing job 101 wakes job 102.
	rel1()
	select {
	case rel2 := <-job2Acquired:
		if act, qd := q.StorageStats("oss"); act != 1 || qd != 0 {
			t.Fatalf("oss stats after promotion = (%d,%d), want (1,0)", act, qd)
		}
		rel2()
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for job 102 to acquire oss after job 101 released")
	}
	rel3()
	if act, qd := q.EngineStats(1); act != 0 || qd != 0 {
		t.Fatalf("final engine stats = (%d,%d), want (0,0)", act, qd)
	}
}

func TestNativeProgressMultiPairExactPaths(t *testing.T) {
	jt := &JobTask{
		TaskID:     55,
		CreateTime: float64(time.Now().Unix() - 5),
		Job:        map[string]interface{}{"method": 0},
	}
	jt.initRuntime()
	jt.nativeStats.Store(&nativeProgressSnapshot{
		src: "/fallback-src",
		dst: "/fallback-dst",
		stats: &native.Stats{
			Bytes:      500,
			TotalBytes: 2000,
			Speed:      100,
		},
		transferring: []nativeTransferringItem{
			{
				TransferItem: native.TransferItem{Name: "sub1/a.bin", Size: 1000, Bytes: 250, Percentage: 25},
				srcBase:      "/本地/Photos",
				dstBase:      "/oss/Backup/Photos",
			},
			{
				TransferItem: native.TransferItem{Name: "b.bin", Size: 1000, Bytes: 250, Percentage: 25},
				srcBase:      "/本地/Docs",
				dstBase:      "/webdav/Backup/Docs",
			},
		},
	})

	payload := jt.getCurrentPayload()
	if len(payload.DoingTask) != 2 {
		t.Fatalf("DoingTask len = %d, want 2", len(payload.DoingTask))
	}
	if payload.DoingTask[0].SrcPath != "/本地/Photos/sub1" || payload.DoingTask[0].DstPath != "/oss/Backup/Photos/sub1" || payload.DoingTask[0].FileName != "a.bin" {
		t.Fatalf("unexpected pair 0 paths: %+v", payload.DoingTask[0])
	}
	if payload.DoingTask[1].SrcPath != "/本地/Docs" || payload.DoingTask[1].DstPath != "/webdav/Backup/Docs" || payload.DoingTask[1].FileName != "b.bin" {
		t.Fatalf("unexpected pair 1 paths: %+v", payload.DoingTask[1])
	}

	maps := jt.doingTaskMaps()
	if len(maps) != 2 || maps[0]["dstPath"] != "/oss/Backup/Photos/sub1" || maps[1]["dstPath"] != "/webdav/Backup/Docs" {
		t.Fatalf("unexpected doingTaskMaps: %#v", maps)
	}
}

func TestNativeRetryFilterSupportsFilesAndDirectories(t *testing.T) {
	f := newNativeRetryFilter()
	// File retry row (isPath=0)
	f.addRow(map[string]interface{}{
		"isPath":   0,
		"srcPath":  "/本地/Photos/2026",
		"dstPath":  "/oss/Backup/2026",
		"fileName": "img1.jpg",
	})
	// Directory retry row (isPath=1)
	f.addRow(map[string]interface{}{
		"isPath":   1,
		"srcPath":  "/本地/Docs",
		"dstPath":  "/oss/Docs",
		"fileName": "reports",
	})
	// Empty-fileName directory retry row
	f.addRow(map[string]interface{}{
		"isPath":   0,
		"srcPath":  "/本地/EmptyDir",
		"dstPath":  "/oss/EmptyDir",
		"fileName": "",
	})

	if f.empty() {
		t.Fatal("filter unexpectedly empty")
	}
	// Mark overlapping pair for the empty directory so legitimately empty dirs don't trigger unmatched error.
	f.markPairMatch("/本地/EmptyDir", "/oss/EmptyDir")

	// Unrelated file is rejected.
	if f.matchesAndConsume("/本地/Photos/2026/other.jpg", "/oss/Backup/2026/other.jpg") {
		t.Fatal("unrelated file matched retry filter")
	}
	// Exact file matches once and is consumed.
	if !f.matchesAndConsume("/本地/Photos/2026/img1.jpg", "/oss/Backup/2026/img1.jpg") {
		t.Fatal("expected exact file row to match")
	}
	if f.matchesAndConsume("/本地/Photos/2026/img1.jpg", "/oss/Backup/2026/img1.jpg") {
		t.Fatal("consumed file row should not match twice")
	}
	// Directory retry row matches multiple nested files underneath it.
	if !f.matchesAndConsume("/本地/Docs/reports/q1.pdf", "/oss/Docs/reports/q1.pdf") {
		t.Fatal("expected nested file inside retried directory to match")
	}
	if !f.matchesAndConsume("/本地/Docs/reports/sub/q2.pdf", "/oss/Docs/reports/sub/q2.pdf") {
		t.Fatal("expected deeper nested file inside retried directory to match")
	}
	if f.hasUnmatched() {
		t.Fatal("expected all retry entries to be matched")
	}

	// Unmatched directory entry triggers hasUnmatched() == true.
	f2 := newNativeRetryFilter()
	f2.addRow(map[string]interface{}{
		"isPath":   1,
		"srcPath":  "/本地/Gone",
		"dstPath":  "/oss/Gone",
		"fileName": "sub",
	})
	if !f2.hasUnmatched() {
		t.Fatal("expected unmatched directory entry to report hasUnmatched() == true")
	}
}

func TestLegacyOpenListJobsAutoDisabledAndBlocked(t *testing.T) {
	defer setupPureRcloneServiceTestDB(t)()

	nativeID, err := EnsureNativeEngine()
	if err != nil {
		t.Fatalf("EnsureNativeEngine: %v", err)
	}
	legacyID, err := mapper.AddAlist("LegacyOpenList", "http://10.0.0.1:5244", "admin", "legacy-tok")
	if err != nil {
		t.Fatalf("AddAlist legacy: %v", err)
	}
	jobID, err := mapper.AddJob(map[string]interface{}{
		"enable":   1,
		"remark":   "旧版同步任务",
		"srcPath":  `["/old-src"]`,
		"dstPath":  `["/old-dst"]`,
		"alistId":  legacyID,
		"method":   0,
		"isCron":   0,
		"interval": 60,
	})
	if err != nil {
		t.Fatalf("AddJob legacy: %v", err)
	}
	taskID, err := mapper.AddJobTask(jobID, time.Now().Unix())
	if err != nil {
		t.Fatalf("AddJobTask: %v", err)
	}

	// InitJobs must auto-disable legacy OpenList jobs and skip scheduling them.
	stubRetentionCleanup(t, nil)
	InitJobs()
	waitForIdleRetention(t)

	row, err := mapper.GetJobByID(jobID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if row["enable"].(int64) != 0 {
		t.Fatalf("expected legacy job enable=0 after InitJobs, got %v", row["enable"])
	}
	jobClientListMu.RLock()
	_, scheduled := jobClientList[jobID]
	jobClientListMu.RUnlock()
	if scheduled {
		t.Fatal("legacy OpenList job must not be scheduled in jobClientList")
	}

	// Job list and dashboard both mark legacyOpenList=true.
	listRes, err := GetJobList(map[string]interface{}{"pageNum": 1, "pageSize": 20})
	if err != nil {
		t.Fatalf("GetJobList: %v", err)
	}
	rows := listRes["dataList"].([]map[string]interface{})
	if len(rows) != 1 || rows[0]["legacyOpenList"] != true || rows[0]["enable"] != 0 {
		t.Fatalf("unexpected GetJobList legacy metadata: %+v", rows)
	}

	dash, err := GetDashboardOverview()
	if err != nil {
		t.Fatalf("GetDashboardOverview: %v", err)
	}
	dashJobs := dash["jobs"].([]map[string]interface{})
	if len(dashJobs) != 1 || dashJobs[0]["legacyOpenList"] != true || dashJobs[0]["enable"] != 0 {
		t.Fatalf("unexpected dashboard legacy job summary: %+v", dashJobs)
	}

	// GetJobCurrent must return nil, nil cleanly (no error banner on view).
	if cur, err := GetJobCurrent(jobID, map[string]interface{}{}); err != nil || cur != nil {
		t.Fatalf("GetJobCurrent(legacy) = (%v, %v), want (nil, nil)", cur, err)
	}

	// All execution/mutation actions are rejected with clear guidance.
	for name, actErr := range map[string]error{
		"DoJobManual": DoJobManual(jobID),
		"ContinueJob": ContinueJob(jobID),
		"EditJobClient": EditJobClient(map[string]interface{}{
			"id":      jobID,
			"alistId": nativeID,
			"remark":  "企图编辑旧任务",
			"srcPath": []string{"/a"},
			"dstPath": []string{"/b"},
			"method":  0,
			"isCron":  2,
			"enable":  1,
		}),
		"RetryFailedTask": RetryFailedTaskItems(taskID, nil),
	} {
		if actErr == nil || !strings.Contains(actErr.Error(), "删除后重新创建") {
			t.Fatalf("%s on legacy job = %v, want legacy disabled error", name, actErr)
		}
	}
	if _, err := StartDryRun(jobID); err == nil || !strings.Contains(err.Error(), "删除后重新创建") {
		t.Fatalf("StartDryRun(legacy) = %v, want legacy disabled error", err)
	}
	if _, err := StartJobCheck(jobID, false, false); err == nil || !strings.Contains(err.Error(), "删除后重新创建") {
		t.Fatalf("StartJobCheck(legacy) = %v, want legacy disabled error", err)
	}

	// Deleting a legacy job must succeed cleanly.
	if err := RemoveJobClient(jobID); err != nil {
		t.Fatalf("RemoveJobClient(legacy) failed: %v", err)
	}
}

func TestRenameNativeStorageRewritesDstMapAndClearsBisyncState(t *testing.T) {
	defer setupPureRcloneServiceTestDB(t)()

	nativeID, err := EnsureNativeEngine()
	if err != nil {
		t.Fatal(err)
	}
	cfg := native.Config{
		"s3src": {"type": "s3", "provider": "Minio", "endpoint": "https://a.example", "access_key_id": "ak", "secret_access_key": "sk"},
		"s3dst": {"type": "s3", "provider": "Minio", "endpoint": "https://b.example", "access_key_id": "ak", "secret_access_key": "sk"},
	}
	raw, _ := json.Marshal(cfg)
	secret := string(raw)
	if err := mapper.UpdateAlist(nativeID, "内置存储", native.EngineURL, &secret); err != nil {
		t.Fatal(err)
	}

	multiJob := map[string]interface{}{
		"remark":  "多源任务",
		"alistId": nativeID,
		"method":  0,
		"isCron":  2,
		"enable":  1,
		"srcPath": []string{"/s3src/b1/photos", "/s3src/b2/docs"},
		"dstPath": []string{"/s3dst/target"},
	}
	multiID, err := CreateJobClient(multiJob)
	if err != nil {
		t.Fatalf("CreateJobClient multi: %v", err)
	}

	biJob := map[string]interface{}{
		"remark":  "双向任务",
		"alistId": nativeID,
		"method":  3,
		"isCron":  2,
		"enable":  1,
		"srcPath": []string{"/s3src/bi-a"},
		"dstPath": []string{"/s3dst/bi-b"},
	}
	biID, err := CreateJobClient(biJob)
	if err != nil {
		t.Fatalf("CreateJobClient bisync: %v", err)
	}
	if err := mapper.UpsertBisyncEntry(mapper.BisyncEntry{JobID: biID, RelPath: "listing.txt", State: mapper.BisyncStateSynced}); err != nil {
		t.Fatal(err)
	}

	if _, err := RenameNativeStorage(context.Background(), NativeRenameInput{OldName: "s3src", NewName: "s3src2"}); err != nil {
		t.Fatalf("Rename s3src -> s3src2: %v", err)
	}
	if _, err := RenameNativeStorage(context.Background(), NativeRenameInput{OldName: "s3dst", NewName: "s3dst2"}); err != nil {
		t.Fatalf("Rename s3dst -> s3dst2: %v", err)
	}

	updatedMulti, err := mapper.GetJobByID(multiID)
	if err != nil {
		t.Fatal(err)
	}
	storedMap := parseStoredJobDstMap(updatedMulti["dstMap"])
	if storedMap == nil || len(storedMap.Base) != 1 || storedMap.Base[0] != "/s3dst2/target/" {
		t.Fatalf("dstMap.Base not rewritten: %+v", storedMap)
	}
	wantKey := jobDstMapKey("/s3src2/b1/photos", "/s3dst2/target")
	if got := storedMap.Map[wantKey]; got != "/s3dst2/target/b1/photos/" {
		t.Fatalf("dstMap.Map[%q] = %q, want /s3dst2/target/b1/photos/", wantKey, got)
	}
	if entries, err := mapper.ListBisyncEntries(biID); err != nil || len(entries) != 0 {
		t.Fatalf("expected bisync baseline cleared after rename, got %v (%v)", entries, err)
	}
}

func TestAuditFixesFilenObscureS3SrcProbeBisyncConflictFilterAndRetry(t *testing.T) {
	defer setupPureRcloneServiceTestDB(t)()

	// 1. filen api_key must be flagged in skipObscure on edit when preserved.
	merged, skip, err := mergeEditRemoteParams(
		map[string]string{"type": "filen", "email": "a@b.c", "password": "obs_pw", "api_key": "obs_ak"},
		map[string]string{"type": "filen", "email": "a@b.c"},
		nil,
	)
	if err != nil {
		t.Fatalf("mergeEditRemoteParams(filen): %v", err)
	}
	if merged["api_key"] != "obs_ak" || !skip["api_key"] || !skip["password"] {
		t.Fatalf("filen edit did not mark api_key/password in skipObscure: merged=%v skip=%v", merged, skip)
	}

	// 2. bisyncMaxDelete validates 0..100 percentage.
	if _, _, err := parseNativeExtraFlags(map[string]interface{}{"bisyncMaxDelete": 101}); err == nil {
		t.Fatal("expected bisyncMaxDelete=101 to be rejected")
	}
	if extra, _, err := parseNativeExtraFlags(map[string]interface{}{"bisyncMaxDelete": 100}); err != nil || extra.BisyncMaxDelete != 100 {
		t.Fatalf("expected bisyncMaxDelete=100 accepted, got %+v (%v)", extra, err)
	}

	// 3. buildNativeRcloneFilterLines emits case-insensitive extension globs and single dir rules.
	lines := buildNativeRcloneFilterLines(map[string]interface{}{
		"exclude":    "photos/raw/\n*.jpg\nREADME.md",
		"filterMode": 1,
		"include":    "*.png\nKeep.txt",
	})
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "- photos/raw/**") || strings.Contains(joined, "**/photos/raw/**") {
		t.Fatalf("unexpected directory filter lines: %v", lines)
	}
	if !strings.Contains(joined, "- *.[jJ][pP][gG]") || !strings.Contains(joined, "+ *.[pP][nN][gG]") {
		t.Fatalf("expected case-insensitive extension globs, got: %v", lines)
	}
	if !strings.Contains(joined, "- README.md") || !strings.Contains(joined, "+ Keep.txt") {
		t.Fatalf("expected filename rules preserved case-sensitively, got: %v", lines)
	}

	// 4. collectS3ProbeBuckets scans srcPath as well as dstPath, and cross-job conflict guards bisync srcPath.
	nativeID, err := EnsureNativeEngine()
	if err != nil {
		t.Fatal(err)
	}
	cfg := native.Config{
		"s3a": {"type": "s3", "provider": "Minio", "endpoint": "https://a.example", "access_key_id": "ak", "secret_access_key": "sk"},
		"s3b": {"type": "s3", "provider": "Minio", "endpoint": "https://b.example", "access_key_id": "ak", "secret_access_key": "sk"},
	}
	raw, _ := json.Marshal(cfg)
	secret := string(raw)
	if err := mapper.UpdateAlist(nativeID, "内置存储", native.EngineURL, &secret); err != nil {
		t.Fatal(err)
	}

	biID, err := CreateJobClient(map[string]interface{}{
		"remark":  "双向A-B",
		"alistId": nativeID,
		"method":  3,
		"isCron":  2,
		"enable":  1,
		"srcPath": []string{"/s3a/src-bucket/folder"},
		"dstPath": []string{"/s3b/dst-bucket/folder"},
	})
	if err != nil {
		t.Fatalf("CreateJobClient bisync: %v", err)
	}

	buckets := collectS3ProbeBuckets(nativeID, cfg)
	if buckets["s3a"] != "/s3a/src-bucket" || buckets["s3b"] != "/s3b/dst-bucket" {
		t.Fatalf("collectS3ProbeBuckets = %v, want both src and dst S3 buckets", buckets)
	}

	// A second job writing into the bisync job's srcPath must be rejected!
	if _, err := CreateJobClient(map[string]interface{}{
		"remark":  "冲突任务",
		"alistId": nativeID,
		"method":  0,
		"isCron":  2,
		"enable":  1,
		"srcPath": []string{"/s3b/other-bucket/x"},
		"dstPath": []string{"/s3a/src-bucket/folder/sub"},
	}); err == nil {
		t.Fatal("expected destination overlapping existing bisync srcPath to be rejected")
	}

	// Selective / failed-item retry on bisync task must be rejected cleanly.
	biTaskID, err := mapper.AddJobTask(biID, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if err := RetryFailedTaskItems(biTaskID, nil); err == nil || !strings.Contains(err.Error(), "双向同步不支持") {
		t.Fatalf("RetryFailedTaskItems(bisync) = %v, want bisync unsupported error", err)
	}
}
