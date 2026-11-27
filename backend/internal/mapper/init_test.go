package mapper

import (
	"database/sql"
	"opensync/internal/config"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestInitSQLCreatesSchemaWithSchemaVersionTable(t *testing.T) {
	resetGlobalDBForTest(t, &config.Config{
		DB:     config.DBConfig{DBName: filepath.Join(t.TempDir(), "opensync.db")},
		Server: config.ServerConfig{PasswdStr: "test-secret"},
	})

	InitSQL()

	var version int64
	if err := GetDB().QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if version != currentVersion {
		t.Fatalf("schema version = %d, want %d", version, currentVersion)
	}
	if tableExists(GetDB(), "user_list") {
		t.Fatalf("user_list should not exist on fresh install")
	}
}

func TestInitSQLCanRestartWithExistingSchema(t *testing.T) {
	resetGlobalDBForTest(t, &config.Config{
		DB:     config.DBConfig{DBName: filepath.Join(t.TempDir(), "opensync.db")},
		Server: config.ServerConfig{PasswdStr: "test-secret"},
	})

	InitSQL()
	if err := CloseDB(); err != nil {
		t.Fatalf("CloseDB() error: %v", err)
	}

	InitSQL()

	var version int64
	if err := GetDB().QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("read schema_version after restart: %v", err)
	}
	if version != currentVersion {
		t.Fatalf("schema version = %d, want %d", version, currentVersion)
	}
}

func TestMigrateDBTxSkipsLegacyRenameWhenSpeedColumnMissing(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()

	if _, err := testDB.Exec(`CREATE TABLE user_list(
		id integer primary key autoincrement,
		userName text,
		passwd text,
		sqlVersion integer
	)`); err != nil {
		t.Fatalf("create user_list: %v", err)
	}
	if _, err := testDB.Exec("INSERT INTO user_list(userName, passwd, sqlVersion) VALUES ('admin', 'x', 250520)"); err != nil {
		t.Fatalf("insert user: %v", err)
	}

	if _, err := testDB.Exec(`CREATE TABLE job(
		id integer primary key autoincrement,
		useCacheT integer DEFAULT 0
	)`); err != nil {
		t.Fatalf("create job: %v", err)
	}

	if err := migrateDBTx(testDB, 250520); err != nil {
		t.Fatalf("migrateDBTx() error: %v", err)
	}

	var version int64
	if err := testDB.QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != currentVersion {
		t.Fatalf("schema version = %d, want currentVersion %d", version, currentVersion)
	}

	for _, column := range []string{"scanIntervalT", "useCacheS", "scanIntervalS"} {
		if !tableHasColumn(testDB, "job", column) {
			t.Fatalf("job table missing migrated column %q", column)
		}
	}
}

func TestMigrateDBTxAddsJobFileSizeRangeColumns(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()

	if _, err := testDB.Exec(`CREATE TABLE user_list(
		id integer primary key autoincrement,
		userName text,
		passwd text,
		sqlVersion integer
	)`); err != nil {
		t.Fatalf("create user_list: %v", err)
	}
	if _, err := testDB.Exec("INSERT INTO user_list(userName, passwd, sqlVersion) VALUES ('admin', 'x', 250608)"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := testDB.Exec(`CREATE TABLE job(
		id integer primary key autoincrement,
		exclude text DEFAULT NULL
	)`); err != nil {
		t.Fatalf("create job: %v", err)
	}

	if err := migrateDBTx(testDB, 250608); err != nil {
		t.Fatalf("migrateDBTx() error: %v", err)
	}

	for _, column := range []string{"minFileSize", "maxFileSize"} {
		if !tableHasColumn(testDB, "job", column) {
			t.Fatalf("job table missing migrated column %q", column)
		}
	}
}

func TestMigrateDBTxDropsUnusedCronColumns(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()

	if _, err := testDB.Exec(`CREATE TABLE user_list(
		id integer primary key autoincrement,
		userName text,
		passwd text,
		sqlVersion integer
	)`); err != nil {
		t.Fatalf("create user_list: %v", err)
	}
	if _, err := testDB.Exec("INSERT INTO user_list(userName, passwd, sqlVersion) VALUES ('admin', 'x', 260605)"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := testDB.Exec(`CREATE TABLE job(
		id integer primary key autoincrement,
		year text DEFAULT NULL,
		month text DEFAULT NULL,
		day text DEFAULT NULL,
		week text DEFAULT NULL,
		day_of_week text DEFAULT NULL,
		hour text DEFAULT NULL,
		minute text DEFAULT NULL,
		second text DEFAULT NULL,
		start_date text DEFAULT NULL,
		end_date text DEFAULT NULL
	)`); err != nil {
		t.Fatalf("create job: %v", err)
	}

	if err := migrateDBTx(testDB, 260605); err != nil {
		t.Fatalf("migrateDBTx() error: %v", err)
	}

	for _, column := range []string{"year", "week", "start_date", "end_date"} {
		if tableHasColumn(testDB, "job", column) {
			t.Fatalf("job table still has unused cron column %q", column)
		}
	}
	for _, column := range []string{"month", "day", "day_of_week", "hour", "minute", "second"} {
		if !tableHasColumn(testDB, "job", column) {
			t.Fatalf("job table missing active cron column %q", column)
		}
	}
}

func TestMigrateDBTxDropsLegacyCronColumn(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()

	if _, err := testDB.Exec(`CREATE TABLE user_list(
		id integer primary key autoincrement,
		userName text,
		passwd text,
		sqlVersion integer
	)`); err != nil {
		t.Fatalf("create user_list: %v", err)
	}
	if _, err := testDB.Exec("INSERT INTO user_list(userName, passwd, sqlVersion) VALUES ('admin', 'x', 260612)"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := testDB.Exec(`CREATE TABLE job(
		id integer primary key autoincrement,
		cron text DEFAULT NULL,
		isCron integer DEFAULT 0,
		month text DEFAULT NULL,
		day text DEFAULT NULL,
		day_of_week text DEFAULT NULL,
		hour text DEFAULT NULL,
		minute text DEFAULT NULL,
		second text DEFAULT NULL
	)`); err != nil {
		t.Fatalf("create job: %v", err)
	}

	if err := migrateDBTx(testDB, 260612); err != nil {
		t.Fatalf("migrateDBTx() error: %v", err)
	}

	if tableHasColumn(testDB, "job", "cron") {
		t.Fatalf("job table still has legacy cron column")
	}
	for _, column := range []string{"isCron", "month", "day", "day_of_week", "hour", "minute", "second"} {
		if !tableHasColumn(testDB, "job", column) {
			t.Fatalf("job table missing active scheduling column %q", column)
		}
	}
}

func TestEnsureIndexesCreatesTaskStatusTimeIndex(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()

	if _, err := testDB.Exec(`CREATE TABLE job_task(
		id integer primary key autoincrement,
		jobId integer,
		status integer,
		runTime integer,
		createTime integer
	)`); err != nil {
		t.Fatalf("create job_task: %v", err)
	}
	if _, err := testDB.Exec(`CREATE TABLE job_task_item(
		id integer primary key autoincrement,
		taskId integer,
		status integer,
		type integer,
		isPath integer,
		createTime integer
	)`); err != nil {
		t.Fatalf("create job_task_item: %v", err)
	}

	ensureIndexes(testDB)

	if !indexExists(testDB, "idx_job_task_item_task_status_time") {
		t.Fatalf("expected idx_job_task_item_task_status_time to exist")
	}
	if !indexExists(testDB, "idx_job_task_item_task_type_time") {
		t.Fatalf("expected idx_job_task_item_task_type_time to exist")
	}
	if !indexExists(testDB, "idx_job_task_item_task_is_path_time") {
		t.Fatalf("expected idx_job_task_item_task_is_path_time to exist")
	}
	if !indexExists(testDB, "idx_job_task_item_recent") {
		t.Fatalf("expected idx_job_task_item_recent to exist")
	}
}

// A database created by an earlier release still has the FTS5 index, its
// triggers and the redundant (taskId, status) index. The 261120 migration must
// remove all of them without touching the item rows.
func TestMigrateDBTxDropsLegacyJobTaskItemFTSAndRedundantIndex(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()

	if _, err := testDB.Exec(`CREATE TABLE user_list(
		id integer primary key autoincrement,
		userName text,
		passwd text,
		sqlVersion integer
	)`); err != nil {
		t.Fatalf("create user_list: %v", err)
	}
	if _, err := testDB.Exec("INSERT INTO user_list(userName, passwd, sqlVersion) VALUES ('admin', 'x', 260606)"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := testDB.Exec(`CREATE TABLE job_task_item(
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
	)`); err != nil {
		t.Fatalf("create job_task_item: %v", err)
	}
	if _, err := testDB.Exec(`INSERT INTO job_task_item(id, taskId, srcPath, dstPath, fileName, alistTaskId, errMsg)
		VALUES (1, 10, '/src/photos/cat.jpg', '/backup/photos/cat.jpg', 'cat.jpg', 'copy_task_002', 'copy failed')`); err != nil {
		t.Fatalf("insert existing task item: %v", err)
	}
	legacy := []string{
		`CREATE VIRTUAL TABLE job_task_item_fts USING fts5(
			fileName, srcPath, dstPath,
			content='job_task_item', content_rowid='id', tokenize='trigram'
		)`,
		`CREATE TRIGGER job_task_item_ai AFTER INSERT ON job_task_item BEGIN
			INSERT INTO job_task_item_fts(rowid, fileName, srcPath, dstPath)
			VALUES (new.id, new.fileName, new.srcPath, new.dstPath);
		END`,
		"CREATE INDEX idx_job_task_item_task_status ON job_task_item(taskId, status)",
	}
	for _, stmt := range legacy {
		if _, err := testDB.Exec(stmt); err != nil {
			t.Fatalf("create legacy object: %v\nSQL: %s", err, stmt)
		}
	}

	if err := migrateDBTx(testDB, 260606); err != nil {
		t.Fatalf("migrateDBTx() error: %v", err)
	}

	if tableExists(testDB, "job_task_item_fts") {
		t.Fatal("job_task_item_fts still exists after the migration")
	}
	var triggerCount int
	if err := testDB.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'job_task_item_a%'").Scan(&triggerCount); err != nil {
		t.Fatalf("count triggers: %v", err)
	}
	if triggerCount != 0 {
		t.Fatalf("FTS triggers remaining after migration = %d, want 0", triggerCount)
	}
	if indexExists(testDB, "idx_job_task_item_task_status") {
		t.Fatal("redundant idx_job_task_item_task_status still exists after the migration")
	}
	var rows int
	if err := testDB.QueryRow("SELECT COUNT(*) FROM job_task_item").Scan(&rows); err != nil {
		t.Fatalf("count items: %v", err)
	}
	if rows != 1 {
		t.Fatalf("item rows = %d, want the migration to keep the existing row", rows)
	}
}

func tableHasColumn(db *sql.DB, tableName, columnName string) bool {
	rows, err := db.Query("PRAGMA table_info(" + tableName + ")")
	if err != nil {
		return false
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue interface{}
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false
		}
		if name == columnName {
			return true
		}
	}
	return false
}

func indexExists(db *sql.DB, indexName string) bool {
	var name string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='index' AND name=?", indexName).Scan(&name)
	return err == nil && name == indexName
}

func tableExists(db *sql.DB, tableName string) bool {
	var name string
	err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", tableName).Scan(&name)
	return err == nil && name == tableName
}
