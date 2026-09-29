package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An empty secret.key (left by an older non-atomic write) is regenerated only
// when nothing in the database was encrypted with the lost key.
func TestDatabaseHasEncryptedValues(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "openSync.db")
	if databaseHasEncryptedValues(db) {
		t.Fatal("missing database reported as holding encrypted values")
	}
	if err := os.WriteFile(db, []byte("SQLite format 3\x00 plain rows only"), 0600); err != nil {
		t.Fatal(err)
	}
	if databaseHasEncryptedValues(db) {
		t.Fatal("database without ciphertext reported as holding encrypted values")
	}
	if err := os.WriteFile(db+"-wal", []byte("xx enc:v1:abc"), 0600); err != nil {
		t.Fatal(err)
	}
	if !databaseHasEncryptedValues(db) {
		t.Fatal("ciphertext in the WAL was not detected")
	}
}

func TestFileContainsFindsMatchAcrossReadBoundary(t *testing.T) {
	name := filepath.Join(t.TempDir(), "f")
	content := strings.Repeat("a", (1<<20)-3) + "enc:v1:"
	if err := os.WriteFile(name, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	found, err := fileContains(name, []byte("enc:v1:"))
	if err != nil || !found {
		t.Fatalf("fileContains() = %v, %v; want a match spanning two reads", found, err)
	}
}
