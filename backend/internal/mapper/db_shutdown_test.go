package mapper

import (
	"opensync/internal/config"
	"os"
	"path/filepath"
	"testing"
)

// CloseDB only cleared the handle and reset the once guard, so anything still
// running — a debounced persist flush, the retention cron, a request outliving
// the server — reopened the database through GetDB and recreated the file that
// shutdown had just released.
func TestShutdownDBDoesNotReopenOnLaterGetDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opensync.db")
	resetGlobalDBForTest(t, &config.Config{DB: config.DBConfig{DBName: dbPath}})

	InitDB()
	if err := ShutdownDB(); err != nil {
		t.Fatalf("ShutdownDB() error: %v", err)
	}
	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("Remove(db) error: %v", err)
	}

	// This is the straggler: before the fix it silently re-created the database.
	handle := GetDB()
	if handle == nil {
		t.Fatal("GetDB() after ShutdownDB() = nil; callers dereference it without a nil check")
	}
	if err := handle.Ping(); err == nil {
		t.Fatal("GetDB() after ShutdownDB() returned a live handle, want a closed one")
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("GetDB() after ShutdownDB() recreated the database file (stat err = %v)", err)
	}
}

// A query through the post-shutdown handle must fail rather than appear to work.
func TestShutdownDBMakesQueriesFail(t *testing.T) {
	resetGlobalDBForTest(t, &config.Config{
		DB: config.DBConfig{DBName: filepath.Join(t.TempDir(), "opensync.db")},
	})

	InitDB()
	if err := ShutdownDB(); err != nil {
		t.Fatalf("ShutdownDB() error: %v", err)
	}
	if _, err := GetDB().Exec("CREATE TABLE late(id integer)"); err == nil {
		t.Fatal("a write succeeded after ShutdownDB()")
	}
}

// CloseDB keeps its reinitialization contract: it is the handle-swap used by
// callers that intend to open the database again.
func TestCloseDBStillAllowsReinitAfterShutdownExists(t *testing.T) {
	resetGlobalDBForTest(t, &config.Config{
		DB: config.DBConfig{DBName: filepath.Join(t.TempDir(), "opensync.db")},
	})

	InitDB()
	if err := CloseDB(); err != nil {
		t.Fatalf("CloseDB() error: %v", err)
	}
	reopened := GetDB()
	if reopened == nil {
		t.Fatal("GetDB() after CloseDB() = nil, want a fresh handle")
	}
	if err := reopened.Ping(); err != nil {
		t.Fatalf("reopened handle Ping() error: %v", err)
	}
}

// ShutdownDB runs on a defer that may follow an earlier CloseDB, and is safe to
// call more than once.
func TestShutdownDBIsIdempotent(t *testing.T) {
	resetGlobalDBForTest(t, &config.Config{
		DB: config.DBConfig{DBName: filepath.Join(t.TempDir(), "opensync.db")},
	})

	InitDB()
	if err := ShutdownDB(); err != nil {
		t.Fatalf("first ShutdownDB() error: %v", err)
	}
	if err := ShutdownDB(); err != nil {
		t.Fatalf("second ShutdownDB() error: %v", err)
	}
}
