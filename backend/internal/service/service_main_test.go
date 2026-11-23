package service

import (
	"os"
	"path/filepath"
	"testing"

	"opensync/internal/config"
)

// TestMain installs a deterministic baseline configuration before any test runs.
// Several service tests snapshot config.GetConfig() before overriding it;
// without a baseline, config.GetConfig() would load or create files inside
// internal/service/data/ in the source tree.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "opensync-service-test-")
	if err != nil {
		panic(err)
	}
	config.SetConfigForTest(&config.Config{
		DB: config.DBConfig{DBName: filepath.Join(dir, "baseline.db")},
		Server: config.ServerConfig{
			TaskSave:        30,
			Timeout:         48,
			CopyConcurrency: config.DefaultCopyConcurrency,
			ScanConcurrency: config.DefaultScanConcurrency,
			MaxRetries:      config.DefaultMaxRetries,
			PasswdStr:       "service-test-baseline-key",
		},
	})
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
