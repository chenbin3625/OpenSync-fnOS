package service

import (
	"encoding/json"
	"testing"
	"time"

	"opensync/internal/native"
)

func TestNativeProgressReportsCheckedAndTotalFiles(t *testing.T) {
	jt := &JobTask{
		TaskID:         42,
		CreateTime:     float64(time.Now().Unix() - 5),
		nativeConfig:   native.Config{},
		FinishedCounts: make(map[taskStatus]int),
		FinishedSizes:  make(map[taskStatus]int64),
		Doing:          make(map[int64]*CopyItem),
		Waiting:        newCopyQueueWithCapacity(16),
	}
	jt.ScanTotalDirs.Store(581)
	jt.ScanDoneDirs.Store(581)
	jt.ScanTotalFiles.Store(62655)
	jt.ScanCheckedFiles.Store(18420)
	jt.nativeStats.Store(&nativeProgressSnapshot{
		src: "/本地/X~abc",
		dst: "/openlist/quark/X",
		stats: &native.Stats{
			Checks:      18420,
			TotalChecks: 62655,
		},
	})

	payload := jt.getCurrentPayload()
	if payload.ScanFinish {
		t.Fatal("expected ScanFinish=false while rclone is still checking")
	}
	if payload.FirstSync != nil {
		t.Fatalf("expected FirstSync=nil before any transfer starts, got %v", *payload.FirstSync)
	}
	if payload.Scan == nil {
		t.Fatal("expected Scan progress payload to be populated")
	}
	if payload.Scan.ScannedDirs != 581 || payload.Scan.TotalFiles != 62655 || payload.Scan.CheckedFiles != 18420 {
		t.Fatalf("unexpected scan progress: %+v", *payload.Scan)
	}

	legacy := jt.GetCurrent()
	scanMap, ok := legacy["scan"].(map[string]int64)
	if !ok {
		t.Fatalf("expected scan map in GetCurrent(), got %T", legacy["scan"])
	}
	if scanMap["totalFiles"] != 62655 || scanMap["checkedFiles"] != 18420 {
		t.Fatalf("unexpected legacy scan map: %+v", scanMap)
	}

	// Non-native task (ScanTotalFiles == 0) must omit checkedFiles/totalFiles in JSON.
	nonNative := &JobTask{
		TaskID:         43,
		CreateTime:     float64(time.Now().Unix() - 2),
		FinishedCounts: make(map[taskStatus]int),
		FinishedSizes:  make(map[taskStatus]int64),
		Doing:          make(map[int64]*CopyItem),
		Waiting:        newCopyQueueWithCapacity(16),
	}
	nonNative.ScanTotalDirs.Store(10)
	nonNative.ScanDoneDirs.Store(4)
	raw, err := json.Marshal(nonNative.getCurrentPayload())
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	decodedScan, _ := decoded["scan"].(map[string]interface{})
	if _, exists := decodedScan["totalFiles"]; exists {
		t.Fatalf("non-native scan JSON must omit totalFiles, got %v", decodedScan)
	}
	if _, exists := decodedScan["checkedFiles"]; exists {
		t.Fatalf("non-native scan JSON must omit checkedFiles, got %v", decodedScan)
	}
}
