package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"opensync/internal/mapper"
	"opensync/internal/msg"
)

// swapAlistClientCache gives a test private client/load maps and restores the
// package state afterwards.
func swapAlistClientCache(t *testing.T) {
	t.Helper()
	alistClientListMu.Lock()
	oldList, oldLoads := alistClientList, alistClientLoads
	alistClientList = make(map[int64]*AlistClient)
	alistClientLoads = make(map[int64]*alistClientLoad)
	alistClientListMu.Unlock()
	t.Cleanup(func() {
		alistClientListMu.Lock()
		alistClientList, alistClientLoads = oldList, oldLoads
		alistClientListMu.Unlock()
	})
}

// The shared client load must not inherit the first caller's cancellation: a
// waiter with a healthy context has to receive the client even though the
// request that started the load was aborted.
func TestGetClientByIDOwnerCancelDoesNotFailWaiters(t *testing.T) {
	swapAlistClientCache(t)
	release := make(chan struct{})
	started := make(chan struct{})
	d := *alistDeps
	d.GetAlistByID = func(int64) (map[string]interface{}, error) {
		return map[string]interface{}{"url": "https://example.test", "token": "t"}, nil
	}
	d.NewAlistClientCtx = func(ctx context.Context, alistURL, token string, alistID int64) (*AlistClient, error) {
		close(started)
		select {
		case <-release:
			return &AlistClient{URL: alistURL, AlistID: alistID}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	t.Cleanup(SetAlistDepsForTest(&d))

	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	ownerErr := make(chan error, 1)
	go func() {
		_, err := GetClientByIDContext(ownerCtx, 3)
		ownerErr <- err
	}()
	<-started

	waiterResult := make(chan *AlistClient, 1)
	waiterErr := make(chan error, 1)
	go func() {
		client, err := GetClientByIDContext(context.Background(), 3)
		waiterResult <- client
		waiterErr <- err
	}()
	// Give the waiter time to join the in-flight load before the owner aborts.
	time.Sleep(20 * time.Millisecond)
	cancelOwner()
	if err := <-ownerErr; err == nil {
		t.Fatal("owner GetClientByIDContext() error = nil, want its own cancellation")
	}
	close(release)

	if err := <-waiterErr; err != nil {
		t.Fatalf("waiter GetClientByIDContext() error = %v, want the shared load to succeed", err)
	}
	if client := <-waiterResult; client == nil {
		t.Fatal("waiter got a nil client")
	}
}

// A load that read the engine row before RemoveClient deleted it must not
// cache a client for the removed id.
func TestAlistClientLoadDiscardedAfterRemoval(t *testing.T) {
	swapAlistClientCache(t)
	load, owner := beginAlistClientLoad(11)
	if !owner {
		t.Fatal("beginAlistClientLoad() owner = false, want the first caller to own the load")
	}
	markAlistClientRemoved(11)

	transport := &closeTrackingTransport{}
	built := &AlistClient{AlistID: 11, client: &http.Client{Transport: transport}}
	finishAlistClientLoad(11, load, built, nil)

	alistClientListMu.RLock()
	_, cached := alistClientList[11]
	alistClientListMu.RUnlock()
	if cached {
		t.Fatal("client for a removed engine was cached")
	}
	if load.err == nil || load.err.Error() != msg.T(msg.AlistNotFound) {
		t.Fatalf("load.err = %v, want AlistNotFound", load.err)
	}
	if !transport.closed.Load() {
		t.Fatal("discarded client was not closed")
	}
}

// A DB failure after validation (here: UNIQUE on url) must close the client
// that will never be cached instead of leaking its transport.
func TestAddClientClosesClientWhenInsertFails(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()
	if _, err := testDB.Exec(`CREATE TABLE alist_list(
		id integer primary key autoincrement, remark text, url text UNIQUE, userName text, token text)`); err != nil {
		t.Fatalf("create alist_list: %v", err)
	}
	if _, err := testDB.Exec("INSERT INTO alist_list(remark, url, userName, token) VALUES ('', 'https://dup.example', 'u', 't')"); err != nil {
		t.Fatalf("insert alist: %v", err)
	}
	t.Cleanup(mapper.SetDBForTest(testDB))

	transport := &closeTrackingTransport{}
	d := *alistDeps
	d.NewAlistClient = func(alistURL, token string, alistID int64) (*AlistClient, error) {
		return &AlistClient{URL: alistURL, User: "u", client: &http.Client{Transport: transport}}, nil
	}
	t.Cleanup(SetAlistDepsForTest(&d))

	err = AddClient(map[string]interface{}{"url": "https://dup.example", "token": "t2"})
	if err == nil {
		t.Fatal("AddClient() error = nil, want the UNIQUE conflict")
	}
	if !transport.closed.Load() {
		t.Fatal("validated client was not closed after the insert failed")
	}
}

// When the driver clamps the page size, later pages must request the clamped
// size the page count was derived from. A server that honours per_page for
// offsets would otherwise skip entries between pages.
func TestFileListApiContextRequestsClampedPageSizeForLaterPages(t *testing.T) {
	d := *alistDeps
	d.FileListPageSize = 10
	t.Cleanup(SetAlistDepsForTest(&d))

	const total, clamp = 6, 2
	var mu sync.Mutex
	perPages := map[int]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req alistListRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode list request: %v", err)
			return
		}
		mu.Lock()
		perPages[req.Page] = req.PerPage
		mu.Unlock()
		size := req.PerPage
		if size > clamp {
			size = clamp
		}
		// Offsets follow the requested per_page, the content is clamped.
		start := (req.Page - 1) * req.PerPage
		items := []string{}
		for i := start; i < start+size && i < total; i++ {
			items = append(items, `{"name":"f`+strconv.Itoa(i)+`","is_dir":false,"size":1}`)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"content":[` + strings.Join(items, ",") + `],"total":6}}`))
	}))
	defer server.Close()

	client := &AlistClient{URL: server.URL, client: server.Client()}
	files, err := client.FileListApiContext(context.Background(), "/tv", 0, 0)
	if err != nil {
		t.Fatalf("FileListApiContext() error = %v", err)
	}
	if len(files) != total {
		t.Fatalf("FileListApiContext() returned %d entries, want %d", len(files), total)
	}
	mu.Lock()
	defer mu.Unlock()
	for page := 2; page <= total/clamp; page++ {
		if perPages[page] != clamp {
			t.Fatalf("page %d requested per_page=%d, want the clamped %d", page, perPages[page], clamp)
		}
	}
}

// An undone task without an id must not be indexed under "<nil>".
func TestFetchUndoneByTypeSkipsTasksWithoutID(t *testing.T) {
	client := &copyItemTestClient{undone: []map[string]interface{}{
		{"id": nil, "state": 1},
		{"state": 1},
		{"id": "task-1", "state": 1},
	}}
	monitor, watch := newCopyMonitorWatch(client)
	byType := monitor.fetchUndoneByType([]*copyTaskWatch{watch})
	tasks := byType[taskItemTypeCopy]
	if _, ok := tasks["<nil>"]; ok {
		t.Fatal(`task without an id was indexed as "<nil>"`)
	}
	if len(tasks) != 1 {
		t.Fatalf("indexed %d tasks, want only task-1", len(tasks))
	}
}

// stoppingCopyRuntime reports a user stop once the transfer request has been
// sent, mirroring requestBreak cancelling the task context mid-request.
type stoppingCopyRuntime struct {
	copyItemTestRuntime
	stopped bool
}

func (r *stoppingCopyRuntime) isBreak() bool { return r.stopped }

func (r *stoppingCopyRuntime) waitForBreak(time.Duration) bool { return !r.stopped }

type stopDuringTransferClient struct {
	*copyItemTestClient
	runtime *stoppingCopyRuntime
}

func (c stopDuringTransferClient) CopyFileContext(context.Context, string, string, string) (string, error) {
	c.copyCalls++
	c.runtime.stopped = true
	return "", context.Canceled
}

// A copy request cancelled by a user stop may already have created the AList
// task. It must be found in the undone list and cancelled and deleted, not
// left transferring while the item reads "stopped".
func TestCopyItemStopMidRequestCancelsOrphanedRemoteTask(t *testing.T) {
	pinCopyTestDeps(t)
	runtime := &stoppingCopyRuntime{}
	base := &copyItemTestClient{undone: []map[string]interface{}{
		{"id": "orphan-1", "name": "copy [/src](/file.txt) to [/dst](/)"},
	}}
	client := stopDuringTransferClient{copyItemTestClient: base, runtime: runtime}
	item := newCopyItem(runtime, client, "/src", "/dst", "file.txt", int64(1), taskItemTypeCopy)

	item.DoIt()

	if status := item.status(); status != taskStatusStopped {
		t.Fatalf("status = %d, want stopped", status)
	}
	if base.undoneCalls != 1 {
		t.Fatalf("undoneCalls = %d, want 1 lookup for the orphaned task", base.undoneCalls)
	}
	if base.cancelCalls != 1 || base.deleteCalls != 1 {
		t.Fatalf("cancel/delete calls = %d/%d, want the orphaned task cancelled and deleted", base.cancelCalls, base.deleteCalls)
	}
	if got := item.taskID(); got != "orphan-1" {
		t.Fatalf("taskID = %q, want the adopted orphan id", got)
	}
}

// Without an exact match nothing is cancelled: an unrelated task must never be
// touched.
func TestCopyItemStopMidRequestLeavesUnrelatedTasks(t *testing.T) {
	pinCopyTestDeps(t)
	runtime := &stoppingCopyRuntime{}
	base := &copyItemTestClient{undone: []map[string]interface{}{
		{"id": "other", "name": "copy [/src](/another.txt) to [/dst](/)"},
	}}
	client := stopDuringTransferClient{copyItemTestClient: base, runtime: runtime}
	item := newCopyItem(runtime, client, "/src", "/dst", "file.txt", int64(1), taskItemTypeCopy)

	item.DoIt()

	if base.cancelCalls != 0 || base.deleteCalls != 0 {
		t.Fatalf("cancel/delete calls = %d/%d, want unrelated tasks left alone", base.cancelCalls, base.deleteCalls)
	}
}

// After a 404 the destination of a copy may still hold the previous version
// with the same size (Overwrite:true). Only a destination written since the
// submission counts as success.
func TestCopyMonitorPollTaskInfo404RejectsStaleSameSizeCopy(t *testing.T) {
	submittedAt := time.Now().Unix()
	client := &copyItemTestClient{
		taskInfoFn: func(int) (map[string]interface{}, error) {
			return nil, &alistStatusError{httpStatus: 404}
		},
	}
	statClient := statCopyItemTestClient{copyItemTestClient: client, statFn: func(dir, name string) (FileStat, error) {
		if dir == "/src" {
			return FileStat{Exists: true, SizeKnown: true, Size: 1024, Modified: submittedAt - 10}, nil
		}
		return FileStat{Exists: true, SizeKnown: true, Size: 1024, Modified: submittedAt - 3600}, nil
	}}
	monitor, watch := newCopyMonitorWatch(statClient, func(ci *CopyItem) {
		ci.FileSize = int64(1024)
		ci.setSubmittedAt(submittedAt)
	})

	monitor.pollTaskInfo(watch)
	if status := watch.ci.status(); status != taskStatusFailed {
		t.Fatalf("status = %d, want failed for a stale same-size destination", status)
	}
}

func TestCopyMonitorPollTaskInfo404AcceptsFreshCopy(t *testing.T) {
	submittedAt := time.Now().Unix()
	client := &copyItemTestClient{
		stat: &FileStat{Exists: true, SizeKnown: true, Size: 1024, Modified: submittedAt + 5},
		taskInfoFn: func(int) (map[string]interface{}, error) {
			return nil, &alistStatusError{httpStatus: 404}
		},
	}
	monitor, watch := newCopyMonitorWatch(statCopyItemTestClient{copyItemTestClient: client}, func(ci *CopyItem) {
		ci.FileSize = int64(1024)
		ci.setSubmittedAt(submittedAt)
	})

	monitor.pollTaskInfo(watch)
	if status := watch.ci.status(); status != taskStatusSuccess {
		t.Fatalf("status = %d, want success for a destination written after submission", status)
	}
}

// HTTP 401 (not only AList's business code 401) is an auth failure and evicts
// the cached client.
func TestHTTP401EvictsMatchingClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	swapAlistClientCache(t)
	client := &AlistClient{AlistID: 78, URL: server.URL, client: server.Client()}
	alistClientListMu.Lock()
	alistClientList[78] = client
	alistClientListMu.Unlock()

	_, err := client.GetContext(context.Background(), "/api/fs/get", nil)
	if err == nil || !strings.Contains(err.Error(), msg.T(msg.AlistUnAuth)) {
		t.Fatalf("GetContext() error = %v, want AlistUnAuth", err)
	}
	var statusErr *alistStatusError
	if errors.As(err, &statusErr) {
		t.Fatal("HTTP 401 surfaced as a status error instead of the auth error")
	}
	alistClientListMu.RLock()
	_, cached := alistClientList[78]
	alistClientListMu.RUnlock()
	if cached {
		t.Fatal("HTTP 401 did not evict the cached client")
	}
}

// A listing reservation (interval/N) must not prune a full-interval mkdir
// reservation on the same bucket early.
func TestCheckWaitPruneKeepsLongerReservation(t *testing.T) {
	client := &AlistClient{}
	now := time.Now()
	client.waits = map[string]alistWaitSlot{
		"long":  {at: now.Add(-500 * time.Millisecond), interval: 2 * time.Second},
		"short": {at: now.Add(-500 * time.Millisecond), interval: 100 * time.Millisecond},
	}
	client.mu.Lock()
	client.pruneWaitsLocked(now)
	_, keptLong := client.waits["long"]
	_, keptShort := client.waits["short"]
	client.mu.Unlock()
	if !keptLong {
		t.Fatal("reservation still inside its own interval was pruned")
	}
	if keptShort {
		t.Fatal("expired reservation was kept")
	}
}
