package handler

import (
	"encoding/json"
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
		{"method": 3},
	} {
		if err := validateJobFields(req); err == nil {
			t.Errorf("validateJobFields(%v) = nil, want rejection", req)
		}
	}
	for _, req := range []map[string]interface{}{
		{"interval": float64(1440), "method": float64(2)},
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
// for a well-formed request. It must now reach the action (and fail later for
// other reasons, which is outside this test), not be rejected at binding.
func TestUpdateJobAcceptsNumericTaskIDAndRejectsFraction(t *testing.T) {
	status, _, message := serveJSON(t, http.MethodPut, "/svr/job", UpdateJob, `{"taskId":1.5,"action":"stop"}`)
	if status != http.StatusBadRequest || message != msg.T(msg.LostPart) {
		t.Fatalf("fractional taskId = %d %q, want 400 LostPart", status, message)
	}
	// Unknown action with a valid numeric id is rejected after id parsing,
	// proving the numeric id itself bound successfully.
	status, _, message = serveJSON(t, http.MethodPut, "/svr/job", UpdateJob, `{"taskId":7,"action":"nope"}`)
	if status != http.StatusBadRequest || message != msg.T(msg.LostPart) {
		t.Fatalf("numeric taskId = %d %q, want 400 LostPart from the action switch", status, message)
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
