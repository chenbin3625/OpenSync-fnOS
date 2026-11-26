package native

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"opensync/internal/pan123"
)

func TestPan123ConfigValidationAndMetadata(t *testing.T) {
	if !IsSupportedType("pan123") {
		t.Fatal("expected pan123 to be a supported remote type")
	}
	for _, secretKey := range []string{"client_secret", "access_token", "web_password", "direct_link_key"} {
		if !IsSecretField(secretKey) {
			t.Fatalf("expected %q to be in SecretFields", secretKey)
		}
	}

	// OpenAPI mode valid with client_id + client_secret.
	openCfg := Config{
		"p123": {
			"type":          "pan123",
			"protocol":      "open_api",
			"client_id":     "cid",
			"client_secret": "csec",
		},
	}
	if err := openCfg.Validate(); err != nil {
		t.Fatalf("valid open_api config failed: %v", err)
	}
	if !openCfg.UsesBuiltInDriver("p123") {
		t.Fatal("expected pan123 to be marked as built-in driver")
	}
	if _, err := openCfg.ExportINI(context.Background(), []string{"p123"}, ""); err == nil {
		t.Fatal("expected ExportINI to reject direct export of built-in pan123 remote")
	}

	// Web mode valid with web_user + web_password or access_token.
	webCfg := Config{
		"p123web": {
			"type":         "pan123",
			"protocol":     "web",
			"web_user":     "u@example.com",
			"web_password": "pw",
		},
	}
	if err := webCfg.Validate(); err != nil {
		t.Fatalf("valid web config failed: %v", err)
	}

	// Missing credentials rejected.
	badOpen := Config{"p123": {"type": "pan123", "protocol": "open_api"}}
	if err := badOpen.Validate(); err == nil {
		t.Fatal("expected open_api without credentials to fail validation")
	}
	badWeb := Config{"p123": {"type": "pan123", "protocol": "web"}}
	if err := badWeb.Validate(); err == nil {
		t.Fatal("expected web without credentials to fail validation")
	}
	badRoot := Config{"p123": {"type": "pan123", "access_token": "tok", "root_folder_id": "abc"}}
	if err := badRoot.Validate(); err == nil {
		t.Fatal("expected non-numeric root_folder_id to fail validation")
	}

	// Compare mode: local <-> pan123 must default to Size+MD5 (2), NOT size-only (1).
	localEp := Endpoint{IsLocal: true, Backend: "local"}
	panEp := Endpoint{Backend: "pan123", UnreliableModTime: true}
	if !panEp.SupportsNativeMD5() {
		t.Fatal("expected pan123 to support native MD5")
	}
	if panEp.HasReliableModTime() {
		t.Fatal("expected pan123 to report unreliable ModTime")
	}
	if got := EffectiveCompareMode(localEp, panEp, 0); got != 2 {
		t.Fatalf("EffectiveCompareMode(local, pan123, 0) = %d, want 2 (Size+MD5)", got)
	}

	// Summary labels distinguish OpenAPI vs Web.
	if s := RemoteSummary(openCfg["p123"]); !strings.Contains(s, "开放平台") {
		t.Fatalf("unexpected open_api summary: %q", s)
	}
	if s := RemoteSummary(webCfg["p123web"]); !strings.Contains(s, "个人盘 Web") {
		t.Fatalf("unexpected web summary: %q", s)
	}
}

func TestPan123BridgeDAVAndDirectUpload(t *testing.T) {
	ctx := context.Background()

	var (
		mu       sync.Mutex
		nextFID  = int64(10)
		children = map[int64][]map[string]any{}
		payloads = map[int64][]byte{}
	)
	seedData := []byte("bridge-dav-payload")
	seedSum := md5.Sum(seedData)
	seedMD5 := hex.EncodeToString(seedSum[:])
	children[0] = []map[string]any{{
		"fileId":       int64(1),
		"filename":     "seed.txt",
		"type":         0,
		"size":         int64(len(seedData)),
		"etag":         seedMD5,
		"parentFileId": int64(0),
		"trashed":      0,
		"updateAt":     "2026-10-01 10:00:00",
	}}
	payloads[1] = seedData

	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/cdn/1":
			w.Header().Set("Content-Length", strconv.Itoa(len(payloads[1])))
			_, _ = w.Write(payloads[1])
		case "/api/v2/file/list":
			pid, _ := strconv.ParseInt(r.URL.Query().Get("parentFileId"), 10, 64)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"lastFileId": -1, "fileList": children[pid]},
			})
		case "/api/v1/file/download_info":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"downloadUrl": srvURL + "/cdn/1"},
			})
		case "/upload/v1/file/mkdir":
			var req struct {
				Name     string `json:"name"`
				ParentID int64  `json:"parentID"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			nextFID++
			id := nextFID
			children[req.ParentID] = append(children[req.ParentID], map[string]any{
				"fileId": id, "filename": req.Name, "type": 1, "size": 0, "parentFileId": req.ParentID, "trashed": 0,
			})
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"dirID": id}})
		case "/upload/v2/file/domain":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": []string{srvURL}})
		case "/upload/v2/file/single/create":
			_ = r.ParseMultipartForm(4 << 20)
			pid, _ := strconv.ParseInt(r.FormValue("parentFileID"), 10, 64)
			fname := r.FormValue("filename")
			etag := r.FormValue("etag")
			f, _, _ := r.FormFile("file")
			body, _ := io.ReadAll(f)
			_ = f.Close()
			nextFID++
			id := nextFID
			payloads[id] = body
			children[pid] = append(children[pid], map[string]any{
				"fileId": id, "filename": fname, "type": 0, "size": int64(len(body)), "etag": etag, "parentFileId": pid, "trashed": 0,
			})
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"fileID": id, "completed": true},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	restore := setPan123OptionsHookForTest(func(o *pan123.Options) {
		o.OpenAPIBaseURL = srv.URL
		o.TPSLimit = 20
	})
	defer restore()

	params := map[string]string{
		"type":         "pan123",
		"protocol":     "open_api",
		"access_token": "static-tok",
	}
	client, err := newPan123ClientFromParams(params, SessionOptions{}, nil)
	if err != nil {
		t.Fatalf("newPan123ClientFromParams failed: %v", err)
	}
	defer client.Close()

	// Exercise BridgeFS OwnCloud DAV PROPFIND checksum passthrough + GET.
	h := serveBridgeDAV(&pan123BridgeFS{client: client}, "/dav", diskFullWrite, true)
	req := httptest.NewRequest("PROPFIND", "/dav/", bytes.NewBufferString(`<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns"><d:prop><oc:checksums/></d:prop></d:propfind>`))
	req.Header.Set("Depth", "1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND status = %d, want 207", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<oc:checksum>MD5:"+seedMD5+"</oc:checksum>") {
		t.Fatalf("PROPFIND body missing MD5 checksum: %s", rec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/dav/seed.txt", nil)
	getRec := httptest.NewRecorder()
	h.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK || getRec.Body.String() != string(seedData) {
		t.Fatalf("GET /dav/seed.txt = %d %q", getRec.Code, getRec.Body.String())
	}

	// Exercise directUploadFilesToPan123 fast path.
	srcDir := t.TempDir()
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", srcDir)
	roots := authorizedRoots()
	if len(roots) == 0 {
		t.Fatal("expected authorized root for temp dir")
	}

	upContent := []byte("direct-upload-hello-123pan")
	spacedContent := []byte("filename with a trailing space")
	if err := os.WriteFile(filepath.Join(srcDir, "note.txt"), upContent, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "note.txt "), spacedContent, 0o600); err != nil {
		t.Fatal(err)
	}
	var events []Event
	err = directUploadFilesToPan123(
		ctx,
		roots[0].ID,
		"p123",
		params,
		"backup ",
		[]resumeFileMeta{
			{Rel: "note.txt", Size: int64(len(upContent)), ModTime: time.Now().Unix()},
			{Rel: "note.txt ", Size: int64(len(spacedContent)), ModTime: time.Now().Unix()},
		},
		CopyOptions{Transfers: 1, ResumeStateDir: t.TempDir()},
		func(ev Event) { events = append(events, ev) },
	)
	if err != nil {
		t.Fatalf("directUploadFilesToPan123 failed: %v", err)
	}
	foundDone := false
	for _, ev := range events {
		if ev.File == "note.txt" && !ev.Failed {
			foundDone = true
		}
	}
	if !foundDone {
		t.Fatalf("expected completed event for note.txt, got %+v", events)
	}
	mu.Lock()
	defer mu.Unlock()
	var backupID int64
	for _, item := range children[0] {
		if item["filename"] == "backup " {
			backupID = item["fileId"].(int64)
		}
	}
	contents := make(map[string]string)
	for _, item := range children[backupID] {
		contents[item["filename"].(string)] = string(payloads[item["fileId"].(int64)])
	}
	if backupID == 0 || len(contents) != 2 || contents["note.txt"] != string(upContent) || contents["note.txt "] != string(spacedContent) {
		t.Fatalf("expected distinct remote paths and contents, got %v", contents)
	}
}
