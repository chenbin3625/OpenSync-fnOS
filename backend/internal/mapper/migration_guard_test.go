package mapper

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"opensync/internal/config"
)

func mustExec(t *testing.T, db *sql.DB, query string, args ...interface{}) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("exec %q: %v", query, err)
	}
}

func mustCount(t *testing.T, db *sql.DB, query string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return n
}

// errorInspector simulates a schema that cannot be read (locked/corrupt DB).
type errorInspector struct{ err error }

func (e errorInspector) tableExists(string) (bool, error)            { return false, e.err }
func (e errorInspector) tableHasColumn(string, string) (bool, error) { return false, e.err }

// P1-1: a guard that cannot read the schema must return an error instead of
// being folded into "already applied".
func TestMigrationGuardReturnsQueryErrors(t *testing.T) {
	db := newFileTestDB(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	// Every query on a finished transaction fails; the guards must surface it.
	insp := txSchemaInspector{tx: tx}
	for _, stmt := range []string{
		"ALTER TABLE job ADD COLUMN remark text",
		"ALTER TABLE job DROP COLUMN cron",
		"ALTER TABLE job RENAME COLUMN speed TO useCacheT",
		"INSERT INTO job_task_item_fts(job_task_item_fts) VALUES('rebuild')",
	} {
		if _, err := shouldSkipMigrationStatement(insp, stmt, false); err == nil {
			t.Fatalf("shouldSkipMigrationStatement(%q) error = nil after the transaction finished, want a guard error", stmt)
		}
	}
}

// P1-1: when a guard fails, the migration must abort without reaching the
// version bump, so the next start retries the migration.
func TestMigrateAbortsWithoutAdvancingVersionWhenGuardFails(t *testing.T) {
	db := newFileTestDB(t)
	mustExec(t, db, "CREATE TABLE schema_version(version INTEGER NOT NULL)")
	mustExec(t, db, "INSERT INTO schema_version(version) VALUES(1)")

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	guardErr := errors.New("simulated schema introspection failure")
	stmts := []string{
		"ALTER TABLE job ADD COLUMN remark text",
		fmt.Sprintf("UPDATE schema_version SET version=%d", currentVersion),
	}
	err = migrateStatementsInTx(tx, stmts, errorInspector{err: guardErr}, false)
	if !errors.Is(err, guardErr) {
		_ = tx.Rollback()
		t.Fatalf("migrateStatementsInTx error = %v, want the guard error", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := mustCount(t, db, "SELECT version FROM schema_version"); got != 1 {
		t.Fatalf("schema version = %d, want the old version 1: the version bump ran despite the aborted migration", got)
	}
}

// P2-6: replaying the migrations because the version read as 0 must not rewrite
// real job settings on a schema that never had the legacy speed column.
func TestMigrateDBTxKeepsJobSettingsWhenLegacySpeedColumnNeverExisted(t *testing.T) {
	db := newFileTestDB(t)
	mustExec(t, db, "CREATE TABLE job(id integer primary key autoincrement, useCacheT integer DEFAULT 0, scanIntervalT integer DEFAULT 0)")
	mustExec(t, db, "INSERT INTO job(id, useCacheT, scanIntervalT) VALUES(1, 2, 7)")

	if err := migrateDBTx(db, 0); err != nil {
		t.Fatalf("migrateDBTx(0) error = %v", err)
	}
	if got := mustCount(t, db, "SELECT useCacheT FROM job WHERE id=1"); got != 2 {
		t.Fatalf("useCacheT = %d, want the user's 2: the legacy data migration was replayed on a current schema", got)
	}
	if got := mustCount(t, db, "SELECT scanIntervalT FROM job WHERE id=1"); got != 7 {
		t.Fatalf("scanIntervalT = %d, want the user's 7", got)
	}
}

// P2-6: the same migration must still run for a genuine legacy database whose
// speed=2 meant "use cache".
func TestMigrateDBTxRewritesLegacySpeedValue(t *testing.T) {
	db := newFileTestDB(t)
	mustExec(t, db, "CREATE TABLE job(id integer primary key autoincrement, speed integer DEFAULT 0)")
	mustExec(t, db, "INSERT INTO job(id, speed) VALUES(1, 2)")

	if err := migrateDBTx(db, 0); err != nil {
		t.Fatalf("migrateDBTx(0) error = %v", err)
	}
	if got := mustCount(t, db, "SELECT useCacheT FROM job WHERE id=1"); got != 0 {
		t.Fatalf("useCacheT = %d, want 0 after migrating legacy speed=2", got)
	}
	if got := mustCount(t, db, "SELECT scanIntervalT FROM job WHERE id=1"); got != 10 {
		t.Fatalf("scanIntervalT = %d, want 10 after migrating legacy speed=2", got)
	}
}

// P3-16: a NULL legacy version means "unknown/old", not a fatal driver error.
func TestSchemaVersionTreatsNullValuesAsZero(t *testing.T) {
	t.Run("null schema_version row", func(t *testing.T) {
		db := newFileTestDB(t)
		mustExec(t, db, "CREATE TABLE schema_version(version INTEGER)")
		mustExec(t, db, "INSERT INTO schema_version(version) VALUES(NULL)")
		got, err := schemaVersion(db)
		if err != nil || got != 0 {
			t.Fatalf("schemaVersion() = %d, %v; want 0, nil", got, err)
		}
	})
	t.Run("null legacy sqlVersion", func(t *testing.T) {
		db := newFileTestDB(t)
		mustExec(t, db, "CREATE TABLE user_list(id integer primary key, sqlVersion integer)")
		mustExec(t, db, "INSERT INTO user_list(id, sqlVersion) VALUES(1, NULL)")
		got, err := schemaVersion(db)
		if err != nil || got != 0 {
			t.Fatalf("schemaVersion() = %d, %v; want 0, nil", got, err)
		}
	})
}

// P3-12: an end-to-end upgrade of a real legacy database must migrate the
// schema, keep the user's job data and drop the legacy auth table.
func TestInitSQLUpgradesLegacyDatabaseAndKeepsData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	for _, stmt := range []string{
		`CREATE TABLE user_list(id integer primary key autoincrement, userName text, passwd text, sqlVersion integer)`,
		`INSERT INTO user_list(userName, passwd, sqlVersion) VALUES('admin', 'x', 240730)`,
		`CREATE TABLE job(
			id integer primary key autoincrement, enable integer, remark text, srcPath text, dstPath text,
			alistId integer, speed integer, method integer, interval integer)`,
		`INSERT INTO job(id, enable, remark, srcPath, dstPath, alistId, speed, method, interval)
		 VALUES(7, 1, 'legacy job', '/src', '/dst', 1, 2, 0, 5)`,
		`CREATE TABLE job_task(id integer primary key autoincrement, jobId integer, status integer, runTime integer, createTime integer)`,
		`INSERT INTO job_task(id, jobId, status, runTime) VALUES(11, 7, 2, 100)`,
		`CREATE TABLE alist_list(id integer primary key autoincrement, url text, token text)`,
		`CREATE TABLE notify(id integer primary key autoincrement, enable integer, method integer, params text)`,
	} {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("legacy setup %q: %v", stmt, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	resetGlobalDBForTest(t, &config.Config{
		DB:     config.DBConfig{DBName: path},
		Server: config.ServerConfig{PasswdStr: "legacy-upgrade-test"},
	})
	InitSQL()

	db := GetDB()
	if got := mustCount(t, db, "SELECT version FROM schema_version"); got != currentVersion {
		t.Fatalf("schema version = %d, want %d", got, currentVersion)
	}
	if ok, err := dbTableExists(db, "user_list"); err != nil || ok {
		t.Fatalf("user_list exists = %v, err = %v; want dropped", ok, err)
	}
	rows, err := FetchAllToTable("SELECT * FROM job WHERE id=7")
	if err != nil || len(rows) != 1 {
		t.Fatalf("legacy job row = %#v, err = %v; want it preserved", rows, err)
	}
	job := rows[0]
	if job["remark"] != "legacy job" || job["srcPath"] != "/src" || job["dstPath"] != "/dst" {
		t.Fatalf("legacy job data was lost: %#v", job)
	}
	if _, ok := job["speed"]; ok {
		t.Fatalf("legacy speed column was not renamed: %#v", job)
	}
	if job["useCacheT"] != int64(0) {
		t.Fatalf("migrated useCacheT = %#v, want 0 (legacy speed=2 means use cache)", job["useCacheT"])
	}
	for _, column := range []string{"errMsg", "taskNum"} {
		if ok, err := hasColumnErr(db, "job_task", column); err != nil || !ok {
			t.Fatalf("job_task column %q exists = %v, err = %v", column, ok, err)
		}
	}
	if ok, err := hasColumnErr(db, "notify", "lastSendStatus"); err != nil || !ok {
		t.Fatalf("notify.lastSendStatus exists = %v, err = %v", ok, err)
	}
}
