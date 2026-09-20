package config

import (
	"bufio"
	"fmt"
	"log"
	"opensync/internal/msg"
	"opensync/pkg/crypto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// ServerConfig holds server configuration
type ServerConfig struct {
	Bind            string
	Port            int
	LogLevel        int
	ConsoleLevel    int
	LogSave         int
	TaskSave        int
	Timeout         int
	CopyConcurrency int
	ScanConcurrency int
	MaxRetries      int
	PasswdStr       string
	// AllowedOrigins lists the origins allowed to perform mutations. Entries are
	// full origins ("http://nas.example:5666") or bare hosts ("nas.example");
	// full origins match scheme and effective port exactly, while bare hosts
	// accept any port. Configuring it retires the Referer-based same-origin
	// fallback in platform.GatewayRequired, so a deployment that sets it must
	// list the address the NAS is actually reached at — otherwise the UI's own
	// writes are rejected.
	AllowedOrigins []string
	// TLSCertFile and TLSKeyFile enable HTTPS/HTTP2 and opportunistic HTTP3.
	TLSCertFile string
	TLSKeyFile  string
}

// DBConfig holds database configuration
type DBConfig struct {
	DBName string
}

// Config holds all configuration
type Config struct {
	Server ServerConfig
	DB     DBConfig
}

var (
	sysConfig *Config
	configMu  sync.RWMutex
)

const (
	defaultBind         = "0.0.0.0"
	defaultPort         = 8023
	defaultLogLevel     = 1
	defaultConsoleLevel = 2
	defaultLogSave      = 7
	defaultTaskSave     = 30
	defaultTaskTimeout  = 48

	minTaskSave    = 0
	maxTaskSave    = 3650
	minTaskTimeout = 0
	maxTaskTimeout = 8760
)

const (
	DefaultCopyConcurrency = 5
	DefaultScanConcurrency = 16
	DefaultMaxRetries      = 2

	MinCopyConcurrency = 1
	MaxCopyConcurrency = 100
	MinScanConcurrency = 1
	MaxScanConcurrency = 50
	MinMaxRetries      = 0
	MaxRetryAttempts   = 10
)

// SystemSettings is the subset of backend settings exposed for runtime editing.
type SystemSettings struct {
	TaskTimeout     int `json:"taskTimeout"`
	TaskSave        int `json:"taskSave"`
	CopyConcurrency int `json:"copyConcurrency"`
	ScanConcurrency int `json:"scanConcurrency"`
	MaxRetries      int `json:"maxRetries"`
}

// GetPasswordStr gets or generates the encryption secret key. Persistence is
// mandatory: a key that only exists in memory would rotate on restart and
// invalidate every stored cookie and encrypted token, so a write failure is
// fatal at startup.
func GetPasswordStr() string {
	_ = os.MkdirAll(DataDir(), 0700)
	key, err := crypto.ReadOrSetFile(filepath.Join(DataDir(), "secret.key"), crypto.GeneratePassword(256), false)
	if err != nil {
		log.Fatalf("Failed to persist data/secret.key: %v", err)
	}
	return key
}

// GetConfig returns the global config (singleton)
func GetConfig() *Config {
	configMu.RLock()
	cfg := sysConfig
	configMu.RUnlock()
	if cfg != nil {
		return cfg
	}

	configMu.Lock()
	defer configMu.Unlock()
	if sysConfig != nil {
		return sysConfig
	}

	passwdStr := GetPasswordStr()
	dbname := filepath.Join(DataDir(), "openSync.db")

	sCfg := ServerConfig{
		Bind:            defaultBind,
		Port:            defaultPort,
		LogLevel:        defaultLogLevel,
		ConsoleLevel:    defaultConsoleLevel,
		LogSave:         defaultLogSave,
		TaskSave:        defaultTaskSave,
		Timeout:         defaultTaskTimeout,
		CopyConcurrency: DefaultCopyConcurrency,
		ScanConcurrency: DefaultScanConcurrency,
		MaxRetries:      DefaultMaxRetries,
		PasswdStr:       passwdStr,
	}

	if _, err := os.Stat(filepath.Join(ConfigDir(), "config.ini")); err == nil {
		// Read config.ini
		iniMap := readINI(filepath.Join(ConfigDir(), "config.ini"))
		if opensync, ok := iniMap["opensync"]; ok {
			if v, ok := opensync["bind"]; ok {
				sCfg.Bind = stringConfigValue(v, sCfg.Bind)
			}
			if v, ok := opensync["port"]; ok {
				sCfg.Port = intConfigValue(v, sCfg.Port, "port")
			}
			if v, ok := opensync["log_level"]; ok {
				sCfg.LogLevel = intConfigValue(v, sCfg.LogLevel, "log_level")
			}
			if v, ok := opensync["console_level"]; ok {
				sCfg.ConsoleLevel = intConfigValue(v, sCfg.ConsoleLevel, "console_level")
			}
			if v, ok := opensync["log_save"]; ok {
				sCfg.LogSave = intConfigValue(v, sCfg.LogSave, "log_save")
			}
			if v, ok := opensync["task_save"]; ok {
				sCfg.TaskSave = intConfigValue(v, sCfg.TaskSave, "task_save")
			}
			if v, ok := opensync["task_timeout"]; ok {
				sCfg.Timeout = intConfigValue(v, sCfg.Timeout, "task_timeout")
			}
			if v, ok := opensync["copy_concurrency"]; ok {
				sCfg.CopyConcurrency = intConfigValue(v, sCfg.CopyConcurrency, "copy_concurrency")
			}
			if v, ok := opensync["scan_concurrency"]; ok {
				sCfg.ScanConcurrency = intConfigValue(v, sCfg.ScanConcurrency, "scan_concurrency")
			}
			if v, ok := opensync["max_retries"]; ok {
				sCfg.MaxRetries = intConfigValue(v, sCfg.MaxRetries, "max_retries")
			}
			if v, ok := opensync["allowed_origins"]; ok {
				sCfg.AllowedOrigins = splitList(v)
			}
		}
	} else {
		// Read from environment variables
		sCfg.Bind = envStringConfigValue("OPENSYNC_BIND", sCfg.Bind)
		sCfg.Port = envIntConfigValue("OPENSYNC_PORT", sCfg.Port)
		sCfg.LogLevel = envIntConfigValue("OPENSYNC_LOG_LEVEL", sCfg.LogLevel)
		sCfg.ConsoleLevel = envIntConfigValue("OPENSYNC_CONSOLE_LEVEL", sCfg.ConsoleLevel)
		sCfg.LogSave = envIntConfigValue("OPENSYNC_LOG_SAVE", sCfg.LogSave)
		sCfg.TaskSave = envIntConfigValue("OPENSYNC_TASK_SAVE", sCfg.TaskSave)
		sCfg.Timeout = envIntConfigValue("OPENSYNC_TASK_TIMEOUT", sCfg.Timeout)
		sCfg.CopyConcurrency = envIntConfigValue("OPENSYNC_COPY_CONCURRENCY", sCfg.CopyConcurrency)
		sCfg.ScanConcurrency = envIntConfigValue("OPENSYNC_SCAN_CONCURRENCY", sCfg.ScanConcurrency)
		sCfg.MaxRetries = envIntConfigValue("OPENSYNC_MAX_RETRIES", sCfg.MaxRetries)
		sCfg.AllowedOrigins = splitList(os.Getenv("OPENSYNC_ALLOWED_ORIGINS"))
	}
	sCfg.TLSCertFile = envStringConfigValue("OPENSYNC_TLS_CERT", sCfg.TLSCertFile)
	sCfg.TLSKeyFile = envStringConfigValue("OPENSYNC_TLS_KEY", sCfg.TLSKeyFile)

	sysConfig = &Config{
		DB:     DBConfig{DBName: dbname},
		Server: sCfg,
	}
	clampServerConfig(&sysConfig.Server)
	return sysConfig
}

// clampServerConfig enforces the same ranges as validateSystemSettings on
// values loaded from config.ini or environment variables. Manually edited
// values that fall outside the allowed range fall back to the default rather
// than silently taking effect (e.g. copy_concurrency=99999 spawning an
// unbounded number of goroutines, or task_timeout=-1 producing a negative
// timeout).
func clampServerConfig(sCfg *ServerConfig) {
	sCfg.Bind = stringConfigValue(sCfg.Bind, defaultBind)
	sCfg.Timeout = clampInt(sCfg.Timeout, minTaskTimeout, maxTaskTimeout, defaultTaskTimeout)
	sCfg.TaskSave = clampInt(sCfg.TaskSave, minTaskSave, maxTaskSave, defaultTaskSave)
	sCfg.CopyConcurrency = clampInt(sCfg.CopyConcurrency, MinCopyConcurrency, MaxCopyConcurrency, DefaultCopyConcurrency)
	sCfg.ScanConcurrency = clampInt(sCfg.ScanConcurrency, MinScanConcurrency, MaxScanConcurrency, DefaultScanConcurrency)
	sCfg.MaxRetries = clampInt(sCfg.MaxRetries, MinMaxRetries, MaxRetryAttempts, DefaultMaxRetries)
}

func (s ServerConfig) TLSEnabled() bool {
	return strings.TrimSpace(s.TLSCertFile) != "" && strings.TrimSpace(s.TLSKeyFile) != ""
}

func clampInt(value, min, max, fallback int) int {
	if value < min || value > max {
		return fallback
	}
	return value
}

// splitList splits a comma-separated config value, dropping blank entries.
func splitList(value string) []string {
	var items []string
	for _, part := range strings.Split(value, ",") {
		if p := strings.TrimSpace(part); p != "" {
			items = append(items, p)
		}
	}
	return items
}

// SetConfigForTest swaps the process config for tests in other packages.
func SetConfigForTest(cfg *Config) {
	configMu.Lock()
	defer configMu.Unlock()
	sysConfig = cfg
}

// GetSystemSettings returns the runtime-editable settings.
func GetSystemSettings() SystemSettings {
	cfg := GetConfig()
	return SystemSettings{
		TaskTimeout:     cfg.Server.Timeout,
		TaskSave:        cfg.Server.TaskSave,
		CopyConcurrency: cfg.Server.CopyConcurrency,
		ScanConcurrency: cfg.Server.ScanConcurrency,
		MaxRetries:      cfg.Server.MaxRetries,
	}
}

// UpdateSystemSettings validates, persists, and applies runtime-editable settings.
func UpdateSystemSettings(settings SystemSettings) error {
	if err := validateSystemSettings(settings); err != nil {
		return err
	}

	GetConfig()
	configMu.Lock()
	defer configMu.Unlock()

	cfg := sysConfig
	nextServer := cfg.Server
	nextServer.Timeout = settings.TaskTimeout
	nextServer.TaskSave = settings.TaskSave
	nextServer.CopyConcurrency = settings.CopyConcurrency
	nextServer.ScanConcurrency = settings.ScanConcurrency
	nextServer.MaxRetries = settings.MaxRetries

	if err := writeConfigFile(nextServer); err != nil {
		return err
	}
	sysConfig = &Config{
		DB:     cfg.DB,
		Server: nextServer,
	}
	return nil
}

func validateSystemSettings(settings SystemSettings) error {
	checks := []struct {
		name     string
		value    int
		min, max int
	}{
		{msg.SettingsTaskTimeout, settings.TaskTimeout, minTaskTimeout, maxTaskTimeout},
		{msg.SettingsTaskSave, settings.TaskSave, minTaskSave, maxTaskSave},
		{msg.SettingsCopyConcurrency, settings.CopyConcurrency, MinCopyConcurrency, MaxCopyConcurrency},
		{msg.SettingsScanConcurrency, settings.ScanConcurrency, MinScanConcurrency, MaxScanConcurrency},
		{msg.SettingsMaxRetries, settings.MaxRetries, MinMaxRetries, MaxRetryAttempts},
	}
	for _, item := range checks {
		if item.value < item.min || item.value > item.max {
			return fmt.Errorf("%s", msg.SettingsRangeError(item.name, item.min, item.max))
		}
	}
	return nil
}

func envIntConfigValue(envName string, fallback int) int {
	value := os.Getenv(envName)
	if value == "" {
		return fallback
	}
	return intConfigValue(value, fallback, envName)
}

func envStringConfigValue(envName string, fallback string) string {
	return stringConfigValue(os.Getenv(envName), fallback)
}

// configManagedKeys are the [opensync] keys the server writes itself. Any
// other line in config.ini — comments, unknown keys, other sections — is
// preserved verbatim when settings are updated at runtime.
var configManagedKeys = []string{
	"bind", "port", "log_level", "console_level", "log_save",
	"task_save", "task_timeout", "copy_concurrency", "scan_concurrency",
	"max_retries", "allowed_origins",
}

func configManagedValues(sCfg ServerConfig) map[string]string {
	return map[string]string{
		"bind":             sCfg.Bind,
		"port":             strconv.Itoa(sCfg.Port),
		"log_level":        strconv.Itoa(sCfg.LogLevel),
		"console_level":    strconv.Itoa(sCfg.ConsoleLevel),
		"log_save":         strconv.Itoa(sCfg.LogSave),
		"task_save":        strconv.Itoa(sCfg.TaskSave),
		"task_timeout":     strconv.Itoa(sCfg.Timeout),
		"copy_concurrency": strconv.Itoa(sCfg.CopyConcurrency),
		"scan_concurrency": strconv.Itoa(sCfg.ScanConcurrency),
		"max_retries":      strconv.Itoa(sCfg.MaxRetries),
		"allowed_origins":  strings.Join(sCfg.AllowedOrigins, ","),
	}
}

// writeConfigFile rewrites the [opensync] section of data/config.ini while
// preserving comments, unknown keys, and any other sections the operator added.
func writeConfigFile(sCfg ServerConfig) error {
	if err := os.MkdirAll(ConfigDir(), 0700); err != nil {
		return err
	}
	values := configManagedValues(sCfg)
	pending := make(map[string]string, len(values))
	for k, v := range values {
		pending[k] = v
	}

	var out []string
	inOpensync := false
	if existing, err := os.ReadFile(filepath.Join(ConfigDir(), "config.ini")); err == nil {
		for _, line := range strings.Split(string(existing), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
				// Append managed keys that were missing before leaving the
				// [opensync] section.
				if inOpensync && len(pending) > 0 {
					for _, key := range configManagedKeys {
						if v, ok := pending[key]; ok {
							out = append(out, key+"="+v)
							delete(pending, key)
						}
					}
				}
				inOpensync = trimmed == "[opensync]"
				out = append(out, line)
				continue
			}
			if inOpensync {
				if key, _, isKV := strings.Cut(trimmed, "="); isKV {
					key = strings.TrimSpace(key)
					if _, managed := pending[key]; managed {
						out = append(out, key+"="+pending[key])
						delete(pending, key)
						continue
					}
				}
			}
			out = append(out, line)
		}
	}
	if len(pending) > 0 {
		if len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		out = append(out, "[opensync]")
		for _, key := range configManagedKeys {
			if v, ok := pending[key]; ok {
				out = append(out, key+"="+v)
			}
		}
	}
	content := strings.Join(out, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	tmpFile, err := os.CreateTemp(ConfigDir(), "config.ini.*")
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmpFile.WriteString(content); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Chmod(0644); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(ConfigDir(), "config.ini")); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func intConfigValue(value string, fallback int, key string) int {
	i, err := strconv.Atoi(value)
	if err != nil {
		log.Printf("配置项 %s=%q 不是有效整数，将使用默认值 %d", key, value, fallback)
		return fallback
	}
	return i
}

func stringConfigValue(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

// readINI parses a simple INI file
func readINI(filename string) map[string]map[string]string {
	result := make(map[string]map[string]string)
	f, err := os.Open(filename)
	if err != nil {
		log.Printf("配置文件读取失败: %v", err)
		return result
	}
	defer f.Close()

	var section string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			if _, ok := result[section]; !ok {
				result[section] = make(map[string]string)
			}
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 && section != "" {
			result[section][strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return result
}
