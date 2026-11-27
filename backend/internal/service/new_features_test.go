package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"opensync/internal/config"
	"opensync/internal/mapper"
	"opensync/internal/native"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	if !shouldSendNotifyForEvent(map[string]interface{}{"eventMode": "errors_only"}, "timeout", false) {
		t.Fatal("errors_only should send on timeout")
	}
	custom := map[string]interface{}{"eventMode": "custom", "events": []interface{}{"timeout", "fail"}}
	if shouldSendNotifyForEvent(custom, "success", false) || !shouldSendNotifyForEvent(custom, "timeout", false) {
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
	isolateJobClientRegistryForTest(t)

	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "a")
	dstDir := filepath.Join(tmp, "b")
	for _, d := range []string{srcDir, dstDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", srcDir+":"+dstDir)
	t.Setenv("TRIM_PKGTMP", "")
	roots := native.AuthorizedRoots()
	var srcV, dstV string
	for _, r := range roots {
		if r.Path == srcDir {
			srcV = "/" + native.Local + "/" + r.ID
		} else if r.Path == dstDir {
			dstV = "/" + native.Local + "/" + r.ID
		}
	}

	rawCfg := `{"oss":{"type":"s3","provider":"Alibaba","endpoint":"oss-cn-hangzhou.aliyuncs.com","access_key_id":"ak1","secret_access_key":"super_secret_sk"}}`
	alistID, _ := mapper.AddAlistWithConcurrency("内置存储", native.EngineURL, "rclone", rawCfg, 1)
	notifyID, _ := mapper.AddNotify(map[string]interface{}{
		"enable": 1, "method": 6, "params": `{"botToken":"123456:secret_token_9999","chatId":"42"}`,
	})
	_, _ = mapper.AddJob(map[string]interface{}{
		"enable": 1, "remark": "备份任务", "srcPath": fmt.Sprintf("[%q]", srcV), "dstPath": fmt.Sprintf("[%q]", dstV),
		"alistId": alistID, "method": 0, "isCron": 2,
		"notifyMode": 1, "notifyIds": "[" + jsonNumberString(notifyID) + "]",
	})

	// Export without secrets should redact remote secrets
	redacted, err := ExportSystemBackup(false)
	if err != nil {
		t.Fatalf("ExportSystemBackup(false): %v", err)
	}
	if redacted.HasSecrets || strings.Contains(fmt.Sprint(redacted.Engines[0]["token"]), "super_secret_sk") {
		t.Fatalf("expected redacted native secret when includeSecrets=false: %+v", redacted.Engines[0])
	}

	// Export with secrets and import into a fresh DB
	full, err := ExportSystemBackup(true)
	if err != nil {
		t.Fatalf("ExportSystemBackup(true): %v", err)
	}
	if !full.HasSecrets || full.Engines[0]["token"] != rawCfg {
		t.Fatalf("expected raw native config when includeSecrets=true: %+v", full.Engines[0])
	}

	_, cleanup2 := setupFullFeatureTestDB(t)
	defer cleanup2()

	// Pre-insert a legacy row so the imported native engine gets ID=2 (testing remapping!)
	_, _ = mapper.AddAlistWithConcurrency("LegacyRow", "http://10.0.0.99:5244", "x", "y", 0)
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

func TestDryRunFullSyncIsReadOnly(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	dstDir := filepath.Join(tmp, "dst")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(srcDir, "keep.txt"), []byte("12345"), 0o644)
	_ = os.WriteFile(filepath.Join(srcDir, "new.txt"), []byte("new-file"), 0o644)
	_ = os.WriteFile(filepath.Join(dstDir, "keep.txt"), []byte("12345"), 0o644)
	_ = os.WriteFile(filepath.Join(dstDir, "extra.txt"), []byte("extra-file"), 0o644)
	sameTime := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_ = os.Chtimes(filepath.Join(srcDir, "keep.txt"), sameTime, sameTime)
	_ = os.Chtimes(filepath.Join(dstDir, "keep.txt"), sameTime, sameTime)

	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", srcDir+":"+dstDir)
	t.Setenv("TRIM_PKGTMP", "")
	roots := native.AuthorizedRoots()
	var srcV, dstV string
	for _, r := range roots {
		if r.Path == srcDir {
			srcV = "/" + native.Local + "/" + r.ID
		} else if r.Path == dstDir {
			dstV = "/" + native.Local + "/" + r.ID
		}
	}

	sess := &dryRunSession{
		result: DryRunResult{JobID: 1, Status: "running", Method: 1, Items: []DryRunItem{}},
	}
	job := map[string]interface{}{
		"method":         1,
		"compareMode":    0,
		"conflictPolicy": 0,
		"srcPath":        []string{srcV},
		"dstPath":        []string{dstV},
	}
	if err := dryRunNativeFullSync(context.Background(), sess, native.Config{}, job); err != nil {
		t.Fatalf("dryRunNativeFullSync error: %v", err)
	}
	snap := sess.snapshot()
	if snap.CopyCount != 1 || snap.DeleteCount != 1 || snap.TotalItems != 2 {
		t.Fatalf("unexpected dry run full sync counts: %+v", snap)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "extra.txt")); err != nil {
		t.Fatalf("dry-run modified destination file extra.txt: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("dry-run created destination file new.txt: %v", err)
	}
}

func TestDashboardOverviewWithBuiltInEngine(t *testing.T) {
	_, cleanup := setupFullFeatureTestDB(t)
	defer cleanup()

	alistID, err := EnsureNativeEngine()
	if err != nil {
		t.Fatalf("EnsureNativeEngine: %v", err)
	}
	jobID, err := mapper.AddJob(map[string]interface{}{
		"enable": 1, "remark": "总览任务", "srcPath": `["/a"]`, "dstPath": `["/b"]`,
		"alistId": alistID, "method": 3, "isCron": 3,
	})
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	for _, relPath := range []string{"open.txt", "resolved.txt"} {
		conflictID, err := mapper.UpsertBisyncConflict(mapper.BisyncConflict{
			JobID: jobID, RelPath: relPath, ConflictType: mapper.BisyncConflictContent,
		})
		if err != nil {
			t.Fatalf("UpsertBisyncConflict(%s): %v", relPath, err)
		}
		if relPath == "resolved.txt" {
			if err := mapper.ResolveBisyncConflict(conflictID); err != nil {
				t.Fatalf("ResolveBisyncConflict: %v", err)
			}
		}
	}

	overview, err := GetDashboardOverview()
	if err != nil {
		t.Fatalf("GetDashboardOverview: %v", err)
	}
	kpi, _ := overview["kpi"].(map[string]interface{})
	if kpi["totalJobs"] != 1 {
		t.Fatalf("expected totalJobs=1, got %+v", kpi)
	}
	jobs, err := GetJobList(nil)
	if err != nil {
		t.Fatalf("GetJobList: %v", err)
	}
	for source, items := range map[string]interface{}{"dashboard": overview["jobs"], "job list": jobs["dataList"]} {
		rows, ok := items.([]map[string]interface{})
		if !ok || len(rows) != 1 {
			t.Fatalf("%s jobs = %+v, want one job", source, items)
		}
		status, _ := rows[0]["syncStatus"].(map[string]interface{})
		if status["conflictCount"] != 1 {
			t.Fatalf("%s syncStatus = %+v, want one unresolved conflict", source, status)
		}
	}

	cfg := config.GetSystemSettings()
	if cfg.EngineHealthCheckInterval < 0 {
		t.Fatalf("invalid config defaults: %+v", cfg)
	}
}

func TestBatchEnableDisableAllAndRecentTaskItems(t *testing.T) {
	_, cleanup := setupFullFeatureTestDB(t)
	defer cleanup()
	isolateJobClientRegistryForTest(t)

	alistID, err := EnsureNativeEngine()
	if err != nil {
		t.Fatalf("EnsureNativeEngine: %v", err)
	}

	// Job 1: paused incremental continuous sync (eligible for enable_all)
	j1, err := mapper.AddJob(map[string]interface{}{
		"enable": 0, "remark": "增量备份", "srcPath": `["/本地/Photos"]`, "dstPath": `["/Aliyun/Photos"]`,
		"alistId": alistID, "method": 0, "isCron": 3,
	})
	if err != nil {
		t.Fatalf("AddJob j1: %v", err)
	}
	// Job 2: paused mirror continuous sync with no prior success (must be skipped by enable_all)
	j2, err := mapper.AddJob(map[string]interface{}{
		"enable": 0, "remark": "未预览镜像", "srcPath": `["/本地/Media"]`, "dstPath": `["/Aliyun/Media"]`,
		"alistId": alistID, "method": 1, "isCron": 3,
	})
	if err != nil {
		t.Fatalf("AddJob j2: %v", err)
	}
	// Job 3: local-to-local manual job (must not fail disable_all)
	j3, err := mapper.AddJob(map[string]interface{}{
		"enable": 1, "remark": "本地互备", "srcPath": `["/本地/A"]`, "dstPath": `["/本地/B"]`,
		"alistId": alistID, "method": 0, "isCron": 2,
	})
	if err != nil {
		t.Fatalf("AddJob j3: %v", err)
	}

	enableRes, err := BatchJobAction("enable_all", nil)
	if err != nil {
		t.Fatalf("BatchJobAction(enable_all): %v", err)
	}
	if enableRes["succeeded"] != 1 || enableRes["failed"] != 0 || enableRes["skipped"] != 1 {
		t.Fatalf("unexpected enable_all result: %+v", enableRes)
	}
	skippedJobs, _ := enableRes["skippedJobs"].([]map[string]interface{})
	if len(skippedJobs) != 1 || skippedJobs[0]["jobId"] != j2 || skippedJobs[0]["jobName"] != "未预览镜像" {
		t.Fatalf("unexpected skippedJobs: %+v", skippedJobs)
	}

	row1, _ := mapper.GetJobByID(j1)
	row2, _ := mapper.GetJobByID(j2)
	if int(row1["enable"].(int64)) != 1 || int(row2["enable"].(int64)) != 0 {
		t.Fatalf("job enable states after enable_all: j1=%v j2=%v", row1["enable"], row2["enable"])
	}

	disableRes, err := BatchJobAction("disable_all", nil)
	if err != nil {
		t.Fatalf("BatchJobAction(disable_all): %v", err)
	}
	if disableRes["succeeded"] != 1 || disableRes["failed"] != 0 {
		t.Fatalf("unexpected disable_all result: %+v", disableRes)
	}
	row1After, _ := mapper.GetJobByID(j1)
	if int(row1After["enable"].(int64)) != 0 {
		t.Fatalf("expected j1 disabled after disable_all, got %v", row1After["enable"])
	}

	// Recent task items filtered by storage name and __local__
	t1, _ := mapper.AddJobTask(j1, time.Now().Unix())
	t3, _ := mapper.AddJobTask(j3, time.Now().Unix())
	_ = mapper.AddJobTaskItemMany([]map[string]interface{}{
		{"taskId": t1, "srcPath": "/本地/Photos/1.jpg", "dstPath": "/Aliyun/Photos/1.jpg", "isPath": 0, "fileName": "1.jpg", "fileSize": int64(100), "type": 0, "status": 2},
		{"taskId": t3, "srcPath": "/本地/A/doc.txt", "dstPath": "/本地/B/doc.txt", "isPath": 0, "fileName": "doc.txt", "fileSize": int64(200), "type": 0, "status": 7, "errMsg": "disk full"},
	})

	aliyunRecent, err := GetRecentTaskItemList(map[string]interface{}{"storage": "Aliyun", "pageNum": 1, "pageSize": 10})
	if err != nil {
		t.Fatalf("GetRecentTaskItemList(Aliyun): %v", err)
	}
	if aliyunRecent["count"] != int64(1) {
		t.Fatalf("Aliyun recent count = %v, want 1", aliyunRecent["count"])
	}

	localRecent, err := GetRecentTaskItemList(map[string]interface{}{"storage": "__local__", "pageNum": 1, "pageSize": 10})
	if err != nil {
		t.Fatalf("GetRecentTaskItemList(__local__): %v", err)
	}
	if localRecent["count"] != int64(1) {
		t.Fatalf("__local__ recent count = %v, want 1", localRecent["count"])
	}
}

func TestValidateJobInputEnforcesSyncDirectionMode(t *testing.T) {
	run := func(job map[string]interface{}) error {
		if err := CleanJobInput(job); err != nil {
			return err
		}
		return ValidateJobInput(job)
	}

	validUpload := map[string]interface{}{
		"alistId":    1,
		"srcPath":    []string{"/本地/Photos"},
		"dstPath":    []string{"/Aliyun/Photos"},
		"method":     0,
		"isCron":     3,
		"extraFlags": `{"syncDirectionMode":"upload","oneWayDeleteMode":"tracked"}`,
	}
	if err := run(validUpload); err != nil {
		t.Fatalf("expected valid upload syncDirectionMode to succeed, got: %v", err)
	}

	reversedUpload := map[string]interface{}{
		"alistId":    1,
		"srcPath":    []string{"/Aliyun/Photos"},
		"dstPath":    []string{"/本地/Photos"},
		"method":     0,
		"isCron":     3,
		"extraFlags": `{"syncDirectionMode":"upload"}`,
	}
	if err := run(reversedUpload); err == nil {
		t.Fatalf("expected upload syncDirectionMode with remote source to fail")
	}

	validDownload := map[string]interface{}{
		"alistId":    1,
		"srcPath":    []string{"/Aliyun/Photos"},
		"dstPath":    []string{"/本地/Photos"},
		"method":     0,
		"isCron":     3,
		"extraFlags": `{"syncDirectionMode":"download"}`,
	}
	if err := run(validDownload); err != nil {
		t.Fatalf("expected valid download syncDirectionMode to succeed, got: %v", err)
	}

	reversedDownload := map[string]interface{}{
		"alistId":    1,
		"srcPath":    []string{"/本地/Photos"},
		"dstPath":    []string{"/Aliyun/Photos"},
		"method":     0,
		"isCron":     3,
		"extraFlags": `{"syncDirectionMode":"download"}`,
	}
	if err := run(reversedDownload); err == nil {
		t.Fatalf("expected download syncDirectionMode with remote destination to fail")
	}
}
