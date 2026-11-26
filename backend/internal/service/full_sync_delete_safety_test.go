package service

import (
	"fmt"
	"testing"
)

// filesUnder returns a snapshot directory listing of n plain files.
func filesUnder(prefix string, n int) FileListResult {
	items := make(FileListResult, n)
	for i := 0; i < n; i++ {
		items[fmt.Sprintf("%s%d.txt", prefix, i)] = FileMetadata{Size: 1}
	}
	return items
}

func TestUnsafeDeleteNamesRejected(t *testing.T) {
	for _, name := range []string{"", "/", ".", "..", "../", "a/b", "a/b/"} {
		if isSafeDeleteTargetName(name) {
			t.Fatalf("isSafeDeleteTargetName(%q) = true, want false", name)
		}
	}
	for _, name := range []string{"keep-working.txt", "sub/"} {
		if !isSafeDeleteTargetName(name) {
			t.Fatalf("isSafeDeleteTargetName(%q) = false, want true", name)
		}
	}
}

func TestSafetyValveWeighsDirectoryDeletesBySubtree(t *testing.T) {
	src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{
		"": {"keep.txt": {Size: 1}},
	}}
	dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{
		"":            {"keep.txt": {Size: 1}, "mount/": {}},
		"mount/":      {"deep/": {}},
		"mount/deep/": filesUnder("m", 150),
	}}
	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{"method": 1})
	if len(plan.extraDeletes) != 1 {
		t.Fatalf("extraDeletes = %#v, want the single mount/ root", plan.extraDeletes)
	}
	weight := plan.plannedDeleteWeight(plan.relocations(), plan.writeIndex())
	if _, unsafe := plan.unsafeDeleteVolume(weight); !unsafe {
		t.Fatalf("unsafeDeleteVolume(weight=%d) = false, want true for subtree deletion", weight)
	}
}

func TestSafetyValveAllowsSmallDirectoryDelete(t *testing.T) {
	src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{
		"":      {"keep/": {}},
		"keep/": filesUnder("k", 300),
	}}
	dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{
		"":      {"keep/": {}, "old/": {}},
		"keep/": filesUnder("k", 300),
		"old/":  filesUnder("o", 150),
	}}
	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{"method": 1})

	weight := plan.plannedDeleteWeight(plan.relocations(), plan.writeIndex())
	if reason, unsafe := plan.unsafeDeleteVolume(weight); unsafe {
		t.Fatalf("unsafeDeleteVolume(weight=%d) = true (%q), want false (151 of 452 entries is under ratio)", weight, reason)
	}
}

func TestSubtreeEntryCountIncludesNestedDirectories(t *testing.T) {
	snapshot := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{
		"":     {"a/": {}, "ab/": {}},
		"a/":   {"b/": {}, "x": {}},
		"a/b/": {"y": {}, "z": {}},
		"ab/":  {"w": {}},
	}}
	cases := map[string]int{"a/": 4, "a/b/": 2, "ab/": 1, "missing/": 0}
	for dir, want := range cases {
		if got := snapshot.subtreeEntryCount(dir); got != want {
			t.Errorf("subtreeEntryCount(%q) = %d, want %d", dir, got, want)
		}
	}
	if got := snapshot.deleteWeight(fullSyncDelete{name: "a/", subtree: "a/"}); got != 5 {
		t.Errorf("deleteWeight(a/) = %d, want 5 (the directory plus 4 entries)", got)
	}
	if got := snapshot.deleteWeight(fullSyncDelete{name: "x"}); got != 1 {
		t.Errorf("deleteWeight(file) = %d, want 1", got)
	}
}

func TestSafetyValveCoversTypeConflictDeletes(t *testing.T) {
	src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{
		"": {"media": {Size: 5}},
	}}
	dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{
		"":       {"media/": {}},
		"media/": filesUnder("p", 200),
	}}
	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{"method": 1})
	if len(plan.blockers) != 1 || len(plan.extraDeletes) != 0 {
		t.Fatalf("blockers=%#v extraDeletes=%#v, want one blocker only", plan.blockers, plan.extraDeletes)
	}
	weight := plan.plannedDeleteWeight(plan.relocations(), plan.writeIndex())
	if _, unsafe := plan.unsafeDeleteVolume(weight); !unsafe {
		t.Fatalf("unsafeDeleteVolume(weight=%d) = false, want true for large type-conflict blocker", weight)
	}
}

func TestFullSyncWriteIndexCollision(t *testing.T) {
	index := make(fullSyncWriteIndex)
	index.track("/dst/a/")
	index.track("/dst/")
	index.add("/dst/a/", "Photo.jpg")
	index.add("/dst/", "media")
	index.add("/dst/b/", "Photo.jpg")

	if written, ok := index.collision("/dst/a", "photo.jpg"); !ok || written != "Photo.jpg" {
		t.Fatalf("collision(photo.jpg) = %q %v, want Photo.jpg", written, ok)
	}
	if _, ok := index.collision("/dst/b/", "photo.jpg"); ok {
		t.Fatal("collision matched across directories")
	}
	if _, ok := index.collision("/dst/", "media/"); ok {
		t.Fatal("same-name type conflict treated as a collision")
	}
	if _, ok := index.collision("/dst/a/", "other.jpg"); ok {
		t.Fatal("unrelated name treated as a collision")
	}
	if foldedEntryName("A\u0301") != foldedEntryName("\u00e1") {
		t.Fatal("NFD and NFC forms fold differently")
	}
}

func TestFullSyncSuspiciousEmptySourceSubtree(t *testing.T) {
	t.Run("large subtree is protected", func(t *testing.T) {
		srcRoot := filesUnder("keep", 500)
		dstRoot := filesUnder("keep", 500)
		srcRoot["media/"] = FileMetadata{}
		dstRoot["media/"] = FileMetadata{}
		src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{
			"":       srcRoot,
			"media/": {},
		}}
		dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{
			"":       dstRoot,
			"media/": filesUnder("m", 150),
		}}
		plan := newFullSyncPlan(src, dst, nil)
		plan.build(map[string]interface{}{"method": 1})
		suspicious := plan.suspiciousEmptySourceDirs()
		if suspicious["/dst/media"] != 150 {
			t.Fatalf("suspiciousEmptySourceDirs = %#v, want /dst/media=150", suspicious)
		}
	})

	t.Run("small subtree still mirrors", func(t *testing.T) {
		srcRoot := filesUnder("keep", 500)
		dstRoot := filesUnder("keep", 500)
		srcRoot["media/"] = FileMetadata{}
		dstRoot["media/"] = FileMetadata{}
		src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{
			"":       srcRoot,
			"media/": {},
		}}
		dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{
			"":       dstRoot,
			"media/": filesUnder("m", 99),
		}}
		plan := newFullSyncPlan(src, dst, nil)
		plan.build(map[string]interface{}{"method": 1})
		if suspicious := plan.suspiciousEmptySourceDirs(); len(suspicious) != 0 {
			t.Fatalf("suspiciousEmptySourceDirs = %#v, want empty for small dir", suspicious)
		}
	})
}

func TestFullSyncPathBlockedHandlesRootPath(t *testing.T) {
	blocked := map[string]struct{}{"/": {}}
	for _, p := range []string{"/", "/file.txt", "/a/b/file.txt"} {
		if !fullSyncPathBlocked(p, blocked) {
			t.Fatalf(`fullSyncPathBlocked(%q, {"/": {}}) = false, want true`, p)
		}
	}
	if fullSyncPathBlocked("relative.txt", blocked) {
		t.Fatal(`fullSyncPathBlocked(relative.txt, {"/": {}}) = true, want false`)
	}
}

func TestFullSyncRootDestinationGuardsDeletesOnEmptySource(t *testing.T) {
	src := &fullSyncSnapshot{root: "/", dirs: map[string]FileListResult{"": {}}}
	dst := &fullSyncSnapshot{root: "/", dirs: map[string]FileListResult{
		"":     {"keep.txt": {Size: 1}, "sub/": {}},
		"sub/": {"nested.txt": {Size: 2}},
	}}
	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{"method": 1})

	suspicious := plan.suspiciousEmptySourceDirs()
	if _, ok := suspicious["/"]; !ok {
		t.Fatalf(`suspiciousEmptySourceDirs = %#v, want root "/" flagged`, suspicious)
	}
}
