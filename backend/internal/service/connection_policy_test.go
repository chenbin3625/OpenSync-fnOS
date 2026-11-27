package service

import (
	"context"
	"encoding/json"
	"testing"

	"opensync/internal/mapper"
	"opensync/internal/native"
)

func TestConnectionPolicyRoundTripKeepsOpenSyncMetadata(t *testing.T) {
	params := map[string]string{
		"type":                           "s3",
		native.ConnectionPollIntervalKey: "300",
		native.ConnectionBwLimitKey:      "10M",
		native.ConnectionPausedKey:       "true",
	}
	policy, err := parseConnectionPolicy(params)
	if err != nil {
		t.Fatalf("parseConnectionPolicy() error = %v", err)
	}
	if policy.PollIntervalSec != 300 || policy.BwLimit != "10M" || !policy.Paused {
		t.Fatalf("unexpected policy: %+v", policy)
	}
	if err := encodeConnectionPolicy(params, NativeConnectionPolicy{PollIntervalSec: 600, BwLimit: "5M"}); err != nil {
		t.Fatalf("encodeConnectionPolicy() error = %v", err)
	}
	if params[native.ConnectionPollIntervalKey] != "600" || params[native.ConnectionBwLimitKey] != "5M" {
		t.Fatalf("encoded policy = %#v", params)
	}
	if _, ok := params[native.ConnectionPausedKey]; ok {
		t.Fatal("cleared pause metadata must not remain in the remote definition")
	}
}

func TestMergeEditRemoteParamsPreservesConnectionPolicy(t *testing.T) {
	oldParams := map[string]string{
		"type":                           "webdav",
		"url":                            "https://dav.example.com",
		"user":                           "alice",
		"pass":                           "obscured-secret",
		native.ConnectionPollIntervalKey: "180",
		native.ConnectionBwLimitKey:      "8M",
		native.ConnectionPausedKey:       "true",
	}
	merged, _, err := mergeEditRemoteParams(oldParams, map[string]string{
		"type": "webdav",
		"url":  "https://dav2.example.com",
		"user": "alice",
	}, nil)
	if err != nil {
		t.Fatalf("mergeEditRemoteParams() error = %v", err)
	}
	if merged[native.ConnectionPollIntervalKey] != "180" ||
		merged[native.ConnectionBwLimitKey] != "8M" ||
		merged[native.ConnectionPausedKey] != "true" {
		t.Fatalf("editing credentials wiped connection policy: %#v", merged)
	}
}

func TestConnectionDefaultsInheritAndRespectTaskOverride(t *testing.T) {
	days := make([][]string, 7)
	for d := 0; d < 7; d++ {
		days[d] = make([]string, 24)
		for h := 0; h < 24; h++ {
			days[d][h] = WeeklySlotFull
		}
	}
	days[0][0] = WeeklySlotLimited
	ws := &WeeklyScheduleConfig{
		Enabled:   true,
		LimitRate: "2M",
		Days:      days,
	}
	wsJSON, err := json.Marshal(ws)
	if err != nil {
		t.Fatalf("marshal weekly schedule: %v", err)
	}
	cfg := native.Config{
		"cloud-a": {
			"type":                           "s3",
			native.ConnectionPollIntervalKey: "300",
			native.ConnectionWeeklyScheduleKey: string(wsJSON),
		},
		"crypt-a": {
			"type":   "crypt",
			"remote": "cloud-a:/enc",
		},
	}

	// Local-to-local job must not inherit remote defaults.
	localJob := map[string]interface{}{
		"srcPath":    "/本地/a",
		"dstPath":    "/本地/b",
		"extraFlags": "",
	}
	localApplied := applyConnectionDefaults(localJob, cfg)
	if localApplied["extraFlags"] != "" {
		t.Fatalf("local-to-local job unexpectedly inherited connection defaults: %v", localApplied["extraFlags"])
	}

	// Crypt job inherits from its underlying remote chain when task fields are zero.
	cryptJob := map[string]interface{}{
		"srcPath":    "/本地/docs",
		"dstPath":    "/crypt-a/docs",
		"extraFlags": "",
	}
	cryptApplied := applyConnectionDefaults(cryptJob, cfg)
	extra, _, err := parseNativeExtraFlags(cryptApplied["extraFlags"])
	if err != nil {
		t.Fatalf("parseNativeExtraFlags: %v", err)
	}
	if extra.SyncPollIntervalSec != 300 {
		t.Fatalf("crypt job pollIntervalSec = %d, want 300", extra.SyncPollIntervalSec)
	}
	if extra.WeeklySchedule == nil || !extra.WeeklySchedule.Enabled || extra.WeeklySchedule.LimitRate != "2M" {
		t.Fatalf("crypt job weeklySchedule = %+v, want inherited 2M schedule", extra.WeeklySchedule)
	}

	// Task explicit override wins over connection default.
	explicitJob := map[string]interface{}{
		"srcPath":    "/本地/docs",
		"dstPath":    "/cloud-a/docs",
		"extraFlags": `{"syncPollIntervalSec":120}`,
	}
	explicitApplied := applyConnectionDefaults(explicitJob, cfg)
	explicitExtra, _, err := parseNativeExtraFlags(explicitApplied["extraFlags"])
	if err != nil {
		t.Fatalf("parseNativeExtraFlags: %v", err)
	}
	if explicitExtra.SyncPollIntervalSec != 120 {
		t.Fatalf("explicit task pollIntervalSec = %d, want 120", explicitExtra.SyncPollIntervalSec)
	}
	if explicitExtra.WeeklySchedule == nil || !explicitExtra.WeeklySchedule.Enabled {
		t.Fatalf("unset weeklySchedule should still inherit from connection: %+v", explicitExtra.WeeklySchedule)
	}
}

func TestConnectionBandwidthGroupSplitsAndRebalancesSharedCap(t *testing.T) {
	group := newConnectionBandwidthGroup("cloud-a", NativeConnectionPolicy{BwLimit: "10M"})
	first := &JobTask{
		rcController:          native.NewRCController(""),
		connectionBwLimits:    make(map[string]string),
		connectionBwInherited: true,
	}
	second := &JobTask{
		rcController:          native.NewRCController(""),
		connectionBwLimits:    make(map[string]string),
		connectionBwInherited: true,
	}
	defer first.rcController.Close()
	defer second.rcController.Close()

	releaseFirst := group.register(first)
	if got := native.ParseBwLimitBytesPerSec(first.rcController.BwLimit()); got != native.ParseBwLimitBytesPerSec("10M") {
		t.Fatalf("first task rate after first registration = %q", first.rcController.BwLimit())
	}
	releaseSecond := group.register(second)
	firstRate := native.ParseBwLimitBytesPerSec(first.rcController.BwLimit())
	secondRate := native.ParseBwLimitBytesPerSec(second.rcController.BwLimit())
	capRate := native.ParseBwLimitBytesPerSec("10M")
	if firstRate+secondRate > capRate {
		t.Fatalf("shared connection cap exceeded: first=%d second=%d cap=%d", firstRate, secondRate, capRate)
	}
	if firstRate != secondRate {
		t.Fatalf("active tasks should receive equal shares, first=%d second=%d", firstRate, secondRate)
	}

	releaseSecond()
	if got := native.ParseBwLimitBytesPerSec(first.rcController.BwLimit()); got != capRate {
		t.Fatalf("remaining task rate after peer exit = %q, want 10M", first.rcController.BwLimit())
	}
	releaseFirst()
}

func TestConnectionBandwidthGroupBoundsExplicitTaskAndClearsCleanly(t *testing.T) {
	group := newConnectionBandwidthGroup("cloud-b", NativeConnectionPolicy{BwLimit: "10M"})
	group.setSystemLimit("20M")

	explicitHigh := &JobTask{
		rcController:       native.NewRCController("8M"),
		connectionBwLimits: make(map[string]string),
		connectionBwSystem: "20M",
		taskBaseBwLimit:    "8M",
		taskExplicitBw:     true,
	}
	inherited := &JobTask{
		rcController:       native.NewRCController("10M"),
		connectionBwLimits: make(map[string]string),
		connectionBwSystem: "20M",
	}
	defer explicitHigh.rcController.Close()
	defer inherited.rcController.Close()

	rel1 := group.register(explicitHigh)
	rel2 := group.register(inherited)
	defer rel1()
	defer rel2()

	// Both tasks share the 10M connection cap (5M each), bounding the 8M explicit task.
	if got := native.ParseBwLimitBytesPerSec(explicitHigh.rcController.BwLimit()); got > native.ParseBwLimitBytesPerSec("5M") {
		t.Fatalf("explicit 8M task exceeded shared 5M slice: %q", explicitHigh.rcController.BwLimit())
	}
	if got := native.ParseBwLimitBytesPerSec(inherited.rcController.BwLimit()); got > native.ParseBwLimitBytesPerSec("5M") {
		t.Fatalf("inherited task exceeded shared 5M slice: %q", inherited.rcController.BwLimit())
	}

	// Clearing the connection cap restores the explicit task's 8M and the inherited task's 20M system limit (not split!).
	group.setPolicy(NativeConnectionPolicy{BwLimit: ""})
	if got := native.ParseBwLimitBytesPerSec(explicitHigh.rcController.BwLimit()); got != native.ParseBwLimitBytesPerSec("8M") {
		t.Fatalf("explicit task rate after clearing cap = %q, want 8M", explicitHigh.rcController.BwLimit())
	}
	if got := native.ParseBwLimitBytesPerSec(inherited.rcController.BwLimit()); got != native.ParseBwLimitBytesPerSec("20M") {
		t.Fatalf("inherited task rate after clearing cap = %q, want 20M", inherited.rcController.BwLimit())
	}
}

func TestConnectionPauseIsolationAndSystemBackupRoundTrip(t *testing.T) {
	cleanup := setupNativeTestDB(t)
	defer cleanup()

	cfg := native.Config{
		"cloud-a": {
			"type":                           "webdav",
			"url":                            "https://dav-a.example.com",
			native.ConnectionPollIntervalKey: "240",
			native.ConnectionBwLimitKey:      "6M",
			native.ConnectionPausedKey:       "true",
		},
		"cloud-b": {
			"type": "webdav",
			"url":  "https://dav-b.example.com",
		},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal cfg: %v", err)
	}
	if _, err := mapper.AddAlist("内置存储", native.EngineURL, "rclone", string(raw)); err != nil {
		t.Fatalf("AddAlist: %v", err)
	}

	exported, err := ExportSystemBackup(true)
	if err != nil {
		t.Fatalf("ExportSystemBackup(true): %v", err)
	}
	// Modify live policy, then restore from backup with overwrite and confirm policy returns.
	if err := UpdateNativeStoragePolicy(context.Background(), NativeConnectionPolicyInput{
		Name:            "cloud-a",
		PollIntervalSec: 0,
		BwLimit:         "",
		Paused:          false,
	}); err != nil {
		t.Fatalf("UpdateNativeStoragePolicy(clear): %v", err)
	}
	if _, err := ImportSystemBackup(*exported, "overwrite"); err != nil {
		t.Fatalf("ImportSystemBackup(overwrite): %v", err)
	}
	state, err := GetNativeStorageState(context.Background())
	if err != nil {
		t.Fatalf("GetNativeStorageState: %v", err)
	}
	var found *NativeRemoteItem
	for i := range state.Remotes {
		if state.Remotes[i].Name == "cloud-a" {
			found = &state.Remotes[i]
			break
		}
	}
	if found == nil {
		t.Fatal("cloud-a missing after restore")
	}
	if found.PollIntervalSec != 240 || found.BwLimit != "6M" || !found.Paused {
		t.Fatalf("restored policy = %+v, want poll=240 bw=6M paused=true", found)
	}
}
