package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMappingStaysInsideAuthorizedDirectories(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	child := filepath.Join(root, "Photos")
	_ = os.Mkdir(child, 0700)
	if err := ValidateLocalPath(child, []string{root}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outside, root + "-other", filepath.Join(root, "..", "escape")} {
		if ValidateLocalPath(path, []string{root}) == nil {
			t.Fatalf("accepted unauthorized path %s", path)
		}
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err == nil && ValidateLocalPath(link, []string{root}) == nil {
		t.Fatal("accepted symlink escape")
	}
}

func TestMappingValidationRequiresEngineAndAbsoluteVirtualPath(t *testing.T) {
	root := t.TempDir()
	for _, m := range []Mapping{{Path: root}, {Path: root, AlistID: 1, VirtualPath: "relative"}, {Path: root, AlistID: 1, VirtualPath: "/a/../b"}} {
		if ValidateMapping(m, []string{root}) == nil {
			t.Fatalf("accepted %#v", m)
		}
	}
	if err := ValidateMapping(Mapping{Path: root, AlistID: 1, VirtualPath: "/Photos"}, []string{root}); err != nil {
		t.Fatal(err)
	}
}
