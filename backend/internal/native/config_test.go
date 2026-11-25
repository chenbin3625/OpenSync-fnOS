package native

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestAuthorizedPathsAndMountIdentity(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "disk")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	grants := filepath.Join(tmp, "native-roots")
	if err := os.WriteFile(grants, []byte(root), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRIM_PKGTMP", tmp)
	if AuthorizedCount() != 1 {
		t.Fatalf("expected 1 authorized root, got %d", AuthorizedCount())
	}
	original, e := virtualLocal(root)
	if e != nil {
		t.Fatal(e)
	}
	c := Config{}
	if e = os.Symlink(tmp, filepath.Join(root, "link")); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Resolve(original + "/link"); e == nil {
		t.Fatal("directory link must not be selectable")
	}
	if e = os.Rename(root, root+"-old"); e != nil {
		t.Fatal(e)
	}
	if e = os.Mkdir(root, 0700); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Resolve(original); e == nil {
		t.Fatal("replaced directory accepted")
	}
	if e = os.WriteFile(grants, nil, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = virtualLocal(root); e == nil {
		t.Fatal("revoked grant accepted")
	}
}

func TestConfigValidationAndPrepareEnhancements(t *testing.T) {
	// Schemeless S3 endpoints (aliyun OSS / tencent COS / B2 / R2 / host:port)
	for _, ep := range []string{
		"oss-cn-hangzhou.aliyuncs.com",
		"cos.ap-guangzhou.myqcloud.com",
		"127.0.0.1:9000",
		"https://s3.us-west-004.backblazeb2.com",
	} {
		c := Config{
			"oss": {
				"type":              "s3",
				"provider":          "Alibaba",
				"endpoint":          ep,
				"access_key_id":     "ak",
				"secret_access_key": "sk",
			},
		}
		if err := c.Validate(); err != nil {
			t.Fatalf("endpoint %q should be valid, got: %v", ep, err)
		}
	}
	for _, badEP := range []string{"ftp://example.com", "https://user:pass@example.com", "oss.example.com?foo=1", "oss.example.com:99999"} {
		c := Config{
			"oss": {
				"type":              "s3",
				"provider":          "Alibaba",
				"endpoint":          badEP,
				"access_key_id":     "ak",
				"secret_access_key": "sk",
			},
		}
		if err := c.Validate(); err == nil {
			t.Fatalf("bad endpoint %q should be rejected", badEP)
		}
	}

	// SFTP: shell_type=unix normalized to none, multiline known_hosts allowed, empty optional fields stripped
	genKnownHost := func(host string) string {
		t.Helper()
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		sshPub, err := ssh.NewPublicKey(pub)
		if err != nil {
			t.Fatal(err)
		}
		return knownhosts.Line([]string{host}, sshPub)
	}
	kh1 := genKnownHost("192.168.1.10")
	kh2 := genKnownHost("[192.168.1.10]:2222")
	sftpAdd := Config{
		"nas": {
			"type":        "sftp",
			"host":        "192.168.1.10",
			"port":        "   ",
			"user":        "backup",
			"key_pem":     "-----BEGIN TEST KEY-----\\nabc\\n-----END TEST KEY-----",
			"shell_type":  "unix",
			"known_hosts": "# comment\n" + kh1 + "\n" + kh2 + "\n",
		},
	}
	if err := Prepare(context.Background(), nil, sftpAdd, false); err != nil {
		t.Fatalf("Prepare sftp failed: %v", err)
	}
	if sftpAdd["nas"]["shell_type"] != "none" {
		t.Fatalf("expected shell_type=none, got %q", sftpAdd["nas"]["shell_type"])
	}
	if _, hasPort := sftpAdd["nas"]["port"]; hasPort {
		t.Fatalf("expected blank port to be stripped")
	}
	if err := sftpAdd.Validate(); err != nil {
		t.Fatalf("Validate sftp failed: %v", err)
	}

	// Actionable error messages for disallowed SFTP rclone.conf fields
	for field, wantSubstr := range map[string]string{
		"known_hosts_file": "known_hosts",
		"key_file":         "key_pem",
		"md5sum_command":   "安全限制",
	} {
		bad := Config{
			"nas": {
				"type":        "sftp",
				"host":        "192.168.1.10",
				"user":        "u",
				"known_hosts": kh1,
				"key_pem":     "PEM",
				field:         "/etc/passwd",
			},
		}
		err := bad.Validate()
		if err == nil || !strings.Contains(err.Error(), wantSubstr) {
			t.Fatalf("field %q: expected error containing %q, got %v", field, wantSubstr, err)
		}
	}

	// Step-by-step crypt referencing an already-saved local alias
	tmp := t.TempDir()
	root := filepath.Join(tmp, "vault-disk")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", root)
	t.Setenv("TRIM_PKGTMP", "")
	existing := Config{"disk": {"type": "local"}}
	addCrypt := Config{
		"vault": {
			"type":     "crypt",
			"remote":   "disk:" + root,
			"password": "AAAAAAAAAAAAAAAAAAAAAA", // 16+ bytes decoded base64url
		},
	}
	if err := Prepare(context.Background(), existing, addCrypt, false); err != nil {
		t.Fatalf("Prepare step-by-step crypt -> existing local failed: %v", err)
	}
	merged, err := existing.Merge(addCrypt)
	if err != nil {
		t.Fatalf("Merge step-by-step crypt failed: %v", err)
	}
	if !strings.HasPrefix(merged["vault"]["remote"], Local+":") {
		t.Fatalf("expected crypt remote rewritten to %s:, got %q", Local, merged["vault"]["remote"])
	}
}

func TestCryptEndpointConflictIsolation(t *testing.T) {
	c := Config{
		"s3": {
			"type":              "s3",
			"provider":          "Alibaba",
			"endpoint":          "oss-cn-hangzhou.aliyuncs.com",
			"access_key_id":     "ak",
			"secret_access_key": "sk",
		},
		"vault": {
			"type":     "crypt",
			"remote":   "s3:mybucket/enc",
			"password": "AAAAAAAAAAAAAAAAAAAAAA",
		},
		"vaultOtherKey": {
			"type":     "crypt",
			"remote":   "s3:mybucket/enc",
			"password": "BBBBBBBBBBBBBBBBBBBBBB",
		},
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	epPhotos, err := c.Resolve("/vault/photos")
	if err != nil {
		t.Fatal(err)
	}
	epDocs, err := c.Resolve("/vault/docs")
	if err != nil {
		t.Fatal(err)
	}
	epPhotosNested, err := c.Resolve("/vault/photos/2026")
	if err != nil {
		t.Fatal(err)
	}
	epPlainUnderEnc, err := c.Resolve("/s3/mybucket/enc/plain")
	if err != nil {
		t.Fatal(err)
	}
	epOtherVault, err := c.Resolve("/vaultOtherKey/docs")
	if err != nil {
		t.Fatal(err)
	}

	if EndpointsConflict(epPhotos, epDocs) {
		t.Fatal("sibling directories in the same crypt remote must not conflict")
	}
	if !EndpointsConflict(epPhotos, epPhotosNested) {
		t.Fatal("nested directories in the same crypt remote must conflict")
	}
	if !EndpointsConflict(epPhotos, epPlainUnderEnc) {
		t.Fatal("crypt target and plain target on the same backing tree must conflict")
	}
	if !EndpointsConflict(epPhotos, epOtherVault) {
		t.Fatal("two different crypt configs sharing the same backing tree must conflict")
	}
}

func TestListSortsLocalFirst(t *testing.T) {
	c := Config{
		"archive": {"type": "local"},
		"backup":  {"type": "local"},
	}
	items, err := c.List(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0]["name"] != Local {
		t.Fatalf("expected %q first in root listing, got %+v", Local, items)
	}
}

func TestEncryptedImport(t *testing.T) {
	bin := os.Getenv("OPENSYNC_RCLONE_BIN")
	if bin == "" {
		t.Skip("real rclone required")
	}
	f := filepath.Join(t.TempDir(), "rclone.conf")
	if e := os.WriteFile(f, []byte("[disk]\ntype = local\n"), 0600); e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(bin, "config", "encryption", "set", "--password-command", "echo example-password", "--config", f)
	if e := cmd.Run(); e != nil {
		t.Fatal(e)
	}
	content, e := os.ReadFile(f)
	if e != nil {
		t.Fatal(e)
	}
	c, e := ImportFile(context.Background(), string(content), "example-password")
	if e != nil || c["disk"]["type"] != "local" {
		t.Fatalf("encrypted import: %v", e)
	}
	if _, e = ImportFile(context.Background(), string(content), "wrong"); e == nil {
		t.Fatal("accepted incorrect password")
	}
}
