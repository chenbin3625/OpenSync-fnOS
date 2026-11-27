package mapper

import (
	"database/sql"
	"strings"
	"time"
)

// OnewayBaselineEntry records a file confirmed successfully synced by a
// tracked one-way job for a specific (srcRoot, dstRoot) pair.
type OnewayBaselineEntry struct {
	JobID    int64
	SrcRoot  string
	DstRoot  string
	RelPath  string
	Size     int64
	ModTime  int64
	Hash     string
	SyncedAt int64
}

func onewayBaselineSchemaStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS oneway_baseline (
		 jobId INTEGER NOT NULL,
		 srcRoot TEXT NOT NULL,
		 dstRoot TEXT NOT NULL,
		 relPath TEXT NOT NULL,
		 size INTEGER NOT NULL DEFAULT 0,
		 modTime INTEGER NOT NULL DEFAULT 0,
		 hash TEXT NOT NULL DEFAULT '',
		 syncedAt INTEGER NOT NULL DEFAULT 0,
		 PRIMARY KEY(jobId, srcRoot, dstRoot, relPath))`,
	}
}

// ListOnewayBaseline loads all tracked baseline entries for one job's (srcRoot, dstRoot) pair.
func ListOnewayBaseline(jobID int64, srcRoot, dstRoot string) ([]OnewayBaselineEntry, error) {
	if jobID <= 0 {
		return nil, nil
	}
	var out []OnewayBaselineEntry
	err := withReadTx(func(tx *sql.Tx) error {
		rows, err := tx.Query(
			`SELECT jobId,srcRoot,dstRoot,relPath,size,modTime,hash,syncedAt
			 FROM oneway_baseline WHERE jobId=? AND srcRoot=? AND dstRoot=?
			 ORDER BY relPath ASC`,
			jobID, srcRoot, dstRoot,
		)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e OnewayBaselineEntry
			if err := rows.Scan(&e.JobID, &e.SrcRoot, &e.DstRoot, &e.RelPath, &e.Size, &e.ModTime, &e.Hash, &e.SyncedAt); err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	return out, err
}

// UpsertOnewayBaselineEntries records or refreshes confirmed synced files in the
// tracked one-way baseline table.
func UpsertOnewayBaselineEntries(jobID int64, srcRoot, dstRoot string, entries []OnewayBaselineEntry) error {
	if jobID <= 0 || len(entries) == 0 {
		return nil
	}
	now := time.Now().Unix()
	return withTx(func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`INSERT INTO oneway_baseline(jobId,srcRoot,dstRoot,relPath,size,modTime,hash,syncedAt)
		 VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(jobId,srcRoot,dstRoot,relPath) DO UPDATE SET
		 size=excluded.size,modTime=excluded.modTime,hash=excluded.hash,syncedAt=excluded.syncedAt`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, e := range entries {
			rel := strings.TrimSpace(e.RelPath)
			if rel == "" {
				continue
			}
			ts := e.SyncedAt
			if ts <= 0 {
				ts = now
			}
			if _, err := stmt.Exec(jobID, srcRoot, dstRoot, rel, e.Size, e.ModTime, e.Hash, ts); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteOnewayBaselinePaths removes deleted/absent tracked paths from the baseline.
func DeleteOnewayBaselinePaths(jobID int64, srcRoot, dstRoot string, relPaths []string) error {
	if jobID <= 0 || len(relPaths) == 0 {
		return nil
	}
	return withTx(func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(`DELETE FROM oneway_baseline WHERE jobId=? AND srcRoot=? AND dstRoot=? AND relPath=?`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, rel := range relPaths {
			rel = strings.TrimSpace(rel)
			if rel == "" {
				continue
			}
			if _, err := stmt.Exec(jobID, srcRoot, dstRoot, rel); err != nil {
				return err
			}
		}
		return nil
	})
}

// ClearOnewayBaseline deletes all tracked one-way baseline records for jobID.
func ClearOnewayBaseline(jobID int64) error {
	if jobID <= 0 {
		return nil
	}
	return ExecuteUpdate("DELETE FROM oneway_baseline WHERE jobId=?", jobID)
}
