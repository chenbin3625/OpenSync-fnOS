package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteFilesBatchLocalRootAndProtection(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", root)
	t.Setenv("TRIM_PKGTMP", "")

	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"a.txt", "b.txt", "keep.txt", "sub/c.txt"} {
		if err := os.WriteFile(filepath.Join(root, rel), []byte("hello"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	roots := AuthorizedRoots()
	if len(roots) != 1 {
		t.Fatalf("expected 1 authorized root, got %d", len(roots))
	}
	rootVirtual := "/" + Local + "/" + roots[0].ID

	cfg := Config{}
	errs, err := cfg.DeleteFilesBatch(context.Background(), rootVirtual, []string{
		"a.txt",
		"b.txt",
		"sub/c.txt",
		"missing.txt",
		"a.txt",          // duplicate should be deduplicated
		"../outside.txt", // traversal should be ignored
	}, SessionOptions{})
	if err != nil {
		t.Fatalf("DeleteFilesBatch error = %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("unexpected per-file errors: %+v", errs)
	}

	for _, deleted := range []string{"a.txt", "b.txt", "sub/c.txt"} {
		if _, statErr := os.Stat(filepath.Join(root, deleted)); !os.IsNotExist(statErr) {
			t.Fatalf("expected %s to be deleted, statErr=%v", deleted, statErr)
		}
	}
	if _, statErr := os.Stat(filepath.Join(root, "keep.txt")); statErr != nil {
		t.Fatalf("expected keep.txt to remain untouched, statErr=%v", statErr)
	}
}

func TestListTreeLocalStreamingAndEntries(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", root)
	t.Setenv("TRIM_PKGTMP", "")

	if err := os.MkdirAll(filepath.Join(root, "dir1", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "root.txt"), []byte("1234"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "dir1", "nested", "deep.txt"), []byte("123456"), 0o644); err != nil {
		t.Fatal(err)
	}

	roots := AuthorizedRoots()
	if len(roots) != 1 {
		t.Fatalf("expected 1 authorized root, got %d", len(roots))
	}
	rootVirtual := "/" + Local + "/" + roots[0].ID

	cfg := Config{}
	entries, exists, err := cfg.ListTree(context.Background(), rootVirtual, TreeOptions{})
	if err != nil || !exists {
		t.Fatalf("ListTree() exists=%v err=%v", exists, err)
	}
	found := map[string]TreeEntry{}
	for _, e := range entries {
		found[e.RelPath] = e
	}
	if e, ok := found["root.txt"]; !ok || e.IsDir || e.Size != 4 {
		t.Fatalf("unexpected root.txt entry: %+v (ok=%v)", e, ok)
	}
	if e, ok := found["dir1/nested/deep.txt"]; !ok || e.IsDir || e.Size != 6 {
		t.Fatalf("unexpected deep.txt entry: %+v (ok=%v)", e, ok)
	}
	if e, ok := found["dir1"]; !ok || !e.IsDir {
		t.Fatalf("unexpected dir1 entry: %+v (ok=%v)", e, ok)
	}
}
