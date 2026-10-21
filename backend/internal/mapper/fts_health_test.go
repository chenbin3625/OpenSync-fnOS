package mapper

import (
	"database/sql"
	"strings"
	"testing"
)

// resetFTSAvailabilityCache clears the per-handle "is FTS usable" memo so a
// test can switch the schema underneath the cached result.
func resetFTSAvailabilityCache() {
	ftsAvailability.mu.Lock()
	ftsAvailability.known = false
	ftsAvailability.db = nil
	ftsAvailability.ok = false
	ftsAvailability.mu.Unlock()
}

const testJobTaskItemDDL = `CREATE TABLE job_task_item(
	id integer primary key autoincrement,
	taskId integer,
	srcPath text,
	dstPath text,
	isPath integer DEFAULT 0,
	fileName text,
	fileSize integer,
	type integer,
	alistTaskId text,
	status integer DEFAULT 0,
	progress real,
	errMsg text,
	createTime integer DEFAULT (strftime('%s', 'now'))
)`

func newFTSItemDB(t *testing.T) *sql.DB {
	t.Helper()
	db := newFileTestDB(t)
	mustExec(t, db, testJobTaskItemDDL)
	if err := installJobTaskItemFTS(db, true); err != nil {
		t.Fatalf("installJobTaskItemFTS: %v", err)
	}
	t.Cleanup(resetFTSAvailabilityCache)
	return db
}

func insertItem(t *testing.T, db *sql.DB, id int64, fileName string) {
	t.Helper()
	mustExec(t, db,
		`INSERT INTO job_task_item(id, taskId, srcPath, dstPath, isPath, fileName, type, status, createTime)
		 VALUES(?, 1, '/src', '/dst', 0, ?, 0, 2, 1)`,
		id, fileName,
	)
}

// P2-7: a virtual table without its sync triggers does not index new rows. The
// keyword search must detect that and fall back to LIKE instead of reporting
// "nothing found".
func TestKeywordSearchFallsBackToLikeWhenFTSTriggersAreMissing(t *testing.T) {
	db := newFTSItemDB(t)
	insertItem(t, db, 1, "unique-needle.txt")
	for _, trigger := range jobTaskItemFTSTriggers {
		mustExec(t, db, "DROP TRIGGER "+trigger)
	}
	resetFTSAvailabilityCache()
	restore := SetDBForTest(db)
	defer restore()

	healthy, err := ftsObjectsHealthy(db)
	if err != nil || healthy {
		t.Fatalf("ftsObjectsHealthy() = %v, %v; want false, nil after dropping the triggers", healthy, err)
	}
	if taskItemFTSAvailable() {
		t.Fatal("taskItemFTSAvailable() = true although the sync triggers are gone")
	}
	filter, _ := taskItemKeywordFilter("needle")
	if strings.Contains(filter, "MATCH") {
		t.Fatalf("keyword filter still uses FTS without triggers: %q", filter)
	}
	result, err := GetJobTaskItemList(map[string]interface{}{"taskId": int64(1), "keyword": "needle"})
	if err != nil {
		t.Fatalf("GetJobTaskItemList() error = %v", err)
	}
	rows := result["dataList"].([]map[string]interface{})
	if len(rows) != 1 {
		t.Fatalf("rows = %#v, want the LIKE fallback to find the item", rows)
	}
}

// P2-7: startup repairs a table whose triggers were lost, and the repair also
// rebuilds rows that were inserted while the index was not maintained.
func TestEnsureJobTaskItemFTSHealthRebuildsMissingTriggers(t *testing.T) {
	db := newFTSItemDB(t)
	for _, trigger := range jobTaskItemFTSTriggers {
		mustExec(t, db, "DROP TRIGGER "+trigger)
	}
	// Inserted after the triggers were dropped, so it is missing from the index.
	insertItem(t, db, 1, "missed-by-index.txt")

	var count int64
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM job_task_item_fts WHERE job_task_item_fts MATCH ?",
		fts5Phrase("missed-by-index"),
	).Scan(&count); err != nil {
		t.Fatalf("match before repair: %v", err)
	}
	if count != 0 {
		t.Fatalf("index already contains the row (%d matches); the test fixture does not model a stale index", count)
	}

	if err := ensureJobTaskItemFTSHealth(db); err != nil {
		t.Fatalf("ensureJobTaskItemFTSHealth() error = %v", err)
	}
	healthy, err := ftsObjectsHealthy(db)
	if err != nil || !healthy {
		t.Fatalf("ftsObjectsHealthy() after repair = %v, %v; want true", healthy, err)
	}
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM job_task_item_fts WHERE job_task_item_fts MATCH ?",
		fts5Phrase("missed-by-index"),
	).Scan(&count); err != nil {
		t.Fatalf("match after repair: %v", err)
	}
	if count != 1 {
		t.Fatalf("matches after repair = %d, want 1 (the rebuild must index rows the triggers missed)", count)
	}

	// A second call on a healthy database is a no-op that keeps working.
	if err := ensureJobTaskItemFTSHealth(db); err != nil {
		t.Fatalf("second ensureJobTaskItemFTSHealth() error = %v", err)
	}
}

// P3-12: keyword escaping. FTS and LIKE must find the same rows for keywords
// containing quoting/LIKE metacharacters.
func TestKeywordSearchHandlesSpecialCharactersConsistently(t *testing.T) {
	db := newFTSItemDB(t)
	names := []string{"50%off.txt", "a_b.txt", "axb.txt", `quote"file.txt`, "plain.txt"}
	for i, name := range names {
		insertItem(t, db, int64(i+1), name)
	}
	restore := SetDBForTest(db)
	defer restore()

	cases := map[string]string{
		"50%off":    "50%off.txt",
		"a_b":       "a_b.txt",
		`quote"fil`: `quote"file.txt`,
	}
	for keyword, wantName := range cases {
		result, err := GetJobTaskItemList(map[string]interface{}{"taskId": int64(1), "keyword": keyword})
		if err != nil {
			t.Fatalf("keyword %q: GetJobTaskItemList() error = %v", keyword, err)
		}
		rows := result["dataList"].([]map[string]interface{})
		if len(rows) != 1 || rows[0]["fileName"] != wantName {
			t.Fatalf("keyword %q: rows = %#v, want only %q", keyword, rows, wantName)
		}
	}

	// Same keywords through the LIKE fallback (no FTS table at all).
	mustExec(t, db, "DROP TABLE job_task_item_fts")
	resetFTSAvailabilityCache()
	for keyword, wantName := range cases {
		result, err := GetJobTaskItemList(map[string]interface{}{"taskId": int64(1), "keyword": keyword})
		if err != nil {
			t.Fatalf("LIKE keyword %q: GetJobTaskItemList() error = %v", keyword, err)
		}
		rows := result["dataList"].([]map[string]interface{})
		if len(rows) != 1 || rows[0]["fileName"] != wantName {
			t.Fatalf("LIKE keyword %q: rows = %#v, want only %q", keyword, rows, wantName)
		}
	}
}

func TestEscapeLikeAndFTSPhrase(t *testing.T) {
	for input, want := range map[string]string{
		"plain":  "plain",
		"50%":    `50\%`,
		"a_b":    `a\_b`,
		`back\s`: `back\\s`,
	} {
		if got := escapeLike(input); got != want {
			t.Fatalf("escapeLike(%q) = %q, want %q", input, got, want)
		}
	}
	for input, want := range map[string]string{
		"plain":  `"plain"`,
		`a"b`:    `"a""b"`,
		"a*b(c)": `"a*b(c)"`,
	} {
		got := fts5Phrase(input)
		if got != want {
			t.Fatalf("fts5Phrase(%q) = %q, want %q", input, got, want)
		}
		if strings.Count(got, `"`)%2 != 0 {
			t.Fatalf("fts5Phrase(%q) produced an unbalanced phrase %q", input, got)
		}
	}
	// A keyword made only of quotes: every quote is doubled, so the phrase stays
	// balanced no matter how many the user types.
	quotes := strings.Repeat(`"`, 5)
	if got, want := fts5Phrase(quotes), strings.Repeat(`"`, 12); got != want {
		t.Fatalf("fts5Phrase(5 quotes) = %q, want %q", got, want)
	}
}
