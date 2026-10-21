package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"opensync/internal/config"
	"opensync/internal/model"
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

// --- round 2 fixes: oversized body, path segments, notify test timeout, SSE ---

// A chunked body has no Content-Length, so the middleware pre-check cannot
// reject it; the MaxBytesReader error must still answer 413 rather than look
// like a malformed parameter.
func TestBindJSONAnswers413ForOversizedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/svr/x", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32)
		var req map[string]interface{}
		bindJSON(c, &req)
	})
	body := `{"key":"` + strings.Repeat("x", 256) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/svr/x", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = -1
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", w.Code)
	}
	code, message := decodeEnvelope(t, w.Body.Bytes())
	if code != http.StatusRequestEntityTooLarge || message != msg.T(msg.RequestTooLarge) {
		t.Fatalf("response = %s, want the request-too-large envelope", w.Body.String())
	}
}

func TestUpdateSystemConfigAnswers413ForOversizedBody(t *testing.T) {
	useSystemConfig(t, t.TempDir())
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.PUT("/svr/system/config", func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32)
		UpdateSystemConfig(c)
	})
	body := `{"taskTimeout":48,"pad":"` + strings.Repeat("x", 256) + `"}`
	req := httptest.NewRequest(http.MethodPut, "/svr/system/config", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = -1
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body=%s, want 413", w.Code, w.Body.String())
	}
}

// Legal directory names may contain two dots; only a ".." segment traverses.
func TestValidateJobFieldsAcceptsDotsInDirectoryNames(t *testing.T) {
	for _, paths := range []interface{}{
		"/Photos/backup..2024",
		[]interface{}{"/Photos/backup..2024", "/Photos/监控..2024"},
		[]string{"/Photos/a..b"},
	} {
		req := map[string]interface{}{"srcPath": paths, "dstPath": paths}
		if err := validateJobFields(req); err != nil {
			t.Fatalf("validateJobFields(%#v) = %v, want the names accepted", paths, err)
		}
	}
}

func TestValidateJobFieldsRejectsParentPathSegments(t *testing.T) {
	for _, paths := range []interface{}{
		"/Photos/../etc",
		"../etc",
		"/Photos/..",
		`..\windows`,
		[]interface{}{"/Photos", ".."},
		[]interface{}{"/Photos", "../etc"},
	} {
		req := map[string]interface{}{"srcPath": paths}
		if err := validateJobFields(req); err == nil {
			t.Fatalf("validateJobFields(%#v) = nil, want a traversal rejection", paths)
		}
	}
}

// A slow provider must not hold the HTTP request indefinitely.
func TestTestNotifyTimesOutAndAnswersWithGenericFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	release := make(chan struct{})
	defer close(release)
	oldTimeout, oldService := notifyTestTimeout, testNotifyService
	notifyTestTimeout = 50 * time.Millisecond
	testNotifyService = func(map[string]interface{}) error {
		<-release
		return nil
	}
	t.Cleanup(func() {
		notifyTestTimeout = oldTimeout
		testNotifyService = oldService
	})

	router := gin.New()
	router.POST("/svr/notify/test", TestNotify)
	body := `{"notify":{"method":1,"params":{"sendKey":"k"}}}`
	req := httptest.NewRequest(http.MethodPost, "/svr/notify/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	code, message := decodeEnvelope(t, w.Body.Bytes())
	if code != 500 || message != msg.T(msg.NotifySendFail) {
		t.Fatalf("response = %s, want the generic send-failure envelope", w.Body.String())
	}
}

func TestTestNotifyMapsPublicServiceError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	old := testNotifyService
	// A public error is actionable and must be shown verbatim.
	testNotifyService = func(map[string]interface{}) error {
		return model.PublicError("配置无效")
	}
	t.Cleanup(func() { testNotifyService = old })

	router := gin.New()
	router.POST("/svr/notify/test", TestNotify)
	req := httptest.NewRequest(http.MethodPost, "/svr/notify/test", strings.NewReader(`{"notify":{"method":1}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	code, message := decodeEnvelope(t, w.Body.Bytes())
	if code != 500 || message != "配置无效" {
		t.Fatalf("response = %s, want the public message", w.Body.String())
	}
}

func TestTestNotifyAnswersOKOnSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	old := testNotifyService
	testNotifyService = func(map[string]interface{}) error { return nil }
	t.Cleanup(func() { testNotifyService = old })

	router := gin.New()
	router.POST("/svr/notify/test", TestNotify)
	req := httptest.NewRequest(http.MethodPost, "/svr/notify/test", strings.NewReader(`{"notify":{"method":1}}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	code, _ := decodeEnvelope(t, w.Body.Bytes())
	if code != 200 {
		t.Fatalf("response = %s, want success", w.Body.String())
	}
}

func stubSubscribeJobProgress(t *testing.T, fn func(int64) (<-chan []byte, []byte, bool, error)) {
	t.Helper()
	old := subscribeJobProgress
	subscribeJobProgress = fn
	t.Cleanup(func() {
		subscribeJobProgress = old
		globalSSEConns.Store(0)
	})
}

func TestStreamJobCurrentWritesInitialSnapshotThenStops(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var gotID int64
	stubSubscribeJobProgress(t, func(jobID int64) (<-chan []byte, []byte, bool, error) {
		gotID = jobID
		ch := make(chan []byte)
		close(ch) // no further frames: the handler must write the snapshot and return
		return ch, []byte(`{"code":200,"data":{"taskId":1}}`), true, nil
	})
	router := gin.New()
	router.GET("/svr/job/stream", StreamJobCurrent)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/svr/job/stream?id=7", nil))

	if gotID != 7 {
		t.Fatalf("subscribe jobID = %d, want 7", gotID)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	if !strings.Contains(w.Body.String(), `data: {"code":200`) {
		t.Fatalf("body = %q, want the initial snapshot frame", w.Body.String())
	}
	if active := globalSSEConns.Load(); active != 0 {
		t.Fatalf("globalSSEConns = %d after the stream ended, want 0", active)
	}
}

func TestStreamJobCurrentReportsBuildFailureAsJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stubSubscribeJobProgress(t, func(int64) (<-chan []byte, []byte, bool, error) {
		return nil, nil, true, errors.New("database is locked")
	})
	router := gin.New()
	router.GET("/svr/job/stream", StreamJobCurrent)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/svr/job/stream?id=7", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if !strings.Contains(w.Body.String(), msg.T(msg.InternalError)) {
		t.Fatalf("body = %q, want the localized internal error", w.Body.String())
	}
}

func TestStreamJobCurrentReportsSubscriptionLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stubSubscribeJobProgress(t, func(int64) (<-chan []byte, []byte, bool, error) {
		return nil, nil, false, nil
	})
	router := gin.New()
	router.GET("/svr/job/stream", StreamJobCurrent)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/svr/job/stream?id=7", nil))

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", w.Code)
	}
}
