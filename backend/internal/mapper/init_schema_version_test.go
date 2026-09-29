package mapper

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func openSchemaVersionDB(t *testing.T) *sql.DB {
	t.Helper()
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	testDB.SetMaxOpenConns(1)
	t.Cleanup(func() { testDB.Close() })
	return testDB
}

func TestSchemaVersionReadsSchemaVersionTable(t *testing.T) {
	testDB := openSchemaVersionDB(t)
	if _, err := testDB.Exec("CREATE TABLE schema_version(version INTEGER NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.Exec("INSERT INTO schema_version(version) VALUES (260922)"); err != nil {
		t.Fatal(err)
	}
	got, err := schemaVersion(testDB)
	if err != nil || got != 260922 {
		t.Fatalf("schemaVersion() = %d, %v; want 260922, nil", got, err)
	}
}

func TestSchemaVersionFallsBackToLegacyUserList(t *testing.T) {
	testDB := openSchemaVersionDB(t)
	if _, err := testDB.Exec("CREATE TABLE user_list(id integer primary key, sqlVersion integer)"); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.Exec("INSERT INTO user_list(sqlVersion) VALUES (250520)"); err != nil {
		t.Fatal(err)
	}
	got, err := schemaVersion(testDB)
	if err != nil || got != 250520 {
		t.Fatalf("schemaVersion() = %d, %v; want 250520, nil", got, err)
	}
}

// Only a genuinely missing table or row means version 0.
func TestSchemaVersionTreatsMissingTablesAndRowsAsZero(t *testing.T) {
	for name, stmts := range map[string][]string{
		"no tables at all":                {},
		"empty schema_version":            {"CREATE TABLE schema_version(version INTEGER NOT NULL)"},
		"legacy table without sqlVersion": {"CREATE TABLE user_list(id integer primary key)"},
		"legacy table with no rows":       {"CREATE TABLE user_list(id integer primary key, sqlVersion integer)"},
	} {
		t.Run(name, func(t *testing.T) {
			testDB := openSchemaVersionDB(t)
			for _, stmt := range stmts {
				if _, err := testDB.Exec(stmt); err != nil {
					t.Fatal(err)
				}
			}
			got, err := schemaVersion(testDB)
			if err != nil || got != 0 {
				t.Fatalf("schemaVersion() = %d, %v; want 0, nil", got, err)
			}
		})
	}
}

// Any other failure must surface: guessing 0 would replay non-idempotent
// migrations on a current schema.
func TestSchemaVersionReturnsOtherErrors(t *testing.T) {
	testDB := openSchemaVersionDB(t)
	if _, err := testDB.Exec("CREATE TABLE schema_version(version INTEGER NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if err := testDB.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := schemaVersion(testDB); err == nil {
		t.Fatalf("schemaVersion() on a closed handle = %d, nil; want an error", got)
	}
}

func TestEnsureIndexesCreatesTaskIDCursorIndex(t *testing.T) {
	testDB := openSchemaVersionDB(t)
	for _, stmt := range []string{
		"CREATE TABLE job_task(id integer primary key, jobId integer, status integer, runTime integer, createTime integer)",
		"CREATE TABLE job_task_item(id integer primary key, taskId integer, status integer, type integer, isPath integer, createTime integer)",
	} {
		if _, err := testDB.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	ensureIndexes(testDB)
	// Idempotent: a second startup must not fail on the existing index.
	ensureIndexes(testDB)
	if !indexExists(testDB, "idx_job_task_item_task_id") {
		t.Fatal("expected idx_job_task_item_task_id to exist")
	}

	var detail string
	rows, err := testDB.Query("EXPLAIN QUERY PLAN SELECT id FROM job_task_item WHERE taskId=? AND status IN (4,7) AND id > ? ORDER BY id LIMIT 10", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var a, b, c int
		if err := rows.Scan(&a, &b, &c, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	for _, step := range plan {
		if step == "SCAN job_task_item" {
			t.Fatalf("retry cursor plan scans the whole table: %v", plan)
		}
	}
}
