package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const encryptedValuePrefix = "enc:v1:"

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

const (
	charset           = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	recoveryKeyLength = 24
)

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

// GenerateRecoveryKey creates a one-time recovery key shown only to the user.
func GenerateRecoveryKey() string {
	return GeneratePassword(recoveryKeyLength)
}

// HashPassword creates a bcrypt password hash for newly stored passwords.
func HashPassword(passwd string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(passwd), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// CheckPassword validates modern bcrypt password hashes.
func CheckPassword(passwd string, storedHash string) bool {
	if !IsModernPasswordHash(storedHash) {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(passwd)) == nil
}

// IsModernPasswordHash reports whether a stored hash uses the current format.
func IsModernPasswordHash(storedHash string) bool {
	return strings.HasPrefix(storedHash, "$2a$") ||
		strings.HasPrefix(storedHash, "$2b$") ||
		strings.HasPrefix(storedHash, "$2y$")
}

// ReadOrSetFile reads file content, creating it with defaultValue when it does
// not exist. Callers must treat a persist error as fatal when the value feeds
// cryptography: falling back to an in-memory default would silently rotate the
// secret on every restart and invalidate all stored cookies and tokens.
func ReadOrSetFile(fileName string, defaultVal string, force bool) (string, error) {
	if !force {
		if data, err := os.ReadFile(fileName); err == nil && len(strings.TrimSpace(string(data))) > 0 {
			_ = os.Chmod(fileName, 0600)
			return string(data), nil
		}
	}
	dir := filepath.Dir(fileName)
	if dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", err
		}
	}
	if err := os.WriteFile(fileName, []byte(defaultVal), 0600); err != nil {
		return "", err
	}
	return defaultVal, nil
}
