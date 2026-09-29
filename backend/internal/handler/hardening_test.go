package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"opensync/internal/config"
	"opensync/internal/msg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func decodeEnvelope(t *testing.T, body []byte) (int, string) {
	t.Helper()
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("invalid response JSON %s: %v", body, err)
	}
	return resp.Code, resp.Msg
}

// PUT /svr/job with an empty body used to fall through to DoAllJobManual and
// start every enabled job.
func TestUpdateJobWithoutIDDoesNotRunAllJobs(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.PUT("/svr/job", UpdateJob)

	req := httptest.NewRequest(http.MethodPut, "/svr/job", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	code, message := decodeEnvelope(t, w.Body.Bytes())
	if code != 500 || message != msg.T(msg.LostPart) {
		t.Fatalf("response = %s, want LostPart error", w.Body.String())
	}
}

func intPtr(v int) *int { return &v }

func TestSystemSettingsPatchKeepsOmittedFields(t *testing.T) {
	current := config.SystemSettings{
		TaskTimeout: 48, TaskSave: 30, CopyConcurrency: 5, ScanConcurrency: 16, MaxRetries: 2,
	}
	var patch systemSettingsPatch
	if err := json.Unmarshal([]byte(`{"copyConcurrency":8,"taskSave":0}`), &patch); err != nil {
		t.Fatal(err)
	}
	got := patch.applyTo(current)
	want := config.SystemSettings{
		TaskTimeout: 48, TaskSave: 0, CopyConcurrency: 8, ScanConcurrency: 16, MaxRetries: 2,
	}
	if got != want {
		t.Fatalf("applyTo() = %+v, want %+v (explicit 0 kept, omitted fields unchanged)", got, want)
	}
	if (systemSettingsPatch{}).applyTo(current) != current {
		t.Fatal("empty patch changed settings")
	}
	full := systemSettingsPatch{intPtr(1), intPtr(2), intPtr(3), intPtr(4), intPtr(5)}.applyTo(current)
	if full != (config.SystemSettings{TaskTimeout: 1, TaskSave: 2, CopyConcurrency: 3, ScanConcurrency: 4, MaxRetries: 5}) {
		t.Fatalf("full patch = %+v", full)
	}
}

func useSystemConfig(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("TRIM_PKGETC", dir)
	config.SetConfigForTest(&config.Config{Server: config.ServerConfig{
		Timeout: 48, TaskSave: 30, CopyConcurrency: 5, ScanConcurrency: 16, MaxRetries: 2,
	}})
	t.Cleanup(func() { config.SetConfigForTest(nil) })
}

func putSystemConfig(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.PUT("/svr/system/config", UpdateSystemConfig)
	req := httptest.NewRequest(http.MethodPut, "/svr/system/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

// A partial body is validated after the merge: omitted concurrency fields keep
// their current in-range values instead of becoming 0 and failing the check.
func TestUpdateSystemConfigValidatesMergedSettings(t *testing.T) {
	useSystemConfig(t, t.TempDir())
	w := putSystemConfig(t, `{"scanConcurrency":51}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an out-of-range field", w.Code)
	}
	_, message := decodeEnvelope(t, w.Body.Bytes())
	if !strings.Contains(message, msg.T(msg.SettingsScanConcurrency)) {
		t.Fatalf("message = %q, want the scan concurrency range error", message)
	}
	if got := config.GetSystemSettings().ScanConcurrency; got != 16 {
		t.Fatalf("scanConcurrency = %d after rejected update, want 16", got)
	}
}

func TestUpdateSystemConfigHidesWriteErrorDetails(t *testing.T) {
	// A regular file where the config directory should be makes MkdirAll fail
	// with an error that names the path.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(blocker, "etc")
	useSystemConfig(t, configDir)

	w := putSystemConfig(t, `{"copyConcurrency":8}`)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	_, message := decodeEnvelope(t, w.Body.Bytes())
	if message != msg.T(msg.ConfigSaveFail) {
		t.Fatalf("message = %q, want the generic save failure", message)
	}
	if strings.Contains(w.Body.String(), blocker) || strings.Contains(w.Body.String(), "not a directory") {
		t.Fatalf("response leaked OS error: %s", w.Body.String())
	}
	if got := config.GetSystemSettings().CopyConcurrency; got != 5 {
		t.Fatalf("copyConcurrency = %d after failed write, want 5", got)
	}
}

type fakeDeadline struct {
	err      error
	deadline time.Time
	calls    int
}

func (f *fakeDeadline) SetWriteDeadline(d time.Time) error {
	f.calls++
	f.deadline = d
	return f.err
}

func TestWriteSSEFrameArmsDeadlineBeforeWriting(t *testing.T) {
	rec := httptest.NewRecorder()
	rc := &fakeDeadline{}
	before := time.Now()
	if !writeSSEFrame(rec, rc, rec, "data: x\n\n") {
		t.Fatal("writeSSEFrame() = false, want success")
	}
	if rc.calls != 1 {
		t.Fatalf("SetWriteDeadline calls = %d, want 1", rc.calls)
	}
	if d := rc.deadline.Sub(before); d < sseWriteTimeout || d > sseWriteTimeout+time.Second {
		t.Fatalf("deadline offset = %v, want about %v", d, sseWriteTimeout)
	}
	if rec.Body.String() != "data: x\n\n" || !rec.Flushed {
		t.Fatalf("body=%q flushed=%v", rec.Body.String(), rec.Flushed)
	}
}

func TestWriteSSEFrameIgnoresUnsupportedDeadline(t *testing.T) {
	rec := httptest.NewRecorder()
	// A real ResponseController over a recorder reports ErrNotSupported.
	if !writeSSEFrame(rec, http.NewResponseController(rec), rec, ": heartbeat\n\n") {
		t.Fatal("writeSSEFrame() = false, want ErrNotSupported ignored")
	}
	if rec.Body.String() != ": heartbeat\n\n" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestWriteSSEFrameStopsWhenDeadlineCannotBeSet(t *testing.T) {
	rec := httptest.NewRecorder()
	if writeSSEFrame(rec, &fakeDeadline{err: errors.New("use of closed network connection")}, rec, "data: x\n\n") {
		t.Fatal("writeSSEFrame() = true, want the stream stopped")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %q, want nothing written", rec.Body.String())
	}
}

// gin's ResponseWriter exposes Unwrap, so the controller reaches the real
// connection and the deadline is applied rather than silently unsupported.
func TestSSEDeadlineReachesConnectionThroughGinWriter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	deadlineErr := make(chan error, 1)
	router.GET("/stream", func(c *gin.Context) {
		rc := http.NewResponseController(c.Writer)
		deadlineErr <- rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
		_ = rc.SetWriteDeadline(time.Time{})
		c.Status(http.StatusNoContent)
	})
	server := httptest.NewServer(router)
	defer server.Close()
	resp, err := http.Get(server.URL + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if err := <-deadlineErr; err != nil {
		t.Fatalf("SetWriteDeadline through gin writer = %v, want nil", err)
	}
}
