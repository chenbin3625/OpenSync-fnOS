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
