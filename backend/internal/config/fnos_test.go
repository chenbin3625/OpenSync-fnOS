package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFnosUsesSystemDataDirectory(t *testing.T) {
	withTempConfigDir(t)
	dir := t.TempDir()
	t.Setenv("TRIM_PKGVAR", dir)
	configMu.Lock()
	sysConfig = nil
	configMu.Unlock()
	cfg := GetConfig()
	if cfg.DB.DBName != filepath.Join(dir, "openSync.db") {
		t.Fatalf("database = %q, want fnOS data directory", cfg.DB.DBName)
	}
	if _, err := os.Stat(filepath.Join(dir, "secret.key")); err != nil {
		t.Fatalf("secret not in fnOS data directory: %v", err)
	}
}
