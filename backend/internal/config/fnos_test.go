package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFnosUsesSystemDataDirectory(t *testing.T) {
	// fnOS sets TRIM_PKGVAR (data) and TRIM_PKGETC (config); GetConfig must keep
	// the database and the secret key under the data directory. No chdir: the
	// paths come from the environment, so the test cannot race with other tests
	// through process-wide state (review P3-15).
	dir := t.TempDir()
	t.Setenv("TRIM_PKGVAR", dir)
	t.Setenv("TRIM_PKGETC", t.TempDir())
	resetConfigForTest(t)

	cfg := GetConfig()
	if cfg.DB.DBName != filepath.Join(dir, "openSync.db") {
		t.Fatalf("database = %q, want fnOS data directory", cfg.DB.DBName)
	}
	if _, err := os.Stat(filepath.Join(dir, "secret.key")); err != nil {
		t.Fatalf("secret not in fnOS data directory: %v", err)
	}
}

// ConfigDir is independent from DataDir when TRIM_PKGETC is set; saving
// settings must write there, not into the data directory.
func TestConfigDirIsSeparateFromDataDir(t *testing.T) {
	dataDir := t.TempDir()
	configDir := t.TempDir()
	t.Setenv("TRIM_PKGVAR", dataDir)
	t.Setenv("TRIM_PKGETC", configDir)
	resetConfigForTest(t)

	_ = GetConfig()
	if err := UpdateSystemSettings(SystemSettings{
		TaskTimeout: 30, TaskSave: 20, CopyConcurrency: 4, ScanConcurrency: 8, MaxRetries: 1,
	}); err != nil {
		t.Fatalf("UpdateSystemSettings() error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "config.ini")); err != nil {
		t.Fatalf("config.ini not in TRIM_PKGETC: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "config.ini")); !os.IsNotExist(err) {
		t.Fatalf("config.ini unexpectedly written to the data directory: %v", err)
	}
}
