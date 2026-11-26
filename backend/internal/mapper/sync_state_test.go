package mapper

import (
	"path/filepath"
	"testing"
	"time"

	"opensync/internal/config"
)

func setupSyncStateTest(t *testing.T) int64 {
	t.Helper()
	resetGlobalDBForTest(t, &config.Config{DB: config.DBConfig{DBName: filepath.Join(t.TempDir(), "sync.db")}, Server: config.ServerConfig{PasswdStr: "sync-test"}})
	InitSQL()
	id, err := ExecuteInsert("INSERT INTO job(srcPath,dstPath,alistId) VALUES('/source','/target',1)")
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureSyncState(id, "config-a"); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSyncInboxSurvivesRestartAndOnlyAcknowledgesOwnGeneration(t *testing.T) {
	id := setupSyncStateTest(t)
	if err := EnqueueSyncChanges(id, []string{"a.txt"}, false); err != nil {
		t.Fatal(err)
	}
	first, err := GetSyncState(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := CloseDB(); err != nil {
		t.Fatal(err)
	}
	restored, err := GetSyncState(id)
	if err != nil || !restored.Pending || len(restored.Paths) != 1 {
		t.Fatalf("restart: %+v, %v", restored, err)
	}
	if err := EnqueueSyncChanges(id, []string{"b.txt"}, false); err != nil {
		t.Fatal(err)
	}
	if err := EnqueueSyncChanges(id, nil, true); err != nil {
		t.Fatal(err)
	}
	if err := CompleteSyncRun(id, first.Generation, ""); err != nil {
		t.Fatal(err)
	}
	next, _ := GetSyncState(id)
	if !next.Pending || len(next.Paths) != 2 {
		t.Fatalf("new changes acknowledged by old run: %+v", next)
	}
	if !next.FullScan {
		t.Fatal("full reconciliation must retain known changed files")
	}
	if err := CompleteSyncRun(id, next.Generation, "network failed"); err != nil {
		t.Fatal(err)
	}
	failed, _ := GetSyncState(id)
	if !failed.Pending || failed.Error != "network failed" || failed.RetryAt <= time.Now().Unix() || failed.Failures != 1 {
		t.Fatalf("failed state: %+v", failed)
	}
	if err := CompleteSyncRun(id, failed.Generation, ""); err != nil {
		t.Fatal(err)
	}
	done, _ := GetSyncState(id)
	if done.Pending || len(done.Paths) != 0 || done.Error != "" || done.LastSuccess == 0 {
		t.Fatalf("checkpoint: %+v", done)
	}
}

func TestSyncObservedIndexQueuesOnlyChangesAndRetainsDeletionTombstones(t *testing.T) {
	id := setupSyncStateTest(t)
	files := []SyncFile{{Path: "a.txt", Size: 4, ModTime: 10}, {Path: "b.txt", Size: 8, ModTime: 20}}
	if err := ObserveSyncTree(id, "/source", files, true); err != nil {
		t.Fatal(err)
	}
	state, _ := GetSyncState(id)
	if !state.FullScan {
		t.Fatal("first observation must reconcile the whole tree")
	}
	if err := CompleteSyncRun(id, state.Generation, ""); err != nil {
		t.Fatal(err)
	}
	if err := ObserveSyncTree(id, "/source", files, true); err != nil {
		t.Fatal(err)
	}
	state, _ = GetSyncState(id)
	if state.Pending {
		t.Fatalf("unchanged tree queued: %+v", state)
	}
	files[0].ModTime++
	if err := ObserveSyncTree(id, "/source", files, true); err != nil {
		t.Fatal(err)
	}
	state, _ = GetSyncState(id)
	if state.FullScan || len(state.Paths) != 1 || state.Paths[0] != "/source/a.txt" {
		t.Fatalf("selective change: %+v", state)
	}
	if err := CompleteSyncRun(id, state.Generation, ""); err != nil {
		t.Fatal(err)
	}
	if err := ObserveSyncTree(id, "/source", files[:1], true); err != nil {
		t.Fatal(err)
	}
	state, _ = GetSyncState(id)
	var deleted bool
	if err := GetDB().QueryRow("SELECT deleted FROM sync_file_state WHERE jobId=? AND path='b.txt'", id).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if !state.FullScan || !deleted {
		t.Fatalf("deletion was not protected: %+v, tombstone=%v", state, deleted)
	}
	if err := EnsureSyncState(id, "config-b"); err != nil {
		t.Fatal(err)
	}
	state, _ = GetSyncState(id)
	var count int
	if err := GetDB().QueryRow("SELECT COUNT(*) FROM sync_file_state WHERE jobId=?", id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 || !state.FullScan {
		t.Fatalf("configuration invalidation lost safety state: %+v, count=%d", state, count)
	}
}

func TestSyncIndexAndInboxCommitAtomically(t *testing.T) {
	id := setupSyncStateTest(t)
	if _, err := GetDB().Exec(`CREATE TRIGGER reject_sync_update BEFORE UPDATE ON sync_state BEGIN SELECT RAISE(ABORT,'blocked'); END`); err != nil {
		t.Fatal(err)
	}
	err := ObserveSyncTree(id, "/source", []SyncFile{{Path: "a.txt", Size: 1}}, true)
	if err == nil {
		t.Fatal("expected inbox failure")
	}
	var count int
	if err := GetDB().QueryRow("SELECT COUNT(*) FROM sync_file_state WHERE jobId=?", id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("index committed without durable inbox")
	}
}

func TestUnchangedSyncTreeDoesNotWaitForSQLiteWriter(t *testing.T) {
	id := setupSyncStateTest(t)
	files := []SyncFile{{Path: "same.txt", Size: 1}}
	if err := ObserveSyncTree(id, "/source", files, true); err != nil {
		t.Fatal(err)
	}
	writer, err := GetDB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	if _, err := writer.Exec("UPDATE sync_state SET lastPoll=1 WHERE jobId=?", id); err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() { result <- ObserveSyncTree(id, "/source", files, true) }()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		_ = writer.Rollback()
		<-result
		t.Fatal("unchanged sync observation waited for the write lock")
	}
}

func TestSyncAuditUpgradesSelectiveWorkWithoutResettingDebounce(t *testing.T) {
	id := setupSyncStateTest(t)
	if err := EnqueueSyncChanges(id, []string{"/source/a.txt"}, false); err != nil {
		t.Fatal(err)
	}
	then := time.Now().Unix() - 300
	if err := ExecuteUpdate("UPDATE sync_state SET updatedAt=? WHERE jobId=?", then, id); err != nil {
		t.Fatal(err)
	}
	if err := RequestSyncAudit(id); err != nil {
		t.Fatal(err)
	}
	s, err := GetSyncState(id)
	if err != nil || !s.FullScan || s.UpdatedAt != then || len(s.Paths) != 1 {
		t.Fatalf("audit lost queued work or postponed it: %+v %v", s, err)
	}
}

func TestSyncSchemaMigrationIsReplayableAndJobDeletionCleansState(t *testing.T) {
	id := setupSyncStateTest(t)
	for range 2 {
		if err := migrateDBTx(GetDB(), 261124); err != nil {
			t.Fatal(err)
		}
	}
	if err := ObserveSyncTree(id, "/source", []SyncFile{{Path: "a", Size: 1}}, true); err != nil {
		t.Fatal(err)
	}
	if err := DeleteJob(id); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"sync_state", "sync_file_state"} {
		var count int
		if err := GetDB().QueryRow("SELECT COUNT(*) FROM "+table+" WHERE jobId=?", id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("orphan rows in %s", table)
		}
	}
}
