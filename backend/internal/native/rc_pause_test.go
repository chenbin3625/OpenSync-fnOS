package native

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRCControllerWaitIfPausedAndResume(t *testing.T) {
	ctrl := NewRCController("")
	defer ctrl.Close()

	if ctrl.IsPaused() {
		t.Fatal("new controller must not start paused")
	}
	if err := ctrl.Pause(); err != nil {
		t.Fatalf("Pause() unexpected error: %v", err)
	}
	if !ctrl.IsPaused() {
		t.Fatal("expected IsPaused() = true after Pause()")
	}
	// Idempotent second Pause()
	if err := ctrl.Pause(); err != nil {
		t.Fatalf("second Pause() unexpected error: %v", err)
	}

	unblocked := make(chan error, 1)
	go func() {
		unblocked <- ctrl.WaitIfPaused(context.Background())
	}()

	select {
	case err := <-unblocked:
		t.Fatalf("WaitIfPaused returned early while paused: %v", err)
	case <-time.After(60 * time.Millisecond):
	}

	if err := ctrl.Resume(); err != nil {
		t.Fatalf("Resume() unexpected error: %v", err)
	}
	if ctrl.IsPaused() {
		t.Fatal("expected IsPaused() = false after Resume()")
	}

	select {
	case err := <-unblocked:
		if err != nil {
			t.Fatalf("WaitIfPaused returned error after Resume: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitIfPaused did not unblock after Resume()")
	}

	if got := ctrl.PausedDuration(); got < 40*time.Millisecond {
		t.Fatalf("PausedDuration() = %v, want >= 40ms", got)
	}
}

func TestRCControllerWaitIfPausedHonorsContextCancel(t *testing.T) {
	ctrl := NewRCController("")
	defer ctrl.Close()

	if err := ctrl.Pause(); err != nil {
		t.Fatalf("Pause() failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ctrl.WaitIfPaused(ctx)
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("WaitIfPaused err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("WaitIfPaused did not return on context cancel")
	}
}

func TestRCControllerPauseResumeSyncsDeferredBwLimit(t *testing.T) {
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

	if err := ctrl.Pause(); err != nil {
		t.Fatalf("Pause() failed: %v", err)
	}
	if err := ctrl.SetBwLimit(context.Background(), "12M"); err != nil {
		t.Fatalf("SetBwLimit while paused failed: %v", err)
	}
	// While paused, RC push must be deferred.
	mu.Lock()
	if len(received) != 0 {
		mu.Unlock()
		t.Fatalf("expected no RC push while paused, got %v", received)
	}
	mu.Unlock()

	if err := ctrl.Resume(); err != nil {
		t.Fatalf("Resume() failed: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := append([]string(nil), received...)
		mu.Unlock()
		if len(got) == 1 && got[0] == "12M" {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	mu.Lock()
	t.Fatalf("expected deferred bwlimit 12M to be pushed on Resume(), got %v", received)
	mu.Unlock()
}

func TestRCControllerSubprocessPauseResumeAndCancelLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("SIGSTOP/SIGCONT subprocess verification is Linux-specific")
	}

	ctrl := NewRCController("")
	defer ctrl.Close()

	// Spawn a lightweight sleep subprocess and verify Pause/Resume/Attach-exited-PID/ResumeAllProcesses.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start subprocess: %v", err)
	}
	detach := ctrl.Attach(cmd.Process.Pid, "", "", "")
	defer detach()

	if err := ctrl.Pause(); err != nil {
		t.Fatalf("Pause() on live subprocess failed: %v", err)
	}
	if !ctrl.IsPaused() {
		t.Fatal("controller should report paused")
	}
	if err := ctrl.Resume(); err != nil {
		t.Fatalf("Resume() on live subprocess failed: %v", err)
	}

	// Pause again, then cancel via ResumeAllProcesses + cancel() so the process exits cleanly without hanging.
	if err := ctrl.Pause(); err != nil {
		t.Fatalf("second Pause() failed: %v", err)
	}
	ctrl.ResumeAllProcesses()
	cancel()

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case <-waitDone:
	case <-time.After(3 * time.Second):
		t.Fatal("SIGSTOP'd subprocess hung after ResumeAllProcesses + cancel")
	}

	// Pausing/resuming with an already-exited PID still attached must ignore ESRCH cleanly.
	if err := ctrl.Pause(); err != nil {
		t.Fatalf("Pause() with exited PID must ignore ESRCH, got: %v", err)
	}
	if err := ctrl.Resume(); err != nil {
		t.Fatalf("Resume() with exited PID must ignore ESRCH, got: %v", err)
	}
}
