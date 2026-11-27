package mapper

import (
	"testing"

	_ "modernc.org/sqlite"
)

func TestMigrateTo261128BackfillsJobTaskItemJobIDAndIndexes(t *testing.T) {
	testDB := openSchemaVersionDB(t)
	stmts := []string{
		`CREATE TABLE schema_version(version INTEGER NOT NULL)`,
		`INSERT INTO schema_version(version) VALUES (261127)`,
		`CREATE TABLE job(id integer primary key autoincrement, isCron integer default 3)`,
		`CREATE TABLE job_task(id integer primary key autoincrement, jobId integer, status integer, errMsg text, runTime integer, createTime integer)`,
		`CREATE TABLE job_task_item(
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
		)`,
		`INSERT INTO job(id, isCron) VALUES (1, 3), (2, 3)`,
		`INSERT INTO job_task(id, jobId, status, createTime) VALUES (10, 1, 2, 100), (11, 1, 7, 200), (20, 2, 2, 300)`,
		`INSERT INTO job_task_item(id, taskId, fileName, status, createTime) VALUES
			(1, 10, 'a.txt', 2, 101),
			(2, 11, 'b.txt', 7, 201),
			(3, 20, 'c.txt', 2, 301)`,
	}
	for _, stmt := range stmts {
		if _, err := testDB.Exec(stmt); err != nil {
			t.Fatalf("seed stmt failed (%s): %v", stmt, err)
		}
	}

	if err := migrateDBTx(testDB, 261127); err != nil {
		t.Fatalf("migrateDBTx(261127) error: %v", err)
	}
	ensureIndexes(testDB)

	ver, err := schemaVersion(testDB)
	if err != nil || ver != currentVersion {
		t.Fatalf("schemaVersion() = %d, %v; want %d, nil", ver, err, currentVersion)
	}
	hasCol, err := tableHasColumnDBErr(testDB, "job_task_item", "jobId")
	if err != nil || !hasCol {
		t.Fatalf("tableHasColumnDBErr(job_task_item, jobId) = %v, %v; want true, nil", hasCol, err)
	}

	wantJobIDs := map[int64]int64{1: 1, 2: 1, 3: 2}
	for itemID, wantJobID := range wantJobIDs {
		var gotJobID int64
		if err := testDB.QueryRow(`SELECT jobId FROM job_task_item WHERE id=?`, itemID).Scan(&gotJobID); err != nil {
			t.Fatalf("query item %d: %v", itemID, err)
		}
		if gotJobID != wantJobID {
			t.Fatalf("item %d jobId = %d, want %d", itemID, gotJobID, wantJobID)
		}
	}

	for _, idx := range []string{
		"idx_job_task_item_job_time",
		"idx_job_task_item_job_status_time",
		"idx_job_task_item_job_type_time",
		"idx_job_task_item_job_is_path_time",
	} {
		if !indexExists(testDB, idx) {
			t.Fatalf("expected index %s to exist after migration", idx)
		}
	}
}

func TestJobScopedQueriesGroupsRecentErrorsAndRetrySelection(t *testing.T) {
	testDB := openSchemaVersionDB(t)
	restore := SetDBForTest(testDB)
	defer restore()

	stmts := []string{
		`CREATE TABLE job_task(id integer primary key autoincrement, jobId integer, status integer, errMsg text, runTime integer, createTime integer)`,
		`CREATE TABLE job_task_item(
			id integer primary key autoincrement,
			taskId integer,
			jobId integer default 0,
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
		)`,
		// Task 10: completed with 1 success item
		// Task 11: completed with failed items (must NOT appear in recentErrors)
		// Task 12: scan/auth error with 0 failed items (MUST appear in recentErrors)
		// Task 13: no-op run status=8 (must NOT appear in recentErrors)
		// Task 20: different job (must be excluded)
		`INSERT INTO job_task(id, jobId, status, errMsg, runTime, createTime) VALUES
			(10, 1, 2, '', 105, 100),
			(11, 1, 7, 'partial fail summary', 205, 200),
			(12, 1, 6, 'remote auth expired', 305, 300),
			(13, 1, 8, 'noop notice', 355, 350),
			(20, 2, 7, 'other job error', 405, 400)`,
		`INSERT INTO job_task_item(id, taskId, jobId, srcPath, dstPath, isPath, fileName, fileSize, type, alistTaskId, status, progress, errMsg, createTime) VALUES
			(1, 10, 1, '/src/a.txt', '/dst/a.txt', 0, 'a.txt', 10, 0, '', 2, 100, '', 101),
			(2, 11, 1, '/src/b.txt', '/dst/b.txt', 0, 'b.txt', 20, 0, '', 7, 10, 'disk full', 201),
			(3, 11, 1, '/src/c.txt', '/dst/c.txt', 0, 'c.txt', 30, 1, '', 5, 20, 'disk full', 202),
			(4, 11, 1, '/src/d.txt', '/dst/d.txt', 0, 'd.txt', 40, 0, '', 4, 0, 'timeout', 203),
			(5, 20, 2, '/other/x.txt', '/dst/x.txt', 0, 'x.txt', 50, 0, '', 7, 0, 'other error', 401)`,
	}
	for _, stmt := range stmts {
		if _, err := testDB.Exec(stmt); err != nil {
			t.Fatalf("seed stmt failed: %v", err)
		}
	}

	page, err := GetJobTaskItemList(map[string]interface{}{
		"jobId":    1,
		"pageNum":  1,
		"pageSize": 10,
	})
	if err != nil {
		t.Fatalf("GetJobTaskItemList(jobId=1) error: %v", err)
	}
	if page["count"].(int64) != 4 {
		t.Fatalf("job 1 item count = %v, want 4", page["count"])
	}
	rows := page["dataList"].([]map[string]interface{})
	if rows[0]["fileName"] != "d.txt" || rows[3]["fileName"] != "a.txt" {
		t.Fatalf("unexpected ordering in job 1 items: first=%v last=%v", rows[0]["fileName"], rows[3]["fileName"])
	}

	otherPage, err := GetJobTaskItemList(map[string]interface{}{
		"jobId":  1,
		"status": -1,
	})
	if err != nil {
		t.Fatalf("GetJobTaskItemList(status=-1) error: %v", err)
	}
	if otherPage["count"].(int64) != 2 {
		t.Fatalf("status=-1 count = %v, want 2 (status 5 and 4)", otherPage["count"])
	}

	groups, err := GetJobErrorGroups(1, map[string]interface{}{}, 10)
	if err != nil {
		t.Fatalf("GetJobErrorGroups() error: %v", err)
	}
	if len(groups) != 2 || groups[0]["reason"] != "disk full" || groups[0]["count"].(int64) != 2 {
		t.Fatalf("GetJobErrorGroups() = %#v", groups)
	}

	recentErrors, err := GetJobRecentTaskErrors(1, map[string]interface{}{}, 10)
	if err != nil {
		t.Fatalf("GetJobRecentTaskErrors() error: %v", err)
	}
	if len(recentErrors) != 1 || recentErrors[0]["errMsg"] != "remote auth expired" {
		t.Fatalf("GetJobRecentTaskErrors() = %#v; want single task #12 error", recentErrors)
	}

	count, err := CountJobTaskItemsByJobAndIDs(1, []int64{1, 2, 4, 5}, []int{4, 5, 6, 7})
	if err != nil || count != 2 {
		t.Fatalf("CountJobTaskItemsByJobAndIDs() = %d, %v; want 2 (items 2 and 4)", count, err)
	}

	var replayed []string
	if err := ForEachJobTaskItemsByJobAndIDs(1, []int64{1, 2, 3, 5}, []int{4, 5, 6, 7}, 1, func(batch []map[string]interface{}) error {
		for _, item := range batch {
			replayed = append(replayed, item["fileName"].(string))
		}
		return nil
	}); err != nil {
		t.Fatalf("ForEachJobTaskItemsByJobAndIDs() error: %v", err)
	}
	if len(replayed) != 2 || replayed[0] != "b.txt" || replayed[1] != "c.txt" {
		t.Fatalf("ForEachJobTaskItemsByJobAndIDs replayed = %#v; want [b.txt c.txt]", replayed)
	}
}
