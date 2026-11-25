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
