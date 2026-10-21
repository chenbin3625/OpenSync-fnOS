package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"opensync/internal/msg"
	"opensync/pkg/crypto"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// ServerConfig holds server configuration
//
// The server only listens on the fnOS app socket (or 127.0.0.1:--port in
// development), so the former bind/port/log_level/console_level/log_save keys
// and the OPENSYNC_TLS_* variables have no effect. They are still tolerated in
// existing config.ini files (kept verbatim on save) but are no longer parsed
// or written; see deprecatedConfigKeys.
type ServerConfig struct {
	TaskSave        int
	Timeout         int
	CopyConcurrency int
	ScanConcurrency int
	MaxRetries      int
	PasswdStr       string
	// AllowedOrigins lists the origins allowed to perform mutations. Entries are
	// full origins ("http://nas.example:5666") or bare hosts ("nas.example");
	// full origins match scheme and effective port exactly, while bare hosts
	// accept any port. When empty, platform.GatewayRequired accepts JSON writes
	// that the browser does not mark cross-site; setting it additionally
	// requires every write's Origin header to match an entry, so a deployment
	// that sets it must list the address the NAS is actually reached at —
	// otherwise the UI's own writes are rejected.
	AllowedOrigins []string
	// Locale controls the language for user-facing messages (e.g. "zh", "en").
	// Defaults to "zh".
	Locale string
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
	defaultTaskSave    = 30
	defaultTaskTimeout = 48

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
	key, err := loadOrCreatePasswordStr(DataDir())
	if err != nil {
		log.Fatalf("Failed to persist data/secret.key: %v", err)
	}
	return key
}

// loadOrCreatePasswordStr is the testable core of GetPasswordStr. The database
// is scanned for ciphertext before a new key is generated: a missing or empty
// secret.key while stored credentials are encrypted with the lost key is a
// hard error (fail closed). Silently rotating the key would make every stored
// AList token and notification secret permanently undecryptable while the app
// still starts and merely shows empty credentials. Recovery for an empty key is
// delegated to crypto.EnsureNonEmptyFile, which serializes with other processes
// instead of blindly overwriting whatever they wrote.
func loadOrCreatePasswordStr(dataDir string) (string, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return "", fmt.Errorf("create data directory %s: %w", dataDir, err)
	}
	keyFile := filepath.Join(dataDir, "secret.key")
	dbFile := filepath.Join(dataDir, "openSync.db")

	// Scanning the (potentially multi-gigabyte) database is only needed when the
	// key is missing or empty; the common path with a valid key must not pay for
	// it. Encrypted values may also live in the WAL, which
	// databaseHasEncryptedValues covers.
	encryptedScanDone := false
	hasEncrypted := false
	needEncryptedCheck := func() bool {
		if !encryptedScanDone {
			encryptedScanDone = true
			hasEncrypted = databaseHasEncryptedValues(dbFile)
		}
		return hasEncrypted
	}

	_, statErr := os.Stat(keyFile)
	keyMissing := errors.Is(statErr, fs.ErrNotExist)
	if statErr != nil && !keyMissing {
		return "", fmt.Errorf("stat %s: %w", keyFile, statErr)
	}
	if keyMissing && needEncryptedCheck() {
		return "", fmt.Errorf("%s 缺失，但 %s 中已存在以该密钥加密的凭据（%q）；拒绝生成新密钥，以免已存凭据永久不可解密。请从备份恢复 secret.key 后重试", keyFile, dbFile, crypto.EncryptedValuePrefix)
	}

	key, err := crypto.ReadOrSetFile(keyFile, crypto.GeneratePassword(256), false)
	if err == nil {
		return key, nil
	}
	if !keyFileEmpty(keyFile) {
		return "", err
	}
	if needEncryptedCheck() {
		return "", fmt.Errorf("%s 为空，但 %s 中已存在以该密钥加密的凭据（%q）；拒绝生成新密钥，以免已存凭据永久不可解密。请从备份恢复 secret.key 后重试", keyFile, dbFile, crypto.EncryptedValuePrefix)
	}
	log.Printf("Warning: %s is empty and no stored credential uses it; generating a new key", keyFile)
	return crypto.EnsureNonEmptyFile(keyFile, crypto.GeneratePassword(256))
}

func keyFileEmpty(name string) bool {
	data, err := os.ReadFile(name)
	return err == nil && len(strings.TrimSpace(string(data))) == 0
}

// databaseHasEncryptedValues reports whether the database (or its WAL) may
// hold a value encrypted with the application key. It scans the raw files for
// the ciphertext prefix instead of opening the database, because the key is
// needed before the database is initialised. Any read error counts as "yes":
// the caller only uses a "no" to justify replacing the key.
func databaseHasEncryptedValues(dbFile string) bool {
	for _, name := range []string{dbFile, dbFile + "-wal"} {
		found, err := fileContains(name, []byte(crypto.EncryptedValuePrefix))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || found {
			return true
		}
	}
	return false
}

func fileContains(name string, needle []byte) (bool, error) {
	f, err := os.Open(name)
	if err != nil {
		return false, err
	}
	defer f.Close()
	buf := make([]byte, 1<<20)
	carry := 0
	for {
		n, readErr := f.Read(buf[carry:])
		window := buf[:carry+n]
		if bytes.Contains(window, needle) {
			return true, nil
		}
		// Keep the tail so a match spanning two reads is not missed.
		carry = len(needle) - 1
		if carry > len(window) {
			carry = len(window)
		}
		copy(buf, window[len(window)-carry:])
		if readErr == io.EOF {
			return false, nil
		}
		if readErr != nil {
			return false, readErr
		}
	}
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

	sCfg, err := loadServerConfig(passwdStr)
	if err != nil {
		// Fail closed. Falling back to defaults here would silently drop
		// allowed_origins (and every other operator setting) whenever the file
		// exists but cannot be read, turning a strict origin policy into the
		// permissive default without anyone noticing.
		log.Fatalf("配置文件 %s 存在但无法解析或不完整，已停止启动以免以默认配置运行: %v", filepath.Join(ConfigDir(), "config.ini"), err)
	}

	sysConfig = &Config{
		DB:     DBConfig{DBName: dbname},
		Server: sCfg,
	}
	clampServerConfig(&sysConfig.Server)
	// SetLocale normalizes and validates the value ("EN", "en-US" and "zh_CN"
	// all work); store the effective locale so callers never see an unusable
	// string that differs from what msg.T actually uses.
	msg.SetLocale(sysConfig.Server.Locale)
	sysConfig.Server.Locale = msg.Locale()
	return sysConfig
}

// deprecatedConfigKeys are [opensync] keys older releases wrote but that never
// had any effect: the server listens only on the fnOS app socket. They are
// ignored on load and left untouched (not rewritten) on save, so existing
// config.ini files keep working.
var deprecatedConfigKeys = []string{"bind", "port", "log_level", "console_level", "log_save"}

// deprecatedConfigEnv are environment variables with no effect any more.
var deprecatedConfigEnv = []string{
	"OPENSYNC_BIND", "OPENSYNC_PORT", "OPENSYNC_LOG_LEVEL", "OPENSYNC_CONSOLE_LEVEL",
	"OPENSYNC_LOG_SAVE", "OPENSYNC_TLS_CERT", "OPENSYNC_TLS_KEY",
}

// knownConfigKeys are the [opensync] keys this release understands. Anything
// else is reported (not fatal) so a typo such as "max_retry" cannot silently
// leave the default in place.
var knownConfigKeys = map[string]struct{}{
	"task_save": {}, "task_timeout": {}, "copy_concurrency": {},
	"scan_concurrency": {}, "max_retries": {},
	"allowed_origins": {}, "locale": {},
}

// warnUnknownConfigKeys logs [opensync] keys that have no effect. Keys are
// sorted so the log is stable across runs.
func warnUnknownConfigKeys(opensync map[string]string) {
	keys := make([]string, 0, len(opensync))
	for key := range opensync {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := knownConfigKeys[key]; ok {
			continue
		}
		deprecated := false
		for _, deprecatedKey := range deprecatedConfigKeys {
			if key == deprecatedKey {
				// Already reported by the deprecated-key loop with a clearer text.
				deprecated = true
				break
			}
		}
		if !deprecated {
			log.Printf("Warning: config.ini key %s is unknown and has no effect", key)
		}
	}
}

// loadServerConfig layers the sources: defaults, then OPENSYNC_* environment
// variables, then every key present in config.ini. config.ini wins because it
// is what the settings page writes; a key the file does not contain (for
// example locale or allowed_origins after the page created the file) keeps the
// environment value instead of silently reverting to the default. An existing
// file that cannot be stat'ed, opened or parsed is an error rather than a
// reason to use defaults.
func loadServerConfig(passwdStr string) (ServerConfig, error) {
	sCfg := ServerConfig{
		TaskSave:        defaultTaskSave,
		Timeout:         defaultTaskTimeout,
		CopyConcurrency: DefaultCopyConcurrency,
		ScanConcurrency: DefaultScanConcurrency,
		MaxRetries:      DefaultMaxRetries,
		PasswdStr:       passwdStr,
	}

	sCfg.TaskSave = envIntConfigValue("OPENSYNC_TASK_SAVE", sCfg.TaskSave)
	sCfg.Timeout = envIntConfigValue("OPENSYNC_TASK_TIMEOUT", sCfg.Timeout)
	sCfg.CopyConcurrency = envIntConfigValue("OPENSYNC_COPY_CONCURRENCY", sCfg.CopyConcurrency)
	sCfg.ScanConcurrency = envIntConfigValue("OPENSYNC_SCAN_CONCURRENCY", sCfg.ScanConcurrency)
	sCfg.MaxRetries = envIntConfigValue("OPENSYNC_MAX_RETRIES", sCfg.MaxRetries)
	sCfg.AllowedOrigins = splitList(os.Getenv("OPENSYNC_ALLOWED_ORIGINS"))
	sCfg.Locale = envStringConfigValue("OPENSYNC_LOCALE", sCfg.Locale)
	for _, name := range deprecatedConfigEnv {
		if os.Getenv(name) != "" {
			log.Printf("Warning: environment variable %s is deprecated and has no effect", name)
		}
	}

	configPath := filepath.Join(ConfigDir(), "config.ini")
	_, statErr := os.Stat(configPath)
	if statErr != nil && !errors.Is(statErr, fs.ErrNotExist) {
		return ServerConfig{}, statErr
	}
	if statErr == nil {
		iniMap, err := readINI(configPath)
		if err != nil {
			return ServerConfig{}, err
		}
		opensync, ok := iniMap["opensync"]
		if !ok {
			// A file without an [opensync] section used to be ignored, silently
			// replacing allowed_origins/locale/etc. with defaults. Fail closed:
			// the caller reports it and stops startup instead of running with a
			// policy nobody chose.
			return ServerConfig{}, fmt.Errorf("配置文件 %s 存在但缺少 [opensync] 配置段（仅解析出 %d 个配置段）；已停止启动以免静默使用默认配置。确认无误后可删除该文件以恢复默认值", configPath, len(iniMap))
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
		if v, ok := opensync["locale"]; ok {
			sCfg.Locale = stringConfigValue(v, sCfg.Locale)
		}
		for _, key := range deprecatedConfigKeys {
			if _, ok := opensync[key]; ok {
				log.Printf("Warning: config.ini key %s is deprecated and has no effect", key)
			}
		}
		warnUnknownConfigKeys(opensync)
	}
	return sCfg, nil
}

// clampServerConfig enforces the same ranges as validateSystemSettings on
// values loaded from config.ini or environment variables. Manually edited
// values that fall outside the allowed range fall back to the default rather
// than silently taking effect (e.g. copy_concurrency=99999 spawning an
// unbounded number of goroutines, or task_timeout=-1 producing a negative
// timeout).
func clampServerConfig(sCfg *ServerConfig) {
	sCfg.Timeout = clampInt(sCfg.Timeout, minTaskTimeout, maxTaskTimeout, defaultTaskTimeout)
	sCfg.TaskSave = clampInt(sCfg.TaskSave, minTaskSave, maxTaskSave, defaultTaskSave)
	sCfg.CopyConcurrency = clampInt(sCfg.CopyConcurrency, MinCopyConcurrency, MaxCopyConcurrency, DefaultCopyConcurrency)
	sCfg.ScanConcurrency = clampInt(sCfg.ScanConcurrency, MinScanConcurrency, MaxScanConcurrency, DefaultScanConcurrency)
	sCfg.MaxRetries = clampInt(sCfg.MaxRetries, MinMaxRetries, MaxRetryAttempts, DefaultMaxRetries)
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
	return settingsFromServer(GetConfig().Server)
}

func settingsFromServer(s ServerConfig) SystemSettings {
	return SystemSettings{
		TaskTimeout:     s.Timeout,
		TaskSave:        s.TaskSave,
		CopyConcurrency: s.CopyConcurrency,
		ScanConcurrency: s.ScanConcurrency,
		MaxRetries:      s.MaxRetries,
	}
}

// ErrConfigWrite marks a failure to persist config.ini. The wrapped cause
// carries OS paths and errno text, so callers log it instead of showing it;
// validation errors are returned unwrapped and are safe to display.
var ErrConfigWrite = errors.New("write config file")

// configConflictError reports that config.ini was edited by hand while this
// process was running. Its message is localized and safe to show to the user,
// so UpdateSystemSettings returns it unwrapped instead of ErrConfigWrite (the
// handler shows non-ErrConfigWrite errors as 400 with their text).
type configConflictError struct {
	detail string
}

func (e *configConflictError) Error() string {
	return fmt.Sprintf(msg.T(msg.ConfigChangedOnDisk), e.detail)
}

// UpdateSystemSettings validates, persists, and applies runtime-editable settings.
func UpdateSystemSettings(settings SystemSettings) error {
	return UpdateSystemSettingsWith(func(SystemSettings) SystemSettings { return settings })
}

// UpdateSystemSettingsWith computes the new settings from the current ones and
// persists them under a single hold of the config lock, so concurrent partial
// updates (PUT with a subset of fields) cannot overwrite each other's fields
// with a stale read.
func UpdateSystemSettingsWith(update func(current SystemSettings) SystemSettings) error {
	GetConfig()
	configMu.Lock()
	defer configMu.Unlock()

	cfg := sysConfig
	settings := update(settingsFromServer(cfg.Server))
	if err := validateSystemSettings(settings); err != nil {
		return err
	}
	nextServer := cfg.Server
	nextServer.Timeout = settings.TaskTimeout
	nextServer.TaskSave = settings.TaskSave
	nextServer.CopyConcurrency = settings.CopyConcurrency
	nextServer.ScanConcurrency = settings.ScanConcurrency
	nextServer.MaxRetries = settings.MaxRetries

	if err := writeConfigFile(nextServer, cfg.Server); err != nil {
		var conflict *configConflictError
		if errors.As(err, &conflict) {
			// User-actionable conflict, not an opaque OS write failure.
			return err
		}
		return fmt.Errorf("%w: %w", ErrConfigWrite, err)
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
		{msg.T(msg.SettingsTaskTimeout), settings.TaskTimeout, minTaskTimeout, maxTaskTimeout},
		{msg.T(msg.SettingsTaskSave), settings.TaskSave, minTaskSave, maxTaskSave},
		{msg.T(msg.SettingsCopyConcurrency), settings.CopyConcurrency, MinCopyConcurrency, MaxCopyConcurrency},
		{msg.T(msg.SettingsScanConcurrency), settings.ScanConcurrency, MinScanConcurrency, MaxScanConcurrency},
		{msg.T(msg.SettingsMaxRetries), settings.MaxRetries, MinMaxRetries, MaxRetryAttempts},
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

// configManagedKeys are the [opensync] keys the settings page writes. Any
// other line in config.ini — comments, allowed_origins, locale, deprecated
// keys, other sections — is preserved verbatim when settings are updated at
// runtime. Writing only these keys keeps environment-provided values such as
// OPENSYNC_ALLOWED_ORIGINS and OPENSYNC_LOCALE in effect after the first save.
var configManagedKeys = []string{
	"task_save", "task_timeout", "copy_concurrency", "scan_concurrency", "max_retries",
}

func configManagedValues(sCfg ServerConfig) map[string]string {
	return map[string]string{
		"task_save":        strconv.Itoa(sCfg.TaskSave),
		"task_timeout":     strconv.Itoa(sCfg.Timeout),
		"copy_concurrency": strconv.Itoa(sCfg.CopyConcurrency),
		"scan_concurrency": strconv.Itoa(sCfg.ScanConcurrency),
		"max_retries":      strconv.Itoa(sCfg.MaxRetries),
	}
}

// writeConfigFile rewrites the [opensync] section of data/config.ini while
// preserving comments, unknown keys, deprecated keys, allowed_origins, locale
// and any other sections the operator added.
//
// before is the settings snapshot that is currently in effect. When a managed
// key on disk differs from that snapshot, someone edited config.ini by hand
// while the app was running: persisting the in-memory value would silently
// revert their edit, so the write is refused with a user-facing conflict error
// and the operator is asked to restart (which reloads the file and makes the
// snapshot match again). Values that are not valid integers on disk are
// ignored for the conflict check because startup already clamps or defaults
// them (e.g. copy_concurrency=99999).
func writeConfigFile(sCfg ServerConfig, before ServerConfig) error {
	if err := os.MkdirAll(ConfigDir(), 0700); err != nil {
		return err
	}
	values := configManagedValues(sCfg)
	for key, value := range values {
		if !validConfigValue(value) {
			return fmt.Errorf("拒绝写入非法的配置值 %s=%q", key, value)
		}
	}
	pending := make(map[string]string, len(values))
	for k, v := range values {
		pending[k] = v
	}

	var out []string
	inOpensync := false
	// sawOpensync records that the file already has an [opensync] section, so
	// leftover managed keys are flushed into it (at the next section header, or
	// at EOF) instead of being appended under a second [opensync] header.
	sawOpensync := false
	flushPending := func() {
		for _, key := range configManagedKeys {
			if v, ok := pending[key]; ok {
				out = append(out, key+"="+v)
				delete(pending, key)
			}
		}
	}
	existing, err := os.ReadFile(filepath.Join(ConfigDir(), "config.ini"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		content := strings.TrimPrefix(string(existing), "\ufeff")
		if err := checkManagedValuesConflict(content, before); err != nil {
			return err
		}
		for _, line := range strings.Split(content, "\n") {
			trimmed := strings.TrimSpace(line)
			if sectionName, isSection := parseSectionHeader(trimmed); isSection {
				// Append managed keys that were missing before leaving the
				// [opensync] section.
				if inOpensync && len(pending) > 0 {
					flushPending()
				}
				inOpensync = sectionName == "opensync"
				sawOpensync = sawOpensync || inOpensync
				if inOpensync {
					// Rewrite the header canonically so a hand-written
					// "[ OpenSync ]" cannot end up next to a second section.
					out = append(out, "[opensync]")
				} else {
					out = append(out, line)
				}
				continue
			}
			if inOpensync {
				if key, _, isKV := strings.Cut(trimmed, "="); isKV {
					key = strings.TrimSpace(key)
					if value, managed := values[key]; managed {
						out = append(out, key+"="+value)
						delete(pending, key)
						continue
					}
				}
			}
			out = append(out, line)
		}
	}
	// The section ran to EOF: its remaining keys belong inside it, not under a
	// duplicate header.
	if inOpensync && len(pending) > 0 {
		trailing := []string{}
		for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			trailing = append(trailing, out[len(out)-1])
			out = out[:len(out)-1]
		}
		flushPending()
		out = append(out, trailing...)
	}
	if len(pending) > 0 && sawOpensync {
		// An [opensync] section exists but another section follows it, so the
		// keys were already flushed there; anything left is a bug rather than a
		// reason to emit a second header.
		flushPending()
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
	if err := tmpFile.Chmod(0600); err != nil {
		_ = tmpFile.Close()
		return err
	}
	// The data must be durable before the rename publishes it: otherwise a
	// power loss after the rename can leave config.ini as an empty or partial
	// file, which startup now (correctly) refuses to run with.
	if err := tmpFile.Sync(); err != nil {
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
	// The rename itself lives in the directory entry; fsync the directory so it
	// survives a crash too. The new file is already in place and readable, so a
	// failure here is only logged: returning it would make the caller keep the
	// old in-memory settings while the file already holds the new ones.
	if err := syncDir(ConfigDir()); err != nil {
		log.Printf("Warning: config.ini was replaced but fsync of its directory failed: %v", err)
	}
	return nil
}

// managedIntValue returns the in-memory value of one managed key.
func managedIntValue(key string, sCfg ServerConfig) (int, bool) {
	switch key {
	case "task_save":
		return sCfg.TaskSave, true
	case "task_timeout":
		return sCfg.Timeout, true
	case "copy_concurrency":
		return sCfg.CopyConcurrency, true
	case "scan_concurrency":
		return sCfg.ScanConcurrency, true
	case "max_retries":
		return sCfg.MaxRetries, true
	}
	return 0, false
}

// checkManagedValuesConflict refuses to overwrite managed keys that were
// edited by hand while the app was running. The last occurrence of a key wins,
// matching readINI; non-integer on-disk values are ignored because startup
// clamps/defaults them, so a mismatch there is expected.
func checkManagedValuesConflict(content string, before ServerConfig) error {
	managedValues := configManagedValues(before)
	diskValues := make(map[string]string, len(configManagedKeys))
	inOpensync := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if sectionName, isSection := parseSectionHeader(trimmed); isSection {
			inOpensync = sectionName == "opensync"
			continue
		}
		if !inOpensync {
			continue
		}
		key, rawValue, isKV := strings.Cut(trimmed, "=")
		if !isKV {
			continue
		}
		key = strings.TrimSpace(key)
		if _, managed := managedValues[key]; !managed {
			continue
		}
		diskValues[key] = strings.TrimSpace(stripInlineComment(rawValue))
	}
	for _, key := range configManagedKeys {
		diskValue, ok := diskValues[key]
		if !ok || diskValue == "" {
			continue
		}
		diskInt, err := strconv.Atoi(diskValue)
		if err != nil {
			continue
		}
		current, ok := managedIntValue(key, before)
		if !ok || diskInt == current {
			continue
		}
		return &configConflictError{detail: fmt.Sprintf("%s（内存=%d，config.ini=%d）", key, current, diskInt)}
	}
	return nil
}

// validConfigValue defensively rejects control characters in managed values.
// Managed values are integers today, but the writer must never be able to emit
// a newline that would inject extra config.ini lines.
func validConfigValue(value string) bool {
	return !strings.ContainsAny(value, "\r\n\x00")
}

// syncDir fsyncs a directory so a completed rename inside it is durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
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

// maxConfigLineBytes bounds one config.ini line. The previous default
// bufio.Scanner limit (64 KiB) made a long comment or comma list abort startup
// with an opaque "token too long"; 1 MiB is far beyond any legitimate line.
const maxConfigLineBytes = 1 << 20

// readINI parses a simple INI file strictly enough that malformed content is
// reported instead of being silently skipped:
//   - a UTF-8 BOM is stripped;
//   - section names are trimmed and case-folded ("[ opensync ]", "[OpenSync]");
//   - a key/value line before any section, an unterminated section header, or a
//     garbage line inside [opensync] is an error;
//   - lines without "=" inside foreign sections are preserved/ignored, since
//     writeConfigFile deliberately keeps operator content there;
//   - comments after a value ("task_save=30 # days") are stripped.
func readINI(filename string) (map[string]map[string]string, error) {
	result := make(map[string]map[string]string)
	f, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("配置文件读取失败: %w", err)
	}
	defer f.Close()

	var section string
	lineNo := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxConfigLineBytes)
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if lineNo == 1 {
			line = strings.TrimPrefix(line, "\ufeff")
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			end := strings.IndexByte(line, ']')
			if end < 0 {
				return nil, fmt.Errorf("配置文件第 %d 行段头缺少 ]: %q", lineNo, line)
			}
			if rest := strings.TrimSpace(line[end+1:]); rest != "" && !strings.HasPrefix(rest, "#") && !strings.HasPrefix(rest, ";") {
				return nil, fmt.Errorf("配置文件第 %d 行段头后存在多余内容: %q", lineNo, line)
			}
			section = normalizeSectionName(line[1:end])
			if section == "" {
				return nil, fmt.Errorf("配置文件第 %d 行段名为空", lineNo)
			}
			if _, ok := result[section]; !ok {
				result[section] = make(map[string]string)
			}
			continue
		}
		if section == "" {
			return nil, fmt.Errorf("配置文件第 %d 行出现在任何配置段之前: %q", lineNo, line)
		}
		key, rawValue, isKV := strings.Cut(line, "=")
		if !isKV {
			if section == "opensync" {
				return nil, fmt.Errorf("配置文件第 %d 行不是 key=value 形式: %q", lineNo, line)
			}
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, fmt.Errorf("配置文件第 %d 行缺少键名: %q", lineNo, line)
		}
		result[section][key] = strings.TrimSpace(stripInlineComment(rawValue))
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	return result, nil
}

// normalizeSectionName trims and case-folds a section name so "[ opensync ]"
// and "[OpenSync]" are recognized as the section the app writes.
func normalizeSectionName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// parseSectionHeader recognizes an INI section header line, tolerating a
// trailing comment ("[opensync] # note"). It mirrors readINI so the writer and
// the loader always agree on where a section starts and ends.
func parseSectionHeader(line string) (name string, ok bool) {
	if !strings.HasPrefix(line, "[") {
		return "", false
	}
	end := strings.IndexByte(line, ']')
	if end < 0 {
		return "", false
	}
	if rest := strings.TrimSpace(line[end+1:]); rest != "" && !strings.HasPrefix(rest, "#") && !strings.HasPrefix(rest, ";") {
		return "", false
	}
	name = normalizeSectionName(line[1:end])
	if name == "" {
		return "", false
	}
	return name, true
}

// stripInlineComment removes a trailing "# ..." / "; ..." comment that is
// preceded by whitespace. Values never legitimately contain a space followed by
// those characters, and URLs carry neither.
func stripInlineComment(value string) string {
	for i := 0; i < len(value); i++ {
		if value[i] != '#' && value[i] != ';' {
			continue
		}
		if i == 0 || value[i-1] == ' ' || value[i-1] == '\t' {
			return value[:i]
		}
	}
	return value
}
