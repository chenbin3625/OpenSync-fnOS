package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"opensync/internal/mapper"
	"opensync/internal/native"
	"opensync/pkg/util"
)

func TestMoveExistingFileIsIdenticalSafety(t *testing.T) {
	localEp := native.Endpoint{Backend: "local", IsLocal: true}
	webdavOtherEp := native.Endpoint{Backend: "webdav", Vendor: "other"}
	cryptEp := native.Endpoint{Backend: "s3", IsCrypt: true}

	// compareMode=2 on hashless backend: same size without both MD5 MUST NOT be deemed identical (bug #1).
	src := FileMetadata{Size: 100, Modified: 1000, MD5: "5d41402abc4b2a76b9719d911017c592"}
	dstNoHash := FileMetadata{Size: 100, Modified: 1000, MD5: ""}
	if ok, _ := moveExistingFileIsIdentical(src, dstNoHash, 2, localEp, cryptEp); ok {
		t.Fatal("move mode compareMode=2 must not treat missing-MD5 destination as identical")
	}

	// compareMode=2 with matching MD5: safe.
	dstSameHash := FileMetadata{Size: 100, Modified: 2000, MD5: "5d41402abc4b2a76b9719d911017c592"}
	if ok, reason := moveExistingFileIsIdentical(src, dstSameHash, 2, localEp, localEp); !ok {
		t.Fatalf("expected matching MD5 to be identical, got %q", reason)
	}

	// compareMode=0 when dst is NEWER than src (same size, no MD5): MUST NOT be deemed identical.
	dstNewer := FileMetadata{Size: 100, Modified: 5000}
	if ok, _ := moveExistingFileIsIdentical(FileMetadata{Size: 100, Modified: 1000}, dstNewer, 0, localEp, localEp); ok {
		t.Fatal("move mode compareMode=0 must not delete older source when same-size destination has newer modtime")
	}

	// compareMode=0 on standard WebDAV (unreliable modtime): MUST NOT be deemed identical.
	if ok, _ := moveExistingFileIsIdentical(FileMetadata{Size: 100, Modified: 1000}, FileMetadata{Size: 100, Modified: 1000}, 0, localEp, webdavOtherEp); ok {
		t.Fatal("move mode compareMode=0 must not trust modtime on standard WebDAV")
	}

	// compareMode=1 (explicit size-only): same size is identical.
	if ok, reason := moveExistingFileIsIdentical(src, dstNoHash, 1, localEp, webdavOtherEp); !ok {
		t.Fatalf("expected compareMode=1 same size to be identical, got %q", reason)
	}
}

func TestFullSyncPlanDeepCheckSameSize(t *testing.T) {
	srcSnap := emptyFullSyncSnapshot("/src/")
	dstSnap := emptyFullSyncSnapshot("/dst/")
	srcSnap.dirs[""] = FileListResult{
		"a.bin": {Size: 512, Modified: 1000},
	}
	dstSnap.dirs[""] = FileListResult{
		"a.bin": {Size: 512, Modified: 1000},
	}
	plan := newFullSyncPlan(srcSnap, dstSnap, nil)
	plan.deepCheckSameSize = true
	plan.build(map[string]interface{}{"compareMode": 2, "conflictPolicy": 0})
	if len(plan.changed) != 1 || plan.changed[0].name != "a.bin" {
		t.Fatalf("expected same-size file without MD5 to be queued for deep check, got %+v", plan.changed)
	}
}

func TestFreshNASConfigBackupImportSkipMergesNativeRemotes(t *testing.T) {
	defer setupNativeTestDB(t)()

	// Simulate fresh NAS startup: EnsureNativeEngine creates empty "{}" native row.
	if _, err := EnsureNativeEngine(); err != nil {
		t.Fatalf("EnsureNativeEngine: %v", err)
	}

	tokenJSON, _ := json.Marshal(native.Config{
		"oss": {
			"type":              "s3",
			"provider":          "Alibaba",
			"endpoint":          "oss-cn-hangzhou.aliyuncs.com",
			"access_key_id":     "ak",
			"secret_access_key": "sk",
		},
	})
	payload := SystemBackupPayload{
		Version: backupFormatVersion,
		Engines: []map[string]interface{}{
			{
				"id":                99,
				"remark":            "恢复的内置存储",
				"url":               native.EngineURL,
				"userName":          "rclone",
				"token":             string(tokenJSON),
				"maxConcurrentJobs": 2,
			},
		},
		Jobs: []map[string]interface{}{
			{
				"remark":  "备份任务",
				"alistId": 99,
				"srcPath": `["/oss/bucket/src"]`,
				"dstPath": `["/oss/bucket/dst"]`,
				"method":  0,
				"isCron":  2,
			},
		},
	}

	res, err := ImportSystemBackup(payload, "skip")
	if err != nil {
		t.Fatalf("ImportSystemBackup skip failed: %v", err)
	}
	if util.ToInt(res["enginesUpdated"]) != 1 {
		t.Fatalf("expected enginesUpdated=1 on fresh NAS skip restore, got %+v", res)
	}
	if util.ToInt(res["jobsCreated"]) != 1 {
		t.Fatalf("expected jobsCreated=1 after merging native remotes, got %+v", res)
	}
}

func TestUpdateNativeStorageNotBlockedByUnrelatedOfflineLocalJob(t *testing.T) {
	defer setupNativeTestDB(t)()
	ctx := context.Background()

	tmp := t.TempDir()
	usbDir := filepath.Join(tmp, "usb")
	dstDir := filepath.Join(tmp, "dst")
	_ = os.MkdirAll(usbDir, 0755)
	_ = os.MkdirAll(dstDir, 0755)
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", usbDir+":"+dstDir)
	t.Setenv("TRIM_PKGTMP", "")

	engineID, err := EnsureNativeEngine()
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveNativeStorage(ctx, NativeStorageInput{
		Name: "oss",
		Options: map[string]string{
			"type":              "s3",
			"provider":          "Alibaba",
			"endpoint":          "oss-cn-hangzhou.aliyuncs.com",
			"access_key_id":     "ak1",
			"secret_access_key": "sk1",
		},
	}); err != nil {
		t.Fatal(err)
	}
	roots := native.AuthorizedRoots()
	if _, err := CreateJobClient(map[string]interface{}{
		"remark":  "USB本地任务",
		"alistId": engineID,
		"srcPath": []string{"/" + native.Local + "/" + roots[0].ID},
		"dstPath": []string{"/" + native.Local + "/" + roots[1].ID},
		"method":  0,
		"isCron":  2,
	}); err != nil {
		t.Fatalf("create local job: %v", err)
	}

	// Simulate USB drive unplugged / revoked authorization.
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", "")

	// Editing unrelated S3 remote must NOT be blocked by the offline USB job (bug #5).
	if err := UpdateNativeStorage(ctx, NativeRemoteEditInput{
		Name: "oss",
		Options: map[string]string{
			"type":              "s3",
			"provider":          "Alibaba",
			"endpoint":          "oss-cn-shanghai.aliyuncs.com",
			"access_key_id":     "ak2",
			"secret_access_key": "sk2",
		},
	}); err != nil {
		t.Fatalf("editing unrelated remote was blocked by offline local job: %v", err)
	}
}

func TestMoveModeSourceCrossJobConflictAndInactiveTrashDir(t *testing.T) {
	defer setupNativeTestDB(t)()
	ctx := context.Background()

	engineID, err := EnsureNativeEngine()
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveNativeStorage(ctx, NativeStorageInput{
		Name: "oss",
		Options: map[string]string{
			"type":              "s3",
			"provider":          "Alibaba",
			"endpoint":          "oss-cn-hangzhou.aliyuncs.com",
			"access_key_id":     "ak",
			"secret_access_key": "sk",
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Job 1 moves /oss/bucket/inbox -> /oss/bucket/archive (with invalid hidden trashDir, which must not block move mode, bug #12).
	if _, err := CreateJobClient(map[string]interface{}{
		"remark":      "移动入库",
		"alistId":     engineID,
		"srcPath":     []string{"/oss/bucket/inbox"},
		"dstPath":     []string{"/oss/bucket/archive"},
		"method":      2,
		"isCron":      2,
		"trashEnable": 0,
		"trashDir":    "../bad*trash",
	}); err != nil {
		t.Fatalf("create move job with inactive invalid trashDir should succeed: %v", err)
	}

	// Job 2 writing into /oss/bucket/inbox must be rejected as conflicting with Move job's source (bug #7).
	if _, err := CreateJobClient(map[string]interface{}{
		"remark":  "写入冲突",
		"alistId": engineID,
		"srcPath": []string{"/oss/bucket/other"},
		"dstPath": []string{"/oss/bucket/inbox"},
		"method":  0,
		"isCron":  2,
	}); err == nil {
		t.Fatal("expected cross-job conflict when another job writes into a Move job's source directory")
	}

	// Job 3 also moving from /oss/bucket/inbox/sub must be rejected (bug #7).
	if _, err := CreateJobClient(map[string]interface{}{
		"remark":  "双移动冲突",
		"alistId": engineID,
		"srcPath": []string{"/oss/bucket/inbox/sub"},
		"dstPath": []string{"/oss/bucket/archive2"},
		"method":  2,
		"isCron":  2,
	}); err == nil {
		t.Fatal("expected cross-job conflict between two Move jobs sharing nested source paths")
	}
}

func TestRenameNativeStorageAtomicAndBusyGuard(t *testing.T) {
	defer setupNativeTestDB(t)()
	ctx := context.Background()

	engineID, err := EnsureNativeEngine()
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveNativeStorage(ctx, NativeStorageInput{
		Name: "ossOld",
		Options: map[string]string{
			"type":              "s3",
			"provider":          "Alibaba",
			"endpoint":          "oss-cn-hangzhou.aliyuncs.com",
			"access_key_id":     "ak",
			"secret_access_key": "sk",
		},
	}); err != nil {
		t.Fatal(err)
	}
	jobID, err := CreateJobClient(map[string]interface{}{
		"remark":  "重命名测试任务",
		"alistId": engineID,
		"srcPath": []string{"/ossOld/bucket/src"},
		"dstPath": []string{"/ossOld/bucket/dst"},
		"method":  0,
		"isCron":  2,
	})
	if err != nil {
		t.Fatal(err)
	}

	// While job is marked busy, RenameNativeStorage must reject without changing DB.
	jc, err := GetJobClientByID(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if !jc.tryMarkDoing() {
		t.Fatal("expected tryMarkDoing to succeed")
	}
	if _, err := RenameNativeStorage(ctx, NativeRenameInput{OldName: "ossOld", NewName: "ossNew"}); err == nil || !strings.Contains(err.Error(), "正在执行中") {
		jc.markDone()
		t.Fatalf("expected busy guard error, got: %v", err)
	}
	jc.markDone()

	// Once idle, RenameNativeStorage updates engine & job atomically.
	res, err := RenameNativeStorage(ctx, NativeRenameInput{OldName: "ossOld", NewName: "ossNew"})
	if err != nil {
		t.Fatalf("RenameNativeStorage failed: %v", err)
	}
	if res.UpdatedJobs != 1 {
		t.Fatalf("expected 1 updated job, got %+v", res)
	}
	row, err := mapper.GetJobByID(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(util.StringValue(row["srcPath"]), "/ossNew/bucket/src") ||
		!strings.Contains(util.StringValue(row["dstPath"]), "/ossNew/bucket/dst") {
		t.Fatalf("job paths not updated atomically: src=%v dst=%v", row["srcPath"], row["dstPath"])
	}
}

func TestOpenListDestinationDefaultsCompareModeToSizeOnly(t *testing.T) {
	defer setupNativeTestDB(t)()
	ctx := context.Background()

	engineID, err := EnsureNativeEngine()
	if err != nil {
		t.Fatal(err)
	}
	remotes := []NativeStorageInput{
		{
			Name: "oss",
			Options: map[string]string{
				"type":              "s3",
				"provider":          "Alibaba",
				"endpoint":          "oss-cn-hangzhou.aliyuncs.com",
				"access_key_id":     "ak",
				"secret_access_key": "sk",
			},
		},
		{
			Name: "openlist",
			Options: map[string]string{
				"type":   "webdav",
				"url":    "http://127.0.0.1:5244/dav",
				"vendor": "other",
			},
		},
		{
			Name: "nextcloud",
			Options: map[string]string{
				"type":   "webdav",
				"url":    "https://nc.example.com/remote.php/dav/files/alice",
				"vendor": "nextcloud",
			},
		},
		{
			Name: "cryptOpenlist",
			Options: map[string]string{
				"type":     "crypt",
				"remote":   "openlist:enc",
				"password": "AAAAAAAAAAAAAAAAAAAAAA",
			},
		},
		{
			Name: "combo",
			Options: map[string]string{
				"type":      "combine",
				"upstreams": "dav=openlist: oss=oss:bucket",
			},
		},
	}
	for _, r := range remotes {
		if err := SaveNativeStorage(ctx, r); err != nil {
			t.Fatalf("SaveNativeStorage(%s): %v", r.Name, err)
		}
	}

	assertCreatedCompareMode := func(name string, payload map[string]interface{}, want int) {
		t.Helper()
		payload["remark"] = name
		payload["alistId"] = engineID
		payload["method"] = 0
		payload["isCron"] = 2
		id, err := CreateJobClient(payload)
		if err != nil {
			t.Fatalf("CreateJobClient(%s): %v", name, err)
		}
		row, err := mapper.GetJobByID(id)
		if err != nil {
			t.Fatalf("GetJobByID(%s): %v", name, err)
		}
		if got := util.ToInt(row["compareMode"]); got != want {
			t.Fatalf("%s: compareMode = %d, want %d", name, got, want)
		}
	}

	// Omitted compareMode defaults to 1 (size-only) when dst resolves to OpenList / standard WebDAV.
	assertCreatedCompareMode("openlist-default", map[string]interface{}{
		"srcPath": []string{"/oss/bucket/s1"},
		"dstPath": []string{"/openlist/quark"},
	}, 1)
	assertCreatedCompareMode("crypt-openlist-default", map[string]interface{}{
		"srcPath": []string{"/oss/bucket/s2"},
		"dstPath": []string{"/cryptOpenlist/photos"},
	}, 1)
	assertCreatedCompareMode("combine-openlist-default", map[string]interface{}{
		"srcPath": []string{"/oss/bucket/s3"},
		"dstPath": []string{"/combo/dav/archive"},
	}, 1)

	// Non-standard-WebDAV destinations keep default compareMode=0.
	assertCreatedCompareMode("combine-oss-default", map[string]interface{}{
		"srcPath": []string{"/oss/bucket/s4"},
		"dstPath": []string{"/combo/oss/archive"},
	}, 0)
	assertCreatedCompareMode("oss-default", map[string]interface{}{
		"srcPath": []string{"/oss/bucket/s5"},
		"dstPath": []string{"/oss/bucket/d5"},
	}, 0)
	assertCreatedCompareMode("nextcloud-default", map[string]interface{}{
		"srcPath": []string{"/oss/bucket/s6"},
		"dstPath": []string{"/nextcloud/d6"},
	}, 0)

	// Explicit compareMode is respected.
	assertCreatedCompareMode("openlist-explicit-md5", map[string]interface{}{
		"srcPath":     []string{"/oss/bucket/s7"},
		"dstPath":     []string{"/openlist/explicit2"},
		"compareMode": 2,
	}, 2)
	assertCreatedCompareMode("openlist-explicit-zero", map[string]interface{}{
		"srcPath":     []string{"/oss/bucket/s8"},
		"dstPath":     []string{"/openlist/explicit0"},
		"compareMode": 0,
	}, 0)

	// Runtime pair helper: Compare=0 with OpenList dst resolves to effective size-only (no deep download).
	cfg, err := loadNative(engineID)
	if err != nil {
		t.Fatal(err)
	}
	if got := nativePairEffectiveCompareMode(cfg, "/oss/bucket/s1", "/openlist/quark", 0); got != 1 {
		t.Fatalf("nativePairEffectiveCompareMode(openlist, 0) = %d, want 1", got)
	}
	if nativePairNeedsDeepCompare(cfg, "/oss/bucket/s1", "/openlist/quark", 0) {
		t.Fatal("nativePairNeedsDeepCompare must be false for OpenList under compareMode=0")
	}
	if !nativePairNeedsDeepCompare(cfg, "/oss/bucket/s1", "/openlist/quark", 2) {
		t.Fatal("nativePairNeedsDeepCompare must remain true for OpenList under explicit compareMode=2")
	}

	// FullSync plan with effective size-only override must ignore unreliable mtime differences on same-size files.
	srcSnap := emptyFullSyncSnapshot("/src/")
	dstSnap := emptyFullSyncSnapshot("/dst/")
	srcSnap.dirs[""] = FileListResult{"video.mp4": {Size: 4096, Modified: 2000}}
	dstSnap.dirs[""] = FileListResult{"video.mp4": {Size: 4096, Modified: 1000}}
	plan := newFullSyncPlan(srcSnap, dstSnap, nil)
	plan.compareMode = 1
	plan.hasCompareMode = true
	plan.deepCheckSameSize = false
	plan.build(map[string]interface{}{"compareMode": 0, "conflictPolicy": 0})
	if len(plan.changed) != 0 {
		t.Fatalf("expected same-size file to stay unchanged under effective size-only plan, got %+v", plan.changed)
	}
}
