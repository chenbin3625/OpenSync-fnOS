package service

import (
	"context"
	"opensync/internal/mapper"
	"opensync/internal/native"
	"testing"
	"time"
)

func makeWeeklyGrid(fill string) []interface{} {
	days := make([]interface{}, 7)
	for d := 0; d < 7; d++ {
		hours := make([]interface{}, 24)
		for h := 0; h < 24; h++ {
			hours[h] = fill
		}
		days[d] = hours
	}
	return days
}

func TestWeeklyScheduleValidation(t *testing.T) {
	days := makeWeeklyGrid(WeeklySlotFull)
	// Sunday 01:00 -> limited; Monday 09:00 -> pause
	days[0].([]interface{})[1] = WeeklySlotLimited
	days[1].([]interface{})[9] = WeeklySlotPause

	_, canonical, err := parseNativeExtraFlags(map[string]interface{}{
		"weeklySchedule": map[string]interface{}{
			"enabled":     true,
			"limitRate":   "5M",
			"stopOutside": true,
			"days":        days,
		},
	})
	if err != nil || canonical == "" {
		t.Fatalf("valid weeklySchedule failed: %q (%v)", canonical, err)
	}

	// Re-parse canonical JSON round-trip
	parsed, _, err := parseNativeExtraFlags(canonical)
	if err != nil || parsed.WeeklySchedule == nil || !parsed.WeeklySchedule.Enabled ||
		parsed.WeeklySchedule.LimitRate != "5M" || !parsed.WeeklySchedule.StopOutside ||
		parsed.WeeklySchedule.Days[0][1] != WeeklySlotLimited || parsed.WeeklySchedule.Days[1][9] != WeeklySlotPause {
		t.Fatalf("round-trip parsed mismatch: %+v (%v)", parsed, err)
	}

	invalidCases := []struct {
		name string
		raw  map[string]interface{}
	}{
		{
			name: "missing days when enabled",
			raw:  map[string]interface{}{"enabled": true, "limitRate": "5M"},
		},
		{
			name: "wrong day count 6",
			raw:  map[string]interface{}{"enabled": true, "days": makeWeeklyGrid(WeeklySlotFull)[:6]},
		},
		{
			name: "wrong hour count 23",
			raw: func() map[string]interface{} {
				bad := makeWeeklyGrid(WeeklySlotFull)
				bad[3] = bad[3].([]interface{})[:23]
				return map[string]interface{}{"enabled": true, "days": bad}
			}(),
		},
		{
			name: "invalid slot enum",
			raw: func() map[string]interface{} {
				bad := makeWeeklyGrid(WeeklySlotFull)
				bad[2].([]interface{})[12] = "throttle"
				return map[string]interface{}{"enabled": true, "days": bad}
			}(),
		},
		{
			name: "invalid limitRate",
			raw: map[string]interface{}{
				"enabled":   true,
				"limitRate": "not-a-rate",
				"days":      makeWeeklyGrid(WeeklySlotFull),
			},
		},
		{
			name: "unknown field",
			raw: map[string]interface{}{
				"enabled": false,
				"unknown": 1,
			},
		},
	}

	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := parseNativeExtraFlags(map[string]interface{}{"weeklySchedule": tc.raw}); err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
		})
	}
}

func TestWeeklySchedulePriorityWindowAndRateLimit(t *testing.T) {
	days := makeWeeklyGrid(WeeklySlotFull)
	// Sunday (weekday 0): hour 10 = full, hour 11 = limited, hour 12 = pause
	days[0].([]interface{})[10] = WeeklySlotFull
	days[0].([]interface{})[11] = WeeklySlotLimited
	days[0].([]interface{})[12] = WeeklySlotPause

	_, canonical, err := parseNativeExtraFlags(map[string]interface{}{
		"weeklySchedule": map[string]interface{}{
			"enabled":     true,
			"limitRate":   "5M",
			"stopOutside": true,
			"days":        days,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Legacy window & bwLimit intentionally conflict with weeklySchedule to verify priority.
	job := map[string]interface{}{
		"id":                int64(1),
		"isCron":            1,
		"enable":            1,
		"windowEnable":      1,
		"windowStart":       "23:00",
		"windowEnd":         "23:30",
		"windowStopOutside": 0,
		"bwLimit":           "1M",
		"extraFlags":        canonical,
	}

	sun := time.Date(2026, 11, 22, 10, 15, 0, 0, time.Local) // Sunday
	if sun.Weekday() != time.Sunday {
		t.Fatalf("test date must be Sunday, got %v", sun.Weekday())
	}

	// Full slot: permitted even outside legacy 23:00-23:30, rate is unthrottled ("off").
	if !JobInTimeWindow(job, sun) {
		t.Fatal("weekly full slot should permit run")
	}
	if got := effectiveJobBwLimitAt(job, sun); got != "off" {
		t.Fatalf("weekly full slot effective bwLimit = %q, want off", got)
	}
	if !JobShouldStopOutsideWindow(job) {
		t.Fatal("weekly stopOutside should override legacy windowStopOutside=0")
	}

	// Limited slot: permitted at weekly limitRate "5M".
	sunLimited := time.Date(2026, 11, 22, 11, 30, 0, 0, time.Local)
	if !JobInTimeWindow(job, sunLimited) {
		t.Fatal("weekly limited slot should permit run")
	}
	if got := effectiveJobBwLimitAt(job, sunLimited); got != "5M" {
		t.Fatalf("weekly limited slot effective bwLimit = %q, want 5M", got)
	}

	// Pause slot: disallowed.
	sunPause := time.Date(2026, 11, 22, 12, 0, 0, 0, time.Local)
	if JobInTimeWindow(job, sunPause) {
		t.Fatal("weekly pause slot must forbid run")
	}

	// ApplyGlobalBwLimit must NOT overwrite a weekly-scheduled task.
	task := &JobTask{Job: job, rcController: native.NewRCController("5M")}
	defer task.rcController.Close()
	if err := task.ApplyGlobalBwLimit(context.Background(), "2M"); err != nil {
		t.Fatal(err)
	}
	if got := task.liveBwLimit(); got != "5M" {
		t.Fatalf("weekly-scheduled task bwLimit changed by global limit: %q", got)
	}

	// Fallback when weeklySchedule.enabled == false: preserves legacy window & bwLimit behavior.
	_, disabledCanonical, err := parseNativeExtraFlags(map[string]interface{}{
		"weeklySchedule": map[string]interface{}{
			"enabled":     false,
			"limitRate":   "5M",
			"stopOutside": true,
			"days":        days,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	job["extraFlags"] = disabledCanonical
	if JobInTimeWindow(job, sun) {
		t.Fatal("disabled weeklySchedule must fall back to legacy window 23:00-23:30")
	}
	if got := effectiveJobBwLimitAt(job, sunLimited); got != "1M" {
		t.Fatalf("disabled weeklySchedule must fall back to job bwLimit 1M, got %q", got)
	}
	if JobShouldStopOutsideWindow(job) {
		t.Fatal("disabled weeklySchedule must fall back to legacy windowStopOutside=0")
	}

	// Verify syncStatus exposes weekly schedule status on both active and continuous rows.
	now := time.Now()
	liveDays := makeWeeklyGrid(WeeklySlotLimited)
	liveDays[int(now.Weekday())].([]interface{})[now.Hour()] = WeeklySlotLimited
	_, liveCanonical, err := parseNativeExtraFlags(map[string]interface{}{
		"weeklySchedule": map[string]interface{}{
			"enabled":   true,
			"limitRate": "7M",
			"days":      liveDays,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	statusRow := map[string]interface{}{
		"id":         int64(99),
		"enable":     1,
		"isCron":     1,
		"extraFlags": liveCanonical,
	}
	addSyncStatus(statusRow, mapper.SyncState{}, nil, 0, nil)
	status, ok := statusRow["syncStatus"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected syncStatus map for weekly-scheduled job, got %#v", statusRow["syncStatus"])
	}
	if status["scheduleState"] != WeeklySlotLimited || status["scheduleLimited"] != true ||
		status["schedulePaused"] != false || status["weeklyLimitRate"] != "7M" ||
		status["effectiveBwLimit"] != "7M" {
		t.Fatalf("unexpected weekly syncStatus fields: %+v", status)
	}
}
