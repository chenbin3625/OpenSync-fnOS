package service

import (
	"math"
	"opensync/internal/mapper"
	"opensync/internal/native"
	"testing"
	"time"
)

func TestDashboardSpeedHistoryRingAndPeak(t *testing.T) {
	resetDashboardSpeedHistory()
	defer resetDashboardSpeedHistory()

	base := int64(1_700_000_000)
	// Same-second updates overwrite latest sample instead of duplicating.
	hist, peak := recordDashboardSpeedSample(base, 1024)
	if len(hist) != 1 || peak != 1024 {
		t.Fatalf("initial sample mismatch: hist=%+v peak=%v", hist, peak)
	}
	hist, peak = recordDashboardSpeedSample(base, 2048)
	if len(hist) != 1 || hist[0].Speed != 2048 || peak != 2048 {
		t.Fatalf("same-second update mismatch: hist=%+v peak=%v", hist, peak)
	}

	// NaN / negative speeds are sanitized to 0.
	hist, peak = recordDashboardSpeedSample(base+2, math.NaN())
	if len(hist) != 2 || hist[1].Speed != 0 || peak != 2048 {
		t.Fatalf("NaN sanitize mismatch: hist=%+v peak=%v", hist, peak)
	}

	// Ring buffer caps at dashboardSpeedHistoryMax samples.
	for i := 1; i <= 40; i++ {
		hist, peak = recordDashboardSpeedSample(base+int64(i*2), float64(i*1000))
	}
	if len(hist) != dashboardSpeedHistoryMax {
		t.Fatalf("expected ring size %d, got %d", dashboardSpeedHistoryMax, len(hist))
	}
	if peak != 40_000 {
		t.Fatalf("expected peak 40000, got %v", peak)
	}

	// Stale samples older than max age are evicted.
	hist, peak = recordDashboardSpeedSample(base+1000, 500)
	if len(hist) != 1 || hist[0].Speed != 500 || peak != 500 {
		t.Fatalf("stale prune mismatch: hist=%+v peak=%v", hist, peak)
	}
}

func TestGetDashboardOverviewAggregatesRealtimeSpeed(t *testing.T) {
	_, cleanup := setupFullFeatureTestDB(t)
	defer cleanup()
	resetDashboardSpeedHistory()
	defer resetDashboardSpeedHistory()

	alistID, err := EnsureNativeEngine()
	if err != nil {
		t.Fatalf("EnsureNativeEngine: %v", err)
	}
	jobID, err := mapper.AddJob(map[string]interface{}{
		"enable": 1, "remark": "速率聚合任务", "srcPath": `["/a"]`, "dstPath": `["/b"]`,
		"alistId": alistID, "method": 0, "isCron": 2,
	})
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	nowSec := float64(time.Now().Unix())
	jt := &JobTask{
		TaskID:     901,
		CreateTime: nowSec - 10,
	}
	jt.initRuntime()
	jt.ScanFinish.Store(true)
	jt.FirstSync.Store(int64(nowSec - 10))
	jt.nativeStats.Store(&nativeProgressSnapshot{
		src: "/a",
		dst: "/b",
		stats: &native.Stats{
			Bytes:      50 * 1024 * 1024,
			TotalBytes: 200 * 1024 * 1024,
			Speed:      15 * 1024 * 1024,
		},
	})

	client := &JobClient{JobID: jobID}
	client.setCurrentTask(jt)
	jobClientListMu.Lock()
	jobClientList[jobID] = client
	jobClientListMu.Unlock()
	defer func() {
		jobClientListMu.Lock()
		delete(jobClientList, jobID)
		jobClientListMu.Unlock()
	}()

	overview, err := GetDashboardOverview()
	if err != nil {
		t.Fatalf("GetDashboardOverview: %v", err)
	}

	rt, ok := overview["realtime"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing realtime payload: %#v", overview["realtime"])
	}
	wantSpeed := float64(15 * 1024 * 1024)
	if got, _ := rt["totalSpeed"].(float64); got != wantSpeed {
		t.Fatalf("totalSpeed = %v, want %v", got, wantSpeed)
	}
	if got, _ := rt["activeJobs"].(int); got != 1 {
		t.Fatalf("activeJobs = %v, want 1", got)
	}
	if got, _ := rt["estimatedRemainTime"].(int); got <= 0 {
		t.Fatalf("expected positive estimatedRemainTime, got %v", got)
	}
	if hist, ok := rt["speedHistory"].([]dashboardSpeedSample); !ok || len(hist) != 1 || hist[0].Speed != wantSpeed {
		t.Fatalf("unexpected speedHistory: %#v", rt["speedHistory"])
	}

	jobs, _ := overview["jobs"].([]map[string]interface{})
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if got, _ := jobs[0]["liveSpeed"].(float64); got != wantSpeed {
		t.Fatalf("job liveSpeed = %v, want %v", got, wantSpeed)
	}
	if got, _ := jobs[0]["liveRemainTime"].(int); got <= 0 {
		t.Fatalf("expected positive job liveRemainTime, got %v", got)
	}
}
