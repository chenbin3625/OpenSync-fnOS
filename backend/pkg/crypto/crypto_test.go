package crypto

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEncryptStringRoundTripsAndRejectsWrongKey(t *testing.T) {
	ciphertext, err := EncryptString("sensitive-value", "primary-key")
	if err != nil {
		t.Fatalf("EncryptString() error: %v", err)
	}
	if ciphertext == "sensitive-value" || strings.Contains(ciphertext, "sensitive-value") {
		t.Fatalf("ciphertext exposes plaintext: %q", ciphertext)
	}
	plaintext, encrypted, err := DecryptString(ciphertext, "primary-key")
	if err != nil {
		t.Fatalf("DecryptString() error: %v", err)
	}
	if !encrypted || plaintext != "sensitive-value" {
		t.Fatalf("DecryptString() = %q/%v, want sensitive-value/true", plaintext, encrypted)
	}
	if _, _, err := DecryptString(ciphertext, "wrong-key"); err == nil {
		t.Fatal("DecryptString() accepted the wrong key")
	}
}

func TestDecryptStringKeepsLegacyPlaintextReadable(t *testing.T) {
	plaintext, encrypted, err := DecryptString("legacy-value", "primary-key")
	if err != nil {
		t.Fatalf("DecryptString() error: %v", err)
	}
	if encrypted || plaintext != "legacy-value" {
		t.Fatalf("DecryptString() = %q/%v, want legacy-value/false", plaintext, encrypted)
	}
}

func TestReadOrSetFileCreatesSecretWithOwnerOnlyPermissions(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "nested", "secret.key")

	got, err := ReadOrSetFile(secretPath, "secret-value", false)
	if err != nil {
		t.Fatalf("ReadOrSetFile() error: %v", err)
	}
	if got != "secret-value" {
		t.Fatalf("ReadOrSetFile() = %q, want default secret", got)
	}

	info, err := os.Stat(secretPath)
	if err != nil {
		t.Fatalf("stat secret: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Fatalf("secret permissions = %o, want 0600", mode)
	}
}

func TestReadOrSetFileReportsUnwritableLocation(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup blocker: %v", err)
	}
	// The parent path is a regular file, so creating the nested directory must
	// fail and the error must surface instead of returning the default value.
	if _, err := ReadOrSetFile(filepath.Join(blocker, "secret.key"), "secret-value", false); err == nil {
		t.Fatalf("ReadOrSetFile() = nil error, want persist failure")
	}
}

// An existing key file that reads back empty (truncated by a crash, or a
// placeholder) must not be silently replaced: the old key may be recoverable,
// and a new one makes every stored credential undecryptable.
func TestReadOrSetFileRefusesToReplaceEmptyFile(t *testing.T) {
	for name, content := range map[string]string{"empty": "", "whitespace": " \n\t"} {
		t.Run(name, func(t *testing.T) {
			secretPath := filepath.Join(t.TempDir(), "secret.key")
			if err := os.WriteFile(secretPath, []byte(content), 0o600); err != nil {
				t.Fatalf("setup secret: %v", err)
			}
			if _, err := ReadOrSetFile(secretPath, "replacement", false); err == nil {
				t.Fatal("ReadOrSetFile() = nil error, want refusal for an empty existing file")
			}
			data, err := os.ReadFile(secretPath)
			if err != nil {
				t.Fatalf("read secret: %v", err)
			}
			if string(data) != content {
				t.Fatalf("secret file = %q, want it left untouched as %q", data, content)
			}
		})
	}
}

// A read failure other than "does not exist" must surface; it is not a
// licence to generate and write a new key.
func TestReadOrSetFileReportsReadErrorsWithoutOverwriting(t *testing.T) {
	// A directory at the key path makes ReadFile fail with an error that is not
	// fs.ErrNotExist, standing in for EIO/EACCES.
	secretPath := filepath.Join(t.TempDir(), "secret.key")
	if err := os.Mkdir(secretPath, 0o700); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := ReadOrSetFile(secretPath, "replacement", false); err == nil {
		t.Fatal("ReadOrSetFile() = nil error, want the read error")
	}
	info, err := os.Stat(secretPath)
	if err != nil || !info.IsDir() {
		t.Fatalf("key path was replaced: info=%v err=%v", info, err)
	}
}

func TestReadOrSetFileForceReplacesContentWithOwnerOnlyPermissions(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "secret.key")
	if err := os.WriteFile(secretPath, []byte("old"), 0o644); err != nil {
		t.Fatalf("setup secret: %v", err)
	}
	got, err := ReadOrSetFile(secretPath, "new", true)
	if err != nil || got != "new" {
		t.Fatalf("ReadOrSetFile(force) = %q, %v; want new", got, err)
	}
	data, err := os.ReadFile(secretPath)
	if err != nil || string(data) != "new" {
		t.Fatalf("file = %q, %v; want new", data, err)
	}
	info, err := os.Stat(secretPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("directory entries = %v, %v; want only the key file (no temp leftovers)", entries, err)
	}
}

func TestReadOrSetFileReturnsExistingContent(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "secret.key")
	if err := os.WriteFile(secretPath, []byte("existing"), 0o600); err != nil {
		t.Fatalf("setup secret: %v", err)
	}
	got, err := ReadOrSetFile(secretPath, "replacement", false)
	if err != nil {
		t.Fatalf("ReadOrSetFile() error: %v", err)
	}
	if got != "existing" {
		t.Fatalf("ReadOrSetFile() = %q, want existing content", got)
	}
}

// A key file left with wider permissions by an older release must be tightened
// when it is read (review P3-12).
func TestReadOrSetFileTightensExistingFilePermissions(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "secret.key")
	if err := os.WriteFile(secretPath, []byte("existing"), 0o644); err != nil {
		t.Fatalf("setup secret: %v", err)
	}
	if _, err := ReadOrSetFile(secretPath, "replacement", false); err != nil {
		t.Fatalf("ReadOrSetFile() error: %v", err)
	}
	info, err := os.Stat(secretPath)
	if err != nil {
		t.Fatalf("stat secret: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %o, want 0600", mode)
	}
}

// EnsureNonEmptyFile is the crash-recovery path for an empty secret.key: it
// replaces an empty file, preserves a valid one, and never leaves the lock or
// temp files behind (review P2-1/P3-13).
func TestEnsureNonEmptyFileCreatesAndReplaces(t *testing.T) {
	for name, existing := range map[string]string{
		"absent":     "",
		"empty":      "",
		"whitespace": " \n\t",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			secretPath := filepath.Join(dir, "secret.key")
			if name != "absent" {
				if err := os.WriteFile(secretPath, []byte(existing), 0o600); err != nil {
					t.Fatalf("setup secret: %v", err)
				}
			}
			got, err := EnsureNonEmptyFile(secretPath, "recovered-key")
			if err != nil {
				t.Fatalf("EnsureNonEmptyFile() error: %v", err)
			}
			if got != "recovered-key" {
				t.Fatalf("EnsureNonEmptyFile() = %q, want recovered-key", got)
			}
			data, err := os.ReadFile(secretPath)
			if err != nil || string(data) != "recovered-key" {
				t.Fatalf("file = %q, %v; want recovered-key", data, err)
			}
			info, err := os.Stat(secretPath)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("mode = %v, %v; want 0600", info, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("directory entries = %v, %v; want only secret.key", entries, err)
			}
		})
	}
}

func TestEnsureNonEmptyFilePreservesValidKey(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "secret.key")
	if err := os.WriteFile(secretPath, []byte("current-key"), 0o644); err != nil {
		t.Fatalf("setup secret: %v", err)
	}
	got, err := EnsureNonEmptyFile(secretPath, "replacement")
	if err != nil || got != "current-key" {
		t.Fatalf("EnsureNonEmptyFile() = %q, %v; want current-key", got, err)
	}
	info, err := os.Stat(secretPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v; want 0600 after tightening", info, err)
	}
}

// Two starts recovering the same empty key must converge on one value instead
// of each keeping a different in-memory key (review P3-13).
func TestEnsureNonEmptyFileSerializesConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "secret.key")
	if err := os.WriteFile(secretPath, nil, 0o600); err != nil {
		t.Fatalf("setup empty secret: %v", err)
	}
	const writers = 8
	results := make(chan string, writers)
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := EnsureNonEmptyFile(secretPath, "shared-key")
			if err != nil {
				errs <- err
				return
			}
			results <- got
		}()
	}
	wg.Wait()
	close(errs)
	close(results)
	for err := range errs {
		t.Fatalf("EnsureNonEmptyFile() error: %v", err)
	}
	for got := range results {
		if got != "shared-key" {
			t.Fatalf("EnsureNonEmptyFile() = %q, want shared-key", got)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("directory entries = %v, %v; want only secret.key (no lock/temp leftovers)", entries, err)
	}
}

// A lock left behind by a crashed process must not block recovery forever
// (review P3-13).
func TestEnsureNonEmptyFileRemovesStaleLock(t *testing.T) {
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "secret.key")
	lockPath := secretPath + ".lock"
	if err := os.WriteFile(lockPath, []byte("stale"), 0o600); err != nil {
		t.Fatalf("setup lock: %v", err)
	}
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatalf("age lock: %v", err)
	}
	got, err := EnsureNonEmptyFile(secretPath, "recovered-key")
	if err != nil || got != "recovered-key" {
		t.Fatalf("EnsureNonEmptyFile() = %q, %v; want recovered-key", got, err)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("lock file still exists: %v", err)
	}
}
