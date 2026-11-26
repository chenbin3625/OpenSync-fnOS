package service

import (
	"reflect"
	"sort"
	"testing"
)

func TestFullSyncPlanMatchesMovedFileByStrongFingerprint(t *testing.T) {
	plan := movedFileFullSyncPlan(FileMetadata{
		Size: 8 << 30,
		MD5:  "0123456789abcdef0123456789abcdef",
	})

	relocations := plan.relocations()
	if len(relocations) != 1 {
		t.Fatalf("relocations len = %d, want 1", len(relocations))
	}
	got := relocations[0]
	if got.source.dir != "/dst/old/archive/" || got.target.dstDir != "/dst/new/archive/" {
		t.Fatalf("relocation = %#v, want destination-side old/archive -> new/archive", got)
	}
	if got.source.name != "movie.mkv" || got.target.name != "movie.mkv" {
		t.Fatalf("relocation names = %q -> %q, want movie.mkv", got.source.name, got.target.name)
	}
}

func TestFullSyncPlanDoesNotInferMoveWithoutMD5(t *testing.T) {
	plan := movedFileFullSyncPlan(FileMetadata{Size: 8 << 30})

	if relocations := plan.relocations(); len(relocations) != 0 {
		t.Fatalf("relocations = %#v, want none when only file size matches", relocations)
	}
}

func movedFileFullSyncPlan(metadata FileMetadata) *fullSyncPlan {
	src := &fullSyncSnapshot{
		root: "/src/",
		dirs: map[string]FileListResult{
			"":             {"new/": {}},
			"new/":         {"archive/": {}},
			"new/archive/": {"movie.mkv": metadata},
		},
	}
	dst := &fullSyncSnapshot{
		root: "/dst/",
		dirs: map[string]FileListResult{
			"":             {"old/": {}},
			"old/":         {"archive/": {}},
			"old/archive/": {"movie.mkv": metadata},
		},
	}
	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{"method": 1})
	return plan
}

func TestFullSyncPlanKeepsSizeFilteredDestinationFile(t *testing.T) {
	small := FileMetadata{Size: 500 * 1024}
	src := &fullSyncSnapshot{
		root: "/src/",
		dirs: map[string]FileListResult{"": {"small.txt": small}},
	}
	dst := &fullSyncSnapshot{
		root: "/dst/",
		dirs: map[string]FileListResult{"": {"small.txt": small}},
	}
	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{
		"method":      1,
		"minFileSize": int64(1024 * 1024),
	})

	if len(plan.extraDeletes) != 0 {
		t.Fatalf("extraDeletes = %#v, want none: the file still exists at the source", plan.extraDeletes)
	}
	if len(plan.extraFiles) != 0 {
		t.Fatalf("extraFiles = %#v, want none", plan.extraFiles)
	}
	if len(plan.newFiles) != 0 || len(plan.changed) != 0 {
		t.Fatalf("newFiles=%#v changed=%#v, want none: the file is filtered out of copying", plan.newFiles, plan.changed)
	}
}

func TestFullSyncPlanStillDeletesUnmatchedDestinationFileWithSizeFilter(t *testing.T) {
	src := &fullSyncSnapshot{
		root: "/src/",
		dirs: map[string]FileListResult{"": {}},
	}
	dst := &fullSyncSnapshot{
		root: "/dst/",
		dirs: map[string]FileListResult{"": {"orphan.txt": {Size: 2 * 1024 * 1024}}},
	}
	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{
		"method":      1,
		"minFileSize": int64(1024 * 1024),
	})

	if len(plan.extraDeletes) != 1 {
		t.Fatalf("extraDeletes = %#v, want the orphaned destination file", plan.extraDeletes)
	}
}

func TestFullSyncPlanKeepsSizeFilteredDestinationDirectoryOfSameName(t *testing.T) {
	small := FileMetadata{Size: 500 * 1024}
	src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{
		"": {"small.bin": small},
	}}
	dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{
		"":           {"small.bin/": {}},
		"small.bin/": {"keep.txt": {Size: 1}},
	}}
	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{
		"method":      1,
		"minFileSize": int64(1024 * 1024),
	})

	if len(plan.extraDeletes) != 0 || len(plan.blockers) != 0 || len(plan.extraFiles) != 0 {
		t.Fatalf("extraDeletes=%#v blockers=%#v extraFiles=%#v, want none: the source entry is filtered out",
			plan.extraDeletes, plan.blockers, plan.extraFiles)
	}
	if len(plan.newFiles) != 0 || len(plan.changed) != 0 {
		t.Fatalf("newFiles=%#v changed=%#v, want none: the file is filtered out of copying", plan.newFiles, plan.changed)
	}
}

func planShape(plan *fullSyncPlan) []string {
	shape := make([]string, 0,
		len(plan.newDirs)+len(plan.newFiles)+len(plan.changed)+
			len(plan.extraFiles)+len(plan.extraDeletes)+len(plan.blockers)+len(plan.emptySrcDirs))
	for key, dir := range plan.newDirs {
		shape = append(shape, "dir:"+key+"->"+dir.srcDir+"|"+dir.dstDir)
	}
	for key := range plan.newFiles {
		shape = append(shape, "new:"+key)
	}
	for _, file := range plan.changed {
		shape = append(shape, "changed:"+file.target)
	}
	for key := range plan.extraFiles {
		shape = append(shape, "extra:"+key)
	}
	for key := range plan.extraDeletes {
		shape = append(shape, "delete:"+key)
	}
	for key := range plan.blockers {
		shape = append(shape, "block:"+key)
	}
	for _, dir := range plan.emptySrcDirs {
		shape = append(shape, "emptysrc:"+dir)
	}
	sort.Strings(shape)
	return shape
}

func branchyPlanSnapshots() (*fullSyncSnapshot, *fullSyncSnapshot) {
	changedSrc := FileMetadata{Size: 10, MD5: "0123456789abcdef0123456789abcdef"}
	changedDst := FileMetadata{Size: 11, MD5: "0123456789abcdef0123456789abcdef"}
	src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{
		"":     {"a/": {}, "b/": {}, "c.txt": {Size: 1}},
		"a/":   {"x/": {}, "same.txt": {Size: 1}, "changed.txt": changedSrc, "new-a.bin": {Size: 2}},
		"a/x/": {"deep.txt": {Size: 3}},
		"b/":   {"same.txt": {Size: 4}, "fresh.bin": {Size: 5}},
	}}
	dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{
		"":         {"a/": {}, "b/": {}, "stale-root.txt": {Size: 1}},
		"a/":       {"x/": {}, "same.txt": {Size: 1}, "changed.txt": changedDst, "old-a.bin": {Size: 2}},
		"a/x/":     {"deep.txt": {Size: 3}, "old-deep.bin": {Size: 6}},
		"b/":       {"same.txt": {Size: 4}, "old-b/": {}},
		"b/old-b/": {"stale.bin": {Size: 7}},
	}}
	return src, dst
}

func TestFullSyncPlanConcurrentBuildMatchesSequential(t *testing.T) {
	job := map[string]interface{}{"method": 1}
	src, dst := branchyPlanSnapshots()
	sequential := newFullSyncPlan(src, dst, nil)
	sequential.build(job)
	want := planShape(sequential)

	for i := 0; i < 20; i++ {
		gate := make(chan struct{}, 8)
		concurrent := newFullSyncPlan(src, dst, gate)
		concurrent.build(job)
		if err := concurrent.error(); err != nil {
			t.Fatalf("iteration %d: concurrent build failed: %v", i, err)
		}
		if len(gate) != 0 {
			t.Fatalf("iteration %d: %d branch slots leaked", i, len(gate))
		}
		if got := planShape(concurrent); !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d: concurrent plan differs from sequential\n got: %#v\nwant: %#v", i, got, want)
		}
	}
}

func TestFullSyncPlanBranchPanicIsContainedAndRecorded(t *testing.T) {
	src, dst := branchyPlanSnapshots()
	gate := make(chan struct{}, 8)

	oldHook := fullSyncPlanBranchHook
	fullSyncPlanBranchHook = func() { panic("branch exploded") }
	defer func() { fullSyncPlanBranchHook = oldHook }()

	plan := newFullSyncPlan(src, dst, gate)
	plan.build(map[string]interface{}{"method": 1})

	if err := plan.error(); err == nil {
		t.Fatal("branch panic was not recorded; a partial plan could be executed")
	}
	if len(gate) != 0 {
		t.Fatalf("%d branch slots leaked after panic", len(gate))
	}
}
