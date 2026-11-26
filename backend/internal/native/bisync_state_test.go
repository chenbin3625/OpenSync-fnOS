package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBisyncRequiresCompleteBaselineAndExplicitRecovery(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pair.path1.lst"), []byte("listing"), 0600); err != nil {
		t.Fatal(err)
	}
	if BisyncStateAvailable(dir) {
		t.Fatal("a single listing is not a baseline")
	}
	_, err := (Config{}).Bisync(context.Background(), "/missing/a", "/missing/b", BisyncOptions{WorkDir: dir, RequireExistingState: true}, nil)
	if err == nil || !strings.Contains(err.Error(), "基线缺失") {
		t.Fatalf("baseline loss not blocked before I/O: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other.path2.lst"), []byte("listing"), 0600); err != nil {
		t.Fatal(err)
	}
	if BisyncStateAvailable(dir) {
		t.Fatal("unrelated listings must not form a pair")
	}
	if err := os.WriteFile(filepath.Join(dir, "pair.path2.lst"), []byte("listing"), 0600); err != nil {
		t.Fatal(err)
	}
	if !BisyncStateAvailable(dir) {
		t.Fatal("complete baseline not detected")
	}
	if err := os.Remove(filepath.Join(dir, "pair.path2.lst")); err != nil {
		t.Fatal(err)
	}
	if err := MarkBisyncResyncPending(dir); err != nil {
		t.Fatal(err)
	}
	if BisyncNeedsRebuild(dir) {
		t.Fatal("explicit recovery request was ignored")
	}
}
