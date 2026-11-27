package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opensync/internal/mapper"
	"opensync/internal/native"
	"opensync/pkg/util"
)

func TestOneWayDeleteModeAndDirectionModeValidationAndCompat(t *testing.T) {
	// Valid combinations
	for _, tc := range []map[string]interface{}{
		{"oneWayDeleteMode": "none", "syncDirectionMode": "upload"},
		{"oneWayDeleteMode": "tracked", "syncDirectionMode": "download"},
		{"oneWayDeleteMode": "mirror", "syncDirectionMode": "bidirectional"},
		{"oneWayDeleteMode": "", "syncDirectionMode": ""},
	} {
		extra, _, err := parseNativeExtraFlags(tc)
		if err != nil {
			t.Fatalf("valid extraFlags %+v rejected: %v", tc, err)
		}
		if extra.OneWayDeleteMode != tc["oneWayDeleteMode"] || extra.SyncDirectionMode != tc["syncDirectionMode"] {
			t.Fatalf("parsed mismatch: %+v vs %+v", extra, tc)
		}
	}

	// Invalid enum values rejected
	for _, bad := range []map[string]interface{}{
		{"oneWayDeleteMode": "wipe"},
		{"oneWayDeleteMode": 123},
		{"syncDirectionMode": "sideways"},
		{"syncDirectionMode": true},
	} {
		if _, _, err := parseNativeExtraFlags(bad); err == nil {
			t.Fatalf("expected error for invalid extraFlags %+v", bad)
		}
	}

	// Backward compatibility when oneWayDeleteMode is omitted/empty
	if got := effectiveOneWayDeleteMode(map[string]interface{}{"method": 0}, nativeExtraFlags{}); got != "none" {
		t.Fatalf("method=0 default oneWayDeleteMode = %q, want none", got)
	}
	if got := effectiveOneWayDeleteMode(map[string]interface{}{"method": 1}, nativeExtraFlags{}); got != "mirror" {
		t.Fatalf("method=1 default oneWayDeleteMode = %q, want mirror", got)
	}
	if got := effectiveOneWayDeleteMode(map[string]interface{}{"method": 0}, nativeExtraFlags{OneWayDeleteMode: "tracked"}); got != "tracked" {
		t.Fatalf("method=0 explicit tracked = %q, want tracked", got)
	}
	if got := effectiveOneWayDeleteMode(map[string]interface{}{"method": 1}, nativeExtraFlags{OneWayDeleteMode: "none"}); got != "none" {
		t.Fatalf("method=1 explicit none = %q, want none", got)
	}
}

func TestTrackedOneWayDeleteLifecycleAndTrash(t *testing.T) {
	if os.Getenv("OPENSYNC_RCLONE_BIN") == "" {
		t.Skip("set OPENSYNC_RCLONE_BIN for real-rclone tracked one-way delete integration")
	}

	t.Setenv("OPENSYNC_DATA_DIR", t.TempDir())
	t.Setenv("TRIM_PKGVAR", "")
	defer setupNativeTestDB(t)()
	jobFSWatchers.ResetForTest()
	defer jobFSWatchers.ResetForTest()

	base := t.TempDir()
	srcRoot := filepath.Join(base, "src")
	dstRoot := filepath.Join(base, "dst")
	for _, p := range []string{srcRoot, dstRoot} {
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", srcRoot+":"+dstRoot)
	t.Setenv("TRIM_PKGTMP", "")
	if err := SaveNativeStorage(context.Background(), NativeStorageInput{}); err != nil {
		t.Fatal(err)
	}
	state, err := GetNativeStorageState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	roots, err := GetChildPath(context.Background(), state.EngineID, "/"+native.Local)
	if err != nil {
		t.Fatal(err)
	}
	virtual := func(name string) string {
		for _, r := range roots {
			if filepath.Base(r["name"]) == name {
				return r["path"]
			}
		}
		t.Fatalf("root %s not found", name)
		return ""
	}
	srcVirtual := virtual("src")
	dstVirtual := virtual("dst")

	// Pre-populate unrelated destination files that must ALWAYS stay intact.
	if err := os.WriteFile(filepath.Join(dstRoot, "unrelated.txt"), []byte("keep-me"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dstRoot, "unrelated_dir"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dstRoot, "unrelated_dir", "extra.txt"), []byte("keep-nested"), 0600); err != nil {
		t.Fatal(err)
	}

	// Create tracked source files
	if err := os.WriteFile(filepath.Join(srcRoot, "tracked.txt"), []byte("v1"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "stay.txt"), []byte("stay"), 0600); err != nil {
		t.Fatal(err)
	}

	jobID, err := CreateJobClient(map[string]interface{}{
		"alistId":    state.EngineID,
		"remark":     "tracked one-way delete",
		"srcPath":    srcVirtual,
		"dstPath":    dstVirtual,
		"method":     0,
		"isCron":     2,
		"enable":     1,
		"extraFlags": `{"oneWayDeleteMode":"tracked","syncDirectionMode":"upload"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = RemoveJobClient(jobID) }()

	jc, err := GetJobClientByID(jobID)
	if err != nil {
		t.Fatal(err)
	}
	runAndWait := func() {
		jc.DoJob()
		waitDeadline := time.Now().Add(20 * time.Second)
		for jc.isBusy() && time.Now().Before(waitDeadline) {
			time.Sleep(25 * time.Millisecond)
		}
		if jc.isBusy() {
			t.Fatal("timed out waiting for tracked job task completion")
		}
	}

	// 1. Initial upload: copies tracked.txt & stay.txt, preserves unrelated files, records baseline.
	runAndWait()
	for _, rel := range []string{"tracked.txt", "stay.txt", "unrelated.txt", filepath.Join("unrelated_dir", "extra.txt")} {
		if _, err := os.Stat(filepath.Join(dstRoot, rel)); err != nil {
			t.Fatalf("expected %s after upload: %v", rel, err)
		}
	}
	baseline, err := mapper.ListOnewayBaseline(jobID, normalizeDirPath(srcVirtual), normalizeDirPath(dstVirtual))
	if err != nil || len(baseline) != 2 {
		t.Fatalf("expected 2 baseline entries, got %+v (%v)", baseline, err)
	}

	// 2. Delete synced tracked.txt on source -> dry-run preview shows tracked.txt delete only, not unrelated.txt.
	if err := os.Remove(filepath.Join(srcRoot, "tracked.txt")); err != nil {
		t.Fatal(err)
	}
	dryRes, err := StartDryRun(jobID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for dryRes.Status == "running" && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		dryRes, err = GetDryRun(jobID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if dryRes.Status != "completed" || dryRes.DeleteCount != 1 || len(dryRes.Items) != 1 || dryRes.Items[0].FileName != "tracked.txt" {
		t.Fatalf("unexpected tracked dry-run result: %+v", dryRes)
	}

	// Run real sync: tracked.txt removed from dst & baseline; stay.txt & unrelated files kept!
	runAndWait()
	if _, err := os.Stat(filepath.Join(dstRoot, "tracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected dst/tracked.txt deleted, stat err = %v", err)
	}
	for _, rel := range []string{"stay.txt", "unrelated.txt", filepath.Join("unrelated_dir", "extra.txt")} {
		if _, err := os.Stat(filepath.Join(dstRoot, rel)); err != nil {
			t.Fatalf("expected %s preserved after tracked delete: %v", rel, err)
		}
	}
	baseline, err = mapper.ListOnewayBaseline(jobID, normalizeDirPath(srcVirtual), normalizeDirPath(dstVirtual))
	if err != nil || len(baseline) != 1 || baseline[0].RelPath != "stay.txt" {
		t.Fatalf("expected [stay.txt] in baseline, got %+v (%v)", baseline, err)
	}

	// 3. Delete remaining file on source (small source becomes empty): stay.txt deleted, unrelated still kept!
	if err := os.Remove(filepath.Join(srcRoot, "stay.txt")); err != nil {
		t.Fatal(err)
	}
	runAndWait()
	if _, err := os.Stat(filepath.Join(dstRoot, "stay.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected dst/stay.txt deleted even when small source becomes empty, err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dstRoot, "unrelated.txt")); err != nil {
		t.Fatalf("unrelated.txt must NEVER be deleted: %v", err)
	}

	// 4. Enable trash + exclude filter protection + verify trash move
	if err := os.WriteFile(filepath.Join(srcRoot, "trashme.txt"), []byte("to-trash"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "protected.log"), []byte("log-data"), 0600); err != nil {
		t.Fatal(err)
	}
	jobRow, err := mapper.GetJobByID(jobID)
	if err != nil {
		t.Fatal(err)
	}
	jobRow["trashEnable"] = 1
	jobRow["trashDir"] = ".opensync-trash"
	if err := EditJobClient(jobRow); err != nil {
		t.Fatal(err)
	}
	jc, err = GetJobClientByID(jobID)
	if err != nil {
		t.Fatal(err)
	}
	runAndWait()

	// Add *.log to exclude rules, then remove both files on source.
	jobRow, err = mapper.GetJobByID(jobID)
	if err != nil {
		t.Fatal(err)
	}
	jobRow["exclude"] = "*.log"
	if err := EditJobClient(jobRow); err != nil {
		t.Fatal(err)
	}
	jc, err = GetJobClientByID(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(srcRoot, "trashme.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(srcRoot, "protected.log")); err != nil {
		t.Fatal(err)
	}
	runAndWait()

	if _, err := os.Stat(filepath.Join(dstRoot, "trashme.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected dst/trashme.txt moved out of root to trash, stat err = %v", err)
	}
	// Find trashme.txt inside .opensync-trash
	foundTrashed := false
	_ = filepath.WalkDir(filepath.Join(dstRoot, ".opensync-trash"), func(p string, d os.DirEntry, wErr error) error {
		if wErr == nil && !d.IsDir() && d.Name() == "trashme.txt" {
			foundTrashed = true
		}
		return nil
	})
	if !foundTrashed {
		t.Fatal("expected trashme.txt in dst/.opensync-trash/")
	}
	// Excluded file must remain untouched!
	if _, err := os.Stat(filepath.Join(dstRoot, "protected.log")); err != nil {
		t.Fatalf("excluded protected.log must NOT be deleted: %v", err)
	}
	// Unrelated file STILL preserved!
	if _, err := os.Stat(filepath.Join(dstRoot, "unrelated.txt")); err != nil {
		t.Fatalf("unrelated.txt must stay preserved after trash cycle: %v", err)
	}

	// 5. Empty-source protection when tracked baseline >= emptySourceSubdirMinEntries (100):
	var bulk []mapper.OnewayBaselineEntry
	for i := 0; i < 105; i++ {
		name := fmt.Sprintf("bulk-%03d.dat", i)
		if err := os.WriteFile(filepath.Join(dstRoot, name), []byte("bulk"), 0600); err != nil {
			t.Fatal(err)
		}
		bulk = append(bulk, mapper.OnewayBaselineEntry{RelPath: name, Size: 4, SyncedAt: time.Now().Unix()})
	}
	if err := mapper.UpsertOnewayBaselineEntries(jobID, normalizeDirPath(srcVirtual), normalizeDirPath(dstVirtual), bulk); err != nil {
		t.Fatal(err)
	}
	runAndWait()
	for i := 0; i < 105; i++ {
		name := fmt.Sprintf("bulk-%03d.dat", i)
		if _, err := os.Stat(filepath.Join(dstRoot, name)); err != nil {
			t.Fatalf("empty-source guard failed to protect %s: %v", name, err)
		}
	}
	taskPage, err := mapper.GetJobTaskList(map[string]interface{}{"id": jobID, "page": 1, "limit": 5})
	if err != nil {
		t.Fatalf("GetJobTaskList failed: %v", err)
	}
	tasks, _ := taskPage["dataList"].([]map[string]interface{})
	if len(tasks) == 0 {
		t.Fatalf("expected latest task row, got %+v", taskPage)
	}
	items, err := mapper.GetTopFailedTaskItems(util.ToInt64(tasks[0]["id"]), 20)
	if err != nil {
		t.Fatal(err)
	}
	guardSeen := false
	for _, item := range items {
		if strings.Contains(util.StringValue(item["errMsg"]), "listed empty") {
			guardSeen = true
			break
		}
	}
	if !guardSeen {
		t.Fatalf("expected MirrorDeleteGuard task item for empty-source guard, items=%+v", items)
	}
}
