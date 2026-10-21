package config

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"opensync/internal/msg"
)

// resetConfigForTest clears the cached config under configMu (never by plainly
// assigning the global) and restores it afterwards, so tests cannot race with
// lock-protected readers and cannot leak state into each other (review P3-15).
func resetConfigForTest(t *testing.T) {
	t.Helper()
	configMu.Lock()
	old := sysConfig
	sysConfig = nil
	configMu.Unlock()
	t.Cleanup(func() {
		configMu.Lock()
		sysConfig = old
		configMu.Unlock()
	})
}

// withTempConfigDir points DataDir and ConfigDir at fresh temp directories and
// resets the cached config. It replaces the old process-wide os.Chdir helper:
// chdir is not parallel-safe and made the tests depend on execution order.
func withTempConfigDir(t *testing.T) {
	t.Helper()
	t.Setenv("TRIM_PKGVAR", t.TempDir())
	t.Setenv("TRIM_PKGETC", t.TempDir())
	resetConfigForTest(t)
}

// writeConfigForTest seeds config.ini in the configured ConfigDir.
func writeConfigForTest(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(ConfigDir(), "config.ini")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatalf("MkdirAll(ConfigDir) error: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("WriteFile(config.ini) error: %v", err)
	}
	return path
}

func TestConfigFileInvalidNumbersKeepDefaults(t *testing.T) {
	withTempConfigDir(t)
	writeConfigForTest(t, "[opensync]\ntask_timeout=also-bad\n")

	cfg := GetConfig()
	if cfg.Server.Timeout != 48 {
		t.Fatalf("Timeout = %d, want default 48 for invalid config value", cfg.Server.Timeout)
	}
}

func TestEnvironmentInvalidNumbersKeepDefaultsAndLog(t *testing.T) {
	withTempConfigDir(t)
	t.Setenv("OPENSYNC_COPY_CONCURRENCY", "not-a-number")
	t.Setenv("OPENSYNC_TASK_TIMEOUT", "also-bad")

	var logBuf bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(oldWriter) })

	cfg := GetConfig()
	if cfg.Server.CopyConcurrency != DefaultCopyConcurrency {
		t.Fatalf("CopyConcurrency = %d, want default for invalid env value", cfg.Server.CopyConcurrency)
	}
	if cfg.Server.Timeout != 48 {
		t.Fatalf("Timeout = %d, want default 48 for invalid env value", cfg.Server.Timeout)
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "OPENSYNC_COPY_CONCURRENCY") || !strings.Contains(logs, "OPENSYNC_TASK_TIMEOUT") {
		t.Fatalf("logs = %q, want invalid env keys to be logged", logs)
	}
}

// allowed_origins decides the write-request policy in platform.GatewayRequired,
// so both of its sources have to survive loading and be rewritten on save.
func TestAllowedOriginsLoadFromConfigFileAndEnvironment(t *testing.T) {
	withTempConfigDir(t)
	writeConfigForTest(t, `[opensync]
# 保留注释
allowed_origins = http://10.10.11.250:5666, nas.example.com
`)

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
	saved, err := os.ReadFile(filepath.Join(ConfigDir(), "config.ini"))
	if err != nil {
		t.Fatalf("ReadFile(config.ini) error: %v", err)
	}
	// allowed_origins is no longer a managed key, so the operator's line is
	// kept byte-for-byte rather than re-serialized.
	if !strings.Contains(string(saved), "allowed_origins = http://10.10.11.250:5666, nas.example.com") {
		t.Fatalf("config.ini = %q, want allowed_origins preserved", saved)
	}
	if !strings.Contains(string(saved), "# 保留注释") {
		t.Fatalf("config.ini = %q, want unrelated comments preserved", saved)
	}
}

// Saving settings from the page creates config.ini. Environment-provided keys
// the page does not manage (locale, allowed_origins) must stay in effect after
// that, and the file must not grow those keys or the deprecated ones.
func TestEnvironmentSurvivesFirstSettingsSave(t *testing.T) {
	withTempConfigDir(t)
	t.Setenv("OPENSYNC_LOCALE", "en")
	t.Setenv("OPENSYNC_ALLOWED_ORIGINS", "http://nas.example:5666")
	t.Setenv("OPENSYNC_TASK_SAVE", "10")

	if got := GetConfig().Server.TaskSave; got != 10 {
		t.Fatalf("TaskSave = %d, want env value 10", got)
	}
	if err := UpdateSystemSettings(SystemSettings{
		TaskTimeout: 12, TaskSave: 20, CopyConcurrency: 3, ScanConcurrency: 4, MaxRetries: 1,
	}); err != nil {
		t.Fatalf("UpdateSystemSettings() error: %v", err)
	}
	saved, err := os.ReadFile(filepath.Join(ConfigDir(), "config.ini"))
	if err != nil {
		t.Fatalf("ReadFile(config.ini) error: %v", err)
	}
	for _, key := range []string{"allowed_origins", "locale", "bind", "port", "log_level", "console_level", "log_save"} {
		if strings.Contains(string(saved), key+"=") {
			t.Fatalf("config.ini = %q, must not write %s", saved, key)
		}
	}

	// Restart: the file wins for the keys it holds, env fills in the rest.
	resetConfigForTest(t)
	cfg := GetConfig()
	if cfg.Server.TaskSave != 20 {
		t.Fatalf("TaskSave after restart = %d, want saved 20", cfg.Server.TaskSave)
	}
	if cfg.Server.Locale != "en" {
		t.Fatalf("Locale after restart = %q, want env en", cfg.Server.Locale)
	}
	if len(cfg.Server.AllowedOrigins) != 1 || cfg.Server.AllowedOrigins[0] != "http://nas.example:5666" {
		t.Fatalf("AllowedOrigins after restart = %#v, want env value", cfg.Server.AllowedOrigins)
	}
}

// Old config.ini files still carry bind/port/...; they must load and survive a
// save untouched.
func TestDeprecatedConfigKeysAreToleratedAndPreserved(t *testing.T) {
	withTempConfigDir(t)
	writeConfigForTest(t, "[opensync]\nbind=0.0.0.0\nport=8023\nlog_save=7\ntask_save=5\n")
	if got := GetConfig().Server.TaskSave; got != 5 {
		t.Fatalf("TaskSave = %d, want 5", got)
	}
	if err := UpdateSystemSettings(SystemSettings{
		TaskTimeout: 48, TaskSave: 6, CopyConcurrency: 5, ScanConcurrency: 16, MaxRetries: 2,
	}); err != nil {
		t.Fatalf("UpdateSystemSettings() error: %v", err)
	}
	saved, _ := os.ReadFile(filepath.Join(ConfigDir(), "config.ini"))
	for _, line := range []string{"bind=0.0.0.0", "port=8023", "log_save=7", "task_save=6"} {
		if !strings.Contains(string(saved), line) {
			t.Fatalf("config.ini = %q, want %q", saved, line)
		}
	}
	if strings.Count(string(saved), "[opensync]") != 1 {
		t.Fatalf("config.ini = %q, want exactly one [opensync] section", saved)
	}
}

// Two partial updates racing must both land: the merge happens under the lock.
func TestUpdateSystemSettingsWithMergesUnderLock(t *testing.T) {
	withTempConfigDir(t)
	GetConfig()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = UpdateSystemSettingsWith(func(s SystemSettings) SystemSettings { s.CopyConcurrency = 7; return s })
	}()
	_ = UpdateSystemSettingsWith(func(s SystemSettings) SystemSettings { s.MaxRetries = 3; return s })
	<-done
	got := GetSystemSettings()
	if got.CopyConcurrency != 7 || got.MaxRetries != 3 {
		t.Fatalf("settings = %#v, want both partial updates applied", got)
	}
}

// A missing secret.key with ciphertext already in the database must abort
// instead of silently generating a key that makes stored credentials
// permanently undecryptable (review P2-1).
func TestMissingSecretKeyWithEncryptedDatabaseFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "openSync.db"), []byte("SQLite format 3\x00 rows enc:v1:abc"), 0600); err != nil {
		t.Fatalf("setup db: %v", err)
	}
	_, err := loadOrCreatePasswordStr(dir)
	if err == nil {
		t.Fatal("loadOrCreatePasswordStr() = nil error, want refusal for missing key with encrypted DB")
	}
	if !strings.Contains(err.Error(), "缺失") || !strings.Contains(err.Error(), "secret.key") {
		t.Fatalf("error = %v, want an actionable missing-key message", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "secret.key")); !os.IsNotExist(statErr) {
		t.Fatalf("secret.key was created despite encrypted DB: %v", statErr)
	}
}

// Ciphertext that exists only in the WAL must be detected too.
func TestMissingSecretKeyDetectsCiphertextInWAL(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "openSync.db-wal"), []byte("xx enc:v1:abc"), 0600); err != nil {
		t.Fatalf("setup wal: %v", err)
	}
	if _, err := loadOrCreatePasswordStr(dir); err == nil {
		t.Fatal("loadOrCreatePasswordStr() = nil error, want refusal when the WAL holds ciphertext")
	}
}

// An empty secret.key left by a crash is regenerated only when nothing depends
// on it; the rewrite must be atomic and leave owner-only permissions.
func TestEmptySecretKeyRecovery(t *testing.T) {
	t.Run("no encrypted data", func(t *testing.T) {
		dir := t.TempDir()
		keyPath := filepath.Join(dir, "secret.key")
		if err := os.WriteFile(keyPath, []byte(" \n\t"), 0600); err != nil {
			t.Fatalf("setup secret: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "openSync.db"), []byte("SQLite format 3\x00 plain rows"), 0600); err != nil {
			t.Fatalf("setup db: %v", err)
		}
		key, err := loadOrCreatePasswordStr(dir)
		if err != nil {
			t.Fatalf("loadOrCreatePasswordStr() error: %v", err)
		}
		if strings.TrimSpace(key) == "" {
			t.Fatal("loadOrCreatePasswordStr() returned an empty key")
		}
		info, err := os.Stat(keyPath)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("secret.key mode = %v, %v; want 0600", info, err)
		}
	})

	t.Run("encrypted data present", func(t *testing.T) {
		dir := t.TempDir()
		keyPath := filepath.Join(dir, "secret.key")
		if err := os.WriteFile(keyPath, nil, 0600); err != nil {
			t.Fatalf("setup secret: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "openSync.db"), []byte("enc:v1:abc"), 0600); err != nil {
			t.Fatalf("setup db: %v", err)
		}
		_, err := loadOrCreatePasswordStr(dir)
		if err == nil || !strings.Contains(err.Error(), "为空") {
			t.Fatalf("error = %v, want refusal for empty key with encrypted DB", err)
		}
		if data, _ := os.ReadFile(keyPath); len(strings.TrimSpace(string(data))) != 0 {
			t.Fatalf("secret.key was replaced: %q", data)
		}
	})
}

// First start and an existing valid key must keep working (no false refusals).
func TestPasswordStrCreationAndReuse(t *testing.T) {
	t.Run("first start creates the key", func(t *testing.T) {
		dir := t.TempDir()
		key, err := loadOrCreatePasswordStr(dir)
		if err != nil || strings.TrimSpace(key) == "" {
			t.Fatalf("loadOrCreatePasswordStr() = %q, %v", key, err)
		}
		if info, err := os.Stat(filepath.Join(dir, "secret.key")); err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("secret.key stat = %v, %v; want mode 0600", info, err)
		}
	})
	t.Run("existing key is reused", func(t *testing.T) {
		dir := t.TempDir()
		keyPath := filepath.Join(dir, "secret.key")
		if err := os.WriteFile(keyPath, []byte("known-key"), 0644); err != nil {
			t.Fatalf("setup secret: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "openSync.db"), []byte("enc:v1:abc"), 0600); err != nil {
			t.Fatalf("setup db: %v", err)
		}
		key, err := loadOrCreatePasswordStr(dir)
		if err != nil || key != "known-key" {
			t.Fatalf("loadOrCreatePasswordStr() = %q, %v; want known-key", key, err)
		}
		if info, err := os.Stat(keyPath); err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("secret.key mode = %v, %v; want 0600 after tightening", info, err)
		}
	})
}

// A config.ini that exists but cannot be interpreted must be rejected loudly
// instead of silently falling back to permissive defaults (review P2-2).
func TestLoadServerConfigRejectsSemanticallyInvalidFiles(t *testing.T) {
	valid := []struct {
		name    string
		content string
		check   func(t *testing.T, cfg ServerConfig)
	}{
		{
			name:    "utf8 bom",
			content: "\ufeff[opensync]\ntask_save=7\n",
			check:   func(t *testing.T, cfg ServerConfig) { expectTaskSave(t, cfg, 7) },
		},
		{
			name:    "spaced section name",
			content: "[ opensync ]\ntask_save=8\n",
			check:   func(t *testing.T, cfg ServerConfig) { expectTaskSave(t, cfg, 8) },
		},
		{
			name:    "mixed case section name",
			content: "[OpenSync]\ntask_save=9\n",
			check:   func(t *testing.T, cfg ServerConfig) { expectTaskSave(t, cfg, 9) },
		},
		{
			name:    "inline comment on value",
			content: "[opensync]\ntask_save=11 # days to keep\n",
			check:   func(t *testing.T, cfg ServerConfig) { expectTaskSave(t, cfg, 11) },
		},
		{
			name:    "very long comment line",
			content: "[opensync]\n# " + strings.Repeat("a", 200*1024) + "\ntask_save=12\n",
			check:   func(t *testing.T, cfg ServerConfig) { expectTaskSave(t, cfg, 12) },
		},
		{
			name:    "empty opensync section",
			content: "[opensync]\n",
			check: func(t *testing.T, cfg ServerConfig) {
				if cfg.TaskSave != defaultTaskSave {
					t.Fatalf("TaskSave = %d, want default %d", cfg.TaskSave, defaultTaskSave)
				}
			},
		},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TRIM_PKGETC", t.TempDir())
			writeConfigForTest(t, tc.content)
			cfg, err := loadServerConfig("secret")
			if err != nil {
				t.Fatalf("loadServerConfig() error: %v", err)
			}
			tc.check(t, cfg)
		})
	}

	invalid := map[string]string{
		"empty file":                "",
		"comments only":             "# nothing here\n; and nothing after\n",
		"foreign section only":      "[other]\nfoo=bar\n",
		"key before any section":    "task_save=5\n",
		"unterminated section":      "[opensync\ntask_save=5\n",
		"garbage line in opensync":  "[opensync]\nthis is not a key value pair\n",
		"section header with extra": "[opensync] trailing\ntask_save=5\n",
	}
	for name, content := range invalid {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TRIM_PKGETC", t.TempDir())
			writeConfigForTest(t, content)
			if _, err := loadServerConfig("secret"); err == nil {
				t.Fatalf("loadServerConfig() = nil error, want a parse error")
			}
		})
	}
}

func expectTaskSave(t *testing.T, cfg ServerConfig, want int) {
	t.Helper()
	if cfg.TaskSave != want {
		t.Fatalf("TaskSave = %d, want %d", cfg.TaskSave, want)
	}
}

// Unknown and deprecated keys are reported instead of silently ignored.
func TestConfigKeyWarnings(t *testing.T) {
	t.Setenv("TRIM_PKGETC", t.TempDir())
	writeConfigForTest(t, "[opensync]\nmax_retry=3\ntask_save=4\n")

	var logBuf bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(oldWriter) })

	if _, err := loadServerConfig("secret"); err != nil {
		t.Fatalf("loadServerConfig() error: %v", err)
	}
	logs := logBuf.String()
	if !strings.Contains(logs, "max_retry") || !strings.Contains(logs, "unknown") {
		t.Fatalf("logs = %q, want an unknown-key warning for max_retry", logs)
	}
}

// A hand edit to config.ini while the app runs must not be overwritten by the
// next settings save; the operator is asked to restart instead (review P3-11).
func TestWriteConfigFileRefusesToOverwriteManualEdit(t *testing.T) {
	withTempConfigDir(t)
	writeConfigForTest(t, "[opensync]\ntask_save=10\n")
	if got := GetConfig().Server.TaskSave; got != 10 {
		t.Fatalf("TaskSave = %d, want 10", got)
	}

	// Operator edits the file after startup (valid value, different from memory).
	writeConfigForTest(t, "[opensync]\ntask_save=11\n")
	err := UpdateSystemSettings(SystemSettings{
		TaskTimeout: 48, TaskSave: 12, CopyConcurrency: 5, ScanConcurrency: 16, MaxRetries: 2,
	})
	if err == nil {
		t.Fatal("UpdateSystemSettings() = nil error, want a conflict error")
	}
	if !strings.Contains(err.Error(), "配置文件已被手动修改") ||
		!strings.Contains(err.Error(), "task_save") || !strings.Contains(err.Error(), "config.ini=11") {
		t.Fatalf("error = %v, want a conflict message naming task_save and the on-disk value", err)
	}
	if errors.Is(err, ErrConfigWrite) {
		t.Fatalf("conflict error must not be reported as ErrConfigWrite (the handler would hide it): %v", err)
	}
	if got := GetSystemSettings().TaskSave; got != 10 {
		t.Fatalf("in-memory TaskSave = %d, want unchanged 10", got)
	}

	// Restart reloads the hand-edited value, after which saving works again.
	resetConfigForTest(t)
	if got := GetConfig().Server.TaskSave; got != 11 {
		t.Fatalf("TaskSave after restart = %d, want 11", got)
	}
	if err := UpdateSystemSettings(SystemSettings{
		TaskTimeout: 48, TaskSave: 12, CopyConcurrency: 5, ScanConcurrency: 16, MaxRetries: 2,
	}); err != nil {
		t.Fatalf("UpdateSystemSettings() after restart error: %v", err)
	}
	saved, _ := os.ReadFile(filepath.Join(ConfigDir(), "config.ini"))
	if !strings.Contains(string(saved), "task_save=12") {
		t.Fatalf("config.ini = %q, want task_save=12", saved)
	}
}

// A hand-written section header is canonicalized on the next save so no second
// [opensync] header can appear (review P3-10/P2-2).
func TestWriteConfigFileCanonicalizesSectionHeader(t *testing.T) {
	for name, existing := range map[string]string{
		"spaced and mixed case": "[ OpenSync ]\ntask_save=1\n",
		"header with comment":   "[opensync] # operator note\ntask_save=1\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("TRIM_PKGETC", t.TempDir())
			writeConfigForTest(t, existing+"\n[other] # note\nfoo=bar\n")
			before := ServerConfig{TaskSave: 1, Timeout: 48, CopyConcurrency: 5, ScanConcurrency: 16, MaxRetries: 2}
			after := before
			after.TaskSave = 2
			if err := writeConfigFile(after, before); err != nil {
				t.Fatalf("writeConfigFile() error: %v", err)
			}
			saved, err := os.ReadFile(filepath.Join(ConfigDir(), "config.ini"))
			if err != nil {
				t.Fatalf("ReadFile(config.ini) error: %v", err)
			}
			content := string(saved)
			if strings.Count(content, "[opensync]") != 1 {
				t.Fatalf("config.ini = %q, want exactly one canonical [opensync] header", content)
			}
			if strings.Contains(content, "[ OpenSync ]") || strings.Contains(content, "[opensync] #") {
				t.Fatalf("config.ini = %q, want the original header rewritten", content)
			}
			// The foreign section must stay a boundary: managed keys belong to
			// [opensync], not to the section that follows it.
			parsed, err := readINI(filepath.Join(ConfigDir(), "config.ini"))
			if err != nil {
				t.Fatalf("readINI() error: %v", err)
			}
			if parsed["opensync"]["task_save"] != "2" {
				t.Fatalf("opensync task_save = %q, want 2", parsed["opensync"]["task_save"])
			}
			if _, ok := parsed["other"]["task_save"]; ok {
				t.Fatalf("managed key leaked into [other]: %#v", parsed["other"])
			}
			if !strings.Contains(content, "[other] # note") || !strings.Contains(content, "foo=bar") {
				t.Fatalf("config.ini = %q, want the foreign section preserved", content)
			}
		})
	}
}

// Locale values are normalized and invalid ones fall back to zh with a warning
// (review P3-3).
func TestLocaleIsNormalized(t *testing.T) {
	t.Run("region suffix", func(t *testing.T) {
		withTempConfigDir(t)
		t.Setenv("OPENSYNC_LOCALE", "EN-US")
		cfg := GetConfig()
		if cfg.Server.Locale != "en" || msg.Locale() != "en" {
			t.Fatalf("Locale = %q / msg.Locale() = %q, want en", cfg.Server.Locale, msg.Locale())
		}
	})
	t.Run("unsupported falls back", func(t *testing.T) {
		withTempConfigDir(t)
		t.Setenv("OPENSYNC_LOCALE", "fr")
		cfg := GetConfig()
		if cfg.Server.Locale != "zh" || msg.Locale() != "zh" {
			t.Fatalf("Locale = %q / msg.Locale() = %q, want zh fallback", cfg.Server.Locale, msg.Locale())
		}
	})
}

// Managed values must never carry control characters that would inject extra
// config.ini lines (review P3-11).
func TestValidConfigValueRejectsControlCharacters(t *testing.T) {
	for _, value := range []string{"1", "3650", "http://nas.example:5666"} {
		if !validConfigValue(value) {
			t.Fatalf("validConfigValue(%q) = false, want true", value)
		}
	}
	for _, value := range []string{"1\nport=1", "1\r\n[opensync]", "1\x00"} {
		if validConfigValue(value) {
			t.Fatalf("validConfigValue(%q) = true, want false", value)
		}
	}
}

// Precedence is explicit: defaults < environment < config.ini (per key). The
// settings page writes only the keys it owns, so env values survive; a key the
// file does contain wins over the environment (review P2-3).
func TestConfigFileWinsOverEnvironmentPerKey(t *testing.T) {
	withTempConfigDir(t)
	t.Setenv("OPENSYNC_TASK_SAVE", "10")
	t.Setenv("OPENSYNC_TASK_TIMEOUT", "24")
	t.Setenv("OPENSYNC_LOCALE", "en")
	writeConfigForTest(t, "[opensync]\ntask_save=20\n")

	cfg := GetConfig()
	if cfg.Server.TaskSave != 20 {
		t.Fatalf("TaskSave = %d, want the config.ini value 20", cfg.Server.TaskSave)
	}
	if cfg.Server.Timeout != 24 {
		t.Fatalf("Timeout = %d, want the env value 24 (key absent from file)", cfg.Server.Timeout)
	}
	if cfg.Server.Locale != "en" {
		t.Fatalf("Locale = %q, want env en (key absent from file)", cfg.Server.Locale)
	}
}

// Variables that no longer have an effect are called out instead of silently
// ignored (review P3-1).
func TestDeprecatedEnvironmentVariablesAreReported(t *testing.T) {
	withTempConfigDir(t)
	t.Setenv("OPENSYNC_PORT", "9999")
	t.Setenv("OPENSYNC_TLS_CERT", "/tmp/cert.pem")

	var logBuf bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(oldWriter) })

	_ = GetConfig()
	logs := logBuf.String()
	for _, name := range []string{"OPENSYNC_PORT", "OPENSYNC_TLS_CERT"} {
		if !strings.Contains(logs, name) || !strings.Contains(logs, "deprecated") {
			t.Fatalf("logs = %q, want a deprecation warning for %s", logs, name)
		}
	}
}
