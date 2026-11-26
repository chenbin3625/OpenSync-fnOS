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

func TestNativePlannedProgressAcrossMultiPairAndLargeBacklog(t *testing.T) {
	jt := &JobTask{
		TaskID:         77,
		CreateTime:     float64(time.Now().Unix() - 30),
		nativeConfig:   native.Config{},
		FinishedCounts: make(map[taskStatus]int),
		FinishedSizes:  make(map[taskStatus]int64),
		Doing:          make(map[int64]*CopyItem),
		Waiting:        newCopyQueueWithCapacity(defaultCopyQueueCapacity),
	}

	// Simulate 4 pairs totaling 25,000 files and 100 GB (exceeding rclone's 10,000 backlog cap).
	const gb = int64(1 << 30)
	jt.initNativePlanned(25000, 100*gb)
	jt.ScanFinish.Store(true)

	// Pair 1 finished 1,024 files totaling 20 GB (exact byte divisor).
	const pair1Files = 1024
	jt.FinishedCounts[taskStatusSuccess] = pair1Files
	jt.FinishedSizes[taskStatusSuccess] = 20 * gb
	for i := 0; i < pair1Files; i++ {
		jt.recordNativePlannedFileDone(20*gb/pair1Files, true)
	}

	// Between Pair 1 and Pair 2: nativeStats is nil, progress must NOT flicker to 100%.
	between := jt.getCurrentPayload()
	if between.DoneSize != 20*gb || between.RemainSize != 80*gb {
		t.Fatalf("between-pair sizes = (%d, %d), want (%d, %d)", between.DoneSize, between.RemainSize, 20*gb, 80*gb)
	}
	if between.Num.Wait != 25000-pair1Files {
		t.Fatalf("between-pair wait = %d, want %d", between.Num.Wait, 25000-pair1Files)
	}
	if pct := calcTaskProgressPercent(between); pct < 19.9 || pct > 20.1 {
		t.Fatalf("between-pair progress = %v, want ~20", pct)
	}

	// Pair 2 starts: rclone only reports Pair 2's backlog-capped stats (10,000 files, 2 GB),
	// with 2 files currently transferring (512 MB done of 1 GB each).
	jt.nativeStats.Store(&nativeProgressSnapshot{
		src: "/本地/pair2",
		dst: "/google/pair2",
		stats: &native.Stats{
			Bytes:          1 * gb,
			TotalBytes:     2 * gb,
			Transfers:      0,
			TotalTransfers: 10000,
			Speed:          50 << 20,
			Transferring: []native.TransferItem{
				{Name: "a.bin", Size: 1 * gb, Bytes: gb / 2, Percentage: 50, Speed: 25 << 20},
				{Name: "b.bin", Size: 1 * gb, Bytes: gb / 2, Percentage: 50, Speed: 25 << 20},
			},
		},
	})

	duringPair2 := jt.getCurrentPayload()
	// Progress must reflect all 4 pairs (21 GB / 100 GB = 21%), NOT 20 GB / 21 GB = 95%.
	if duringPair2.DoneSize != 21*gb || duringPair2.RemainSize != 79*gb {
		t.Fatalf("pair2 sizes = (%d, %d), want (%d, %d)", duringPair2.DoneSize, duringPair2.RemainSize, 21*gb, 79*gb)
	}
	if duringPair2.Num.Running != 2 || duringPair2.Num.Wait != 25000-pair1Files-2 {
		t.Fatalf("pair2 counts = %+v, want running=2 wait=%d", duringPair2.Num, 25000-pair1Files-2)
	}
	if pct := calcTaskProgressPercent(duringPair2); pct < 20.9 || pct > 21.1 {
		t.Fatalf("pair2 progress = %v, want ~21", pct)
	}
}

func TestNativeIncrementalProgressIgnoresUncheckedWaitingAndRetryInflation(t *testing.T) {
	jt := &JobTask{
		TaskID:         78,
		CreateTime:     float64(time.Now().Unix() - 10),
		nativeConfig:   native.Config{},
		FinishedCounts: make(map[taskStatus]int),
		FinishedSizes:  make(map[taskStatus]int64),
		Doing:          make(map[int64]*CopyItem),
		Waiting:        newCopyQueueWithCapacity(defaultCopyQueueCapacity),
	}
	// Populate jt.Waiting with 500 prescanned source files (before rclone decides which differ).
	pending := make([]native.PendingFile, 500)
	for i := range pending {
		pending[i] = native.PendingFile{Path: "f.bin", Size: 1024}
	}
	jt.populateNativeWaiting("/src", "/dst", pending)

	// While rclone is checking files (TotalTransfers == 0), waitCount must be 0 rather than 500.
	jt.nativeStats.Store(&nativeProgressSnapshot{
		src: "/src",
		dst: "/dst",
		stats: &native.Stats{
			Checks:      200,
			TotalChecks: 500,
		},
	})
	checking := jt.getCurrentPayload()
	if checking.Num.Wait != 0 {
		t.Fatalf("while checking with TotalTransfers=0, wait = %d, want 0", checking.Num.Wait)
	}

	// Simulate rclone retry byte inflation: stats.Bytes (800) > stats.TotalBytes (500)
	// while a 500-byte file is still transferring at 200/500.
	jt.ScanFinish.Store(true)
	jt.nativeStats.Store(&nativeProgressSnapshot{
		src: "/src",
		dst: "/dst",
		stats: &native.Stats{
			Bytes:          800,
			TotalBytes:     500,
			Transfers:      0,
			TotalTransfers: 1,
			Transferring: []native.TransferItem{
				{Name: "retry.bin", Size: 500, Bytes: 200, Percentage: 40, Speed: 10},
			},
		},
	})
	retrying := jt.getCurrentPayload()
	if retrying.DoneSize != 200 || retrying.RemainSize != 300 {
		t.Fatalf("retry inflation sizes = (%d, %d), want (200, 300)", retrying.DoneSize, retrying.RemainSize)
	}
	if pct := calcTaskProgressPercent(retrying); pct < 39.9 || pct > 40.1 {
		t.Fatalf("retry inflation progress = %v, want ~40", pct)
	}
}

func TestCalcTaskProgressPercentZeroByteAndCap(t *testing.T) {
	// Zero-byte workload falls back to item count.
	zeroBytes := jobCurrentPayload{
		ScanFinish: true,
		Num:        taskNumStats{Wait: 25, Running: 5, Success: 70},
	}
	if pct := calcTaskProgressPercent(zeroBytes); pct < 69.9 || pct > 70.1 {
		t.Fatalf("zero-byte progress = %v, want ~70", pct)
	}

	// Nearly-complete bytes with remaining work caps at 99% rather than rounding to 100%.
	almostDone := jobCurrentPayload{
		ScanFinish: true,
		DoneSize:   999,
		RemainSize: 1,
		Num:        taskNumStats{Wait: 1, Running: 1, Success: 99},
	}
	if pct := calcTaskProgressPercent(almostDone); pct != 99 {
		t.Fatalf("almost-done running progress = %v, want 99", pct)
	}

	// Finished scan and zero remaining work reaches 100%.
	finished := jobCurrentPayload{
		ScanFinish: true,
		DoneSize:   1000,
		RemainSize: 0,
		Num:        taskNumStats{Success: 100},
	}
	if pct := calcTaskProgressPercent(finished); pct != 100 {
		t.Fatalf("finished progress = %v, want 100", pct)
	}
}
