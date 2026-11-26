package handler

import (
	"net/http"
	"opensync/internal/msg"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestNativeStorageHandlersRejectMalformedOrUnknownFields(t *testing.T) {
	cases := []struct {
		name    string
		method  string
		path    string
		handler gin.HandlerFunc
		body    string
	}{
		{"SaveNativeEngine unknown field", http.MethodPut, "/svr/native/engine", SaveNativeEngine, `{"remark":"x","bogus":1}`},
		{"UpdateNativeStorage unknown field", http.MethodPut, "/svr/native/storage", UpdateNativeStorage, `{"name":"a","options":{"type":"s3"},"extra":true}`},
		{"PreviewNativeImport unknown field", http.MethodPost, "/svr/native/storage/import/preview", PreviewNativeImport, `{"config":"[a]","foo":1}`},
		{"ImportNativeStorage unknown field", http.MethodPost, "/svr/native/storage/import", ImportNativeStorage, `{"config":"[a]","foo":1}`},
		{"TestNativeStorage unknown field", http.MethodPost, "/svr/native/storage/test", TestNativeStorage, `{"name":"a","foo":1}`},
		{"RenameNativeStorage unknown field", http.MethodPost, "/svr/native/storage/rename", RenameNativeStorage, `{"oldName":"a","newName":"b","extra":1}`},
		{"ExportNativeConfig unknown field", http.MethodPost, "/svr/native/storage/export", ExportNativeConfig, `{"names":["a"],"bogus":true}`},
		{"AboutNativeStorage unknown field", http.MethodPost, "/svr/native/storage/about", AboutNativeStorage, `{"name":"a","bogus":true}`},
		{"BrowseNativeStorage unknown field", http.MethodPost, "/svr/native/storage/browse", BrowseNativeStorage, `{"path":"/a","bogus":true}`},
		{"StartAsyncRestoreNativeFile unknown field", http.MethodPost, "/svr/native/storage/transfers/restore", StartAsyncRestoreNativeFile, `{"srcPath":"/a","dstLocalDir":"/b","bogus":true}`},
		{"MkdirNativeStorage unknown field", http.MethodPost, "/svr/native/storage/mkdir", MkdirNativeStorage, `{"path":"/a/b","bogus":1}`},
		{"MoveNativeStoragePath unknown field", http.MethodPost, "/svr/native/storage/move", MoveNativeStoragePath, `{"srcPath":"/a","dstPath":"/b","bogus":1}`},
		{"DeleteNativeStoragePath unknown field", http.MethodPost, "/svr/native/storage/delete", DeleteNativeStoragePath, `{"path":"/a","bogus":1}`},
		{"SizeNativeStorage unknown field", http.MethodPost, "/svr/native/storage/size", SizeNativeStorage, `{"path":"/a","bogus":1}`},
		{"StartNativeStorageAuth unknown field", http.MethodPost, "/svr/native/storage/auth/start", StartNativeStorageAuth, `{"type":"onedrive","bogus":1}`},
		{"SubmitNativeStorageAuthCallback unknown field", http.MethodPost, "/svr/native/storage/auth/callback", SubmitNativeStorageAuthCallback, `{"sessionId":"s1","callbackUrl":"http://localhost:53682/?code=a","bogus":1}`},
		{"ContinueNativeStorageAuth unknown field", http.MethodPost, "/svr/native/storage/auth/continue", ContinueNativeStorageAuth, `{"sessionId":"s1","state":"st","answer":"ok","bogus":1}`},
		{"StartJobCheck unknown field", http.MethodPost, "/svr/job/check", StartJobCheck, `{"id":1,"download":true,"bogus":1}`},
		{"ResolveJobConflict unknown field", http.MethodPost, "/svr/job/conflicts/resolve", ResolveJobConflict, `{"jobId":1,"conflictId":1,"action":"keep_both","bogus":1}`},
		{"SaveNativeEngine trailing json", http.MethodPut, "/svr/native/engine", SaveNativeEngine, `{"remark":"a"}{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, code, message := serveJSON(t, tc.method, tc.path, tc.handler, tc.body)
			if status != http.StatusBadRequest || code != 500 || message != msg.T(msg.LostPart) {
				t.Fatalf("%s = %d/%d %q, want 400/500 LostPart", tc.name, status, code, message)
			}
		})
	}
}

func TestDeleteNativeStorageRequiresQueryName(t *testing.T) {
	status, code, message := serveJSON(t, http.MethodDelete, "/svr/native/storage", DeleteNativeStorage, "")
	if status != http.StatusBadRequest || code != 500 || message != msg.T(msg.LostPart) {
		t.Fatalf("DELETE /svr/native/storage without name = %d/%d %q, want 400/500 LostPart", status, code, message)
	}
}

func TestNativeStorageAuthQueryRequiresSessionID(t *testing.T) {
	for _, tc := range []struct {
		method  string
		path    string
		handler gin.HandlerFunc
	}{
		{http.MethodGet, "/svr/native/storage/auth/status", GetNativeStorageAuth},
		{http.MethodDelete, "/svr/native/storage/auth", CancelNativeStorageAuth},
	} {
		status, code, message := serveJSON(t, tc.method, tc.path, tc.handler, "")
		if status != http.StatusBadRequest || code != 500 || message != msg.T(msg.LostPart) {
			t.Fatalf("%s %s without sessionId = %d/%d %q, want 400/500 LostPart", tc.method, tc.path, status, code, message)
		}
	}
}

func TestJobCheckRequiresID(t *testing.T) {
	for _, tc := range []struct {
		method  string
		handler gin.HandlerFunc
		body    string
	}{
		{http.MethodPost, StartJobCheck, `{}`},
		{http.MethodGet, GetJobCheck, ``},
		{http.MethodDelete, CancelJobCheck, ``},
	} {
		status, code, message := serveJSON(t, tc.method, "/svr/job/check", tc.handler, tc.body)
		if status != http.StatusBadRequest || code != 500 || message != msg.T(msg.LostPart) {
			t.Fatalf("%s /svr/job/check without id = %d/%d %q, want 400/500 LostPart", tc.method, status, code, message)
		}
	}
}

func TestUpdateSystemConfigValidatesBwLimitAndProxyURL(t *testing.T) {
	for _, body := range []string{
		`{"defaultBwLimit":"not-a-speed"}`,
		`{"defaultBwLimit":"10MB"}`,
		`{"defaultBwLimit":"08:00,5MB 23:00,off"}`,
		`{"proxyUrl":"ftp://127.0.0.1:21"}`,
		`{"proxyUrl":"not a url"}`,
	} {
		status, code, _ := serveJSON(t, http.MethodPut, "/svr/system/config", UpdateSystemConfig, body)
		if status != http.StatusBadRequest || code != 500 {
			t.Fatalf("PUT /svr/system/config %s = %d/%d, want 400/500 rejection", body, status, code)
		}
	}
}
