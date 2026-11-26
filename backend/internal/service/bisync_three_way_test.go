package service

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opensync/internal/mapper"
	"opensync/internal/native"
)

func setupBisyncThreeWayDB(t *testing.T) func() {
	t.Helper()
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	testDB.SetMaxOpenConns(1)
	restore := mapper.SetDBForTest(testDB)
	mapper.InitSQL()
	return func() {
		restore()
		_ = testDB.Close()
	}
}

func TestBuildBisyncPlanTruthTableAndEdgeCases(t *testing.T) {
	now := time.Unix(1764150000, 0)
	capsA := BisyncEndpointCaps{Side: "A", Label: "本地", IsLocal: true, SupportsMD5: true, Endpoint: native.Endpoint{Backend: "local", IsLocal: true}}
	capsB := BisyncEndpointCaps{Side: "B", Label: "云端", IsLocal: false, SupportsMD5: true, CaseInsensitive: true, Endpoint: native.Endpoint{Backend: "onedrive"}}

	base := map[string]mapper.BisyncEntry{
		"unchanged.txt":      {JobID: 1, RelPath: "unchanged.txt", State: mapper.BisyncStateSynced, ASize: 10, AModTime: 100, BSize: 10, BModTime: 100},
		"edit_a.txt":         {JobID: 1, RelPath: "edit_a.txt", State: mapper.BisyncStateSynced, ASize: 10, AModTime: 100, BSize: 10, BModTime: 100},
		"edit_b.txt":         {JobID: 1, RelPath: "edit_b.txt", State: mapper.BisyncStateSynced, ASize: 10, AModTime: 100, BSize: 10, BModTime: 100},
		"del_a.txt":          {JobID: 1, RelPath: "del_a.txt", State: mapper.BisyncStateSynced, ASize: 10, AModTime: 100, BSize: 10, BModTime: 100},
		"del_b.txt":          {JobID: 1, RelPath: "del_b.txt", State: mapper.BisyncStateSynced, ASize: 10, AModTime: 100, BSize: 10, BModTime: 100},
		"both_del.txt":       {JobID: 1, RelPath: "both_del.txt", State: mapper.BisyncStateSynced, ASize: 10, AModTime: 100, BSize: 10, BModTime: 100},
		"edit_a_del_b.txt":   {JobID: 1, RelPath: "edit_a_del_b.txt", State: mapper.BisyncStateSynced, ASize: 10, AModTime: 100, BSize: 10, BModTime: 100},
		"del_a_edit_b.txt":   {JobID: 1, RelPath: "del_a_edit_b.txt", State: mapper.BisyncStateSynced, ASize: 10, AModTime: 100, BSize: 10, BModTime: 100},
		"both_edit.txt":      {JobID: 1, RelPath: "both_edit.txt", State: mapper.BisyncStateSynced, ASize: 10, AModTime: 100, BSize: 10, BModTime: 100},
		"newly_excluded.tmp": {JobID: 1, RelPath: "newly_excluded.tmp", State: mapper.BisyncStateSynced, ASize: 5, AModTime: 100, BSize: 5, BModTime: 100},
		"old_local_name.txt": {JobID: 1, RelPath: "old_local_name.txt", State: mapper.BisyncStateSynced, ASize: 42, AModTime: 100, AInode: 7777, BSize: 42, BModTime: 100},
		"old_rem_name.txt":   {JobID: 1, RelPath: "old_rem_name.txt", State: mapper.BisyncStateSynced, ASize: 88, AModTime: 100, BSize: 88, BModTime: 100, BObjectID: "obj-99"},
	}

	snapA := map[string]BisyncNode{
		"adopt.txt":          {RelPath: "adopt.txt", Size: 20, ModTime: 200, Hash: "aaa"},
		"new_a.txt":          {RelPath: "new_a.txt", Size: 15, ModTime: 200},
		"unchanged.txt":      {RelPath: "unchanged.txt", Size: 10, ModTime: 100},
		"edit_a.txt":         {RelPath: "edit_a.txt", Size: 12, ModTime: 250},
		"edit_b.txt":         {RelPath: "edit_b.txt", Size: 10, ModTime: 100},
		"del_b.txt":          {RelPath: "del_b.txt", Size: 10, ModTime: 100},
		"edit_a_del_b.txt":   {RelPath: "edit_a_del_b.txt", Size: 99, ModTime: 300},
		"both_edit.txt":      {RelPath: "both_edit.txt", Size: 11, ModTime: 300},
		"new_local_name.txt": {RelPath: "new_local_name.txt", Size: 42, ModTime: 100, Inode: 7777},
		"old_rem_name.txt":   {RelPath: "old_rem_name.txt", Size: 88, ModTime: 100},
		"type_clash":         {RelPath: "type_clash", IsDir: true},
		"Report.md":          {RelPath: "Report.md", Size: 7, ModTime: 200},
		"bad_space ":         {RelPath: "bad_space ", Size: 3, ModTime: 200},
	}

	snapB := map[string]BisyncNode{
		"adopt.txt":          {RelPath: "adopt.txt", Size: 20, ModTime: 201, Hash: "aaa"},
		"new_b.txt":          {RelPath: "new_b.txt", Size: 16, ModTime: 200},
		"unchanged.txt":      {RelPath: "unchanged.txt", Size: 10, ModTime: 100},
		"edit_a.txt":         {RelPath: "edit_a.txt", Size: 10, ModTime: 100},
		"edit_b.txt":         {RelPath: "edit_b.txt", Size: 13, ModTime: 260},
		"del_a.txt":          {RelPath: "del_a.txt", Size: 10, ModTime: 100},
		"del_a_edit_b.txt":   {RelPath: "del_a_edit_b.txt", Size: 55, ModTime: 310},
		"both_edit.txt":      {RelPath: "both_edit.txt", Size: 14, ModTime: 350},
		"old_local_name.txt": {RelPath: "old_local_name.txt", Size: 42, ModTime: 100},
		"new_rem_name.txt":   {RelPath: "new_rem_name.txt", Size: 88, ModTime: 100, ObjectID: "obj-99"},
		"type_clash":         {RelPath: "type_clash", IsDir: false, Size: 5, ModTime: 200},
		"report.md":          {RelPath: "report.md", Size: 9, ModTime: 190},
	}

	plan := BuildBisyncPlan(BisyncPlanInput{
		JobID:          1,
		Now:            now,
		CompareMode:    0,
		ConflictPolicy: 2,
		CapsA:          capsA,
		CapsB:          capsB,
		Base:           base,
		SnapA:          snapA,
		SnapB:          snapB,
		Excluded:       map[string]bool{"newly_excluded.tmp": true},
	})

	hasTransfer := func(list []BisyncTransfer, rel string) bool {
		for _, it := range list {
			if it.RelPath == rel {
				return true
			}
		}
		return false
	}
	hasDelete := func(list []BisyncDelete, rel string) bool {
		for _, it := range list {
			if it.RelPath == rel {
				return true
			}
		}
		return false
	}
	findConflict := func(rel, kind string) *BisyncConflictAction {
		for i := range plan.Conflicts {
			if plan.Conflicts[i].RelPath == rel && plan.Conflicts[i].ConflictType == kind {
				return &plan.Conflicts[i]
			}
		}
		return nil
	}

	// Zero-cost adopt
	if len(plan.Adopt) != 1 || plan.Adopt[0].RelPath != "adopt.txt" {
		t.Fatalf("expected adopt.txt zero-cost adopted, got %+v", plan.Adopt)
	}
	// Newly excluded transitions to ignored without deleting
	if len(plan.MarkIgnored) != 1 || plan.MarkIgnored[0].RelPath != "newly_excluded.tmp" {
		t.Fatalf("expected newly_excluded.tmp marked ignored, got %+v", plan.MarkIgnored)
	}
	// Both deleted -> tombstone
	if len(plan.MarkTombstone) != 1 || plan.MarkTombstone[0].RelPath != "both_del.txt" {
		t.Fatalf("expected both_del.txt tombstoned, got %+v", plan.MarkTombstone)
	}
	// Uploads: new_a, edit_a, edit_a_del_b (edit wins over delete)
	for _, rel := range []string{"new_a.txt", "edit_a.txt", "edit_a_del_b.txt"} {
		if !hasTransfer(plan.CopyA2B, rel) {
			t.Fatalf("expected %s in CopyA2B, got %+v", rel, plan.CopyA2B)
		}
	}
	// Downloads: new_b, edit_b, del_a_edit_b (edit wins over delete)
	for _, rel := range []string{"new_b.txt", "edit_b.txt", "del_a_edit_b.txt"} {
		if !hasTransfer(plan.CopyB2A, rel) {
			t.Fatalf("expected %s in CopyB2A, got %+v", rel, plan.CopyB2A)
		}
	}
	// Deletes: del_b on A, del_a on B (and renamed files NOT deleted!)
	if !hasDelete(plan.DeleteA, "del_b.txt") || hasDelete(plan.DeleteA, "old_rem_name.txt") {
		t.Fatalf("unexpected DeleteA: %+v", plan.DeleteA)
	}
	if !hasDelete(plan.DeleteB, "del_a.txt") || hasDelete(plan.DeleteB, "old_local_name.txt") {
		t.Fatalf("unexpected DeleteB: %+v", plan.DeleteB)
	}
	// Rename detection: local inode -> MoveB; remote ObjectID -> MoveA
	if len(plan.MoveB) != 1 || plan.MoveB[0].From != "old_local_name.txt" || plan.MoveB[0].To != "new_local_name.txt" {
		t.Fatalf("expected local rename detected as MoveB, got %+v", plan.MoveB)
	}
	if len(plan.MoveA) != 1 || plan.MoveA[0].From != "old_rem_name.txt" || plan.MoveA[0].To != "new_rem_name.txt" {
		t.Fatalf("expected remote rename detected as MoveA, got %+v", plan.MoveA)
	}
	// All 4 conflict types
	if cf := findConflict("both_edit.txt", mapper.BisyncConflictContent); cf == nil || cf.WinnerSide != "B" || cf.RenameSide != "A" || !strings.Contains(cf.ConflictCopyPath, "__本地__") {
		t.Fatalf("expected content conflict keeping both with A renamed, got %+v", cf)
	}
	if cf := findConflict("type_clash", mapper.BisyncConflictType); cf == nil || cf.WinnerSide != "A" || cf.RenameSide != "B" || !strings.Contains(cf.ConflictCopyPath, "__TypeConflict") {
		t.Fatalf("expected type conflict keeping dir A and renaming B file, got %+v", cf)
	}
	if cf := findConflict("Report.md", mapper.BisyncConflictCase); cf == nil || !strings.Contains(cf.ConflictCopyPath, "__CaseConflict") {
		t.Fatalf("expected case conflict for Report.md vs report.md on case-insensitive B, got %+v", cf)
	}
	if cf := findConflict("bad_space ", mapper.BisyncConflictInvalidName); cf == nil {
		t.Fatalf("expected invalid_name conflict for trailing space on OneDrive, got %+v", plan.Conflicts)
	}
}

func TestBuildBisyncPlanDeleteGuardAndWebDAVNoPingPong(t *testing.T) {
	now := time.Unix(1764150000, 0)
	capsA := BisyncEndpointCaps{Side: "A", Label: "本地", IsLocal: true}
	capsB := BisyncEndpointCaps{Side: "B", Label: "OpenList", UnreliableModTime: true}

	// WebDAV server rewrote ModTime after upload, size unchanged -> must NOT trigger false download!
	planDAV := BuildBisyncPlan(BisyncPlanInput{
		JobID: 2, Now: now, CompareMode: 0, ConflictPolicy: 2, CapsA: capsA, CapsB: capsB,
		Base:  map[string]mapper.BisyncEntry{"a.txt": {JobID: 2, RelPath: "a.txt", State: mapper.BisyncStateSynced, ASize: 100, AModTime: 1000, BSize: 100, BModTime: 1005}},
		SnapA: map[string]BisyncNode{"a.txt": {RelPath: "a.txt", Size: 100, ModTime: 1000}},
		SnapB: map[string]BisyncNode{"a.txt": {RelPath: "a.txt", Size: 100, ModTime: 999999}},
	})
	if planDAV.HasWork() {
		t.Fatalf("expected zero work on unreliable-mtime backend with matching size, got %+v", planDAV)
	}

	// Delete volume guard triggers when >=5 files and >=50% deleted.
	base := map[string]mapper.BisyncEntry{}
	snapB := map[string]BisyncNode{}
	for i := 0; i < 6; i++ {
		rel := "f" + string(rune('a'+i)) + ".txt"
		base[rel] = mapper.BisyncEntry{JobID: 3, RelPath: rel, State: mapper.BisyncStateSynced, ASize: 10, AModTime: 100, BSize: 10, BModTime: 100}
		snapB[rel] = BisyncNode{RelPath: rel, Size: 10, ModTime: 100}
	}
	// A deleted 5 of 6 files.
	snapA := map[string]BisyncNode{"fa.txt": {RelPath: "fa.txt", Size: 10, ModTime: 100}}
	planGuard := BuildBisyncPlan(BisyncPlanInput{
		JobID: 3, Now: now, MaxDeletePercent: 50, CapsA: capsA, CapsB: capsB,
		Base: base, SnapA: snapA, SnapB: snapB,
	})
	if !planGuard.DeleteGuardB || len(planGuard.DeleteB) != 5 {
		t.Fatalf("expected DeleteGuardB triggered for 5/6 deletes, got guard=%v len=%d", planGuard.DeleteGuardB, len(planGuard.DeleteB))
	}
}

func TestBuildBisyncPlanPreservesCaseOnlyRenameConflict(t *testing.T) {
	plan := BuildBisyncPlan(BisyncPlanInput{
		CapsA: BisyncEndpointCaps{IsLocal: true, Label: "本地"},
		CapsB: BisyncEndpointCaps{CaseInsensitive: true, Label: "云端"},
		Base: map[string]mapper.BisyncEntry{
			"Report.txt": {RelPath: "Report.txt", State: mapper.BisyncStateSynced, ASize: 10, AModTime: 100, AInode: 7, BSize: 10, BModTime: 100},
		},
		SnapA: map[string]BisyncNode{
			"report.txt": {RelPath: "report.txt", Size: 10, ModTime: 100, Inode: 7},
		},
		SnapB: map[string]BisyncNode{
			"Report.txt": {RelPath: "Report.txt", Size: 10, ModTime: 100},
		},
	})
	if len(plan.MoveB) != 0 || len(plan.DeleteB) != 0 || len(plan.CopyA2B) != 0 {
		t.Fatalf("case-only rename must not overwrite or delete the remote file: %+v", plan)
	}
	if len(plan.Conflicts) != 1 || plan.Conflicts[0].ConflictType != mapper.BisyncConflictCase {
		t.Fatalf("expected a case conflict, got %+v", plan.Conflicts)
	}
}

func TestBisyncBatchVerificationKeepsOnlyUnambiguousPaths(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "rclone")
	script := `#!/bin/sh
case "$*" in
  *'lsjson --no-mimetype --hash -- remote:folder') ;;
  *) exit 1 ;;
esac
printf '%s\n' '[{"Name":"one","Size":12,"ID":"id-one","Hashes":{"MD5":"0123456789abcdef0123456789abcdef"}},{"Name":"duplicate"},{"Name":"duplicate"},{"Name":"irrelevant"}]'
`
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENSYNC_RCLONE_BIN", bin)
	cfg := native.Config{"remote": {"type": "webdav", "url": "https://example.test"}}
	entries := listBisyncVerificationDir(t.Context(), cfg, "/remote", "folder",
		[]string{"folder/one", "folder/duplicate", "folder/missing"}, true, native.SessionOptions{})
	if got := entries["one"]; got == nil || got.Size != 12 || got.ObjectID != "id-one" || got.MD5 == "" {
		t.Fatalf("unexpected verified metadata: %+v", got)
	}
	if len(entries) != 3 || entries["duplicate"] != nil || entries["missing"] != nil {
		t.Fatalf("ambiguous and missing paths should fall back to exact stat: %+v", entries)
	}
}

func TestThreeWayBisyncEndToEndConflictTrashAndPolicyEdit(t *testing.T) {
	if os.Getenv("OPENSYNC_RCLONE_BIN") == "" {
		t.Skip("set OPENSYNC_RCLONE_BIN to run real-rclone 3-way bisync E2E tests")
	}
	cleanupDB := setupBisyncThreeWayDB(t)
	defer cleanupDB()

	tmp := t.TempDir()
	dirA := filepath.Join(tmp, "sideA")
	dirB := filepath.Join(tmp, "sideB")
	for _, d := range []string{dirA, dirB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", dirA+":"+dirB)
	t.Setenv("TRIM_PKGTMP", "")

	roots := native.AuthorizedRoots()
	var vA, vB string
	for _, r := range roots {
		switch r.Path {
		case dirA:
			vA = "/" + native.Local + "/" + r.ID
		case dirB:
			vB = "/" + native.Local + "/" + r.ID
		}
	}
	if vA == "" || vB == "" {
		t.Fatalf("failed to resolve authorized local roots: %+v", roots)
	}

	engineID, err := EnsureNativeEngine()
	if err != nil {
		t.Fatal(err)
	}

	// Seed initial files: shared.txt identical on both (adopt!), only_a.txt, only_b.txt.
	ts := time.Now().Add(-10 * time.Second).Truncate(time.Second)
	for _, p := range []string{filepath.Join(dirA, "shared.txt"), filepath.Join(dirB, "shared.txt")} {
		_ = os.WriteFile(p, []byte("same-content"), 0o644)
		_ = os.Chtimes(p, ts, ts)
	}
	_ = os.WriteFile(filepath.Join(dirA, "only_a.txt"), []byte("from-A"), 0o644)
	_ = os.WriteFile(filepath.Join(dirB, "only_b.txt"), []byte("from-B"), 0o644)

	jobID, err := CreateJobClient(map[string]interface{}{
		"remark":         "3way-e2e",
		"alistId":        engineID,
		"method":         3,
		"isCron":         2,
		"enable":         1,
		"srcPath":        []string{vA},
		"dstPath":        []string{vB},
		"conflictPolicy": 2,
		"trashEnable":    1,
		"trashDir":       ".opensync-trash",
	})
	if err != nil {
		t.Fatalf("CreateJobClient failed: %v", err)
	}
	runAndWait := func() {
		t.Helper()
		if err := DoJobManual(jobID); err != nil {
			t.Fatalf("DoJobManual failed: %v", err)
		}
		jc, err := GetJobClientByID(jobID)
		if err != nil {
			t.Fatal(err)
		}
		if !jc.waitUntilIdle(30 * time.Second) {
			t.Fatal("bisync job timed out")
		}
	}

	// Run 1: Initial 3-way sync (adopt + upload + download).
	runAndWait()
	if b, err := os.ReadFile(filepath.Join(dirB, "only_a.txt")); err != nil || string(b) != "from-A" {
		t.Fatalf("only_a.txt missing on B: %q (%v)", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(dirA, "only_b.txt")); err != nil || string(b) != "from-B" {
		t.Fatalf("only_b.txt missing on A: %q (%v)", b, err)
	}
	entries, err := mapper.ListBisyncEntries(jobID)
	if err != nil || len(entries) < 3 {
		t.Fatalf("expected SQLite bisync_entry populated, got %d (%v)", len(entries), err)
	}
	beforeRename, err := os.Stat(filepath.Join(dirB, "only_b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dirA, "only_b.txt"), filepath.Join(dirA, "renamed_b.txt")); err != nil {
		t.Fatal(err)
	}
	runAndWait()
	afterRename, err := os.Stat(filepath.Join(dirB, "renamed_b.txt"))
	if err != nil || !os.SameFile(beforeRename, afterRename) {
		t.Fatalf("expected the existing file on B to be renamed in place: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirB, "only_b.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected the old name to be absent on B, got %v", err)
	}
	entries, err = mapper.ListBisyncEntries(jobID)
	if err != nil || entries["renamed_b.txt"].State != mapper.BisyncStateSynced {
		t.Fatalf("expected the new path in the baseline, got %+v (%v)", entries["renamed_b.txt"], err)
	}

	// Editing trash/filter policies must NOT clear SQLite bisync_entry!
	jobRow, _ := mapper.GetJobByID(jobID)
	jobRow["exclude"] = "*.bak"
	if err := EditJobClient(jobRow); err != nil {
		t.Fatalf("EditJobClient exclude failed: %v", err)
	}
	entriesAfterEdit, _ := mapper.ListBisyncEntries(jobID)
	if len(entriesAfterEdit) != len(entries) {
		t.Fatalf("editing exclude wiped bisync_entry! before=%d after=%d", len(entries), len(entriesAfterEdit))
	}

	// Run 2: Delete only_a.txt on A -> B must move it to .opensync-trash EVEN with conflictPolicy=2!
	if err := os.Remove(filepath.Join(dirA, "only_a.txt")); err != nil {
		t.Fatal(err)
	}
	runAndWait()
	if _, err := os.Stat(filepath.Join(dirB, "only_a.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected only_a.txt deleted on B, err=%v", err)
	}
	// Verify it landed inside B's .opensync-trash!
	foundTrash := false
	_ = filepath.Walk(filepath.Join(dirB, ".opensync-trash"), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && info.Name() == "only_a.txt" {
			foundTrash = true
		}
		return nil
	})
	if !foundTrash {
		t.Fatal("expected deleted only_a.txt preserved in B/.opensync-trash even when conflictPolicy=2")
	}

	// Run 3: Create content conflict on shared.txt (modify both sides differently, B newer).
	tA := time.Now().Add(-2 * time.Second).Truncate(time.Second)
	tB := time.Now().Truncate(time.Second)
	_ = os.WriteFile(filepath.Join(dirA, "shared.txt"), []byte("version-from-A-older"), 0o644)
	_ = os.Chtimes(filepath.Join(dirA, "shared.txt"), tA, tA)
	_ = os.WriteFile(filepath.Join(dirB, "shared.txt"), []byte("version-from-B-newer-content"), 0o644)
	_ = os.Chtimes(filepath.Join(dirB, "shared.txt"), tB, tB)

	runAndWait()

	conflicts, err := ListJobBisyncConflicts(jobID, false)
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("expected 1 unresolved conflict, got %+v (%v)", conflicts, err)
	}
	cf := conflicts[0]
	if cf.RelPath != "shared.txt" || cf.ConflictType != mapper.BisyncConflictContent || cf.WinnerSide != "B" || cf.ConflictCopyPath == "" {
		t.Fatalf("unexpected conflict record: %+v", cf)
	}
	// Main file on both sides should hold winner (B), conflict copy should hold loser (A).
	if got, _ := os.ReadFile(filepath.Join(dirA, "shared.txt")); string(got) != "version-from-B-newer-content" {
		t.Fatalf("expected A/shared.txt updated to winner B, got %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dirB, cf.ConflictCopyPath)); string(got) != "version-from-A-older" {
		t.Fatalf("expected B/%s to preserve loser A, got %q", cf.ConflictCopyPath, got)
	}

	rightCopy := filepath.Join(dirB, cf.ConflictCopyPath)
	if err := os.Rename(rightCopy, rightCopy+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := ResolveJobBisyncConflict(context.Background(), jobID, cf.ID, "keep_local"); err == nil {
		t.Fatal("resolving with a missing right copy succeeded")
	}
	for _, root := range []string{dirA, dirB} {
		if got, err := os.ReadFile(filepath.Join(root, "shared.txt")); err != nil || string(got) != "version-from-B-newer-content" {
			t.Fatalf("original %s changed after preflight failed: %q (%v)", root, got, err)
		}
	}
	if err := os.Rename(rightCopy+".saved", rightCopy); err != nil {
		t.Fatal(err)
	}

	// Resolve conflict via Conflict Center choosing keep_local (promotes A's conflict copy back to shared.txt on both sides!).
	if err := ResolveJobBisyncConflict(context.Background(), jobID, cf.ID, "keep_local"); err != nil {
		t.Fatalf("ResolveJobBisyncConflict keep_local failed: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dirA, "shared.txt")); string(got) != "version-from-A-older" {
		t.Fatalf("expected A/shared.txt promoted to local version, got %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dirB, "shared.txt")); string(got) != "version-from-A-older" {
		t.Fatalf("expected B/shared.txt promoted to local version, got %q", got)
	}
	remaining, _ := ListJobBisyncConflicts(jobID, false)
	if len(remaining) != 0 {
		t.Fatalf("expected 0 unresolved conflicts after resolution, got %+v", remaining)
	}

	deletions := []string{"delete-1", "delete-2", "delete-3", "delete-4", "delete-5", "delete-6", "delete-7", "delete-8"}
	for _, name := range deletions {
		if err := os.WriteFile(filepath.Join(dirA, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runAndWait()
	for _, name := range deletions {
		if err := os.Remove(filepath.Join(dirA, name)); err != nil {
			t.Fatal(err)
		}
	}
	storedJob, err := mapper.GetJobByID(jobID)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loadNative(engineID)
	if err != nil {
		t.Fatal(err)
	}
	preview := &dryRunSession{}
	if err := dryRunNativeBisync(context.Background(), preview, cfg, storedJob); err != nil {
		t.Fatal(err)
	}
	if got := preview.snapshot(); got.DeleteCount != 0 || got.SkipCount != len(deletions) {
		t.Fatalf("protected deletes in dry run: %+v", got)
	}
	runAndWait()
	for _, name := range deletions {
		if _, err := os.Stat(filepath.Join(dirB, name)); err != nil {
			t.Fatalf("protected %s was not kept: %v", name, err)
		}
		// Leave later phases independent of these deliberately unmatched files.
		if err := os.WriteFile(filepath.Join(dirA, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// A directory replaced by a file on B must be rebuilt with all of A's children.
	dir := filepath.Join(dirA, "folder")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "child.txt"), []byte("keep child"), 0o644); err != nil {
		t.Fatal(err)
	}
	runAndWait()
	if err := os.Remove(filepath.Join(dirB, "folder", "child.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dirB, "folder")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "folder"), []byte("keep file too"), 0o644); err != nil {
		t.Fatal(err)
	}
	runAndWait()
	runAndWait()
	for _, side := range []string{dirA, dirB} {
		got, err := os.ReadFile(filepath.Join(side, "folder", "child.txt"))
		if err != nil || string(got) != "keep child" {
			t.Fatalf("expected %s to retain the child, got %q (%v)", side, got, err)
		}
	}
	unresolved, err := ListJobBisyncConflicts(jobID, false)
	if err != nil || len(unresolved) != 1 || unresolved[0].ConflictType != mapper.BisyncConflictType {
		t.Fatalf("expected type conflict, got %+v (%v)", unresolved, err)
	}
	if err := ResolveJobBisyncConflict(context.Background(), jobID, unresolved[0].ID, "keep_remote"); err == nil {
		t.Fatal("type conflicts must reject destructive version selection")
	}
}

func TestBisyncRenameWithChangedContent(t *testing.T) {
	old := BisyncNode{RelPath: "old.txt", Size: 3, ModTime: 100, Hash: "old", Inode: 123}
	newVersion := BisyncNode{RelPath: "new.txt", Size: 3, ModTime: 110, Hash: "new", Inode: 123}
	plan := BuildBisyncPlan(BisyncPlanInput{
		CompareMode: 2,
		CapsA:       BisyncEndpointCaps{IsLocal: true},
		Base:        map[string]mapper.BisyncEntry{old.RelPath: makeBisyncEntry(1, old.RelPath, false, old, old, 100)},
		SnapA:       map[string]BisyncNode{newVersion.RelPath: newVersion},
		SnapB:       map[string]BisyncNode{old.RelPath: old},
	})
	if len(plan.MoveB) != 0 || len(plan.CopyA2B) != 1 || plan.CopyA2B[0].RelPath != newVersion.RelPath {
		t.Fatalf("expected edited contents to be transferred, got %+v", plan)
	}
}

func TestBisyncCaseOnlyRenameKeepsBothVersions(t *testing.T) {
	old := BisyncNode{RelPath: "Foo.txt", Size: 3, ModTime: 100}
	newVersion := BisyncNode{RelPath: "foo.txt", Size: 4, ModTime: 110}
	plan := BuildBisyncPlan(BisyncPlanInput{
		CompareMode: 1,
		CapsB:       BisyncEndpointCaps{CaseInsensitive: true},
		Base:        map[string]mapper.BisyncEntry{old.RelPath: makeBisyncEntry(1, old.RelPath, false, old, old, 100)},
		SnapA:       map[string]BisyncNode{newVersion.RelPath: newVersion},
		SnapB:       map[string]BisyncNode{old.RelPath: old},
	})
	if len(plan.Conflicts) != 1 || plan.Conflicts[0].ConflictType != mapper.BisyncConflictCase ||
		len(plan.CopyA2B) != 0 || len(plan.DeleteB) != 0 || len(plan.MarkTombstone) != 1 {
		t.Fatalf("expected the old path and incoming version to survive, got %+v", plan)
	}
}

func TestLegacyBisyncResyncFlagStillLoads(t *testing.T) {
	flags, canonical, err := parseNativeExtraFlags(`{"watchEnable":true,"bisyncResyncMode":"path1"}`)
	if err != nil || !flags.WatchEnable || canonical != `{"watchEnable":true}` {
		t.Fatalf("expected only the obsolete field to be discarded, got %q (%v)", canonical, err)
	}
}
