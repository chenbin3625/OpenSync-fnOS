package mapper

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"time"
)

func syncSchemaStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS sync_state (
		 jobId INTEGER PRIMARY KEY, fingerprint TEXT NOT NULL DEFAULT '',
		 generation INTEGER NOT NULL DEFAULT 0, pending INTEGER NOT NULL DEFAULT 0,
		 fullScan INTEGER NOT NULL DEFAULT 0, paths TEXT NOT NULL DEFAULT '[]',
		 updatedAt INTEGER NOT NULL DEFAULT 0, lastSuccess INTEGER NOT NULL DEFAULT 0,
		 lastPoll INTEGER NOT NULL DEFAULT 0, lastAudit INTEGER NOT NULL DEFAULT 0,
		 failures INTEGER NOT NULL DEFAULT 0, retryAt INTEGER NOT NULL DEFAULT 0,
		 error TEXT NOT NULL DEFAULT '', bisyncInitialized INTEGER NOT NULL DEFAULT 0,
		 bisyncNeedsRebuild INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS sync_file_state (
		 jobId INTEGER NOT NULL, root TEXT NOT NULL, path TEXT NOT NULL,
		 size INTEGER NOT NULL, modTime INTEGER NOT NULL, hash TEXT NOT NULL,
		 isDir INTEGER NOT NULL, deleted INTEGER NOT NULL DEFAULT 0, observedAt INTEGER NOT NULL,
		 PRIMARY KEY(jobId, root, path))`,
	}
}

// SyncState is a durable inbox. Generation prevents an older run from
// acknowledging changes that arrived while that run was transferring files.
type SyncState struct {
	JobID, Generation, UpdatedAt, LastSuccess, LastPoll, LastAudit, RetryAt int64
	Fingerprint, Error                                                      string
	Pending, FullScan, BisyncInitialized, BisyncNeedsRebuild                bool
	Failures                                                                int
	Paths                                                                   []string
}

func readSyncState(row interface{ Scan(...interface{}) error }) (SyncState, error) {
	var s SyncState
	var paths string
	err := row.Scan(&s.JobID, &s.Fingerprint, &s.Generation, &s.Pending, &s.FullScan,
		&paths, &s.UpdatedAt, &s.LastSuccess, &s.LastPoll, &s.LastAudit,
		&s.Failures, &s.RetryAt, &s.Error, &s.BisyncInitialized, &s.BisyncNeedsRebuild)
	if err == nil {
		err = json.Unmarshal([]byte(paths), &s.Paths)
	}
	return s, err
}

func GetSyncState(jobID int64) (SyncState, error) {
	return readSyncState(GetDB().QueryRow("SELECT * FROM sync_state WHERE jobId=?", jobID))
}

func ListSyncStates() (map[int64]SyncState, error) {
	rows, err := GetDB().Query("SELECT * FROM sync_state")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]SyncState)
	for rows.Next() {
		s, err := readSyncState(rows)
		if err != nil {
			return nil, err
		}
		out[s.JobID] = s
	}
	return out, rows.Err()
}

func EnsureSyncState(jobID int64, fingerprint string) error {
	return withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO sync_state(jobId, fingerprint)
		 SELECT id, ? FROM job WHERE id=? ON CONFLICT(jobId) DO NOTHING`, fingerprint, jobID); err != nil {
			return err
		}
		var old string
		if err := tx.QueryRow("SELECT fingerprint FROM sync_state WHERE jobId=?", jobID).Scan(&old); err != nil {
			return err
		}
		if old == fingerprint || fingerprint == "" {
			return nil
		}
		if _, err := tx.Exec("DELETE FROM sync_file_state WHERE jobId=?", jobID); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE sync_state SET fingerprint=?, generation=generation+1,
		 pending=1, fullScan=1, paths='[]', updatedAt=?, retryAt=0, failures=0, error='',
		 lastSuccess=0, lastPoll=0, lastAudit=0, bisyncNeedsRebuild=bisyncInitialized WHERE jobId=?`, fingerprint, time.Now().Unix(), jobID)
		return err
	})
}

func enqueueSyncChanges(tx *sql.Tx, jobID int64, paths []string, full bool) error {
	s, err := readSyncState(tx.QueryRow("SELECT * FROM sync_state WHERE jobId=?", jobID))
	if err != nil {
		return err
	}
	full = full || s.FullScan
	merged := make(map[string]bool)
	for _, p := range append(s.Paths, paths...) {
		if p != "" {
			merged[p] = true
		}
	}
	if len(merged) > 200 {
		full = true
	}
	list := make([]string, 0, len(merged))
	for p := range merged {
		list = append(list, p)
	}
	sort.Strings(list)
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE sync_state SET generation=generation+1, pending=1,
	 fullScan=?, paths=?, updatedAt=? WHERE jobId=?`, full, string(raw), time.Now().Unix(), jobID)
	return err
}

func EnqueueSyncChanges(jobID int64, paths []string, full bool) error {
	return withTx(func(tx *sql.Tx) error { return enqueueSyncChanges(tx, jobID, paths, full) })
}

func CompleteSyncRun(jobID, generation int64, runErr string) error {
	if runErr != "" {
		return RecordSyncError(jobID, runErr)
	}
	err := ExecuteUpdate(`UPDATE sync_state SET
	 pending=CASE WHEN generation=? THEN 0 ELSE pending END,
	 fullScan=CASE WHEN generation=? THEN 0 ELSE fullScan END,
	 paths=CASE WHEN generation=? THEN '[]' ELSE paths END,
	 lastSuccess=?, failures=0, retryAt=0, error='' WHERE jobId=?`,
		generation, generation, generation, time.Now().Unix(), jobID)
	return err
}

func RecordSyncError(jobID int64, message string) error {
	return withTx(func(tx *sql.Tx) error {
		var failures int
		if err := tx.QueryRow("SELECT failures FROM sync_state WHERE jobId=?", jobID).Scan(&failures); err != nil {
			return err
		}
		delay := min(3600, 10<<min(failures, 9))
		_, err := tx.Exec(`UPDATE sync_state SET error=?, failures=failures+1, retryAt=? WHERE jobId=?`,
			message, time.Now().Unix()+int64(delay), jobID)
		return err
	})
}

func MarkSyncPoll(jobID int64) error {
	return ExecuteUpdate("UPDATE sync_state SET lastPoll=? WHERE jobId=?", time.Now().Unix(), jobID)
}

func RequestSyncAudit(jobID int64) error {
	now := time.Now().Unix()
	return ExecuteUpdate(`UPDATE sync_state SET
	 generation=generation+CASE WHEN pending=1 AND fullScan=1 THEN 0 ELSE 1 END,
	 updatedAt=CASE WHEN pending=1 THEN updatedAt ELSE ? END,
	 pending=1,fullScan=1,lastAudit=? WHERE jobId=?`, now, now, jobID)
}

func MarkBisyncInitialized(jobID int64) error {
	err := ExecuteUpdate("UPDATE sync_state SET bisyncInitialized=1,bisyncNeedsRebuild=0 WHERE jobId=?", jobID)
	return err
}

type SyncFile struct {
	Path          string
	Size, ModTime int64
	Hash          string
	IsDir         bool
}

// ObserveSyncTree commits the observed index and its inbox changes atomically.
// The index describes observations, not successful transfers; only CompleteSyncRun
// advances the successful-sync checkpoint. Deleted entries remain as tombstones.
func ObserveSyncTree(jobID int64, root string, files []SyncFile, partial bool) error {
	return withTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(`SELECT path,size,modTime,hash,isDir FROM sync_file_state
		 WHERE jobId=? AND root=? AND deleted=0`, jobID, root)
		if err != nil {
			return err
		}
		old := make(map[string]SyncFile)
		for rows.Next() {
			var f SyncFile
			if err := rows.Scan(&f.Path, &f.Size, &f.ModTime, &f.Hash, &f.IsDir); err != nil {
				rows.Close()
				return err
			}
			old[f.Path] = f
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		_, initialized := old[""]
		delete(old, "")
		full := !initialized
		var changed []string
		now := time.Now().Unix()
		stmt, err := tx.Prepare(`INSERT INTO sync_file_state(jobId,root,path,size,modTime,hash,isDir,deleted,observedAt)
		 VALUES(?,?,?,?,?,?,?,0,?) ON CONFLICT(jobId,root,path) DO UPDATE SET
		 size=excluded.size,modTime=excluded.modTime,hash=excluded.hash,isDir=excluded.isDir,deleted=0,observedAt=excluded.observedAt`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		files = append(files, SyncFile{IsDir: true}) // Root sentinel also represents an empty tree.
		for _, f := range files {
			previous, ok := old[f.Path]
			if (f.Path == "" && initialized) || (ok && previous == f) {
				delete(old, f.Path)
				continue
			}
			if f.Path != "" && (!ok || previous != f) {
				if !f.IsDir && initialized {
					changed = append(changed, path.Join(root, f.Path))
				}
				if !partial || f.IsDir || (ok && previous.IsDir) {
					full = true
				}
			}
			delete(old, f.Path)
			if _, err := stmt.Exec(jobID, root, f.Path, f.Size, f.ModTime, f.Hash, f.IsDir, now); err != nil {
				return err
			}
		}
		for p := range old {
			full = true // Deletions use the existing mirror/bisync safety checks.
			if _, err := tx.Exec(`UPDATE sync_file_state SET deleted=1,observedAt=? WHERE jobId=? AND root=? AND path=?`, now, jobID, root, p); err != nil {
				return err
			}
		}
		if _, err := tx.Exec("DELETE FROM sync_file_state WHERE jobId=? AND deleted=1 AND observedAt<?", jobID, now-30*86400); err != nil {
			return err
		}
		if full || len(changed) > 0 {
			return enqueueSyncChanges(tx, jobID, changed, full)
		}
		return nil
	})
}
