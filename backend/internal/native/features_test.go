package native

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func requireRclone(t *testing.T) {
	t.Helper()
	if os.Getenv("OPENSYNC_RCLONE_BIN") == "" {
		t.Skip("set OPENSYNC_RCLONE_BIN to run real-rclone integration tests")
	}
}

func startLocalWebDAVServer(t *testing.T, ctx context.Context, rootDir string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	cmd := exec.CommandContext(ctx, os.Getenv("OPENSYNC_RCLONE_BIN"), "serve", "webdav", rootDir, "--addr", addr, "--config", os.DevNull)
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "XDG_CONFIG_HOME="+t.TempDir())
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	for i := 0; i < 100; i++ {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return "http://" + addr
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("webdav server on %s did not start in time", addr)
	return ""
}

func TestExpandedBackendsValidationAndCycleDetection(t *testing.T) {
	// 1. Valid new backends: ftp, http, b2, onedrive (with valid JSON token), chunker, compress, hasher, union
	cfg := Config{
		"ftpbox": {
			"type": "ftp",
			"host": "127.0.0.1",
			"port": "21",
			"user": "alice",
			"pass": "AAAAAAAAAAAAAAAAAAAAAA",
		},
		"mirror": {
			"type": "http",
			"url":  "https://mirrors.example.com/files/",
		},
		"b2store": {
			"type":    "b2",
			"account": "acct123",
			"key":     "appkey456",
		},
		"od": {
			"type":  "onedrive",
			"token": `{"access_token":"tok1","refresh_token":"ref1"}`,
		},
		"chunked": {
			"type":       "chunker",
			"remote":     "b2store:bucket/chunks",
			"chunk_size": "100M",
		},
		"compressed": {
			"type":   "compress",
			"remote": "b2store:bucket/gz",
			"mode":   "gzip",
		},
		"hashed": {
			"type":   "hasher",
			"remote": "ftpbox:data",
			"hashes": "md5",
		},
		"merged": {
			"type":      "union",
			"upstreams": "b2store:bucket/a ftpbox:b:ro",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid expanded config, got: %v", err)
	}

	// 2. HTTP backend is read-only: allowed as source, rejected as destination
	if _, _, err := cfg.Pair("/mirror/iso", "/b2store/bucket"); err != nil {
		t.Fatalf("expected http as source allowed, got: %v", err)
	}
	if _, _, err := cfg.Pair("/b2store/bucket", "/mirror/iso"); err == nil {
		t.Fatal("expected http as destination to be rejected")
	}

	// 3. OAuth backend with empty token `{}` must still fail validation
	badOD := Config{
		"bad_od": {
			"type":  "onedrive",
			"token": `{}`,
		},
	}
	if err := badOD.Validate(); err == nil {
		t.Fatal("expected onedrive with empty token {} to fail validation")
	}

	// 4. Cycle detection across wrapper & union remotes
	cycle := Config{
		"a": {"type": "chunker", "remote": "b:"},
		"b": {"type": "union", "upstreams": "a:sub"},
	}
	if err := cycle.Validate(); err == nil {
		t.Fatal("expected cycle across chunker -> union -> chunker to fail validation")
	}

	// 5. Rename cascades to chunker, compress, hasher, union
	renamed, err := cfg.Rename("b2store", "b2prod")
	if err != nil {
		t.Fatalf("Rename failed: %v", err)
	}
	if _, ok := renamed["b2store"]; ok {
		t.Fatal("old key still present after rename")
	}
	if got := renamed["chunked"]["remote"]; got != "b2prod:bucket/chunks" {
		t.Fatalf("chunker remote not updated: %q", got)
	}
	if got := renamed["compressed"]["remote"]; got != "b2prod:bucket/gz" {
		t.Fatalf("compress remote not updated: %q", got)
	}
	if got := renamed["merged"]["upstreams"]; !strings.Contains(got, "b2prod:bucket/a") {
		t.Fatalf("union upstreams not updated: %q", got)
	}
}

func TestAllSyncDirectionsConflictTrashSymlinksAndTuning(t *testing.T) {
	requireRclone(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	base := t.TempDir()
	localA := filepath.Join(base, "localA")
	localB := filepath.Join(base, "localB")
	davDir1 := filepath.Join(base, "dav1")
	davDir2 := filepath.Join(base, "dav2")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{localA, localB, davDir1, davDir2, outside} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", localA+":"+localB)
	t.Setenv("TRIM_PKGTMP", "")

	davURL1 := startLocalWebDAVServer(t, ctx, davDir1)
	davURL2 := startLocalWebDAVServer(t, ctx, davDir2)

	cfg := Config{
		"r1": {"type": "webdav", "url": davURL1, "vendor": "rclone"},
		"r2": {"type": "webdav", "url": davURL2, "vendor": "rclone"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	vLocalA, err := virtualLocal(localA)
	if err != nil {
		t.Fatal(err)
	}
	vLocalB, err := virtualLocal(localB)
	if err != nil {
		t.Fatal(err)
	}

	// Seed localA with regular file, empty subdir, in-root symlink, and escaping symlink
	if err := os.WriteFile(filepath.Join(localA, "doc.txt"), []byte("v1-content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(localA, "empty_sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("doc.txt", filepath.Join(localA, "inroot_link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("top-secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(localA, "escape_link.txt")); err != nil {
		t.Fatal(err)
	}

	// 1. Local -> Remote (r1) with CreateEmptySrcDirs + FollowSymlinks + tuning flags
	err = cfg.Copy(ctx, vLocalA, "/r1", CopyOptions{
		Transfers:          2,
		Checkers:           2,
		BwLimit:            "100M",
		TPSLimit:           50,
		ModifyWindow:       "2s",
		MultiThreadStreams: 2,
		CreateEmptySrcDirs: true,
		FollowSymlinks:     true,
		CheckFirst:         true,
	}, nil)
	if err != nil {
		t.Fatalf("local -> remote copy failed: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(davDir1, "doc.txt")); err != nil || string(data) != "v1-content" {
		t.Fatalf("doc.txt not synced to r1: %q (%v)", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(davDir1, "inroot_link.txt")); err != nil || string(data) != "v1-content" {
		t.Fatalf("in-root symlink not followed: %q (%v)", data, err)
	}
	if _, err := os.Stat(filepath.Join(davDir1, "escape_link.txt")); err == nil {
		t.Fatal("security violation: escaping symlink was copied to remote!")
	}
	if st, err := os.Stat(filepath.Join(davDir1, "empty_sub")); err != nil || !st.IsDir() {
		t.Fatalf("empty_sub was not created on destination: %v", err)
	}

	// 2. Remote -> Remote (r1 -> r2)
	if err := cfg.Copy(ctx, "/r1", "/r2", CopyOptions{Transfers: 2, Checkers: 2}, nil); err != nil {
		t.Fatalf("remote -> remote copy failed: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(davDir2, "doc.txt")); err != nil || string(data) != "v1-content" {
		t.Fatalf("doc.txt not synced r1 -> r2: %q (%v)", data, err)
	}

	// 3. Remote -> Local (r2 -> localB)
	if err := cfg.Copy(ctx, "/r2", vLocalB, CopyOptions{Transfers: 2, Checkers: 2}, nil); err != nil {
		t.Fatalf("remote -> local copy failed: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(localB, "doc.txt")); err != nil || string(data) != "v1-content" {
		t.Fatalf("doc.txt not synced r2 -> localB: %q (%v)", data, err)
	}

	// 4. ConflictPolicy 2 (keep both with --suffix, NO --backup-dir)
	future := time.Now().Add(2 * time.Minute)
	if err := os.WriteFile(filepath.Join(localA, "doc.txt"), []byte("v2-newer-content"), 0644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(filepath.Join(localA, "doc.txt"), future, future)
	if err := cfg.Copy(ctx, vLocalA, vLocalB, CopyOptions{
		Transfers:      2,
		Checkers:       2,
		ConflictPolicy: 2,
		ConflictSuffix: ".conflict-20261125-120000",
		OnlyFiles:      []PendingFile{{Path: "doc.txt"}},
	}, nil); err != nil {
		t.Fatalf("conflict policy 2 copy failed: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(localB, "doc.txt")); err != nil || string(data) != "v2-newer-content" {
		t.Fatalf("expected updated doc.txt, got %q (%v)", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(localB, "doc.conflict-20261125-120000.txt")); err != nil || string(data) != "v1-content" {
		t.Fatalf("expected conflict-suffixed backup file, got %q (%v)", data, err)
	}

	// 5. Overwrite with Trash BackupDir
	future2 := future.Add(2 * time.Minute)
	if err := os.WriteFile(filepath.Join(localA, "doc.txt"), []byte("v3-newest-content"), 0644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(filepath.Join(localA, "doc.txt"), future2, future2)
	if err := cfg.Copy(ctx, vLocalA, vLocalB, CopyOptions{
		Transfers: 2,
		Checkers:  2,
		BackupDir: ".opensync-trash/2026-11-25/overwrite",
		OnlyFiles: []PendingFile{{Path: "doc.txt"}},
	}, nil); err != nil {
		t.Fatalf("backup-dir trash copy failed: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(localB, ".opensync-trash/2026-11-25/overwrite/doc.txt")); err != nil || string(data) != "v2-newer-content" {
		t.Fatalf("expected overwritten file in trash dir, got %q (%v)", data, err)
	}
}

func TestExportImportEncryptedAboutBrowseRestoreCheckAndBisync(t *testing.T) {
	requireRclone(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	base := t.TempDir()
	localSrc := filepath.Join(base, "src")
	localDst := filepath.Join(base, "dst")
	cryptBacking := filepath.Join(base, "crypt_store")
	restoreDir := filepath.Join(base, "restore")
	for _, d := range []string{localSrc, localDst, cryptBacking, restoreDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", strings.Join([]string{localSrc, localDst, cryptBacking, restoreDir}, ":"))
	t.Setenv("TRIM_PKGTMP", "")

	vSrc, _ := virtualLocal(localSrc)
	vDst, _ := virtualLocal(localDst)
	vRestore, _ := virtualLocal(restoreDir)

	cfg := Config{
		"vault": {
			"type":      "crypt",
			"remote":    cryptBacking,
			"password":  "my-super-secret-pass",
			"password2": "my-salt-value",
		},
	}
	if err := Prepare(ctx, nil, cfg, true); err != nil {
		t.Fatalf("Prepare crypt failed: %v", err)
	}

	// 1. ExportINI plain + encrypted round-trip
	plainINI, err := cfg.ExportINI(ctx, nil, "")
	if err != nil || !strings.Contains(plainINI, "[vault]") {
		t.Fatalf("plain ExportINI failed: %v", err)
	}
	encINI, err := cfg.ExportINI(ctx, []string{"vault"}, "export-pass-123")
	if err != nil || !strings.Contains(encINI, "RCLONE_ENCRYPT_V0") {
		t.Fatalf("encrypted ExportINI failed: %v (len=%d)", err, len(encINI))
	}
	roundTrip, wasEnc, err := parseImportRaw(ctx, encINI, "export-pass-123")
	if err != nil || !wasEnc || roundTrip["vault"]["type"] != "crypt" {
		t.Fatalf("encrypted round-trip parseImportRaw failed: wasEnc=%v err=%v", wasEnc, err)
	}

	// 2. About local authorized roots
	aboutLocal, err := cfg.About(ctx, Local, SessionOptions{})
	if err != nil || !aboutLocal.Supported || aboutLocal.Total == nil || *aboutLocal.Total <= 0 {
		t.Fatalf("About(Local) failed: %+v (%v)", aboutLocal, err)
	}

	// 3. Copy plaintext into crypt, browse decrypted entries, and Restore back to local (both abs & virtual paths)
	if err := os.WriteFile(filepath.Join(localSrc, "alpha.txt"), []byte("hello-crypt"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Copy(ctx, vSrc, "/vault", CopyOptions{Transfers: 2, Checkers: 2}, nil); err != nil {
		t.Fatalf("copy to crypt failed: %v", err)
	}
	entries, exists, err := cfg.ListDirEntries(ctx, "/vault", SessionOptions{})
	if err != nil || !exists || len(entries) != 1 || entries[0].Name != "alpha.txt" {
		t.Fatalf("ListDirEntries(/vault) unexpected: exists=%v entries=%+v err=%v", exists, entries, err)
	}
	if err := cfg.Restore(ctx, "/vault/alpha.txt", restoreDir, false, false, SessionOptions{}); err != nil {
		t.Fatalf("Restore with absolute authorized path failed: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(restoreDir, "alpha.txt")); err != nil || string(got) != "hello-crypt" {
		t.Fatalf("restored crypt file mismatch: %q (%v)", got, err)
	}
	// Restore again with virtual path + overwrite=true
	if err := cfg.Restore(ctx, "/vault/alpha.txt", vRestore, false, true, SessionOptions{}); err != nil {
		t.Fatalf("Restore with virtual path failed: %v", err)
	}

	// 4. Consistency Check (`rclone check`): match, differ, missingOnDst, missingOnSrc
	if err := os.WriteFile(filepath.Join(localSrc, "same.txt"), []byte("identical"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDst, "same.txt"), []byte("identical"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localSrc, "diff.txt"), []byte("src-bytes"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDst, "diff.txt"), []byte("dst-BYTES"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDst, "extra_dst.txt"), []byte("only-dst"), 0644); err != nil {
		t.Fatal(err)
	}
	chk, err := cfg.Check(ctx, vSrc, vDst, CheckOptions{Download: true, Checkers: 2}, nil)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if chk.MatchedCount != 1 || chk.DifferCount != 1 || chk.MissingOnDstCount != 1 || chk.MissingOnSrcCount != 1 {
		t.Fatalf("unexpected check counts: %+v", chk)
	}
	if !slices.Contains(chk.Differ, "diff.txt") || !slices.Contains(chk.MissingOnDst, "alpha.txt") || !slices.Contains(chk.MissingOnSrc, "extra_dst.txt") {
		t.Fatalf("unexpected check file lists: %+v", chk)
	}

	// 5. Bisync (`rclone bisync`): 1st run auto-resyncs both ways, 2nd run incrementally syncs new files both ways
	biA := filepath.Join(base, "biA")
	biB := filepath.Join(base, "biB")
	workDir := filepath.Join(base, "bisync_work")
	_ = os.MkdirAll(biA, 0755)
	_ = os.MkdirAll(biB, 0755)
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", biA+":"+biB)
	vBiA, _ := virtualLocal(biA)
	vBiB, _ := virtualLocal(biB)

	_ = os.WriteFile(filepath.Join(biA, "from_a.txt"), []byte("AAA"), 0644)
	_ = os.WriteFile(filepath.Join(biB, "from_b.txt"), []byte("BBB"), 0644)

	res1, err := cfg.Bisync(ctx, vBiA, vBiB, BisyncOptions{WorkDir: workDir, Transfers: 2, Checkers: 2}, nil)
	if err != nil {
		t.Fatalf("Bisync run 1 failed: %v", err)
	}
	if !res1.Resynced {
		t.Fatal("expected run 1 to trigger initial resync")
	}
	if got, err := os.ReadFile(filepath.Join(biB, "from_a.txt")); err != nil || string(got) != "AAA" {
		t.Fatalf("from_a.txt not synced to biB: %q (%v)", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(biA, "from_b.txt")); err != nil || string(got) != "BBB" {
		t.Fatalf("from_b.txt not synced to biA: %q (%v)", got, err)
	}

	// Add new files on both sides and run 2nd incremental bisync (must NOT resync!)
	_ = os.WriteFile(filepath.Join(biA, "inc_a.txt"), []byte("INC-A"), 0644)
	_ = os.WriteFile(filepath.Join(biB, "inc_b.txt"), []byte("INC-B"), 0644)
	res2, err := cfg.Bisync(ctx, vBiA, vBiB, BisyncOptions{WorkDir: workDir, Transfers: 2, Checkers: 2}, nil)
	if err != nil {
		t.Fatalf("Bisync run 2 failed: %v", err)
	}
	if res2.Resynced {
		t.Fatal("expected run 2 to use existing listing state without resync")
	}
	if got, err := os.ReadFile(filepath.Join(biB, "inc_a.txt")); err != nil || string(got) != "INC-A" {
		t.Fatalf("inc_a.txt not synced to biB on run 2: %q (%v)", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(biA, "inc_b.txt")); err != nil || string(got) != "INC-B" {
		t.Fatalf("inc_b.txt not synced to biA on run 2: %q (%v)", got, err)
	}

	// Verify Version() returns non-empty version string
	ver, err := Version(ctx)
	if err != nil || ver == "" {
		t.Fatalf("Version() failed: %q (%v)", ver, err)
	}
}

func TestExtendedBackendsCombineAliasArchiveAndNewOps(t *testing.T) {
	// 1. Validate new backends, alias, archive, combine, and cycle detection
	cfg := Config{
		"yx": {
			"type":  "yandex",
			"token": `{"access_token":"tok","refresh_token":"ref"}`,
		},
		"kf": {
			"type":     "koofr",
			"endpoint": "https://app.koofr.net",
			"user":     "u@example.com",
			"password": "AAAAAAAAAAAAAAAAAAAAAA",
		},
		"sf": {
			"type": "seafile",
			"url":  "https://seafile.example.com",
			"user": "u@example.com",
			"pass": "AAAAAAAAAAAAAAAAAAAAAA",
		},
		"sj": {
			"type":         "storj",
			"access_grant": "grant123",
		},
		"qs": {
			"type":              "qingstor",
			"access_key_id":     "ak",
			"secret_access_key": "sk",
		},
		"oci": {
			"type":      "oracleobjectstorage",
			"namespace": "ns",
			"region":    "ap-tokyo-1",
		},
		"af": {
			"type":       "azurefiles",
			"account":    "acct",
			"key":        "key",
			"share_name": "share",
		},
		"ali": {
			"type":   "alias",
			"remote": "sj:bucket/sub",
		},
		"arc": {
			"type":   "archive",
			"remote": "sj:bucket/archive.zip",
		},
		"comb": {
			"type":      "combine",
			"upstreams": "koofr_dir=kf: yandex_dir=yx:backup",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected new backends config valid, got: %v", err)
	}
	if _, _, err := cfg.Pair("/sj/bucket/src", "/arc/inner"); err == nil {
		t.Fatal("expected archive backend to be rejected as read-only destination")
	}
	renamed, err := cfg.Rename("kf", "koofr_main")
	if err != nil {
		t.Fatalf("Rename kf failed: %v", err)
	}
	if !strings.Contains(renamed["comb"]["upstreams"], "koofr_dir=koofr_main:") {
		t.Fatalf("combine upstreams did not cascade rename: %q", renamed["comb"]["upstreams"])
	}
	cycle := Config{
		"a": {"type": "alias", "remote": "b:"},
		"b": {"type": "combine", "upstreams": "sub=a:"},
	}
	if err := cycle.Validate(); err == nil {
		t.Fatal("expected cycle across alias -> combine -> alias to fail validation")
	}

	// 2. Validate tuning & SSRF URL guards
	if err := ValidateRcloneSize("64M", false, "bad"); err != nil {
		t.Fatalf("ValidateRcloneSize valid failed: %v", err)
	}
	if err := ValidateRcloneSize("-1M", false, "bad"); err == nil {
		t.Fatal("expected negative size rejected")
	}
	if err := ValidateOrderBy("size,asc:modtime,desc"); err != nil {
		t.Fatalf("ValidateOrderBy valid failed: %v", err)
	}
	if err := ValidateOrderBy("invalid,asc"); err == nil {
		t.Fatal("expected invalid order-by field rejected")
	}
	if err := validateCopyURLTarget("http://127.0.0.1:8080/secret"); err == nil {
		t.Fatal("expected loopback copyurl rejected")
	}
	if err := validateCopyURLTarget("https://example.com/file.bin"); err != nil {
		t.Fatalf("expected valid https copyurl accepted, got: %v", err)
	}

	requireRclone(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	base := t.TempDir()
	srcDir := filepath.Join(base, "src")
	cryptBacking := filepath.Join(base, "crypt_backing")
	_ = os.MkdirAll(srcDir, 0755)
	_ = os.MkdirAll(cryptBacking, 0755)
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", srcDir+":"+cryptBacking)
	t.Setenv("TRIM_PKGTMP", "")
	vSrc, _ := virtualLocal(srcDir)

	_ = os.WriteFile(filepath.Join(srcDir, "small.txt"), []byte("hi"), 0644)
	_ = os.WriteFile(filepath.Join(srcDir, "big.bin"), []byte("0123456789abcdef"), 0644)

	rtCfg := Config{
		"vault": {
			"type":     "crypt",
			"remote":   cryptBacking,
			"password": "vault-password-123",
		},
	}
	if err := Prepare(ctx, nil, rtCfg, true); err != nil {
		t.Fatalf("Prepare crypt failed: %v", err)
	}
	if err := rtCfg.Copy(ctx, vSrc, "/vault", CopyOptions{Transfers: 2, Checkers: 2, MaxBacklog: 100, BufferSize: "16M"}, nil); err != nil {
		t.Fatalf("copy to crypt failed: %v", err)
	}

	// CryptCheck matches identical encrypted files without --download
	cc, err := rtCfg.Check(ctx, vSrc, "/vault", CheckOptions{CryptCheck: true, Checkers: 2}, nil)
	if err != nil {
		t.Fatalf("CryptCheck failed: %v", err)
	}
	if cc.Mode != "cryptcheck" || cc.MatchedCount != 2 || cc.DifferCount != 0 || cc.MissingOnDstCount != 0 {
		t.Fatalf("unexpected cryptcheck result: %+v", cc)
	}

	// Check with MinSize filter ignores small.txt (2 bytes)
	chkFiltered, err := rtCfg.Check(ctx, vSrc, "/vault", CheckOptions{CryptCheck: true, MinSize: 10, Checkers: 2}, nil)
	if err != nil {
		t.Fatalf("filtered CryptCheck failed: %v", err)
	}
	if chkFiltered.MatchedCount != 1 {
		t.Fatalf("expected 1 matched file with MinSize=10, got %+v", chkFiltered)
	}

	// Size & Dedupe list on crypt remote
	szLocal, err := rtCfg.Size(ctx, vSrc, SessionOptions{})
	if err != nil || szLocal.Count != 2 || szLocal.Bytes != 18 {
		t.Fatalf("Size(vSrc) unexpected: %+v (%v)", szLocal, err)
	}
	szVault, err := rtCfg.Size(ctx, "/vault", SessionOptions{})
	if err != nil || szVault.Count != 2 || szVault.Bytes != 18 {
		t.Fatalf("Size(/vault) unexpected: %+v (%v)", szVault, err)
	}
	dedupeRes, err := rtCfg.Dedupe(ctx, "/vault", DedupeOptions{Mode: "list", DryRun: true}, SessionOptions{})
	if err != nil || !dedupeRes.DryRun {
		t.Fatalf("Dedupe list failed: %+v (%v)", dedupeRes, err)
	}

	// Bisync pending resync marker triggers --resync
	workDir := filepath.Join(base, "bi_work")
	biA := filepath.Join(base, "biA")
	biB := filepath.Join(base, "biB")
	_ = os.MkdirAll(biA, 0755)
	_ = os.MkdirAll(biB, 0755)
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", biA+":"+biB)
	vBiA, _ := virtualLocal(biA)
	vBiB, _ := virtualLocal(biB)
	_ = os.WriteFile(filepath.Join(biA, "a.txt"), []byte("A"), 0644)
	if _, err := rtCfg.Bisync(ctx, vBiA, vBiB, BisyncOptions{WorkDir: workDir, Transfers: 2, Checkers: 2}, nil); err != nil {
		t.Fatalf("initial bisync failed: %v", err)
	}
	if err := MarkBisyncResyncPending(workDir); err != nil {
		t.Fatalf("MarkBisyncResyncPending failed: %v", err)
	}
	res2, err := rtCfg.Bisync(ctx, vBiA, vBiB, BisyncOptions{WorkDir: workDir, ResyncMode: "newer", Transfers: 2, Checkers: 2}, nil)
	if err != nil || !res2.Resynced {
		t.Fatalf("expected bisync with pending marker to resync, got %+v (%v)", res2, err)
	}
}
