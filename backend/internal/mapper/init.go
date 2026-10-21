package mapper

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
)

const currentVersion = 260922

// InitSQL initializes the database schema and runs migrations
func InitSQL() {
	db := GetDB()

	// Check if schema already exists (schema_version for new installs, user_list for legacy)
	var name string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name IN ('schema_version', 'user_list') ORDER BY name LIMIT 1").Scan(&name)

	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			// A transient failure (locked DB, I/O error) must not be mistaken
			// for an empty database, or the "first run" branch would try to
			// re-create the schema on top of existing data.
			log.Fatalf("Failed to inspect database schema: %v", err)
		}
		// First run - create all tables
		stmts := []string{
			fmt.Sprintf(`CREATE TABLE schema_version(version INTEGER NOT NULL DEFAULT %d)`, currentVersion),
			fmt.Sprintf(`INSERT INTO schema_version(version) VALUES(%d)`, currentVersion),

			`CREATE TABLE alist_list(
				id integer primary key autoincrement,
				remark text,
				url text,
				userName text,
				token text,
				createTime integer DEFAULT (strftime('%s', 'now')),
				UNIQUE (url, userName)
			)`,

			`CREATE TABLE job(
				id integer primary key autoincrement,
				enable integer DEFAULT 1,
				remark text,
				srcPath text,
				dstPath text,
				alistId integer,
				useCacheT integer DEFAULT 0,
				scanIntervalT integer DEFAULT 0,
				useCacheS integer DEFAULT 0,
				scanIntervalS integer DEFAULT 0,
				method integer,
				interval integer,
				isCron integer DEFAULT 0,
				month text DEFAULT NULL,
				day text DEFAULT NULL,
				day_of_week text DEFAULT NULL,
				hour text DEFAULT NULL,
				minute text DEFAULT NULL,
				second text DEFAULT NULL,
				exclude text DEFAULT NULL,
				minFileSize integer DEFAULT 0,
				maxFileSize integer DEFAULT 0,
				createTime integer DEFAULT (strftime('%s', 'now')),
				UNIQUE (srcPath, dstPath, alistId)
			)`,

			`CREATE TABLE job_task(
				id integer primary key autoincrement,
				jobId integer,
				status integer DEFAULT 1,
				errMsg text,
				runTime integer,
				taskNum text,
				createTime integer DEFAULT (strftime('%s', 'now'))
			)`,

			`CREATE TABLE job_task_item(
				id integer primary key autoincrement,
				taskId integer,
				srcPath text,
				dstPath text,
				isPath integer DEFAULT 0,
				fileName text,
				fileSize integer,
				type integer,
				alistTaskId text,
				status integer DEFAULT 0,
				progress real,
				errMsg text,
				createTime integer DEFAULT (strftime('%s', 'now'))
			)`,
		}

		stmts = append(stmts, jobTaskItemFTSStatements(false)...)
		stmts = append(stmts,

			`CREATE TABLE notify(
				id integer primary key autoincrement,
				enable integer DEFAULT 1,
				method integer,
				params text,
				lastSendStatus integer DEFAULT 0,
				lastSendTime integer DEFAULT 0,
				lastSendError text DEFAULT NULL,
				createTime integer DEFAULT (strftime('%s', 'now'))
			)`,
		)

		tx, err := db.Begin()
		if err != nil {
			log.Fatalf("Failed to begin transaction for database initialization: %v", err)
		}
		for _, stmt := range stmts {
			if _, err := tx.Exec(stmt); err != nil {
				_ = tx.Rollback()
				log.Fatalf("Failed to initialize database: %v\nSQL: %s", err, stmt)
			}
		}
		if err := tx.Commit(); err != nil {
			log.Fatalf("Failed to commit database initialization: %v", err)
		}
		ensureIndexes(db)
		if err := ensureJobTaskItemFTSHealth(db); err != nil {
			log.Fatalf("Failed to verify the task item search index: %v", err)
		}
		if err := migrateStoredCredentials(db); err != nil {
			log.Fatalf("Failed to initialize credential encryption: %v", err)
		}

		log.Printf("Database initialized")
		return
	}

	// Existing database - check version and migrate if needed. A fresh schema
	// can exist without any user rows when the server was stopped before web
	// setup completed. schemaVersion() returns 0 in that case so the idempotent
	// migrations run (a no-op on an already-current schema), which is important
	// when the schema was created by an older binary missing newer columns.
	sqlVersion, err := schemaVersion(db)
	if err != nil {
		// Guessing 0 here would replay every migration on top of a current
		// schema. The guards make replaying safe except for the legacy
		// scanInterval data update, which migrationStatements only emits when
		// the old speed column existed (see shouldSkipMigrationStatement), so a
		// transient error must not be mistaken for an old database.
		log.Fatalf("Failed to read database schema version: %v", err)
	}

	if sqlVersion < int64(currentVersion) {
		if err := migrateDB(sqlVersion); err != nil {
			log.Fatalf("Failed to migrate database from version %d to %d: %v", sqlVersion, currentVersion, err)
		}
	}
	ensureIndexes(db)
	// A previous migration may have skipped the FTS table or its triggers (the
	// guards used to swallow query errors), which silently stops keyword search
	// from seeing new rows. Repair it instead of serving a stale index.
	if err := ensureJobTaskItemFTSHealth(db); err != nil {
		log.Fatalf("Failed to verify the task item search index: %v", err)
	}
	if err := migrateStoredCredentials(db); err != nil {
		log.Fatalf("Failed to migrate stored credentials: %v", err)
	}
}

// schemaVersion reads the recorded schema version. Only a missing table, a
// missing row, or a row whose version is NULL means "version 0" (a
// pre-schema_version database, or one stopped before its first write); every
// other failure is returned so InitSQL stops instead of re-running migrations
// against a schema it could not read.
func schemaVersion(db *sql.DB) (int64, error) {
	var version sql.NullInt64
	err := db.QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version)
	if err == nil {
		if !version.Valid {
			return 0, nil
		}
		return version.Int64, nil
	}
	if !isNoSuchTableOrNoRows(err) {
		return 0, err
	}
	// Fall back to legacy user_list for databases not yet migrated.
	legacy, err := tableHasColumnDBErr(db, "user_list", "sqlVersion")
	if err != nil {
		return 0, err
	}
	if !legacy {
		return 0, nil
	}
	// sqlVersion is nullable on legacy tables: a NULL (or non-integer) value
	// means "unknown/old" and must not abort startup with a raw driver error.
	err = db.QueryRow("SELECT sqlVersion FROM user_list LIMIT 1").Scan(&version)
	if err == nil {
		if !version.Valid {
			return 0, nil
		}
		return version.Int64, nil
	}
	if isNoSuchTableOrNoRows(err) {
		return 0, nil
	}
	return 0, err
}

func isNoSuchTableOrNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "no such table")
}

func ensureIndexes(db *sql.DB) {
	indexes := []string{
		"CREATE INDEX IF NOT EXISTS idx_job_task_job_time ON job_task(jobId, createTime DESC, id DESC)",
		"CREATE INDEX IF NOT EXISTS idx_job_task_status_job ON job_task(status, jobId)",
		"CREATE INDEX IF NOT EXISTS idx_job_task_effective_time ON job_task(COALESCE(NULLIF(runTime, 0), createTime), id)",
		"CREATE INDEX IF NOT EXISTS idx_job_task_item_task_time ON job_task_item(taskId, createTime DESC, id DESC)",
		"CREATE INDEX IF NOT EXISTS idx_job_task_item_task_status ON job_task_item(taskId, status)",
		"CREATE INDEX IF NOT EXISTS idx_job_task_item_task_status_time ON job_task_item(taskId, status, createTime DESC, id DESC)",
		// The time suffix lets type/object-filtered detail pages stream rows in
		// create order without scanning every item for the task and sorting the
		// matches. The composite type index supersedes the old type-only index.
		"CREATE INDEX IF NOT EXISTS idx_job_task_item_task_type_time ON job_task_item(taskId, type, createTime DESC)",
		"DROP INDEX IF EXISTS idx_job_task_item_task_type",
		"CREATE INDEX IF NOT EXISTS idx_job_task_item_task_is_path_time ON job_task_item(taskId, isPath, createTime DESC)",
		// Serves the retry replay cursor (taskId=? AND id>? ORDER BY id) over a
		// multi-status IN list, and the per-task COUNT(*) behind the detail page
		// total. Without it the cursor walks the rowid across every task's items.
		"CREATE INDEX IF NOT EXISTS idx_job_task_item_task_id ON job_task_item(taskId, id)",
	}
	for _, stmt := range indexes {
		if _, err := db.Exec(stmt); err != nil {
			// Non-fatal (the schema works without indexes), but loud: silent
			// degradation would show up only as unexplained slowness.
			log.Printf("WARNING: index creation failed, query performance may degrade: %v\nSQL: %s", err, stmt)
		}
	}
}

// rowQuerier is satisfied by *sql.DB and *sql.Tx and is used for single-row
// schema introspection.
type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// sqliteObjectExists reports whether sqlite_master has an object of the given
// type and name. Any query failure is returned, never folded into "missing".
func sqliteObjectExists(q rowQuerier, objectType, name string) (bool, error) {
	var found string
	err := q.QueryRow("SELECT name FROM sqlite_master WHERE type=? AND name=?", objectType, name).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return found == name, nil
}

func dbTableExists(db *sql.DB, table string) (bool, error) {
	return sqliteObjectExists(db, "table", table)
}

// jobTaskItemFTSTriggers are the three triggers that keep the external-content
// FTS index in sync with job_task_item. A virtual table without them accepts
// queries but silently stops indexing new rows.
var jobTaskItemFTSTriggers = []string{"job_task_item_ai", "job_task_item_ad", "job_task_item_au"}

// ftsObjectsHealthy reports whether the FTS table and all sync triggers exist.
func ftsObjectsHealthy(q rowQuerier) (bool, error) {
	exists, err := sqliteObjectExists(q, "table", "job_task_item_fts")
	if err != nil || !exists {
		return false, err
	}
	for _, trigger := range jobTaskItemFTSTriggers {
		exists, err := sqliteObjectExists(q, "trigger", trigger)
		if err != nil || !exists {
			return false, err
		}
	}
	return true, nil
}

// ensureJobTaskItemFTSHealth repairs an incomplete full-text index. Older
// builds could skip the FTS migration statements when a guard query failed
// (a missing table, or a missing trigger, does not break anything else, so the
// omission went unnoticed and keyword search silently missed rows forever).
// Repairing is a one-off full rebuild; on a healthy database it is a few
// sqlite_master lookups.
func ensureJobTaskItemFTSHealth(db *sql.DB) error {
	itemExists, err := dbTableExists(db, "job_task_item")
	if err != nil {
		return err
	}
	if !itemExists {
		return nil
	}
	healthy, err := ftsObjectsHealthy(db)
	if err != nil {
		return err
	}
	if healthy {
		return nil
	}
	log.Printf("WARNING: the task item search index is incomplete; rebuilding job_task_item_fts")
	return retryOnBusy(func() error {
		return runTxOn(db, nil, func(tx *sql.Tx) error {
			for _, stmt := range rebuildJobTaskItemFTSStatements() {
				if _, err := tx.Exec(stmt); err != nil {
					return fmt.Errorf("rebuild task item search index: %w\nSQL: %s", err, stmt)
				}
			}
			return nil
		})
	})
}

// legacyScanIntervalUpdate is the only data migration whose meaning depends on
// the legacy schema: value 2 in the old `speed` column meant "use cache". It is
// a named constant so shouldSkipMigrationStatement can match it exactly and
// skip it outside a genuine legacy migration.
const legacyScanIntervalUpdate = "UPDATE job SET scanIntervalT = 10, useCacheT = 0 WHERE useCacheT = 2"

func migrationStatements(fromVersion int64) []string {
	var stmts []string
	if fromVersion < 240731 {
		stmts = append(stmts,
			fmt.Sprintf("ALTER TABLE user_list ADD COLUMN sqlVersion integer DEFAULT %d", currentVersion),
			"ALTER TABLE job_task ADD COLUMN errMsg text",
		)
	}
	if fromVersion < 240813 {
		stmts = append(stmts,
			"ALTER TABLE job ADD COLUMN isCron integer DEFAULT 0",
			"ALTER TABLE job ADD COLUMN month text DEFAULT NULL",
			"ALTER TABLE job ADD COLUMN day text DEFAULT NULL",
			"ALTER TABLE job ADD COLUMN day_of_week text DEFAULT NULL",
			"ALTER TABLE job ADD COLUMN hour text DEFAULT NULL",
			"ALTER TABLE job ADD COLUMN minute text DEFAULT NULL",
			"ALTER TABLE job ADD COLUMN second text DEFAULT NULL",
		)
	}
	if fromVersion < 240905 {
		stmts = append(stmts, "ALTER TABLE job ADD COLUMN exclude text DEFAULT NULL")
	}
	if fromVersion < 241014 {
		stmts = append(stmts, `CREATE TABLE IF NOT EXISTS notify(
			id integer primary key autoincrement,
			enable integer DEFAULT 1,
			method integer,
			params text,
			createTime integer DEFAULT (strftime('%s', 'now'))
		)`)
	}
	if fromVersion < 250307 {
		stmts = append(stmts, "ALTER TABLE job_task ADD COLUMN taskNum text")
	}
	if fromVersion < 250416 {
		stmts = append(stmts, "ALTER TABLE job ADD COLUMN remark text")
	}
	if fromVersion < 250520 {
		stmts = append(stmts, "ALTER TABLE job_task_item ADD COLUMN isPath integer DEFAULT 0")
	}
	if fromVersion < 250608 {
		stmts = append(stmts,
			"ALTER TABLE job RENAME COLUMN speed TO useCacheT",
			"ALTER TABLE job ADD COLUMN scanIntervalT integer DEFAULT 0",
			"ALTER TABLE job ADD COLUMN useCacheS integer DEFAULT 0",
			"ALTER TABLE job ADD COLUMN scanIntervalS integer DEFAULT 0",
			legacyScanIntervalUpdate,
		)
	}
	if fromVersion < 260605 {
		stmts = append(stmts,
			"ALTER TABLE job ADD COLUMN minFileSize integer DEFAULT 0",
			"ALTER TABLE job ADD COLUMN maxFileSize integer DEFAULT 0",
		)
	}
	if fromVersion < 260606 {
		stmts = append(stmts,
			"ALTER TABLE job DROP COLUMN year",
			"ALTER TABLE job DROP COLUMN week",
			"ALTER TABLE job DROP COLUMN start_date",
			"ALTER TABLE job DROP COLUMN end_date",
		)
	}
	if fromVersion < 260611 {
		stmts = append(stmts, jobTaskItemFTSStatements(true)...)
	}
	if fromVersion < 260612 {
		stmts = append(stmts, "ALTER TABLE user_list ADD COLUMN recoveryKey text")
	}
	if fromVersion < 260613 {
		// Drop the legacy single 'cron' column carried over from the earlier
		// Python implementation; scheduling now uses the dedicated isCron +
		// second/minute/hour/day/month/day_of_week fields.
		stmts = append(stmts, "ALTER TABLE job DROP COLUMN cron")
	}
	if fromVersion >= 260611 && fromVersion < 260614 {
		stmts = append(stmts, rebuildJobTaskItemFTSStatements()...)
	}
	if fromVersion < 260920 {
		// Migrate version tracking from user_list to dedicated schema_version table,
		// then drop user_list (auth is now handled by the fnOS gateway).
		stmts = append(stmts,
			"CREATE TABLE IF NOT EXISTS schema_version(version INTEGER NOT NULL)",
			"DELETE FROM schema_version",
			fmt.Sprintf("INSERT INTO schema_version(version) VALUES(%d)", currentVersion),
			"DROP TABLE IF EXISTS user_list",
		)
	}
	if fromVersion < 260922 {
		// Delivery outcome per notify config. Task-completion sends used to fail
		// with nothing but a log line, so the UI showed a healthy config while
		// every message was being rejected.
		stmts = append(stmts,
			"ALTER TABLE notify ADD COLUMN lastSendStatus integer DEFAULT 0",
			"ALTER TABLE notify ADD COLUMN lastSendTime integer DEFAULT 0",
			"ALTER TABLE notify ADD COLUMN lastSendError text DEFAULT NULL",
		)
	}
	stmts = append(stmts, fmt.Sprintf("UPDATE schema_version SET version=%d", currentVersion))
	return stmts
}

type sqlExecer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

func installJobTaskItemFTS(exec sqlExecer, rebuild bool) error {
	for _, stmt := range jobTaskItemFTSStatements(rebuild) {
		if _, err := exec.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func rebuildJobTaskItemFTSStatements() []string {
	stmts := []string{
		"DROP TRIGGER IF EXISTS job_task_item_ai",
		"DROP TRIGGER IF EXISTS job_task_item_ad",
		"DROP TRIGGER IF EXISTS job_task_item_au",
		"DROP TABLE IF EXISTS job_task_item_fts",
	}
	return append(stmts, jobTaskItemFTSStatements(true)...)
}

func jobTaskItemFTSStatements(rebuild bool) []string {
	stmts := []string{
		`CREATE VIRTUAL TABLE IF NOT EXISTS job_task_item_fts USING fts5(
			fileName,
			srcPath,
			dstPath,
			content='job_task_item',
			content_rowid='id',
			tokenize='trigram'
		)`,
	}
	if rebuild {
		stmts = append(stmts, "INSERT INTO job_task_item_fts(job_task_item_fts) VALUES('rebuild')")
	}
	stmts = append(stmts,
		`CREATE TRIGGER IF NOT EXISTS job_task_item_ai AFTER INSERT ON job_task_item BEGIN
			INSERT INTO job_task_item_fts(rowid, fileName, srcPath, dstPath)
			VALUES (new.id, new.fileName, new.srcPath, new.dstPath);
		END`,
		`CREATE TRIGGER IF NOT EXISTS job_task_item_ad AFTER DELETE ON job_task_item BEGIN
			INSERT INTO job_task_item_fts(job_task_item_fts, rowid, fileName, srcPath, dstPath)
			VALUES ('delete', old.id, old.fileName, old.srcPath, old.dstPath);
		END`,
		`CREATE TRIGGER IF NOT EXISTS job_task_item_au AFTER UPDATE ON job_task_item BEGIN
			INSERT INTO job_task_item_fts(job_task_item_fts, rowid, fileName, srcPath, dstPath)
			VALUES ('delete', old.id, old.fileName, old.srcPath, old.dstPath);
			INSERT INTO job_task_item_fts(rowid, fileName, srcPath, dstPath)
			VALUES (new.id, new.fileName, new.srcPath, new.dstPath);
		END`,
	)
	return stmts
}

// migrateDB runs database migrations
func migrateDB(fromVersion int64) error {
	if err := migrateDBTx(GetDB(), fromVersion); err != nil {
		return err
	}
	log.Printf("Database migrated from version %d to %d", fromVersion, currentVersion)
	return nil
}

// schemaInspector answers the existence questions the migration guards need.
// It is an interface so the guard error path (a schema that cannot be read)
// can be tested without corrupting a real database.
type schemaInspector interface {
	tableExists(table string) (bool, error)
	tableHasColumn(table, column string) (bool, error)
}

type txSchemaInspector struct {
	tx *sql.Tx
}

func (i txSchemaInspector) tableExists(table string) (bool, error) {
	return txTableExists(i.tx, table)
}

func (i txSchemaInspector) tableHasColumn(table, column string) (bool, error) {
	return hasColumnErr(i.tx, table, column)
}

func migrateDBTx(db *sql.DB, fromVersion int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if err := migrateInTx(tx, fromVersion); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// migrateInTx runs the migrations for fromVersion inside an open transaction.
// The version row is only advanced by the trailing UPDATE statement in the
// statement list, so any error aborts (and rolls back) the whole migration and
// leaves the old version in place for the next startup to retry.
func migrateInTx(tx *sql.Tx, fromVersion int64) error {
	insp := txSchemaInspector{tx: tx}
	legacySpeed, err := insp.tableHasColumn("job", "speed")
	if err != nil {
		return fmt.Errorf("migration: inspect job.speed: %w", err)
	}
	return migrateStatementsInTx(tx, migrationStatements(fromVersion), insp, legacySpeed)
}

// migrateStatementsInTx executes stmts, consulting the guards first. It is
// separated from migrateDBTx so tests can feed it a failing inspector.
func migrateStatementsInTx(tx *sql.Tx, stmts []string, insp schemaInspector, legacySpeed bool) error {
	for _, stmt := range stmts {
		skip, err := shouldSkipMigrationStatement(insp, stmt, legacySpeed)
		if err != nil {
			// A guard that could not read the schema must abort the migration.
			// Skipping the statement instead would commit the transaction
			// (including the version bump) and the schema gap would never be
			// repaired on a later start.
			return fmt.Errorf("migration guard failed for %q: %w", stmt, err)
		}
		if skip {
			continue
		}
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("migration SQL failed: %w\nSQL: %s", err, stmt)
		}
	}
	return nil
}

func shouldSkipMigrationStatement(insp schemaInspector, stmt string, legacySpeed bool) (bool, error) {
	if tableName, columnName, ok := parseAlterTableAddColumn(stmt); ok {
		// Skip if the target table does not exist (e.g. user_list after migration to schema_version)
		// or if the column is already present.
		exists, err := insp.tableExists(tableName)
		if err != nil {
			return false, err
		}
		if !exists {
			return true, nil
		}
		return insp.tableHasColumn(tableName, columnName)
	}

	switch stmt {
	case "ALTER TABLE job RENAME COLUMN speed TO useCacheT":
		hasSpeed, err := insp.tableHasColumn("job", "speed")
		if err != nil {
			return false, err
		}
		if hasSpeed {
			return false, nil
		}
		return insp.tableHasColumn("job", "useCacheT")
	case legacyScanIntervalUpdate:
		// This is the only data migration whose meaning depends on the old
		// schema: value 2 in the legacy `speed` column meant "use cache". On a
		// database that never had that column (e.g. a current schema whose
		// version row was lost and reads as 0) replaying it would silently
		// disable caching and reset the scan interval of real jobs.
		return !legacySpeed, nil
	case "ALTER TABLE job DROP COLUMN cron":
		has, err := insp.tableHasColumn("job", "cron")
		return !has, err
	case "ALTER TABLE job DROP COLUMN year":
		has, err := insp.tableHasColumn("job", "year")
		return !has, err
	case "ALTER TABLE job DROP COLUMN week":
		has, err := insp.tableHasColumn("job", "week")
		return !has, err
	case "ALTER TABLE job DROP COLUMN start_date":
		has, err := insp.tableHasColumn("job", "start_date")
		return !has, err
	case "ALTER TABLE job DROP COLUMN end_date":
		has, err := insp.tableHasColumn("job", "end_date")
		return !has, err
	case `CREATE VIRTUAL TABLE IF NOT EXISTS job_task_item_fts USING fts5(
			fileName,
			srcPath,
			dstPath,
			content='job_task_item',
			content_rowid='id',
			tokenize='trigram'
		)`:
		exists, err := insp.tableExists("job_task_item")
		return !exists, err
	case "INSERT INTO job_task_item_fts(job_task_item_fts) VALUES('rebuild')":
		exists, err := insp.tableExists("job_task_item_fts")
		return !exists, err
	case `CREATE TRIGGER IF NOT EXISTS job_task_item_ai AFTER INSERT ON job_task_item BEGIN
			INSERT INTO job_task_item_fts(rowid, fileName, srcPath, dstPath)
			VALUES (new.id, new.fileName, new.srcPath, new.dstPath);
		END`:
		return skipTriggerWithoutTables(insp)
	case `CREATE TRIGGER IF NOT EXISTS job_task_item_ad AFTER DELETE ON job_task_item BEGIN
			INSERT INTO job_task_item_fts(job_task_item_fts, rowid, fileName, srcPath, dstPath)
			VALUES ('delete', old.id, old.fileName, old.srcPath, old.dstPath);
		END`:
		return skipTriggerWithoutTables(insp)
	case `CREATE TRIGGER IF NOT EXISTS job_task_item_au AFTER UPDATE ON job_task_item BEGIN
			INSERT INTO job_task_item_fts(job_task_item_fts, rowid, fileName, srcPath, dstPath)
			VALUES ('delete', old.id, old.fileName, old.srcPath, old.dstPath);
			INSERT INTO job_task_item_fts(rowid, fileName, srcPath, dstPath)
			VALUES (new.id, new.fileName, new.srcPath, new.dstPath);
		END`:
		return skipTriggerWithoutTables(insp)
	default:
		return false, nil
	}
}

// skipTriggerWithoutTables skips an FTS trigger when either table it needs is
// absent (legacy databases that do not have the item table yet).
func skipTriggerWithoutTables(insp schemaInspector) (bool, error) {
	itemExists, err := insp.tableExists("job_task_item")
	if err != nil {
		return false, err
	}
	if !itemExists {
		return true, nil
	}
	ftsExists, err := insp.tableExists("job_task_item_fts")
	if err != nil {
		return false, err
	}
	return !ftsExists, nil
}

func parseAlterTableAddColumn(stmt string) (string, string, bool) {
	fields := strings.Fields(stmt)
	if len(fields) < 6 {
		return "", "", false
	}
	if !strings.EqualFold(fields[0], "ALTER") ||
		!strings.EqualFold(fields[1], "TABLE") ||
		!strings.EqualFold(fields[3], "ADD") ||
		!strings.EqualFold(fields[4], "COLUMN") {
		return "", "", false
	}
	tableName := strings.Trim(fields[2], "`\"[]")
	columnName := strings.Trim(fields[5], "`\"[]")
	if !isSafeSQLIdentifier(tableName) || !isSafeSQLIdentifier(columnName) {
		return "", "", false
	}
	return tableName, columnName, true
}

func txTableExists(tx *sql.Tx, tableName string) (bool, error) {
	return sqliteObjectExists(tx, "table", tableName)
}

// querier is satisfied by both *sql.Tx and *sql.DB.
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// hasColumnErr is hasColumn with the query error surfaced, for callers that must
// tell "column absent" apart from "could not look".
func hasColumnErr(q querier, tableName, columnName string) (bool, error) {
	if !isSafeSQLIdentifier(tableName) {
		return false, nil
	}
	rows, err := q.Query("PRAGMA table_info(" + tableName + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == columnName {
			return true, nil
		}
	}
	return false, rows.Err()
}

func tableHasColumnDBErr(db *sql.DB, tableName, columnName string) (bool, error) {
	return hasColumnErr(db, tableName, columnName)
}
