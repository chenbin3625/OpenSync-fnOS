package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opensync/internal/config"
	"opensync/internal/mapper"
	"opensync/internal/native"
)

func TestNativeFullSyncMoveBisyncDryRunCheckRenameAndRedaction(t *testing.T) {
	if os.Getenv("OPENSYNC_RCLONE_BIN") == "" {
		t.Skip("set OPENSYNC_RCLONE_BIN to run real-rclone service integration tests")
	}
	t.Setenv("OPENSYNC_DATA_DIR", t.TempDir())
	t.Setenv("TRIM_PKGVAR", "")
	cleanupDB := setupNativeTestDB(t)
	jobClientListMu.Lock()
	prevJobClients := jobClientList
	jobClientList = make(map[int64]*JobClient)
	jobClientListMu.Unlock()
	defer func() {
		jobClientListMu.Lock()
		clients := jobClientList
		jobClientList = prevJobClients
		jobClientListMu.Unlock()
		for _, jc := range clients {
			_ = jc.StopJob(true)
		}
		cleanupDB()
	}()
	ctx := context.Background()

	runAndWait := func(jobID int64) {
		t.Helper()
		if err := DoJobManual(jobID); err != nil {
			t.Fatalf("DoJobManual(%d): %v", jobID, err)
		}
		jc, err := GetJobClientByID(jobID)
		if err != nil {
			t.Fatalf("GetJobClientByID(%d): %v", jobID, err)
		}
		if !jc.waitUntilIdle(30 * time.Second) {
			t.Fatalf("job %d did not finish within timeout", jobID)
		}
	}

	// Initialize native engine with a sample S3 remote + local dirs
	if err := SaveNativeStorage(ctx, NativeStorageInput{
		Name: "CloudBox",
		Options: map[string]string{
			"type":              "s3",
			"provider":          "Alibaba",
			"endpoint":          "oss-cn-hangzhou.aliyuncs.com",
			"access_key_id":     "ak_test",
			"secret_access_key": "sk_super_secret",
		},
	}); err != nil {
		t.Fatalf("SaveNativeStorage: %v", err)
	}
	state, err := GetNativeStorageState(ctx)
	if err != nil || !state.Enabled || state.RcloneVersion == "" || len(state.SupportedTypes) < 20 {
		t.Fatalf("unexpected NativeStorageState: %+v (%v)", state, err)
	}
	engineID := state.EngineID

	base := t.TempDir()
	srcFull := filepath.Join(base, "src_full")
	dstFull := filepath.Join(base, "dst_full")
	srcMove := filepath.Join(base, "src_move")
	dstMove := filepath.Join(base, "dst_move")
	biA := filepath.Join(base, "bi_a")
	biB := filepath.Join(base, "bi_b")
	for _, d := range []string{srcFull, dstFull, srcMove, dstMove, biA, biB} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", strings.Join([]string{srcFull, dstFull, srcMove, dstMove, biA, biB}, ":"))
	t.Setenv("TRIM_PKGTMP", "")

	roots, err := GetChildPath(ctx, engineID, "/"+native.Local)
	if err != nil {
		t.Fatalf("GetChildPath: %v", err)
	}
	findRoot := func(prefix string) string {
		for _, r := range roots {
			if filepath.Base(r["name"]) == prefix {
				return r["path"]
			}
		}
		t.Fatalf("root %q not found in %+v", prefix, roots)
		return ""
	}
	vSrcFull := findRoot("src_full")
	vDstFull := findRoot("dst_full")
	vSrcMove := findRoot("src_move")
	vDstMove := findRoot("dst_move")
	vBiA := findRoot("bi_a")
	vBiB := findRoot("bi_b")

	// --- 1. Native Full Sync (method=1) + Trash (direct deletion without secondary approval) ---
	_ = os.WriteFile(filepath.Join(srcFull, "keep.txt"), []byte("keep-v1"), 0644)
	_ = os.WriteFile(filepath.Join(dstFull, "stale.txt"), []byte("stale-to-delete"), 0644)
	staleDir := filepath.Join(dstFull, "stale-dir")
	if err := os.Mkdir(staleDir, 0755); err != nil {
		t.Fatal(err)
	}
	// This directory alone accounts for more than half the destination entries.
	for i := 0; i < 100; i++ {
		if err := os.WriteFile(filepath.Join(staleDir, fmt.Sprintf("stale-%d.txt", i)), []byte("stale"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	fullJobID, err := CreateJobClient(map[string]interface{}{
		"remark":             "原生全量同步",
		"alistId":            engineID,
		"method":             1,
		"isCron":             2,
		"enable":             1,
		"srcPath":            []string{vSrcFull},
		"dstPath":            []string{vDstFull},
		"trashEnable":        1,
		"trashDir":           ".opensync-trash",
		"trashRetentionDays": 30,
		"bwLimit":            "100M",
		"modifyWindow":       "2s",
	})
	if err != nil {
		t.Fatalf("CreateJobClient full sync: %v", err)
	}

	// Dry-run full sync first
	dryRes, err := StartDryRun(fullJobID)
	if err != nil {
		t.Fatalf("StartDryRun full sync: %v", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for dryRes.Status == "running" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		dryRes, _ = GetDryRun(fullJobID)
	}
	if dryRes.Status != "completed" || dryRes.CopyCount != 1 || dryRes.DeleteCount != 2 {
		t.Fatalf("unexpected full sync dry-run: %+v", dryRes)
	}

	// An installation that explicitly opted into confirmation keeps destination files.
	originalConfig := config.GetConfig()
	guardedConfig := *originalConfig
	guardedConfig.Server.ScheduledFullSyncConfirm = 1
	func() {
		config.SetConfigForTest(&guardedConfig)
		defer config.SetConfigForTest(originalConfig)
		runAndWait(fullJobID)
	}()
	if _, err := os.ReadFile(filepath.Join(dstFull, "keep.txt")); err != nil {
		t.Fatalf("legacy deletion guard blocked copy: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dstFull, "stale.txt")); err != nil {
		t.Fatalf("legacy deletion guard failed to preserve destination: %v", err)
	}
	if _, err := os.Stat(staleDir); err != nil {
		t.Fatalf("legacy deletion guard failed to preserve destination directory: %v", err)
	}

	// Allowing direct deletion lets the mirror trash the stale file and directory.
	runAndWait(fullJobID)
	if _, err := os.ReadFile(filepath.Join(dstFull, "keep.txt")); err != nil {
		t.Fatalf("keep.txt not copied during full sync: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dstFull, "stale.txt")); err == nil {
		t.Fatalf("stale.txt should have been deleted/trashed directly during full sync")
	}
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatalf("stale directory should have been trashed during full sync: %v", err)
	}
	foundInTrash := false
	_ = filepath.Walk(filepath.Join(dstFull, ".opensync-trash"), func(p string, info os.FileInfo, e error) error {
		if e == nil && !info.IsDir() && filepath.Base(p) == "stale.txt" {
			foundInTrash = true
		}
		return nil
	})
	if !foundInTrash {
		t.Fatal("expected stale.txt to be moved into .opensync-trash")
	}

	// --- 2. Native Move (method=2) + deleteEmptySrcDirs ---
	_ = os.MkdirAll(filepath.Join(srcMove, "sub"), 0755)
	_ = os.WriteFile(filepath.Join(srcMove, "sub", "item.txt"), []byte("move-payload"), 0644)
	moveJobID, err := CreateJobClient(map[string]interface{}{
		"remark":             "原生移动",
		"alistId":            engineID,
		"method":             2,
		"isCron":             2,
		"enable":             1,
		"srcPath":            []string{vSrcMove},
		"dstPath":            []string{vDstMove},
		"deleteEmptySrcDirs": 1,
	})
	if err != nil {
		t.Fatalf("CreateJobClient move: %v", err)
	}
	runAndWait(moveJobID)
	if got, err := os.ReadFile(filepath.Join(dstMove, "sub", "item.txt")); err != nil || string(got) != "move-payload" {
		t.Fatalf("moved file missing at dst: %q (%v)", got, err)
	}
	if _, err := os.Stat(filepath.Join(srcMove, "sub", "item.txt")); err == nil {
		t.Fatal("source file was not removed after verified native move")
	}
	if _, err := os.Stat(filepath.Join(srcMove, "sub")); err == nil {
		t.Fatal("empty source subdir was not removed when deleteEmptySrcDirs=1")
	}

	// --- 3. Native Bisync (method=3) + Check ---
	_ = os.WriteFile(filepath.Join(biA, "left.txt"), []byte("L"), 0644)
	_ = os.WriteFile(filepath.Join(biB, "right.txt"), []byte("R"), 0644)
	biJobID, err := CreateJobClient(map[string]interface{}{
		"remark":  "原生双向同步",
		"alistId": engineID,
		"method":  3,
		"isCron":  2,
		"enable":  1,
		"srcPath": []string{vBiA},
		"dstPath": []string{vBiB},
	})
	if err != nil {
		t.Fatalf("CreateJobClient bisync: %v", err)
	}
	runAndWait(biJobID)
	if got, err := os.ReadFile(filepath.Join(biB, "left.txt")); err != nil || string(got) != "L" {
		t.Fatalf("bisync left.txt missing on biB: %q (%v)", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(biA, "right.txt")); err != nil || string(got) != "R" {
		t.Fatalf("bisync right.txt missing on biA: %q (%v)", got, err)
	}

	// Consistency check on synced bisync job should report 2 matched, 0 differ
	chk, err := StartJobCheck(biJobID, true, false)
	if err != nil {
		t.Fatalf("StartJobCheck failed: %v", err)
	}
	deadline = time.Now().Add(20 * time.Second)
	for chk.Status == "running" && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		chk, _ = GetJobCheck(biJobID)
	}
	if chk.Status != "completed" || chk.MatchedCount != 2 || chk.DifferCount != 0 || chk.MissingOnDstCount != 0 || chk.MissingOnSrcCount != 0 {
		t.Fatalf("unexpected consistency check result: %+v", chk)
	}

	// --- 4. RenameNativeStorage cascades to job paths ---
	cloudJobID, err := CreateJobClient(map[string]interface{}{
		"remark":  "云端任务",
		"alistId": engineID,
		"method":  0,
		"isCron":  2,
		"enable":  1,
		"srcPath": []string{vSrcFull},
		"dstPath": []string{"/CloudBox/bucket/path"},
	})
	if err != nil {
		t.Fatalf("CreateJobClient cloudJob: %v", err)
	}
	renRes, err := RenameNativeStorage(ctx, NativeRenameInput{OldName: "CloudBox", NewName: "CloudProd"})
	if err != nil || renRes.UpdatedJobs != 1 {
		t.Fatalf("RenameNativeStorage failed: %+v (%v)", renRes, err)
	}
	updatedRow, err := mapper.GetJobByID(cloudJobID)
	if err != nil {
		t.Fatal(err)
	}
	if dsts := parsePathList(updatedRow["dstPath"]); len(dsts) != 1 || dsts[0] != "/CloudProd/bucket/path" {
		t.Fatalf("job dstPath was not cascaded on rename: %+v", dsts)
	}

	// --- 5. Non-secret backup redacts native secrets, and log redaction scrubs rclone secrets ---
	backup, err := ExportSystemBackup(false)
	if err != nil {
		t.Fatalf("ExportSystemBackup(false): %v", err)
	}
	rawBackup, _ := json.Marshal(backup)
	if strings.Contains(string(rawBackup), "sk_super_secret") {
		t.Fatal("ExportSystemBackup(false) leaked native secret_access_key")
	}
	if !strings.Contains(string(rawBackup), native.RedactedSecretPlaceholder) {
		t.Fatal("ExportSystemBackup(false) expected redacted secret placeholder for non-empty native config")
	}
	if _, err := ImportSystemBackup(*backup, "overwrite"); err != nil {
		t.Fatalf("ImportSystemBackup(redacted): %v", err)
	}
	_, storedCfg, err := loadExistingNativeRow()
	if err != nil || storedCfg["CloudProd"]["secret_access_key"] != "sk_super_secret" {
		t.Fatalf("redacted import clobbered real native secret: %q (%v)", storedCfg["CloudProd"]["secret_access_key"], err)
	}

	// Verify log redaction scrubs rclone secrets
	scrubbed := redactLogLine("failed remote secret_access_key=sk_123456 password=my_pw token=eyJabc")
	if strings.Contains(scrubbed, "sk_123456") || strings.Contains(scrubbed, "my_pw") || strings.Contains(scrubbed, "eyJabc") {
		t.Fatalf("redactLogLine did not scrub rclone secrets: %q", scrubbed)
	}

	// Verify method=3 is rejected on non-native OpenList engine
	openListID, err := mapper.AddAlist("OpenList", "http://127.0.0.1:5244", "admin", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateJobClient(map[string]interface{}{
		"remark":  "非法双向",
		"alistId": openListID,
		"method":  3,
		"isCron":  2,
		"enable":  1,
		"srcPath": []string{"/a"},
		"dstPath": []string{"/b"},
	}); err == nil {
		t.Fatal("expected method=3 on non-native engine to be rejected")
	}

	// --- 6. Storage browser write & size ops + Bisync baseline reset + extended extraFlags ---
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", strings.Join([]string{srcFull, dstFull, biA, biB}, ":"))
	if err := MkdirNativeStorage(ctx, NativePathInput{Path: vSrcFull + "/created_sub"}); err != nil {
		t.Fatalf("MkdirNativeStorage failed: %v", err)
	}
	if st, err := os.Stat(filepath.Join(srcFull, "created_sub")); err != nil || !st.IsDir() {
		t.Fatalf("created_sub missing on disk: %v", err)
	}
	if err := MoveNativeStoragePath(ctx, NativeMovePathInput{SrcPath: vSrcFull + "/created_sub", DstPath: vSrcFull + "/renamed_sub", IsDir: true}); err != nil {
		t.Fatalf("MoveNativeStoragePath failed: %v", err)
	}
	if sz, err := SizeNativeStorage(ctx, NativePathInput{Path: vSrcFull}); err != nil || sz.Count == 0 {
		t.Fatalf("SizeNativeStorage unexpected: %+v (%v)", sz, err)
	}
	if err := DeleteNativeStoragePath(ctx, NativeDeletePathInput{Path: vSrcFull + "/renamed_sub", IsDir: true}); err != nil {
		t.Fatalf("DeleteNativeStoragePath failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(srcFull, "renamed_sub")); !os.IsNotExist(err) {
		t.Fatal("expected renamed_sub deleted")
	}
	if err := DoJobManual(biJobID); err != nil {
		t.Fatalf("DoJobManual bisync failed: %v", err)
	}
	jc, _ := GetJobClientByID(biJobID)
	if !jc.waitUntilIdle(30 * time.Second) {
		t.Fatalf("bisync job did not finish")
	}
	if _, canonical, err := parseNativeExtraFlags(map[string]interface{}{
		"maxTransfer":     "1G",
		"cutoffMode":      "soft",
		"orderBy":         "size,asc",
		"bufferSize":      "16M",
		"useMmap":         true,
		"bisyncMaxDelete": 50,
	}); err != nil || !strings.Contains(canonical, `"cutoffMode":"soft"`) {
		t.Fatalf("parseNativeExtraFlags extended failed: %q (%v)", canonical, err)
	}
	if _, _, err := parseNativeExtraFlags(map[string]interface{}{"cutoffMode": "bad"}); err == nil {
		t.Fatal("expected invalid cutoffMode rejected")
	}
}
