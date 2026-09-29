package crypto

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
