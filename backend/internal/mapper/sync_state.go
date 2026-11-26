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
		 error TEXT NOT NULL DEFAULT '')`,
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
	Pending, FullScan                                                       bool
	Failures                                                                int
	Paths                                                                   []string
}

func readSyncState(row interface{ Scan(...interface{}) error }) (SyncState, error) {
	var s SyncState
	var paths string
	err := row.Scan(&s.JobID, &s.Fingerprint, &s.Generation, &s.Pending, &s.FullScan,
		&paths, &s.UpdatedAt, &s.LastSuccess, &s.LastPoll, &s.LastAudit,
		&s.Failures, &s.RetryAt, &s.Error)
	if err == nil {
		err = json.Unmarshal([]byte(paths), &s.Paths)
	}
	return s, err
}

func GetSyncState(jobID int64) (SyncState, error) {
	return readSyncState(GetDB().QueryRow(`SELECT jobId,fingerprint,generation,pending,fullScan,
		paths,updatedAt,lastSuccess,lastPoll,lastAudit,failures,retryAt,error
		FROM sync_state WHERE jobId=?`, jobID))
}

func ListSyncStates(jobIDs ...int64) (map[int64]SyncState, error) {
	query := `SELECT jobId,fingerprint,generation,pending,fullScan,
		paths,updatedAt,lastSuccess,lastPoll,lastAudit,failures,retryAt,error
		FROM sync_state`
	var args []interface{}
	if len(jobIDs) > 0 {
		var clause string
		clause, args = int64InClause(jobIDs)
		query += " WHERE jobId IN (" + clause + ")"
	}
	rows, err := GetDB().Query(query, args...)
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
		 lastSuccess=0, lastPoll=0, lastAudit=0 WHERE jobId=?`, fingerprint, time.Now().Unix(), jobID)
		return err
	})
}

func enqueueSyncChanges(tx *sql.Tx, jobID int64, paths []string, full bool) error {
	s, err := readSyncState(tx.QueryRow(`SELECT jobId,fingerprint,generation,pending,fullScan,
		paths,updatedAt,lastSuccess,lastPoll,lastAudit,failures,retryAt,error
		FROM sync_state WHERE jobId=?`, jobID))
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
	now := time.Now().Unix()
	return withTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM sync_file_state WHERE jobId=? AND deleted=1 AND observedAt<?", jobID, now-30*86400); err != nil {
			return err
		}
		_, err := tx.Exec("UPDATE sync_state SET lastPoll=? WHERE jobId=?", now, jobID)
		return err
	})
}

func RequestSyncAudit(jobID int64) error {
	now := time.Now().Unix()
	return ExecuteUpdate(`UPDATE sync_state SET
	 generation=generation+CASE WHEN pending=1 AND fullScan=1 THEN 0 ELSE 1 END,
	 updatedAt=CASE WHEN pending=1 THEN updatedAt ELSE ? END,
	 pending=1,fullScan=1,lastAudit=? WHERE jobId=?`, now, now, jobID)
}

type SyncFile struct {
	Path          string
	Size, ModTime int64
	Hash          string
	IsDir         bool
}

type syncTreeChanges struct {
	upserts []SyncFile
	deleted map[string]SyncFile
	paths   []string
	full    bool
}

func compareSyncTree(tx *sql.Tx, jobID int64, root string, files []SyncFile, partial bool) (syncTreeChanges, error) {
	var changes syncTreeChanges
	rows, err := tx.Query(`SELECT path,size,modTime,hash,isDir FROM sync_file_state
	 WHERE jobId=? AND root=? AND deleted=0`, jobID, root)
	if err != nil {
		return changes, err
	}
	defer rows.Close()
	old := make(map[string]SyncFile)
	for rows.Next() {
		var f SyncFile
		if err := rows.Scan(&f.Path, &f.Size, &f.ModTime, &f.Hash, &f.IsDir); err != nil {
			return changes, err
		}
		old[f.Path] = f
	}
	if err := rows.Err(); err != nil {
		return changes, err
	}

	_, initialized := old[""]
	delete(old, "")
	changes.full = !initialized
	if !initialized {
		changes.upserts = append(changes.upserts, SyncFile{IsDir: true})
	}
	for _, f := range files {
		if f.Path == "" {
			continue
		}
		previous, ok := old[f.Path]
		delete(old, f.Path)
		if ok && previous == f {
			continue
		}
		if !f.IsDir && initialized {
			changes.paths = append(changes.paths, path.Join(root, f.Path))
		}
		changes.full = changes.full || !partial || f.IsDir || previous.IsDir
		changes.upserts = append(changes.upserts, f)
	}
	changes.deleted = old
	changes.full = changes.full || len(old) > 0
	return changes, nil
}

// ObserveSyncTree commits the observed index and its inbox changes atomically.
// The index describes observations, not successful transfers; only CompleteSyncRun
// advances the successful-sync checkpoint. Deleted entries remain as tombstones.
func ObserveSyncTree(jobID int64, root string, files []SyncFile, partial bool) error {
	const versionSQL = "SELECT generation,fingerprint FROM sync_state WHERE jobId=?"
	var generation int64
	var fingerprint string
	var changes syncTreeChanges
	if err := withReadTx(func(tx *sql.Tx) error {
		if err := tx.QueryRow(versionSQL, jobID).Scan(&generation, &fingerprint); err != nil {
			return err
		}
		var err error
		changes, err = compareSyncTree(tx, jobID, root, files, partial)
		return err
	}); err != nil {
		return err
	}
	if !changes.full && len(changes.paths) == 0 {
		return nil
	}
	return withTx(func(tx *sql.Tx) error {
		var currentGeneration int64
		var currentFingerprint string
		if err := tx.QueryRow(versionSQL, jobID).Scan(&currentGeneration, &currentFingerprint); err != nil {
			return err
		}
		if currentFingerprint != fingerprint {
			return errors.New("同步配置已变更，请重新扫描")
		}
		if currentGeneration != generation {
			// Another observer or a filesystem event changed the inbox. Recheck
			// under the write lock before committing the matching observation.
			var err error
			changes, err = compareSyncTree(tx, jobID, root, files, partial)
			if err != nil {
				return err
			}
			if !changes.full && len(changes.paths) == 0 {
				return nil
			}
		}
		now := time.Now().Unix()
		if len(changes.upserts) > 0 {
			stmt, err := tx.Prepare(`INSERT INTO sync_file_state(jobId,root,path,size,modTime,hash,isDir,deleted,observedAt)
			 VALUES(?,?,?,?,?,?,?,0,?) ON CONFLICT(jobId,root,path) DO UPDATE SET
			 size=excluded.size,modTime=excluded.modTime,hash=excluded.hash,isDir=excluded.isDir,deleted=0,observedAt=excluded.observedAt`)
			if err != nil {
				return err
			}
			defer stmt.Close()
			for _, f := range changes.upserts {
				if _, err := stmt.Exec(jobID, root, f.Path, f.Size, f.ModTime, f.Hash, f.IsDir, now); err != nil {
					return err
				}
			}
		}
		for p := range changes.deleted {
			if _, err := tx.Exec(`UPDATE sync_file_state SET deleted=1,observedAt=? WHERE jobId=? AND root=? AND path=?`, now, jobID, root, p); err != nil {
				return err
			}
		}
		return enqueueSyncChanges(tx, jobID, changes.paths, changes.full)
	})
}
