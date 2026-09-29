package mapper

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// A database without job_task_item stands in for any read failure (locked,
// I/O error): the query itself fails.
func newCountsFailingDB(t *testing.T) *sql.DB {
	t.Helper()
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	t.Cleanup(func() { testDB.Close() })
	return testDB
}

func TestGetJobTaskCountsByTaskIDsReturnsErrorInsteadOfZeros(t *testing.T) {
	restore := SetDBForTest(newCountsFailingDB(t))
	defer restore()

	counts, err := GetJobTaskCountsByTaskIDs([]int64{10, 20})
	if err == nil {
		t.Fatalf("GetJobTaskCountsByTaskIDs() error = nil, counts = %#v; want the query error", counts)
	}
	if counts != nil {
		t.Fatalf("counts = %#v, want nil so no caller can cache zeros", counts)
	}
}

func TestQueryJobTaskCountsReturnsErrorInsteadOfZeros(t *testing.T) {
	restore := SetDBForTest(newCountsFailingDB(t))
	defer restore()

	counts, err := QueryJobTaskCounts(10)
	if err == nil {
		t.Fatalf("QueryJobTaskCounts() error = nil, counts = %#v; want the query error", counts)
	}
	if counts != nil {
		t.Fatalf("counts = %#v, want nil", counts)
	}
}

func TestGetJobTaskCountsByTaskIDsSkipsQueryForNoValidIDs(t *testing.T) {
	restore := SetDBForTest(newCountsFailingDB(t))
	defer restore()

	counts, err := GetJobTaskCountsByTaskIDs([]int64{0, -1})
	if err != nil {
		t.Fatalf("GetJobTaskCountsByTaskIDs() error: %v", err)
	}
	if len(counts) != 0 {
		t.Fatalf("counts = %#v, want empty", counts)
	}
}
