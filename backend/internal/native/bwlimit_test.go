package native

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestValidateBwLimitExactRcloneSyntax(t *testing.T) {
	valid := []string{
		"",
		"off",
		"0",
		"10M",
		"10Mi",
		"10MiB",
		"500K",
		"500Ki",
		"500KiB",
		"1.5G",
		"100B",
		"100",
		"5M:20M",
		"5M:off",
		"off:20M",
		"08:00,5M 23:00,off",
		"08:00,5M:10M 19:00,2M 23:00,off",
		"Mon-09:00,5M Mon-18:00,off Tue-09:00,5M Tue-18:00,off Wed-09:00,5M Wed-18:00,off Thu-09:00,5M Thu-18:00,off Fri-09:00,5M Fri-18:00,off",
		// > 256 chars multi-slot weekly timetable must now be accepted
		"Mon-08:00,5M Mon-12:00,off Mon-14:00,2M Mon-18:00,off Tue-08:00,5M Tue-12:00,off Tue-14:00,2M Tue-18:00,off Wed-08:00,5M Wed-12:00,off Wed-14:00,2M Wed-18:00,off Thu-08:00,5M Thu-12:00,off Thu-14:00,2M Thu-18:00,off Fri-08:00,5M Fri-12:00,off Fri-14:00,2M Fri-18:00,off",
	}
	for _, spec := range valid {
		if err := ValidateBwLimit(spec); err != nil {
			t.Fatalf("ValidateBwLimit(%q) unexpected error: %v", spec, err)
		}
	}

	invalid := []string{
		"10MB",
		"500KB",
		"1GB",
		"-10M",
		"not-a-speed",
		"25:00,5M",
		"08:60,5M",
		"Foo-08:00,5M",
		"08:00,5M:10M:2M",
		"08:00,10MB 23:00,off",
		"08:00,5M\n23:00,off",
	}
	for _, spec := range invalid {
		if err := ValidateBwLimit(spec); err == nil {
			t.Fatalf("ValidateBwLimit(%q) expected error, got nil", spec)
		}
	}
}

func TestResolveBwLimitAtDailyAndWeeklyTimetables(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)

	// 1. Daily schedule: 08:00,5M 23:00,off
	daily := "08:00,5M 23:00,off"
	// 07:30 -> wraps back to 23:00,off; next change today 08:00
	t1 := time.Date(2026, 11, 25, 7, 30, 15, 0, loc) // Wednesday
	rate, next, sched := ResolveBwLimitAt(daily, t1)
	if !sched || rate != "off" {
		t.Fatalf("07:30 got (%q, %v), want (off, true)", rate, sched)
	}
	wantNext1 := time.Date(2026, 11, 25, 8, 0, 0, 0, loc)
	if !next.Equal(wantNext1) {
		t.Fatalf("07:30 nextChange = %v, want %v", next, wantNext1)
	}

	// 08:00 -> active 5M; next change today 23:00
	t2 := time.Date(2026, 11, 25, 8, 0, 0, 0, loc)
	rate, next, sched = ResolveBwLimitAt(daily, t2)
	if !sched || rate != "5M" {
		t.Fatalf("08:00 got (%q, %v), want (5M, true)", rate, sched)
	}
	wantNext2 := time.Date(2026, 11, 25, 23, 0, 0, 0, loc)
	if !next.Equal(wantNext2) {
		t.Fatalf("08:00 nextChange = %v, want %v", next, wantNext2)
	}

	// 23:15 -> active off; next change tomorrow 08:00
	t3 := time.Date(2026, 11, 25, 23, 15, 0, 0, loc)
	rate, next, sched = ResolveBwLimitAt(daily, t3)
	if !sched || rate != "off" {
		t.Fatalf("23:15 got (%q, %v), want (off, true)", rate, sched)
	}
	wantNext3 := time.Date(2026, 11, 26, 8, 0, 0, 0, loc)
	if !next.Equal(wantNext3) {
		t.Fatalf("23:15 nextChange = %v, want %v", next, wantNext3)
	}

	// 2. Weekday schedule: Mon-Fri 09:00-18:00 = 5M:20M, rest off
	weekly := "Mon-09:00,5M:20M Mon-18:00,off Tue-09:00,5M:20M Tue-18:00,off Wed-09:00,5M:20M Wed-18:00,off Thu-09:00,5M:20M Thu-18:00,off Fri-09:00,5M:20M Fri-18:00,off"
	// Saturday 12:00 -> off, next change Monday 09:00
	sat := time.Date(2026, 11, 28, 12, 0, 0, 0, loc) // Saturday
	rate, next, sched = ResolveBwLimitAt(weekly, sat)
	if !sched || rate != "off" {
		t.Fatalf("Saturday 12:00 got (%q, %v), want (off, true)", rate, sched)
	}
	wantMon := time.Date(2026, 11, 30, 9, 0, 0, 0, loc) // Monday
	if !next.Equal(wantMon) {
		t.Fatalf("Saturday nextChange = %v, want %v", next, wantMon)
	}

	// Wednesday 10:00 -> 5M:20M, bytes/sec uses conservative smaller cap = 5 MiB/s
	wed := time.Date(2026, 11, 25, 10, 0, 0, 0, loc)
	bps := ParseBwLimitBytesPerSecAt(weekly, wed)
	if bps != 5*1024*1024 {
		t.Fatalf("Wednesday 10:00 bps = %d, want %d", bps, 5*1024*1024)
	}
	if ParseBwLimitBytesPerSecAt(weekly, sat) != 0 {
		t.Fatalf("Saturday 12:00 bps should be 0 (unlimited)")
	}
}

func TestRCControllerSetBwLimitPushesResolvedRateForTimetable(t *testing.T) {
	var (
		mu       sync.Mutex
		received []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/core/bwlimit" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Rate string `json:"rate"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Reject raw timetables just like real rclone /core/bwlimit!
		if strings.ContainsAny(body.Rate, " ,") {
			http.Error(w, `{"error":"need exactly 1 bandwidth setting"}`, http.StatusInternalServerError)
			return
		}
		mu.Lock()
		received = append(received, body.Rate)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"rate":"` + body.Rate + `"}`))
	}))
	defer srv.Close()

	addr := strings.TrimPrefix(srv.URL, "http://")
	ctrl := NewRCController("")
	defer ctrl.Close()
	detach := ctrl.Attach(0, addr, "", "")
	defer detach()

	// Applying a timetable live must succeed by pushing only the currently active single rate.
	timetable := "00:00,5M 23:59,5M"
	if err := ctrl.SetBwLimit(context.Background(), timetable); err != nil {
		t.Fatalf("SetBwLimit(timetable) failed: %v", err)
	}
	if got := ctrl.BwLimit(); got != timetable {
		t.Fatalf("ctrl.BwLimit() = %q, want %q", got, timetable)
	}
	if got := ctrl.bytesPerSec.Load(); got != 5*1024*1024 {
		t.Fatalf("ctrl.bytesPerSec = %d, want %d", got, 5*1024*1024)
	}

	mu.Lock()
	pushed := append([]string(nil), received...)
	mu.Unlock()
	if len(pushed) != 1 || pushed[0] != "5M" {
		t.Fatalf("pushed rates = %v, want [5M]", pushed)
	}

	// Clearing limit pushes "off" and clears bytesPerSec.
	if err := ctrl.SetBwLimit(context.Background(), ""); err != nil {
		t.Fatalf("SetBwLimit(empty) failed: %v", err)
	}
	if ctrl.bytesPerSec.Load() != 0 {
		t.Fatalf("expected bytesPerSec = 0 after clearing")
	}
	mu.Lock()
	pushed = append([]string(nil), received...)
	mu.Unlock()
	if len(pushed) != 2 || pushed[1] != "off" {
		t.Fatalf("pushed rates = %v, want [5M off]", pushed)
	}
}
