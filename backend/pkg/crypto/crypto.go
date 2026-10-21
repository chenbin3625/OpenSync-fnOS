package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const encryptedValuePrefix = "enc:v1:"

// EncryptedValuePrefix marks values produced by EncryptString.
const EncryptedValuePrefix = encryptedValuePrefix

// EncryptString encrypts a value with AES-GCM using a key derived from the
// persisted application secret. The versioned prefix supports future formats.
func EncryptString(value, secret string) (string, error) {
	if secret == "" {
		return "", errors.New("encryption secret is empty")
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(value), []byte(encryptedValuePrefix))
	return encryptedValuePrefix + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// DecryptString decrypts versioned ciphertext. Legacy plaintext is returned
// unchanged with encrypted=false so callers can migrate it safely.
func DecryptString(value, secret string) (plaintext string, encrypted bool, err error) {
	if !strings.HasPrefix(value, encryptedValuePrefix) {
		return value, false, nil
	}
	if secret == "" {
		return "", true, errors.New("encryption secret is empty")
	}
	payload, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, encryptedValuePrefix))
	if err != nil {
		return "", true, fmt.Errorf("decode encrypted value: %w", err)
	}
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", true, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", true, err
	}
	if len(payload) < gcm.NonceSize() {
		return "", true, errors.New("encrypted value is truncated")
	}
	nonce, ciphertext := payload[:gcm.NonceSize()], payload[gcm.NonceSize():]
	plaintextBytes, err := gcm.Open(nil, nonce, ciphertext, []byte(encryptedValuePrefix))
	if err != nil {
		return "", true, fmt.Errorf("decrypt encrypted value: %w", err)
	}
	return string(plaintextBytes), true, nil
}

const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// GeneratePassword generates a random password of given length
func GeneratePassword(length int) string {
	b := make([]byte, length)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			panic(err)
		}
		b[i] = charset[n.Int64()]
	}
	return string(b)
}

// ReadOrSetFile reads file content, creating it with defaultValue when it does
// not exist. Callers must treat a persist error as fatal when the value feeds
// cryptography: falling back to an in-memory default would silently rotate the
// secret on every restart and invalidate all stored cookies and tokens.
//
// A new value is written only when the file is genuinely absent. Any other read
// failure (EIO, EACCES, a directory in the way) and an existing file that is
// empty or whitespace-only are returned as errors: the file may still hold —
// or have held — the key every stored credential was encrypted with, and
// replacing it would make all of them permanently undecryptable. force keeps
// its meaning of "always write defaultVal", and replaces the file atomically.
func ReadOrSetFile(fileName string, defaultVal string, force bool) (string, error) {
	if !force {
		data, err := os.ReadFile(fileName)
		switch {
		case err == nil:
			if len(strings.TrimSpace(string(data))) == 0 {
				return "", fmt.Errorf("%s exists but is empty; refusing to replace it with a new value", fileName)
			}
			// A secret file that an older release created with wider permissions
			// must be tightened; silently leaving it world-readable would expose
			// every stored credential. A failed chmod is treated like any other
			// persist failure so startup stops instead of running with a leak.
			if err := os.Chmod(fileName, 0600); err != nil {
				return "", fmt.Errorf("chmod %s to 0600: %w", fileName, err)
			}
			return string(data), nil
		case !errors.Is(err, fs.ErrNotExist):
			return "", fmt.Errorf("read %s: %w", fileName, err)
		}
	}
	dir := filepath.Dir(fileName)
	if dir != "." {
		// The directory holds key material: keep it owner-only rather than
		// relying on the caller having created it with the right mode.
		if err := os.MkdirAll(dir, 0700); err != nil {
			return "", err
		}
	}
	if force {
		if err := writeFileAtomic(fileName, []byte(defaultVal)); err != nil {
			return "", err
		}
		return defaultVal, nil
	}
	// O_EXCL: if another process created the file between the read above and
	// here, keep its value instead of overwriting it.
	if err := writeNewFile(fileName, []byte(defaultVal)); err != nil {
		if errors.Is(err, fs.ErrExist) {
			data, readErr := os.ReadFile(fileName)
			if readErr != nil {
				return "", fmt.Errorf("read %s: %w", fileName, readErr)
			}
			if len(strings.TrimSpace(string(data))) == 0 {
				return "", fmt.Errorf("%s exists but is empty; refusing to replace it with a new value", fileName)
			}
			return string(data), nil
		}
		return "", err
	}
	return defaultVal, nil
}

// EnsureNonEmptyFile returns the current content of fileName, creating or
// replacing it with defaultVal only when it is absent or empty/whitespace.
//
// Unlike ReadOrSetFile(force=true), which blindly overwrites, this function is
// safe to call when another process may be doing the same thing: it serializes
// on a sibling lock file, re-reads before deciding to write, and returns the
// value that is actually on disk afterwards. Callers use it to recover a
// secret that a crash left empty without ever rotating a valid key.
func EnsureNonEmptyFile(fileName string, defaultVal string) (string, error) {
	dir := filepath.Dir(fileName)
	if dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return "", err
		}
	}
	unlock, err := acquireFileLock(fileName)
	if err != nil {
		return "", err
	}
	defer unlock()

	if existing, ok, err := readNonEmptyFile(fileName); err != nil {
		return "", err
	} else if ok {
		return existing, nil
	}
	if err := writeFileAtomic(fileName, []byte(defaultVal)); err != nil {
		return "", err
	}
	// Another writer may have replaced the file between our rename and this
	// read; return what is genuinely persisted so callers never keep a value
	// that is not on disk.
	value, ok, err := readNonEmptyFile(fileName)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("%s is empty after writing a new value", fileName)
	}
	return value, nil
}

// readNonEmptyFile reads a non-empty file and tightens its permissions. ok is
// false when the file does not exist or contains only whitespace.
func readNonEmptyFile(fileName string) (value string, ok bool, err error) {
	data, err := os.ReadFile(fileName)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("read %s: %w", fileName, err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return "", false, nil
	}
	if err := os.Chmod(fileName, 0600); err != nil {
		return "", false, fmt.Errorf("chmod %s to 0600: %w", fileName, err)
	}
	return string(data), true, nil
}

const (
	fileLockTimeout      = 5 * time.Second
	fileLockStaleAfter   = 30 * time.Second
	fileLockPollInterval = 10 * time.Millisecond
)

// acquireFileLock serializes key rotation between processes (and goroutines)
// using an O_EXCL sibling file. A lock older than fileLockStaleAfter is treated
// as left behind by a crash and removed, so a dead process cannot block startup
// forever.
func acquireFileLock(fileName string) (unlock func(), err error) {
	lockName := fileName + ".lock"
	deadline := time.Now().Add(fileLockTimeout)
	for {
		f, openErr := os.OpenFile(lockName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if openErr == nil {
			_ = f.Close()
			return func() { _ = os.Remove(lockName) }, nil
		}
		if !errors.Is(openErr, fs.ErrExist) {
			return nil, openErr
		}
		if info, statErr := os.Stat(lockName); statErr == nil && time.Since(info.ModTime()) > fileLockStaleAfter {
			_ = os.Remove(lockName)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for %s; another process may be rotating the key", lockName)
		}
		time.Sleep(fileLockPollInterval)
	}
}

// writeNewFile creates fileName exclusively with owner-only permissions and
// fsyncs both the data and the directory entry, so a crash cannot leave an
// empty key file behind that the next start would refuse to use.
func writeNewFile(fileName string, data []byte) error {
	f, err := os.OpenFile(fileName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(fileName)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(fileName)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(fileName)
		return err
	}
	return syncDir(filepath.Dir(fileName))
}

// writeFileAtomic replaces fileName via a synced temp file and rename, so a
// crash leaves either the old or the new content, never a truncated file.
func writeFileAtomic(fileName string, data []byte) error {
	dir := filepath.Dir(fileName)
	tmp, err := os.CreateTemp(dir, filepath.Base(fileName)+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, fileName); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
