package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"opensync/internal/mapper"
	"opensync/internal/native"
	"opensync/pkg/util"
)

func TestContinuousJobValidationAndScheduleCompatibility(t *testing.T) {
	job := map[string]interface{}{"alistId": 1, "srcPath": "/本地/root/a", "dstPath": "/cloud/a", "isCron": 3, "method": 0, "enable": 0, "extraFlags": `{"syncDirection":"upload","syncPollIntervalSec":10,"syncAuditIntervalMin":60}`}
	if err := CleanJobInput(job); err != nil {
		t.Fatal(err)
	}
	if err := ValidateJobInput(job); err != nil {
		t.Fatal(err)
	}
	if util.ToInt(job["enable"]) != 0 {
		t.Fatal("continuous jobs must remain paused after save")
	}
	s := NewScheduler()
	defer s.Stop()
	if err := s.AddJob(3, job, func() { t.Error("continuous jobs must not use cron") }); err != nil {
		t.Fatal(err)
	}
	if len(s.cron.Entries()) != 0 {
		t.Fatal("unexpected cron entry")
	}
	job["method"] = 2
	if err := ValidateJobInput(job); err == nil {
		t.Fatal("continuous move must be rejected")
	}
	job["method"] = 0
	job["extraFlags"] = `{"syncDirection":"download"}`
	if err := ValidateJobInput(job); err == nil {
		t.Fatal("download cannot read from a local source")
	}
	for _, raw := range []string{`{"syncPollIntervalSec":9}`, `{"syncAuditIntervalMin":-1}`, `{"syncDirection":"sideways"}`} {
		if _, _, err := parseNativeExtraFlags(raw); err == nil {
			t.Fatalf("invalid continuous option accepted: %s", raw)
		}
	}
}

func TestContinuousSourceChangeCopiesSameSizeAndReconcilesAfterPause(t *testing.T) {
	if os.Getenv("OPENSYNC_RCLONE_BIN") == "" {
		t.Skip("set OPENSYNC_RCLONE_BIN for real-rclone continuous integration")
	}
	t.Setenv("OPENSYNC_DATA_DIR", t.TempDir())
	t.Setenv("TRIM_PKGVAR", "")
	defer setupNativeTestDB(t)()
	jobFSWatchers.ResetForTest()
	defer jobFSWatchers.ResetForTest()
	base := t.TempDir()
	source, target := filepath.Join(base, "source"), filepath.Join(base, "target")
	for _, p := range []string{source, target} {
		if err := os.Mkdir(p, 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", source+":"+target)
	t.Setenv("TRIM_PKGTMP", "")
	if err := SaveNativeStorage(context.Background(), NativeStorageInput{}); err != nil {
		t.Fatal(err)
	}
	state, err := GetNativeStorageState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	roots, err := GetChildPath(context.Background(), state.EngineID, "/"+native.Local)
	if err != nil {
		t.Fatal(err)
	}
	virtual := func(name string) string {
		for _, r := range roots {
			if filepath.Base(r["name"]) == name {
				return r["path"]
			}
		}
		t.Fatalf("root %s not found", name)
		return ""
	}
	if err := os.WriteFile(filepath.Join(source, "data.txt"), []byte("aaaa"), 0600); err != nil {
		t.Fatal(err)
	}
	job := map[string]interface{}{"alistId": state.EngineID, "remark": "continuous integration", "srcPath": virtual("source"), "dstPath": virtual("target"), "method": 1, "isCron": 3, "enable": 1, "extraFlags": `{"watchDebounceSec":2,"syncPollIntervalSec":10,"syncAuditIntervalMin":60}`}
	jobID, err := CreateJobClient(job)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = RemoveJobClient(jobID) }()
	waitFor := func(label string, check func() bool) {
		t.Helper()
		deadline := time.Now().Add(25 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(40 * time.Millisecond)
		}
		s, _ := mapper.GetSyncState(jobID)
		t.Fatalf("%s timed out: %+v", label, s)
	}
	content := func(name, want string) bool {
		b, err := os.ReadFile(filepath.Join(target, name))
		return err == nil && string(b) == want
	}
	waitFor("initial sync", func() bool { return content("data.txt", "aaaa") })
	waitFor("initial checkpoint", func() bool { s, _ := mapper.GetSyncState(jobID); return !s.Pending && s.LastSuccess > 0 })
	if runtime.GOOS == "linux" {
		s, err := mapper.GetSyncState(jobID)
		if err != nil || s.LastPoll != 0 {
			t.Fatalf("the watched source should not need a polling scan: %+v (%v)", s, err)
		}
	}
	// Keep both sides' size and timestamp identical, so only the watcher-driven
	// source-change flag can discover this content update.
	old, err := os.Stat(filepath.Join(source, "data.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "data.txt"), []byte("bbbb"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(source, "data.txt"), old.ModTime(), old.ModTime()); err != nil {
		t.Fatal(err)
	}
	waitFor("same-size source edit", func() bool { return content("data.txt", "bbbb") })
	waitFor("selective checkpoint", func() bool { s, _ := mapper.GetSyncState(jobID); return !s.Pending })
	if err := os.WriteFile(filepath.Join(source, "data.txt"), []byte("cccc"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(source, "data.txt"), old.ModTime(), old.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := mapper.EnqueueSyncChanges(jobID, []string{virtual("source") + "/data.txt"}, true); err != nil {
		t.Fatal(err)
	}
	waitFor("mixed full and same-size edit", func() bool { return content("data.txt", "cccc") })
	waitFor("full checkpoint", func() bool { s, _ := mapper.GetSyncState(jobID); return !s.Pending })
	if err := os.Remove(filepath.Join(target, "data.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(target, "data.txt"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "data.txt", "old.txt"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "data.txt"), []byte("dddd"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := mapper.EnqueueSyncChanges(jobID, []string{virtual("source") + "/data.txt"}, true); err != nil {
		t.Fatal(err)
	}
	waitFor("mirror type conflict recovery", func() bool { return content("data.txt", "dddd") })
	if err := PauseJob(jobID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "offline.txt"), []byte("while paused"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ContinueJob(jobID); err != nil {
		t.Fatal(err)
	}
	waitFor("pause recovery", func() bool { return content("offline.txt", "while paused") })
	if err := PauseJob(jobID); err != nil {
		t.Fatal(err)
	}
	saved, err := mapper.GetJobByID(jobID)
	if err != nil || util.ToInt(saved["enable"]) != 0 {
		t.Fatalf("pause was not persisted: %v %v", saved, err)
	}
}

func TestContinuousSelectivePreflightFailureIsNotAcknowledged(t *testing.T) {
	job := map[string]interface{}{"id": int64(1), "srcPath": "/missing/source", "dstPath": "/missing/target", "method": 0}
	jt := &JobTask{SyncPartial: true, SyncChangedFiles: []string{"/missing/source/data.txt"}}
	out := runNativePairsWithTask(context.Background(), jt, native.Config{}, job, false, nil, func(string, string, native.Event) {})
	if out.err == nil || !out.untrackedPairFailure {
		t.Fatalf("preflight error swallowed: %+v", out)
	}
}

func TestContinuousFingerprintTracksEndpointButNotTokenRefresh(t *testing.T) {
	job := map[string]interface{}{"srcPath": "/cloud/data", "dstPath": "/other/data", "alistId": 1}
	cfg := native.Config{"cloud": {"type": "webdav", "url": "https://a.example.com", "pass": "old"}, "other": {"type": "webdav", "url": "https://b.example.com"}}
	before := syncFingerprint(job, cfg)
	cfg["cloud"]["pass"] = "new"
	if syncFingerprint(job, cfg) != before {
		t.Fatal("credential refresh invalidated sync identity")
	}
	job["extraFlags"] = `{"syncPollIntervalSec":300,"watchDebounceSec":60}`
	if syncFingerprint(job, cfg) != before {
		t.Fatal("polling or debounce tuning invalidated sync identity")
	}
	cfg["cloud"]["url"] = "https://c.example.com"
	if syncFingerprint(job, cfg) == before {
		t.Fatal("endpoint change retained old sync identity")
	}
}

func TestContinuousAuditDoesNotPostponeExistingPendingWork(t *testing.T) {
	defer setupNativeTestDB(t)()
	id, err := mapper.AddJob(map[string]interface{}{"srcPath": "/s", "dstPath": "/d", "alistId": 1, "isCron": 3, "method": 0, "enable": 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := mapper.EnsureSyncState(id, "audit"); err != nil {
		t.Fatal(err)
	}
	if err := mapper.EnqueueSyncChanges(id, nil, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if err := mapper.ExecuteUpdate("UPDATE sync_state SET updatedAt=?,lastPoll=?,lastAudit=? WHERE jobId=?", now-30, now, now-120, id); err != nil {
		t.Fatal(err)
	}
	job := map[string]interface{}{"id": id, "enable": 1, "method": 0}
	jc := &JobClient{JobID: id, Job: job}
	ctx, cancel := context.WithCancel(context.Background())
	w := &jobWatcher{jobID: id, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1)}
	go w.runSync(ctx, jc, cloneJobConfig(job), nativeExtraFlags{WatchDebounceSec: 3600, SyncPollIntervalSec: 3600, SyncAuditIntervalMin: 1}, true)
	w.wake <- struct{}{}
	time.Sleep(1100 * time.Millisecond)
	w.stop()
	s, err := mapper.GetSyncState(id)
	if err != nil || s.UpdatedAt != now-30 || !s.Pending || s.LastAudit < now {
		t.Fatalf("audit postponed queued work: %+v %v", s, err)
	}
}

func TestContinuousFailedPersistenceRetainsKnownChangedPaths(t *testing.T) {
	defer setupNativeTestDB(t)()
	id, err := mapper.AddJob(map[string]interface{}{"srcPath": "/s", "dstPath": "/d", "alistId": 1, "isCron": 3, "enable": 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := mapper.EnsureSyncState(id, "persistence"); err != nil {
		t.Fatal(err)
	}
	if err := mapper.ExecuteUpdate(`CREATE TRIGGER reject_sync_event BEFORE UPDATE ON sync_state BEGIN SELECT RAISE(ABORT,'temporary write failure'); END`); err != nil {
		t.Fatal(err)
	}
	w := &jobWatcher{jobID: id, wake: make(chan struct{}, 1)}
	w.enqueue([]string{"/s/same-size.txt"}, false)
	if w.persistenceError == "" {
		t.Fatal("expected persistence failure")
	}
	if err := mapper.ExecuteUpdate("DROP TRIGGER reject_sync_event"); err != nil {
		t.Fatal(err)
	}
	w.enqueue(nil, true)
	s, err := mapper.GetSyncState(id)
	if err != nil || !s.FullScan || len(s.Paths) != 1 || s.Paths[0] != "/s/same-size.txt" {
		t.Fatalf("lost source-change information after write recovery: %+v %v", s, err)
	}
}

func TestContinuousInboxWaitsOutsideWindowAndWhileBusy(t *testing.T) {
	defer setupNativeTestDB(t)()
	id, err := mapper.AddJob(map[string]interface{}{"srcPath": "/s", "dstPath": "/d", "alistId": 1, "method": 0, "isCron": 3, "enable": 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := mapper.EnsureSyncState(id, "window"); err != nil {
		t.Fatal(err)
	}
	if err := mapper.EnqueueSyncChanges(id, []string{"a.txt"}, false); err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(schedulerLocation())
	job := map[string]interface{}{"id": id, "enable": 1, "windowEnable": 1, "windowStart": now.Add(time.Hour).Format("15:04"), "windowEnd": now.Add(2 * time.Hour).Format("15:04")}
	jc := &JobClient{JobID: id, Job: job, JobDoing: true}
	ctx, cancel := context.WithCancel(context.Background())
	w := &jobWatcher{jobID: id, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1)}
	go w.runSync(ctx, jc, cloneJobConfig(job), nativeExtraFlags{WatchDebounceSec: 2}, false)
	w.wake <- struct{}{}
	time.Sleep(1100 * time.Millisecond)
	w.stop()
	s, err := mapper.GetSyncState(id)
	if err != nil || !s.Pending || strings.Join(s.Paths, ",") != "a.txt" {
		t.Fatalf("window/busy discarded pending changes: %+v %v", s, err)
	}
	row := cloneJobConfig(job)
	row["isCron"] = 3
	addSyncStatus(row, s, nil, 0, nil)
	status := row["syncStatus"].(map[string]interface{})
	if fmt.Sprint(status["state"]) != "error" { // No registered worker must be visible as unhealthy.
		t.Fatalf("missing worker reported healthy: %v", status)
	}
}
