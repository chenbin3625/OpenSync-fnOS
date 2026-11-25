package handler

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"opensync/internal/mapper"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	_ "modernc.org/sqlite"
)

func setupHandlerFeatureDB(t *testing.T) func() {
	t.Helper()
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	testDB.SetMaxOpenConns(1)
	restore := mapper.SetDBForTest(testDB)
	mapper.InitSQL()
	return func() {
		restore()
		_ = testDB.Close()
	}
}

func TestNewFeatureHTTPHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cleanup := setupHandlerFeatureDB(t)
	defer cleanup()

	router := gin.New()
	router.GET("/svr/dashboard", GetDashboard)
	router.GET("/svr/system/export", ExportSystemConfig)
	router.POST("/svr/system/import", ImportSystemConfig)
	router.GET("/svr/system/logs", GetSystemLogs)
	router.GET("/svr/system/logs/download", DownloadSystemLogs)
	router.GET("/svr/alist/health", GetAlistHealth)
	router.POST("/svr/job/filter/test", TestJobFilter)
	router.POST("/svr/job/tasks/delete", DeleteTasksBatch)
	router.GET("/svr/job/items/export", ExportTaskItems)

	// 1. Dashboard endpoint
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/svr/dashboard", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /svr/dashboard status = %d, body = %s", w.Code, w.Body.String())
	}
	var dashResp struct {
		Code int                    `json:"code"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &dashResp); err != nil || dashResp.Code != 200 {
		t.Fatalf("invalid dashboard response: %s", w.Body.String())
	}

	// 2. Filter tester endpoint
	filterBody := `{"filterMode":1,"include":"*.jpg","samplePath":"a.jpg","sampleSize":100,"sampleAgeMinutes":10}`
	req := httptest.NewRequest(http.MethodPost, "/svr/job/filter/test", strings.NewReader(filterBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"willSync":true`) {
		t.Fatalf("POST /svr/job/filter/test status=%d body=%s", w.Code, w.Body.String())
	}

	// 3. System export & import endpoints
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/svr/system/export?includeSecrets=0", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"version":1`) {
		t.Fatalf("GET /svr/system/export status=%d body=%s", w.Code, w.Body.String())
	}

	importPayload := `{"conflictMode":"skip","payload":{"version":1,"engines":[{"id":1,"remark":"E0","url":"http://127.0.0.1:5244","userName":"u0","token":""}],"notifications":[],"jobs":[]}}`
	req = httptest.NewRequest(http.MethodPost, "/svr/system/import", bytes.NewBufferString(importPayload))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"code":200`) {
		t.Fatalf("POST /svr/system/import status=%d body=%s", w.Code, w.Body.String())
	}

	// 4. Logs endpoints
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/svr/system/logs?lines=50", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"lines"`) {
		t.Fatalf("GET /svr/system/logs status=%d body=%s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/svr/system/logs/download", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("GET /svr/system/logs/download status=%d headers=%v", w.Code, w.Header())
	}

	// 5. Batch delete & items export endpoints
	alistID, _ := mapper.AddAlistWithConcurrency("E", "http://127.0.0.1:5244", "u", "t", 0)
	jobID, _ := mapper.AddJob(map[string]interface{}{
		"enable": 1, "remark": "H", "srcPath": `["/s"]`, "dstPath": `["/d"]`,
		"alistId": alistID, "method": 0, "isCron": 2,
	})
	taskID, _ := mapper.AddJobTask(jobID, time.Now().Unix())
	_ = mapper.UpdateJobTaskStatusAndNum(taskID, 2, nil, `{"allNum":1,"successNum":1,"failNum":0}`)
	_ = mapper.AddJobTaskItemMany([]map[string]interface{}{
		{"taskId": taskID, "srcPath": "/s", "dstPath": "/d", "isPath": 0, "fileName": "ok.txt", "fileSize": int64(12), "type": 0, "status": 2},
	})

	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/svr/job/items/export?taskId=1&format=txt", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "ok.txt") {
		t.Fatalf("GET /svr/job/items/export status=%d body=%s", w.Code, w.Body.String())
	}

	delReq := httptest.NewRequest(http.MethodPost, "/svr/job/tasks/delete", strings.NewReader(`{"taskIds":[1]}`))
	delReq.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, delReq)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"deleted":1`) {
		t.Fatalf("POST /svr/job/tasks/delete status=%d body=%s", w.Code, w.Body.String())
	}

	// 6. Health endpoint
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/svr/alist/health", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"code":200`) {
		t.Fatalf("GET /svr/alist/health status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestPendingApprovalHTTPHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cleanup := setupHandlerFeatureDB(t)
	defer cleanup()

	router := gin.New()
	router.GET("/svr/job/approval", GetPendingApproval)
	router.PUT("/svr/job", UpdateJob)

	// List all pending approvals when empty.
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/svr/job/approval", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"data":[]`) {
		t.Fatalf("GET /svr/job/approval status=%d body=%s", w.Code, w.Body.String())
	}

	// Single job lookup when none pending returns data:null.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/svr/job/approval?jobId=42", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"data":null`) {
		t.Fatalf("GET /svr/job/approval?jobId=42 status=%d body=%s", w.Code, w.Body.String())
	}

	// Invalid jobId is rejected with HTTP 400.
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/svr/job/approval?jobId=bad", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("GET /svr/job/approval?jobId=bad status=%d, want 400 (body=%s)", w.Code, w.Body.String())
	}

	// Approving/rejecting when none pending returns friendly error envelope.
	for _, action := range []string{"approve", "reject"} {
		req := httptest.NewRequest(http.MethodPut, "/svr/job", strings.NewReader(`{"taskId":9999,"action":"`+action+`"}`))
		req.Header.Set("Content-Type", "application/json")
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if !strings.Contains(w.Body.String(), `"code":500`) || !strings.Contains(w.Body.String(), "当前没有待确认的删除项") {
			t.Fatalf("PUT /svr/job %s status=%d body=%s", action, w.Code, w.Body.String())
		}
	}
}
