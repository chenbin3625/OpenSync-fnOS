package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"opensync/internal/mapper"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestJobItemsHTTPHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cleanup := setupHandlerFeatureDB(t)
	defer cleanup()

	now := time.Now().Unix()
	if _, err := mapper.GetDB().Exec(`INSERT INTO job(id, enable, remark, isCron, createTime) VALUES (1, 1, 'sync-job', 3, ?)`, now); err != nil {
		t.Fatalf("insert job: %v", err)
	}
	if _, err := mapper.GetDB().Exec(`INSERT INTO job_task(id, jobId, status, errMsg, runTime, createTime) VALUES
		(101, 1, 2, '', ?, ?),
		(102, 1, 7, 'remote scan failed', ?, ?)`, now, now-10, now, now-5); err != nil {
		t.Fatalf("insert job_task: %v", err)
	}
	if _, err := mapper.GetDB().Exec(`INSERT INTO job_task_item(id, taskId, jobId, srcPath, dstPath, isPath, fileName, fileSize, type, alistTaskId, status, progress, errMsg, createTime) VALUES
		(1, 101, 1, '/src/a.txt', '/dst/a.txt', 0, 'a.txt', 100, 0, '', 2, 100, '', ?),
		(2, 101, 1, '/src/b.txt', '/dst/b.txt', 0, 'b.txt', 200, 0, '', 7, 10, 'io error', ?)`, now-8, now-7); err != nil {
		t.Fatalf("insert job_task_item: %v", err)
	}

	router := gin.New()
	router.GET("/svr/job/items", GetJobItems)
	router.GET("/svr/job/items/export", ExportTaskItems)
	router.PUT("/svr/job", UpdateJob)

	// 1. Missing jobId rejected
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/svr/job/items", nil))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":500`) {
		t.Fatalf("GET /svr/job/items without jobId = %d %s; want 400 code 500", w.Code, w.Body.String())
	}

	// 2. Job-scoped item query returns items + errorGroups + recentErrors
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/svr/job/items?jobId=1&pageNum=1&pageSize=20", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /svr/job/items status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Count        int                      `json:"count"`
			DataList     []map[string]interface{} `json:"dataList"`
			ErrorGroups  []map[string]interface{} `json:"errorGroups"`
			RecentErrors []map[string]interface{} `json:"recentErrors"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Code != 200 {
		t.Fatalf("invalid response: %v %s", err, w.Body.String())
	}
	if resp.Data.Count != 2 || len(resp.Data.DataList) != 2 {
		t.Fatalf("count/dataList = %d/%d, want 2/2", resp.Data.Count, len(resp.Data.DataList))
	}
	if len(resp.Data.ErrorGroups) != 1 || resp.Data.ErrorGroups[0]["reason"] != "io error" {
		t.Fatalf("errorGroups = %#v", resp.Data.ErrorGroups)
	}
	if len(resp.Data.RecentErrors) != 1 || resp.Data.RecentErrors[0]["errMsg"] != "remote scan failed" {
		t.Fatalf("recentErrors = %#v", resp.Data.RecentErrors)
	}

	// 3. Job-scoped CSV export
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/svr/job/items/export?jobId=1", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Disposition"), "opensync-job-1-items.csv") || !strings.Contains(w.Body.String(), "a.txt") {
		t.Fatalf("GET /svr/job/items/export = %d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
	}

	// 4. PUT /svr/job retry with empty itemIds returns error
	req := httptest.NewRequest(http.MethodPut, "/svr/job", strings.NewReader(`{"id":"1","action":"retry","itemIds":[]}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `"code":500`) {
		t.Fatalf("PUT /svr/job empty itemIds = %d %s; want code 500", w.Code, w.Body.String())
	}
}
