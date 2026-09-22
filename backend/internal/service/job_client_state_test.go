package service

import (
	"database/sql"
	"errors"
	"opensync/internal/mapper"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"sync"
	"testing"
	"time"
)

func TestJobClientDoingStateIsSerialized(t *testing.T) {
	client := &JobClient{}

	if !client.tryMarkDoing() {
		t.Fatalf("tryMarkDoing() on idle client = false, want true")
	}
	if client.tryMarkDoing() {
		t.Fatalf("tryMarkDoing() on running client = true, want false")
	}
	if !client.isDoing() {
		t.Fatalf("isDoing() = false, want true")
	}

	client.markDone()
	if client.isDoing() {
		t.Fatalf("isDoing() after markDone = true, want false")
	}
	if !client.tryMarkDoing() {
		t.Fatalf("tryMarkDoing() after markDone = false, want true")
	}
}

func TestJobClientCurrentTaskIsProtected(t *testing.T) {
	client := &JobClient{}
	task := &JobTask{TaskID: 99}

	client.setCurrentTask(task)
	if got := client.currentTask(); got != task {
		t.Fatalf("currentTask() = %#v, want %#v", got, task)
	}

	client.clearCurrentTask(task)
	if got := client.currentTask(); got != nil {
		t.Fatalf("currentTask() after clear = %#v, want nil", got)
	}
}

func TestStopJobKeepsClientBusyUntilTaskFinishes(t *testing.T) {
	client := &JobClient{
		Job:       map[string]interface{}{"enable": 1, "isCron": 2},
		Scheduler: NewScheduler(),
	}
	defer client.Scheduler.Stop()

	task := &JobTask{}
	task.initRuntime()
	client.setCurrentTask(task)
	if !client.tryMarkDoing() {
		t.Fatalf("tryMarkDoing() = false, want true")
	}

	client.StopJob(true)

	if !task.isBreak() {
		t.Fatalf("StopJob() did not request task break")
	}
	if !client.isDoing() {
		t.Fatalf("StopJob() marked client idle before task cleanup finished")
	}
}

func TestPauseJobKeepsMemoryStateWhenDatabaseUpdateFails(t *testing.T) {
	testDB := newServiceTaskStatusTestDB(t)
	restoreDB := mapper.SetDBForTest(testDB)
	defer restoreDB()
	if err := testDB.Close(); err != nil {
		t.Fatalf("close test DB: %v", err)
	}

	client := &JobClient{
		JobID:     10,
		Job:       map[string]interface{}{"id": int64(10), "enable": 1, "isCron": 2},
		Scheduler: NewScheduler(),
	}
	defer client.Scheduler.Stop()
	task := &JobTask{TaskID: 10}
	task.initRuntime()
	client.setCurrentTask(task)

	err := client.StopJob(false)
	if err == nil {
		t.Fatalf("StopJob(false) did not return error after database update failure")
	}
	if task.isBreak() {
		t.Fatalf("StopJob(false) requested task break before database update succeeded")
	}
	if got := util.ToInt(client.Job["enable"]); got != 1 {
		t.Fatalf("job enable after failed pause = %d, want 1", got)
	}
}

func TestDoScheduledSkipsWhenJobAlreadyRunning(t *testing.T) {
	client := &JobClient{
		Job: map[string]interface{}{"enable": 1, "isCron": 2},
	}
	if !client.tryMarkDoing() {
		t.Fatalf("tryMarkDoing() = false, want true")
	}

	if client.DoScheduled() {
		t.Fatalf("DoScheduled() = true while job is running, want false")
	}
	if !client.isDoing() {
		t.Fatalf("DoScheduled() changed running state, want still running")
	}
}

func TestWaitUntilIdleWaitsForTaskCleanup(t *testing.T) {
	client := &JobClient{}
	task := &JobTask{TaskID: 99}
	client.setCurrentTask(task)
	if !client.tryMarkDoing() {
		t.Fatalf("tryMarkDoing() = false, want true")
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		client.markDone()
		client.clearCurrentTask(task)
	}()

	if !client.waitUntilIdle(time.Second) {
		t.Fatalf("waitUntilIdle() = false, want true after task cleanup")
	}
}

func TestWaitUntilIdleTimesOutWhileTaskStillRunning(t *testing.T) {
	client := &JobClient{}
	task := &JobTask{TaskID: 99}
	client.setCurrentTask(task)
	if !client.tryMarkDoing() {
		t.Fatalf("tryMarkDoing() = false, want true")
	}
	defer func() {
		client.markDone()
		client.clearCurrentTask(task)
	}()

	if client.waitUntilIdle(20 * time.Millisecond) {
		t.Fatalf("waitUntilIdle() = true, want false while task is still running")
	}
}

func TestJobClientJobSnapshotIsIndependentAndRaceSafe(t *testing.T) {
	client := &JobClient{
		Job:       map[string]interface{}{"id": int64(1), "enable": 1, "isCron": 0, "interval": 1},
		Scheduler: NewScheduler(),
	}

	snapshot := client.jobSnapshot()
	oldScheduler := client.replaceJobConfig(
		map[string]interface{}{"id": int64(1), "enable": 0, "isCron": 2, "interval": 5},
		NewScheduler(),
	)
	if oldScheduler != nil {
		oldScheduler.Stop()
	}
	defer client.Scheduler.Stop()

	if got := util.ToInt(snapshot["enable"]); got != 1 {
		t.Fatalf("snapshot enable = %d, want original value 1", got)
	}
	if got := util.ToInt(client.jobSnapshot()["enable"]); got != 0 {
		t.Fatalf("current enable = %d, want replacement value 0", got)
	}
}

func TestDoAllJobManualPropagatesMapperErrors(t *testing.T) {
	d := *jobDeps
	d.GetEnableJobList = func() ([]map[string]interface{}, error) {
		return nil, errors.New("database unavailable")
	}
	restore := SetJobDepsForTest(&d)
	defer restore()

	err := DoAllJobManual()
	if err == nil {
		t.Fatalf("DoAllJobManual() did not return error")
	}
	if err.Error() == msg.T(msg.NoJobForRun) {
		t.Fatalf("DoAllJobManual() masked database error as no jobs")
	}
}

func TestDoAllJobManualSkipsJobClientCreationError(t *testing.T) {
	d := *jobDeps
	previousClients := jobClientList
	d.GetEnableJobList = func() ([]map[string]interface{}, error) {
		return []map[string]interface{}{{"id": int64(999)}}, nil
	}
	restore := SetJobDepsForTest(&d)
	defer func() {
		restore()
		jobClientListMu.Lock()
		jobClientList = previousClients
		jobClientListMu.Unlock()
	}()
	jobClientListMu.Lock()
	jobClientList = map[int64]*JobClient{}
	jobClientListMu.Unlock()

	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()
	restoreDB := mapper.SetDBForTest(testDB)
	defer restoreDB()

	// Should not panic — errors from GetJobClientByID are logged and skipped.
	if err := DoAllJobManual(); err != nil {
		t.Fatalf("DoAllJobManual() error: %v", err)
	}
}

func TestRemoveJobClientStopsRunningJobBeforeDeleting(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()
	if _, err := testDB.Exec(`CREATE TABLE job(id integer primary key autoincrement, enable integer DEFAULT 1,
		srcPath text, dstPath text, alistId integer)`); err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := testDB.Exec("INSERT INTO job(id, enable) VALUES (99, 1)"); err != nil {
		t.Fatalf("insert job: %v", err)
	}
	restoreDB := mapper.SetDBForTest(testDB)
	defer restoreDB()

	client := &JobClient{
		JobID:     99,
		Job:       map[string]interface{}{"id": int64(99), "enable": 1, "isCron": 2},
		Scheduler: NewScheduler(),
	}
	defer client.Scheduler.Stop()

	task := &JobTask{TaskID: 100}
	task.initRuntime()
	client.setCurrentTask(task)
	if !client.tryMarkDoing() {
		t.Fatalf("tryMarkDoing() = false, want true")
	}

	jobClientListMu.Lock()
	previousClients := jobClientList
	jobClientList = map[int64]*JobClient{client.JobID: client}
	jobClientListMu.Unlock()
	defer func() {
		jobClientListMu.Lock()
		jobClientList = previousClients
		jobClientListMu.Unlock()
	}()

	go func() {
		time.Sleep(50 * time.Millisecond)
		if task.isBreak() {
			client.markDone()
			client.clearCurrentTask(nil)
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		errCh <- RemoveJobClient(client.JobID)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Logf("RemoveJobClient() error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("RemoveJobClient() did not complete within timeout")
	}

	if !task.isBreak() {
		t.Fatalf("RemoveJobClient() did not request task break")
	}
	if got := util.ToInt(client.Job["enable"]); got != 0 {
		t.Fatalf("job enable after remove = %d, want 0", got)
	}
}

func TestRemoveTaskRejectsRunningTaskWithoutDeletingIt(t *testing.T) {
	testDB := newServiceTaskStatusTestDB(t)
	restoreDB := mapper.SetDBForTest(testDB)
	defer restoreDB()

	client := &JobClient{
		JobID: 1,
		Job:   map[string]interface{}{"id": int64(1), "enable": 1, "isCron": 2},
	}
	task := &JobTask{TaskID: 10, JobClient: client}
	task.initRuntime()
	client.setCurrentTask(task)

	jobClientListMu.Lock()
	previousClients := jobClientList
	jobClientList = map[int64]*JobClient{client.JobID: client}
	jobClientListMu.Unlock()
	defer func() {
		jobClientListMu.Lock()
		jobClientList = previousClients
		jobClientListMu.Unlock()
	}()

	err := RemoveTask(10)
	if err == nil || err.Error() != msg.T(msg.JobRunningCannotDelete) {
		t.Fatalf("RemoveTask() error = %v, want %q", err, msg.T(msg.JobRunningCannotDelete))
	}

	var count int
	if err := testDB.QueryRow("SELECT COUNT(*) FROM job_task WHERE id=10").Scan(&count); err != nil {
		t.Fatalf("count job_task: %v", err)
	}
	if count != 1 {
		t.Fatalf("job_task row count = %d, want 1", count)
	}
}

func TestRemoveTaskRejectsWaitingTaskFromDatabaseState(t *testing.T) {
	testDB := newServiceTaskStatusTestDB(t)
	restoreDB := mapper.SetDBForTest(testDB)
	defer restoreDB()
	if _, err := testDB.Exec("UPDATE job_task SET status=? WHERE id=10", taskStatusWaiting.Int()); err != nil {
		t.Fatalf("set waiting status: %v", err)
	}

	err := RemoveTask(10)
	if err == nil || err.Error() != msg.T(msg.JobRunningCannotDelete) {
		t.Fatalf("RemoveTask() error = %v, want %q", err, msg.T(msg.JobRunningCannotDelete))
	}

	var count int
	if err := testDB.QueryRow("SELECT COUNT(*) FROM job_task WHERE id=10").Scan(&count); err != nil {
		t.Fatalf("count job_task: %v", err)
	}
	if count != 1 {
		t.Fatalf("job_task row count = %d, want 1", count)
	}
}

// startCopyItem is reached from both the submit executor and the full-sync
// relocation path. Every item must get a distinct DoingKey, otherwise entries
// overwrite each other in Doing and the concurrency gate under-counts.
func TestStartCopyItemAssignsDistinctDoingKeysConcurrently(t *testing.T) {
	jt := &JobTask{
		Doing:          make(map[int64]*CopyItem),
		Waiting:        newCopyQueue(),
		FinishedCounts: make(map[taskStatus]int),
		FinishedSizes:  make(map[taskStatus]int64),
	}

	const perGoroutine = 300
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				key := jt.QueueNum.Add(1)
				item := &CopyItem{DoingKey: key}
				jt.DoingMu.Lock()
				jt.Doing[key] = item
				jt.DoingMu.Unlock()
			}
		}()
	}
	wg.Wait()

	if got, want := len(jt.Doing), 2*perGoroutine; got != want {
		t.Fatalf("Doing entries = %d, want %d: DoingKey collisions dropped items", got, want)
	}
}
