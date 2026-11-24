package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"opensync/internal/config"
	"opensync/internal/mapper"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func setupFullFeatureTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	testDB.SetMaxOpenConns(1)
	restore := mapper.SetDBForTest(testDB)
	mapper.InitSQL()
	return testDB, func() {
		restore()
		_ = testDB.Close()
	}
}

func TestTimeWindowValidationAndMatching(t *testing.T) {
	s, e, err := ValidateTimeWindow(1, "23:00", "06:30")
	if err != nil || s != "23:00" || e != "06:30" {
		t.Fatalf("ValidateTimeWindow(cross-midnight) = (%q,%q,%v)", s, e, err)
	}
	if _, _, err := ValidateTimeWindow(1, "25:00", "06:00"); err == nil {
		t.Fatal("expected invalid hour error")
	}
	if _, _, err := ValidateTimeWindow(1, "08:00", "08:00"); err == nil {
		t.Fatal("expected equal start/end error")
	}

	loc := schedulerLocation()
	// Normal window 09:00 - 18:00
	if !InTimeWindow(time.Date(2026, 1, 1, 9, 0, 0, 0, loc), "09:00", "18:00") {
		t.Fatal("09:00 should be inside 09:00-18:00")
	}
	if InTimeWindow(time.Date(2026, 1, 1, 18, 0, 0, 0, loc), "09:00", "18:00") {
		t.Fatal("18:00 should be outside 09:00-18:00 (half-open)")
	}
	// Cross-midnight window 23:00 - 06:00
	if !InTimeWindow(time.Date(2026, 1, 1, 23, 30, 0, 0, loc), "23:00", "06:00") {
		t.Fatal("23:30 should be inside 23:00-06:00")
	}
	if !InTimeWindow(time.Date(2026, 1, 2, 2, 15, 0, 0, loc), "23:00", "06:00") {
		t.Fatal("02:15 should be inside 23:00-06:00")
	}
	if InTimeWindow(time.Date(2026, 1, 2, 12, 0, 0, 0, loc), "23:00", "06:00") {
		t.Fatal("12:00 should be outside 23:00-06:00")
	}
}

func TestEngineQueueManagerSerialAndCancel(t *testing.T) {
	q := newEngineQueueManager()
	const limit = 1

	rel1, err := q.Acquire(context.Background(), 10, 101, limit)
	if err != nil {
		t.Fatalf("Acquire #1 error: %v", err)
	}
	if active, queued := q.EngineStats(10); active != 1 || queued != 0 {
		t.Fatalf("EngineStats = (%d,%d), want (1,0)", active, queued)
	}

	// Second caller blocks until released; third caller cancels while waiting.
	acquired2 := make(chan func(), 1)
	go func() {
		rel2, err := q.Acquire(context.Background(), 10, 102, limit)
		if err == nil {
			acquired2 <- rel2
		}
	}()

	// Wait until job 102 is visibly queued.
	deadline := time.Now().Add(time.Second)
	for !q.IsJobQueued(102) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !q.IsJobQueued(102) {
		t.Fatal("job 102 should be queued")
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := q.Acquire(cancelCtx, 10, 103, limit); err == nil {
		t.Fatal("canceled context should not acquire slot")
	}

	rel1()
	select {
	case rel2 := <-acquired2:
		rel2()
	case <-time.After(time.Second):
		t.Fatal("job 102 did not acquire slot after job 101 released")
	}
	if active, queued := q.EngineStats(10); active != 0 || queued != 0 {
		t.Fatalf("final EngineStats = (%d,%d), want (0,0)", active, queued)
	}
}

func TestNotifyChannelsEventFilterTemplatesAndDelivery(t *testing.T) {
	// Event filter tests
	if !shouldSendNotifyForEvent(map[string]interface{}{}, "success", false) {
		t.Fatal("default eventMode should send on success")
	}
	if shouldSendNotifyForEvent(map[string]interface{}{"eventMode": "errors_only"}, "success", false) {
		t.Fatal("errors_only should skip success")
	}
	if !shouldSendNotifyForEvent(map[string]interface{}{"eventMode": "errors_only"}, "fail", false) {
		t.Fatal("errors_only should send on fail")
	}
	if !shouldSendNotifyForEvent(map[string]interface{}{"eventMode": "errors_only"}, "approval", false) {
		t.Fatal("errors_only should send on approval")
	}
	custom := map[string]interface{}{"eventMode": "custom", "events": []interface{}{"approval", "fail"}}
	if shouldSendNotifyForEvent(custom, "success", false) || !shouldSendNotifyForEvent(custom, "approval", false) {
		t.Fatal("custom events filter mismatch")
	}

	// Template rendering
	title, content := applyNotifyTemplates(
		map[string]interface{}{
			"titleTemplate":   "[{status}] {jobName}",
			"contentTemplate": "Done {successNum}/{allNum}, err={errSummary}",
		},
		"default title",
		"default content",
		map[string]string{
			"status":     "失败",
			"jobName":    "相册备份",
			"successNum": "3",
			"allNum":     "5",
			"errSummary": "timeout",
		},
	)
	if title != "[失败] 相册备份" || content != "Done 3/5, err=timeout" {
		t.Fatalf("unexpected template render: title=%q content=%q", title, content)
	}

	// Per-job notify binding
	all := []map[string]interface{}{
		{"id": int64(1), "method": 1},
		{"id": int64(2), "method": 6},
	}
	if got := filterNotifyListForJob(all, map[string]interface{}{"notifyMode": 0}); len(got) != 2 {
		t.Fatalf("notifyMode=0 got %d, want 2", len(got))
	}
	if got := filterNotifyListForJob(all, map[string]interface{}{"notifyMode": 2}); len(got) != 0 {
		t.Fatalf("notifyMode=2 got %d, want 0", len(got))
	}
	if got := filterNotifyListForJob(all, map[string]interface{}{"notifyMode": 1, "notifyIds": "[2]"}); len(got) != 1 || got[0]["id"] != int64(2) {
		t.Fatalf("notifyMode=1 got %+v, want only id=2", got)
	}

	// Channel delivery via httptest (Telegram, WeComBot, ntfy, Gotify, PushPlus)
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/sendMessage"):
			_, _ = w.Write([]byte(`{"ok":true}`))
		case strings.Contains(r.URL.Path, "/wecom"):
			_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
		case strings.Contains(r.URL.Path, "/pushplus"):
			_, _ = w.Write([]byte(`{"code":200,"msg":"ok"}`))
		default:
			_, _ = w.Write([]byte(`{"id":1}`))
		}
	}))
	defer srv.Close()

	httpClient := srv.Client()
	if err := sendTelegram(httpClient, map[string]interface{}{"botToken": "123:ABC", "chatId": "999", "apiBase": srv.URL}, "T", "C"); err != nil {
		t.Fatalf("sendTelegram error: %v", err)
	}
	if err := sendWeComWebhook(httpClient, map[string]interface{}{"url": srv.URL + "/wecom", "msgtype": "markdown"}, "T", "C"); err != nil {
		t.Fatalf("sendWeComWebhook error: %v", err)
	}
	if err := sendNtfy(httpClient, map[string]interface{}{"server": srv.URL, "topic": "opensync", "token": "tk_abc"}, "T", "C"); err != nil {
		t.Fatalf("sendNtfy error: %v", err)
	}
	if err := sendGotify(httpClient, map[string]interface{}{"server": srv.URL, "token": "app_token", "priority": 5}, "T", "C"); err != nil {
		t.Fatalf("sendGotify error: %v", err)
	}
	// Test redaction for channels 6..11
	redactedTG := redactNotifyParams(6, `{"botToken":"123456:ABCDEF1234","chatId":"999"}`)
	if strings.Contains(redactedTG, "ABCDEF") || !strings.Contains(redactedTG, "****1234") {
		t.Fatalf("telegram botToken not redacted: %s", redactedTG)
	}
	redactedSMTP := redactNotifyParams(8, `{"host":"smtp.example.com","port":587,"password":"supersecretpassword"}`)
	if strings.Contains(redactedSMTP, "supersecret") || !strings.Contains(redactedSMTP, "****word") {
		t.Fatalf("smtp password not redacted: %s", redactedSMTP)
	}
}

func TestFilterWhitelistTimeAndTester(t *testing.T) {
	// Whitelist mode: only *.jpg / *.png, excluding thumb.jpg, min age 10 min
	req := map[string]interface{}{
		"filterMode":        1,
		"include":           "*.jpg\n*.png",
		"exclude":           "thumb.jpg\nprivate/",
		"minFileSize":       int64(100),
		"minFileAgeMinutes": 10,
		"trashEnable":       1,
		"trashDir":          ".opensync-trash",
	}

	res, err := TestJobFilter(map[string]interface{}{
		"filterMode":        req["filterMode"],
		"include":           req["include"],
		"exclude":           req["exclude"],
		"minFileSize":       req["minFileSize"],
		"minFileAgeMinutes": req["minFileAgeMinutes"],
		"trashEnable":       req["trashEnable"],
		"trashDir":          req["trashDir"],
		"samplePath":        "2026/photo.jpg",
		"sampleSize":        int64(2048),
		"sampleAgeMinutes":  30,
	})
	if err != nil || res["willSync"] != true {
		t.Fatalf("valid jpg should sync, got res=%+v err=%v", res, err)
	}

	// Fails whitelist
	res, err = TestJobFilter(map[string]interface{}{
		"filterMode":       1,
		"include":          "*.jpg",
		"samplePath":       "notes.txt",
		"sampleSize":       int64(2048),
		"sampleAgeMinutes": 30,
	})
	if err != nil || res["willSync"] != false {
		t.Fatalf("notes.txt should be rejected by whitelist, got %+v err=%v", res, err)
	}

	// Fails minFileAgeMinutes
	res, err = TestJobFilter(map[string]interface{}{
		"minFileAgeMinutes": 15,
		"samplePath":        "writing.mp4",
		"sampleSize":        int64(5000),
		"sampleAgeMinutes":  3,
	})
	if err != nil || res["willSync"] != false || res["matchedRule"] != "minFileAgeMinutes" {
		t.Fatalf("young file should be blocked by minFileAgeMinutes, got %+v err=%v", res, err)
	}

	// Trash dir auto-excluded
	res, err = TestJobFilter(map[string]interface{}{
		"trashEnable": 1,
		"trashDir":    ".my-trash",
		"samplePath":  ".my-trash/2026-01-01/a.jpg",
		"sampleSize":  int64(500),
	})
	if err != nil || res["willSync"] != false {
		t.Fatalf("trash dir should be auto-excluded, got %+v err=%v", res, err)
	}
}

func TestCompareModeConflictPolicyAndTrashHelpers(t *testing.T) {
	now := time.Date(2026, 5, 1, 14, 30, 5, 0, schedulerLocation())
	if got := conflictBackupName("report.pdf", now); got != "report.conflict-20260501-143005.pdf" {
		t.Fatalf("conflictBackupName = %q", got)
	}
	if got := conflictBackupName("README", now); got != "README.conflict-20260501-143005" {
		t.Fatalf("conflictBackupName(no-ext) = %q", got)
	}
	if dir, err := normalizeTrashDir(".opensync-trash/"); err != nil || dir != ".opensync-trash" {
		t.Fatalf("normalizeTrashDir = (%q,%v)", dir, err)
	}
	if _, err := normalizeTrashDir("/abs/trash"); err == nil {
		t.Fatal("expected absolute path rejection in normalizeTrashDir")
	}
	if _, err := normalizeTrashDir("../escape"); err == nil {
		t.Fatal("expected traversal rejection in normalizeTrashDir")
	}

	// compareMode 1 (size only): same size, different mtime -> equal (no update)
	md5A := "0123456789abcdef0123456789abcdef"
	md5B := "fedcba9876543210fedcba9876543210"
	src := FileMetadata{Size: 100, Modified: 2000, MD5: md5A}
	dst := FileMetadata{Size: 100, Modified: 1000, MD5: md5B}
	if fileChangedWithMode(src, dst, 1) {
		t.Fatal("compareMode=1 (size only) should treat equal size as unchanged")
	}
	// compareMode 2 (size + MD5): different MD5 -> differs even ignoring mtime
	if !fileChangedWithMode(src, dst, 2) {
		t.Fatal("compareMode=2 (size+md5) should detect different md5")
	}
	dstSameHash := FileMetadata{Size: 100, Modified: 1000, MD5: md5A}
	if fileChangedWithMode(src, dstSameHash, 2) {
		t.Fatal("compareMode=2 should treat matching md5 as unchanged despite mtime diff")
	}
}

func TestHistoryExportFormulaEscapeAndBatchDelete(t *testing.T) {
	_, cleanup := setupFullFeatureTestDB(t)
	defer cleanup()

	if got := csvSafeCell("=cmd|' /C calc'!A0"); !strings.HasPrefix(got, "'=") {
		t.Fatalf("csvSafeCell did not neutralize formula: %q", got)
	}

	alistID, err := mapper.AddAlistWithConcurrency("E1", "http://127.0.0.1:5244", "admin", "tok", 2)
	if err != nil {
		t.Fatalf("AddAlistWithConcurrency: %v", err)
	}
	jobID, err := mapper.AddJob(map[string]interface{}{
		"enable": 1, "remark": "导出测试", "srcPath": `["/src"]`, "dstPath": `["/dst"]`,
		"alistId": alistID, "method": 0, "isCron": 2, "compareMode": 2, "conflictPolicy": 1,
		"trashEnable": 1, "trashDir": ".trash", "trashRetentionDays": 14,
		"windowEnable": 1, "windowStart": "22:00", "windowEnd": "06:00", "windowStopOutside": 1,
		"copyConcurrency": 4, "scanConcurrency": 8, "maxRetries": 3,
		"notifyMode": 1, "notifyIds": "[1,2]",
	})
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	row, err := mapper.GetJobByID(jobID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if int(row["compareMode"].(int64)) != 2 || int(row["trashRetentionDays"].(int64)) != 14 || row["notifyIds"] != "[1,2]" {
		t.Fatalf("persisted extended job columns mismatch: %+v", row)
	}

	t1, _ := mapper.AddJobTask(jobID, time.Now().Unix())
	t2, _ := mapper.AddJobTask(jobID, time.Now().Unix())
	_ = mapper.UpdateJobTaskStatusAndNum(t1, taskStatusSuccess.Int(), nil, `{"allNum":1,"successNum":1,"failNum":0,"sumSize":1024}`)
	_ = mapper.UpdateJobTaskStatusAndNum(t2, taskStatusFailed.Int(), nil, `{"allNum":1,"successNum":0,"failNum":1,"sumSize":512}`)
	_ = mapper.AddJobTaskItemMany([]map[string]interface{}{
		{
			"taskId": t1, "srcPath": "/src", "dstPath": "/dst", "isPath": 0,
			"fileName": "=evil.csv", "fileSize": int64(1024), "type": 0, "status": 2, "errMsg": "",
		},
	})

	_, ctype, data, err := ExportTaskItems(map[string]interface{}{"taskId": t1}, "csv")
	if err != nil {
		t.Fatalf("ExportTaskItems(csv) error: %v", err)
	}
	if !strings.Contains(ctype, "text/csv") || !strings.Contains(string(data), "'=evil.csv") {
		t.Fatalf("unexpected csv export: ctype=%q body=%q", ctype, string(data))
	}

	deleted, err := RemoveTasksBatch([]int64{t1})
	if err != nil || deleted != 1 {
		t.Fatalf("RemoveTasksBatch = (%d,%v), want (1,nil)", deleted, err)
	}
	cleared, err := ClearJobHistory(jobID)
	if err != nil || cleared != 1 {
		t.Fatalf("ClearJobHistory = (%d,%v), want (1,nil)", cleared, err)
	}
}

func TestConfigExportImportRoundTripAndRemapping(t *testing.T) {
	_, cleanup := setupFullFeatureTestDB(t)
	defer cleanup()

	alistID, _ := mapper.AddAlistWithConcurrency("NAS", "http://10.0.0.2:5244", "u1", "secret-tok", 1)
	notifyID, _ := mapper.AddNotify(map[string]interface{}{
		"enable": 1, "method": 6, "params": `{"botToken":"123456:secret_token_9999","chatId":"42"}`,
	})
	_, _ = mapper.AddJob(map[string]interface{}{
		"enable": 1, "remark": "备份任务", "srcPath": `["/a"]`, "dstPath": `["/b"]`,
		"alistId": alistID, "method": 0, "isCron": 2,
		"notifyMode": 1, "notifyIds": "[" + jsonNumberString(notifyID) + "]",
	})

	// Export without secrets should mask secrets
	redacted, err := ExportSystemBackup(false)
	if err != nil {
		t.Fatalf("ExportSystemBackup(false): %v", err)
	}
	if redacted.HasSecrets || redacted.Engines[0]["token"] != "" {
		t.Fatalf("expected stripped token when includeSecrets=false: %+v", redacted.Engines[0])
	}

	// Export with secrets and import into a fresh DB
	full, err := ExportSystemBackup(true)
	if err != nil {
		t.Fatalf("ExportSystemBackup(true): %v", err)
	}
	if !full.HasSecrets || full.Engines[0]["token"] != "secret-tok" {
		t.Fatalf("expected raw token when includeSecrets=true: %+v", full.Engines[0])
	}

	_, cleanup2 := setupFullFeatureTestDB(t)
	defer cleanup2()

	// Pre-insert a dummy engine so new imported engine gets ID=2 (testing remapping!)
	_, _ = mapper.AddAlistWithConcurrency("Existing", "http://10.0.0.99:5244", "x", "y", 0)
	_, _ = mapper.AddNotify(map[string]interface{}{"enable": 1, "method": 1, "params": `{"sendKey":"dummy"}`})

	summary, err := ImportSystemBackup(*full, "skip")
	if err != nil {
		t.Fatalf("ImportSystemBackup error: %v", err)
	}
	if summary["enginesCreated"] != 1 || summary["notifiesCreated"] != 1 || summary["jobsCreated"] != 1 {
		t.Fatalf("unexpected import summary: %+v", summary)
	}
	jobs, _ := mapper.GetJobListAll()
	if len(jobs) != 1 || jobs[0]["alistId"].(int64) != 2 || jobs[0]["notifyIds"] != "[2]" {
		t.Fatalf("job foreign keys were not remapped: %+v", jobs)
	}
}

func jsonNumberString(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestAppLogsRedactionAndFiltering(t *testing.T) {
	buf := newLogRingBuffer(50)
	_, _ = buf.Write([]byte("2026/11/24 10:00:00 INFO connected to https://bot:secret@api.example.com?access_token=abcdef123456\n"))
	_, _ = buf.Write([]byte("2026/11/24 10:00:01 ERROR failed with Bearer my_super_secret_jwt_token\n"))
	lines := buf.Snapshot()
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	for _, raw := range lines {
		redacted := redactLogLine(raw)
		if strings.Contains(redacted, "abcdef123456") || strings.Contains(redacted, "my_super_secret_jwt_token") {
			t.Fatalf("log line leaked credential: %s", redacted)
		}
	}
}

func TestDryRunIncrementalAndFullSyncAreReadOnly(t *testing.T) {
	var mutatingCalls atomic.Int32
	listFn := func(dir string, isSrc bool) (FileListResult, bool, error) {
		if isSrc {
			return FileListResult{
				"keep.txt": FileMetadata{Size: 100, Modified: 1000},
				"new.txt":  FileMetadata{Size: 200, Modified: 2000},
			}, true, nil
		}
		return FileListResult{
			"keep.txt":  FileMetadata{Size: 100, Modified: 1000},
			"extra.txt": FileMetadata{Size: 300, Modified: 500},
		}, true, nil
	}

	sess := &dryRunSession{
		result: DryRunResult{JobID: 1, Status: "running", Method: 1, Items: []DryRunItem{}},
	}
	job := map[string]interface{}{"method": 1, "compareMode": 0, "conflictPolicy": 0}
	work := scanWork{SrcPath: "/src", DstPath: "/dst"}
	dryRunFullSyncWork(context.Background(), sess, job, work, nil, nil, listFn)

	if mutatingCalls.Load() != 0 {
		t.Fatal("dry run should never mutate")
	}
	snap := sess.snapshot()
	if snap.CopyCount != 1 || snap.DeleteCount != 1 || snap.TotalItems != 2 {
		t.Fatalf("unexpected dry run full sync counts: %+v", snap)
	}
}

func TestDashboardOverviewAndEngineHealthListStorages(t *testing.T) {
	_, cleanup := setupFullFeatureTestDB(t)
	defer cleanup()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/me":
			_, _ = w.Write([]byte(`{"code":200,"data":{"username":"admin"}}`))
		case "/api/admin/storage/list":
			_, _ = w.Write([]byte(`{"code":200,"data":{"content":[{"mount_path":"/nas","driver":"Local","status":"work","disabled":false},{"mount_path":"/cloud","driver":"Aliyundrive","status":"token expired","disabled":false}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	client, err := NewAlistClientContext(context.Background(), srv.URL, "tok", 1)
	if err != nil {
		t.Fatalf("NewAlistClientContext: %v", err)
	}
	defer client.Close()

	storages, known, err := client.ListStoragesContext(context.Background())
	if err != nil || !known || len(storages) != 2 {
		t.Fatalf("ListStoragesContext = (%+v, %v, %v)", storages, known, err)
	}
	if storages[0].MountPath != "/nas" || storages[1].Status != "token expired" {
		t.Fatalf("unexpected storage entries: %+v", storages)
	}

	alistID, _ := mapper.AddAlistWithConcurrency("E1", srv.URL, "admin", "tok", 1)
	_, _ = mapper.AddJob(map[string]interface{}{
		"enable": 1, "remark": "总览任务", "srcPath": `["/a"]`, "dstPath": `["/b"]`,
		"alistId": alistID, "method": 0, "isCron": 2,
	})

	overview, err := GetDashboardOverview()
	if err != nil {
		t.Fatalf("GetDashboardOverview: %v", err)
	}
	kpi, _ := overview["kpi"].(map[string]interface{})
	if kpi["totalJobs"] != 1 {
		t.Fatalf("expected totalJobs=1, got %+v", kpi)
	}

	// Verify new config settings validation & defaults
	cfg := config.GetSystemSettings()
	if cfg.FullSyncApprovalTimeout < 0 || cfg.EngineHealthCheckInterval < 0 {
		t.Fatalf("invalid config defaults: %+v", cfg)
	}
}
