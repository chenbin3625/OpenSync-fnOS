package mapper

import (
	"path/filepath"
	"testing"

	"opensync/internal/config"
)

func TestOnewayBaselineCRUDAndDeleteJobCleanup(t *testing.T) {
	id := setupSyncStateTest(t)

	err := UpsertOnewayBaselineEntries(id, "/src/", "/dst/", []OnewayBaselineEntry{
		{RelPath: "b.txt", Size: 20, ModTime: 200, Hash: "h2", SyncedAt: 1000},
		{RelPath: "a.txt", Size: 10, ModTime: 100, Hash: "h1", SyncedAt: 1000},
	})
	if err != nil {
		t.Fatalf("UpsertOnewayBaselineEntries: %v", err)
	}

	got, err := ListOnewayBaseline(id, "/src/", "/dst/")
	if err != nil {
		t.Fatalf("ListOnewayBaseline: %v", err)
	}
	if len(got) != 2 || got[0].RelPath != "a.txt" || got[1].RelPath != "b.txt" {
		t.Fatalf("ListOnewayBaseline = %+v, want sorted [a.txt, b.txt]", got)
	}

	// Update existing entry
	if err := UpsertOnewayBaselineEntries(id, "/src/", "/dst/", []OnewayBaselineEntry{
		{RelPath: "a.txt", Size: 15, SyncedAt: 2000},
	}); err != nil {
		t.Fatalf("upsert update: %v", err)
	}
	got, err = ListOnewayBaseline(id, "/src/", "/dst/")
	if err != nil || len(got) != 2 || got[0].Size != 15 || got[0].SyncedAt != 2000 {
		t.Fatalf("updated baseline mismatch: %+v (%v)", got, err)
	}

	// Delete one tracked entry
	if err := DeleteOnewayBaselinePaths(id, "/src/", "/dst/", []string{"a.txt"}); err != nil {
		t.Fatalf("DeleteOnewayBaselinePaths: %v", err)
	}
	got, err = ListOnewayBaseline(id, "/src/", "/dst/")
	if err != nil || len(got) != 1 || got[0].RelPath != "b.txt" {
		t.Fatalf("after delete baseline = %+v (%v)", got, err)
	}

	// DeleteJob should clean up oneway_baseline automatically
	if err := DeleteJob(id); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	if n := mustCount(t, GetDB(), "SELECT COUNT(*) FROM oneway_baseline WHERE jobId=?", id); n != 0 {
		t.Fatalf("oneway_baseline count after DeleteJob = %d, want 0", n)
	}
}

func TestObserveSyncDirIncrementalAndFullFallback(t *testing.T) {
	id := setupSyncStateTest(t)

	// Uninitialized root must request full rescan cleanly.
	changed, needFull, err := ObserveSyncDir(id, "/src", "docs", []SyncFile{{Path: "docs/a.txt", Size: 5}}, true)
	if err != nil || !needFull || changed {
		t.Fatalf("uninitialized ObserveSyncDir = (%v,%v,%v), want (false,true,nil)", changed, needFull, err)
	}

	// Seed full baseline across docs/ and media/.
	changed, err = ObserveSyncTreeResult(id, "/src", []SyncFile{
		{Path: "docs", IsDir: true},
		{Path: "docs/a.txt", Size: 5, ModTime: 100},
		{Path: "media", IsDir: true},
		{Path: "media/keep.jpg", Size: 50, ModTime: 200},
	}, true)
	if err != nil || !changed {
		t.Fatalf("initial ObserveSyncTreeResult = (%v,%v), want (true,nil)", changed, err)
	}
	s, err := GetSyncState(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := CompleteSyncRun(id, s.Generation, ""); err != nil {
		t.Fatal(err)
	}

	// Same immediate entries in docs/ => no change, no full rescan.
	changed, needFull, err = ObserveSyncDir(id, "/src", "docs", []SyncFile{
		{Path: "docs/a.txt", Size: 5, ModTime: 100},
	}, true)
	if err != nil || changed || needFull {
		t.Fatalf("no-op ObserveSyncDir = (%v,%v,%v), want (false,false,nil)", changed, needFull, err)
	}

	// Add docs/b.txt => incremental change ONLY in docs/; media/keep.jpg untouched!
	changed, needFull, err = ObserveSyncDir(id, "/src", "docs", []SyncFile{
		{Path: "docs/a.txt", Size: 5, ModTime: 100},
		{Path: "docs/b.txt", Size: 12, ModTime: 300},
	}, true)
	if err != nil || !changed || needFull {
		t.Fatalf("add file ObserveSyncDir = (%v,%v,%v), want (true,false,nil)", changed, needFull, err)
	}
	s, err = GetSyncState(id)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Pending || s.FullScan || len(s.Paths) != 1 || s.Paths[0] != "/src/docs/b.txt" {
		t.Fatalf("unexpected pending inbox after partial dir add: %+v", s)
	}
	if cnt := mustCount(t, GetDB(), "SELECT COUNT(*) FROM sync_file_state WHERE jobId=? AND path='media/keep.jpg' AND deleted=0", id); cnt != 1 {
		t.Fatalf("sibling dir media/keep.jpg was unexpectedly touched (count=%d)", cnt)
	}

	// Directory added inside docs/ => must escalate to needFullRescan=true.
	changed, needFull, err = ObserveSyncDir(id, "/src", "docs", []SyncFile{
		{Path: "docs/a.txt", Size: 5, ModTime: 100},
		{Path: "docs/b.txt", Size: 12, ModTime: 300},
		{Path: "docs/nested", IsDir: true},
	}, true)
	if err != nil || !needFull || changed {
		t.Fatalf("new subdir ObserveSyncDir = (%v,%v,%v), want (false,true,nil)", changed, needFull, err)
	}
}

func TestMigrationFrom261127CreatesOnewayBaseline(t *testing.T) {
	resetGlobalDBForTest(t, &config.Config{
		DB:     config.DBConfig{DBName: filepath.Join(t.TempDir(), "migrate.db")},
		Server: config.ServerConfig{PasswdStr: "migrate-test"},
	})
	InitSQL()
	db := GetDB()
	if _, err := db.Exec("DROP TABLE IF EXISTS oneway_baseline"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE schema_version SET version=261127"); err != nil {
		t.Fatal(err)
	}
	if err := CloseDB(); err != nil {
		t.Fatal(err)
	}

	InitSQL()
	if !tableExists(GetDB(), "oneway_baseline") {
		t.Fatalf("expected migration from 261127 to recreate oneway_baseline")
	}
	if got := mustCount(t, GetDB(), "SELECT version FROM schema_version"); got != currentVersion {
		t.Fatalf("schema version = %d, want %d", got, currentVersion)
	}
}
