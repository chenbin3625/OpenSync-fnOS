package mapper

import (
	"database/sql"
	"testing"
)

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

func newKeywordItemDB(t *testing.T) *sql.DB {
	t.Helper()
	db := newFileTestDB(t)
	mustExec(t, db, testJobTaskItemDDL)
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

// Keyword escaping: keywords containing LIKE metacharacters must match
// literally instead of acting as wildcards.
func TestKeywordSearchHandlesSpecialCharactersConsistently(t *testing.T) {
	db := newKeywordItemDB(t)
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
}

func TestEscapeLike(t *testing.T) {
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
}
