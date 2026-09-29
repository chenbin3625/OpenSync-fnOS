package mapper

import (
	"database/sql"
	"errors"
	"fmt"
	"opensync/internal/config"
	"opensync/internal/model"
	"opensync/internal/msg"
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

// seedUndecryptableCredentials stores one row per table encrypted with a key
// the test then replaces, reproducing a lost or regenerated secret.key.
func seedUndecryptableCredentials(t *testing.T, testDB *sql.DB) {
	t.Helper()
	for _, stmt := range []string{
		`CREATE TABLE alist_list(id integer primary key, remark text, url text, userName text, token text)`,
		`CREATE TABLE notify(id integer primary key, enable integer, method integer, params text)`,
	} {
		if _, err := testDB.Exec(stmt); err != nil {
			t.Fatalf("setup SQL %q: %v", stmt, err)
		}
	}
	config.SetConfigForTest(&config.Config{Server: config.ServerConfig{PasswdStr: "old-key"}})
	oldToken, err := encryptCredential("old-token")
	if err != nil {
		t.Fatal(err)
	}
	oldParams, err := encryptCredential(`{"sendKey":"old-key"}`)
	if err != nil {
		t.Fatal(err)
	}
	config.SetConfigForTest(&config.Config{Server: config.ServerConfig{PasswdStr: "new-key"}})
	goodToken, err := encryptCredential("good-token")
	if err != nil {
		t.Fatal(err)
	}
	goodParams, err := encryptCredential(`{"sendKey":"good-key"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		query string
		args  []interface{}
	}{
		{"INSERT INTO alist_list(id, url, token) VALUES (1, 'https://lost.test', ?)", []interface{}{oldToken}},
		{"INSERT INTO alist_list(id, url, token) VALUES (2, 'https://good.test', ?)", []interface{}{goodToken}},
		{"INSERT INTO notify(id, enable, method, params) VALUES (1, 1, 1, ?)", []interface{}{oldParams}},
		{"INSERT INTO notify(id, enable, method, params) VALUES (2, 1, 1, ?)", []interface{}{goodParams}},
	} {
		if _, err := testDB.Exec(row.query, row.args...); err != nil {
			t.Fatalf("seed row: %v", err)
		}
	}
}

// Losing secret.key used to make InitSQL log.Fatalf on the first unreadable
// row, so the service never started again.
func TestMigrateStoredCredentialsSkipsUndecryptableRows(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()
	defer config.SetConfigForTest(nil)
	seedUndecryptableCredentials(t, testDB)
	if _, err := testDB.Exec(`INSERT INTO notify(id, enable, method, params) VALUES (3, 1, 1, '{"sendKey":"legacy"}')`); err != nil {
		t.Fatal(err)
	}
	var before string
	if err := testDB.QueryRow("SELECT token FROM alist_list WHERE id=1").Scan(&before); err != nil {
		t.Fatal(err)
	}

	if err := migrateStoredCredentials(testDB); err != nil {
		t.Fatalf("migrateStoredCredentials() error = %v, want unreadable rows skipped", err)
	}
	var after string
	if err := testDB.QueryRow("SELECT token FROM alist_list WHERE id=1").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("undecryptable ciphertext was modified; it must be left for recovery with the original key")
	}
	var legacy string
	if err := testDB.QueryRow("SELECT params FROM notify WHERE id=3").Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if legacy == `{"sendKey":"legacy"}` {
		t.Fatal("legacy plaintext row was not migrated after skipping an unreadable row")
	}
}

func TestListReadsBlankUndecryptableCredentialsInsteadOfFailing(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()
	defer config.SetConfigForTest(nil)
	seedUndecryptableCredentials(t, testDB)
	restoreDB := SetDBForTest(testDB)
	defer restoreDB()

	alists, err := GetAlistList()
	if err != nil {
		t.Fatalf("GetAlistList() error = %v, want the readable rows", err)
	}
	if len(alists) != 2 {
		t.Fatalf("GetAlistList() returned %d rows, want 2", len(alists))
	}
	tokens := map[int64]interface{}{}
	for _, row := range alists {
		tokens[row["id"].(int64)] = row["token"]
	}
	if tokens[1] != "" || tokens[2] != "good-token" {
		t.Fatalf("tokens = %#v, want row 1 blanked and row 2 decrypted", tokens)
	}

	notifies, err := GetNotifyList(false)
	if err != nil {
		t.Fatalf("GetNotifyList() error = %v, want the readable rows", err)
	}
	if len(notifies) != 2 {
		t.Fatalf("GetNotifyList() returned %d rows, want 2", len(notifies))
	}
	for _, row := range notifies {
		if strings.HasPrefix(fmt.Sprint(row["params"]), "enc:") {
			t.Fatalf("ciphertext leaked into the list: %#v", row)
		}
	}
}

func TestSingleReadsReportUndecryptableCredentials(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()
	defer config.SetConfigForTest(nil)
	seedUndecryptableCredentials(t, testDB)
	restoreDB := SetDBForTest(testDB)
	defer restoreDB()

	if _, err := GetAlistByID(1); !errors.Is(err, ErrCredentialUnreadable) {
		t.Fatalf("GetAlistByID(unreadable) error = %v, want ErrCredentialUnreadable", err)
	} else {
		var pub model.PublicError
		if !errors.As(err, &pub) || string(pub) != msg.T(msg.CredentialUnreadable) {
			t.Fatalf("error = %v, want the public credential message", err)
		}
	}
	if _, err := GetNotifyByID(1); !errors.Is(err, ErrCredentialUnreadable) {
		t.Fatalf("GetNotifyByID(unreadable) error = %v, want ErrCredentialUnreadable", err)
	}
	if row, err := GetAlistByID(2); err != nil || row["token"] != "good-token" {
		t.Fatalf("GetAlistByID(readable) = %#v, %v", row, err)
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
