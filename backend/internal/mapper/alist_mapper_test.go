package mapper

import (
	"database/sql"
	"opensync/internal/config"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestAlistTokenIsEncryptedAtRestAndDecryptedOnRead(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()
	if _, err := testDB.Exec(`CREATE TABLE alist_list(
		id integer primary key autoincrement,
		remark text,
		url text,
		userName text,
		token text
	)`); err != nil {
		t.Fatalf("create alist_list: %v", err)
	}

	restoreDB := SetDBForTest(testDB)
	defer restoreDB()
	config.SetConfigForTest(&config.Config{Server: config.ServerConfig{PasswdStr: "credential-test-key"}})
	defer config.SetConfigForTest(nil)

	id, err := AddAlist("test", "https://example.test", "tester", "plain-token")
	if err != nil {
		t.Fatalf("AddAlist() error: %v", err)
	}
	var stored string
	if err := testDB.QueryRow("SELECT token FROM alist_list WHERE id=?", id).Scan(&stored); err != nil {
		t.Fatalf("read stored token: %v", err)
	}
	if stored == "plain-token" {
		t.Fatal("token was stored in plaintext")
	}

	row, err := GetAlistByID(id)
	if err != nil {
		t.Fatalf("GetAlistByID() error: %v", err)
	}
	if row["token"] != "plain-token" {
		t.Fatalf("decrypted token = %q, want plain-token", row["token"])
	}
}

func TestMigrateStoredCredentialsEncryptsLegacyRows(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()
	for _, stmt := range []string{
		`CREATE TABLE alist_list(id integer primary key, token text)`,
		`CREATE TABLE notify(id integer primary key, params text)`,
		`INSERT INTO alist_list(id, token) VALUES (1, 'legacy-token')`,
		`INSERT INTO notify(id, params) VALUES (1, '{"sendKey":"legacy-key"}')`,
	} {
		if _, err := testDB.Exec(stmt); err != nil {
			t.Fatalf("setup SQL %q: %v", stmt, err)
		}
	}
	config.SetConfigForTest(&config.Config{Server: config.ServerConfig{PasswdStr: "migration-test-key"}})
	defer config.SetConfigForTest(nil)

	if err := migrateStoredCredentials(testDB); err != nil {
		t.Fatalf("migrateStoredCredentials() error: %v", err)
	}
	for _, query := range []string{
		"SELECT token FROM alist_list WHERE id=1",
		"SELECT params FROM notify WHERE id=1",
	} {
		var stored string
		if err := testDB.QueryRow(query).Scan(&stored); err != nil {
			t.Fatalf("read migrated credential: %v", err)
		}
		if stored == "legacy-token" || stored == `{"sendKey":"legacy-key"}` {
			t.Fatalf("credential remained plaintext: %q", stored)
		}
	}

	restoreDB := SetDBForTest(testDB)
	defer restoreDB()
	alist, err := GetAlistByID(1)
	if err != nil {
		t.Fatalf("GetAlistByID() error: %v", err)
	}
	if alist["token"] != "legacy-token" {
		t.Fatalf("migrated token = %q, want legacy-token", alist["token"])
	}
	notify, err := GetNotifyByID(1)
	if err != nil {
		t.Fatalf("GetNotifyByID() error: %v", err)
	}
	if notify["params"] != `{"sendKey":"legacy-key"}` {
		t.Fatalf("migrated params = %q", notify["params"])
	}
}

func TestNotifyParamsAreEncryptedAtRestAndDecryptedOnRead(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()
	if _, err := testDB.Exec(`CREATE TABLE notify(
		id integer primary key autoincrement,
		enable integer,
		method integer,
		params text
	)`); err != nil {
		t.Fatalf("create notify: %v", err)
	}
	restoreDB := SetDBForTest(testDB)
	defer restoreDB()
	config.SetConfigForTest(&config.Config{Server: config.ServerConfig{PasswdStr: "notify-test-key"}})
	defer config.SetConfigForTest(nil)

	params := `{"sendKey":"secret-key"}`
	id, err := AddNotify(map[string]interface{}{
		"enable": 1,
		"method": 1,
		"params": params,
	})
	if err != nil {
		t.Fatalf("AddNotify() error: %v", err)
	}
	var stored string
	if err := testDB.QueryRow("SELECT params FROM notify WHERE id=?", id).Scan(&stored); err != nil {
		t.Fatalf("read stored params: %v", err)
	}
	if stored == params || strings.Contains(stored, "secret-key") {
		t.Fatalf("notification params were stored in plaintext: %q", stored)
	}
	notify, err := GetNotifyByID(id)
	if err != nil {
		t.Fatalf("GetNotifyByID() error: %v", err)
	}
	if notify["params"] != params {
		t.Fatalf("decrypted params = %q, want %q", notify["params"], params)
	}
}

func TestCountJobsByAlistID(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()

	if _, err := testDB.Exec(`CREATE TABLE job(
		id integer primary key autoincrement,
		alistId integer
	)`); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := testDB.Exec("INSERT INTO job(alistId) VALUES (7), (7), (8)"); err != nil {
		t.Fatalf("insert jobs: %v", err)
	}

	oldDB := db
	db = testDB
	defer func() {
		db = oldDB
	}()

	count, err := CountJobsByAlistID(7)
	if err != nil {
		t.Fatalf("CountJobsByAlistID() error: %v", err)
	}
	if count != 2 {
		t.Fatalf("CountJobsByAlistID(7) = %d, want 2", count)
	}
}
