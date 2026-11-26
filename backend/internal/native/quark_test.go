package native

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"opensync/internal/quark"
)

type testQuarkMock struct {
	server *httptest.Server

	mu             sync.Mutex
	nextFID        int
	files          map[string]*testQuarkEntry
	contents       map[string][]byte
	rapidHashes    map[string]bool
	uploads        map[string]*testQuarkUpload
	downloadCalls  atomic.Int64
	putPartCalls   atomic.Int64
	uploadPreCalls atomic.Int64
	emitPuus       string
	qrPolls        atomic.Int32
}

type testQuarkEntry struct {
	FID       string
	ParentFID string
	Name      string
	IsDir     bool
	Size      int64
	UpdatedAt int64
	MD5       string
}

type testQuarkUpload struct {
	TaskID   string
	UploadID string
	ObjKey   string
	PdirFID  string
	FileName string
	Size     int64
	Parts    map[int][]byte
	FID      string
}

func newTestQuarkMock(t *testing.T) *testQuarkMock {
	t.Helper()
	m := &testQuarkMock{
		nextFID:     10,
		files:       map[string]*testQuarkEntry{},
		contents:    map[string][]byte{},
		rapidHashes: map[string]bool{},
		uploads:     map[string]*testQuarkUpload{},
	}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.server.Close)
	restore := setQuarkOptionsHookForTest(func(o *quark.Options) {
		o.APIURL = m.server.URL + "/1/clouddrive"
		o.AccountURL = m.server.URL
		o.QRAuthURL = m.server.URL
		if o.TPSLimit <= 0 || o.TPSLimit < 100 {
			o.TPSLimit = 200
		}
		o.CacheTTL = 2 * time.Second
	})
	t.Cleanup(restore)
	return m
}

func (m *testQuarkMock) addFile(parentFID, name string, data []byte) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextFID++
	fid := fmt.Sprintf("qfid_%d", m.nextFID)
	sum := md5.Sum(data)
	m.files[fid] = &testQuarkEntry{
		FID:       fid,
		ParentFID: parentFID,
		Name:      name,
		IsDir:     false,
		Size:      int64(len(data)),
		UpdatedAt: time.Now().UnixMilli(),
		MD5:       hex.EncodeToString(sum[:]),
	}
	m.contents[fid] = append([]byte(nil), data...)
	return fid
}

func (m *testQuarkMock) listAllNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var names []string
	for _, f := range m.files {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return names
}

func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (m *testQuarkMock) handle(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch p {
	case "/cas/ajax/getTokenForQrcodeLogin":
		writeTestJSON(w, map[string]any{
			"status": 2000000,
			"data":   map[string]any{"members": map[string]any{"token": "qr-tok-native"}},
		})
		return
	case "/cas/ajax/getServiceTicketByQrcodeToken":
		if m.qrPolls.Add(1) == 1 {
			writeTestJSON(w, map[string]any{"status": 50004001, "message": "waiting"})
			return
		}
		writeTestJSON(w, map[string]any{
			"status": 2000000,
			"data":   map[string]any{"members": map[string]any{"service_ticket": "st-native-ok"}},
		})
		return
	case "/account/info":
		http.SetCookie(w, &http.Cookie{Name: "__pus", Value: "qr_pus_val"})
		writeTestJSON(w, map[string]any{
			"success": true,
			"data":    map[string]any{"nickname": "飞牛夸克测试号"},
		})
		return
	}

	if strings.HasPrefix(p, "/cdn/") {
		fid := strings.TrimPrefix(p, "/cdn/")
		m.mu.Lock()
		data, ok := m.contents[fid]
		m.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, fid, time.Unix(1700000000, 0), bytes.NewReader(data))
		return
	}

	if strings.HasPrefix(p, "/qbucket/") {
		uploadID := r.URL.Query().Get("uploadId")
		m.mu.Lock()
		var up *testQuarkUpload
		for _, cand := range m.uploads {
			if cand.UploadID == uploadID {
				up = cand
				break
			}
		}
		m.mu.Unlock()
		if up == nil {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPut {
			pn, _ := strconv.Atoi(r.URL.Query().Get("partNumber"))
			data, _ := io.ReadAll(r.Body)
			m.mu.Lock()
			if _, exists := up.Parts[pn]; exists {
				m.mu.Unlock()
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`<Error><Code>PartAlreadyExist</Code><Message>For sequential multipart upload, you can't overwrite uploaded parts.</Message></Error>`))
				return
			}
			if pn != len(up.Parts)+1 {
				m.mu.Unlock()
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`<Error><Code>PartNotSequential</Code><Message>For sequential multipart upload, you must upload or complete parts with sequential part number.</Message></Error>`))
				return
			}
			up.Parts[pn] = data
			m.mu.Unlock()
			m.putPartCalls.Add(1)
			sum := md5.Sum(data)
			w.Header().Set("ETag", fmt.Sprintf(`"%x"`, sum))
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodPost {
			rawXML, _ := io.ReadAll(r.Body)
			var doc struct {
				Parts []struct {
					PartNumber int `xml:"PartNumber"`
				} `xml:"Part"`
			}
			_ = xml.Unmarshal(rawXML, &doc)
			var buf bytes.Buffer
			m.mu.Lock()
			for _, pt := range doc.Parts {
				buf.Write(up.Parts[pt.PartNumber])
			}
			data := buf.Bytes()
			m.nextFID++
			fid := fmt.Sprintf("qfid_%d", m.nextFID)
			up.FID = fid
			sum := md5.Sum(data)
			m.files[fid] = &testQuarkEntry{
				FID:       fid,
				ParentFID: up.PdirFID,
				Name:      up.FileName,
				IsDir:     false,
				Size:      int64(len(data)),
				UpdatedAt: time.Now().UnixMilli(),
				MD5:       hex.EncodeToString(sum[:]),
			}
			m.contents[fid] = data
			m.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<CompleteMultipartUploadResult/>`))
			return
		}
	}

	if !strings.HasPrefix(p, "/1/clouddrive/") {
		http.NotFound(w, r)
		return
	}
	m.mu.Lock()
	puus := m.emitPuus
	m.emitPuus = ""
	m.mu.Unlock()
	if puus != "" {
		http.SetCookie(w, &http.Cookie{Name: "__puus", Value: puus})
	}

	switch strings.TrimPrefix(p, "/1/clouddrive") {
	case "/file/sort":
		if strings.Contains(r.Header.Get("Cookie"), "qr_pus_val") {
			http.SetCookie(w, &http.Cookie{Name: "__puus", Value: "qr_puus_val"})
		}
		pdir := r.URL.Query().Get("pdir_fid")
		m.mu.Lock()
		var list []map[string]any
		for _, f := range m.files {
			if f.ParentFID != pdir {
				continue
			}
			list = append(list, map[string]any{
				"fid":        f.FID,
				"file_name":  f.Name,
				"pdir_fid":   f.ParentFID,
				"size":       f.Size,
				"updated_at": f.UpdatedAt,
				"dir":        f.IsDir,
				"file":       !f.IsDir,
				"md5":        f.MD5,
			})
		}
		m.mu.Unlock()
		writeTestJSON(w, map[string]any{
			"status":   200,
			"code":     0,
			"data":     map[string]any{"list": list},
			"metadata": map[string]any{"_page": 1, "_size": 100, "_count": len(list), "_total": len(list)},
		})
	case "/file":
		var body struct {
			PdirFID  string `json:"pdir_fid"`
			FileName string `json:"file_name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.nextFID++
		fid := fmt.Sprintf("qfid_%d", m.nextFID)
		m.files[fid] = &testQuarkEntry{
			FID:       fid,
			ParentFID: body.PdirFID,
			Name:      body.FileName,
			IsDir:     true,
			UpdatedAt: time.Now().UnixMilli(),
		}
		m.mu.Unlock()
		writeTestJSON(w, map[string]any{"status": 200, "code": 0, "data": map[string]any{"fid": fid, "finish": true}})
	case "/file/rename":
		var body struct {
			FID      string `json:"fid"`
			FileName string `json:"file_name"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		if f, ok := m.files[body.FID]; ok {
			f.Name = body.FileName
		}
		m.mu.Unlock()
		writeTestJSON(w, map[string]any{"status": 200, "code": 0})
	case "/file/move":
		var body struct {
			Filelist  []string `json:"filelist"`
			ToPdirFID string   `json:"to_pdir_fid"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		for _, fid := range body.Filelist {
			if f, ok := m.files[fid]; ok {
				f.ParentFID = body.ToPdirFID
			}
		}
		m.mu.Unlock()
		writeTestJSON(w, map[string]any{"status": 200, "code": 0, "data": map[string]any{"finish": true}})
	case "/file/delete":
		var body struct {
			Filelist []string `json:"filelist"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		for _, fid := range body.Filelist {
			delete(m.files, fid)
			delete(m.contents, fid)
		}
		m.mu.Unlock()
		writeTestJSON(w, map[string]any{"status": 200, "code": 0, "data": map[string]any{"finish": true}})
	case "/member":
		writeTestJSON(w, map[string]any{
			"status": 200,
			"code":   0,
			"data": map[string]any{
				"member_type":    "VIP",
				"use_capacity":   int64(100 << 30),
				"total_capacity": int64(2 << 40),
			},
		})
	case "/file/download":
		m.downloadCalls.Add(1)
		var body struct {
			FIDs []string `json:"fids"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fid := ""
		if len(body.FIDs) > 0 {
			fid = body.FIDs[0]
		}
		writeTestJSON(w, map[string]any{
			"status": 200,
			"code":   0,
			"data":   []map[string]any{{"fid": fid, "download_url": m.server.URL + "/cdn/" + fid}},
		})
	case "/file/upload/pre":
		m.uploadPreCalls.Add(1)
		var body struct {
			PdirFID  string `json:"pdir_fid"`
			FileName string `json:"file_name"`
			Size     int64  `json:"size"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		id := len(m.uploads) + 1
		taskID := fmt.Sprintf("qt_%d", id)
		uploadID := fmt.Sprintf("qu_%d", id)
		objKey := fmt.Sprintf("k/%d/%s", id, body.FileName)
		m.uploads[taskID] = &testQuarkUpload{
			TaskID:   taskID,
			UploadID: uploadID,
			ObjKey:   objKey,
			PdirFID:  body.PdirFID,
			FileName: body.FileName,
			Size:     body.Size,
			Parts:    map[int][]byte{},
		}
		m.mu.Unlock()
		writeTestJSON(w, map[string]any{
			"status": 200,
			"code":   0,
			"data": map[string]any{
				"task_id":     taskID,
				"upload_id":   uploadID,
				"obj_key":     objKey,
				"upload_url":  m.server.URL,
				"bucket":      "qbucket",
				"auth_info":   "auth",
				"format_type": "application/octet-stream",
			},
			"metadata": map[string]any{"part_size": int64(32 << 10)},
		})
	case "/file/update/hash":
		var body struct {
			TaskID string `json:"task_id"`
			MD5    string `json:"md5"`
			SHA1   string `json:"sha1"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		up := m.uploads[body.TaskID]
		if m.rapidHashes[body.MD5+"|"+body.SHA1] && up != nil {
			m.nextFID++
			fid := fmt.Sprintf("qfid_%d", m.nextFID)
			m.files[fid] = &testQuarkEntry{
				FID:       fid,
				ParentFID: up.PdirFID,
				Name:      up.FileName,
				IsDir:     false,
				Size:      up.Size,
				UpdatedAt: time.Now().UnixMilli(),
				MD5:       body.MD5,
			}
			up.FID = fid
			m.mu.Unlock()
			writeTestJSON(w, map[string]any{"status": 200, "code": 0, "data": map[string]any{"finish": true, "fid": fid}})
			return
		}
		m.mu.Unlock()
		writeTestJSON(w, map[string]any{"status": 200, "code": 0, "data": map[string]any{"finish": false}})
	case "/file/upload/auth":
		writeTestJSON(w, map[string]any{"status": 200, "code": 0, "data": map[string]any{"auth_key": "OSS test:sig"}})
	case "/file/upload/finish":
		var body struct {
			TaskID string `json:"task_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		fid := ""
		if up := m.uploads[body.TaskID]; up != nil {
			fid = up.FID
		}
		m.mu.Unlock()
		writeTestJSON(w, map[string]any{"status": 200, "code": 0, "data": map[string]any{"fid": fid, "finish": true}})
	default:
		http.NotFound(w, r)
	}
}

func TestQuarkConfigValidateSummaryAndExportINI(t *testing.T) {
	// NormalizeParams strips "Cookie:" prefix and line breaks safely.
	norm := NormalizeParams(map[string]string{
		"type":               "quark",
		"cookie":             "Cookie: __pus=abc;\n__puus=def\r\n",
		"root_folder_id":     "0",
		"account_name":       "我的夸克",
		"upload_concurrency": "4",
		"tps_limit":          "3.5",
	})
	if norm["cookie"] != "__pus=abc;__puus=def" {
		t.Fatalf("unexpected normalized cookie: %q", norm["cookie"])
	}
	if err := ValidateEntry("qk", norm, true); err != nil {
		t.Fatalf("ValidateEntry valid quark failed: %v", err)
	}
	if sum := RemoteSummary(norm); sum != "夸克网盘 · 我的夸克" {
		t.Fatalf("unexpected RemoteSummary: %q", sum)
	}
	pub, secrets := RedactParams(norm)
	if _, leaked := pub["cookie"]; leaked {
		t.Fatal("cookie must be redacted from public params")
	}
	if len(secrets) != 1 || secrets[0] != "cookie" {
		t.Fatalf("expected [cookie] in secrets, got %v", secrets)
	}

	// Invalid cookie or concurrency rejected.
	if err := ValidateEntry("qk", map[string]string{"type": "quark", "cookie": "noequals"}, true); err == nil {
		t.Fatal("expected invalid cookie format to fail")
	}
	if err := ValidateEntry("qk", map[string]string{"type": "quark", "cookie": "__pus=1", "upload_concurrency": "16"}, true); err == nil {
		t.Fatal("expected upload_concurrency=16 to fail")
	}

	// ExportINI policy
	cfg := Config{
		"qk":      norm,
		"qkCrypt": {"type": "crypt", "remote": "qk:vault", "password": base64.RawURLEncoding.EncodeToString(make([]byte, 24))},
		"s3":      {"type": "s3", "provider": "Minio", "endpoint": "https://minio.local", "access_key_id": "ak", "secret_access_key": "sk"},
	}
	if _, err := cfg.ExportINI(context.Background(), []string{"qk"}, ""); err == nil {
		t.Fatal("expected explicit quark export to fail")
	}
	if _, err := cfg.ExportINI(context.Background(), []string{"qkCrypt"}, ""); err == nil {
		t.Fatal("expected crypt-over-quark export to fail")
	}
	ini, err := cfg.ExportINI(context.Background(), nil, "")
	if err != nil {
		t.Fatalf("ExportINI all with mixed remotes failed: %v", err)
	}
	if !strings.Contains(ini, "[s3]") || strings.Contains(ini, "[qk]") || !strings.Contains(ini, "跳过内置驱动") {
		t.Fatalf("unexpected mixed ExportINI output:\n%s", ini)
	}
	onlyQuark := Config{"qk": norm}
	if _, err := onlyQuark.ExportINI(context.Background(), nil, ""); err == nil {
		t.Fatal("expected ExportINI with only quark remotes to return clear error")
	}
}

func TestQuarkEffectiveCompareModeMatrix(t *testing.T) {
	localEp := Endpoint{Backend: "local", IsLocal: true}
	quarkEp := Endpoint{Backend: "quark", UnreliableModTime: true}
	s3Ep := Endpoint{Backend: "s3"}
	cryptQuarkEp := Endpoint{Backend: "quark", IsCrypt: true, UnreliableModTime: true}
	sftpNoHash := Endpoint{Backend: "sftp", AllowHashCheck: false}
	sftpWithHash := Endpoint{Backend: "sftp", AllowHashCheck: true}
	openlistWebDAV := Endpoint{Backend: "webdav", Vendor: "other", UnreliableModTime: true}

	cases := []struct {
		name string
		src  Endpoint
		dst  Endpoint
		mode int
		want int
	}{
		{"local->quark default uses Size+MD5", localEp, quarkEp, 0, 2},
		{"quark->local default uses Size+MD5", quarkEp, localEp, 0, 2},
		{"s3->quark default uses Size+MD5", s3Ep, quarkEp, 0, 2},
		{"local->crypt(quark) falls back to size-only", localEp, cryptQuarkEp, 0, 1},
		{"sftp(no hash)->quark falls back to size-only", sftpNoHash, quarkEp, 0, 1},
		{"sftp(with hash)->quark uses Size+MD5", sftpWithHash, quarkEp, 0, 2},
		{"local->quark explicit size-only honored", localEp, quarkEp, 1, 1},
		{"local->openlist webdav stays size-only", localEp, openlistWebDAV, 0, 1},
	}
	for _, tc := range cases {
		if got := EffectiveCompareMode(tc.src, tc.dst, tc.mode); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}

	// Verify Config.Resolve marks quark and union(quark) as UnreliableModTime.
	cfg := Config{
		"qk": {"type": "quark", "cookie": "__pus=1; __puus=2"},
		"u":  {"type": "union", "upstreams": "qk:sub"},
	}
	epQ, err := cfg.Resolve("/qk/docs")
	if err != nil || epQ.HasReliableModTime() || !epQ.SupportsNativeMD5() {
		t.Fatalf("unexpected quark Endpoint: %+v err=%v", epQ, err)
	}
	epU, err := cfg.Resolve("/u/docs")
	if err != nil || epU.HasReliableModTime() {
		t.Fatalf("expected union over quark to propagate UnreliableModTime, got %+v err=%v", epU, err)
	}
}

func TestBridgeDAVOwnCloudProtocol(t *testing.T) {
	mock := newTestQuarkMock(t)
	payload := []byte("hello-quark-webdav-bridge")
	sum := md5.Sum(payload)
	wantMD5 := hex.EncodeToString(sum[:])
	mock.addFile("0", "hello.txt", payload)

	qClient, err := newQuarkClientFromParams(map[string]string{
		"type":   "quark",
		"cookie": "__pus=1; __puus=2",
	}, SessionOptions{}, nil)
	if err != nil {
		t.Fatalf("newQuarkClientFromParams: %v", err)
	}
	s := &Session{}
	davURL, err := s.mountBridgeDAV(&quarkBridgeFS{client: qClient}, diskFullWrite, true)
	if err != nil {
		t.Fatalf("mountBridgeDAV: %v", err)
	}
	defer s.Close()

	hc := &http.Client{Timeout: 5 * time.Second}

	// OPTIONS
	req, _ := http.NewRequest(http.MethodOptions, davURL, nil)
	resp, err := hc.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK || resp.Header.Get("DAV") == "" {
		t.Fatalf("OPTIONS failed: resp=%v err=%v", resp, err)
	}
	_ = resp.Body.Close()

	// PROPFIND Depth: 1 returns <oc:checksums><oc:checksum>MD5:...</oc:checksum></oc:checksums>
	req, _ = http.NewRequest("PROPFIND", davURL, nil)
	req.Header.Set("Depth", "1")
	resp, err = hc.Do(req)
	if err != nil || resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("PROPFIND failed: resp=%v err=%v", resp, err)
	}
	xmlBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(xmlBody), "MD5:"+wantMD5) {
		t.Fatalf("PROPFIND missing MD5 checksum %s:\n%s", wantMD5, string(xmlBody))
	}

	// HEAD uses Stat only (0 /file/download calls)
	req, _ = http.NewRequest(http.MethodHead, davURL+"hello.txt", nil)
	resp, err = hc.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK || resp.ContentLength != int64(len(payload)) {
		t.Fatalf("HEAD failed: resp=%v err=%v", resp, err)
	}
	_ = resp.Body.Close()
	if mock.downloadCalls.Load() != 0 {
		t.Fatalf("HEAD must not call /file/download, got %d", mock.downloadCalls.Load())
	}

	// GET with Range
	req, _ = http.NewRequest(http.MethodGet, davURL+"hello.txt", nil)
	req.Header.Set("Range", "bytes=0-4")
	resp, err = hc.Do(req)
	if err != nil || resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("GET Range failed: resp=%v err=%v", resp, err)
	}
	part, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(part) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(part))
	}

	// MKCOL + PUT + MOVE + DELETE
	req, _ = http.NewRequest("MKCOL", davURL+"sub", nil)
	resp, err = hc.Do(req)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("MKCOL failed: resp=%v err=%v", resp, err)
	}
	_ = resp.Body.Close()

	putBody := []byte("uploaded-via-bridge-put")
	req, _ = http.NewRequest(http.MethodPut, davURL+"sub/new.txt", bytes.NewReader(putBody))
	req.ContentLength = int64(len(putBody))
	resp, err = hc.Do(req)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("PUT failed: resp=%v err=%v", resp, err)
	}
	_ = resp.Body.Close()

	// MOVE Overwrite: F to existing hello.txt -> 412
	req, _ = http.NewRequest("MOVE", davURL+"sub/new.txt", nil)
	req.Header.Set("Destination", davURL+"hello.txt")
	req.Header.Set("Overwrite", "F")
	resp, err = hc.Do(req)
	if err != nil || resp.StatusCode != http.StatusPreconditionFailed {
		t.Fatalf("MOVE Overwrite:F expected 412, got resp=%v err=%v", resp, err)
	}
	_ = resp.Body.Close()

	// MOVE Overwrite: T -> 201
	req, _ = http.NewRequest("MOVE", davURL+"sub/new.txt", nil)
	req.Header.Set("Destination", davURL+"moved.txt")
	resp, err = hc.Do(req)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("MOVE failed: resp=%v err=%v", resp, err)
	}
	_ = resp.Body.Close()

	// DELETE -> 204
	req, _ = http.NewRequest(http.MethodDelete, davURL+"moved.txt", nil)
	resp, err = hc.Do(req)
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE failed: resp=%v err=%v", resp, err)
	}
	_ = resp.Body.Close()
}

func TestQuarkSessionTokenRefreshAndAbout(t *testing.T) {
	mock := newTestQuarkMock(t)
	mock.addFile("0", "a.txt", []byte("hi"))

	cfg := Config{
		"qk": {
			"type":   "quark",
			"cookie": "__pus=orig_pus; __puus=orig_puus",
		},
	}

	// 1. Direct About returns quota and propagates rotated __puus cookie.
	var aboutUpdates map[string]TokenUpdate
	mock.mu.Lock()
	mock.emitPuus = "rotated_about_puus"
	mock.mu.Unlock()
	info, err := cfg.About(context.Background(), "qk", SessionOptions{
		OnTokenUpdate: func(u map[string]TokenUpdate) {
			aboutUpdates = u
		},
	})
	if err != nil || !info.Supported || info.Total == nil || *info.Total != int64(2<<40) {
		t.Fatalf("unexpected About result: %+v err=%v", info, err)
	}
	if got := aboutUpdates["qk"].Fields["cookie"]; !strings.Contains(got, "__puus=rotated_about_puus") {
		t.Fatalf("expected About to dispatch rotated __puus, got %+v", aboutUpdates)
	}

	// 2. SessionFor mounts Quark in-memory (no disk rclone.conf) and flushes rotated cookie on Close().
	var sessUpdates map[string]TokenUpdate
	s, err := cfg.SessionFor([]SessionSpec{{Name: "qk", Mode: diskReadOnly}}, SessionOptions{
		OnTokenUpdate: func(u map[string]TokenUpdate) {
			sessUpdates = u
		},
	})
	if err != nil {
		t.Fatalf("SessionFor quark: %v", err)
	}
	if s.configPath != "" {
		t.Fatalf("expected quark-only session to avoid disk rclone.conf, got %q", s.configPath)
	}
	// Read generated loopback WebDAV URL from unlinked configFile and trigger a PROPFIND with __puus rotation.
	_, _ = s.configFile.Seek(0, io.SeekStart)
	confBytes, _ := io.ReadAll(s.configFile)
	var davURL string
	for _, line := range strings.Split(string(confBytes), "\n") {
		if strings.HasPrefix(line, "url = ") {
			davURL = strings.TrimSpace(strings.TrimPrefix(line, "url = "))
		}
	}
	if davURL == "" {
		t.Fatalf("missing url in generated config:\n%s", string(confBytes))
	}
	mock.mu.Lock()
	mock.emitPuus = "rotated_session_puus"
	mock.mu.Unlock()
	req, _ := http.NewRequest("PROPFIND", davURL, nil)
	req.Header.Set("Depth", "1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusMultiStatus {
		t.Fatalf("session PROPFIND failed: resp=%v err=%v", resp, err)
	}
	_ = resp.Body.Close()

	s.Close()
	if got := sessUpdates["qk"].Fields["cookie"]; !strings.Contains(got, "__puus=rotated_session_puus") {
		t.Fatalf("expected Session.Close to flush rotated __puus, got %+v", sessUpdates)
	}
}

func TestQuarkDirectUploadFastPath(t *testing.T) {
	mock := newTestQuarkMock(t)
	ResetMD5CacheForTest()
	defer ResetMD5CacheForTest()

	localRoot := t.TempDir()
	srcDir := filepath.Join(localRoot, "photos")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	rapidBytes := []byte("instant-rapid-file-content")
	mSum := md5.Sum(rapidBytes)
	sSum := sha1.Sum(rapidBytes)
	mock.rapidHashes[hex.EncodeToString(mSum[:])+"|"+hex.EncodeToString(sSum[:])] = true

	normalBytes := bytes.Repeat([]byte("MULTIPART-LOCAL-QUARK-PAYLOAD-1234567890"), 2500) // ~100 KiB -> multiple 32 KiB parts
	if err := os.WriteFile(filepath.Join(srcDir, "rapid.txt"), rapidBytes, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "video.bin"), normalBytes, 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TRIM_PKGTMP", "")
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", localRoot)
	roots := AuthorizedRoots()
	if len(roots) != 1 {
		t.Fatalf("expected 1 authorized root, got %+v", roots)
	}
	srcVirtual := "/" + Local + "/" + roots[0].ID + "/photos"

	cfg := Config{
		"qk": {
			"type":               "quark",
			"cookie":             "__pus=1; __puus=2",
			"upload_concurrency": "2",
		},
	}

	var completedFiles []string
	var mu sync.Mutex
	err := cfg.Copy(context.Background(), srcVirtual, "/qk/backup", CopyOptions{
		Compare:        0, // Decision 1.A -> Size + MD5
		Transfers:      2,
		ResumeStateDir: t.TempDir(),
	}, func(ev Event) {
		if ev.File != "" && !ev.Failed {
			mu.Lock()
			completedFiles = append(completedFiles, ev.File)
			mu.Unlock()
		}
	})
	if err != nil {
		t.Fatalf("first local->quark Copy failed: %v", err)
	}
	sort.Strings(completedFiles)
	if len(completedFiles) != 2 || completedFiles[0] != "rapid.txt" || completedFiles[1] != "video.bin" {
		t.Fatalf("unexpected uploaded files: %v", completedFiles)
	}
	firstPreCalls := mock.uploadPreCalls.Load()
	if firstPreCalls != 2 {
		t.Fatalf("expected 2 /file/upload/pre calls, got %d", firstPreCalls)
	}
	if mock.putPartCalls.Load() < 3 {
		t.Fatalf("expected multipart OSS PUTs for video.bin, got %d", mock.putPartCalls.Load())
	}

	// Second Copy with Compare: 0 (Size+MD5) should skip BOTH unchanged files (0 new uploads!).
	completedFiles = nil
	err = cfg.Copy(context.Background(), srcVirtual, "/qk/backup", CopyOptions{
		Compare:        0,
		Transfers:      2,
		ResumeStateDir: t.TempDir(),
	}, func(ev Event) {
		if ev.File != "" && !ev.Failed {
			mu.Lock()
			completedFiles = append(completedFiles, ev.File)
			mu.Unlock()
		}
	})
	if err != nil {
		t.Fatalf("second local->quark Copy failed: %v", err)
	}
	if len(completedFiles) != 0 || mock.uploadPreCalls.Load() != firstPreCalls {
		t.Fatalf("expected unchanged files skipped by Size+MD5, got files=%v preCalls=%d", completedFiles, mock.uploadPreCalls.Load())
	}

	// Mutate rapid.txt with SAME byte length -> Size+MD5 detects content change & re-uploads with ConflictPolicy=2 (keep both).
	sameLenModified := []byte("INSTANT-RAPID-FILE-CONTENT")
	if len(sameLenModified) != len(rapidBytes) {
		t.Fatal("test setup length mismatch")
	}
	if err := os.WriteFile(filepath.Join(srcDir, "rapid.txt"), sameLenModified, 0644); err != nil {
		t.Fatal(err)
	}
	err = cfg.Copy(context.Background(), srcVirtual, "/qk/backup", CopyOptions{
		Compare:        0,
		ConflictPolicy: 2,
		ConflictSuffix: ".old",
		ResumeStateDir: t.TempDir(),
	}, func(ev Event) {
		if ev.File != "" && !ev.Failed {
			mu.Lock()
			completedFiles = append(completedFiles, ev.File)
			mu.Unlock()
		}
	})
	if err != nil {
		t.Fatalf("third local->quark Copy (same-size modified) failed: %v", err)
	}
	if len(completedFiles) != 1 || completedFiles[0] != "rapid.txt" {
		t.Fatalf("expected only rapid.txt re-uploaded on same-size MD5 diff, got %v", completedFiles)
	}
	names := mock.listAllNames()
	foundConflict := false
	for _, n := range names {
		if n == "rapid.old.txt" {
			foundConflict = true
		}
	}
	if !foundConflict {
		t.Fatalf("expected conflict backup rapid.old.txt on Quark, got %v", names)
	}
}

func TestQuarkQRAuthSessionLifecycle(t *testing.T) {
	_ = newTestQuarkMock(t)
	prevInterval := quarkQRPollInterval
	quarkQRPollInterval = 15 * time.Millisecond
	t.Cleanup(func() {
		quarkQRPollInterval = prevInterval
		ShutdownAuthSessions()
	})

	snap, err := StartAuthSession(context.Background(), "quark", nil, nil, "")
	if err != nil {
		t.Fatalf("StartAuthSession(quark): %v", err)
	}
	if snap.Status != "qrcode" || snap.QRStatus != "waiting" || !strings.HasPrefix(snap.QRCodeImage, "data:image/svg+xml;base64,") {
		t.Fatalf("unexpected initial QR snapshot: %+v", snap)
	}

	// Wait for background poller to receive waiting -> confirmed.
	deadline := time.Now().Add(3 * time.Second)
	var latest *AuthStepSnapshot
	for time.Now().Before(deadline) {
		latest, err = GetAuthSessionStatus(snap.SessionID)
		if err != nil {
			t.Fatalf("GetAuthSessionStatus: %v", err)
		}
		if latest.Status == "completed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if latest == nil || latest.Status != "completed" {
		t.Fatalf("expected QR auth session to complete, got %+v", latest)
	}
	if latest.PublicOptions["account_name"] != "飞牛夸克测试号" {
		t.Fatalf("expected nickname in publicOptions, got %+v", latest.PublicOptions)
	}
	if len(latest.ConfiguredSecrets) != 1 || latest.ConfiguredSecrets[0] != "cookie" {
		t.Fatalf("expected cookie in configuredSecrets, got %v", latest.ConfiguredSecrets)
	}

	completed, err := LookupCompletedAuthSession(snap.SessionID, "quark")
	if err != nil {
		t.Fatalf("LookupCompletedAuthSession: %v", err)
	}
	if !strings.Contains(completed.Params["cookie"], "__pus=qr_pus_val") || !strings.Contains(completed.Params["cookie"], "__puus=qr_puus_val") {
		t.Fatalf("unexpected harvested cookie: %q", completed.Params["cookie"])
	}
	ConsumeAuthSession(snap.SessionID)
}

func TestQuarkEndToEndWithRealRclone(t *testing.T) {
	ctx := context.Background()
	if err := Available(ctx); err != nil {
		t.Skipf("rclone binary unavailable (%v); set OPENSYNC_RCLONE_BIN to run E2E test", err)
	}

	mock := newTestQuarkMock(t)
	payload := []byte("real-rclone-e2e-quark-content-0123456789")
	wantSum := md5.Sum(payload)
	wantMD5 := hex.EncodeToString(wantSum[:])
	mock.addFile("0", "doc.txt", payload)

	localRoot := t.TempDir()
	restoreDir := filepath.Join(localRoot, "restore")
	cryptSrcDir := filepath.Join(localRoot, "secret_src")
	cryptDstDir := filepath.Join(localRoot, "secret_dst")
	for _, d := range []string{restoreDir, cryptSrcDir, cryptDstDir} {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}
	secretPayload := []byte("top-secret-encrypted-for-quark-cloud-drive")
	if err := os.WriteFile(filepath.Join(cryptSrcDir, "vault.txt"), secretPayload, 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TRIM_PKGTMP", "")
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", localRoot)
	roots := AuthorizedRoots()
	if len(roots) != 1 {
		t.Fatalf("expected 1 authorized root, got %+v", roots)
	}
	rootID := roots[0].ID

	// Obscure a real crypt password via bundled rclone.
	obscuredPass, err := obscureSecret(ctx, "test-crypt-passphrase")
	if err != nil {
		t.Fatalf("obscureSecret: %v", err)
	}

	cfg := Config{
		"qk": {
			"type":   "quark",
			"cookie": "__pus=1; __puus=2",
		},
		"qkVault": {
			"type":     "crypt",
			"remote":   "qk:vault",
			"password": obscuredPass,
		},
	}

	// 1. ListTree with IncludeHash: true via real rclone lsjson --hash -> BridgeFS OwnCloud MD5 passthrough.
	entries, exists, err := cfg.ListTree(ctx, "/qk", TreeOptions{IncludeHash: true})
	if err != nil || !exists {
		t.Fatalf("ListTree(/qk) failed: exists=%v err=%v", exists, err)
	}
	if len(entries) != 1 || entries[0].RelPath != "doc.txt" || entries[0].MD5 != wantMD5 {
		t.Fatalf("expected real rclone to parse Quark MD5 %s via BridgeFS, got %+v", wantMD5, entries)
	}

	// 2. Quark -> Local download via real rclone copy (Size+MD5 mode).
	restoreVirtual := "/" + Local + "/" + rootID + "/restore"
	if err := cfg.Copy(ctx, "/qk", restoreVirtual, CopyOptions{Compare: 0}, nil); err != nil {
		t.Fatalf("Quark -> Local Copy failed: %v", err)
	}
	gotRestored, err := os.ReadFile(filepath.Join(restoreDir, "doc.txt"))
	if err != nil || !bytes.Equal(gotRestored, payload) {
		t.Fatalf("restored file mismatch: got=%q err=%v", string(gotRestored), err)
	}

	// 3. Local -> crypt(Quark) -> Local round-trip through BridgeFS WebDAV PUT & GET.
	cryptSrcVirtual := "/" + Local + "/" + rootID + "/secret_src"
	cryptDstVirtual := "/" + Local + "/" + rootID + "/secret_dst"
	if err := cfg.Copy(ctx, cryptSrcVirtual, "/qkVault", CopyOptions{Compare: 0}, nil); err != nil {
		t.Fatalf("Local -> crypt(Quark) Copy failed: %v", err)
	}
	if err := cfg.Copy(ctx, "/qkVault", cryptDstVirtual, CopyOptions{Compare: 0}, nil); err != nil {
		t.Fatalf("crypt(Quark) -> Local Copy failed: %v", err)
	}
	gotDecrypted, err := os.ReadFile(filepath.Join(cryptDstDir, "vault.txt"))
	if err != nil || !bytes.Equal(gotDecrypted, secretPayload) {
		t.Fatalf("decrypted crypt(Quark) round-trip mismatch: got=%q err=%v", string(gotDecrypted), err)
	}
}

