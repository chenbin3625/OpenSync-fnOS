//go:build linux

package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"opensync/internal/mapper"
)

func TestComputeInotifyWatchCap(t *testing.T) {
	if got := computeInotifyWatchCap(0); got != 65536 {
		t.Fatalf("sysMax=0 => %d, want 65536", got)
	}
	if got := computeInotifyWatchCap(524288); got != 65536 {
		t.Fatalf("sysMax=524288 => %d, want 65536", got)
	}
	if got := computeInotifyWatchCap(65536); got != 32768 {
		t.Fatalf("sysMax=65536 => %d, want 32768", got)
	}
	if got := computeInotifyWatchCap(10); got != 5 {
		t.Fatalf("sysMax=10 => %d, want 5", got)
	}
}

func TestInotifyExcludePruningAvoidsWatchExhaustionAndReportsDegradation(t *testing.T) {
	rootDir := t.TempDir()
	// Active synced dirs: root + keep + keep/sub (3 watches)
	if err := os.MkdirAll(filepath.Join(rootDir, "keep", "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	// Noisy excluded subdirs exceeding watch cap=5 if not pruned during WalkDir.
	for i := 0; i < 6; i++ {
		if err := os.MkdirAll(filepath.Join(rootDir, "node_modules", fmt.Sprintf("pkg-%d", i)), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(rootDir, ".git", "objects", "00"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rootDir, "@eaDir", "thumb"), 0755); err != nil {
		t.Fatal(err)
	}

	origRead := readMaxUserWatchesFn
	readMaxUserWatchesFn = func() int { return 10 } // cap = 5 watches per job
	defer func() { readMaxUserWatchesFn = origRead }()

	// 1. With exclude rules: node_modules/, .git/, @eaDir/ are pruned at WalkDir time.
	ctx1, cancel1 := context.WithCancel(context.Background())
	readyCh := make(chan bool, 1)
	var degradedMu sync.Mutex
	degradedReasons := []string(nil)
	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		watchLocalRootsPlatform(ctx1, []watchLocalRoot{{virtualPath: "/src", absDir: rootDir}}, func([]string, bool) {}, localWatchOptions{
			exclude: newExcludeMatcher("node_modules/\n.git/\n@eaDir/"),
			onReady: func(covers bool) {
				readyCh <- covers
			},
			onDegraded: func(reason string) {
				degradedMu.Lock()
				degradedReasons = append(degradedReasons, reason)
				degradedMu.Unlock()
			},
		})
	}()

	select {
	case covers := <-readyCh:
		if !covers {
			t.Fatal("expected full inotify coverage when excluded dirs are pruned")
		}
	case <-time.After(3 * time.Second):
		cancel1()
		<-done1
		t.Fatal("timed out waiting for pruned inotify ready")
	}
	cancel1()
	<-done1
	if len(degradedReasons) != 0 {
		t.Fatalf("unexpected degradation with exclude pruning: %v", degradedReasons)
	}

	// 2. Without exclude rules: >5 dirs exhaust dynamic cap and trigger degradation notice.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	ready2 := false
	var reason2 string
	watchLocalRootsPlatform(ctx2, []watchLocalRoot{{virtualPath: "/src", absDir: rootDir}}, func([]string, bool) {}, localWatchOptions{
		onReady: func(bool) {
			ready2 = true
		},
		onDegraded: func(reason string) {
			reason2 = reason
		},
	})
	if ready2 {
		t.Fatal("expected unpruned tree > cap to skip onReady full coverage")
	}
	if reason2 == "" || !strings.Contains(reason2, "5") {
		t.Fatalf("expected watchDegradedReason mentioning dynamic cap 5, got %q", reason2)
	}
}

func TestAddSyncStatusExposesWatchDegradation(t *testing.T) {
	jobFSWatchers.ResetForTest()
	defer jobFSWatchers.ResetForTest()

	const jobID int64 = 77701
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &jobWatcher{jobID: jobID, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1)}
	w.setWatchDegraded(true, "本地目录数超过 inotify 监听上限（65536），已降级为周期轮询")

	jobFSWatchers.mu.Lock()
	jobFSWatchers.watchers[jobID] = w
	jobFSWatchers.mu.Unlock()

	row := map[string]interface{}{
		"id":     jobID,
		"isCron": 3,
		"enable": 1,
	}
	addSyncStatus(row, mapper.SyncState{LastSuccess: 100}, nil, 0, nil)
	status, ok := row["syncStatus"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing syncStatus: %#v", row)
	}
	if status["watchDegraded"] != true {
		t.Fatalf("watchDegraded = %#v, want true", status["watchDegraded"])
	}
	if status["watchDegradedReason"] != "本地目录数超过 inotify 监听上限（65536），已降级为周期轮询" {
		t.Fatalf("unexpected watchDegradedReason: %#v", status["watchDegradedReason"])
	}
}
