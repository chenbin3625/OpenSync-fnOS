package mapper

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"opensync/internal/config"
)

// P3-1: GetDB's contract is "never nil". ShutdownDB before any database was
// ever opened used to leave closedDB nil and GetDB returned nil.
func TestGetDBNeverReturnsNilAfterShutdownWithoutInitialization(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "never-opened.db")
	resetGlobalDBForTest(t, &config.Config{DB: config.DBConfig{DBName: dbPath}})

	if err := ShutdownDB(); err != nil {
		t.Fatalf("ShutdownDB() error = %v", err)
	}
	handle := GetDB()
	if handle == nil {
		t.Fatal("GetDB() after ShutdownDB() = nil; callers dereference it without a nil check")
	}
	if err := handle.Ping(); err == nil {
		t.Fatal("GetDB() after ShutdownDB() returned a live handle, want a closed one")
	}
	if _, err := handle.Exec("CREATE TABLE late(id integer)"); err == nil {
		t.Fatal("a write succeeded on the post-shutdown handle")
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("the post-shutdown placeholder recreated the database file: %v", err)
	}
}

// P3-2: a CloseDB that lands while InitDB is opening the handle must not let the
// half-initialised handle be published afterwards.
func TestInitDBDiscardsHandleWhenCloseDBRacesInitialization(t *testing.T) {
	resetGlobalDBForTest(t, &config.Config{
		DB:     config.DBConfig{DBName: filepath.Join(t.TempDir(), "race.db")},
		Server: config.ServerConfig{PasswdStr: "race-test"},
	})
	closedDuringFirstAttempt := false
	initDBPauseForTest = func() {
		if closedDuringFirstAttempt {
			return
		}
		closedDuringFirstAttempt = true
		if err := CloseDB(); err != nil {
			t.Errorf("CloseDB() during initialization error = %v", err)
		}
	}
	t.Cleanup(func() { initDBPauseForTest = nil })

	handle := InitDB()
	if !closedDuringFirstAttempt {
		t.Fatal("test seam did not run; the race was not exercised")
	}
	if handle == nil {
		t.Fatal("InitDB() after the discarded attempt returned nil")
	}
	if err := handle.Ping(); err != nil {
		t.Fatalf("InitDB() did not recover from the raced CloseDB: %v", err)
	}
	if _, err := handle.Exec("CREATE TABLE recovered(id integer)"); err != nil {
		t.Fatalf("handle recovered after the race is unusable: %v", err)
	}
}

// P3-4: startup verifies the settings the concurrency design relies on instead
// of only logging a warning and running degraded.
func TestInitDBValidatesWALBusyTimeoutAndForeignKeys(t *testing.T) {
	resetGlobalDBForTest(t, &config.Config{
		DB:     config.DBConfig{DBName: filepath.Join(t.TempDir(), "settings.db")},
		Server: config.ServerConfig{PasswdStr: "settings-test"},
	})
	handle := InitDB()

	var journalMode string
	if err := handle.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
	var busyTimeout int
	if err := handle.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busyTimeout != sqliteBusyTimeoutMS {
		t.Fatalf("busy_timeout = %d, want %d", busyTimeout, sqliteBusyTimeoutMS)
	}
	var foreignKeys int
	if err := handle.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}
}

// P3-3: a :memory: database must use a single pooled connection (every extra
// connection would be a separate empty database) and still get BEGIN IMMEDIATE.
func TestInitDBMemoryDatabaseUsesSingleConnection(t *testing.T) {
	resetGlobalDBForTest(t, &config.Config{
		DB:     config.DBConfig{DBName: ":memory:"},
		Server: config.ServerConfig{PasswdStr: "memory-test"},
	})
	InitSQL()

	handle := GetDB()
	if got := handle.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d for an in-memory database, want 1", got)
	}
	if _, err := FetchAllToTable("SELECT * FROM job"); err != nil {
		t.Fatalf("schema created by InitSQL is not visible through GetDB(): %v", err)
	}
	var busyTimeout int
	if err := handle.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busyTimeout != sqliteBusyTimeoutMS {
		t.Fatalf("busy_timeout = %d on :memory:, want %d (the DSN used to special-case it away)", busyTimeout, sqliteBusyTimeoutMS)
	}
}

func TestSQLiteDSNMemoryKeepsPragmasAndImmediateTxlock(t *testing.T) {
	dsn := sqliteDSN(":memory:")
	if dsn == ":memory:" {
		t.Fatal("sqliteDSN(:memory:) dropped every PRAGMA/_txlock again")
	}
	for _, needle := range []string{"_txlock=immediate", "_pragma=busy_timeout", "_pragma=foreign_keys"} {
		if !strings.Contains(dsn, needle) {
			t.Fatalf("memory DSN %q lacks %q", dsn, needle)
		}
	}
	if !isMemoryDBName(":memory:") || !isMemoryDBName("file::memory:") || !isMemoryDBName("file:x?mode=memory") {
		t.Fatal("isMemoryDBName does not recognise a memory database")
	}
	if isMemoryDBName(filepath.Join(t.TempDir(), "db.sqlite")) {
		t.Fatal("isMemoryDBName misclassified a file database as memory")
	}
}

// P3-7: the WAL checkpoint after history cleanup now reports "could not finish"
// instead of discarding the result.
func TestWalCheckpointTruncateReportsBusyWhenReaderPinsTheWAL(t *testing.T) {
	handle := newFileTestDB(t)
	handle.SetMaxOpenConns(4)
	restore := SetDBForTest(handle)
	defer restore()
	mustExec(t, handle, "CREATE TABLE t(id integer primary key, v integer)")
	if _, err := handle.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatalf("disable autocheckpoint: %v", err)
	}

	ctx := context.Background()
	readTx, err := handle.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("begin read tx: %v", err)
	}
	defer readTx.Rollback()
	var n int
	if err := readTx.QueryRow("SELECT COUNT(*) FROM t").Scan(&n); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	// A commit after the reader's snapshot leaves WAL frames that TRUNCATE
	// cannot discard while that reader is active.
	mustExec(t, handle, "INSERT INTO t(id, v) VALUES(1, 1)")

	if err := walCheckpointTruncate(ctx); err == nil {
		t.Fatal("walCheckpointTruncate() error = nil while a reader pins the WAL, want a busy report")
	}
	if err := readTx.Rollback(); err != nil {
		t.Fatalf("rollback reader: %v", err)
	}
	if err := walCheckpointTruncate(ctx); err != nil {
		t.Fatalf("walCheckpointTruncate() after the reader finished error = %v, want nil", err)
	}
}

func TestNoSuchTableOrNoRowsDoesNotMatchOtherErrors(t *testing.T) {
	if isNoSuchTableOrNoRows(sql.ErrTxDone) {
		t.Fatal("isNoSuchTableOrNoRows matched sql.ErrTxDone")
	}
	if !isNoSuchTableOrNoRows(sql.ErrNoRows) {
		t.Fatal("isNoSuchTableOrNoRows did not match sql.ErrNoRows")
	}
	if isNoSuchTableOrNoRows(errors.New("no such column: taskNum")) {
		t.Fatal("isNoSuchTableOrNoRows matched a missing-column error")
	}
}
