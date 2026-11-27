package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSystemLocalPathSupportAndSecurityBoundaries(t *testing.T) {
	base := t.TempDir()
	allowedRoot := filepath.Join(base, "sync")
	subDir := filepath.Join(allowedRoot, "rime")
	nestedDir := filepath.Join(subDir, "build")
	outsideDir := filepath.Join(base, "secret-outside")
	for _, dir := range []string{nestedDir, outsideDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	sampleFile := filepath.Join(subDir, "note.txt")
	if err := os.WriteFile(sampleFile, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write sample file: %v", err)
	}

	t.Setenv("TRIM_PKGTMP", "")
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", allowedRoot)

	cfg := Config{}

	if !IsAuthorizedLocalPath(allowedRoot) {
		t.Fatalf("IsAuthorizedLocalPath(%q) = false, want true", allowedRoot)
	}
	if !IsAuthorizedLocalPath(subDir) {
		t.Fatalf("IsAuthorizedLocalPath(%q) = false, want true", subDir)
	}
	if IsAuthorizedLocalPath(outsideDir) {
		t.Fatalf("IsAuthorizedLocalPath(%q) = true, want false", outsideDir)
	}

	// Resolve real system path directly.
	epSys, err := cfg.Resolve(subDir)
	if err != nil {
		t.Fatalf("Resolve(%q) error = %v", subDir, err)
	}
	if !epSys.IsLocal || epSys.LocalPath != subDir {
		t.Fatalf("Resolve(%q) = %+v, want IsLocal=true LocalPath=%q", subDir, epSys, subDir)
	}

	// Legacy /本地/<rootID>/... resolves to the exact same BackingIdentity and conflicts properly.
	roots := AuthorizedRoots()
	if len(roots) != 1 {
		t.Fatalf("AuthorizedRoots() len = %d, want 1", len(roots))
	}
	legacyPath := "/" + Local + "/" + roots[0].ID + "/rime"
	epLegacy, err := cfg.Resolve(legacyPath)
	if err != nil {
		t.Fatalf("Resolve(legacy %q) error = %v", legacyPath, err)
	}
	if epSys.BackingIdentity != epLegacy.BackingIdentity {
		t.Fatalf("BackingIdentity mismatch: sys=%q legacy=%q", epSys.BackingIdentity, epLegacy.BackingIdentity)
	}
	if !EndpointsConflict(epSys, epLegacy) {
		t.Fatalf("expected system path and legacy /本地 path for same folder to conflict")
	}

	// List returns real system child paths when queried with a real system path.
	listed, err := cfg.List(context.Background(), allowedRoot)
	if err != nil {
		t.Fatalf("List(%q) error = %v", allowedRoot, err)
	}
	if len(listed) != 1 || listed[0]["name"] != "rime" || listed[0]["path"] != subDir {
		t.Fatalf("List(%q) = %+v, want [{name:rime path:%s}]", allowedRoot, listed, subDir)
	}

	// ListDirEntries and StatEntry work on real system paths too.
	entries, exists, err := cfg.ListDirEntries(context.Background(), subDir, SessionOptions{})
	if err != nil || !exists {
		t.Fatalf("ListDirEntries(%q) = (exists=%v, err=%v), want exists=true nil", subDir, exists, err)
	}
	if len(entries) != 2 {
		t.Fatalf("ListDirEntries(%q) len = %d, want 2", subDir, len(entries))
	}
	statEntry, statExists, err := cfg.StatEntry(context.Background(), sampleFile, true, SessionOptions{})
	if err != nil || !statExists || statEntry == nil || statEntry.MD5 == "" {
		t.Fatalf("StatEntry(%q) = (%+v, exists=%v, err=%v), want existing file with MD5", sampleFile, statEntry, statExists, err)
	}

	// Unauthorized paths and parent traversals must still be rejected.
	if _, err := cfg.Resolve(outsideDir); err == nil {
		t.Fatalf("Resolve(%q) = nil error, want rejection for unauthorized directory", outsideDir)
	}
	if _, err := cfg.Resolve(allowedRoot + "/../secret-outside"); err == nil {
		t.Fatalf("Resolve(traversal) = nil error, want rejection")
	}
}
