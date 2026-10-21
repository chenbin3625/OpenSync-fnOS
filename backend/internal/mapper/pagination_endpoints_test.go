package mapper

import (
	"errors"
	"testing"
)

// P3-12: the "unpaginated request that does not fit" contract was only tested on
// the generic helper; pin it on the two real endpoints.
func TestGetJobTaskListUnpaginatedOverLimitIsRefused(t *testing.T) {
	db := newFileTestDB(t)
	createJobSchema(t, db)
	restore := SetDBForTest(db)
	defer restore()

	mustExec(t, db, `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x<501)
		INSERT INTO job_task(id, jobId, status, runTime, createTime)
		SELECT x, 1, 2, 100, 100 FROM c`)

	_, err := GetJobTaskList(map[string]interface{}{"id": int64(1)})
	if !errors.Is(err, ErrListTooLarge) {
		t.Fatalf("GetJobTaskList() error = %v, want ErrListTooLarge", err)
	}
	result, err := GetJobTaskList(map[string]interface{}{"id": int64(1), "pageSize": 100, "pageNum": 1})
	if err != nil {
		t.Fatalf("paginated GetJobTaskList() error = %v", err)
	}
	if result["count"] != int64(501) {
		t.Fatalf("count = %v, want 501", result["count"])
	}
	if result["pageSize"] != 100 {
		t.Fatalf("effective pageSize = %v, want 100", result["pageSize"])
	}
}

func TestGetJobTaskItemListUnpaginatedOverLimitIsRefused(t *testing.T) {
	db := newFileTestDB(t)
	createJobSchema(t, db)
	restore := SetDBForTest(db)
	defer restore()

	mustExec(t, db, `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x<501)
		INSERT INTO job_task_item(taskId, srcPath, dstPath, fileName, type, status, createTime)
		SELECT 1, '/src', '/dst', 'file-' || x || '.bin', 0, 2, 1 FROM c`)

	_, err := GetJobTaskItemList(map[string]interface{}{"taskId": int64(1)})
	if !errors.Is(err, ErrListTooLarge) {
		t.Fatalf("GetJobTaskItemList() error = %v, want ErrListTooLarge", err)
	}
	result, err := GetJobTaskItemList(map[string]interface{}{"taskId": int64(1), "pageSize": 100, "pageNum": 2})
	if err != nil {
		t.Fatalf("paginated GetJobTaskItemList() error = %v", err)
	}
	if result["count"] != int64(501) {
		t.Fatalf("count = %v, want 501", result["count"])
	}
	rows := result["dataList"].([]map[string]interface{})
	if len(rows) != 100 {
		t.Fatalf("page rows = %d, want 100", len(rows))
	}
}

// A page and its total come from one read-only snapshot now; this is the basic
// consistency contract (the previous implementation queried them separately for
// UNION statements).
func TestPageAndCountAreConsistent(t *testing.T) {
	db := newFileTestDB(t)
	createJobSchema(t, db)
	restore := SetDBForTest(db)
	defer restore()

	mustExec(t, db, `WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x<7)
		INSERT INTO job_task(id, jobId, status, runTime, createTime)
		SELECT x, 1, 2, 100, 100 FROM c`)
	result, err := GetJobTaskList(map[string]interface{}{"id": int64(1), "pageSize": 3, "pageNum": 3})
	if err != nil {
		t.Fatalf("GetJobTaskList() error = %v", err)
	}
	rows := result["dataList"].([]map[string]interface{})
	if len(rows) != 1 {
		t.Fatalf("last page rows = %d, want 1", len(rows))
	}
	if result["count"] != int64(7) {
		t.Fatalf("count = %v, want 7", result["count"])
	}
}
