package mapper

import (
	"database/sql"
	"fmt"
	"opensync/internal/msg"
	"reflect"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func newTaskItemPageDB(t *testing.T) *sql.DB {
	t.Helper()
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	t.Cleanup(func() { testDB.Close() })
	testDB.SetMaxOpenConns(1)
	if _, err := testDB.Exec(`CREATE TABLE job_task_item(
		id integer primary key autoincrement,
		taskId integer,
		srcPath text,
		dstPath text,
		isPath integer,
		fileName text,
		fileSize integer,
		type integer,
		alistTaskId text,
		status integer,
		progress real,
		errMsg text,
		createTime integer
	)`); err != nil {
		t.Fatalf("create job_task_item: %v", err)
	}
	// Seven items of task 10 sharing createTime pairs (ties are the norm, so
	// the id tiebreaker must order them), plus two items of another task.
	for i := 1; i <= 7; i++ {
		status := 2
		if i%3 == 0 {
			status = 7
		}
		if _, err := testDB.Exec(
			`INSERT INTO job_task_item(id, taskId, fileName, status, type, isPath, createTime) VALUES (?, 10, ?, ?, 0, 0, ?)`,
			i, fmt.Sprintf("f%d", i), status, (i+1)/2,
		); err != nil {
			t.Fatalf("insert item %d: %v", i, err)
		}
	}
	for i := 8; i <= 9; i++ {
		if _, err := testDB.Exec(
			`INSERT INTO job_task_item(id, taskId, fileName, status, type, isPath, createTime) VALUES (?, 20, ?, 2, 0, 0, 1)`,
			i, fmt.Sprintf("other%d", i),
		); err != nil {
			t.Fatalf("insert other item %d: %v", i, err)
		}
	}
	return testDB
}

func pageIDs(t *testing.T, result map[string]interface{}) []int64 {
	t.Helper()
	rows, ok := result["dataList"].([]map[string]interface{})
	if !ok {
		t.Fatalf("dataList = %#v, want rows", result["dataList"])
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		if _, leaked := row[pageTotalColumn]; leaked {
			t.Fatalf("row carries the internal window column: %#v", row)
		}
		ids = append(ids, row["id"].(int64))
	}
	return ids
}

// The item list takes its total from a separate COUNT(*) instead of a window
// column; pages, totals and ordering must stay exactly as before.
func TestGetJobTaskItemListPagesWithSeparateCount(t *testing.T) {
	restore := SetDBForTest(newTaskItemPageDB(t))
	defer restore()

	for _, tc := range []struct {
		pageNum int
		want    []int64
	}{
		{1, []int64{7, 6, 5}},
		{2, []int64{4, 3, 2}},
		{3, []int64{1}},
		{4, []int64{}},
	} {
		result, err := GetJobTaskItemList(map[string]interface{}{"taskId": int64(10), "pageSize": 3, "pageNum": tc.pageNum})
		if err != nil {
			t.Fatalf("page %d: GetJobTaskItemList() error: %v", tc.pageNum, err)
		}
		if got := pageIDs(t, result); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("page %d ids = %v, want %v", tc.pageNum, got, tc.want)
		}
		if result["count"] != int64(7) {
			t.Fatalf("page %d count = %#v, want int64(7)", tc.pageNum, result["count"])
		}
	}
}

func TestGetJobTaskItemListCountsWithFilter(t *testing.T) {
	restore := SetDBForTest(newTaskItemPageDB(t))
	defer restore()

	result, err := GetJobTaskItemList(map[string]interface{}{"taskId": int64(10), "status": 7, "pageSize": 1, "pageNum": 1})
	if err != nil {
		t.Fatalf("GetJobTaskItemList() error: %v", err)
	}
	if got := pageIDs(t, result); !reflect.DeepEqual(got, []int64{6}) {
		t.Fatalf("ids = %v, want [6]", got)
	}
	if result["count"] != int64(2) {
		t.Fatalf("count = %#v, want int64(2)", result["count"])
	}
}

func TestGetJobTaskItemListEmptyTaskReturnsEmptyList(t *testing.T) {
	restore := SetDBForTest(newTaskItemPageDB(t))
	defer restore()

	result, err := GetJobTaskItemList(map[string]interface{}{"taskId": int64(99), "pageSize": 10, "pageNum": 1})
	if err != nil {
		t.Fatalf("GetJobTaskItemList() error: %v", err)
	}
	if got := pageIDs(t, result); len(got) != 0 {
		t.Fatalf("ids = %v, want none", got)
	}
	if result["count"] != int64(0) {
		t.Fatalf("count = %#v, want int64(0)", result["count"])
	}
}

func TestGetJobTaskItemListUnpaginated(t *testing.T) {
	restore := SetDBForTest(newTaskItemPageDB(t))
	defer restore()

	result, err := GetJobTaskItemList(map[string]interface{}{"taskId": int64(20)})
	if err != nil {
		t.Fatalf("GetJobTaskItemList() error: %v", err)
	}
	if got := pageIDs(t, result); !reflect.DeepEqual(got, []int64{9, 8}) {
		t.Fatalf("ids = %v, want [9 8]", got)
	}
	if result["count"] != int64(2) {
		t.Fatalf("count = %#v, want int64(2)", result["count"])
	}
}

// The unpaginated cap keeps its contract on the separate-count path: refuse
// rather than return a short list next to the full total.
func TestFetchPageSeparateCountRefusesOverLimitUnpaginated(t *testing.T) {
	restore := SetDBForTest(newTaskItemPageDB(t))
	defer restore()

	_, err := fetchPageSeparateCount(
		"SELECT id FROM job_task_item WHERE taskId=? ORDER BY createTime DESC, id DESC",
		"SELECT COUNT(*) FROM job_task_item WHERE taskId=?",
		5, 0, false, []interface{}{int64(10)},
	)
	if err == nil || err.Error() != msg.T(msg.ListTooLarge) {
		t.Fatalf("fetchPageSeparateCount() error = %v, want %q", err, msg.T(msg.ListTooLarge))
	}
}

func TestGetJobTaskItemListQueryHasNoWindowCount(t *testing.T) {
	// Guard against a regression to FetchAllToPage for this table: the window
	// count is what made each page sort the whole task.
	restore := SetDBForTest(newTaskItemPageDB(t))
	defer restore()
	var seen []string
	oldTrace := pageQueryTrace
	pageQueryTrace = func(query string) { seen = append(seen, query) }
	defer func() { pageQueryTrace = oldTrace }()

	if _, err := GetJobTaskItemList(map[string]interface{}{"taskId": int64(10), "pageSize": 3, "pageNum": 1}); err != nil {
		t.Fatalf("GetJobTaskItemList() error: %v", err)
	}
	if len(seen) == 0 {
		t.Fatal("no page query traced")
	}
	for _, query := range seen {
		if strings.Contains(strings.ToUpper(query), " OVER(") {
			t.Fatalf("item page query uses a window function: %s", query)
		}
	}
}

func TestForEachJobTaskItemsByStatusesUsesIDCursorAndKeepsNullCreateTime(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()
	if _, err := testDB.Exec(`CREATE TABLE job_task_item(
		id integer primary key autoincrement,
		taskId integer,
		srcPath text,
		dstPath text,
		isPath integer,
		fileName text,
		fileSize integer,
		type integer,
		alistTaskId text,
		status integer,
		errMsg text,
		createTime integer
	)`); err != nil {
		t.Fatalf("create job_task_item: %v", err)
	}
	for _, row := range []struct {
		id         int
		taskID     int
		status     int
		createTime interface{}
		name       string
	}{
		{1, 10, 7, int64(10), "a"},
		{2, 10, 7, nil, "null-time"}, // skipped by the old (createTime, id) cursor
		{3, 20, 7, int64(10), "other-task"},
		{4, 10, 2, int64(10), "success"},
		{5, 10, 4, int64(11), "b"},
		{6, 10, 7, int64(11), "c"},
	} {
		if _, err := testDB.Exec(
			"INSERT INTO job_task_item(id, taskId, fileName, status, createTime) VALUES (?, ?, ?, ?, ?)",
			row.id, row.taskID, row.name, row.status, row.createTime,
		); err != nil {
			t.Fatalf("insert %d: %v", row.id, err)
		}
	}
	restore := SetDBForTest(testDB)
	defer restore()

	var got []string
	var batches []int
	if err := ForEachJobTaskItemsByStatuses(10, []int{4, 7}, 2, func(items []map[string]interface{}) error {
		batches = append(batches, len(items))
		for _, item := range items {
			got = append(got, item["fileName"].(string))
		}
		return nil
	}); err != nil {
		t.Fatalf("ForEachJobTaskItemsByStatuses() error: %v", err)
	}
	if want := []string{"a", "null-time", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("items = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(batches, []int{2, 2}) {
		t.Fatalf("batches = %v, want [2 2]", batches)
	}
}
