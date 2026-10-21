package service

import (
	"opensync/internal/config"
	"opensync/internal/mapper"
	"path/filepath"
	"testing"
	"time"
)

func setupRetryFailedTaskTest(t *testing.T, enable int) (*JobClient, int64) {
	t.Helper()
	oldConfig := config.GetConfig()
	config.SetConfigForTest(&config.Config{
		DB: config.DBConfig{DBName: filepath.Join(t.TempDir(), "opensync-test.db")},
		Server: config.ServerConfig{
			Timeout:         0,
			CopyConcurrency: 1,
			ScanConcurrency: 1,
			PasswdStr:       "test-secret",
		},
	})
	t.Cleanup(func() {
		config.SetConfigForTest(oldConfig)
		resetJobClientsForTest()
		mapper.CloseDB()
	})

	mapper.CloseDB()
	mapper.InitSQL()
	resetJobClientsForTest()

	// AddJobClient validates that the engine exists before inserting the job.
	ad := *alistDeps
	ad.GetAlistByID = func(alistID int64) (map[string]interface{}, error) {
		return map[string]interface{}{"id": alistID, "url": "https://alist.test", "token": "t"}, nil
	}
	restoreAlistDeps := SetAlistDepsForTest(&ad)
	t.Cleanup(restoreAlistDeps)

	if err := AddJobClient(map[string]interface{}{
		"enable":        enable,
		"remark":        "retry-test",
		"srcPath":       []string{"/src"},
		"dstPath":       []string{"/dst"},
		"alistId":       1,
		"useCacheT":     0,
		"scanIntervalT": 0,
		"useCacheS":     0,
		"scanIntervalS": 0,
		"method":        0,
		"interval":      60,
		"isCron":        0,
		"minFileSize":   0,
		"maxFileSize":   0,
	}, false); err != nil {
		t.Fatalf("AddJobClient: %v", err)
	}
	client := onlyJobClientForTest(t)

	taskID, err := mapper.AddJobTask(client.JobID, time.Now().Unix())
	if err != nil {
		t.Fatalf("AddJobTask: %v", err)
	}
	return client, taskID
}

func addRetryTestItem(t *testing.T, taskID int64, name string, status int) {
	t.Helper()
	if err := mapper.AddJobTaskItemMany([]map[string]interface{}{
		{
			"taskId":     taskID,
			"srcPath":    "/src",
			"dstPath":    "/dst",
			"isPath":     0,
			"fileName":   name,
			"fileSize":   int64(1),
			"type":       0,
			"status":     status,
			"createTime": time.Now().Unix(),
		},
	}); err != nil {
		t.Fatalf("AddJobTaskItemMany: %v", err)
	}
}

func TestRetryFailedTaskReturnsErrorWhenNoUnfinishedItems(t *testing.T) {
	_, taskID := setupRetryFailedTaskTest(t, 1)
	addRetryTestItem(t, taskID, "ok.txt", taskStatusSuccess.Int())

	err := RetryFailedTask(taskID)
	if err == nil {
		t.Fatalf("RetryFailedTask did not return error when there are no unfinished items")
	}
}

func TestRetryFailedTaskReturnsErrorWhenJobBusy(t *testing.T) {
	client, taskID := setupRetryFailedTaskTest(t, 1)
	addRetryTestItem(t, taskID, "bad.txt", taskStatusFailed.Int())
	if !client.tryMarkDoing() {
		t.Fatalf("tryMarkDoing() = false, want true")
	}
	t.Cleanup(func() { client.markDone() })

	err := RetryFailedTask(taskID)
	if err == nil {
		t.Fatalf("RetryFailedTask did not return error when job is busy")
	}
}

func TestRetryFailedTaskReturnsErrorWhenJobDisabled(t *testing.T) {
	_, taskID := setupRetryFailedTaskTest(t, 0)
	addRetryTestItem(t, taskID, "bad.txt", taskStatusFailed.Int())

	err := RetryFailedTask(taskID)
	if err == nil {
		t.Fatalf("RetryFailedTask did not return error when job is disabled")
	}
}

// A retry run replays its source task's items in batches. Deleting the source
// task mid-run would silently drop the unread remainder while the retry still
// reports success, so RemoveTask must refuse while that task is claimed.
func TestRemoveTaskRejectsTaskBeingReplayed(t *testing.T) {
	client, taskID := setupRetryFailedTaskTest(t, 1)
	// Finish the source task so RemoveTask's status check would otherwise allow
	// the delete.
	if _, err := mapper.GetDB().Exec("UPDATE job_task SET status=? WHERE id=?", taskStatusStopped.Int(), taskID); err != nil {
		t.Fatalf("finish source task: %v", err)
	}

	client.setRetrySource(taskID)
	if err := RemoveTask(taskID); err == nil {
		t.Fatal("RemoveTask() deleted a task that a retry run is replaying")
	}
	var count int
	if err := mapper.GetDB().QueryRow("SELECT COUNT(*) FROM job_task WHERE id=?", taskID).Scan(&count); err != nil {
		t.Fatalf("count source task: %v", err)
	}
	if count != 1 {
		t.Fatalf("source task rows = %d, want the task kept while replayed", count)
	}

	// Once the replay is over the claim is released and the task is deletable.
	client.clearCurrentTask(nil)
	if err := RemoveTask(taskID); err != nil {
		t.Fatalf("RemoveTask() after the replay finished: %v", err)
	}
	if err := mapper.GetDB().QueryRow("SELECT COUNT(*) FROM job_task WHERE id=?", taskID).Scan(&count); err != nil {
		t.Fatalf("count source task after delete: %v", err)
	}
	if count != 0 {
		t.Fatalf("source task rows = %d, want 0 after a permitted delete", count)
	}
}
