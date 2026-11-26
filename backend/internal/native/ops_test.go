package native

import (
	"context"
	"net"
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

func TestCopyURLTargetBlocksReservedAddresses(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1/file",
		"http://[::1]/file",
		"http://100.64.0.1/file",
		"http://198.18.0.1/file",
		"http://[fd00::1]/file",
		"http://user:pass@example.com/file",
	} {
		if err := validateCopyURLTarget(raw); err == nil {
			t.Errorf("reserved or credential-bearing URL %q was accepted", raw)
		}
	}
}

func TestBlockedCopyURLIPRanges(t *testing.T) {
	for _, raw := range []string{
		"100.64.0.1", "198.18.0.1", "192.0.2.1", "198.51.100.1", "203.0.113.1", "fc00::1",
	} {
		if !isBlockedCopyURLIP(net.ParseIP(raw)) {
			t.Errorf("reserved address %s was not blocked", raw)
		}
	}
	if isBlockedCopyURLIP(net.ParseIP("8.8.8.8")) {
		t.Fatal("public address 8.8.8.8 was incorrectly blocked")
	}
}
