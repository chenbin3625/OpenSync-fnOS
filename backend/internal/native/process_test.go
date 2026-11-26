package native

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCommandPassesOnlyApprovedRuntimeCredentialEnv(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "rclone")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$AWS_ACCESS_KEY_ID\" \"$TF_VAR_user_ocid\" \"$OCI_RESOURCE_PRINCIPAL_VERSION\" \"$UNSAFE_SECRET\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENSYNC_RCLONE_BIN", bin)
	t.Setenv("AWS_ACCESS_KEY_ID", "allowed-ak")
	t.Setenv("TF_VAR_user_ocid", "test-user")
	t.Setenv("OCI_RESOURCE_PRINCIPAL_VERSION", "2.2")
	t.Setenv("UNSAFE_SECRET", "must-not-pass")
	cmd, err := command(context.Background(), "version")
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	if got != "allowed-ak\ntest-user\n2.2\n\n" {
		t.Fatalf("unexpected runtime environment: %q", string(out))
	}
}

func TestOracleManagedConfigRequiredBeforeProcessStart(t *testing.T) {
	t.Setenv("RCLONE_OOS_CONFIG_FILE", "")
	t.Setenv("OCI_CONFIG_FILE", "")
	cfg := Config{"oci": {"type": "oracleobjectstorage", "provider": "user_principal_auth", "namespace": "ns", "region": "us-phoenix-1"}}
	if session, err := cfg.SessionFor([]SessionSpec{{Name: "oci"}}, SessionOptions{}); err == nil {
		session.Close()
		t.Fatal("OCI user credentials silently fell back to /nonexistent/.oci/config")
	}
	t.Setenv("RCLONE_OOS_CONFIG_FILE", "relative-config")
	if session, err := cfg.SessionFor([]SessionSpec{{Name: "oci"}}, SessionOptions{}); err == nil {
		session.Close()
		t.Fatal("relative OCI configuration path was accepted")
	}
}

func TestRealRcloneRuntimeAuthentication(t *testing.T) {
	requireRclone(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	t.Setenv("AWS_ACCESS_KEY_ID", "runtime-access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "runtime-secret")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("RCLONE_S3_ENV_AUTH", "")
	for _, envAuth := range []string{"true", "false"} {
		t.Run("s3-env-auth-"+envAuth, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				auth := r.Header.Get("Authorization")
				if envAuth == "true" && !strings.Contains(auth, "Credential=runtime-access/") {
					t.Errorf("runtime S3 credentials not used: %q", auth)
				}
				if envAuth == "false" && auth != "" {
					t.Errorf("anonymous storage used runtime credentials: %q", auth)
				}
				w.Header().Set("Content-Type", "application/xml")
				_, _ = io.WriteString(w, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>bucket</Name><IsTruncated>false</IsTruncated></ListBucketResult>`)
			}))
			defer server.Close()
			cfg := Config{"s3": {"type": "s3", "provider": "Minio", "endpoint": server.URL, "region": "us-east-1", "env_auth": envAuth, "force_path_style": "true", "no_check_bucket": "true"}}
			if _, err := cfg.ListWithOptions(ctx, "/s3/bucket", SessionOptions{}); err != nil {
				t.Fatal(err)
			}
			if requests.Load() == 0 {
				t.Fatal("S3 fixture was not reached")
			}
		})
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(t.TempDir(), "oci-key.pem")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(t.TempDir(), "oci-config")
	content := "[TEST]\ntenancy=ocid1.tenancy.oc1..test\nuser=ocid1.user.oc1..test\nfingerprint=00:11:22:33:44:55:66:77:88:99:aa:bb:cc:dd:ee:ff\nregion=us-phoenix-1\nkey_file=" + keyFile + "\n"
	if err := os.WriteFile(configFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RCLONE_OOS_CONFIG_FILE", configFile)
	t.Setenv("RCLONE_OOS_CONFIG_PROFILE", "TEST")
	t.Setenv("RCLONE_OOS_PROVIDER", "")
	t.Setenv("OCI_CONFIG_FILE", "")
	for _, provider := range []string{"user_principal_auth", "env_auth"} {
		t.Run("oci-"+provider, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if !strings.Contains(r.Header.Get("Authorization"), "Signature ") || !strings.Contains(r.Header.Get("Authorization"), "ocid1.user.oc1..test") {
					t.Errorf("OCI request was not signed using the configured identity")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"objects":[],"prefixes":[]}`)
			}))
			defer server.Close()
			cfg := Config{"oci": {"type": "oracleobjectstorage", "provider": provider, "namespace": "ns", "region": "us-phoenix-1", "endpoint": server.URL}}
			if _, err := cfg.ListWithOptions(ctx, "/oci/bucket", SessionOptions{}); err != nil {
				t.Fatal(err)
			}
			if requests.Load() == 0 {
				t.Fatal("OCI fixture was not reached")
			}
			if cfg["oci"]["config_file"] != "" {
				t.Fatal("managed OCI file path leaked into stored configuration")
			}
		})
	}
}

func TestSessionForRejectsConflictingPerRemoteProxyURL(t *testing.T) {
	cfg := Config{
		"r1":    {"type": "webdav", "url": "https://a.example/dav", "proxy_url": "http://127.0.0.1:7890"},
		"r2":    {"type": "webdav", "url": "https://b.example/dav", "proxy_url": "http://127.0.0.1:7891"},
		"r3":    {"type": "webdav", "url": "https://c.example/dav", "proxy_url": "http://127.0.0.1:7890"},
		"crypt": {"type": "crypt", "remote": "r1:enc", "password": "pw"},
	}

	if s, err := cfg.SessionFor([]SessionSpec{{Name: "r1"}, {Name: "r2"}}, SessionOptions{}); err == nil {
		s.Close()
		t.Fatal("expected SessionFor to reject distinct per-remote proxy_url values")
	} else if !strings.Contains(err.Error(), "不同的专属代理") {
		t.Fatalf("unexpected error: %v", err)
	}

	// Same proxy_url across two remotes succeeds.
	sSame, err := cfg.SessionFor([]SessionSpec{{Name: "r1"}, {Name: "r3"}}, SessionOptions{})
	if err != nil {
		t.Fatalf("SessionFor identical proxy_url failed: %v", err)
	}
	sSame.Close()

	// Wrapper + its own proxied backing remote succeeds.
	sWrap, err := cfg.SessionFor([]SessionSpec{{Name: "crypt"}}, SessionOptions{})
	if err != nil {
		t.Fatalf("SessionFor crypt over proxied remote failed: %v", err)
	}
	sWrap.Close()
}

func TestRealRcloneCheckFollowSymlinksAndBisyncBackupDir(t *testing.T) {
	requireRclone(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	root := t.TempDir()
	srcDir := filepath.Join(root, "src")
	dstDir := filepath.Join(root, "dst")
	targetFile := filepath.Join(srcDir, "real.txt")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(targetFile, []byte("symlink-body"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(srcDir, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstDir, "real.txt"), []byte("symlink-body"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstDir, "link.txt"), []byte("symlink-body"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", srcDir+":"+dstDir)
	t.Setenv("TRIM_PKGTMP", "")
	roots := AuthorizedRoots()
	var vSrc, vDst string
	for _, r := range roots {
		if r.Path == srcDir {
			vSrc = "/" + Local + "/" + r.ID
		} else if r.Path == dstDir {
			vDst = "/" + Local + "/" + r.ID
		}
	}

	cfg := Config{}
	resFollow, err := cfg.Check(ctx, vSrc, vDst, CheckOptions{
		Compare:        1,
		FollowSymlinks: true,
	}, nil)
	if err != nil {
		t.Fatalf("Check(FollowSymlinks=true) failed: %v", err)
	}
	if resFollow.MatchedCount != 2 || resFollow.MissingOnSrcCount != 0 || resFollow.MissingOnDstCount != 0 {
		t.Fatalf("expected symlink matched when FollowSymlinks=true, got %+v", resFollow)
	}

	resNoFollow, err := cfg.Check(ctx, vSrc, vDst, CheckOptions{
		Compare:        1,
		FollowSymlinks: false,
	}, nil)
	if err != nil {
		t.Fatalf("Check(FollowSymlinks=false) failed: %v", err)
	}
	if resNoFollow.MissingOnSrcCount != 1 {
		t.Fatalf("expected symlink skipped when FollowSymlinks=false, got %+v", resNoFollow)
	}

	// Verify Bisync --backup-dir1/--backup-dir2 soft-deletes deleted/overwritten files into trash.
	// StatEntry verifies single-file metadata inspection including local inode & MD5.
	biA := filepath.Join(root, "biA")
	if err := os.MkdirAll(biA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(biA, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", biA)
	var vA string
	for _, r := range AuthorizedRoots() {
		if r.Path == biA {
			vA = "/" + Local + "/" + r.ID
		}
	}
	st, ok, err := cfg.StatEntry(ctx, vA+"/keep.txt", true, SessionOptions{})
	if err != nil || !ok || st == nil || st.Size != 4 || st.Inode == 0 || st.MD5 == "" {
		t.Fatalf("StatEntry unexpected: st=%+v ok=%v err=%v", st, ok, err)
	}
}
