package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRootDeleteAndMoveRejected(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TRIM_PKGTMP", "")
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", root)
	local, err := virtualLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "preserved.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		"dav1":    {"type": "webdav", "url": "https://dav1.example.com"},
		"dav2":    {"type": "webdav", "url": "https://dav2.example.com"},
		"wrapped": {"type": "alias", "remote": "dav1:root"},
	}
	ctx := context.Background()
	for _, target := range []string{"/dav1", "/dav1/", "/wrapped", local, local + "/"} {
		for _, isDir := range []bool{true, false} {
			if err := cfg.DeletePath(ctx, target, isDir, SessionOptions{}); err == nil || !strings.Contains(err.Error(), "存储根目录") {
				t.Fatalf("expected root delete refusal for %q, got %v", target, err)
			}
			if err := cfg.MovePath(ctx, target, "/dav2/sub", isDir, SessionOptions{}); err == nil || !strings.Contains(err.Error(), "存储根目录") {
				t.Fatalf("expected source root move refusal for %q, got %v", target, err)
			}
			if err := cfg.MovePath(ctx, "/dav2/sub", target, isDir, SessionOptions{}); err == nil || !strings.Contains(err.Error(), "存储根目录") {
				t.Fatalf("expected destination root move refusal for %q, got %v", target, err)
			}
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "preserved.txt")); err != nil || string(data) != "keep" {
		t.Fatalf("root contents changed: %q, %v", data, err)
	}
}

func TestDeleteFileRefusesDirectoryReplacement(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TRIM_PKGTMP", "")
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", root)
	local, err := virtualLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "replaced")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(child, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{}
	target := local + "/replaced"
	if err := cfg.DeletePath(context.Background(), target, false, SessionOptions{}); err == nil {
		t.Fatal("file deletion accepted a replacement directory")
	}
	if err := cfg.RemoveEmptyDir(context.Background(), target, SessionOptions{}); err == nil {
		t.Fatal("empty directory removal accepted a non-empty directory")
	}
	if got, err := os.ReadFile(child); err != nil || string(got) != "keep" {
		t.Fatalf("replacement directory contents changed: %q (%v)", got, err)
	}
}
