package handler

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"opensync/internal/msg"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func serveJSON(t *testing.T, method, path string, h gin.HandlerFunc, body string) (int, int, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Handle(method, path, h)
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	code, message := decodeEnvelope(t, w.Body.Bytes())
	return w.Code, code, message
}

func TestStrictInt64(t *testing.T) {
	accept := map[string]interface{}{
		"int": 3, "float integral": float64(7), "string": " 12 ", "json number": json.Number("42"), "negative": "-1",
	}
	for name, v := range accept {
		if _, ok := strictInt64(v); !ok {
			t.Errorf("%s: strictInt64(%#v) rejected", name, v)
		}
	}
	reject := map[string]interface{}{
		"fraction": 1.9, "junk": "abc", "float string": "1.5", "bool": true, "nil": nil, "object": map[string]interface{}{}, "empty": "",
		"float64 2^63": float64(1 << 63), "float64 below MinInt64": float64(math.MinInt64) - 2048,
	}
	for name, v := range reject {
		if _, ok := strictInt64(v); ok {
			t.Errorf("%s: strictInt64(%#v) accepted", name, v)
		}
	}
}

func TestValidateJobFieldsRejectsNonIntegerNumbers(t *testing.T) {
	for _, req := range []map[string]interface{}{
		{"interval": "abc"},
		{"interval": 1.5},
		{"interval": -1},
		{"method": "abc"},
		{"method": 1.9},
		{"method": 4},
	} {
		if err := validateJobFields(req); err == nil {
			t.Errorf("validateJobFields(%v) = nil, want rejection", req)
		}
	}
	for _, req := range []map[string]interface{}{
		{"interval": float64(1440), "method": float64(2)},
		{"interval": float64(60), "method": float64(3)},
		{"interval": "60", "method": "0"},
		{"interval": nil, "method": ""},
	} {
		if err := validateJobFields(req); err != nil {
			t.Errorf("validateJobFields(%v) = %v, want accepted", req, err)
		}
	}
}

func TestAddJobRejectsMalformedEditID(t *testing.T) {
	for _, body := range []string{`{"id":"abc"}`, `{"id":1.5}`, `{"id":0}`, `{"id":true}`} {
		status, code, message := serveJSON(t, http.MethodPost, "/svr/job", AddJob, body)
		if status != http.StatusBadRequest || code != 500 || message != msg.T(msg.LostPart) {
			t.Errorf("POST %s = %d/%d %q, want 400 LostPart", body, status, code, message)
		}
	}
}

func TestAddJobReturnsCreatedID(t *testing.T) {
	oldCreate := createJobService
	createJobService = func(job map[string]interface{}) (int64, error) {
		return 77, nil
	}
	t.Cleanup(func() { createJobService = oldCreate })

	router := gin.New()
	router.POST("/svr/job", AddJob)
	req := httptest.NewRequest(http.MethodPost, "/svr/job", strings.NewReader(`{"remark":"测试","alistId":1,"srcPath":["/a"],"dstPath":["/b"]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("POST /svr/job status = %d, want 200 (body=%s)", w.Code, w.Body.String())
	}
	var resp struct {
		Code int              `json:"code"`
		Data map[string]int64 `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Code != 200 || resp.Data["id"] != 77 {
		t.Fatalf("POST /svr/job response = %+v, want code=200 data.id=77", resp)
	}
}

func TestFlexibleIDAcceptsStringAndNumber(t *testing.T) {
	var req struct {
		ID *flexibleID `json:"id"`
	}
	for body, want := range map[string]int64{`{"id":"5"}`: 5, `{"id":5}`: 5, `{"id":9007199254740993}`: 9007199254740993} {
		req.ID = nil
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("Unmarshal(%s) error: %v", body, err)
		}
		got, err := req.ID.parse()
		if err != nil || got != want {
			t.Errorf("%s: parse() = %d, %v; want %d", body, got, err, want)
		}
	}
	for _, body := range []string{`{"id":null}`, `{"id":"x"}`, `{"id":2.5}`, `{"id":-3}`, `{}`} {
		req.ID = nil
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("Unmarshal(%s) error: %v", body, err)
		}
		if _, err := req.ID.parse(); err == nil {
			t.Errorf("%s: parse() = nil error, want rejection", body)
		}
	}
}

// A numeric id used to fail ShouldBindJSON into *string and answer LostPart
// for a well-formed request. Verify both that a fractional taskId is rejected
// and that a valid numeric taskId reaches the action handler with the exact id.
func TestUpdateJobAcceptsNumericTaskIDAndRejectsFraction(t *testing.T) {
	status, _, message := serveJSON(t, http.MethodPut, "/svr/job", UpdateJob, `{"taskId":1.5,"action":"stop"}`)
	if status != http.StatusBadRequest || message != msg.T(msg.LostPart) {
		t.Fatalf("fractional taskId = %d %q, want 400 LostPart", status, message)
	}

	var gotID int64
	oldStop := stopTaskService
	stopTaskService = func(taskID int64) error {
		gotID = taskID
		return nil
	}
	t.Cleanup(func() { stopTaskService = oldStop })

	status, code, _ := serveJSON(t, http.MethodPut, "/svr/job", UpdateJob, `{"taskId":7,"action":"stop"}`)
	if status != http.StatusOK || code != 200 || gotID != 7 {
		t.Fatalf("numeric taskId stop = status %d code %d gotID=%d, want 200/200 gotID=7", status, code, gotID)
	}
	status, _, message = serveJSON(t, http.MethodPut, "/svr/job", UpdateJob, `{"taskId":7,"action":"nope"}`)
	if status != http.StatusBadRequest || message != msg.T(msg.LostPart) {
		t.Fatalf("unknown action = %d %q, want 400 LostPart", status, message)
	}
}

func TestHandlersRejectJSONNullBodyWithoutNilMapPanic(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{"AddJob", http.MethodPost, "/svr/job", AddJob},
		{"AddAlist", http.MethodPost, "/svr/alist", AddAlist},
		{"UpdateAlist", http.MethodPut, "/svr/alist", UpdateAlist},
		{"UpdateNotify", http.MethodPut, "/svr/notify", UpdateNotify},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code, message := serveJSON(t, tc.method, tc.path, tc.handler, "null")
			if status != http.StatusBadRequest || code != 500 || message != msg.T(msg.LostPart) {
				t.Fatalf("%s(null) = %d/%d %q, want 400 LostPart", tc.name, status, code, message)
			}
		})
	}
}

func TestUpdateJobRejectsMalformedPayloadsWithoutRunningJob(t *testing.T) {
	payloads := []string{
		`null`,
		`{"id":1,"action":"stop"}`,
		`{"id":1,"abort":true}`,
		`{"id":1,"pasue":true}`,
		`{"id":1,"pause":false,"abort":true}`,
		`{"id":1,"taskId":2,"action":"stop"}`,
		`{"id":1}{}`,
	}
	for _, body := range payloads {
		status, code, message := serveJSON(t, http.MethodPut, "/svr/job", UpdateJob, body)
		if status != http.StatusBadRequest || code != 500 || message != msg.T(msg.LostPart) {
			t.Errorf("PUT /svr/job %s = %d/%d %q, want 400 LostPart", body, status, code, message)
		}
	}
}

func TestUpdateNotifyEditRequiresID(t *testing.T) {
	for _, body := range []string{
		`{"notify":{"method":0,"params":"{}"}}`,
		`{"notify":{"id":"abc","method":0}}`,
		`{"notify":{"id":0,"method":0}}`,
		`{"notifyId":1.5,"enable":1}`,
	} {
		status, code, message := serveJSON(t, http.MethodPut, "/svr/notify", UpdateNotify, body)
		if status != http.StatusBadRequest || code != 500 || message != msg.T(msg.LostPart) {
			t.Errorf("PUT %s = %d/%d %q, want 400 LostPart", body, status, code, message)
		}
	}
}

func TestDeleteAlistRequiresQueryID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.DELETE("/svr/alist", DeleteAlist)
	req := httptest.NewRequest(http.MethodDelete, "/svr/alist", strings.NewReader("id=3"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if _, message := decodeEnvelope(t, w.Body.Bytes()); w.Code != http.StatusBadRequest || message != msg.T(msg.LostPart) {
		t.Fatalf("form-body id = %d %s, want 400 LostPart (query id only)", w.Code, w.Body.String())
	}
}
