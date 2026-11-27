package service

import (
	"context"
	"testing"
	"time"

	"opensync/internal/mapper"
	"opensync/internal/native"
)

func TestAddSyncStatusClassifiesLegacyAuthStateAndExposesEndpoint(t *testing.T) {
	jobFSWatchers.ResetForTest()
	defer jobFSWatchers.ResetForTest()
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	jobFSWatchers.mu.Lock()
	jobFSWatchers.watchers[9101] = &jobWatcher{
		jobID:  9101,
		cancel: cancel,
		wake:   make(chan struct{}, 1),
	}
	jobFSWatchers.mu.Unlock()

	row := map[string]interface{}{
		"id":         int64(9101),
		"isCron":     3,
		"enable":     1,
		"srcPath":    `["/本地/photos"]`,
		"dstPath":    `["/PhotoCloud/photos"]`,
		"extraFlags": `{}`,
	}
	state := mapper.SyncState{
		Error:       "登录状态已失效，请重新授权",
		Pending:     true,
		Paths:       []string{"/本地/photos/a.jpg"},
		RetryAt:     time.Now().Add(time.Minute).Unix(),
		LastSuccess: 100,
	}

	addSyncStatus(row, state, nil, 0, nil)
	status, ok := row["syncStatus"].(map[string]interface{})
	if !ok {
		t.Fatalf("syncStatus = %#v", row["syncStatus"])
	}
	if status["errorType"] != mapper.SyncErrorAuth || status["errorSide"] != "remote" || status["errorEndpoint"] != "PhotoCloud" {
		t.Fatalf("legacy auth classification = %#v", status)
	}
}

func TestRecoverNativeStorageJobsOnlyClearsAffectedEnabledAuthFailures(t *testing.T) {
	defer setupNativeTestDB(t)()

	const engineID int64 = 9102
	cfg := native.Config{
		"PhotoCloud": {"type": "webdav", "url": "https://photos.example"},
		"OtherCloud": {"type": "webdav", "url": "https://other.example"},
	}
	jobs := []struct {
		name   string
		remote string
		enable int
		typeID string
	}{
		{name: "affected", remote: "PhotoCloud", enable: 1, typeID: mapper.SyncErrorAuth},
		{name: "unrelated", remote: "OtherCloud", enable: 1, typeID: mapper.SyncErrorAuth},
		{name: "rate-limited", remote: "PhotoCloud", enable: 1, typeID: mapper.SyncErrorRateLimited},
		{name: "paused", remote: "PhotoCloud", enable: 0, typeID: mapper.SyncErrorAuth},
	}
	ids := make(map[string]int64, len(jobs))
	for _, item := range jobs {
		id, err := mapper.AddJob(map[string]interface{}{
			"enable":  item.enable,
			"remark":  item.name,
			"srcPath": "/本地/source-" + item.name,
			"dstPath": "/" + item.remote + "/target",
			"alistId": engineID,
			"isCron":  3,
			"method":  0,
		})
		if err != nil {
			t.Fatal(err)
		}
		ids[item.name] = id
		if err := mapper.EnsureSyncState(id, item.name); err != nil {
			t.Fatal(err)
		}
		if err := mapper.RecordSyncError(id, "登录状态已失效", mapper.SyncError{Type: item.typeID, Side: "remote", Endpoint: item.remote}); err != nil {
			t.Fatal(err)
		}
		if item.typeID == mapper.SyncErrorRateLimited {
			if err := mapper.RecordSyncError(id, "接口触发限流", mapper.SyncError{Type: item.typeID, Side: "remote", Endpoint: item.remote}); err != nil {
				t.Fatal(err)
			}
		}
	}

	recoverNativeStorageJobs(engineID, cfg, []string{"PhotoCloud"})

	for name, wantCleared := range map[string]bool{
		"affected":     true,
		"unrelated":    false,
		"rate-limited": false,
		"paused":       false,
	} {
		state, err := mapper.GetSyncState(ids[name])
		if err != nil {
			t.Fatal(err)
		}
		if wantCleared && (state.Error != "" || state.ErrorType != "" || state.RetryAt != 0) {
			t.Fatalf("%s was not recovered: %+v", name, state)
		}
		if !wantCleared && state.Error == "" {
			t.Fatalf("%s was cleared unexpectedly: %+v", name, state)
		}
	}
}
