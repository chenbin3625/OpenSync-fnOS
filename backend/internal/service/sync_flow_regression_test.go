package service

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opensync/internal/mapper"
	"opensync/internal/native"
	"opensync/pkg/util"
)

func setupSyncFlowTest(t *testing.T, cfg native.Config) int64 {
	t.Helper()
	t.Setenv("OPENSYNC_DATA_DIR", t.TempDir())
	t.Setenv("TRIM_PKGVAR", "")
	cleanupDB := setupNativeTestDB(t)
	jobClientListMu.Lock()
	previous := jobClientList
	jobClientList = make(map[int64]*JobClient)
	jobClientListMu.Unlock()
	t.Cleanup(func() {
		resetJobClientsForTest()
		jobClientListMu.Lock()
		jobClientList = previous
		jobClientListMu.Unlock()
		cleanupDB()
	})
	id, err := EnsureNativeEngine()
	if err != nil {
		t.Fatal(err)
	}
	writeSyncFlowConfig(t, id, cfg)
	return id
}

func writeSyncFlowConfig(t *testing.T, id int64, cfg native.Config) {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(data)
	if err := mapper.UpdateAlist(id, "test", native.EngineURL, &raw); err != nil {
		t.Fatal(err)
	}
}

func TestNativeJobRejectsEquivalentPan123RootIDs(t *testing.T) {
	cfg := native.Config{
		"first":  {"type": "pan123", "protocol": "open_api", "client_id": "account", "root_folder_id": "0"},
		"second": {"type": "pan123", "protocol": "open_api", "client_id": "account", "root_folder_id": "00"},
	}
	err := validateNativeSyncPaths(cfg, map[string]interface{}{
		"method": 2, "srcPath": []string{"/first/photos"}, "dstPath": []string{"/second/photos"},
	})
	if err == nil || !strings.Contains(err.Error(), "实际目录重叠") {
		t.Fatalf("move between identical 123 roots: %v", err)
	}
}

func TestNativeJobRejectsCrossPairPhysicalSourceOverlap(t *testing.T) {
	cfg := native.Config{
		"a": {"type": "s3", "provider": "Other", "endpoint": "https://storage.example.test"},
		"b": {"type": "s3", "provider": "Other", "endpoint": "https://storage.example.test"},
	}
	id := setupSyncFlowTest(t, cfg)
	job := map[string]interface{}{
		"alistId": id, "enable": 1, "isCron": 2, "method": 1,
		"srcPath": []string{"/a/bucket/source/photos", "/a/bucket/backup/source/photos/x"},
		"dstPath": []string{"/b/bucket/backup"},
	}
	if err := CleanJobInput(job); err != nil {
		t.Fatal(err)
	}
	// Each individual pair is disjoint; only the cross-pair comparison finds
	// that the first destination contains the second source through alias b.
	for _, src := range parsePathList(job["srcPath"]) {
		if _, _, err := cfg.Pair(src, jobDstForSrc(job, src, "/b/bucket/backup")); err != nil {
			t.Fatalf("individual pair should be disjoint: %v", err)
		}
	}
	if _, err := CreateJobClient(job); err == nil || !strings.Contains(err.Error(), "实际目录重叠") {
		t.Fatalf("cross-pair overlap accepted: %v", err)
	}
	rows, err := mapper.GetJobListAll()
	if err != nil || len(rows) != 0 {
		t.Fatalf("rejected job persisted: %v %v", rows, err)
	}
	// A pre-existing unsafe row must also be refused at execution and when a
	// storage edit makes two formerly distinct remotes point at the same tree.
	jobID, err := mapper.AddJob(job)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateExistingNativeJobs(id, cfg, []string{"b"}); err == nil {
		t.Fatal("storage replacement accepted cross-pair overlap")
	}
	jc, err := GetJobClientByID(jobID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := mapper.AddJobTask(jobID, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	jt, err := newJobTask(taskID, jc)
	if err != nil {
		t.Fatal(err)
	}
	jt.runNative() // Must fail before invoking rclone or contacting a remote.
	row, err := mapper.GetJobTaskByID(taskID)
	if err != nil || !strings.Contains(util.StringValue(row["errMsg"]), "实际目录重叠") {
		t.Fatalf("runtime failed to guard stored unsafe job: %v %v", row, err)
	}
	job["dstPath"] = []string{"/b/bucket/separate"}
	delete(job, "dstMap")
	if err := CleanJobInput(job); err != nil {
		t.Fatal(err)
	}
	if err := validateNativeSyncPaths(cfg, job); err != nil {
		t.Fatalf("disjoint aliases rejected: %v", err)
	}
}

func TestEditedRunningJobKeepsOldTargetReservedUntilFinished(t *testing.T) {
	cfg := native.Config{
		"cloud": {"type": "s3", "provider": "Other", "endpoint": "https://storage.example.test"},
		"alias": {"type": "s3", "provider": "Other", "endpoint": "https://storage.example.test"},
	}
	id := setupSyncFlowTest(t, cfg)
	job := map[string]interface{}{
		"alistId": id, "enable": 1, "isCron": 2, "method": 1,
		"srcPath": []string{"/cloud/bucket/source-a"}, "dstPath": []string{"/cloud/bucket/old"},
	}
	jobID, err := CreateJobClient(job)
	if err != nil {
		t.Fatal(err)
	}
	jc, err := GetJobClientByID(jobID)
	if err != nil {
		t.Fatal(err)
	}
	running := &JobTask{Job: jc.jobSnapshot(), nativeConfig: cfg}
	jc.setCurrentTask(running)
	defer jc.clearCurrentTask(running)
	if !jc.tryMarkDoing() {
		t.Fatal("cannot mark task running")
	}
	defer jc.markDone()
	edit := cloneJobConfig(running.Job)
	edit["dstPath"] = []string{"/cloud/bucket/new"}
	if err := EditJobClient(edit); err != nil {
		t.Fatalf("editing next-run configuration: %v", err)
	}
	other := map[string]interface{}{
		"alistId": id, "enable": 1, "isCron": 2, "method": 1,
		"srcPath": []string{"/cloud/bucket/source-b"}, "dstPath": []string{"/cloud/bucket/old"},
	}
	if err := validateJobDestinationConflicts(other); err == nil {
		t.Fatal("virtual conflict check released running task's old target")
	}
	if _, err := CreateJobClient(other); err == nil {
		t.Fatal("new job accepted running task's old target")
	}
	other["dstPath"] = []string{"/alias/bucket/old"}
	if err := CleanJobInput(other); err != nil {
		t.Fatal(err)
	}
	if err := validateNativeDestinations(cfg, other); err == nil {
		t.Fatal("runtime conflict check accepted physical alias of active target")
	}
	if _, err := CreateJobClient(other); err == nil {
		t.Fatal("new job accepted physical alias of active target")
	}
	// The reservation is temporary; completing the old run releases it.
	jc.markDone()
	jc.clearCurrentTask(running)
	if _, err := CreateJobClient(other); err != nil {
		t.Fatalf("completed task's old target stayed reserved: %v", err)
	}
}

func TestRefreshedNativeTokensRejectStaleSessions(t *testing.T) {
	oldToken := `{"access_token":"old-account","refresh_token":"old-refresh"}`
	newToken := `{"access_token":"new-account","refresh_token":"new-refresh"}`
	refreshedOldToken := `{"access_token":"refreshed-old-account","refresh_token":"rotated-old-refresh"}`
	original := map[string]string{"type": "drive", "token": oldToken, "root_folder_id": "old-root"}
	id := setupSyncFlowTest(t, native.Config{"drive": maps.Clone(original)})
	for _, tc := range []struct {
		name    string
		current map[string]string
		accept  bool
	}{
		{"unchanged", maps.Clone(original), true},
		{"reauthorized", map[string]string{"type": "drive", "token": newToken, "root_folder_id": "old-root"}, false},
		{"edited-account", map[string]string{"type": "drive", "token": oldToken, "root_folder_id": "new-root"}, false},
		{"replaced-backend", map[string]string{"type": "dropbox", "token": oldToken}, false},
		{"deleted", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := native.Config{}
			if tc.current != nil {
				cfg["drive"] = maps.Clone(tc.current)
			}
			writeSyncFlowConfig(t, id, cfg)
			persistRefreshedNativeTokens(map[string]native.TokenUpdate{
				"DRIVE": {Original: original, Fields: map[string]string{"token": refreshedOldToken}},
			})
			stored, err := loadNative(id)
			if err != nil {
				t.Fatal(err)
			}
			want := maps.Clone(tc.current)
			if tc.accept {
				want["token"] = refreshedOldToken
			}
			if !maps.Equal(stored["drive"], want) {
				t.Fatal("token refresh did not respect configuration version")
			}
		})
	}
	// Two sessions based on the same version may close in any order: once one
	// refresh wins, the other must not roll it back or partially mix credentials.
	proton := map[string]string{"type": "protondrive", "username": "user", "password": "AAAAAAAAAAAAAAAAAAAAAA", "client_access_token": "a0", "client_refresh_token": "r0"}
	writeSyncFlowConfig(t, id, native.Config{"proton": maps.Clone(proton)})
	for _, suffix := range []string{"1", "stale"} {
		persistRefreshedNativeTokens(map[string]native.TokenUpdate{
			"proton": {Original: proton, Fields: map[string]string{"client_access_token": "a" + suffix, "client_refresh_token": "r" + suffix}},
		})
	}
	stored, err := loadNative(id)
	if err != nil {
		t.Fatal(err)
	}
	if stored["proton"]["client_access_token"] != "a1" || stored["proton"]["client_refresh_token"] != "r1" {
		t.Fatal("stale session replaced a newer credential pair")
	}
}

func TestNativeStartupWaitsForConcurrentJobCreation(t *testing.T) {
	cfg := native.Config{"cloud": {"type": "s3", "provider": "Other", "endpoint": "https://storage.example.test"}}
	id := setupSyncFlowTest(t, cfg)
	t.Setenv("OPENSYNC_RCLONE_BIN", filepath.Join(t.TempDir(), "missing-rclone"))
	jobID, err := CreateJobClient(map[string]interface{}{
		"alistId": id, "enable": 1, "isCron": 2, "method": 0,
		"srcPath": []string{"/cloud/bucket/source-a"}, "dstPath": []string{"/cloud/bucket/old"},
	})
	if err != nil {
		t.Fatal(err)
	}
	jc, err := GetJobClientByID(jobID)
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := mapper.AddJobTask(jobID, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	jt, err := newJobTask(taskID, jc) // Old configuration, not yet published.
	if err != nil {
		t.Fatal(err)
	}
	defer jt.cancelTaskRuntime()
	edit := jc.jobSnapshot()
	edit["dstPath"] = []string{"/cloud/bucket/new"}
	if err := EditJobClient(edit); err != nil {
		t.Fatal(err)
	}
	other := map[string]interface{}{
		"alistId": id, "enable": 1, "isCron": 2, "method": 0,
		"srcPath": []string{"/cloud/bucket/source-b"}, "dstPath": []string{"/cloud/bucket/old"},
	}
	if err := CleanJobInput(other); err != nil {
		t.Fatal(err)
	}
	// Model B's create request between conflict validation and row insertion.
	alistRefMu.Lock()
	locked := true
	defer func() {
		if locked {
			alistRefMu.Unlock()
		}
	}()
	if err := validateNativeDestinations(cfg, other); err != nil {
		t.Fatal(err)
	}
	jc.setCurrentTask(jt)
	defer jc.clearCurrentTask(jt)
	started, done := make(chan struct{}), make(chan struct{})
	go func() {
		close(started)
		jt.runNative()
		close(done)
	}()
	<-started
	select {
	case <-done:
		t.Fatal("task startup bypassed the in-progress create transaction")
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := mapper.AddJob(other); err != nil {
		t.Fatal(err)
	}
	alistRefMu.Unlock()
	locked = false
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("task startup remained blocked after create completed")
	}
	row, err := mapper.GetJobTaskByID(taskID)
	if err != nil || !strings.Contains(util.StringValue(row["errMsg"]), "重叠") {
		t.Fatalf("startup did not recheck the newly committed target: %v %v", row, err)
	}
}

func TestNativeMultiPairProgressDoesNotBlockWhenQueueIsFull(t *testing.T) {
	// The fake engine waits until both prescan callbacks have started before
	// emitting completion. The old blocking enqueue deadlocked under eventMu.
	fake := filepath.Join(t.TempDir(), "rclone")
	script := "#!/bin/sh\nwhile [ ! -f \"$0.ready\" ]; do sleep 0.01; done\nprintf '%s\\n' '{\"level\":\"info\",\"msg\":\"Copied (new)\",\"object\":\"f0\"}' >&2\n"
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENSYNC_RCLONE_BIN", fake)
	cfg := native.Config{
		"src": {"type": "webdav", "url": "https://source.example.test"},
		"d1":  {"type": "webdav", "url": "https://dest1.example.test"},
		"d2":  {"type": "webdav", "url": "https://dest2.example.test"},
	}
	setupSyncFlowTest(t, cfg)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	jt := &JobTask{ctx: ctx, cancel: cancel, Waiting: newCopyQueueWithCapacity(defaultCopyQueueCapacity)}
	for i := 0; i < defaultCopyQueueCapacity; i++ {
		jt.OnlyFiles = append(jt.OnlyFiles, fmt.Sprintf("f%d", i))
	}
	job := map[string]interface{}{"srcPath": []string{"/src"}, "dstPath": []string{"/d1", "/d2"}, "method": 0}
	prescans, completed := 0, 0
	out := runNativePairsWithTask(ctx, jt, cfg, job, false, nil, func(src, dst string, event native.Event) {
		if len(event.PendingFiles) > 0 {
			prescans++
			if prescans == 2 {
				if err := os.WriteFile(fake+".ready", nil, 0600); err != nil {
					t.Error(err)
				}
			}
			jt.populateNativeWaiting(src, dst, event.PendingFiles)
		}
		if event.File != "" {
			completed++
			jt.Waiting.removeMatching(path.Join(src, path.Dir(event.File)), path.Join(dst, path.Dir(event.File)), path.Base(event.File))
		}
	})
	if out.err != nil || prescans != 2 || completed != 2 {
		t.Fatalf("multi-pair progress stalled: err=%v prescans=%d completed=%d", out.err, prescans, completed)
	}
	if jt.Waiting.len() > defaultCopyQueueCapacity || !jt.nativePendingOverflow.Load() {
		t.Fatal("overflow must stay bounded and retain interruption retry fallback")
	}
}

func TestNativeWaitingOverflowRetainsDirectoryRetry(t *testing.T) {
	setupSyncFlowTest(t, native.Config{})
	ctx, cancel := context.WithCancel(t.Context())
	jt := &JobTask{TaskID: 42, ctx: ctx, cancel: cancel, Waiting: newCopyQueueWithCapacity(1)}
	defer jt.cancelTaskRuntime()
	jt.populateNativeWaiting("/src1", "/dst1", []native.PendingFile{{Path: "a", Size: 1}})
	jt.populateNativeWaiting("/src2", "/dst2", []native.PendingFile{{Path: "b", Size: 1}})
	cancel()
	jt.recordInterruptedPendingItems([][2]string{{"/src1", "/dst1"}, {"/src2", "/dst2"}})
	if err := jt.persistRemainingTaskItems(); err != nil {
		t.Fatal(err)
	}
	items, err := mapper.GetJobTaskItemList(map[string]interface{}{"taskId": 42, "pageNum": 1, "pageSize": 10})
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := items["dataList"].([]map[string]interface{})
	found := false
	for _, row := range rows {
		if row["srcPath"] == "/src2" && taskItemObjectFromValue(row["isPath"]) == taskItemPath {
			found = true
		}
	}
	if !found {
		t.Fatal("overflowed pair lost directory-level retry on interruption")
	}
}

func TestNativeMovePreservesFilteredDestinationConflicts(t *testing.T) {
	if os.Getenv("OPENSYNC_RCLONE_BIN") == "" {
		t.Skip("set OPENSYNC_RCLONE_BIN to run real-rclone move integration tests")
	}
	for _, tc := range []struct {
		name        string
		field       string
		value       int
		srcAge      time.Duration
		dstAge      time.Duration
		dstContents string
	}{
		{"min-size", "minFileSize", 5, 0, 0, "OLD"},
		{"max-size", "maxFileSize", 15, 0, 0, "old-target-larger-than-limit"},
		{"min-age", "minFileAgeMinutes", 30, time.Hour, 0, "OLD"},
		{"older-than", "olderThanDays", 1, 48 * time.Hour, 0, "OLD"},
		{"newer-than", "newerThanDays", 1, 0, 48 * time.Hour, "OLD"},
		{"keepboth-backup", "conflictPolicy", 2, 0, 0, "OLD"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := setupSyncFlowTest(t, native.Config{})
			src, dst := filepath.Join(t.TempDir(), "source"), filepath.Join(t.TempDir(), "target")
			for _, dir := range []string{src, dst} {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			now := time.Now()
			conflictName := "conflict.txt"
			if tc.name == "keepboth-backup" {
				conflictName = "a.conflict-20260101-120000.txt"
			}
			for _, file := range []struct {
				p, contents string
				age         time.Duration
			}{
				{filepath.Join(src, conflictName), "0123456789", tc.srcAge},
				{filepath.Join(src, "new.txt"), "0123456789", tc.srcAge},
				{filepath.Join(dst, conflictName), tc.dstContents, tc.dstAge},
			} {
				if err := os.WriteFile(file.p, []byte(file.contents), 0644); err != nil {
					t.Fatal(err)
				}
				stamp := now.Add(-file.age)
				if err := os.Chtimes(file.p, stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", src+":"+dst)
			t.Setenv("TRIM_PKGTMP", "")
			job := map[string]interface{}{
				"alistId": id, "enable": 1, "isCron": 2, "method": 2, "conflictPolicy": 1,
				"srcPath": []string{testVirtualLocalRoot(t, src)}, "dstPath": []string{testVirtualLocalRoot(t, dst)},
			}
			job[tc.field] = tc.value
			jobID, err := CreateJobClient(job)
			if err != nil {
				t.Fatal(err)
			}
			jc, err := GetJobClientByID(jobID)
			if err != nil {
				t.Fatal(err)
			}
			taskID, err := mapper.AddJobTask(jobID, now.Unix())
			if err != nil {
				t.Fatal(err)
			}
			jt, err := newJobTask(taskID, jc)
			if err != nil {
				t.Fatal(err)
			}
			defer jt.cancelTaskRuntime()
			if err := jt.runNativeMove(nil); err != nil {
				t.Fatal(err)
			}
			if err := jt.persistRemainingTaskItems(); err != nil {
				t.Fatal(err)
			}
			for _, file := range []struct{ p, want string }{
				{filepath.Join(src, conflictName), "0123456789"},
				{filepath.Join(dst, conflictName), tc.dstContents},
				{filepath.Join(dst, "new.txt"), "0123456789"},
			} {
				data, err := os.ReadFile(file.p)
				if err != nil || string(data) != file.want {
					t.Fatalf("unexpected contents at %s: %q, %v", file.p, data, err)
				}
			}
			if _, err := os.Stat(filepath.Join(src, "new.txt")); !os.IsNotExist(err) {
				t.Fatalf("new, non-conflicting file was not moved: %v", err)
			}
			counts, err := mapper.QueryJobTaskCounts(taskID)
			if err != nil || util.ToInt(counts["failNum"]) != 1 {
				t.Fatalf("conflicting file should be reported as failed: %v %v", counts, err)
			}
		})
	}
}

func TestNativeJobRejectsReadOnlyCombineMount(t *testing.T) {
	cfg := native.Config{
		"src":    {"type": "s3", "provider": "Other", "endpoint": "https://storage.example.test"},
		"public": {"type": "http", "url": "https://files.example.test"},
		"combo":  {"type": "combine", "upstreams": "docs=public: writable=src:bucket/target"},
	}
	id := setupSyncFlowTest(t, cfg)
	for _, method := range []int{0, 1, 2, 3} {
		job := map[string]interface{}{
			"alistId": id, "enable": 1, "isCron": 2, "method": method,
			"srcPath": []string{"/src/bucket/source"}, "dstPath": []string{"/combo/docs"},
		}
		if _, err := CreateJobClient(job); err == nil || !strings.Contains(err.Error(), "只读") {
			t.Fatalf("method %d accepted read-only target: %v", method, err)
		}
		job["srcPath"], job["dstPath"] = []string{"/combo/docs"}, []string{"/src/bucket/target"}
		if method == 2 || method == 3 {
			if _, err := CreateJobClient(job); err == nil || !strings.Contains(err.Error(), "只读") {
				t.Fatalf("method %d accepted read-only source: %v", method, err)
			}
		}
	}
	if _, err := CreateJobClient(map[string]interface{}{
		"alistId": id, "enable": 1, "isCron": 2, "method": 0,
		"srcPath": []string{"/combo/docs"}, "dstPath": []string{"/combo/writable"},
	}); err != nil {
		t.Fatalf("copy from HTTP mount to writable sibling rejected: %v", err)
	}
}
