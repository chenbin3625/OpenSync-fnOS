package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"opensync/internal/mapper"
	"opensync/internal/native"
)

type stubServiceRemoteDeltaProber struct {
	mu      sync.Mutex
	calls   int
	cursors []string
	fn      func(callIdx int, rootVirtual string, opts native.RemoteDeltaOptions) (native.RemoteDeltaResult, error)
}

func (s *stubServiceRemoteDeltaProber) PollRemoteChanges(ctx context.Context, cfg native.Config, ep native.Endpoint, rootVirtual string, opts native.RemoteDeltaOptions) (native.RemoteDeltaResult, error) {
	s.mu.Lock()
	s.calls++
	idx := s.calls
	s.cursors = append(s.cursors, opts.Cursor)
	s.mu.Unlock()
	return s.fn(idx, rootVirtual, opts)
}

func TestAdaptivePollBackoffProgressionAndResets(t *testing.T) {
	jobFSWatchers.ResetForTest()
	defer jobFSWatchers.ResetForTest()

	poll := 30 * time.Second
	if got := computeAdaptivePollInterval(poll, 0); got != 30*time.Second {
		t.Fatalf("streak 0 = %v, want 30s", got)
	}
	if got := computeAdaptivePollInterval(poll, 1); got != 30*time.Second {
		t.Fatalf("streak 1 = %v, want 30s", got)
	}
	if got := computeAdaptivePollInterval(poll, 2); got != 60*time.Second {
		t.Fatalf("streak 2 = %v, want 60s", got)
	}
	if got := computeAdaptivePollInterval(poll, 3); got != 120*time.Second {
		t.Fatalf("streak 3 = %v, want 120s", got)
	}
	if got := computeAdaptivePollInterval(poll, 10); got != 3600*time.Second {
		t.Fatalf("streak 10 = %v, want 3600s cap", got)
	}
	if got := computeAdaptivePollInterval(7200*time.Second, 5); got != 7200*time.Second {
		t.Fatalf("basePoll > 3600s should cap at max(poll,3600s)=7200s, got %v", got)
	}

	const jobID int64 = 77802
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &jobWatcher{jobID: jobID, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1)}
	w.initPollBackoff(poll)

	jobFSWatchers.mu.Lock()
	jobFSWatchers.watchers[jobID] = w
	jobFSWatchers.mu.Unlock()

	if eff, streak := w.pollStatus(poll); eff != 30*time.Second || streak != 0 {
		t.Fatalf("initial pollStatus = (%v,%d)", eff, streak)
	}
	_ = w.advancePollBackoff(poll) // streak=1 -> 30s
	eff2 := w.advancePollBackoff(poll)
	if eff2 != 60*time.Second {
		t.Fatalf("2nd no-change backoff = %v, want 60s", eff2)
	}
	eff3 := w.advancePollBackoff(poll)
	if eff3 != 120*time.Second {
		t.Fatalf("3rd no-change backoff = %v, want 120s", eff3)
	}

	// syncStatus must reflect effectivePollSec and nextPoll widening
	row := map[string]interface{}{
		"id":         jobID,
		"isCron":     3,
		"enable":     1,
		"extraFlags": `{"syncPollIntervalSec":30}`,
	}
	addSyncStatus(row, mapper.SyncState{LastPoll: 1000, LastSuccess: 1000}, nil, 0, nil)
	status := row["syncStatus"].(map[string]interface{})
	if status["pollIntervalSec"] != 30 || status["effectivePollSec"] != 120 || status["pollBackoffStreak"] != 3 || status["nextPoll"] != int64(1120) {
		t.Fatalf("unexpected backed-off syncStatus: %+v", status)
	}

	// Reset via ResetPollBackoff (manual trigger / recovery)
	jobFSWatchers.ResetPollBackoff(jobID)
	if eff, streak := w.pollStatus(poll); eff != 30*time.Second || streak != 0 {
		t.Fatalf("ResetPollBackoff failed: (%v,%d)", eff, streak)
	}
	// Drain wake channel sent by ResetPollBackoff
	select {
	case <-w.wake:
	default:
	}

	// Escalate again and verify local enqueue reset
	_ = w.advancePollBackoff(poll)
	_ = w.advancePollBackoff(poll)
	if eff, _ := w.pollStatus(poll); eff != 60*time.Second {
		t.Fatalf("expected 60s before enqueue reset, got %v", eff)
	}
	w.mu.Lock()
	w.resetPollBackoffLocked(0)
	w.mu.Unlock()
	if eff, streak := w.pollStatus(poll); eff != 30*time.Second || streak != 0 {
		t.Fatalf("resetPollBackoffLocked failed: (%v,%d)", eff, streak)
	}
}

func TestRemoteDeltaProbeTargetedDirRefreshWithoutFullTreeScan(t *testing.T) {
	if os.Getenv("OPENSYNC_RCLONE_BIN") == "" {
		t.Skip("set OPENSYNC_RCLONE_BIN for ListDirEntries delta integration")
	}

	t.Setenv("OPENSYNC_DATA_DIR", t.TempDir())
	t.Setenv("TRIM_PKGVAR", "")
	defer setupNativeTestDB(t)()
	jobFSWatchers.ResetForTest()
	defer jobFSWatchers.ResetForTest()

	base := t.TempDir()
	srcRoot := filepath.Join(base, "src")
	dstRoot := filepath.Join(base, "dst")
	for _, p := range []string{
		filepath.Join(srcRoot, "sub"),
		filepath.Join(srcRoot, "other"),
		dstRoot,
	} {
		if err := os.MkdirAll(p, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "sub", "initial.txt"), []byte("init"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "other", "untouched.txt"), []byte("stay"), 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", srcRoot+":"+dstRoot)
	t.Setenv("TRIM_PKGTMP", "")
	if err := SaveNativeStorage(context.Background(), NativeStorageInput{}); err != nil {
		t.Fatal(err)
	}
	state, err := GetNativeStorageState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	roots, err := GetChildPath(context.Background(), state.EngineID, "/"+native.Local)
	if err != nil {
		t.Fatal(err)
	}
	virtual := func(name string) string {
		for _, r := range roots {
			if filepath.Base(r["name"]) == name {
				return r["path"]
			}
		}
		t.Fatalf("root %s not found", name)
		return ""
	}
	srcVirtual := virtual("src")
	dstVirtual := virtual("dst")

	jobID, err := CreateJobClient(map[string]interface{}{
		"alistId": state.EngineID,
		"remark":  "delta probe test",
		"srcPath": srcVirtual,
		"dstPath": dstVirtual,
		"method":  0,
		"isCron":  3,
		"enable":  0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = RemoveJobClient(jobID) }()
	jobRow, err := mapper.GetJobByID(jobID)
	if err != nil {
		t.Fatal(err)
	}

	prober := &stubServiceRemoteDeltaProber{
		fn: func(callIdx int, rootVirtual string, opts native.RemoteDeltaOptions) (native.RemoteDeltaResult, error) {
			switch callIdx {
			case 1:
				// Initial baseline build via ListTree
				return native.RemoteDeltaResult{Supported: true, HasChanges: true, FullRescan: true, NextCursor: "cur-1"}, nil
			case 2:
				// No changes -> short-circuit without any tree/dir scan
				return native.RemoteDeltaResult{Supported: true, HasChanges: false, NextCursor: "cur-2"}, nil
			default:
				// Incremental single-dir change -> targeted ListDirEntries ONLY on "sub"
				return native.RemoteDeltaResult{Supported: true, HasChanges: true, NextCursor: "cur-3", ChangedDirs: []string{"sub"}}, nil
			}
		},
	}
	defer native.SetRemoteChangeProberForTest(prober)()

	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &jobWatcher{jobID: jobID, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1)}

	// 1. Initial full scan establishes sync_file_state baseline.
	changed, err := w.scanSyncRootsWithResult(context.Background(), jobRow, []string{srcVirtual}, true)
	if err != nil || !changed {
		t.Fatalf("initial scan = (%v,%v), want (true,nil)", changed, err)
	}
	s, err := mapper.GetSyncState(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if err := mapper.CompleteSyncRun(jobID, s.Generation, ""); err != nil {
		t.Fatal(err)
	}

	// 2. Unchanged delta short-circuits cleanly without reporting changes.
	changed, err = w.scanSyncRootsWithResult(context.Background(), jobRow, []string{srcVirtual}, true)
	if err != nil || changed {
		t.Fatalf("no-change delta scan = (%v,%v), want (false,nil)", changed, err)
	}

	// 3. Add a file in "sub" AND an unreported file in "other": because delta targets ONLY "sub",
	// ListDirEntries discovers sub/added.txt WITHOUT running ListTree on "other" or the full tree.
	if err := os.WriteFile(filepath.Join(srcRoot, "sub", "added.txt"), []byte("new-sub"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "other", "unscanned.txt"), []byte("not-in-delta"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err = w.scanSyncRootsWithResult(context.Background(), jobRow, []string{srcVirtual}, true)
	if err != nil || !changed {
		t.Fatalf("targeted dir delta scan = (%v,%v), want (true,nil)", changed, err)
	}

	s, err = mapper.GetSyncState(jobID)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(s.Paths, ",")
	if s.FullScan || !strings.Contains(joined, "sub/added.txt") || strings.Contains(joined, "other/unscanned.txt") {
		t.Fatalf("expected incremental inbox containing sub/added.txt only (no full ListTree), got %+v", s)
	}
	if prober.calls != 3 || prober.cursors[0] != "" || prober.cursors[1] != "cur-1" || prober.cursors[2] != "cur-2" {
		t.Fatalf("unexpected delta cursor progression: calls=%d cursors=%v", prober.calls, prober.cursors)
	}
}
