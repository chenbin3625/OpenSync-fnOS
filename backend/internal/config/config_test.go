package config

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigFileInvalidNumbersKeepDefaults(t *testing.T) {
	oldConfig := sysConfig
	sysConfig = nil
	defer func() {
		sysConfig = oldConfig
	}()

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error: %v", err)
	}
	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir(temp) error: %v", err)
	}
	defer os.Chdir(oldWD)

	if err := os.MkdirAll("data", 0755); err != nil {
		t.Fatalf("MkdirAll(data) error: %v", err)
	}
	configPath := filepath.Join("data", "config.ini")
	if err := os.WriteFile(configPath, []byte(`[opensync]
port=not-a-number
task_timeout=also-bad
`), 0644); err != nil {
		t.Fatalf("WriteFile(config.ini) error: %v", err)
	}

	cfg := GetConfig()
	if cfg.Server.Bind != "0.0.0.0" {
		t.Fatalf("Bind = %q, want default 0.0.0.0", cfg.Server.Bind)
	}
	if cfg.Server.Port != 8023 {
		t.Fatalf("Port = %d, want default 8023 for invalid config value", cfg.Server.Port)
	}
	if cfg.Server.Timeout != 48 {
		t.Fatalf("Timeout = %d, want default 48 for invalid config value", cfg.Server.Timeout)
	}
}

func TestEnvironmentInvalidNumbersKeepDefaultsAndLog(t *testing.T) {
	oldConfig := sysConfig
	sysConfig = nil
	defer func() {
		sysConfig = oldConfig
	}()

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error: %v", err)
	}
	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir(temp) error: %v", err)
	}
	defer os.Chdir(oldWD)

	if err := os.MkdirAll("data", 0755); err != nil {
		t.Fatalf("MkdirAll(data) error: %v", err)
	}
	t.Setenv("OPENSYNC_PORT", "not-a-number")
	t.Setenv("OPENSYNC_BIND", "0.0.0.0")
	t.Setenv("OPENSYNC_TASK_TIMEOUT", "also-bad")

	var logBuf bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(oldWriter)

	cfg := GetConfig()
	if cfg.Server.Bind != "0.0.0.0" {
		t.Fatalf("Bind = %q, want env override 0.0.0.0", cfg.Server.Bind)
	}
	if cfg.Server.Port != 8023 {
		t.Fatalf("Port = %d, want default 8023 for invalid env value", cfg.Server.Port)
	}
	if cfg.Server.Timeout != 48 {
		t.Fatalf("Timeout = %d, want default 48 for invalid env value", cfg.Server.Timeout)
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "OPENSYNC_PORT") || !strings.Contains(logs, "OPENSYNC_TASK_TIMEOUT") {
		t.Fatalf("logs = %q, want invalid env keys to be logged", logs)
	}
}

// allowed_origins decides the write-request policy in platform.GatewayRequired,
// so both of its sources have to survive loading and be rewritten on save.
func TestAllowedOriginsLoadFromConfigFileAndEnvironment(t *testing.T) {
	oldConfig := sysConfig
	sysConfig = nil
	defer func() { sysConfig = oldConfig }()

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error: %v", err)
	}
	tmpDir := t.TempDir()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir(temp) error: %v", err)
	}
	defer os.Chdir(oldWD)

	if err := os.MkdirAll("data", 0755); err != nil {
		t.Fatalf("MkdirAll(data) error: %v", err)
	}
	if err := os.WriteFile(filepath.Join("data", "config.ini"), []byte(`[opensync]
# 保留注释
allowed_origins = http://10.10.11.250:5666, nas.example.com
`), 0644); err != nil {
		t.Fatalf("WriteFile(config.ini) error: %v", err)
	}

	cfg := GetConfig()
	want := []string{"http://10.10.11.250:5666", "nas.example.com"}
	if len(cfg.Server.AllowedOrigins) != len(want) {
		t.Fatalf("AllowedOrigins = %#v, want %#v", cfg.Server.AllowedOrigins, want)
	}
	for i, entry := range want {
		if cfg.Server.AllowedOrigins[i] != entry {
			t.Fatalf("AllowedOrigins[%d] = %q, want %q", i, cfg.Server.AllowedOrigins[i], entry)
		}
	}

	// Saving unrelated settings must not drop the key or the file's comments.
	if err := UpdateSystemSettings(SystemSettings{
		TaskTimeout: 48, TaskSave: 30,
		CopyConcurrency: 5, ScanConcurrency: 8, MaxRetries: 2,
	}); err != nil {
		t.Fatalf("UpdateSystemSettings() error: %v", err)
	}
	saved, err := os.ReadFile(filepath.Join("data", "config.ini"))
	if err != nil {
		t.Fatalf("ReadFile(config.ini) error: %v", err)
	}
	if !strings.Contains(string(saved), "allowed_origins=http://10.10.11.250:5666,nas.example.com") {
		t.Fatalf("config.ini = %q, want allowed_origins preserved", saved)
	}
	if !strings.Contains(string(saved), "# 保留注释") {
		t.Fatalf("config.ini = %q, want unrelated comments preserved", saved)
	}
}
