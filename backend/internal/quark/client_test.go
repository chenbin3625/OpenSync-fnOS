package quark

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
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
)

type mockQuarkServer struct {
	t      *testing.T
	server *httptest.Server

	mu            sync.Mutex
	nextFID       int
	files         map[string]*mockEntry // fid -> entry
	contents      map[string][]byte     // fid -> raw file content
	rapidHashes   map[string]string     // md5|sha1 -> fid/content seed
	tasks         map[string]int        // taskID -> remaining pending polls
	uploads       map[string]*mockUpload
	sortCalls     atomic.Int64
	downloadCalls atomic.Int64
	putPartCalls  atomic.Int64
	rateLimitOnce atomic.Bool
	forceCDN403   atomic.Bool
	failPartNum   atomic.Int32
	emitNewPuus   string

	// QR auth mock state
	qrToken   string
	qrPolls   atomic.Int32
	qrExpired bool
}

type mockEntry struct {
	FID       string
	ParentFID string
	Name      string
	IsDir     bool
	Size      int64
	UpdatedAt int64
	MD5       string
}

type mockUpload struct {
	TaskID    string
	UploadID  string
	ObjKey    string
	PdirFID   string
	FileName  string
	Size      int64
	MD5       string
	Parts     map[int][]byte
	Committed bool
	Finished  bool
	FID       string
}

func newMockQuarkServer(t *testing.T) *mockQuarkServer {
	t.Helper()
	m := &mockQuarkServer{
		t:           t,
		nextFID:     100,
		files:       map[string]*mockEntry{},
		contents:    map[string][]byte{},
		rapidHashes: map[string]string{},
		tasks:       map[string]int{},
		uploads:     map[string]*mockUpload{},
		qrToken:     "mock-qr-token-123",
	}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.server.Close)
	return m
}

func (m *mockQuarkServer) allocFIDLocked() string {
	m.nextFID++
	return fmt.Sprintf("fid_%d", m.nextFID)
}

func (m *mockQuarkServer) addDir(parentFID, name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	fid := m.allocFIDLocked()
	m.files[fid] = &mockEntry{
		FID:       fid,
		ParentFID: parentFID,
		Name:      name,
		IsDir:     true,
		UpdatedAt: time.Now().UnixMilli(),
	}
	return fid
}

func (m *mockQuarkServer) addFile(parentFID, name string, content []byte) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	fid := m.allocFIDLocked()
	sum := md5.Sum(content)
	m.files[fid] = &mockEntry{
		FID:       fid,
		ParentFID: parentFID,
		Name:      name,
		IsDir:     false,
		Size:      int64(len(content)),
		UpdatedAt: time.Now().UnixMilli(),
		MD5:       hex.EncodeToString(sum[:]),
	}
	cp := make([]byte, len(content))
	copy(cp, content)
	m.contents[fid] = cp
	return fid
}

func (m *mockQuarkServer) newClient(opts ...func(*Options)) *Client {
	o := Options{
		Cookie:     "__pus=test_pus_value; __puus=initial_puus",
		TPSLimit:   100,
		CacheTTL:   2 * time.Second,
		APIURL:     m.server.URL + "/1/clouddrive",
		AccountURL: m.server.URL,
		QRAuthURL:  m.server.URL,
	}
	for _, fn := range opts {
		fn(&o)
	}
	c, err := NewClient(o)
	if err != nil {
		m.t.Fatalf("NewClient: %v", err)
	}
	m.t.Cleanup(func() { _ = c.Close() })
	return c
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (m *mockQuarkServer) handle(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// CAS QR endpoints
	switch path {
	case "/cas/ajax/getTokenForQrcodeLogin":
		writeJSON(w, 200, map[string]any{
			"status":  qrStatusSuccess,
			"message": "ok",
			"data": map[string]any{
				"members": map[string]any{
					"token": m.qrToken,
				},
			},
		})
		return
	case "/cas/ajax/getServiceTicketByQrcodeToken":
		if m.qrExpired {
			writeJSON(w, 200, map[string]any{"status": qrStatusExpired, "message": "expired"})
			return
		}
		n := m.qrPolls.Add(1)
		if n == 1 {
			writeJSON(w, 200, map[string]any{"status": qrStatusWaiting, "message": "waiting"})
			return
		}
		writeJSON(w, 200, map[string]any{
			"status":  qrStatusSuccess,
			"message": "ok",
			"data": map[string]any{
				"members": map[string]any{
					"service_ticket": "st-mock-999",
				},
			},
		})
		return
	case "/account/info":
		if r.URL.Query().Get("st") == "st-mock-999" {
			http.SetCookie(w, &http.Cookie{Name: "__pus", Value: "scanned_pus"})
		}
		writeJSON(w, 200, map[string]any{
			"success": true,
			"code":    "OK",
			"data": map[string]any{
				"nickname":  "测试夸克用户",
				"avatarUri": "https://example.invalid/avatar.png",
			},
		})
		return
	}

	// CDN download endpoint
	if strings.HasPrefix(path, "/cdn/") {
		m.handleCDN(w, r)
		return
	}

	// OSS upload endpoint (path-style: /<bucket>/<objKey>)
	if strings.HasPrefix(path, "/mock-bucket/") {
		m.handleOSS(w, r)
		return
	}

	// Clouddrive API endpoints
	if strings.HasPrefix(path, "/1/clouddrive/") {
		if m.rateLimitOnce.CompareAndSwap(true, false) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"status":429,"code":429,"message":"too many requests"}`))
			return
		}
		m.mu.Lock()
		newPuus := m.emitNewPuus
		m.emitNewPuus = ""
		m.mu.Unlock()
		if newPuus != "" {
			http.SetCookie(w, &http.Cookie{Name: "__puus", Value: newPuus})
		}

		sub := strings.TrimPrefix(path, "/1/clouddrive")
		switch sub {
		case "/file/sort":
			m.handleSort(w, r)
		case "/file":
			m.handleMkdir(w, r)
		case "/file/rename":
			m.handleRename(w, r)
		case "/file/move":
			m.handleMove(w, r)
		case "/file/delete":
			m.handleDelete(w, r)
		case "/task":
			m.handleTask(w, r)
		case "/member":
			m.handleMember(w, r)
		case "/file/download":
			m.handleDownloadLink(w, r)
		case "/file/upload/pre":
			m.handleUpPre(w, r)
		case "/file/update/hash":
			m.handleUpHash(w, r)
		case "/file/upload/auth":
			m.handleUpAuth(w, r)
		case "/file/upload/finish":
			m.handleUpFinish(w, r)
		default:
			http.NotFound(w, r)
		}
		return
	}

	http.NotFound(w, r)
}

func (m *mockQuarkServer) handleSort(w http.ResponseWriter, r *http.Request) {
	m.sortCalls.Add(1)
	if strings.Contains(r.Header.Get("Cookie"), "scanned_pus") && !strings.Contains(r.Header.Get("Cookie"), "__puus=") {
		http.SetCookie(w, &http.Cookie{Name: "__puus", Value: "scanned_puus"})
	}
	pdir := r.URL.Query().Get("pdir_fid")
	page, _ := strconv.Atoi(r.URL.Query().Get("_page"))
	if page <= 0 {
		page = 1
	}
	size, _ := strconv.Atoi(r.URL.Query().Get("_size"))
	if size <= 0 {
		size = 100
	}

	m.mu.Lock()
	var list []apiFileItem
	for _, f := range m.files {
		if f.ParentFID != pdir {
			continue
		}
		list = append(list, apiFileItem{
			FID:       f.FID,
			FileName:  f.Name,
			PdirFID:   f.ParentFID,
			Size:      f.Size,
			UpdatedAt: f.UpdatedAt,
			Dir:       f.IsDir,
			File:      !f.IsDir,
			MD5:       f.MD5,
		})
	}
	m.mu.Unlock()
	sort.Slice(list, func(i, j int) bool {
		if list[i].FileName != list[j].FileName {
			return list[i].FileName < list[j].FileName
		}
		return list[i].FID < list[j].FID
	})

	total := len(list)
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	writeJSON(w, 200, map[string]any{
		"status": 200,
		"code":   0,
		"data": map[string]any{
			"list": list[start:end],
		},
		"metadata": map[string]any{
			"_page":  page,
			"_size":  size,
			"_count": end - start,
			"_total": total,
		},
	})
}

func (m *mockQuarkServer) handleMkdir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PdirFID  string `json:"pdir_fid"`
		FileName string `json:"file_name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	fid := m.addDir(body.PdirFID, body.FileName)
	writeJSON(w, 200, map[string]any{
		"status": 200,
		"code":   0,
		"data":   map[string]any{"fid": fid, "finish": true},
	})
}

func (m *mockQuarkServer) handleRename(w http.ResponseWriter, r *http.Request) {
	var body struct {
		FID      string `json:"fid"`
		FileName string `json:"file_name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	m.mu.Lock()
	if f, ok := m.files[body.FID]; ok {
		f.Name = body.FileName
		f.UpdatedAt = time.Now().UnixMilli()
	}
	m.mu.Unlock()
	writeJSON(w, 200, map[string]any{"status": 200, "code": 0})
}

func (m *mockQuarkServer) handleMove(w http.ResponseWriter, r *http.Request) {
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
	taskID := "task_move_" + strconv.Itoa(len(m.tasks)+1)
	m.tasks[taskID] = 1
	m.mu.Unlock()
	writeJSON(w, 200, map[string]any{
		"status": 200,
		"code":   0,
		"data":   map[string]any{"task_id": taskID, "finish": false},
	})
}

func (m *mockQuarkServer) handleDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Filelist []string `json:"filelist"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	m.mu.Lock()
	for _, fid := range body.Filelist {
		delete(m.files, fid)
		delete(m.contents, fid)
	}
	taskID := "task_del_" + strconv.Itoa(len(m.tasks)+1)
	m.tasks[taskID] = 1
	m.mu.Unlock()
	writeJSON(w, 200, map[string]any{
		"status": 200,
		"code":   0,
		"data":   map[string]any{"task_id": taskID, "finish": false},
	})
}

func (m *mockQuarkServer) handleTask(w http.ResponseWriter, r *http.Request) {
	taskID := r.URL.Query().Get("task_id")
	m.mu.Lock()
	rem := m.tasks[taskID]
	status := 2
	if rem > 0 {
		m.tasks[taskID] = rem - 1
		status = 1
	}
	m.mu.Unlock()
	writeJSON(w, 200, map[string]any{
		"status": 200,
		"code":   0,
		"data":   map[string]any{"task_id": taskID, "status": status},
	})
}

func (m *mockQuarkServer) handleMember(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]any{
		"status": 200,
		"code":   0,
		"data": map[string]any{
			"member_type":    "SUPER_VIP",
			"use_capacity":   int64(250 << 30),
			"total_capacity": int64(6 << 40),
		},
	})
}

func (m *mockQuarkServer) handleDownloadLink(w http.ResponseWriter, r *http.Request) {
	m.downloadCalls.Add(1)
	var body struct {
		FIDs []string `json:"fids"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if len(body.FIDs) == 0 {
		writeJSON(w, 200, map[string]any{"status": 404, "code": 404, "message": "not found"})
		return
	}
	fid := body.FIDs[0]
	writeJSON(w, 200, map[string]any{
		"status": 200,
		"code":   0,
		"data": []map[string]any{
			{
				"fid":          fid,
				"download_url": m.server.URL + "/cdn/" + fid,
			},
		},
	})
}

func (m *mockQuarkServer) handleCDN(w http.ResponseWriter, r *http.Request) {
	if m.forceCDN403.CompareAndSwap(true, false) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	fid := strings.TrimPrefix(r.URL.Path, "/cdn/")
	m.mu.Lock()
	data, ok := m.contents[fid]
	m.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, fid, time.Unix(1700000000, 0), bytes.NewReader(data))
}

func (m *mockQuarkServer) handleUpPre(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PdirFID  string `json:"pdir_fid"`
		FileName string `json:"file_name"`
		Size     int64  `json:"size"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	m.mu.Lock()
	idx := len(m.uploads) + 1
	taskID := fmt.Sprintf("up_task_%d", idx)
	uploadID := fmt.Sprintf("up_id_%d", idx)
	objKey := fmt.Sprintf("obj/%d/%s", idx, body.FileName)
	m.uploads[taskID] = &mockUpload{
		TaskID:   taskID,
		UploadID: uploadID,
		ObjKey:   objKey,
		PdirFID:  body.PdirFID,
		FileName: body.FileName,
		Size:     body.Size,
		Parts:    map[int][]byte{},
	}
	m.mu.Unlock()

	writeJSON(w, 200, map[string]any{
		"status": 200,
		"code":   0,
		"data": map[string]any{
			"task_id":     taskID,
			"finish":      false,
			"upload_id":   uploadID,
			"obj_key":     objKey,
			"upload_url":  m.server.URL,
			"bucket":      "mock-bucket",
			"auth_info":   "mock-auth-info",
			"format_type": "application/octet-stream",
			"callback": map[string]any{
				"callbackUrl":  "https://callback.invalid/oss",
				"callbackBody": "ok=1",
			},
		},
		"metadata": map[string]any{
			"part_size":   int64(64 << 10), // 64 KiB parts for fast multipart testing
			"part_thread": 4,
		},
	})
}

func (m *mockQuarkServer) handleUpHash(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TaskID string `json:"task_id"`
		MD5    string `json:"md5"`
		SHA1   string `json:"sha1"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	m.mu.Lock()
	up := m.uploads[body.TaskID]
	if up != nil {
		up.MD5 = body.MD5
	}
	key := body.MD5 + "|" + body.SHA1
	if _, rapid := m.rapidHashes[key]; rapid && up != nil {
		fid := m.allocFIDLocked()
		m.files[fid] = &mockEntry{
			FID:       fid,
			ParentFID: up.PdirFID,
			Name:      up.FileName,
			IsDir:     false,
			Size:      up.Size,
			UpdatedAt: time.Now().UnixMilli(),
			MD5:       body.MD5,
		}
		up.Finished = true
		up.FID = fid
		m.mu.Unlock()
		writeJSON(w, 200, map[string]any{
			"status": 200,
			"code":   0,
			"data":   map[string]any{"finish": true, "fid": fid},
		})
		return
	}
	m.mu.Unlock()

	writeJSON(w, 200, map[string]any{
		"status": 200,
		"code":   0,
		"data":   map[string]any{"finish": false},
	})
}

func (m *mockQuarkServer) handleUpAuth(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AuthInfo string `json:"auth_info"`
		AuthMeta string `json:"auth_meta"`
		TaskID   string `json:"task_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.AuthInfo == "" || body.AuthMeta == "" || body.TaskID == "" {
		writeJSON(w, 400, map[string]any{"status": 400, "code": 400, "message": "missing auth params"})
		return
	}
	writeJSON(w, 200, map[string]any{
		"status": 200,
		"code":   0,
		"data":   map[string]any{"auth_key": "OSS mock:sig"},
	})
}

func (m *mockQuarkServer) handleOSS(w http.ResponseWriter, r *http.Request) {
	uploadID := r.URL.Query().Get("uploadId")
	m.mu.Lock()
	var up *mockUpload
	for _, cand := range m.uploads {
		if cand.UploadID == uploadID {
			up = cand
			break
		}
	}
	m.mu.Unlock()
	if up == nil {
		http.Error(w, "unknown uploadId", http.StatusNotFound)
		return
	}

	if r.Method == http.MethodPut {
		partNum, _ := strconv.Atoi(r.URL.Query().Get("partNumber"))
		if int32(partNum) == m.failPartNum.Load() {
			http.Error(w, "simulated part error", http.StatusBadRequest)
			return
		}
		data, _ := io.ReadAll(r.Body)
		m.putPartCalls.Add(1)
		m.mu.Lock()
		up.Parts[partNum] = data
		m.mu.Unlock()
		sum := md5.Sum(data)
		w.Header().Set("ETag", fmt.Sprintf(`"%x"`, sum))
		w.WriteHeader(http.StatusOK)
		return
	}

	if r.Method == http.MethodPost {
		rawXML, _ := io.ReadAll(r.Body)
		sum := md5.Sum(rawXML)
		wantMD5 := base64.StdEncoding.EncodeToString(sum[:])
		if gotMD5 := r.Header.Get("Content-MD5"); gotMD5 != wantMD5 {
			http.Error(w, fmt.Sprintf("Content-MD5 mismatch: got %s want %s", gotMD5, wantMD5), http.StatusBadRequest)
			return
		}
		if r.Header.Get("X-Oss-Callback") == "" {
			http.Error(w, "missing X-Oss-Callback", http.StatusBadRequest)
			return
		}
		var doc struct {
			Parts []struct {
				PartNumber int    `xml:"PartNumber"`
				ETag       string `xml:"ETag"`
			} `xml:"Part"`
		}
		if err := xml.Unmarshal(rawXML, &doc); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var assembled bytes.Buffer
		m.mu.Lock()
		for _, p := range doc.Parts {
			assembled.Write(up.Parts[p.PartNumber])
		}
		up.Committed = true
		fid := m.allocFIDLocked()
		up.FID = fid
		data := assembled.Bytes()
		fileMD5 := md5.Sum(data)
		m.files[fid] = &mockEntry{
			FID:       fid,
			ParentFID: up.PdirFID,
			Name:      up.FileName,
			IsDir:     false,
			Size:      int64(len(data)),
			UpdatedAt: time.Now().UnixMilli(),
			MD5:       hex.EncodeToString(fileMD5[:]),
		}
		m.contents[fid] = data
		m.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<CompleteMultipartUploadResult/>`))
		return
	}

	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (m *mockQuarkServer) handleUpFinish(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ObjKey string `json:"obj_key"`
		TaskID string `json:"task_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	m.mu.Lock()
	up := m.uploads[body.TaskID]
	var fid string
	if up != nil {
		up.Finished = true
		fid = up.FID
	}
	m.mu.Unlock()
	writeJSON(w, 200, map[string]any{
		"status": 200,
		"code":   0,
		"data":   map[string]any{"fid": fid, "finish": true},
	})
}

func TestListStatCacheMkdirMoveDelete(t *testing.T) {
	srv := newMockQuarkServer(t)
	// Add 105 files across 2 pages + HTML-escaped name + unsafe name.
	for i := 0; i < 103; i++ {
		srv.addFile("0", fmt.Sprintf("file_%03d.txt", i), []byte("data"))
	}
	srv.addFile("0", "rock&#39;n&#39;roll.txt", []byte("music"))
	srv.addFile("0", "../evil.txt", []byte("blocked"))

	client := srv.newClient()
	ctx := context.Background()

	entries, err := client.ListDir(ctx, "")
	if err != nil {
		t.Fatalf("ListDir root: %v", err)
	}
	if len(entries) != 104 {
		t.Fatalf("expected 104 safe entries (103 + unescaped), got %d", len(entries))
	}
	if srv.sortCalls.Load() != 2 {
		t.Fatalf("expected 2 paginated /file/sort calls, got %d", srv.sortCalls.Load())
	}

	// Second ListDir & Stat hit cache without new /file/sort calls.
	st, err := client.Stat(ctx, "rock'n'roll.txt")
	if err != nil {
		t.Fatalf("Stat unescaped name: %v", err)
	}
	if st.Name != "rock'n'roll.txt" || st.Size != 5 {
		t.Fatalf("unexpected Stat entry: %+v", st)
	}
	if srv.sortCalls.Load() != 2 {
		t.Fatalf("expected cache hit with no extra /file/sort calls, got %d", srv.sortCalls.Load())
	}

	// Nested MkdirAll
	subFID, err := client.MkdirAll(ctx, "docs/2026/reports")
	if err != nil || subFID == "" {
		t.Fatalf("MkdirAll failed: fid=%q err=%v", subFID, err)
	}

	// Move across directories + rename
	if err := client.Move(ctx, "rock'n'roll.txt", "docs/2026/reports/song.txt", false); err != nil {
		t.Fatalf("Move cross-dir+rename: %v", err)
	}
	if _, err := client.Stat(ctx, "rock'n'roll.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected old path ErrNotExist, got %v", err)
	}
	moved, err := client.Stat(ctx, "docs/2026/reports/song.txt")
	if err != nil || moved.Name != "song.txt" {
		t.Fatalf("expected moved file, got %+v err=%v", moved, err)
	}

	// Same-dir rename & overwrite guard
	if err := client.Move(ctx, "docs/2026/reports/song.txt", "docs/2026/reports/song_v2.txt", false); err != nil {
		t.Fatalf("same-dir rename: %v", err)
	}
	srv.addFile(subFID, "existing.txt", []byte("old"))
	client.cache.invalidateDir(subFID, "docs/2026/reports")
	if err := client.Move(ctx, "docs/2026/reports/song_v2.txt", "docs/2026/reports/existing.txt", false); !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected os.ErrExist when overwrite=false, got %v", err)
	}
	if err := client.Move(ctx, "docs/2026/reports/song_v2.txt", "docs/2026/reports/existing.txt", true); err != nil {
		t.Fatalf("expected overwrite=true to succeed, got %v", err)
	}

	// Delete with async /task poll
	if err := client.Delete(ctx, "docs/2026/reports/existing.txt"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, err := client.Stat(ctx, "docs/2026/reports/existing.txt"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected deleted file ErrNotExist, got %v", err)
	}
}

func TestDownloadStreamRangeCacheAnd403Retry(t *testing.T) {
	srv := newMockQuarkServer(t)
	payload := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	srv.addFile("0", "sample.bin", payload)
	srv.addFile("0", "empty.bin", nil)

	client := srv.newClient()
	ctx := context.Background()

	// Concurrent downloads should share a single /file/download call.
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rc, hdr, code, err := client.DownloadStream(ctx, "sample.bin", "bytes=10-19", "")
			if err != nil {
				t.Errorf("DownloadStream range: %v", err)
				return
			}
			defer rc.Close()
			got, _ := io.ReadAll(rc)
			if code != http.StatusPartialContent || string(got) != "abcdefghij" {
				t.Errorf("unexpected range response: code=%d body=%q hdr=%v", code, string(got), hdr)
			}
		}()
	}
	wg.Wait()
	if calls := srv.downloadCalls.Load(); calls != 1 {
		t.Fatalf("expected 1 deduplicated /file/download call, got %d", calls)
	}

	// Simulate expired CDN URL (HTTP 403) -> client must evict cache and re-fetch URL.
	srv.forceCDN403.Store(true)
	rc, _, code, err := client.DownloadStream(ctx, "sample.bin", "", "")
	if err != nil {
		t.Fatalf("DownloadStream after 403: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if code != http.StatusOK || !bytes.Equal(got, payload) {
		t.Fatalf("unexpected retry body: code=%d len=%d", code, len(got))
	}
	if calls := srv.downloadCalls.Load(); calls != 2 {
		t.Fatalf("expected 2 /file/download calls after 403 refresh, got %d", calls)
	}

	// Zero-byte file should not even call /file/download.
	rc0, _, code0, err := client.DownloadStream(ctx, "empty.bin", "", "")
	if err != nil || code0 != http.StatusOK {
		t.Fatalf("zero-byte DownloadStream: code=%d err=%v", code0, err)
	}
	emptyBytes, _ := io.ReadAll(rc0)
	_ = rc0.Close()
	if len(emptyBytes) != 0 || srv.downloadCalls.Load() != 2 {
		t.Fatalf("unexpected zero-byte download behavior: len=%d calls=%d", len(emptyBytes), srv.downloadCalls.Load())
	}
}

func TestUploadFileRapidMultipartResumeAndSafeOverwrite(t *testing.T) {
	srv := newMockQuarkServer(t)
	client := srv.newClient()
	ctx := context.Background()
	tmpDir := t.TempDir()

	// 1. Rapid upload (秒传)
	rapidData := []byte("instant-rapid-upload-payload")
	mSum := md5.Sum(rapidData)
	sSum := sha1.Sum(rapidData)
	srv.rapidHashes[hex.EncodeToString(mSum[:])+"|"+hex.EncodeToString(sSum[:])] = "ok"

	rapidPath := filepath.Join(tmpDir, "rapid.txt")
	if err := os.WriteFile(rapidPath, rapidData, 0600); err != nil {
		t.Fatal(err)
	}
	fRapid, err := os.Open(rapidPath)
	if err != nil {
		t.Fatal(err)
	}
	defer fRapid.Close()

	res, err := client.UploadFile(ctx, "backups/rapid.txt", UploadFileOptions{
		File:      fRapid,
		Size:      int64(len(rapidData)),
		Overwrite: true,
	})
	if err != nil {
		t.Fatalf("rapid UploadFile: %v", err)
	}
	if !res.Rapid || res.MD5 != hex.EncodeToString(mSum[:]) || srv.putPartCalls.Load() != 0 {
		t.Fatalf("expected rapid upload with 0 OSS PUTs, got %+v puts=%d", res, srv.putPartCalls.Load())
	}

	// 2. Multipart upload with simulated part #2 failure -> resume from state file!
	bigData := bytes.Repeat([]byte("QUARK-MULTIPART-DATA-0123456789!"), 5500) // ~176 KiB -> 3 parts of 64 KiB
	bigPath := filepath.Join(tmpDir, "big.bin")
	if err := os.WriteFile(bigPath, bigData, 0600); err != nil {
		t.Fatal(err)
	}
	resumePath := filepath.Join(tmpDir, "resume.json")

	// First attempt fails on part #2 (concurrency=1 so part #1 finishes and saves state before #2 fails).
	srv.failPartNum.Store(2)
	fBig1, _ := os.Open(bigPath)
	_, err = client.UploadFile(ctx, "backups/big.bin", UploadFileOptions{
		File:            fBig1,
		Size:            int64(len(bigData)),
		Concurrency:     1,
		ResumeStatePath: resumePath,
		Overwrite:       true,
	})
	_ = fBig1.Close()
	if err == nil {
		t.Fatal("expected first attempt to fail on part #2")
	}
	if _, statErr := os.Stat(resumePath); statErr != nil {
		t.Fatalf("expected resume state file to exist: %v", statErr)
	}
	putsAfterFail := srv.putPartCalls.Load()
	if putsAfterFail != 1 {
		t.Fatalf("expected 1 successful part PUT before failure, got %d", putsAfterFail)
	}

	// Second attempt clears failure and resumes: should upload only parts #2 and #3!
	srv.failPartNum.Store(0)
	fBig2, _ := os.Open(bigPath)
	res2, err := client.UploadFile(ctx, "backups/big.bin", UploadFileOptions{
		File:            fBig2,
		Size:            int64(len(bigData)),
		Concurrency:     2,
		ResumeStatePath: resumePath,
		Overwrite:       true,
	})
	_ = fBig2.Close()
	if err != nil {
		t.Fatalf("resumed UploadFile failed: %v", err)
	}
	if res2.Rapid {
		t.Fatal("expected non-rapid multipart upload")
	}
	if totalPuts := srv.putPartCalls.Load(); totalPuts != 3 {
		t.Fatalf("expected exactly 3 total OSS part PUTs across resume (1 + 2), got %d", totalPuts)
	}
	if _, statErr := os.Stat(resumePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected resume state file cleaned up after completion, got %v", statErr)
	}

	// Verify uploaded content matches byte-for-byte via DownloadStream.
	rc, _, _, err := client.DownloadStream(ctx, "backups/big.bin", "", "")
	if err != nil {
		t.Fatalf("DownloadStream uploaded big.bin: %v", err)
	}
	gotBig, _ := io.ReadAll(rc)
	_ = rc.Close()
	if !bytes.Equal(gotBig, bigData) {
		t.Fatalf("downloaded big.bin mismatch: got %d bytes want %d", len(gotBig), len(bigData))
	}

	// 3. Safe atomic overwrite: failure preserves old file; success replaces cleanly.
	newData := []byte("brand-new-replacement-version")
	newPath := filepath.Join(tmpDir, "big_v2.bin")
	_ = os.WriteFile(newPath, newData, 0600)

	srv.failPartNum.Store(1)
	fFail, _ := os.Open(newPath)
	_, err = client.UploadFile(ctx, "backups/big.bin", UploadFileOptions{
		File:      fFail,
		Size:      int64(len(newData)),
		Overwrite: true,
	})
	_ = fFail.Close()
	if err == nil {
		t.Fatal("expected overwrite attempt with failing part to fail")
	}
	// Old file must still exist with original size.
	oldSt, err := client.Stat(ctx, "backups/big.bin")
	if err != nil || oldSt.Size != int64(len(bigData)) {
		t.Fatalf("expected old file preserved on failed overwrite, got %+v err=%v", oldSt, err)
	}

	// Succeeding overwrite replaces file atomically.
	srv.failPartNum.Store(0)
	fOk, _ := os.Open(newPath)
	res3, err := client.UploadFile(ctx, "backups/big.bin", UploadFileOptions{
		File:      fOk,
		Size:      int64(len(newData)),
		Overwrite: true,
	})
	_ = fOk.Close()
	if err != nil {
		t.Fatalf("atomic overwrite failed: %v", err)
	}
	if res3.Size != int64(len(newData)) {
		t.Fatalf("unexpected overwritten size: %+v", res3)
	}
	newSt, err := client.Stat(ctx, "backups/big.bin")
	if err != nil || newSt.Size != int64(len(newData)) {
		t.Fatalf("expected new file after atomic overwrite, got %+v err=%v", newSt, err)
	}
}

func TestUploadStreamRapidAndUnknownSize(t *testing.T) {
	srv := newMockQuarkServer(t)
	client := srv.newClient()
	ctx := context.Background()

	// Known small stream triggers rapid upload when hash matches.
	rapidBody := []byte("stream-rapid-match")
	mSum := md5.Sum(rapidBody)
	sSum := sha1.Sum(rapidBody)
	srv.rapidHashes[hex.EncodeToString(mSum[:])+"|"+hex.EncodeToString(sSum[:])] = "ok"

	res, err := client.UploadStream(ctx, "stream/rapid.txt", bytes.NewReader(rapidBody), int64(len(rapidBody)), nil)
	if err != nil || !res.Rapid {
		t.Fatalf("expected rapid UploadStream, got %+v err=%v", res, err)
	}

	// Unknown-size stream (size = -1) spools and completes multipart upload.
	unknownBody := []byte("unknown-length-stream-payload")
	res2, err := client.UploadStream(ctx, "stream/unknown.txt", bytes.NewReader(unknownBody), -1, nil)
	if err != nil || res2.Size != int64(len(unknownBody)) {
		t.Fatalf("unknown-size UploadStream failed: %+v err=%v", res2, err)
	}
}

func TestCookieRefreshRateLimitRetryAndAbout(t *testing.T) {
	srv := newMockQuarkServer(t)
	var updatedCookie atomic.Value
	client := srv.newClient(func(o *Options) {
		o.OnCookieUpdate = func(newCookie string) {
			updatedCookie.Store(newCookie)
		}
	})
	ctx := context.Background()

	// Trigger both a 429 retry and a __puus cookie rotation.
	srv.rateLimitOnce.Store(true)
	srv.mu.Lock()
	srv.emitNewPuus = "rotated_puus_v2"
	srv.mu.Unlock()

	quota, err := client.About(ctx)
	if err != nil {
		t.Fatalf("About after 429 retry failed: %v", err)
	}
	if quota.Total != int64(6<<40) || quota.Used != int64(250<<30) || quota.Free != quota.Total-quota.Used {
		t.Fatalf("unexpected quota: %+v", quota)
	}
	gotCookie, _ := updatedCookie.Load().(string)
	if !strings.Contains(gotCookie, "__puus=rotated_puus_v2") || !strings.Contains(gotCookie, "__pus=test_pus_value") {
		t.Fatalf("expected OnCookieUpdate with merged rotated __puus, got %q", gotCookie)
	}

	acc, err := client.AccountInfo(ctx)
	if err != nil || acc.Nickname != "测试夸克用户" {
		t.Fatalf("unexpected AccountInfo: %+v err=%v", acc, err)
	}
}

func TestQRAuthFlowAndQRCodeEncoding(t *testing.T) {
	srv := newMockQuarkServer(t)
	ctx := context.Background()

	opts := Options{
		TPSLimit:   100,
		APIURL:     srv.server.URL + "/1/clouddrive",
		AccountURL: srv.server.URL,
		QRAuthURL:  srv.server.URL,
	}
	sess, err := StartQRAuth(ctx, opts)
	if err != nil {
		t.Fatalf("StartQRAuth: %v", err)
	}
	defer sess.Close()

	if !strings.Contains(sess.QRCodeURL(), "token=mock-qr-token-123") {
		t.Fatalf("unexpected QRCodeURL: %q", sess.QRCodeURL())
	}
	if !strings.HasPrefix(sess.QRCodeImage(), "data:image/svg+xml;base64,") {
		t.Fatalf("unexpected QRCodeImage prefix: %q", sess.QRCodeImage())
	}
	rawSVG, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sess.QRCodeImage(), "data:image/svg+xml;base64,"))
	if err != nil || !strings.Contains(string(rawSVG), "<svg") || !strings.Contains(string(rawSVG), "<path") {
		t.Fatalf("invalid QR SVG output: err=%v", err)
	}

	// Poll 1 -> waiting
	p1, err := sess.Poll(ctx)
	if err != nil || p1.State != QRAuthWaiting {
		t.Fatalf("expected QRAuthWaiting, got %+v err=%v", p1, err)
	}

	// Poll 2 -> confirmed + cookie exchange + nickname
	p2, err := sess.Poll(ctx)
	if err != nil || p2.State != QRAuthConfirmed {
		t.Fatalf("expected QRAuthConfirmed, got %+v err=%v", p2, err)
	}
	if p2.Nickname != "测试夸克用户" || !strings.Contains(p2.Cookie, "__pus=scanned_pus") || !strings.Contains(p2.Cookie, "__puus=scanned_puus") {
		t.Fatalf("unexpected confirmed QR result: %+v", p2)
	}

	// Expired QR flow
	srv.qrExpired = true
	pExp, err := sess.Poll(ctx)
	if err != nil || pExp.State != QRAuthExpired {
		t.Fatalf("expected QRAuthExpired, got %+v err=%v", pExp, err)
	}

	// Validate QR matrix structure across sizes (v1 & typical Quark login URL).
	for _, sample := range []string{"hello", sess.QRCodeURL()} {
		m, err := EncodeQRMatrix(sample)
		if err != nil {
			t.Fatalf("EncodeQRMatrix(%d bytes): %v", len(sample), err)
		}
		n := len(m)
		if n < 21 || (n-21)%4 != 0 {
			t.Fatalf("invalid QR matrix dimension %d", n)
		}
		// Top-left finder center (3,3) must be dark; separator (7,7) must be light.
		if !m[3][3] || m[7][7] {
			t.Fatalf("invalid finder pattern in %dx%d matrix", n, n)
		}
	}
}

func TestUCProviderProfileConfigurable(t *testing.T) {
	uc := ProfileFor(ProviderUC)
	if uc.Name != ProviderUC || uc.PR != "UCBrowser" || !strings.Contains(uc.APIURL, "uc.cn") {
		t.Fatalf("unexpected UC profile: %+v", uc)
	}
	qk := ProfileFor("")
	if qk.Name != ProviderQuark || qk.PR != "ucpro" || !strings.Contains(qk.APIURL, "quark.cn") {
		t.Fatalf("unexpected default Quark profile: %+v", qk)
	}
}
