package service

import (
	"context"
	"encoding/json"
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

func setupNativeTestDB(t *testing.T) func() {
	t.Helper()
	_, cleanup := setupFullFeatureTestDB(t)
	return cleanup
}

func TestPan123EditOnlyKeepsTokenForUnchangedCredentials(t *testing.T) {
	old := map[string]string{
		"type": "pan123", "protocol": "open_api", "client_id": "first", "client_secret": "secret",
		"access_token": "first-token", "token_expires_at": "old-expiry",
	}
	for _, tc := range []struct {
		name                  string
		edit                  map[string]string
		wantToken, wantExpiry string
	}{
		{"description", map[string]string{"client_id": "first", "description": "changed"}, "first-token", "old-expiry"},
		{"new account", map[string]string{"client_id": "second", "client_secret": "second-secret"}, "", ""},
		{"new token", map[string]string{"client_id": "first", "access_token": "second-token", "token_expires_at": "old-expiry"}, "second-token", ""},
		{"new protocol", map[string]string{"protocol": "web", "web_user": "account", "web_password": "password"}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := mergeEditRemoteParams(old, tc.edit, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got["access_token"] != tc.wantToken || got["token_expires_at"] != tc.wantExpiry {
				t.Fatal("unexpected token inheritance")
			}
		})
	}
}

func TestNativeStorageCRUDAndProtection(t *testing.T) {
	defer setupNativeTestDB(t)()
	ctx := context.Background()

	// 1. Initialize pure local native engine with custom concurrency
	mc := 2
	if err := SaveNativeStorage(ctx, NativeStorageInput{MaxConcurrentJobs: &mc}); err != nil {
		t.Fatalf("init local native storage: %v", err)
	}
	engines, err := GetClientList()
	if err != nil || len(engines) != 1 {
		t.Fatalf("expected 1 engine, got %d (%v)", len(engines), err)
	}
	if util.ToInt(engines[0]["maxConcurrentJobs"]) != 2 {
		t.Fatalf("expected maxConcurrentJobs=2, got %v", engines[0]["maxConcurrentJobs"])
	}

	// 2. Add S3 with schemeless endpoint + crypt on top of it
	if err := SaveNativeStorage(ctx, NativeStorageInput{
		Name: "Archive",
		Options: map[string]string{
			"type":              "s3",
			"provider":          "Alibaba",
			"endpoint":          "oss-cn-hangzhou.aliyuncs.com",
			"access_key_id":     "ak1",
			"secret_access_key": "sk1",
		},
	}); err != nil {
		t.Fatalf("add S3: %v", err)
	}
	if err := SaveNativeStorage(ctx, NativeStorageInput{
		Name: "Vault",
		Options: map[string]string{
			"type":     "crypt",
			"remote":   "Archive:mybucket/enc",
			"password": "AAAAAAAAAAAAAAAAAAAAAA",
		},
	}); err != nil {
		// Plain password requires rclone binary for obscure; skip obscure if no rclone in env
		if os.Getenv("OPENSYNC_RCLONE_BIN") == "" && strings.Contains(err.Error(), "rclone") {
			t.Skip("real rclone required for crypt obscure")
		}
		t.Fatalf("add crypt: %v", err)
	}

	// 3. Case-insensitive replace keeps canonical name "Archive"
	if err := SaveNativeStorage(ctx, NativeStorageInput{
		Name:    "archive",
		Replace: true,
		Options: map[string]string{
			"type":              "s3",
			"provider":          "Alibaba",
			"endpoint":          "oss-cn-shanghai.aliyuncs.com",
			"access_key_id":     "ak2",
			"secret_access_key": "sk2",
		},
	}); err != nil {
		t.Fatalf("case-insensitive replace failed: %v", err)
	}

	// 4. Cannot delete "Archive" while "Vault" crypt depends on it
	if err := DeleteNativeStorage("Archive"); err == nil || !strings.Contains(err.Error(), "Vault") {
		t.Fatalf("expected crypt dependency error, got: %v", err)
	}

	// 5. Same crypt remote allows two sibling jobs AND multi-source job
	tmp := t.TempDir()
	src1 := filepath.Join(tmp, "photos")
	src2 := filepath.Join(tmp, "docs")
	for _, p := range []string{src1, src2} {
		if err := os.Mkdir(p, 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", src1+":"+src2)
	t.Setenv("TRIM_PKGTMP", "")
	engineID := util.ToInt64(engines[0]["id"])
	roots, err := GetChildPath(ctx, engineID, "/"+native.Local)
	if err != nil || len(roots) != 2 {
		t.Fatalf("expected 2 local roots, got %v (%v)", roots, err)
	}
	job1ID, err := CreateJobClient(map[string]interface{}{
		"remark":  "Photos Crypt",
		"alistId": engineID,
		"srcPath": []string{roots[0]["path"]},
		"dstPath": []string{"/Vault/photos"},
		"method":  0,
		"isCron":  2,
	})
	if err != nil {
		t.Fatalf("create job1 on crypt failed: %v", err)
	}
	job2ID, err := CreateJobClient(map[string]interface{}{
		"remark":  "Docs Crypt",
		"alistId": engineID,
		"srcPath": []string{roots[1]["path"]},
		"dstPath": []string{"/Vault/docs"},
		"method":  0,
		"isCron":  2,
	})
	if err != nil {
		t.Fatalf("create sibling job2 on same crypt failed: %v", err)
	}
	// Multi-source job targeting a fresh crypt subdir must also succeed
	job3ID, err := CreateJobClient(map[string]interface{}{
		"remark":  "MultiSrc Crypt",
		"alistId": engineID,
		"srcPath": []string{roots[0]["path"], roots[1]["path"]},
		"dstPath": []string{"/Vault/multi"},
		"method":  0,
		"isCron":  2,
	})
	if err != nil {
		t.Fatalf("create multi-source job on crypt failed: %v", err)
	}

	// 6. Cannot delete "Vault" while jobs reference it
	if err := DeleteNativeStorage("Vault"); err == nil || !strings.Contains(err.Error(), "同步任务") {
		t.Fatalf("expected job reference error when deleting Vault, got: %v", err)
	}

	// 7. After removing jobs, can delete Vault then Archive
	for _, jid := range []int64{job1ID, job2ID, job3ID} {
		if err := RemoveJobClient(jid); err != nil {
			t.Fatalf("remove job %d: %v", jid, err)
		}
	}
	if err := DeleteNativeStorage("Vault"); err != nil {
		t.Fatalf("delete Vault: %v", err)
	}
	if err := DeleteNativeStorage("Archive"); err != nil {
		t.Fatalf("delete Archive: %v", err)
	}
	engines, err = GetClientList()
	if err != nil {
		t.Fatal(err)
	}
	remotes, _ := engines[0]["remotes"].(map[string]string)
	if len(remotes) != 0 {
		t.Fatalf("expected 0 remotes after deletion, got %v", remotes)
	}
}

func TestNativeBackupExportImportPureLocalWithoutSecrets(t *testing.T) {
	defer setupNativeTestDB(t)()
	ctx := context.Background()
	if err := SaveNativeStorage(ctx, NativeStorageInput{}); err != nil {
		t.Fatal(err)
	}

	// Export WITHOUT secrets: pure local "{}" contains no credentials and must survive round-trip.
	exported, err := ExportSystemBackup(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Engines) != 1 || util.StringValue(exported.Engines[0]["token"]) != "{}" {
		t.Fatalf("expected pure local native engine token '{}' in redacted export, got %+v", exported.Engines)
	}

	// Wipe and re-import on a clean DB.
	defer setupNativeTestDB(t)()
	summary, err := ImportSystemBackup(*exported, "skip")
	if err != nil {
		t.Fatal(err)
	}
	if util.ToInt(summary["enginesCreated"]) != 1 {
		t.Fatalf("expected pure local native engine to import without secrets, got summary=%+v", summary)
	}
}

func TestNativeMultiPairContinuesAfterPartialFileFailure(t *testing.T) {
	if os.Getenv("OPENSYNC_RCLONE_BIN") == "" {
		t.Skip("real rclone required")
	}
	defer setupNativeTestDB(t)()
	ctx := context.Background()

	base := t.TempDir()
	src1 := filepath.Join(base, "src1")
	src2 := filepath.Join(base, "src2")
	dst1 := filepath.Join(base, "dst1")
	dst2 := filepath.Join(base, "dst2")
	for _, p := range []string{filepath.Join(src1, "blocked"), src2, dst1, dst2} {
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(src1, "blocked", "fail.txt"), []byte("faildata"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src2, "ok.txt"), []byte("successdata"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", strings.Join([]string{src1, src2, dst1, dst2}, ":"))
	t.Setenv("TRIM_PKGTMP", "")
	if err := SaveNativeStorage(ctx, NativeStorageInput{}); err != nil {
		t.Fatal(err)
	}
	engines, _ := GetClientList()
	engineID := util.ToInt64(engines[0]["id"])
	roots, err := GetChildPath(ctx, engineID, "/"+native.Local)
	if err != nil {
		t.Fatal(err)
	}
	byBase := map[string]string{}
	for _, r := range roots {
		byBase[filepath.Base(r["name"])] = r["path"]
	}
	// Block dst1/src1/blocked with a regular file so pair 1 gets a file-level failure.
	// Multi-source native backup strips the local rootID "~<12hex>" fingerprint
	// from derived destination subdirectories when basenames are unambiguous.
	rawRootID1 := path.Base(byBase["src1"])
	rawRootID2 := path.Base(byBase["src2"])
	if !strings.Contains(rawRootID1, "~") || !strings.Contains(rawRootID2, "~") {
		t.Fatalf("expected fingerprinted local root IDs, got %q and %q", rawRootID1, rawRootID2)
	}
	derived1 := "src1"
	derived2 := "src2"
	if err := os.MkdirAll(filepath.Join(dst1, derived1), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst1, derived1, "blocked"), []byte("blocker"), 0644); err != nil {
		t.Fatal(err)
	}
	srcJSON, _ := json.Marshal([]string{byBase["src1"], byBase["src2"]})
	dstJSON, _ := json.Marshal([]string{byBase["dst1"]})
	job := map[string]interface{}{
		"id":      int64(1),
		"alistId": engineID,
		"srcPath": string(srcJSON),
		"dstPath": string(dstJSON),
		"method":  0,
	}
	var succeeded, failed int
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	outcome := runNativePairs(runCtx, native.Config{}, job, false, nil, func(_, _ string, v native.Event) {
		if v.File == "" {
			return
		}
		if v.Failed {
			failed++
		} else {
			succeeded++
		}
	})
	if !outcome.hadFileFailure || outcome.untrackedPairFailure {
		t.Fatalf("expected tracked file failure only, got %+v", outcome)
	}
	if failed != 1 || succeeded != 1 {
		t.Fatalf("expected pair 2 to continue after pair 1 file failure (failed=1, succeeded=1), got failed=%d succeeded=%d", failed, succeeded)
	}
	data, err := os.ReadFile(filepath.Join(dst1, derived2, "ok.txt"))
	if err != nil || string(data) != "successdata" {
		t.Fatalf("pair 2 output missing at clean dir %q: %q (%v)", derived2, string(data), err)
	}
	if _, err := os.Stat(filepath.Join(dst1, rawRootID2)); !os.IsNotExist(err) {
		t.Fatalf("expected hashed dir %q not to be created, stat err=%v", rawRootID2, err)
	}
}

func TestNativeEngineHealthReturnsActualOfflineReason(t *testing.T) {
	defer setupNativeTestDB(t)()
	ctx := context.Background()
	if err := SaveNativeStorage(ctx, NativeStorageInput{}); err != nil {
		t.Fatal(err)
	}
	rows, err := mapper.GetAlistList()
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	id := util.ToInt64(rows[0]["id"])
	t.Setenv("OPENSYNC_RCLONE_BIN", filepath.Join(t.TempDir(), "missing-rclone"))
	health, err := CheckEngineHealth(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if health == nil || health.Status != "offline" || !strings.Contains(health.Error, "rclone") {
		t.Fatalf("expected offline health with a rclone reason, got %+v", health)
	}
}

func TestNativeStorageStateEngineSettingsAndHealthInvalidation(t *testing.T) {
	defer setupNativeTestDB(t)()
	ctx := context.Background()

	tmp := t.TempDir()
	rootA := filepath.Join(tmp, "photos")
	rootB := filepath.Join(tmp, "docs")
	for _, p := range []string{rootA, rootB} {
		if err := os.Mkdir(p, 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", rootB+":"+rootA)
	t.Setenv("TRIM_PKGTMP", "")

	// 1. In pure-rclone mode, GetNativeStorageState auto-ensures the built-in engine + authorized roots
	st, err := GetNativeStorageState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || st.EngineID <= 0 {
		t.Fatalf("expected enabled built-in native state initially, got %+v", st)
	}
	if len(st.LocalRoots) != 2 || st.LocalRoots[0].Path != rootB || st.LocalRoots[1].Path != rootA {
		t.Fatalf("expected sorted local roots [%s, %s], got %+v", rootB, rootA, st.LocalRoots)
	}

	// 2. Enable via SaveNativeEngine with custom remark & concurrency
	customRemark := "我的私有备份"
	mc := 3
	st, err = SaveNativeEngine(ctx, NativeEngineInput{
		Remark:            &customRemark,
		MaxConcurrentJobs: &mc,
	})
	if err != nil {
		t.Fatalf("SaveNativeEngine failed: %v", err)
	}
	if !st.Enabled || st.EngineID <= 0 || st.Remark != customRemark || st.MaxConcurrentJobs != 3 {
		t.Fatalf("unexpected enabled state: %+v", st)
	}

	// Seed a stale offline health cache entry to verify mutations invalidate it.
	updateEngineHealthCache(st.EngineID, customRemark, native.EngineURL, EngineHealth{
		AlistID: st.EngineID,
		Status:  "offline",
		Error:   "stale error",
	}, false)

	// 3. Add remote: preserves custom remark and invalidates health cache
	if err := SaveNativeStorage(ctx, NativeStorageInput{
		Name: "oss",
		Options: map[string]string{
			"type":              "s3",
			"provider":          "Alibaba",
			"endpoint":          "oss-cn-hangzhou.aliyuncs.com",
			"region":            "cn-hangzhou",
			"access_key_id":     "AKID123",
			"secret_access_key": "SUPER_SECRET_KEY",
			"no_check_bucket":   "true",
		},
	}); err != nil {
		t.Fatalf("SaveNativeStorage failed: %v", err)
	}
	if h := GetEngineHealth(st.EngineID); h.Status != "unknown" {
		t.Fatalf("expected stale health cache to be invalidated on save, got %+v", h)
	}

	// 4. GetNativeStorageState redacts secret_access_key and exposes non-sensitive fields
	st, err = GetNativeStorageState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Remark != customRemark {
		t.Fatalf("custom remark was overwritten: got %q", st.Remark)
	}
	if len(st.Remotes) != 1 {
		t.Fatalf("expected 1 remote, got %+v", st.Remotes)
	}
	r := st.Remotes[0]
	if r.Options["secret_access_key"] != "" {
		t.Fatalf("secret_access_key leaked in GetNativeStorageState: %+v", r.Options)
	}
	if r.Options["access_key_id"] != "AKID123" || r.Options["endpoint"] != "oss-cn-hangzhou.aliyuncs.com" {
		t.Fatalf("public S3 fields missing: %+v", r.Options)
	}
	if len(r.ConfiguredSecrets) != 1 || r.ConfiguredSecrets[0] != "secret_access_key" {
		t.Fatalf("expected configuredSecrets=[secret_access_key], got %+v", r.ConfiguredSecrets)
	}

	// 5. Delete remote: also preserves custom remark & invalidates health cache
	updateEngineHealthCache(st.EngineID, customRemark, native.EngineURL, EngineHealth{
		AlistID: st.EngineID,
		Status:  "online",
	}, false)
	if err := DeleteNativeStorage("oss"); err != nil {
		t.Fatalf("DeleteNativeStorage failed: %v", err)
	}
	if h := GetEngineHealth(st.EngineID); h.Status != "unknown" {
		t.Fatalf("expected health cache invalidated on delete, got %+v", h)
	}
	st, err = GetNativeStorageState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Remark != customRemark || len(st.Remotes) != 0 {
		t.Fatalf("unexpected state after delete: %+v", st)
	}
}

func TestNativeStorageInPlaceEditPreservesSecretsAndKnownHosts(t *testing.T) {
	defer setupNativeTestDB(t)()
	ctx := context.Background()

	// Seed an S3 and an SFTP remote directly with obscured credentials / known_hosts
	// so the test runs deterministically even without dialing a real SFTP server.
	const initialKH = "192.168.1.20 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"
	const obscuredPass = "AAAAAAAAAAAAAAAAAAAAAA" // valid >=16-byte raw base64url
	if err := SaveNativeStorage(ctx, NativeStorageInput{
		Name: "s3main",
		Options: map[string]string{
			"type":              "s3",
			"provider":          "Alibaba",
			"endpoint":          "oss-cn-hangzhou.aliyuncs.com",
			"access_key_id":     "ak_orig",
			"secret_access_key": "sk_orig",
		},
	}); err != nil {
		t.Fatalf("seed s3: %v", err)
	}
	if err := SaveNativeStorage(ctx, NativeStorageInput{
		Config: "[nas]\ntype = sftp\nhost = 192.168.1.20\nport = 22\nuser = backup\npass = " + obscuredPass + "\nknown_hosts = " + initialKH + "\n",
	}); err != nil {
		t.Fatalf("seed sftp: %v", err)
	}

	// 1. In-place edit S3: omit secret_access_key, update endpoint & region -> original secret preserved
	if err := UpdateNativeStorage(ctx, NativeRemoteEditInput{
		Name: "S3MAIN", // case-insensitive match
		Options: map[string]string{
			"type":          "s3",
			"provider":      "Alibaba",
			"endpoint":      "oss-cn-beijing.aliyuncs.com/",
			"region":        "cn-beijing",
			"access_key_id": "ak_updated",
		},
	}); err != nil {
		t.Fatalf("UpdateNativeStorage s3 failed: %v", err)
	}
	_, cfg, err := loadExistingNativeRow()
	if err != nil {
		t.Fatal(err)
	}
	if cfg["s3main"]["secret_access_key"] != "sk_orig" {
		t.Fatalf("expected original secret_access_key preserved, got %q", cfg["s3main"]["secret_access_key"])
	}
	if cfg["s3main"]["endpoint"] != "oss-cn-beijing.aliyuncs.com" || cfg["s3main"]["access_key_id"] != "ak_updated" {
		t.Fatalf("unexpected updated s3 params: %+v", cfg["s3main"])
	}

	// 2. In-place edit SFTP without changing host/port or pass -> keeps obscured pass & pinned known_hosts
	if err := UpdateNativeStorage(ctx, NativeRemoteEditInput{
		Name: "nas",
		Options: map[string]string{
			"type": "sftp",
			"host": "192.168.1.20",
			"port": "22",
			"user": "backup2",
		},
	}); err != nil {
		t.Fatalf("UpdateNativeStorage sftp failed: %v", err)
	}
	_, cfg, err = loadExistingNativeRow()
	if err != nil {
		t.Fatal(err)
	}
	if cfg["nas"]["pass"] != obscuredPass {
		t.Fatalf("expected original obscured pass preserved without re-obscuring, got %q", cfg["nas"]["pass"])
	}
	if cfg["nas"]["known_hosts"] != initialKH {
		t.Fatalf("expected pinned known_hosts preserved when host/port unchanged, got %q", cfg["nas"]["known_hosts"])
	}
	if cfg["nas"]["user"] != "backup2" {
		t.Fatalf("expected user=backup2, got %q", cfg["nas"]["user"])
	}

	// 3. Changing storage type must be rejected
	if err := UpdateNativeStorage(ctx, NativeRemoteEditInput{
		Name: "s3main",
		Options: map[string]string{
			"type": "webdav",
			"url":  "https://dav.example.com",
		},
	}); err == nil || !strings.Contains(err.Error(), "类型不能修改") {
		t.Fatalf("expected type mismatch rejection, got %v", err)
	}

	// 4. Clearing SFTP password while supplying key_pem succeeds
	if err := UpdateNativeStorage(ctx, NativeRemoteEditInput{
		Name: "nas",
		Options: map[string]string{
			"type":    "sftp",
			"host":    "192.168.1.20",
			"user":    "backup2",
			"key_pem": "-----BEGIN TEST KEY-----\nabc\n-----END TEST KEY-----",
		},
		ClearSecrets: []string{"pass"},
	}); err != nil {
		t.Fatalf("switch SFTP from pass to key_pem failed: %v", err)
	}
	_, cfg, err = loadExistingNativeRow()
	if err != nil {
		t.Fatal(err)
	}
	if cfg["nas"]["pass"] != "" || cfg["nas"]["key_pem"] == "" {
		t.Fatalf("expected pass cleared and key_pem saved, got %+v", cfg["nas"])
	}
}

func TestNativeStorageSwitchesFromStaticToRuntimeAndAnonymousCredentials(t *testing.T) {
	defer setupNativeTestDB(t)()
	ctx := context.Background()
	for _, typ := range []string{"s3", "qingstor"} {
		options := map[string]string{"type": typ, "access_key_id": "static-access", "secret_access_key": "static-secret"}
		clearSecrets := []string{"secret_access_key"}
		if typ == "s3" {
			options["session_token"] = "static-session-token"
			clearSecrets = append(clearSecrets, "session_token")
		}
		if err := SaveNativeStorage(ctx, NativeStorageInput{Name: typ, Options: options}); err != nil {
			t.Fatal(err)
		}
		for _, envAuth := range []string{"true", "false"} {
			if err := UpdateNativeStorage(ctx, NativeRemoteEditInput{
				Name: typ, Options: map[string]string{"type": typ, "env_auth": envAuth}, ClearSecrets: clearSecrets,
			}); err != nil {
				t.Fatalf("switch %s env_auth=%s: %v", typ, envAuth, err)
			}
			_, cfg, err := loadExistingNativeRow()
			if err != nil {
				t.Fatal(err)
			}
			p := cfg[typ]
			if p["env_auth"] != envAuth || p["access_key_id"] != "" || p["secret_access_key"] != "" || p["session_token"] != "" {
				t.Fatalf("static credentials survived %s auth mode change: env_auth=%s", typ, p["env_auth"])
			}
		}
	}
}

func TestNativeImportPreviewAndSelectiveImport(t *testing.T) {
	defer setupNativeTestDB(t)()
	ctx := context.Background()

	// Seed an existing S3 remote named "Cloud"
	if err := SaveNativeStorage(ctx, NativeStorageInput{
		Name: "Cloud",
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

	// Empty config must be rejected (never silently treated as local)
	if _, err := PreviewNativeImport(ctx, NativeImportInput{Config: "   "}); err == nil {
		t.Fatal("expected empty config preview to fail")
	}
	if _, err := ImportNativeStorage(ctx, NativeImportInput{Config: ""}); err == nil {
		t.Fatal("expected empty config import to fail")
	}

	// BOM-prefixed rclone.conf containing:
	// - cloud (case-insensitive S3 conflict, replaceable)
	// - dav (ready)
	// - nas (SFTP with auto-dropped md5sum_command & known_hosts_file + inline known_hosts)
	// - onedrive_box (unsupported type -> invalid, must not break preview!)
	conf := "\uFEFF" + strings.Join([]string{
		"[cloud]",
		"type = s3",
		"provider = Alibaba",
		"endpoint = oss-cn-shanghai.aliyuncs.com",
		"access_key_id = ak2",
		"secret_access_key = sk2",
		"",
		"[dav]",
		"type = webdav",
		"url = https://dav.example.com/dav",
		"vendor = nextcloud",
		"",
		"[nas]",
		"type = sftp",
		"host = 192.168.1.50",
		"user = sync",
		"key_pem = -----BEGIN KEY-----\\nbody\\n-----END KEY-----",
		"shell_type = unix",
		"md5sum_command = md5sum",
		"known_hosts_file = /root/.ssh/known_hosts",
		"known_hosts = 192.168.1.50 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl",
		"",
		"[onedrive_box]",
		"type = onedrive",
		"token = {}",
	}, "\n")

	preview, err := PreviewNativeImport(ctx, NativeImportInput{Config: conf})
	if err != nil {
		t.Fatalf("PreviewNativeImport failed: %v", err)
	}
	if preview.Encrypted || len(preview.Items) != 4 {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	byName := map[string]native.ImportCandidate{}
	for _, item := range preview.Items {
		byName[item.Name] = item
	}
	if byName["cloud"].Status != "conflict" || !byName["cloud"].CanReplace || byName["cloud"].ExistingName != "Cloud" {
		t.Fatalf("expected cloud conflict replaceable against Cloud, got %+v", byName["cloud"])
	}
	if byName["dav"].Status != "ready" {
		t.Fatalf("expected dav ready, got %+v", byName["dav"])
	}
	if byName["nas"].Status != "ready" || len(byName["nas"].Warnings) == 0 {
		t.Fatalf("expected nas ready with auto-drop warnings, got %+v", byName["nas"])
	}
	if byName["onedrive_box"].Status != "invalid" {
		t.Fatalf("expected onedrive_box invalid, got %+v", byName["onedrive_box"])
	}

	// Selective import skipping onedrive_box and replacing Cloud
	res, err := ImportNativeStorage(ctx, NativeImportInput{
		Config:   conf,
		Selected: []string{"cloud", "dav", "nas"},
		Replace:  true,
	})
	if err != nil {
		t.Fatalf("ImportNativeStorage failed: %v", err)
	}
	if len(res.Replaced) != 1 || res.Replaced[0] != "Cloud" || len(res.Imported) != 2 {
		t.Fatalf("unexpected import result: %+v", res)
	}
	_, cfg, err := loadExistingNativeRow()
	if err != nil {
		t.Fatal(err)
	}
	if cfg["Cloud"]["endpoint"] != "oss-cn-shanghai.aliyuncs.com" {
		t.Fatalf("expected Cloud updated, got %+v", cfg["Cloud"])
	}
	if cfg["nas"]["shell_type"] != "none" || cfg["nas"]["md5sum_command"] != "" || cfg["nas"]["known_hosts_file"] != "" {
		t.Fatalf("expected nas auto-fields cleaned, got %+v", cfg["nas"])
	}
	if _, hasUnsupported := cfg["onedrive_box"]; hasUnsupported {
		t.Fatal("unsupported remote must not be imported")
	}
}

func TestNativeStorageSingleTestAndDraftValidation(t *testing.T) {
	defer setupNativeTestDB(t)()
	ctx := context.Background()

	// Missing rclone returns ok=false with actionable message
	t.Setenv("OPENSYNC_RCLONE_BIN", filepath.Join(t.TempDir(), "missing-rclone"))
	res, err := TestNativeStorage(ctx, NativeStorageTestInput{Name: native.Local})
	if err != nil {
		t.Fatalf("TestNativeStorage should return structured offline result, got err=%v", err)
	}
	if res.OK || !strings.Contains(res.Message, "rclone") {
		t.Fatalf("expected ok=false mentioning rclone, got %+v", res)
	}

	// Invalid draft form returns validation error (when rclone is available)
	rcloneBin := "/root/code/OpenSync-fnOS/.tools/rclone/bin/rclone"
	if _, statErr := os.Stat(rcloneBin); statErr == nil {
		t.Setenv("OPENSYNC_RCLONE_BIN", rcloneBin)
		t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", "")
		t.Setenv("TRIM_PKGTMP", "")
		localRes, err := TestNativeStorage(ctx, NativeStorageTestInput{Name: native.Local})
		if err != nil {
			t.Fatal(err)
		}
		if localRes.OK || !strings.Contains(localRes.Message, "授权") {
			t.Fatalf("expected empty local roots test to fail cleanly, got %+v", localRes)
		}

		tmp := t.TempDir()
		t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", tmp)
		localRes, err = TestNativeStorage(ctx, NativeStorageTestInput{Name: native.Local})
		if err != nil || !localRes.OK || localRes.DirsCount != 1 {
			t.Fatalf("expected 1 authorized local root, got %+v (%v)", localRes, err)
		}

		// Draft with partial S3 credentials (access key without secret) fails validation cleanly before dialing
		if _, err := TestNativeStorage(ctx, NativeStorageTestInput{
			Options: map[string]string{"type": "s3", "provider": "Alibaba", "access_key_id": "ak-only"},
		}); err == nil {
			t.Fatal("expected partial S3 credentials validation error")
		}
	}
}
