package service

import (
	"testing"
	"time"

	"opensync/internal/mapper"
	"opensync/internal/native"
	"opensync/pkg/util"
)

func TestTaskLevelPauseResumeUpdatesProgressListAndDashboard(t *testing.T) {
	defer setupNativeTestDB(t)()

	engineID, err := EnsureNativeEngine()
	if err != nil {
		t.Fatalf("EnsureNativeEngine: %v", err)
	}
	if err := SaveNativeStorage(t.Context(), NativeStorageInput{
		Name: "oss",
		Options: map[string]string{
			"type":              "s3",
			"provider":          "Alibaba",
			"endpoint":          "oss-cn-hangzhou.aliyuncs.com",
			"access_key_id":     "ak",
			"secret_access_key": "sk",
		},
	}); err != nil {
		t.Fatalf("SaveNativeStorage: %v", err)
	}
	jobID, err := CreateJobClient(map[string]interface{}{
		"remark":  "暂停继续测试任务",
		"alistId": engineID,
		"srcPath": []string{"/oss/bucket/src"},
		"dstPath": []string{"/oss/bucket/dst"},
		"method":  0,
		"isCron":  2,
	})
	if err != nil {
		t.Fatalf("CreateJobClient: %v", err)
	}
	jc, err := GetJobClientByID(jobID)
	if err != nil {
		t.Fatalf("GetJobClientByID: %v", err)
	}

	taskID, err := mapper.AddJobTask(jobID, time.Now().Unix())
	if err != nil {
		t.Fatalf("AddJobTask: %v", err)
	}
	jt, err := newJobTask(taskID, jc)
	if err != nil {
		t.Fatalf("newJobTask: %v", err)
	}
	defer jt.cancelTaskRuntime()

	// Seed active transfer stats so we can verify speed & ETA zeroing while paused.
	jt.FirstSync.Store(time.Now().Unix() - 10)
	jt.nativeStats.Store(&nativeProgressSnapshot{
		src: "/oss/bucket/src",
		dst: "/oss/bucket/dst",
		stats: &native.Stats{
			Bytes:          50 << 20,
			TotalBytes:     100 << 20,
			Transfers:      1,
			TotalTransfers: 2,
			Speed:          10 << 20,
			Transferring: []native.TransferItem{
				{
					Name:       "big.bin",
					Size:       50 << 20,
					Bytes:      10 << 20,
					Percentage: 20,
					Speed:      10 << 20,
				},
			},
		},
	})

	jc.mu.Lock()
	jc.CurrentJobTask = jt
	jc.mu.Unlock()
	defer jc.clearCurrentTask(jt)

	// 1. Running before pause: paused=false, speed > 0, remainTime > 0
	runningPayload := jt.getCurrentPayload()
	if runningPayload.Paused {
		t.Fatal("expected Paused=false initially")
	}
	if runningPayload.Speed <= 0 {
		t.Fatalf("expected Speed > 0 before pause, got %v", runningPayload.Speed)
	}
	if runningPayload.RemainTime <= 0 {
		t.Fatalf("expected RemainTime > 0 before pause, got %d", runningPayload.RemainTime)
	}
	if len(runningPayload.DoingTask) != 1 || runningPayload.DoingTask[0].Speed <= 0 {
		t.Fatalf("expected DoingTask speed > 0 before pause, got %+v", runningPayload.DoingTask)
	}

	// 2. Pause via service.PauseTaskTransfer
	if err := PauseTaskTransfer(taskID); err != nil {
		t.Fatalf("PauseTaskTransfer failed: %v", err)
	}

	pausedPayload := jt.getCurrentPayload()
	if !pausedPayload.Paused {
		t.Fatal("expected Paused=true after PauseTaskTransfer")
	}
	if pausedPayload.Speed != 0 {
		t.Fatalf("expected Speed=0 while paused, got %v", pausedPayload.Speed)
	}
	if pausedPayload.RemainTime != 0 {
		t.Fatalf("expected RemainTime=0 while paused, got %d", pausedPayload.RemainTime)
	}
	if len(pausedPayload.DoingTask) != 1 || pausedPayload.DoingTask[0].Speed != 0 {
		t.Fatalf("expected DoingTask speed=0 while paused, got %+v", pausedPayload.DoingTask)
	}

	// Verify GET /svr/job list exposes paused=true
	jobList, err := GetJobList(map[string]interface{}{"pageNum": 1, "pageSize": 20})
	if err != nil {
		t.Fatalf("GetJobList failed: %v", err)
	}
	rows, _ := jobList["dataList"].([]map[string]interface{})
	foundPausedInList := false
	for _, row := range rows {
		if util.ToInt64(row["id"]) == jobID {
			if row["running"] != true || row["paused"] != true {
				t.Fatalf("GetJobList row running=%v paused=%v, want true/true", row["running"], row["paused"])
			}
			foundPausedInList = true
		}
	}
	if !foundPausedInList {
		t.Fatalf("job %d not found in GetJobList", jobID)
	}

	// Verify GET /svr/dashboard exposes paused=true
	overview, err := GetDashboardOverview()
	if err != nil {
		t.Fatalf("GetDashboardOverview failed: %v", err)
	}
	dashJobs, _ := overview["jobs"].([]map[string]interface{})
	foundPausedInDash := false
	for _, row := range dashJobs {
		if util.ToInt64(row["id"]) == jobID {
			if row["running"] != true || row["paused"] != true {
				t.Fatalf("Dashboard row running=%v paused=%v, want true/true", row["running"], row["paused"])
			}
			foundPausedInDash = true
		}
	}
	if !foundPausedInDash {
		t.Fatalf("job %d not found in GetDashboardOverview", jobID)
	}

	// 3. Resume via service.ResumeTaskTransfer
	if err := ResumeTaskTransfer(taskID); err != nil {
		t.Fatalf("ResumeTaskTransfer failed: %v", err)
	}
	resumedPayload := jt.getCurrentPayload()
	if resumedPayload.Paused {
		t.Fatal("expected Paused=false after ResumeTaskTransfer")
	}
	if resumedPayload.Speed <= 0 {
		t.Fatalf("expected Speed > 0 after ResumeTaskTransfer, got %v", resumedPayload.Speed)
	}

	// 4. Stopping a task rejects subsequent pause/resume
	jt.requestBreak()
	if err := PauseTaskTransfer(taskID); err == nil {
		t.Fatal("expected PauseTaskTransfer on stopping task to fail")
	}
	if err := ResumeTaskTransfer(taskID); err == nil {
		t.Fatal("expected ResumeTaskTransfer on stopping task to fail")
	}

	// 5. Non-running task ID returns error
	jc.clearCurrentTask(jt)
	if err := PauseTaskTransfer(taskID); err == nil {
		t.Fatal("expected PauseTaskTransfer on non-running task to fail")
	}
}
