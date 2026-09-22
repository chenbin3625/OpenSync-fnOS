package service

import (
	"database/sql"
	"opensync/internal/mapper"
	"testing"

	_ "modernc.org/sqlite"
)

func TestGetTaskListFillsMissingTaskNumsInBatch(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()

	if _, err := testDB.Exec(`CREATE TABLE job_task(
		id integer primary key autoincrement,
		jobId integer,
		status integer,
		runTime integer,
		taskNum text,
		createTime integer
	)`); err != nil {
		t.Fatalf("create job_task: %v", err)
	}
	if _, err := testDB.Exec(`CREATE TABLE job_task_item(
		id integer primary key autoincrement,
		taskId integer,
		fileSize integer,
		type integer,
		status integer
	)`); err != nil {
		t.Fatalf("create job_task_item: %v", err)
	}

	for _, stmt := range []string{
		"INSERT INTO job_task(id, jobId, status, runTime, createTime) VALUES (10, 1, 1, 100, 100)",
		"INSERT INTO job_task(id, jobId, status, runTime, createTime) VALUES (20, 1, 1, 200, 200)",
		"INSERT INTO job_task_item(taskId, status, type, fileSize) VALUES (10, 2, 0, 100)",
		"INSERT INTO job_task_item(taskId, status, type, fileSize) VALUES (10, 7, 0, 200)",
		"INSERT INTO job_task_item(taskId, status, type, fileSize) VALUES (20, 0, 0, 300)",
		"INSERT INTO job_task_item(taskId, status, type, fileSize) VALUES (20, 1, 0, 400)",
	} {
		if _, err := testDB.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}

	restore := mapper.SetDBForTest(testDB)
	defer restore()

	result, err := GetTaskList(map[string]interface{}{"id": int64(1)})
	if err != nil {
		t.Fatalf("GetTaskList() error: %v", err)
	}
	items := result["dataList"].([]map[string]interface{})
	if len(items) != 2 {
		t.Fatalf("task len = %d, want 2: %#v", len(items), items)
	}

	byID := map[int64]map[string]interface{}{}
	for _, item := range items {
		byID[item["id"].(int64)] = item
	}
	if byID[10]["successNum"] != int64(1) || byID[10]["failNum"] != int64(1) || byID[10]["sumSize"] != int64(100) {
		t.Fatalf("task 10 counts = %#v, want success/fail/sumSize 1/1/100", byID[10])
	}
	if byID[20]["waitNum"] != int64(1) || byID[20]["runningNum"] != int64(1) || byID[20]["allNum"] != int64(2) {
		t.Fatalf("task 20 counts = %#v, want wait/running/all 1/1/2", byID[20])
	}
}

func TestGetJobCurrentReturnsStalePageWhenExpectedTaskIDDiffers(t *testing.T) {
	previousClients := jobClientList
	jobClientListMu.Lock()
	jobClientList = map[int64]*JobClient{}
	jobClientListMu.Unlock()
	defer func() {
		jobClientListMu.Lock()
		jobClientList = previousClients
		jobClientListMu.Unlock()
	}()

	task := &JobTask{
		TaskID:         123,
		CreateTime:     456,
		Waiting:        newCopyQueue(),
		Doing:          make(map[int64]*CopyItem),
		FinishedCounts: make(map[taskStatus]int),
		FinishedSizes:  make(map[taskStatus]int64),
	}
	task.Doing[1] = &CopyItem{
		SrcPath:    "/src/",
		DstPath:    "/dst/",
		FileName:   "running.txt",
		FileSize:   int64(10),
		CopyType:   taskItemTypeCopy,
		Status:     taskStatusRunning,
		Progress:   50,
		CreateTime: 100,
	}

	jobClientListMu.Lock()
	jobClientList[1] = &JobClient{JobID: 1, CurrentJobTask: task}
	jobClientListMu.Unlock()

	result, err := GetJobCurrent(1, map[string]interface{}{
		"status":             "1",
		"pageSize":           "10",
		"pageNum":            "1",
		"expectedTaskId":     "999",
		"expectedCreateTime": "456",
	})
	if err != nil {
		t.Fatalf("GetJobCurrent() error: %v", err)
	}

	page, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("result = %#v, want page map", result)
	}
	if page["stale"] != true {
		t.Fatalf("stale = %v, want true", page["stale"])
	}
	if page["taskId"] != int64(123) || page["createTime"] != int64(456) {
		t.Fatalf("identity = %v/%v, want 123/456", page["taskId"], page["createTime"])
	}
	if page["count"] != int64(0) {
		t.Fatalf("count = %v, want 0", page["count"])
	}
	items, ok := page["dataList"].([]map[string]interface{})
	if !ok {
		t.Fatalf("dataList = %#v, want task item maps", page["dataList"])
	}
	if len(items) != 0 {
		t.Fatalf("dataList len = %d, want 0", len(items))
	}
}
