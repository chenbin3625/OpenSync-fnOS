package service

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"opensync/internal/config"
	"opensync/internal/mapper"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestCopyQueueStatsTracksWaitTotals(t *testing.T) {
	queue := newCopyQueueWithCapacity(10)
	queue.pushWait(context.Background(), &CopyItem{FileName: "a.txt", FileSize: int64(10), CopyType: taskItemTypeCopy})
	queue.pushWait(context.Background(), &CopyItem{FileName: "b.txt", FileSize: int64(20), CopyType: taskItemTypeCopy})

	count, size := queue.stats()
	if count != 2 || size != 30 {
		t.Fatalf("stats = count %d size %d, want 2/30", count, size)
	}

	if _, ok := queue.pop(); !ok {
		t.Fatal("pop() returned false")
	}
	count, size = queue.stats()
	if count != 1 || size != 20 {
		t.Fatalf("stats after pop = count %d size %d, want 1/20", count, size)
	}
}

func TestCopyQueueKeepsFIFOAndCompacts(t *testing.T) {
	queue := newCopyQueue()

	for i := 0; i < 128; i++ {
		queue.pushWait(context.Background(), &CopyItem{FileName: string(rune('a' + i%26))})
	}

	for i := 0; i < 96; i++ {
		item, ok := queue.pop()
		if !ok {
			t.Fatalf("pop(%d) returned empty queue", i)
		}
		if item.FileName != string(rune('a'+i%26)) {
			t.Fatalf("pop(%d) = %q, want %q", i, item.FileName, string(rune('a'+i%26)))
		}
	}

	if queue.len() != 32 {
		t.Fatalf("queue.len() = %d, want 32", queue.len())
	}
	if len(queue.items) > 64 {
		t.Fatalf("queue retained %d backing items after many pops, want compacted queue", len(queue.items))
	}

	for i := 128; i < 160; i++ {
		queue.pushWait(context.Background(), &CopyItem{FileName: string(rune('a' + i%26))})
	}

	for i := 96; i < 160; i++ {
		item, ok := queue.pop()
		if !ok {
			t.Fatalf("pop(%d) returned empty queue", i)
		}
		if item.FileName != string(rune('a'+i%26)) {
			t.Fatalf("pop(%d) = %q, want %q", i, item.FileName, string(rune('a'+i%26)))
		}
	}
	if _, ok := queue.pop(); ok {
		t.Fatalf("pop() on empty queue returned an item")
	}
}

func TestCopyQueueRejectsPushWhenCapacityIsFull(t *testing.T) {
	queue := newCopyQueueWithCapacity(1)

	if ok := queue.pushWait(context.Background(), &CopyItem{FileName: "one.txt"}); !ok {
		t.Fatalf("first push returned false, want true")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if ok := queue.pushWait(ctx, &CopyItem{FileName: "two.txt"}); ok {
		t.Fatalf("second push returned true for full bounded queue, want false")
	}

	item, ok := queue.pop()
	if !ok {
		t.Fatalf("pop() returned empty queue")
	}
	if item.FileName != "one.txt" {
		t.Fatalf("pop() = %q, want one.txt", item.FileName)
	}
	if ok := queue.pushWait(context.Background(), &CopyItem{FileName: "two.txt"}); !ok {
		t.Fatalf("push after pop returned false, want true")
	}
}

func TestDefaultCopyQueueIsUnboundedForRealtimeTaskList(t *testing.T) {
	queue := newCopyQueue()
	const legacyQueuedCopyItemLimit = 5000

	for i := 0; i < legacyQueuedCopyItemLimit; i++ {
		if ok := queue.pushWait(context.Background(), &CopyItem{FileName: "queued.txt"}); !ok {
			t.Fatalf("push %d returned false, want true", i)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if ok := queue.pushWait(ctx, &CopyItem{FileName: "beyond-legacy-limit.txt"}); !ok {
		t.Fatalf("push beyond legacy 5000 item limit returned false, want true")
	}
	if got := queue.len(); got != legacyQueuedCopyItemLimit+1 {
		t.Fatalf("queue.len() = %d, want %d", got, legacyQueuedCopyItemLimit+1)
	}
}

func TestJobTaskRuntimeInitializationIsConcurrentSafe(t *testing.T) {
	jt := &JobTask{TaskID: 1}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				jt.initRuntime()
				_ = jt.context()
				if j%10 == 0 {
					jt.requestBreak()
				}
			}
		}()
	}
	wg.Wait()

	if jt.Waiting == nil {
		t.Fatalf("Waiting queue was not initialized")
	}
	if jt.Doing == nil {
		t.Fatalf("Doing map was not initialized")
	}
	if jt.context().Err() == nil {
		t.Fatalf("context was not canceled after requestBreak")
	}
}

func TestRuntimeTaskLimitsUseConfiguredValues(t *testing.T) {
	limits := taskRuntimeLimitsFromServer(config.ServerConfig{
		Timeout:         72,
		CopyConcurrency: 7,
		ScanConcurrency: 20,
		MaxRetries:      4,
	})

	if limits.CopyConcurrency != 7 {
		t.Fatalf("CopyConcurrency = %d, want 7", limits.CopyConcurrency)
	}
	if limits.ScanConcurrency != 20 {
		t.Fatalf("ScanConcurrency = %d, want 20", limits.ScanConcurrency)
	}
	if limits.MaxRetries != 4 {
		t.Fatalf("MaxRetries = %d, want 4", limits.MaxRetries)
	}
}

func TestRuntimeTaskLimitsClampInvalidConfiguredValues(t *testing.T) {
	limits := taskRuntimeLimitsFromServer(config.ServerConfig{
		CopyConcurrency: 0,
		ScanConcurrency: 99,
		MaxRetries:      99,
	})

	if limits.CopyConcurrency != config.DefaultCopyConcurrency {
		t.Fatalf("CopyConcurrency = %d, want default %d", limits.CopyConcurrency, config.DefaultCopyConcurrency)
	}
	if limits.ScanConcurrency != config.MaxScanConcurrency {
		t.Fatalf("ScanConcurrency = %d, want max %d", limits.ScanConcurrency, config.MaxScanConcurrency)
	}
	if limits.MaxRetries != config.MaxRetryAttempts {
		t.Fatalf("MaxRetries = %d, want max %d", limits.MaxRetries, config.MaxRetryAttempts)
	}
}

func TestJobTaskItemMapPreservesCopyAndDeletePayloadContract(t *testing.T) {
	copyErr := "copy failed"
	copyItem := JobTaskItem{
		TaskID:      42,
		SrcPath:     "/src/",
		DstPath:     "/dst/",
		IsPath:      taskItemFile,
		FileName:    "file.txt",
		FileSize:    int64(1024),
		Type:        taskItemTypeMove,
		AlistTaskID: "copy-1",
		Status:      taskStatusFailed,
		ErrMsg:      &copyErr,
		CreateTime:  100,
	}

	copyMap := copyItem.ToMap()

	if copyMap["taskId"] != int64(42) || copyMap["srcPath"] != "/src/" || copyMap["dstPath"] != "/dst/" {
		t.Fatalf("copy map paths = %#v", copyMap)
	}
	if copyMap["isPath"] != taskItemFile.Int() || copyMap["fileName"] != "file.txt" || copyMap["fileSize"] != int64(1024) {
		t.Fatalf("copy map file fields = %#v", copyMap)
	}
	if copyMap["type"] != taskItemTypeMove.Int() || copyMap["alistTaskId"] != "copy-1" {
		t.Fatalf("copy map type/task id fields = %#v", copyMap)
	}
	if copyMap["status"] != taskStatusFailed.Int() || copyMap["errMsg"] != &copyErr || copyMap["createTime"] != int64(100) {
		t.Fatalf("copy map status fields = %#v", copyMap)
	}

	deleteItem := NewDeleteJobTaskItem(42, "/dst/", "old.txt", int64(2048), taskStatusSuccess, nil, taskItemFile, 101)
	deleteMap := deleteItem.ToMap()

	if deleteMap["taskId"] != int64(42) || deleteMap["srcPath"] != nil || deleteMap["dstPath"] != "/dst/" {
		t.Fatalf("delete map paths = %#v", deleteMap)
	}
	if deleteMap["type"] != taskItemTypeDelete.Int() || deleteMap["alistTaskId"] != nil {
		t.Fatalf("delete map type/task id fields = %#v", deleteMap)
	}
	if deleteMap["status"] != taskStatusSuccess.Int() || deleteMap["createTime"] != int64(101) {
		t.Fatalf("delete map status fields = %#v", deleteMap)
	}
}

func TestCopyItemMapUsesTaskItemContractAndKeepsRealtimeProgress(t *testing.T) {
	errMsg := "copy failed"
	item := &CopyItem{
		SrcPath:     "/src/",
		DstPath:     "/dst/",
		FileName:    "file.txt",
		FileSize:    int64(1024),
		CopyType:    taskItemTypeCopy,
		AlistTaskID: "copy-1",
		Status:      taskStatusRunning,
		Progress:    35,
		ErrMsg:      &errMsg,
		CreateTime:  100,
	}

	itemMap := item.ToMap(42)

	if itemMap["taskId"] != int64(42) || itemMap["srcPath"] != "/src/" || itemMap["dstPath"] != "/dst/" {
		t.Fatalf("copy item map paths = %#v", itemMap)
	}
	if itemMap["isPath"] != taskItemFile.Int() || itemMap["fileName"] != "file.txt" || itemMap["fileSize"] != int64(1024) {
		t.Fatalf("copy item map file fields = %#v", itemMap)
	}
	if itemMap["type"] != taskItemTypeCopy.Int() || itemMap["alistTaskId"] != "copy-1" {
		t.Fatalf("copy item map type/task id fields = %#v", itemMap)
	}
	if itemMap["status"] != taskStatusRunning.Int() || itemMap["errMsg"] != &errMsg || itemMap["createTime"] != int64(100) {
		t.Fatalf("copy item map status fields = %#v", itemMap)
	}
	if itemMap["progress"] != float64(35) {
		t.Fatalf("copy item map progress = %#v, want 35", itemMap["progress"])
	}
}

func TestCopyHookPersistsFinishedItemsImmediately(t *testing.T) {
	var persisted []map[string]interface{}
	var batchSizes []int
	restorePersist := stubPersistJobTaskItems(t, &persisted, func(items []map[string]interface{}) {
		batchSizes = append(batchSizes, len(items))
	})
	defer restorePersist()

	jt := &JobTask{
		TaskID: 42,
	}
	jt.initRuntime()

	for i := 0; i < 3; i++ {
		jt.CopyHook("/src/", "/dst/", "file.txt", int64(10), "", taskStatusSuccess, nil, taskItemFile, taskItemTypeCopy, int64(100+i))
	}
	if err := jt.flushPersistBuffer(); err != nil {
		t.Fatalf("flushPersistBuffer() error: %v", err)
	}

	if len(persisted) != 3 {
		t.Fatalf("persisted len = %d, want 3", len(persisted))
	}
	if len(batchSizes) != 1 || batchSizes[0] != 3 {
		t.Fatalf("persist batch sizes = %v, want [3]", batchSizes)
	}
	if persisted[0]["createTime"] != int64(100) {
		t.Fatalf("first persisted createTime = %v, want 100", persisted[0]["createTime"])
	}
	count, size := jt.finishedAggregateForStatus(taskStatusSuccess)
	if count != 3 || size != 30 {
		t.Fatalf("finished aggregate = count %d size %d, want 3/30", count, size)
	}
}

func TestCopyHookRecordsPersistenceError(t *testing.T) {
	writeErr := errors.New("write failed")
	d := *jobDeps
	d.PersistJobTaskItems = func([]map[string]interface{}) error {
		return writeErr
	}
	restore := SetJobDepsForTest(&d)
	defer restore()

	jt := &JobTask{
		TaskID: 42,
	}
	jt.initRuntime()

	jt.CopyHook("/src/", "/dst/", "file.txt", int64(10), "", taskStatusSuccess, nil, taskItemFile, taskItemTypeCopy, 100)

	if err := jt.persistRemainingTaskItems(); !errors.Is(err, writeErr) {
		t.Fatalf("persistRemainingTaskItems() = %v, want write error", err)
	}
	if !jt.isBreak() {
		t.Fatalf("CopyHook did not request break after persistence error")
	}
	jt.persistBufMu.Lock()
	buffered := len(jt.persistBuffer)
	jt.persistBufMu.Unlock()
	if buffered != 1 {
		t.Fatalf("persistBuffer len = %d after failed flush, want 1 (items requeued)", buffered)
	}
}

func TestFinishSubmittedTaskMarksTaskFailedWhenPersistenceFails(t *testing.T) {
	writeErr := errors.New("write failed")

	testDB := newServiceTaskStatusTestDB(t)
	oldDB := mapperDBForServiceTest(testDB)
	defer oldDB()

	oldQueue := queueTaskNotification
	queueTaskNotification = func(int64, int, map[string]interface{}, int, float64) {}
	defer func() { queueTaskNotification = oldQueue }()

	client := &JobClient{}
	client.tryMarkDoing()
	jt := &JobTask{
		TaskID:     10,
		JobClient:  client,
		CreateTime: float64(time.Now().Unix()),
	}
	jt.initRuntime()
	jt.recordTaskPersistenceError(writeErr)
	jt.ScanFinish.Store(true)
	client.setCurrentTask(jt)

	persistErr := jt.persistRemainingTaskItems()
	jt.finishSubmittedTask(persistErr)

	status, errMsg := readServiceTaskStatus(t, testDB, 10)
	if status != taskStatusSystemFailed.Int() {
		t.Fatalf("task status = %d, want 6 after persistence failure", status)
	}
	if !strings.Contains(errMsg, "write failed") {
		t.Fatalf("errMsg = %q, want persistence error", errMsg)
	}
}

func TestUpdateTaskStatusReturnsPersistenceErrors(t *testing.T) {
	testDB := newServiceTaskStatusTestDB(t)
	oldDB := mapperDBForServiceTest(testDB)
	defer oldDB()
	if err := testDB.Close(); err != nil {
		t.Fatalf("close test DB: %v", err)
	}

	jt := &JobTask{
		TaskID:     10,
		CreateTime: float64(time.Now().Unix()),
		Waiting:    newCopyQueue(),
	}
	jt.initRuntime()
	jt.ScanFinish.Store(true)

	_, _, _, err := jt.updateTaskStatus()
	if err == nil {
		t.Fatalf("updateTaskStatus() error = nil, want database write error")
	}
}

func newServiceTaskStatusTestDB(t *testing.T) *sql.DB {
	t.Helper()
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	testDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		_ = testDB.Close()
	})

	if _, err := testDB.Exec(`CREATE TABLE job_task(
		id integer primary key autoincrement,
		jobId integer,
		status integer DEFAULT 1,
		errMsg text,
		runTime integer,
		taskNum text,
		createTime integer DEFAULT 1
	)`); err != nil {
		t.Fatalf("create job_task: %v", err)
	}
	if _, err := testDB.Exec(`CREATE TABLE job_task_item(
		id integer primary key autoincrement,
		taskId integer,
		srcPath text,
		dstPath text,
		isPath integer,
		fileName text,
		fileSize integer,
		type integer,
		alistTaskId text,
		status integer,
		progress real,
		errMsg text,
		createTime integer DEFAULT 1
	)`); err != nil {
		t.Fatalf("create job_task_item: %v", err)
	}
	if _, err := testDB.Exec("INSERT INTO job_task(id, jobId, status, runTime) VALUES (10, 1, 1, 100)"); err != nil {
		t.Fatalf("insert job_task: %v", err)
	}
	return testDB
}

func mapperDBForServiceTest(testDB *sql.DB) func() {
	return mapper.SetDBForTest(testDB)
}

func stubPersistJobTaskItems(t *testing.T, persisted *[]map[string]interface{}, after func([]map[string]interface{})) func() {
	t.Helper()
	d := *jobDeps
	d.PersistJobTaskItems = func(items []map[string]interface{}) error {
		if persisted != nil {
			*persisted = append(*persisted, items...)
		}
		if after != nil {
			after(items)
		}
		return nil
	}
	return SetJobDepsForTest(&d)
}

func readServiceTaskStatus(t *testing.T, testDB *sql.DB, taskID int64) (int, string) {
	t.Helper()
	var status int
	var errMsg sql.NullString
	if err := testDB.QueryRow("SELECT status, errMsg FROM job_task WHERE id=?", taskID).Scan(&status, &errMsg); err != nil {
		t.Fatalf("read job_task: %v", err)
	}
	return status, errMsg.String
}

func TestNewTaskContextUsesConfiguredTimeoutHours(t *testing.T) {
	ctx, cancel := newTaskContext(2)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatalf("newTaskContext(2) has no deadline, want deadline")
	}
	remaining := time.Until(deadline)
	if remaining < 119*time.Minute || remaining > 121*time.Minute {
		t.Fatalf("deadline remaining = %s, want about 2h", remaining)
	}
}

func TestNewTaskContextFallsBackToCancelableContextWhenTimeoutDisabled(t *testing.T) {
	ctx, cancel := newTaskContext(0)
	defer cancel()

	if _, ok := ctx.Deadline(); ok {
		t.Fatalf("newTaskContext(0) has deadline, want no deadline")
	}
	cancel()
	if err := ctx.Err(); err != context.Canceled {
		t.Fatalf("ctx.Err() = %v, want context.Canceled", err)
	}
}

func TestFinishSuccessfulTaskReleasesJobBeforeSlowNotifications(t *testing.T) {
	testDB := newServiceTaskStatusTestDB(t)
	restoreDB := mapperDBForServiceTest(testDB)
	defer restoreDB()
	if _, err := testDB.Exec(`CREATE TABLE job(
		id integer primary key,
		srcPath text,
		dstPath text
	)`); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := testDB.Exec(`CREATE TABLE notify(
		id integer primary key,
		enable integer,
		method integer,
		params text
	)`); err != nil {
		t.Fatalf("create notify: %v", err)
	}

	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-releaseResponse
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	defer close(releaseResponse)

	if _, err := testDB.Exec("INSERT INTO job(id, srcPath, dstPath) VALUES (1, '/src/', '/dst/')"); err != nil {
		t.Fatalf("insert job: %v", err)
	}
	if _, err := testDB.Exec("INSERT INTO notify(id, enable, method, params) VALUES (1, 1, 0, ?)", `{"url":"`+server.URL+`"}`); err != nil {
		t.Fatalf("insert notify: %v", err)
	}

	client := &JobClient{JobID: 1, Job: map[string]interface{}{"id": int64(1), "enable": 1}}
	if !client.tryMarkDoing() {
		t.Fatalf("tryMarkDoing() = false, want true")
	}
	jt := &JobTask{
		TaskID:         10,
		JobClient:      client,
		Job:            map[string]interface{}{"id": int64(1), "srcPath": "/src/", "dstPath": "/dst/"},
		CreateTime:     float64(time.Now().Unix()),
		FinishedCounts: make(map[taskStatus]int),
		FinishedSizes:  make(map[taskStatus]int64),
	}
	jt.initRuntime()
	client.setCurrentTask(jt)

	done := make(chan struct{})
	go func() {
		jt.finishSuccessfulTask()
		close(done)
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatalf("notification request did not start")
	}
	if client.isBusy() {
		t.Fatalf("job still busy while notification is blocked")
	}
}

func TestFinalTaskStatusUsesTimeoutWhenContextDeadlineExceeded(t *testing.T) {
	if status := finalTaskStatus(false, context.DeadlineExceeded, 0, 0, 0); status != taskStatusTimeout {
		t.Fatalf("finalTaskStatus() = %d, want timeout status 5", status)
	}
}

func TestFinalTaskStatusKeepsManualBreakAsStopped(t *testing.T) {
	if status := finalTaskStatus(true, context.DeadlineExceeded, 0, 0, 0); status != taskStatusStopped {
		t.Fatalf("finalTaskStatus() = %d, want stopped status 4", status)
	}
}

func TestFinalTaskStatusUsesNoSyncWhenNothingWasQueued(t *testing.T) {
	if status := finalTaskStatus(false, nil, 0, 0, 0); status != taskStatusNoSync {
		t.Fatalf("finalTaskStatus() = %d, want no-sync status 8", status)
	}
}

func TestFinalTaskStatusUsesSuccessWhenItemsCompleted(t *testing.T) {
	if status := finalTaskStatus(false, nil, 3, 3, 0); status != taskStatusSuccess {
		t.Fatalf("finalTaskStatus() = %d, want success status 2", status)
	}
}

func TestFinalTaskStatusUsesFailedWhenEveryItemFailed(t *testing.T) {
	if status := finalTaskStatus(false, nil, 3, 0, 3); status != taskStatusFailed {
		t.Fatalf("finalTaskStatus() = %d, want failed status 7", status)
	}
}

func TestFinalTaskStatusUsesPartialWhenSomeItemsSucceeded(t *testing.T) {
	if status := finalTaskStatus(false, nil, 3, 1, 2); status != taskStatusPartialFail {
		t.Fatalf("finalTaskStatus() = %d, want partial status 3", status)
	}
}

func TestGetCurrentIncludesTaskIDForTaskActions(t *testing.T) {
	jt := &JobTask{
		TaskID:     123,
		CreateTime: float64(time.Now().Unix()),
		Waiting:    newCopyQueue(),
	}
	jt.initRuntime()

	current := jt.GetCurrent()

	if current["taskId"] != int64(123) {
		t.Fatalf("taskId = %v, want 123", current["taskId"])
	}
}

func TestGetCurrentDoesNotCacheFinishedTaskLists(t *testing.T) {
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	jt := &JobTask{
		TaskID:     123,
		CreateTime: float64(time.Now().Unix()),
		Waiting:    newCopyQueue(),
	}
	jt.initRuntime()

	for i := 0; i < 3; i++ {
		jt.CopyHook("/src/", "/dst/", "done.txt", int64(10), "", taskStatusSuccess, nil, taskItemFile, taskItemTypeCopy, int64(100+i))
	}
	if err := jt.flushPersistBuffer(); err != nil {
		t.Fatalf("flushPersistBuffer() error: %v", err)
	}

	current := jt.GetCurrent()

	if current["doingTask"] == nil {
		t.Fatalf("doingTask missing from current payload")
	}
}

func TestGetCurrentByStatusPagePaginatesRecentFinishedItems(t *testing.T) {
	testDB := newServiceTaskStatusTestDB(t)
	oldDB := mapperDBForServiceTest(testDB)
	defer oldDB()

	jt := &JobTask{
		TaskID:     123,
		CreateTime: float64(time.Now().Unix()),
		Waiting:    newCopyQueue(),
	}
	jt.initRuntime()

	for i := 0; i < 5; i++ {
		jt.CopyHook("/src/", "/dst/", "done.txt", int64(10), "", taskStatusSuccess, nil, taskItemFile, taskItemTypeCopy, int64(100+i))
	}
	if err := jt.flushPersistBuffer(); err != nil {
		t.Fatalf("flushPersistBuffer() error: %v", err)
	}

	page := jt.GetCurrentByStatusPage(taskStatusSuccess.Int(), 2, 2)
	items := page["dataList"].([]map[string]interface{})

	if page["count"] != int64(5) {
		t.Fatalf("count = %v, want 5", page["count"])
	}
	if len(items) != 2 {
		t.Fatalf("page item len = %d, want 2", len(items))
	}
	if items[0]["createTime"] != int64(102) || items[1]["createTime"] != int64(101) {
		t.Fatalf("page createTimes = %v, %v; want 102, 101", items[0]["createTime"], items[1]["createTime"])
	}
}

func TestFlushPersistBufferRestoresItemsOnPanic(t *testing.T) {
	jt := &JobTask{TaskID: 99}
	jt.initRuntime()
	jt.persistBuffer = []JobTaskItem{
		{TaskID: 99, FileName: "a.txt", Status: taskStatusSuccess},
		{TaskID: 99, FileName: "b.txt", Status: taskStatusSuccess},
	}

	func() {
		defer func() { _ = recover() }()
		_ = jt.flushPersistBufferItemsWith(func([]map[string]interface{}) error {
			panic("simulated db panic")
		})
	}()

	jt.persistBufMu.Lock()
	buffered := len(jt.persistBuffer)
	jt.persistBufMu.Unlock()
	if buffered != 2 {
		t.Fatalf("persistBuffer len after panic = %d, want 2 restored items", buffered)
	}
}

func TestFinishSuccessfulTaskHandlesNilJobClient(t *testing.T) {
	testDB := newServiceTaskStatusTestDB(t)
	restoreDB := mapperDBForServiceTest(testDB)
	defer restoreDB()

	oldQueue := queueTaskNotification
	queueTaskNotification = func(int64, int, map[string]interface{}, int, float64) {}
	defer func() { queueTaskNotification = oldQueue }()

	jt := &JobTask{
		TaskID:     10,
		CreateTime: float64(time.Now().Unix()),
		Waiting:    newCopyQueue(),
	}
	jt.initRuntime()
	jt.ScanFinish.Store(true)

	jt.finishSuccessfulTask()

	status, _ := readServiceTaskStatus(t, testDB, 10)
	if status != taskStatusNoSync.Int() {
		t.Fatalf("task status = %d, want no-sync status 8", status)
	}
}
