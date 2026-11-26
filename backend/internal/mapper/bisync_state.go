package mapper

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

const (
	BisyncStateSynced    = "synced"
	BisyncStateTombstone = "tombstone"
	BisyncStateConflict  = "conflict"
	BisyncStateIgnored   = "ignored"
	BisyncStateError     = "error"
)

const (
	BisyncConflictContent     = "content"
	BisyncConflictCase        = "case"
	BisyncConflictType        = "type"
	BisyncConflictInvalidName = "invalid_name"
)

func bisyncSchemaStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS bisync_entry (
			jobId INTEGER NOT NULL,
			relPath TEXT NOT NULL,
			normKey TEXT NOT NULL,
			isDir INTEGER NOT NULL DEFAULT 0,
			state TEXT NOT NULL DEFAULT 'synced',
			aSize INTEGER NOT NULL DEFAULT 0,
			aModTime INTEGER NOT NULL DEFAULT 0,
			aHash TEXT NOT NULL DEFAULT '',
			aInode INTEGER NOT NULL DEFAULT 0,
			bSize INTEGER NOT NULL DEFAULT 0,
			bModTime INTEGER NOT NULL DEFAULT 0,
			bHash TEXT NOT NULL DEFAULT '',
			bObjectId TEXT NOT NULL DEFAULT '',
			syncedAt INTEGER NOT NULL DEFAULT 0,
			tombstoneAt INTEGER NOT NULL DEFAULT 0,
			failCount INTEGER NOT NULL DEFAULT 0,
			retryAt INTEGER NOT NULL DEFAULT 0,
			lastError TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (jobId, relPath)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_bisync_entry_norm ON bisync_entry(jobId, normKey)`,
		`CREATE INDEX IF NOT EXISTS idx_bisync_entry_state ON bisync_entry(jobId, state)`,
		`CREATE TABLE IF NOT EXISTS bisync_root_meta (
			jobId INTEGER NOT NULL,
			side TEXT NOT NULL,
			rootPath TEXT NOT NULL,
			storageSig TEXT NOT NULL DEFAULT '',
			fsidHi INTEGER NOT NULL DEFAULT 0,
			fsidLo INTEGER NOT NULL DEFAULT 0,
			lastKnownCount INTEGER NOT NULL DEFAULT 0,
			remoteCursor TEXT NOT NULL DEFAULT '',
			initializedAt INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (jobId, side)
		)`,
		`CREATE TABLE IF NOT EXISTS bisync_conflict (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			jobId INTEGER NOT NULL,
			relPath TEXT NOT NULL,
			conflictType TEXT NOT NULL,
			conflictCopyPath TEXT NOT NULL DEFAULT '',
			winnerSide TEXT NOT NULL DEFAULT '',
			aSize INTEGER NOT NULL DEFAULT 0,
			aModTime INTEGER NOT NULL DEFAULT 0,
			bSize INTEGER NOT NULL DEFAULT 0,
			bModTime INTEGER NOT NULL DEFAULT 0,
			detail TEXT NOT NULL DEFAULT '',
			resolved INTEGER NOT NULL DEFAULT 0,
			createdAt INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_bisync_conflict_job ON bisync_conflict(jobId, resolved, createdAt)`,
	}
}

// BisyncEntry is one per-path 3-way merge baseline row stored in SQLite.
type BisyncEntry struct {
	JobID       int64
	RelPath     string
	NormKey     string
	IsDir       bool
	State       string
	ASize       int64
	AModTime    int64
	AHash       string
	AInode      uint64
	BSize       int64
	BModTime    int64
	BHash       string
	BObjectID   string
	SyncedAt    int64
	TombstoneAt int64
	FailCount   int
	RetryAt     int64
	LastError   string
}

// BisyncRootMeta records root endpoint identity and mount/empty-root sentinels.
type BisyncRootMeta struct {
	JobID          int64
	Side           string // "A" or "B"
	RootPath       string
	StorageSig     string
	FsidHi         int32
	FsidLo         int32
	LastKnownCount int
	RemoteCursor   string
	InitializedAt  int64
}

// BisyncConflict tracks a surfaced conflict for the Conflict Center UI.
type BisyncConflict struct {
	ID                int64  `json:"id"`
	JobID             int64  `json:"jobId"`
	RelPath           string `json:"relPath"`
	ConflictType      string `json:"conflictType"`
	ConflictCopyPath  string `json:"conflictCopyPath"`
	WinnerSide        string `json:"winnerSide"`
	ASize             int64  `json:"aSize"`
	AModTime          int64  `json:"aModTime"`
	BSize             int64  `json:"bSize"`
	BModTime          int64  `json:"bModTime"`
	Detail            string `json:"detail"`
	Resolved          bool   `json:"resolved"`
	ResolutionWritten bool   `json:"-"`
	CreatedAt         int64  `json:"createdAt"`
}

func NormalizeBisyncKey(relPath string) string {
	return strings.ToLower(strings.Trim(relPath, "/"))
}

func scanBisyncEntry(row interface{ Scan(...interface{}) error }) (BisyncEntry, error) {
	var e BisyncEntry
	var isDir int
	var aInode int64
	err := row.Scan(
		&e.JobID, &e.RelPath, &e.NormKey, &isDir, &e.State,
		&e.ASize, &e.AModTime, &e.AHash, &aInode,
		&e.BSize, &e.BModTime, &e.BHash, &e.BObjectID,
		&e.SyncedAt, &e.TombstoneAt, &e.FailCount, &e.RetryAt, &e.LastError,
	)
	e.IsDir = isDir != 0
	if aInode > 0 {
		e.AInode = uint64(aInode)
	}
	return e, err
}

func ListBisyncEntries(jobID int64) (map[string]BisyncEntry, error) {
	rows, err := GetDB().Query(`SELECT jobId,relPath,normKey,isDir,state,
		aSize,aModTime,aHash,aInode,bSize,bModTime,bHash,bObjectId,
		syncedAt,tombstoneAt,failCount,retryAt,lastError
		FROM bisync_entry WHERE jobId=?`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]BisyncEntry)
	for rows.Next() {
		e, err := scanBisyncEntry(rows)
		if err != nil {
			return nil, err
		}
		out[e.RelPath] = e
	}
	return out, rows.Err()
}

func GetBisyncEntry(jobID int64, relPath string) (*BisyncEntry, error) {
	e, err := scanBisyncEntry(GetDB().QueryRow(`SELECT jobId,relPath,normKey,isDir,state,
		aSize,aModTime,aHash,aInode,bSize,bModTime,bHash,bObjectId,
		syncedAt,tombstoneAt,failCount,retryAt,lastError
		FROM bisync_entry WHERE jobId=? AND relPath=?`, jobID, relPath))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

const upsertBisyncEntrySQL = `INSERT INTO bisync_entry(
	jobId,relPath,normKey,isDir,state,
	aSize,aModTime,aHash,aInode,
	bSize,bModTime,bHash,bObjectId,
	syncedAt,tombstoneAt,failCount,retryAt,lastError
) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(jobId,relPath) DO UPDATE SET
	normKey=excluded.normKey,
	isDir=excluded.isDir,
	state=excluded.state,
	aSize=excluded.aSize,
	aModTime=excluded.aModTime,
	aHash=excluded.aHash,
	aInode=excluded.aInode,
	bSize=excluded.bSize,
	bModTime=excluded.bModTime,
	bHash=excluded.bHash,
	bObjectId=excluded.bObjectId,
	syncedAt=excluded.syncedAt,
	tombstoneAt=excluded.tombstoneAt,
	failCount=excluded.failCount,
	retryAt=excluded.retryAt,
	lastError=excluded.lastError`

func bisyncEntryArgs(e BisyncEntry) []interface{} {
	norm := e.NormKey
	if norm == "" {
		norm = NormalizeBisyncKey(e.RelPath)
	}
	state := e.State
	if state == "" {
		state = BisyncStateSynced
	}
	isDir := 0
	if e.IsDir {
		isDir = 1
	}
	return []interface{}{
		e.JobID, e.RelPath, norm, isDir, state,
		e.ASize, e.AModTime, e.AHash, int64(e.AInode),
		e.BSize, e.BModTime, e.BHash, e.BObjectID,
		e.SyncedAt, e.TombstoneAt, e.FailCount, e.RetryAt, e.LastError,
	}
}

func UpsertBisyncEntry(e BisyncEntry) error {
	return ExecuteUpdate(upsertBisyncEntrySQL, bisyncEntryArgs(e)...)
}

func UpsertBisyncEntriesBatch(entries []BisyncEntry) error {
	if len(entries) == 0 {
		return nil
	}
	return withTx(func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(upsertBisyncEntrySQL)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, e := range entries {
			if _, err := stmt.Exec(bisyncEntryArgs(e)...); err != nil {
				return err
			}
		}
		return nil
	})
}

func MarkBisyncTombstone(jobID int64, relPath string, isDir bool, now int64) error {
	if now <= 0 {
		now = time.Now().Unix()
	}
	return UpsertBisyncEntry(BisyncEntry{
		JobID:       jobID,
		RelPath:     relPath,
		NormKey:     NormalizeBisyncKey(relPath),
		IsDir:       isDir,
		State:       BisyncStateTombstone,
		TombstoneAt: now,
	})
}

func DeleteBisyncEntry(jobID int64, relPath string) error {
	return ExecuteUpdate("DELETE FROM bisync_entry WHERE jobId=? AND relPath=?", jobID, relPath)
}

func RecordBisyncEntryError(jobID int64, relPath string, isDir bool, errMsg string, now int64) error {
	if now <= 0 {
		now = time.Now().Unix()
	}
	return withTx(func(tx *sql.Tx) error {
		var failCount int
		err := tx.QueryRow("SELECT failCount FROM bisync_entry WHERE jobId=? AND relPath=?", jobID, relPath).Scan(&failCount)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		delay := int64(min(3600, 10<<min(failCount, 9)))
		retryAt := now + delay
		if errors.Is(err, sql.ErrNoRows) {
			isDirInt := 0
			if isDir {
				isDirInt = 1
			}
			_, err = tx.Exec(`INSERT INTO bisync_entry(jobId,relPath,normKey,isDir,state,failCount,retryAt,lastError)
				VALUES(?,?,?,?,?,?,?,?)`,
				jobID, relPath, NormalizeBisyncKey(relPath), isDirInt, BisyncStateError, 1, retryAt, errMsg)
			return err
		}
		_, err = tx.Exec(`UPDATE bisync_entry SET failCount=failCount+1, retryAt=?, lastError=? WHERE jobId=? AND relPath=?`,
			retryAt, errMsg, jobID, relPath)
		return err
	})
}

func PurgeExpiredBisyncTombstones(jobID int64, cutoff int64) error {
	return ExecuteUpdate("DELETE FROM bisync_entry WHERE jobId=? AND state=? AND tombstoneAt>0 AND tombstoneAt<?",
		jobID, BisyncStateTombstone, cutoff)
}

func GetBisyncRootMeta(jobID int64, side string) (*BisyncRootMeta, error) {
	var m BisyncRootMeta
	err := GetDB().QueryRow(`SELECT jobId,side,rootPath,storageSig,fsidHi,fsidLo,lastKnownCount,remoteCursor,initializedAt
		FROM bisync_root_meta WHERE jobId=? AND side=?`, jobID, side).Scan(
		&m.JobID, &m.Side, &m.RootPath, &m.StorageSig, &m.FsidHi, &m.FsidLo,
		&m.LastKnownCount, &m.RemoteCursor, &m.InitializedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func UpsertBisyncRootMeta(m BisyncRootMeta) error {
	return ExecuteUpdate(`INSERT INTO bisync_root_meta(
		jobId,side,rootPath,storageSig,fsidHi,fsidLo,lastKnownCount,remoteCursor,initializedAt
	) VALUES(?,?,?,?,?,?,?,?,?)
	ON CONFLICT(jobId,side) DO UPDATE SET
		rootPath=excluded.rootPath,
		storageSig=excluded.storageSig,
		fsidHi=excluded.fsidHi,
		fsidLo=excluded.fsidLo,
		lastKnownCount=excluded.lastKnownCount,
		remoteCursor=excluded.remoteCursor,
		initializedAt=excluded.initializedAt`,
		m.JobID, m.Side, m.RootPath, m.StorageSig, m.FsidHi, m.FsidLo,
		m.LastKnownCount, m.RemoteCursor, m.InitializedAt,
	)
}

func UpsertBisyncConflict(c BisyncConflict) (int64, error) {
	if c.CreatedAt <= 0 {
		c.CreatedAt = time.Now().Unix()
	}
	var id int64
	err := withTx(func(tx *sql.Tx) error {
		var existingID int64
		qErr := tx.QueryRow(`SELECT id FROM bisync_conflict
			WHERE jobId=? AND relPath=? AND conflictType=? AND resolved!=1 LIMIT 1`,
			c.JobID, c.RelPath, c.ConflictType).Scan(&existingID)
		if qErr == nil {
			id = existingID
			_, err := tx.Exec(`UPDATE bisync_conflict
				SET conflictCopyPath=?, winnerSide=?, aSize=?, aModTime=?, bSize=?, bModTime=?, detail=?, createdAt=?, resolved=0
				WHERE id=?`,
				c.ConflictCopyPath, c.WinnerSide, c.ASize, c.AModTime, c.BSize, c.BModTime, c.Detail, c.CreatedAt, existingID)
			return err
		}
		if !errors.Is(qErr, sql.ErrNoRows) {
			return qErr
		}
		res, err := tx.Exec(`INSERT INTO bisync_conflict(
			jobId,relPath,conflictType,conflictCopyPath,winnerSide,aSize,aModTime,bSize,bModTime,detail,resolved,createdAt
		) VALUES(?,?,?,?,?,?,?,?,?,?,0,?)`,
			c.JobID, c.RelPath, c.ConflictType, c.ConflictCopyPath, c.WinnerSide,
			c.ASize, c.AModTime, c.BSize, c.BModTime, c.Detail, c.CreatedAt)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

func ListBisyncConflicts(jobID int64, includeResolved bool) ([]BisyncConflict, error) {
	query := `SELECT id,jobId,relPath,conflictType,conflictCopyPath,winnerSide,aSize,aModTime,bSize,bModTime,detail,resolved,createdAt
		FROM bisync_conflict WHERE jobId=?`
	if !includeResolved {
		query += " AND resolved!=1"
	}
	query += " ORDER BY resolved=1 ASC, createdAt DESC, id DESC LIMIT 500"
	rows, err := GetDB().Query(query, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]BisyncConflict, 0)
	for rows.Next() {
		var c BisyncConflict
		var resolved int
		if err := rows.Scan(&c.ID, &c.JobID, &c.RelPath, &c.ConflictType, &c.ConflictCopyPath, &c.WinnerSide,
			&c.ASize, &c.AModTime, &c.BSize, &c.BModTime, &c.Detail, &resolved, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Resolved = resolved == 1
		c.ResolutionWritten = resolved == 2
		out = append(out, c)
	}
	return out, rows.Err()
}

func ListUnresolvedBisyncConflictCounts(jobIDs ...int64) (map[int64]int, error) {
	query := "SELECT jobId, COUNT(*) FROM bisync_conflict WHERE resolved!=1"
	var args []interface{}
	if len(jobIDs) > 0 {
		var clause string
		clause, args = int64InClause(jobIDs)
		query += " AND jobId IN (" + clause + ")"
	}
	rows, err := GetDB().Query(query+" GROUP BY jobId", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]int)
	for rows.Next() {
		var jobID int64
		var count int
		if err := rows.Scan(&jobID, &count); err != nil {
			return nil, err
		}
		out[jobID] = count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func GetBisyncConflictByID(id int64) (*BisyncConflict, error) {
	var c BisyncConflict
	var resolved int
	err := GetDB().QueryRow(`SELECT id,jobId,relPath,conflictType,conflictCopyPath,winnerSide,aSize,aModTime,bSize,bModTime,detail,resolved,createdAt
		FROM bisync_conflict WHERE id=?`, id).Scan(
		&c.ID, &c.JobID, &c.RelPath, &c.ConflictType, &c.ConflictCopyPath, &c.WinnerSide,
		&c.ASize, &c.AModTime, &c.BSize, &c.BModTime, &c.Detail, &resolved, &c.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.Resolved = resolved == 1
	c.ResolutionWritten = resolved == 2
	return &c, nil
}

func ResolveBisyncConflict(id int64) error {
	return ExecuteUpdate("UPDATE bisync_conflict SET resolved=1 WHERE id=?", id)
}

// MarkBisyncConflictResolutionWritten keeps a conflict pending while its verified copies are cleaned up.
func MarkBisyncConflictResolutionWritten(id int64) error {
	return ExecuteUpdate("UPDATE bisync_conflict SET resolved=2 WHERE id=? AND resolved=0", id)
}

func ResolveBisyncConflictsByPath(jobID int64, relPath string) error {
	return ExecuteUpdate("UPDATE bisync_conflict SET resolved=1 WHERE jobId=? AND relPath=? AND resolved=0", jobID, relPath)
}

func ClearBisyncJobState(jobID int64) error {
	return withTx(func(tx *sql.Tx) error {
		for _, table := range []string{"bisync_entry", "bisync_root_meta", "bisync_conflict"} {
			if _, err := tx.Exec("DELETE FROM "+table+" WHERE jobId=?", jobID); err != nil {
				return err
			}
		}
		return nil
	})
}
