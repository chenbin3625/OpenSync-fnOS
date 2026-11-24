package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"opensync/internal/mapper"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func isolateJobClientRegistryForTest(t *testing.T) {
	t.Helper()
	jobClientListMu.Lock()
	saved := jobClientList
	jobClientList = make(map[int64]*JobClient)
	jobClientListMu.Unlock()
	t.Cleanup(func() {
		jobClientListMu.Lock()
		created := jobClientList
		jobClientList = saved
		jobClientListMu.Unlock()
		for _, c := range created {
			if c.Scheduler != nil {
				c.Scheduler.Stop()
			}
		}
	})
}

// Issue #1 (P0): Move mode must never skip copying and delete the source file
// when a driver omits content hashes, even if name, size, and mtime match.
func TestMoveModeDoesNotDeleteSourceWithoutHashWhenSizeAndMtimeMatch(t *testing.T) {
	dir := t.TempDir()
	srcDir := filepath.Join(dir, "src")
	dstDir := filepath.Join(dir, "dst")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcFile := filepath.Join(srcDir, "same.txt")
	dstFile := filepath.Join(dstDir, "same.txt")
	if err := os.WriteFile(srcFile, []byte("SOURCE-CONTENT-A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dstFile, []byte("DESTIN-CONTENT-B"), 0o644); err != nil {
		t.Fatal(err)
	}
	sameTime := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(srcFile, sameTime, sameTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dstFile, sameTime, sameTime); err != nil {
		t.Fatal(err)
	}

	var copyCalls, removeCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/fs/list":
			var req alistListRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			var targetPath string
			switch strings.TrimRight(req.Path, "/") {
			case "/src":
				targetPath = srcFile
			case "/dst":
				targetPath = dstFile
			default:
				_, _ = w.Write([]byte(`{"code":200,"data":{"total":0,"content":[]}}`))
				return
			}
			info, err := os.Stat(targetPath)
			if err != nil {
				_, _ = w.Write([]byte(`{"code":200,"data":{"total":0,"content":[]}}`))
				return
			}
			// Simulate a storage driver that returns size and mtime but NO hash.
			resp := map[string]interface{}{
				"code": 200,
				"data": map[string]interface{}{
					"total": 1,
					"content": []map[string]interface{}{
						{
							"name":     info.Name(),
							"is_dir":   false,
							"size":     info.Size(),
							"modified": info.ModTime().UTC().Format(time.RFC3339),
						},
					},
				},
			}
			_ = json.NewEncoder(w).Encode(resp)
		case "/api/fs/copy":
			copyCalls.Add(1)
			_, _ = w.Write([]byte(`{"code":200,"data":{"tasks":[{"id":"unexpected"}]}}`))
		case "/api/fs/remove":
			removeCalls.Add(1)
			_ = os.Remove(srcFile)
			_, _ = w.Write([]byte(`{"code":200,"data":{}}`))
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	var persisted []map[string]interface{}
	defer stubPersistJobTaskItems(t, &persisted, nil)()
	jt := scanTestTask(t, srv.URL, srv.Client(), map[string]interface{}{
		"method": 2, "srcPath": "/src/", "dstPath": "/dst/",
	})
	item := newCopyItem(jt, jt.AlistClient, "/src/", "/dst/", "same.txt", int64(len("SOURCE-CONTENT-A")), taskItemTypeTransfer)
	item.DoIt()

	if item.status() != taskStatusFailed {
		t.Fatalf("status = %v, want taskStatusFailed", item.status())
	}
	if copyCalls.Load() != 0 || removeCalls.Load() != 0 {
		t.Fatalf("copyCalls=%d removeCalls=%d, want 0/0", copyCalls.Load(), removeCalls.Load())
	}
	gotSrc, err := os.ReadFile(srcFile)
	if err != nil || string(gotSrc) != "SOURCE-CONTENT-A" {
		t.Fatalf("source file damaged: content=%q err=%v", string(gotSrc), err)
	}
	gotDst, err := os.ReadFile(dstFile)
	if err != nil || string(gotDst) != "DESTIN-CONTENT-B" {
		t.Fatalf("destination file damaged: content=%q err=%v", string(gotDst), err)
	}
}

// Issue #2 (P1): Mirror mode with conflictPolicy=2 (keep both) must never
// schedule its own *.conflict-YYYYMMDD-HHMMSS.* backups for deletion.
func TestMirrorModePreservesConflictBackupsUnderKeepBoth(t *testing.T) {
	srcSnap := emptyFullSyncSnapshot("/src/")
	srcSnap.dirs[""] = FileListResult{
		"report.pdf": {Size: 200, Modified: 2000},
	}

	dstSnap := emptyFullSyncSnapshot("/dst/")
	dstSnap.dirs[""] = FileListResult{
		"report.pdf":                         {Size: 200, Modified: 2000},
		"report.conflict-20260501-143005.pdf": {Size: 150, Modified: 1000},
		"removed/":                           {Size: 0, Modified: 1000},
		"extra.tmp":                          {Size: 10, Modified: 1000},
	}
	dstSnap.dirs["removed/"] = FileListResult{
		"note.conflict-20260501-143005.txt": {Size: 80, Modified: 1000},
		"obsolete.txt":                      {Size: 50, Modified: 1000},
	}

	job := map[string]interface{}{"method": 1, "conflictPolicy": 2}
	plan := newFullSyncPlan(srcSnap, dstSnap, nil)
	plan.build(job)

	for _, del := range plan.extraDeletes {
		if strings.Contains(del.name, ".conflict-") || del.object == "/dst/removed/" {
			t.Fatalf("mirror mode scheduled conflict backup or ancestor dir for deletion: %+v", del)
		}
	}
	if _, ok := plan.extraDeletes["/dst/extra.tmp"]; !ok {
		t.Fatalf("expected normal extra file /dst/extra.tmp in extraDeletes, got %+v", plan.extraDeletes)
	}
	if _, ok := plan.extraDeletes["/dst/removed/obsolete.txt"]; !ok {
		t.Fatalf("expected non-conflict file /dst/removed/obsolete.txt in extraDeletes, got %+v", plan.extraDeletes)
	}

	// Dry-run must match real execution and never list the conflict backup as a delete.
	listFn := func(dir string, isSrc bool) (FileListResult, bool, error) {
		if isSrc {
			return FileListResult{"report.pdf": {Size: 200, Modified: 2000}}, true, nil
		}
		if normalizeDirPath(dir) == "/dst/" {
			return FileListResult{
				"report.pdf":                          {Size: 200, Modified: 2000},
				"report.conflict-20260501-143005.pdf": {Size: 150, Modified: 1000},
			}, true, nil
		}
		return FileListResult{}, true, nil
	}
	sess := &dryRunSession{result: DryRunResult{JobID: 1, Status: "running", Method: 1, Items: []DryRunItem{}}}
	if err := dryRunFullSyncWork(context.Background(), sess, job, scanWork{SrcPath: "/src/", DstPath: "/dst/"}, nil, nil, listFn); err != nil {
		t.Fatalf("dryRunFullSyncWork error: %v", err)
	}
	if snap := sess.snapshot(); snap.DeleteCount != 0 {
		t.Fatalf("dry-run DeleteCount = %d (items=%+v), want 0 for conflict backup", snap.DeleteCount, snap.Items)
	}
}

// Issue #3 (P1): Retrying a failed copy item must enforce conflictPolicy
// (skip, keep-both) and trash-before-overwrite instead of blindly overwriting.
func TestRetryCopyEnforcesConflictPolicyAndTrash(t *testing.T) {
	cases := []struct {
		name           string
		conflictPolicy int
		trashEnable    int
		dstExists      bool
		wantCopyCalls  int
		wantOverwrite  *bool
		wantRename     bool
		wantTrashMove  bool
		wantSkipped    bool
	}{
		{
			name:           "skip when destination now exists",
			conflictPolicy: 1,
			dstExists:      true,
			wantCopyCalls:  0,
			wantSkipped:    true,
		},
		{
			name:           "skip policy uses overwrite=false when destination absent",
			conflictPolicy: 1,
			dstExists:      false,
			wantCopyCalls:  1,
			wantOverwrite:  boolPtr(false),
		},
		{
			name:           "keep-both renames existing destination before copy",
			conflictPolicy: 2,
			dstExists:      true,
			wantCopyCalls:  1,
			wantOverwrite:  boolPtr(true),
			wantRename:     true,
		},
		{
			name:           "overwrite with trash moves old destination into trash before copy",
			conflictPolicy: 0,
			trashEnable:    1,
			dstExists:      true,
			wantCopyCalls:  1,
			wantOverwrite:  boolPtr(true),
			wantTrashMove:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var ops []string
			var copyCalls int
			var gotOverwrite bool
			var renamedTo string
			var trashDstDir string
			var trashMoveOverwrite bool
			var dstExists atomic.Bool
			dstExists.Store(tc.dstExists)

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/fs/get":
					if dstExists.Load() {
						_, _ = w.Write([]byte(`{"code":200,"data":{"name":"doc.txt","is_dir":false,"size":10}}`))
					} else {
						_, _ = w.Write([]byte(`{"code":500,"message":"object not found","data":null}`))
					}
				case "/api/fs/rename":
					var req struct {
						Path string `json:"path"`
						Name string `json:"name"`
					}
					_ = json.NewDecoder(r.Body).Decode(&req)
					mu.Lock()
					ops = append(ops, "rename")
					renamedTo = req.Name
					mu.Unlock()
					_, _ = w.Write([]byte(`{"code":200,"data":{}}`))
				case "/api/fs/mkdir":
					_, _ = w.Write([]byte(`{"code":200,"data":{}}`))
				case "/api/fs/move":
					var req alistCopyMoveRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					mu.Lock()
					ops = append(ops, "trash_move")
					trashDstDir = req.DstDir
					trashMoveOverwrite = req.Overwrite
					mu.Unlock()
					_, _ = w.Write([]byte(`{"code":200,"data":{"tasks":[]}}`))
				case "/api/fs/copy":
					var req alistCopyMoveRequest
					_ = json.NewDecoder(r.Body).Decode(&req)
					mu.Lock()
					ops = append(ops, "copy")
					copyCalls++
					gotOverwrite = req.Overwrite
					mu.Unlock()
					// Sync copy returns no task id; subsequent /api/fs/get verifies destination.
					dstExists.Store(true)
					_, _ = w.Write([]byte(`{"code":200,"data":{"tasks":[]}}`))
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer srv.Close()

			var persisted []map[string]interface{}
			defer stubPersistJobTaskItems(t, &persisted, nil)()
			deps := *jobDeps
			rows := []map[string]interface{}{
				{
					"srcPath":  "/src/sub/",
					"dstPath":  "/dst/sub/",
					"fileName": "doc.txt",
					"fileSize": int64(10),
					"type":     taskItemTypeCopy.Int(),
					"isPath":   taskItemFile.Int(),
				},
			}
			deps.CountJobTaskItemsByStatuses = func(int64, []int) (int64, error) { return 1, nil }
			deps.ForEachJobTaskItemsByStatuses = func(_ int64, _ []int, _ int, visit func([]map[string]interface{}) error) error {
				return visit(rows)
			}
			defer SetJobDepsForTest(&deps)()

			jt := scanTestTask(t, srv.URL, srv.Client(), map[string]interface{}{
				"method":         0,
				"srcPath":        "/src/",
				"dstPath":        "/dst/",
				"conflictPolicy": tc.conflictPolicy,
				"trashEnable":    tc.trashEnable,
				"trashDir":       ".opensync-trash",
				"maxRetries":     0,
			})
			jt.RetrySourceTaskID = 99
			jt.RetryStatuses = []taskStatus{taskStatusFailed}
			jt.sync()

			waiting := jt.Waiting.snapshot()
			if len(waiting) != 1 {
				t.Fatalf("waiting count = %d, want 1", len(waiting))
			}
			item := waiting[0]
			if !item.CheckDstConflict {
				t.Fatal("retried copy item must have CheckDstConflict=true")
			}
			if item.DstRoot != "/dst/" {
				t.Fatalf("retried copy item DstRoot = %q, want /dst/", item.DstRoot)
			}
			item.DoIt()

			mu.Lock()
			defer mu.Unlock()
			if copyCalls != tc.wantCopyCalls {
				t.Fatalf("copyCalls = %d, want %d (status=%v err=%q)", copyCalls, tc.wantCopyCalls, item.status(), item.errorMessage())
			}
			if tc.wantOverwrite != nil && gotOverwrite != *tc.wantOverwrite {
				t.Fatalf("copy overwrite = %v, want %v", gotOverwrite, *tc.wantOverwrite)
			}
			if tc.wantSkipped {
				if !item.isSkipped() {
					t.Fatal("expected item to be marked skipped")
				}
				if len(persisted) != 0 {
					t.Fatalf("skipped conflict item should not persist failure: %+v", persisted)
				}
			}
			if tc.wantRename {
				if !isConflictBackupName(renamedTo) {
					t.Fatalf("renamedTo = %q, want *.conflict-* pattern", renamedTo)
				}
				if len(ops) < 2 || ops[0] != "rename" || ops[1] != "copy" {
					t.Fatalf("ops order = %v, want [rename copy]", ops)
				}
			}
			if tc.wantTrashMove {
				if !strings.HasPrefix(trashDstDir, "/dst/.opensync-trash/") {
					t.Fatalf("trashDstDir = %q, want under /dst/.opensync-trash/", trashDstDir)
				}
				if trashMoveOverwrite {
					t.Fatal("trash move must use Overwrite=false")
				}
				if len(ops) < 2 || ops[0] != "trash_move" || ops[1] != "copy" {
					t.Fatalf("ops order = %v, want [trash_move copy]", ops)
				}
			}
		})
	}
}

func boolPtr(v bool) *bool { return &v }

// Issue #4 (P1): Mirror mode must not treat an unreadable destination directory
// (e.g. HTTP 403) as an empty directory and enqueue unconditional copies.
func TestMirrorUnreadableDestinationDoesNotCopyAsEmpty(t *testing.T) {
	for _, tc := range []struct {
		name           string
		forbidPath     string
		conflictPolicy int
		wantQueued     []string
	}{
		{
			name:           "unreadable destination root with skip policy",
			forbidPath:     "/dst/",
			conflictPolicy: 1,
			wantQueued:     nil,
		},
		{
			name:           "unreadable destination root with overwrite policy",
			forbidPath:     "/dst/",
			conflictPolicy: 0,
			wantQueued:     nil,
		},
		{
			name:           "unreadable destination subdir skips only that subdir",
			forbidPath:     "/dst/sub/",
			conflictPolicy: 1,
			wantQueued:     []string{"root-only.txt"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := *jobDeps
			deps.ScanListRetryDelay = func(int) time.Duration { return 0 }
			defer SetJobDepsForTest(&deps)()
			var persisted []map[string]interface{}
			defer stubPersistJobTaskItems(t, &persisted, nil)()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				var req alistListRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				switch req.Path {
				case "/src/":
					_, _ = w.Write([]byte(`{"code":200,"data":{"total":2,"content":[{"name":"root-only.txt","size":10},{"name":"sub","is_dir":true}]}}`))
				case "/src/sub/":
					_, _ = w.Write([]byte(`{"code":200,"data":{"total":1,"content":[{"name":"secret.txt","size":10}]}}`))
				case "/dst/":
					if tc.forbidPath == "/dst/" {
						w.WriteHeader(http.StatusForbidden)
						_, _ = w.Write([]byte(`{"code":403,"message":"permission denied","data":null}`))
						return
					}
					_, _ = w.Write([]byte(`{"code":200,"data":{"total":1,"content":[{"name":"sub","is_dir":true}]}}`))
				case "/dst/sub/":
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte(`{"code":403,"message":"permission denied","data":null}`))
				default:
					t.Errorf("unexpected list path %q", req.Path)
					_, _ = w.Write([]byte(`{"code":200,"data":{"total":0,"content":[]}}`))
				}
			}))
			defer srv.Close()

			jt := scanTestTask(t, srv.URL, srv.Client(), map[string]interface{}{
				"method":         1,
				"srcPath":        "/src/",
				"dstPath":        "/dst/",
				"conflictPolicy": tc.conflictPolicy,
			})
			jt.sync()

			waiting := jt.Waiting.snapshot()
			var got []string
			for _, item := range waiting {
				got = append(got, item.FileName)
			}
			if len(got) != len(tc.wantQueued) {
				t.Fatalf("queued files = %v, want %v", got, tc.wantQueued)
			}
			for i := range got {
				if got[i] != tc.wantQueued[i] {
					t.Fatalf("queued[%d] = %q, want %q", i, got[i], tc.wantQueued[i])
				}
			}
		})
	}
}

// Issue #5 (P1) & Issue #10 (P2):
// - Concurrent trash dir creation must wait until remote Mkdir completes.
// - moveItemToTrash must NOT treat "object not found" as success when the
//   source file still exists, so overwrite cannot proceed without trashing.
// - Trashing the same file twice on the same day must use unique paths and
//   Overwrite=false so the second version never clobbers the first.
func TestTrashConcurrentMkdirMoveErrorAndSameDayVersioning(t *testing.T) {
	t.Run("concurrent mkdir singleflight blocks premature move", func(t *testing.T) {
		mkdirStarted := make(chan struct{})
		releaseMkdir := make(chan struct{})
		var mkdirDone atomic.Bool
		var prematureMove atomic.Bool
		var mkdirCount atomic.Int32
		var once sync.Once

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/fs/mkdir":
				var req struct {
					Path string `json:"path"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				// Hold the very first mkdir (.opensync-trash) to verify goroutine #2 waits.
				if strings.HasSuffix(strings.TrimRight(req.Path, "/"), "/.opensync-trash") {
					mkdirCount.Add(1)
					once.Do(func() {
						close(mkdirStarted)
						<-releaseMkdir
						mkdirDone.Store(true)
					})
				}
				_, _ = w.Write([]byte(`{"code":200,"data":{}}`))
			case "/api/fs/move":
				if !mkdirDone.Load() {
					prematureMove.Store(true)
				}
				_, _ = w.Write([]byte(`{"code":200,"data":{"tasks":[]}}`))
			default:
				t.Errorf("unexpected path %s", r.URL.Path)
			}
		}))
		defer srv.Close()

		jt := scanTestTask(t, srv.URL, srv.Client(), map[string]interface{}{
			"trashEnable": 1, "trashDir": ".opensync-trash",
		})
		var wg sync.WaitGroup
		errCh := make(chan error, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			errCh <- jt.moveItemToTrash("/dst/", "/dst/", "a.txt")
		}()
		select {
		case <-mkdirStarted:
		case <-time.After(3 * time.Second):
			close(releaseMkdir)
			t.Fatal("timed out waiting for root trash mkdir to start")
		}
		go func() {
			defer wg.Done()
			errCh <- jt.moveItemToTrash("/dst/", "/dst/", "b.txt")
		}()
		// Give goroutine #2 time to reach the singleflight wait.
		time.Sleep(30 * time.Millisecond)
		close(releaseMkdir)
		wg.Wait()
		close(errCh)

		for err := range errCh {
			if err != nil {
				t.Fatalf("moveItemToTrash error: %v", err)
			}
		}
		if prematureMove.Load() {
			t.Fatal("second goroutine issued /api/fs/move before /api/fs/mkdir finished")
		}
		if mkdirCount.Load() != 1 {
			t.Fatalf("root trash mkdir count = %d, want 1", mkdirCount.Load())
		}
	})

	t.Run("move object-not-found while file still exists aborts overwrite", func(t *testing.T) {
		var copyCalls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/fs/mkdir":
				_, _ = w.Write([]byte(`{"code":200,"data":{}}`))
			case "/api/fs/move":
				// Simulate target trash directory missing remotely.
				_, _ = w.Write([]byte(`{"code":500,"message":"dst dir object not found","data":null}`))
			case "/api/fs/get":
				// Original file still exists at destination!
				_, _ = w.Write([]byte(`{"code":200,"data":{"name":"keep.txt","is_dir":false,"size":10}}`))
			case "/api/fs/copy":
				copyCalls.Add(1)
				_, _ = w.Write([]byte(`{"code":200,"data":{"tasks":[]}}`))
			default:
				t.Errorf("unexpected path %s", r.URL.Path)
			}
		}))
		defer srv.Close()

		var persisted []map[string]interface{}
		defer stubPersistJobTaskItems(t, &persisted, nil)()
		jt := scanTestTask(t, srv.URL, srv.Client(), map[string]interface{}{
			"trashEnable": 1, "trashDir": ".opensync-trash", "maxRetries": 0,
		})
		item := newCopyItem(jt, jt.AlistClient, "/src/", "/dst/", "keep.txt", int64(10), taskItemTypeCopy)
		item.DstRoot = "/dst/"
		item.IsOverwrite = true
		item.DoIt()

		if item.status() != taskStatusFailed {
			t.Fatalf("status = %v, want taskStatusFailed when trash move fails", item.status())
		}
		if copyCalls.Load() != 0 {
			t.Fatalf("copyCalls = %d, want 0 (must not overwrite after trash move failure)", copyCalls.Load())
		}
	})

	t.Run("same-day repeated trash uses distinct version dirs and overwrite=false", func(t *testing.T) {
		var mu sync.Mutex
		var moves []alistCopyMoveRequest
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/fs/mkdir":
				_, _ = w.Write([]byte(`{"code":200,"data":{}}`))
			case "/api/fs/move":
				var req alistCopyMoveRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				mu.Lock()
				moves = append(moves, req)
				mu.Unlock()
				_, _ = w.Write([]byte(`{"code":200,"data":{"tasks":[]}}`))
			default:
				t.Errorf("unexpected path %s", r.URL.Path)
			}
		}))
		defer srv.Close()

		jt := scanTestTask(t, srv.URL, srv.Client(), map[string]interface{}{
			"trashEnable": 1, "trashDir": ".opensync-trash",
		})
		if err := jt.moveItemToTrash("/dst/", "/dst/docs/", "report.pdf"); err != nil {
			t.Fatalf("first moveItemToTrash error: %v", err)
		}
		if err := jt.moveItemToTrash("/dst/", "/dst/docs/", "report.pdf"); err != nil {
			t.Fatalf("second moveItemToTrash error: %v", err)
		}

		if len(moves) != 2 {
			t.Fatalf("move count = %d, want 2", len(moves))
		}
		if moves[0].Overwrite || moves[1].Overwrite {
			t.Fatalf("trash moves must set Overwrite=false: %+v", moves)
		}
		if moves[0].DstDir == moves[1].DstDir {
			t.Fatalf("same-day repeated trash collided on identical DstDir %q", moves[0].DstDir)
		}
	})
}

// Issue #6 (P1): Empty source dir cleanup after move must quarantine-rename
// before deleting, re-list the quarantined dir, restore newly arrived files,
// and never issue recursive /api/fs/remove directly on the live dir name.
func TestCleanupEmptyMoveSrcDirsQuarantinesAndRestoresNewFiles(t *testing.T) {
	t.Run("restores directory when new file arrives before deletion", func(t *testing.T) {
		var mu sync.Mutex
		var stageName string
		var restoredBack bool
		var removeCalls []string

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/fs/list":
				var req alistListRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				mu.Lock()
				curStage := stageName
				mu.Unlock()
				if curStage != "" && req.Path == "/src/"+curStage+"/" {
					// A downloader wrote a new file before the quarantine rename took effect!
					_, _ = w.Write([]byte(`{"code":200,"data":{"total":1,"content":[{"name":"fresh.bin","size":99}]}}`))
					return
				}
				_, _ = w.Write([]byte(`{"code":200,"data":{"total":0,"content":[]}}`))
			case "/api/fs/rename":
				var req struct {
					Path string `json:"path"`
					Name string `json:"name"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				mu.Lock()
				if req.Path == "/src/sub" && strings.HasPrefix(req.Name, emptySrcDirStagePrefix) {
					stageName = req.Name
				} else if stageName != "" && req.Path == "/src/"+stageName && req.Name == "sub" {
					restoredBack = true
				}
				mu.Unlock()
				_, _ = w.Write([]byte(`{"code":200,"data":{}}`))
			case "/api/fs/remove":
				var req struct {
					Dir   string   `json:"dir"`
					Names []string `json:"names"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				mu.Lock()
				removeCalls = append(removeCalls, req.Dir+strings.Join(req.Names, ","))
				mu.Unlock()
				_, _ = w.Write([]byte(`{"code":200,"data":{}}`))
			default:
				t.Errorf("unexpected path %s", r.URL.Path)
			}
		}))
		defer srv.Close()

		var persisted []map[string]interface{}
		defer stubPersistJobTaskItems(t, &persisted, nil)()
		jt := scanTestTask(t, srv.URL, srv.Client(), map[string]interface{}{
			"method": 2, "srcPath": "/src/", "dstPath": "/dst/", "deleteEmptySrcDirs": 1,
		})
		jt.moveSrcSubdirs = map[string]string{"/src/sub/": "/src/"}
		jt.cleanupEmptyMoveSrcDirs()

		mu.Lock()
		defer mu.Unlock()
		if len(removeCalls) != 0 {
			t.Fatalf("removeCalls = %v, want 0 when new file arrived", removeCalls)
		}
		if !restoredBack {
			t.Fatal("expected quarantined dir containing new file to be renamed back to /src/sub")
		}
	})

	t.Run("truly empty dir is removed only via its quarantined name", func(t *testing.T) {
		var mu sync.Mutex
		var stageName string
		var removedNames []string

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/fs/list":
				_, _ = w.Write([]byte(`{"code":200,"data":{"total":0,"content":[]}}`))
			case "/api/fs/rename":
				var req struct {
					Path string `json:"path"`
					Name string `json:"name"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				mu.Lock()
				if req.Path == "/src/sub" {
					stageName = req.Name
				}
				mu.Unlock()
				_, _ = w.Write([]byte(`{"code":200,"data":{}}`))
			case "/api/fs/remove":
				var req struct {
					Dir   string   `json:"dir"`
					Names []string `json:"names"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				mu.Lock()
				removedNames = append(removedNames, req.Names...)
				mu.Unlock()
				_, _ = w.Write([]byte(`{"code":200,"data":{}}`))
			default:
				t.Errorf("unexpected path %s", r.URL.Path)
			}
		}))
		defer srv.Close()

		var persisted []map[string]interface{}
		defer stubPersistJobTaskItems(t, &persisted, nil)()
		jt := scanTestTask(t, srv.URL, srv.Client(), map[string]interface{}{
			"method": 2, "srcPath": "/src/", "dstPath": "/dst/", "deleteEmptySrcDirs": 1,
		})
		jt.moveSrcSubdirs = map[string]string{"/src/sub/": "/src/"}
		jt.cleanupEmptyMoveSrcDirs()

		mu.Lock()
		defer mu.Unlock()
		if stageName == "" || !strings.HasPrefix(stageName, emptySrcDirStagePrefix) {
			t.Fatalf("stageName = %q, want %q prefix", stageName, emptySrcDirStagePrefix)
		}
		if len(removedNames) != 1 || removedNames[0] != stageName {
			t.Fatalf("removedNames = %v, want [%s] (never live 'sub')", removedNames, stageName)
		}
	})
}

// Issue #7 (P1) & Issue #9 (P2):
// - Cross-instance restore must NOT bind a job to an unrelated engine sharing
//   the same numeric ID when the backup engine was skipped.
// - Restoring a job with notifyMode=1 when all specified channels were skipped
//   must disable notifications (notifyMode=2), never broaden to all channels (0).
func TestBackupRestoreEngineMismatchAndNotifyScope(t *testing.T) {
	_, cleanup := setupFullFeatureTestDB(t)
	defer cleanup()
	isolateJobClientRegistryForTest(t)

	// Target instance already has engine ID=1 (different storage!) and notify ID=1.
	targetEngineID, err := mapper.AddAlistWithConcurrency("TargetStorage", "http://10.99.0.1:5244", "target", "target-tok", 1)
	if err != nil || targetEngineID != 1 {
		t.Fatalf("setup target engine: id=%d err=%v", targetEngineID, err)
	}
	targetNotifyID, err := mapper.AddNotify(map[string]interface{}{
		"enable": 1, "method": 1, "params": `{"sendKey":"unrelated-recipient"}`,
	})
	if err != nil {
		t.Fatalf("setup target notify: %v", err)
	}

	// Case 1 (Issue #7): redacted engine (different URL) skipped -> mirror job
	// with alistId=1 must be skipped, NOT bound to targetEngineID=1.
	backupWrongEngine := SystemBackupPayload{
		Version: backupFormatVersion,
		Engines: []map[string]interface{}{
			{"id": int64(1), "remark": "SourceStorage", "url": "http://10.1.0.1:5244", "userName": "src", "token": "****1234"},
		},
		Jobs: []map[string]interface{}{
			{
				"remark": "危险镜像", "alistId": int64(1), "enable": 1, "method": 1, "isCron": 2,
				"srcPath": `["/src"]`, "dstPath": `["/dst"]`,
			},
		},
	}
	summary, err := ImportSystemBackup(backupWrongEngine, "skip")
	if err != nil {
		t.Fatalf("ImportSystemBackup(wrongEngine) error: %v", err)
	}
	if summary["jobsCreated"] != 0 || summary["jobsSkipped"] != 1 {
		t.Fatalf("expected job skipped when engine skipped, got summary=%+v", summary)
	}
	jobs, _ := mapper.GetJobListAll()
	if len(jobs) != 0 {
		t.Fatalf("job should not have been bound to target engine ID 1: %+v", jobs)
	}

	// Case 2 (Issue #9): valid engine imports, but specified notify channel #5
	// is redacted and skipped -> job must be imported with notifyMode=2 (disabled),
	// never notifyMode=0 (which would broadcast to targetNotifyID).
	backupSkippedNotify := SystemBackupPayload{
		Version: backupFormatVersion,
		Engines: []map[string]interface{}{
			{"id": int64(9), "remark": "GoodEngine", "url": "http://10.2.0.1:5244", "userName": "admin", "token": "real-token"},
		},
		Notifications: []map[string]interface{}{
			{"id": int64(5), "enable": 1, "method": 6, "params": `{"botToken":"****9999","chatId":"42"}`},
		},
		Jobs: []map[string]interface{}{
			{
				"remark": "私密同步", "alistId": int64(9), "enable": 1, "method": 0, "isCron": 2,
				"srcPath": `["/private"]`, "dstPath": `["/backup"]`,
				"notifyMode": 1, "notifyIds": "[5]",
			},
		},
	}
	summary2, err := ImportSystemBackup(backupSkippedNotify, "skip")
	if err != nil {
		t.Fatalf("ImportSystemBackup(skippedNotify) error: %v", err)
	}
	if summary2["jobsCreated"] != 1 {
		t.Fatalf("expected job created, got summary=%+v", summary2)
	}
	jobs, _ = mapper.GetJobListAll()
	if len(jobs) != 1 {
		t.Fatalf("jobs count = %d, want 1", len(jobs))
	}
	if mode := int(jobs[0]["notifyMode"].(int64)); mode != 2 {
		t.Fatalf("imported notifyMode = %d, want 2 (disabled) instead of 0 (all channels)", mode)
	}
	allNotifies, _ := mapper.GetNotifyList(true)
	if len(allNotifies) == 0 || allNotifies[0]["id"].(int64) != targetNotifyID {
		t.Fatalf("expected existing enabled notify channel in DB, got %+v", allNotifies)
	}
	if delivered := filterNotifyListForJob(allNotifies, jobs[0]); len(delivered) != 0 {
		t.Fatalf("filterNotifyListForJob returned %+v, want empty so unrelated channel receives nothing", delivered)
	}
}

// Issue #8 (P1): Concurrent CreateJobClient and EditJobClient calls targeting
// the same destination directory must be serialized so only one succeeds.
func TestConcurrentJobSaveEnforcesDestinationConflict(t *testing.T) {
	_, cleanup := setupFullFeatureTestDB(t)
	defer cleanup()
	isolateJobClientRegistryForTest(t)

	alistID, err := mapper.AddAlistWithConcurrency("E1", "http://127.0.0.1:5244", "admin", "tok", 2)
	if err != nil {
		t.Fatalf("AddAlistWithConcurrency: %v", err)
	}

	// Concurrent CreateJobClient for two mirror jobs targeting /dst/.
	const workers = 6
	start := make(chan struct{})
	var created atomic.Int32
	var rejected atomic.Int32
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		idx := i
		go func() {
			defer wg.Done()
			<-start
			_, err := CreateJobClient(map[string]interface{}{
				"alistId": alistID,
				"remark":  fmt.Sprintf("并发镜像-%d", idx),
				"enable":  1,
				"method":  1,
				"isCron":  2,
				"srcPath": []string{fmt.Sprintf("/src-%d", idx)},
				"dstPath": []string{"/dst/"},
			})
			if err == nil {
				created.Add(1)
			} else {
				rejected.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if created.Load() != 1 || rejected.Load() != workers-1 {
		t.Fatalf("concurrent create: created=%d rejected=%d, want 1/%d", created.Load(), rejected.Load(), workers-1)
	}

	// Concurrent EditJobClient moving two separate jobs onto the same /shared-dst/.
	idA, err := CreateJobClient(map[string]interface{}{
		"alistId": alistID, "remark": "任务A", "enable": 1, "method": 1, "isCron": 2,
		"srcPath": []string{"/srcA"}, "dstPath": []string{"/dstA"},
	})
	if err != nil {
		t.Fatalf("create job A: %v", err)
	}
	idB, err := CreateJobClient(map[string]interface{}{
		"alistId": alistID, "remark": "任务B", "enable": 1, "method": 1, "isCron": 2,
		"srcPath": []string{"/srcB"}, "dstPath": []string{"/dstB"},
	})
	if err != nil {
		t.Fatalf("create job B: %v", err)
	}

	startEdit := make(chan struct{})
	var editOK, editFail atomic.Int32
	var editWG sync.WaitGroup
	editWG.Add(2)
	for _, pair := range []struct {
		id  int64
		src string
	}{{idA, "/srcA"}, {idB, "/srcB"}} {
		pair := pair
		go func() {
			defer editWG.Done()
			<-startEdit
			err := EditJobClient(map[string]interface{}{
				"id": pair.id, "alistId": alistID, "remark": "编辑后", "enable": 1, "method": 1, "isCron": 2,
				"srcPath": []string{pair.src}, "dstPath": []string{"/shared-dst/"},
			})
			if err == nil {
				editOK.Add(1)
			} else {
				editFail.Add(1)
			}
		}()
	}
	close(startEdit)
	editWG.Wait()

	if editOK.Load() != 1 || editFail.Load() != 1 {
		t.Fatalf("concurrent edit: ok=%d fail=%d, want 1/1", editOK.Load(), editFail.Load())
	}
}

// Issue #11 (P2): Exceeding 10,000 rows must neither fail CSV/TXT export nor
// silently zero out the Dashboard 7-day statistics.
func TestLargeDatasetExportAndDashboardOverviewExceeding10000Rows(t *testing.T) {
	testDB, cleanup := setupFullFeatureTestDB(t)
	defer cleanup()
	isolateJobClientRegistryForTest(t)

	alistID, err := mapper.AddAlistWithConcurrency("E1", "http://127.0.0.1:5244", "admin", "tok", 1)
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := mapper.AddJob(map[string]interface{}{
		"enable": 1, "remark": "大数据量测试", "srcPath": `["/src"]`, "dstPath": `["/dst"]`,
		"alistId": alistID, "method": 0, "isCron": 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	const count = 10005
	nowUnix := time.Now().Unix()
	if _, err := testDB.Exec(
		`WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < ?)
		 INSERT INTO job_task(jobId, status, runTime, taskNum, createTime)
		 SELECT ?, 2, ?, '{"allNum":1,"successNum":1,"failNum":0,"sumSize":10}', ? FROM c`,
		count, jobID, nowUnix, nowUnix,
	); err != nil {
		t.Fatalf("bulk insert job_task: %v", err)
	}
	var firstTaskID int64
	if err := testDB.QueryRow(`SELECT MIN(id) FROM job_task WHERE jobId = ?`, jobID).Scan(&firstTaskID); err != nil {
		t.Fatal(err)
	}
	if _, err := testDB.Exec(
		`WITH RECURSIVE c(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM c WHERE x < ?)
		 INSERT INTO job_task_item(taskId, srcPath, dstPath, isPath, fileName, fileSize, type, status, createTime)
		 SELECT ?, '/src/', '/dst/', 0, 'f-' || x || '.bin', 10, 0, 2, ? FROM c`,
		count, firstTaskID, nowUnix,
	); err != nil {
		t.Fatalf("bulk insert job_task_item: %v", err)
	}

	_, _, txtData, err := ExportTaskItems(map[string]interface{}{"taskId": firstTaskID}, "txt")
	if err != nil {
		t.Fatalf("ExportTaskItems(txt, %d rows) error: %v", count, err)
	}
	lines := strings.Split(strings.TrimSpace(string(txtData)), "\n")
	if len(lines) != count {
		t.Fatalf("exported txt lines = %d, want %d", len(lines), count)
	}

	overview, err := GetDashboardOverview()
	if err != nil {
		t.Fatalf("GetDashboardOverview error: %v", err)
	}
	recent, _ := overview["recent7d"].(map[string]interface{})
	if got := recent["totalRuns"]; got != int64(count) {
		t.Fatalf("recent7d.totalRuns = %v, want %d", got, count)
	}
	if got := recent["successRuns"]; got != int64(count) {
		t.Fatalf("recent7d.successRuns = %v, want %d", got, count)
	}
	if got := recent["totalBytes"]; got != int64(count*10) {
		t.Fatalf("recent7d.totalBytes = %v, want %d", got, count*10)
	}
}

// Issue #12 (P2): Retention cleanup must not delete an expired historical task
// that is currently registered as a JobClient's active retrySource.
func TestCleanupExpiredTasksProtectsActiveRetrySource(t *testing.T) {
	testDB, cleanup := setupFullFeatureTestDB(t)
	defer cleanup()
	isolateJobClientRegistryForTest(t)

	expiredUnix := time.Now().AddDate(0, 0, -60).Unix()
	tProtected, err := mapper.AddJobTask(1, expiredUnix)
	if err != nil {
		t.Fatal(err)
	}
	_ = mapper.UpdateJobTaskStatusAndNum(tProtected, taskStatusFailed.Int(), nil, `{"allNum":1,"failNum":1}`)
	_, _ = testDB.Exec(`UPDATE job_task SET runTime = ?, createTime = ? WHERE id = ?`, expiredUnix, expiredUnix, tProtected)
	_ = mapper.AddJobTaskItemMany([]map[string]interface{}{
		{"taskId": tProtected, "srcPath": "/src/", "dstPath": "/dst/", "isPath": 0, "fileName": "a.bin", "fileSize": int64(1), "type": 0, "status": 3},
	})
	tExpired, err := mapper.AddJobTask(1, expiredUnix)
	if err != nil {
		t.Fatal(err)
	}
	_ = mapper.UpdateJobTaskStatusAndNum(tExpired, taskStatusSuccess.Int(), nil, `{"allNum":1,"successNum":1}`)
	_, _ = testDB.Exec(`UPDATE job_task SET runTime = ?, createTime = ? WHERE id = ?`, expiredUnix, expiredUnix, tExpired)

	jc := &JobClient{Job: map[string]interface{}{"id": int64(1)}, retrySourceID: tProtected}
	jobClientListMu.Lock()
	jobClientList[1] = jc
	jobClientListMu.Unlock()

	cleanupExpiredTasksContext(context.Background(), log.New(io.Discard, "", 0), 7, time.Now())

	var keptTask, keptItems, deletedTask int
	_ = testDB.QueryRow(`SELECT COUNT(*) FROM job_task WHERE id = ?`, tProtected).Scan(&keptTask)
	_ = testDB.QueryRow(`SELECT COUNT(*) FROM job_task_item WHERE taskId = ?`, tProtected).Scan(&keptItems)
	_ = testDB.QueryRow(`SELECT COUNT(*) FROM job_task WHERE id = ?`, tExpired).Scan(&deletedTask)
	if keptTask != 1 || keptItems != 1 {
		t.Fatalf("protected retry source task=%d items=%d, want 1/1", keptTask, keptItems)
	}
	if deletedTask != 0 {
		t.Fatalf("unprotected expired task=%d, want 0", deletedTask)
	}
}

// Issue #16 (P2, backend): Mirror dry-run must enforce empty-source protection
// and bulk-delete safety valve identically to real execution.
func TestDryRunMirrorRespectsEmptySourceAndBulkDeleteSafetyValve(t *testing.T) {
	t.Run("empty source root protects destination entries as skip", func(t *testing.T) {
		listFn := func(dir string, isSrc bool) (FileListResult, bool, error) {
			if isSrc {
				return FileListResult{}, true, nil
			}
			return FileListResult{
				"a.txt": {Size: 10, Modified: 1000},
				"b.txt": {Size: 20, Modified: 1000},
				"c.txt": {Size: 30, Modified: 1000},
			}, true, nil
		}
		sess := &dryRunSession{result: DryRunResult{JobID: 1, Status: "running", Method: 1, Items: []DryRunItem{}}}
		if err := dryRunFullSyncWork(context.Background(), sess, map[string]interface{}{"method": 1}, scanWork{SrcPath: "/src/", DstPath: "/dst/"}, nil, nil, listFn); err != nil {
			t.Fatalf("dryRunFullSyncWork error: %v", err)
		}
		snap := sess.snapshot()
		if snap.DeleteCount != 0 {
			t.Fatalf("DeleteCount = %d, want 0 under empty-source guard (items=%+v)", snap.DeleteCount, snap.Items)
		}
		if snap.SkipCount != 3 {
			t.Fatalf("SkipCount = %d, want 3", snap.SkipCount)
		}
		for _, item := range snap.Items {
			if item.Action != "skip" || !strings.Contains(item.Reason, "空源保护") {
				t.Fatalf("unexpected item under empty-source guard: %+v", item)
			}
		}
	})

	t.Run("bulk delete safety valve protects destination entries as skip", func(t *testing.T) {
		dstFiles := FileListResult{"keep.txt": {Size: 10, Modified: 1000}}
		for i := 0; i < 105; i++ {
			dstFiles[fmt.Sprintf("extra-%03d.txt", i)] = FileMetadata{Size: 10, Modified: 1000}
		}
		listFn := func(dir string, isSrc bool) (FileListResult, bool, error) {
			if isSrc {
				return FileListResult{"keep.txt": {Size: 10, Modified: 1000}}, true, nil
			}
			return dstFiles, true, nil
		}
		sess := &dryRunSession{result: DryRunResult{JobID: 1, Status: "running", Method: 1, Items: []DryRunItem{}}}
		if err := dryRunFullSyncWork(context.Background(), sess, map[string]interface{}{"method": 1}, scanWork{SrcPath: "/src/", DstPath: "/dst/"}, nil, nil, listFn); err != nil {
			t.Fatalf("dryRunFullSyncWork error: %v", err)
		}
		snap := sess.snapshot()
		if snap.DeleteCount != 0 {
			t.Fatalf("DeleteCount = %d, want 0 under bulk-delete safety valve", snap.DeleteCount)
		}
		if snap.SkipCount != 105 {
			t.Fatalf("SkipCount = %d, want 105", snap.SkipCount)
		}
		if len(snap.Items) == 0 || !strings.Contains(snap.Items[0].Reason, "批量删除保护") {
			t.Fatalf("expected bulk-delete protection reason, got %+v", snap.Items[:1])
		}
	})
}
