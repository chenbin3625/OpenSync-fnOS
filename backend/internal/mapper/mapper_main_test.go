package mapper

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"opensync/internal/config"
)

// TestMain installs a deterministic baseline configuration before any test
// runs. resetGlobalDBForTest snapshots the "current" configuration; without a
// baseline, config.GetConfig() would load the developer's real data/config.ini
// from the package directory, generate data/secret.key inside the source tree
// and could log.Fatalf the whole test binary when that file is unreadable.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "opensync-mapper-test-")
	if err != nil {
		panic(err)
	}
	config.SetConfigForTest(&config.Config{
		DB:     config.DBConfig{DBName: filepath.Join(dir, "baseline.db")},
		Server: config.ServerConfig{PasswdStr: "mapper-test-baseline-key"},
	})
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// setMapperTestConfig replaces the global configuration for one test and
// restores the previous value afterwards. It never sets the global to nil, so
// no test can trigger a real configuration load.
func setMapperTestConfig(t *testing.T, cfg *config.Config) {
	t.Helper()
	old := config.GetConfig()
	config.SetConfigForTest(cfg)
	t.Cleanup(func() { config.SetConfigForTest(old) })
}

// snapshotConfigForTest returns a function restoring the current configuration.
// Used by tests whose helper swaps the key several times.
func snapshotConfigForTest(t *testing.T) func() {
	t.Helper()
	old := config.GetConfig()
	return func() { config.SetConfigForTest(old) }
}

// openTestDB opens a database through the production DSN (WAL, busy_timeout,
// BEGIN IMMEDIATE) so tests exercise the same locking behaviour as the real
// application.
func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	handle, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	t.Cleanup(func() { handle.Close() })
	return handle
}

// newFileTestDB opens a file-backed test database in t.TempDir().
func newFileTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return openTestDB(t, filepath.Join(t.TempDir(), "mapper-test.db"))
}
