package service

import (
	"opensync/internal/mapper"
	"strings"
	"testing"
	"time"
)

func addJobScopedRetryTestItem(t *testing.T, jobID, taskID int64, name string, status int, errMsg string) int64 {
	t.Helper()
	if err := mapper.AddJobTaskItemMany([]map[string]interface{}{
		{
			"taskId":     taskID,
			"jobId":      jobID,
			"srcPath":    "/src",
			"dstPath":    "/dst",
			"isPath":     0,
			"fileName":   name,
			"fileSize":   int64(128),
			"type":       0,
			"status":     status,
			"errMsg":     errMsg,
			"createTime": time.Now().Unix(),
		},
	}); err != nil {
		t.Fatalf("AddJobTaskItemMany: %v", err)
	}
	var itemID int64
	if err := mapper.GetDB().QueryRow(`SELECT id FROM job_task_item WHERE taskId=? AND fileName=? ORDER BY id DESC LIMIT 1`, taskID, name).Scan(&itemID); err != nil {
		t.Fatalf("query inserted item id: %v", err)
	}
	return itemID
}

func TestGetJobItemListAndExportAcrossRuns(t *testing.T) {
	client, task1 := setupRetryFailedTaskTest(t, 1)
	task2, err := mapper.AddJobTask(client.JobID, time.Now().Unix())
	if err != nil {
		t.Fatalf("AddJobTask(task2): %v", err)
	}
	task3, err := mapper.AddJobTask(client.JobID, time.Now().Unix())
	if err != nil {
		t.Fatalf("AddJobTask(task3): %v", err)
	}

	addJobScopedRetryTestItem(t, client.JobID, task1, "first-ok.txt", taskStatusSuccess.Int(), "")
	addJobScopedRetryTestItem(t, client.JobID, task2, "second-fail.txt", taskStatusFailed.Int(), "connection reset")

	if _, err := mapper.GetDB().Exec(`UPDATE job_task SET status=?, errMsg=?, runTime=? WHERE id=?`,
		taskStatusSystemFailed.Int(), "scan failed: permission denied", time.Now().Unix(), task3); err != nil {
		t.Fatalf("mark task3 failed: %v", err)
	}

	res, err := GetJobItemList(map[string]interface{}{
		"jobId":    client.JobID,
		"pageNum":  1,
		"pageSize": 20,
	})
	if err != nil {
		t.Fatalf("GetJobItemList() error: %v", err)
	}
	if res["count"].(int64) != 2 {
		t.Fatalf("count = %v, want 2 across task1 and task2", res["count"])
	}
	groups, ok := res["errorGroups"].([]map[string]interface{})
	if !ok || len(groups) != 1 || groups[0]["reason"] != "connection reset" {
		t.Fatalf("errorGroups = %#v; want connection reset", res["errorGroups"])
	}
	recentErrors, ok := res["recentErrors"].([]map[string]interface{})
	if !ok || len(recentErrors) != 1 || recentErrors[0]["errMsg"] != "scan failed: permission denied" {
		t.Fatalf("recentErrors = %#v; want task3 error", res["recentErrors"])
	}

	filename, contentType, csvData, err := ExportTaskItems(map[string]interface{}{
		"jobId": client.JobID,
	}, "csv")
	if err != nil {
		t.Fatalf("ExportTaskItems(jobId) error: %v", err)
	}
	if !strings.Contains(filename, "-items.csv") || !strings.Contains(contentType, "text/csv") {
		t.Fatalf("ExportTaskItems metadata = %q, %q", filename, contentType)
	}
	csvText := string(csvData)
	if !strings.Contains(csvText, "first-ok.txt") || !strings.Contains(csvText, "second-fail.txt") {
		t.Fatalf("ExportTaskItems csv missing expected filenames: %s", csvText)
	}
}

func TestRetryJobItemsValidationGuards(t *testing.T) {
	client, taskID := setupRetryFailedTaskTest(t, 1)
	okID := addJobScopedRetryTestItem(t, client.JobID, taskID, "ok.txt", taskStatusSuccess.Int(), "")
	failID := addJobScopedRetryTestItem(t, client.JobID, taskID, "bad.txt", taskStatusFailed.Int(), "timeout")

	if err := RetryJobItems(client.JobID, nil); err == nil {
		t.Fatal("RetryJobItems(nil) succeeded, want error")
	}
	if err := RetryJobItems(client.JobID, []int64{okID}); err == nil {
		t.Fatal("RetryJobItems on succeeded item succeeded, want error")
	}

	if !client.tryMarkDoing() {
		t.Fatal("tryMarkDoing() = false")
	}
	if err := RetryJobItems(client.JobID, []int64{failID}); err == nil {
		client.markDone()
		t.Fatal("RetryJobItems while busy succeeded, want error")
	}
	client.markDone()

	if _, err := mapper.GetDB().Exec(`UPDATE job SET method=3 WHERE id=?`, client.JobID); err != nil {
		t.Fatalf("set bisync: %v", err)
	}
	if err := RetryJobItems(client.JobID, []int64{failID}); err == nil {
		t.Fatal("RetryJobItems on bisync job succeeded, want error")
	}

	clientDisabled, taskDisabled := setupRetryFailedTaskTest(t, 0)
	failDisabled := addJobScopedRetryTestItem(t, clientDisabled.JobID, taskDisabled, "disabled.txt", taskStatusFailed.Int(), "timeout")
	if err := RetryJobItems(clientDisabled.JobID, []int64{failDisabled}); err == nil {
		t.Fatal("RetryJobItems on disabled job succeeded, want error")
	}
}
