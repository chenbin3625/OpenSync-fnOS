package mapper

import (
	"database/sql"
	"fmt"
	"opensync/internal/msg"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func newPageLimitDB(t *testing.T, rows int) *sql.DB {
	t.Helper()
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	t.Cleanup(func() { testDB.Close() })
	if _, err := testDB.Exec("CREATE TABLE widget(id integer primary key autoincrement, name text)"); err != nil {
		t.Fatalf("create widget: %v", err)
	}
	for i := 0; i < rows; i++ {
		if _, err := testDB.Exec("INSERT INTO widget(name) VALUES (?)", fmt.Sprintf("w%d", i)); err != nil {
			t.Fatalf("insert widget %d: %v", i, err)
		}
	}
	return testDB
}

// An unpaginated request that does not fit used to return the first page of rows
// next to the real total and a "truncated" flag nobody read, so the caller saw a
// complete-looking response with rows missing.
func TestUnpaginatedQueryOverLimitFailsInsteadOfTruncating(t *testing.T) {
	restore := SetDBForTest(newPageLimitDB(t, 7))
	defer restore()

	_, err := fetchPage("SELECT * FROM widget ORDER BY id", 5, 0, false, nil)
	if err == nil {
		t.Fatalf("fetchPage() error = nil, want an over-limit error")
	}
	if err.Error() != msg.T(msg.ListTooLarge) {
		t.Fatalf("error = %q, want %q", err, msg.T(msg.ListTooLarge))
	}
}

// A UNION query cannot carry the window count, so the old flag was never set for
// it at all. The check must cover it too.
func TestUnpaginatedUnionQueryOverLimitFails(t *testing.T) {
	restore := SetDBForTest(newPageLimitDB(t, 7))
	defer restore()

	_, err := fetchPage(
		"SELECT id, name FROM widget WHERE id <= 4 UNION SELECT id, name FROM widget WHERE id > 4",
		5, 0, false, nil,
	)
	if err == nil || err.Error() != msg.T(msg.ListTooLarge) {
		t.Fatalf("fetchPage() error = %v, want %q", err, msg.T(msg.ListTooLarge))
	}
}

func TestUnpaginatedQueryWithinLimitStillSucceeds(t *testing.T) {
	restore := SetDBForTest(newPageLimitDB(t, 4))
	defer restore()

	result, err := fetchPage("SELECT * FROM widget ORDER BY id", 5, 0, false, nil)
	if err != nil {
		t.Fatalf("fetchPage() error = %v, want nil", err)
	}
	dataList, ok := result["dataList"].([]map[string]interface{})
	if !ok || len(dataList) != 4 {
		t.Fatalf("dataList = %#v, want 4 rows", result["dataList"])
	}
	if result["count"] != int64(4) {
		t.Fatalf("count = %v, want 4", result["count"])
	}
	if _, present := result["truncated"]; present {
		t.Fatalf("result still carries a truncated flag: %#v", result)
	}
}

// Paginated requests are unaffected: a page is expected to be a slice of a
// larger result.
func TestPaginatedQueryReturnsPartialResultWithoutError(t *testing.T) {
	restore := SetDBForTest(newPageLimitDB(t, 7))
	defer restore()

	result, err := FetchAllToPage(
		"SELECT * FROM widget ORDER BY id",
		map[string]interface{}{"pageSize": 2, "pageNum": 2},
	)
	if err != nil {
		t.Fatalf("FetchAllToPage() error = %v, want nil", err)
	}
	dataList, _ := result["dataList"].([]map[string]interface{})
	if len(dataList) != 2 {
		t.Fatalf("dataList len = %d, want 2", len(dataList))
	}
	if result["count"] != int64(7) {
		t.Fatalf("count = %v, want 7", result["count"])
	}
}

// The unpaginated cap is the same number callers may ask for explicitly, so a
// client told to paginate can actually request a page that large.
func TestMaxPageSizeMatchesUnpagedLimit(t *testing.T) {
	// Asserted one at a time: `x != a || x != b` reads as a copy-paste slip and
	// go vet rejects it as a suspect or, which would fail the build gate.
	if MaxPageSize != defaultUnpagedLimit {
		t.Fatalf("MaxPageSize=%d defaultUnpagedLimit=%d, want equal",
			MaxPageSize, defaultUnpagedLimit)
	}
	if MaxPageSize != maxPageSize {
		t.Fatalf("MaxPageSize=%d maxPageSize=%d, want equal", MaxPageSize, maxPageSize)
	}
	if !strings.Contains(msg.T(msg.ListTooLarge), "pageNum") || !strings.Contains(msg.T(msg.ListTooLarge), "pageSize") {
		t.Fatalf("ListTooLarge = %q, want it to name the pagination parameters", msg.T(msg.ListTooLarge))
	}
}
