package native

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestConfigImportRestrictions(t *testing.T) {
	good := "[cloud]\ntype = s3\nprovider = Other\naccess_key_id = key\nsecret_access_key = secret\n"
	c, e := ParseINI(good)
	if e != nil || c.Validate() != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{good + "[cloud]\ntype = s3", good + "[bad]\ntype = alias\nremote = /", good + "credential_process = forbidden", good + "secret_access_key = again", "[untrusted]\ntype = webdav\nurl = https://example.org\nbearer_token_command = forbidden"} {
		parsed, e := ParseINI(bad)
		if e == nil && parsed.Validate() == nil {
			t.Fatal("accepted unsupported config")
		}
	}
}

func TestNativeTransfers(t *testing.T) {
	if os.Getenv("OPENSYNC_RCLONE_BIN") == "" {
		t.Skip("set OPENSYNC_RCLONE_BIN to the packaged rclone for integration tests")
	}
	base := t.TempDir()
	src := filepath.Join(base, "photos")
	disk := filepath.Join(base, "disk")
	for _, p := range []string{src, disk} {
		if e := os.Mkdir(p, 0700); e != nil {
			t.Fatal(e)
		}
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", src+":"+disk)
	t.Setenv("TRIM_PKGTMP", "")
	sv, _ := virtualLocal(src)
	dv, _ := virtualLocal(disk)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	for _, scenario := range []string{"local", "crypt", "webdav", "s3-Alibaba", "s3-TencentCOS", "s3-Cloudflare", "s3-Minio", "s3-Other", "sftp", "smb"} {
		t.Run(scenario, func(t *testing.T) {
			kind, provider, _ := strings.Cut(scenario, "-")
			if kind == "smb" && os.Getenv("OPENSYNC_TEST_SMB_SERVER") == "" {
				t.Skip("set OPENSYNC_TEST_SMB_SERVER to a local smbserver.py fixture")
			}
			c := Config{}
			dest := dv + "/" + kind
			backing := filepath.Join(disk, scenario)
			if e := os.MkdirAll(backing, 0700); e != nil {
				t.Fatal(e)
			}
			if kind == "crypt" {
				c["vault"] = map[string]string{"type": "crypt", "remote": backing, "password": "test-pass", "password2": "test-salt"}
				dest = "/vault"
			}
			if kind == "webdav" || kind == "s3" || kind == "sftp" || kind == "smb" {
				ln, e := net.Listen("tcp", "127.0.0.1:0")
				if e != nil {
					t.Fatal(e)
				}
				addr := ln.Addr().String()
				ln.Close()
				host, port, _ := net.SplitHostPort(addr)
				args := []string{"serve", kind, backing, "--addr", addr, "--config", os.DevNull}
				bin := os.Getenv("OPENSYNC_RCLONE_BIN")
				switch kind {
				case "smb":
					bin = "python3"
					args = []string{os.Getenv("OPENSYNC_TEST_SMB_SERVER"), "-smb2support", "-ip", host, "-port", port, "-username", "user", "-password", "testing", "backup", backing}
					c["remote"] = map[string]string{"type": "smb", "host": host, "port": port, "user": "user", "pass": "testing"}
				case "sftp":
					args = append(args, "--user", "user", "--pass", "testing")
					c["remote"] = map[string]string{"type": kind, "host": host, "port": port, "user": "user", "pass": "testing"}
				case "webdav":
					c["remote"] = map[string]string{"type": kind, "url": "http://" + addr, "vendor": "other"}
				case "s3":
					args = append(args, "--auth-key", "access,example")
					c["remote"] = map[string]string{"type": "s3", "provider": provider, "force_path_style": "true", "endpoint": "http://" + addr, "region": "us-east-1", "access_key_id": "access", "secret_access_key": "example"}
				}
				cmd := exec.CommandContext(ctx, bin, args...)
				cmd.Dir = t.TempDir()
				cmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "XDG_CONFIG_HOME="+t.TempDir())
				if e = cmd.Start(); e != nil {
					t.Fatal(e)
				}
				defer func() { cmd.Process.Kill(); cmd.Wait() }()
				for i := 0; i < 100; i++ {
					conn, e := net.DialTimeout("tcp", addr, 100*time.Millisecond)
					if e == nil {
						conn.Close()
						break
					}
					time.Sleep(20 * time.Millisecond)
				}
				dest = "/remote/backup"
			}
			if e := Prepare(ctx, nil, c, true); e != nil {
				t.Fatal(e)
			}
			if e := c.Validate(); e != nil {
				t.Fatal(e)
			}
			file := filepath.Join(src, "照片.txt")
			write := func(data string, ts time.Time) {
				t.Helper()
				if e := os.WriteFile(file, []byte(data), 0600); e != nil {
					t.Fatal(e)
				}
				if e := os.Chtimes(file, ts, ts); e != nil {
					t.Fatal(e)
				}
			}
			stamp := time.Now().Add(-time.Hour)
			write("version1", stamp)
			run := func(dry bool) int {
				t.Helper()
				count := 0
				e := c.Copy(ctx, sv, dest, CopyOptions{DryRun: dry, Transfers: 2, Checkers: 2}, func(v Event) {
					if v.File != "" {
						count++
					}
				})
				if e != nil {
					t.Fatal(e)
				}
				return count
			}
			if n := run(true); n != 1 {
				t.Fatalf("preview %d", n)
			}
			if n := run(false); n != 1 {
				t.Fatalf("first %d", n)
			}
			if n := run(false); n != 0 {
				t.Fatalf("repeat %d", n)
			}
			write("version2", stamp.Add(time.Minute))
			if n := run(false); n != 1 {
				t.Fatalf("same-size update %d", n)
			}
			if e := os.Remove(file); e != nil {
				t.Fatal(e)
			}
			if n := run(false); n != 0 {
				t.Fatalf("source deletion %d", n)
			}
			if kind == "local" {
				data, e := os.ReadFile(filepath.Join(backing, "照片.txt"))
				if e != nil || string(data) != "version2" {
					t.Fatalf("local content: %s, %v", data, e)
				}
				return
			}
			n, sub := parts(dest)
			s, e := c.Session(n, false, false)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			cmd, e := s.Cmd(ctx, "cat", "--", n+":"+strings.Trim(sub+"/照片.txt", "/"))
			if e != nil {
				t.Fatal(e)
			}
			data, e := cmd.Output()
			if e != nil || string(data) != "version2" {
				t.Fatalf("restore: %s %v", data, e)
			}
			if kind == "crypt" {
				entries, e := os.ReadDir(backing)
				if e != nil || len(entries) != 1 || entries[0].Name() == "照片.txt" {
					t.Fatal("filename encryption")
				}
				data, e = os.ReadFile(filepath.Join(backing, entries[0].Name()))
				if e != nil || strings.Contains(string(data), "version2") {
					t.Fatal("content encryption")
				}
			}
		})
	}
}

func TestLocalCopyToNonexistentSubdirAndPermissions(t *testing.T) {
	if os.Getenv("OPENSYNC_RCLONE_BIN") == "" {
		t.Skip("real rclone required")
	}
	// Simulate fnOS main script's restrictive umask 077.
	oldMask := syscall.Umask(0o077)
	defer syscall.Umask(oldMask)

	base := t.TempDir()
	src := filepath.Join(base, "src")
	dstRoot := filepath.Join(base, "dst")
	if err := os.MkdirAll(filepath.Join(src, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "top.txt"), []byte("top-content"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "nested", "deep.txt"), []byte("deep-content"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", src+":"+dstRoot)
	t.Setenv("TRIM_PKGTMP", "")
	sv, err := virtualLocal(src)
	if err != nil {
		t.Fatal(err)
	}
	dv, err := virtualLocal(dstRoot)
	if err != nil {
		t.Fatal(err)
	}

	// Destination subdirectory does NOT exist yet (matches multi-src derived
	// dirs and manually typed new local target dirs).
	targetVirtual := dv + "/brand-new/sub-target"
	c := Config{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var copied int
	var sawScanDone bool
	if err := c.Copy(ctx, sv, targetVirtual, CopyOptions{Transfers: 2, Checkers: 2}, func(v Event) {
		if v.ScanDone {
			sawScanDone = true
		}
		if v.File != "" && !v.Failed {
			copied++
		}
	}); err != nil {
		t.Fatalf("copy to nonexistent local subdir failed: %v", err)
	}
	if copied != 2 || !sawScanDone {
		t.Fatalf("expected 2 copied files and ScanDone=true, got copied=%d sawScanDone=%v", copied, sawScanDone)
	}

	outDir := filepath.Join(dstRoot, "brand-new", "sub-target")
	dirStat, err := os.Stat(outDir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirStat.Mode().Perm(); perm&0o044 == 0 {
		t.Fatalf("expected newly created backup dir to be readable despite umask 077, got %04o", perm)
	}
	fileStat, err := os.Stat(filepath.Join(outDir, "top.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fileStat.Mode().Perm(); perm&0o044 == 0 {
		t.Fatalf("expected backed up file to be readable despite umask 077, got %04o", perm)
	}
}

func TestCompareMode2NativeChecksumLocalAndS3(t *testing.T) {
	if os.Getenv("OPENSYNC_RCLONE_BIN") == "" {
		t.Skip("real rclone required")
	}
	base := t.TempDir()
	src := filepath.Join(base, "src")
	dstRoot := filepath.Join(base, "dst")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstRoot, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", src+":"+dstRoot)
	t.Setenv("TRIM_PKGTMP", "")
	sv, _ := virtualLocal(src)
	dv, _ := virtualLocal(dstRoot)

	stamp := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	srcFile := filepath.Join(src, "doc.txt")
	if err := os.WriteFile(srcFile, []byte("aaaa1111"), 0644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(srcFile, stamp, stamp)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := Config{}
	dest := dv + "/csum"

	copyCount := func() int {
		t.Helper()
		n := 0
		if err := c.Copy(ctx, sv, dest, CopyOptions{Transfers: 2, Checkers: 2, Compare: 2}, func(v Event) {
			if v.File != "" && !v.Failed {
				n++
			}
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if n := copyCount(); n != 1 {
		t.Fatalf("initial copy expected 1, got %d", n)
	}
	// Identical content with different mtime must NOT re-copy under Compare==2.
	_ = os.Chtimes(srcFile, stamp.Add(10*time.Minute), stamp.Add(10*time.Minute))
	if n := copyCount(); n != 0 {
		t.Fatalf("same content with different mtime should not recopy under Compare==2, got %d", n)
	}
	// Same size & same mtime but DIFFERENT content MUST re-copy via native MD5 checksum.
	if err := os.WriteFile(srcFile, []byte("bbbb2222"), 0644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(srcFile, stamp, stamp)
	if n := copyCount(); n != 1 {
		t.Fatalf("different content with same size+mtime must recopy under Compare==2, got %d", n)
	}
	got, err := os.ReadFile(filepath.Join(dstRoot, "csum", "doc.txt"))
	if err != nil || string(got) != "bbbb2222" {
		t.Fatalf("expected updated content bbbb2222, got %q (%v)", string(got), err)
	}

	// Verify >10 MiB file also succeeds under Compare==2 (nextcloud_chunk_size=0 avoids chunked upload endpoints).
	big := make([]byte, 11<<20)
	big[0] = 'A'
	big[len(big)-1] = 'Z'
	if err := os.WriteFile(filepath.Join(src, "large.bin"), big, 0644); err != nil {
		t.Fatal(err)
	}
	if n := copyCount(); n != 1 {
		t.Fatalf("expected large.bin copy count 1, got %d", n)
	}
	if n := copyCount(); n != 0 {
		t.Fatalf("expected repeat large.bin copy count 0, got %d", n)
	}

	// Verify Compare==2 native MD5 checksum against S3 (rclone serve s3) as well.
	s3Backing := filepath.Join(base, "s3-backing")
	if err := os.MkdirAll(s3Backing, 0755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	srv := exec.CommandContext(ctx, os.Getenv("OPENSYNC_RCLONE_BIN"), "serve", "s3", s3Backing, "--addr", addr, "--auth-key", "access,example", "--config", os.DevNull)
	srv.Dir = t.TempDir()
	srv.Env = append(os.Environ(), "HOME="+t.TempDir(), "XDG_CONFIG_HOME="+t.TempDir())
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Process.Kill(); _ = srv.Wait() }()
	for i := 0; i < 100; i++ {
		if conn, dialErr := net.DialTimeout("tcp", addr, 100*time.Millisecond); dialErr == nil {
			conn.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	s3Src := filepath.Join(base, "s3-src")
	if err := os.MkdirAll(s3Src, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", src+":"+dstRoot+":"+s3Src)
	s3sv, err := virtualLocal(s3Src)
	if err != nil {
		t.Fatal(err)
	}
	s3File := filepath.Join(s3Src, "obj.txt")
	if err := os.WriteFile(s3File, []byte("1111aaaa"), 0644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(s3File, stamp, stamp)
	s3Cfg := Config{
		"s3": {
			"type":              "s3",
			"provider":          "Other",
			"force_path_style":  "true",
			"endpoint":          "http://" + addr,
			"region":            "us-east-1",
			"access_key_id":     "access",
			"secret_access_key": "example",
		},
	}
	s3Copy := func() int {
		t.Helper()
		n := 0
		if err := s3Cfg.Copy(ctx, s3sv, "/s3/bucket", CopyOptions{Transfers: 2, Checkers: 2, Compare: 2}, func(v Event) {
			if v.File != "" && !v.Failed {
				n++
			}
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := s3Copy(); n != 1 {
		t.Fatalf("S3 initial Compare==2 expected 1, got %d", n)
	}
	// Same size & same mtime, different content -> MD5 mismatch triggers re-upload.
	if err := os.WriteFile(s3File, []byte("2222bbbb"), 0644); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(s3File, stamp, stamp)
	if n := s3Copy(); n != 1 {
		t.Fatalf("S3 different content same size+mtime expected 1, got %d", n)
	}
	if n := s3Copy(); n != 0 {
		t.Fatalf("S3 repeat Compare==2 expected 0, got %d", n)
	}
}

func TestNativeCopyFailedEventCarriesSizeAndReason(t *testing.T) {
	if os.Getenv("OPENSYNC_RCLONE_BIN") == "" {
		t.Skip("real rclone required")
	}
	base := t.TempDir()
	src := filepath.Join(base, "src")
	dstRoot := filepath.Join(base, "dst")
	if err := os.MkdirAll(filepath.Join(src, "blocked"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstRoot, 0755); err != nil {
		t.Fatal(err)
	}
	payload := "1234567890"
	if err := os.WriteFile(filepath.Join(src, "blocked", "f.txt"), []byte(payload), 0644); err != nil {
		t.Fatal(err)
	}
	// Create a regular file at destination where a directory is needed so copy fails.
	if err := os.WriteFile(filepath.Join(dstRoot, "blocked"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", src+":"+dstRoot)
	t.Setenv("TRIM_PKGTMP", "")
	sv, _ := virtualLocal(src)
	dv, _ := virtualLocal(dstRoot)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var failedEvent *Event
	err := Config{}.Copy(ctx, sv, dv, CopyOptions{Transfers: 1, Checkers: 1, Retries: 0}, func(v Event) {
		if v.Failed {
			cp := v
			failedEvent = &cp
		}
	})
	if err == nil {
		t.Fatal("expected copy failure")
	}
	if failedEvent == nil {
		t.Fatal("expected a failed file event")
	}
	if failedEvent.Size != int64(len(payload)) {
		t.Fatalf("expected failed file size %d, got %d", len(payload), failedEvent.Size)
	}
	if strings.TrimSpace(failedEvent.Error) == "" {
		t.Fatal("expected non-empty error detail on failed event")
	}
	if strings.Contains(failedEvent.Error, "127.0.0.1:") {
		t.Fatalf("internal loopback URL leaked in error: %s", failedEvent.Error)
	}
}
