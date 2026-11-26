package pan123

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func md5Hex(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

func TestOpenAPIRejectsEmptyNonterminalPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("lastFileId") == "" {
			_, _ = io.WriteString(w, `{"code":0,"data":{"lastFileId":101,"fileList":[{"fileId":101,"filename":"one.txt"}]}}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":0,"data":{"lastFileId":102,"fileList":[]}}`)
	}))
	defer server.Close()
	b, err := newOpenAPIBackend(Options{OpenAPIBaseURL: server.URL, AccessToken: "test"}, "test", 1, server.Client(), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := b.ListDir(context.Background(), 0); !errors.Is(err, ErrInconsistentListing) || len(entries) != 0 {
		t.Fatalf("partial listing was accepted: %v (%v)", entries, err)
	}
}

func TestOpenAPIRestartsExpiredMultipartCompletion(t *testing.T) {
	var creates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/upload/v2/file/upload_complete":
			_, _ = io.WriteString(w, `{"code":400,"message":"preupload ID expired"}`)
		case "/upload/v2/file/create":
			creates.Add(1)
			_, _ = io.WriteString(w, `{"code":0,"data":{"fileID":123,"reuse":true}}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	b, err := newOpenAPIBackend(Options{OpenAPIBaseURL: server.URL, AccessToken: "test"}, "test", 1, server.Client(), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "*.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data := make([]byte, 2<<20)
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(t.TempDir(), "resume.json")
	saveResumeJSON(statePath, openAPIResumeState{
		FileName: "big.bin", Size: int64(len(data)), MD5: md5Hex(data), PreuploadID: "stale",
		SliceSize: 1 << 20, Servers: []string{server.URL}, DoneSlices: []int64{1, 2},
	})
	opts := UploadFileOptions{File: file, Size: int64(len(data)), ResumeStatePath: statePath}
	if _, err := b.UploadFile(context.Background(), 0, "big.bin", opts, nil); err == nil || creates.Load() != 0 {
		t.Fatalf("expected stale completion before creating a new session: %v", err)
	}
	result, err := b.UploadFile(context.Background(), 0, "big.bin", opts, nil)
	if err != nil || creates.Load() != 1 || result == nil || result.FID != 123 {
		t.Fatalf("retry did not start a new session: %+v (%v)", result, err)
	}
}

func TestOpenAPI_AuthListMutationsDownloadAndUpload(t *testing.T) {
	ctx := context.Background()

	var (
		mu             sync.Mutex
		tokenIssued    int
		activeToken    = "tok-v1"
		force401Once   = true
		nextFID        = int64(200)
		filesByParent  = map[int64][]map[string]any{}
		storedPayload  = map[int64][]byte{}
		singleUploads  int
		sliceUploads   = map[int64][]byte{}
		completePolls  int
		directLinkHits int
	)

	helloData := []byte("hello-123pan-openapi-download")
	filesByParent[0] = []map[string]any{
		{
			"fileId":       int64(101),
			"filename":     "hello.txt",
			"type":         0,
			"size":         int64(len(helloData)),
			"etag":         md5Hex(helloData),
			"parentFileId": int64(0),
			"trashed":      0,
			"updateAt":     "2026-10-01 12:30:00",
		},
		{
			"fileId":       int64(102),
			"filename":     "deleted.txt",
			"type":         0,
			"size":         int64(5),
			"etag":         md5Hex([]byte("12345")),
			"parentFileId": int64(0),
			"trashed":      1, // must be filtered out even when API returns it
			"updateAt":     "2026-10-01 12:31:00",
		},
	}
	storedPayload[101] = helloData

	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/access_token":
			tokenIssued++
			activeToken = fmt.Sprintf("tok-v%d", tokenIssued)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"accessToken": activeToken,
					"expiredAt":   time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339),
				},
			})
			return

		case "/cdn/file":
			data := storedPayload[101]
			if rng := r.Header.Get("Range"); rng == "bytes=0-4" {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-4/%d", len(data)))
				w.Header().Set("Content-Length", "5")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(data[:5])
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		}

		if r.Header.Get("Platform") != openAPIPlatformHeader {
			http.Error(w, "missing platform", http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+activeToken {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 401, "message": "expired"})
			return
		}

		switch r.URL.Path {
		case "/api/v1/user/info":
			if force401Once {
				force401Once = false
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 401, "message": "token invalid"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"uid":            int64(888001),
					"nickname":       "OpenAPI测试号",
					"spaceUsed":      int64(1000),
					"spacePermanent": int64(5000),
					"spaceTemp":      int64(2000),
				},
			})

		case "/api/v2/file/list":
			pid, _ := strconv.ParseInt(r.URL.Query().Get("parentFileId"), 10, 64)
			list := filesByParent[pid]
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"lastFileId": int64(-1),
					"fileList":   list,
				},
			})

		case "/upload/v1/file/mkdir":
			var req struct {
				Name     string `json:"name"`
				ParentID int64  `json:"parentID"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			nextFID++
			id := nextFID
			filesByParent[req.ParentID] = append(filesByParent[req.ParentID], map[string]any{
				"fileId":       id,
				"filename":     req.Name,
				"type":         1,
				"size":         int64(0),
				"parentFileId": req.ParentID,
				"trashed":      0,
				"updateAt":     "2026-10-01 13:00:00",
			})
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"dirID": id}})

		case "/api/v1/file/download_info":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"downloadUrl": srvURL + "/cdn/file"},
			})

		case "/api/v1/direct-link/url":
			directLinkHits++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"url": srvURL + "/cdn/file"},
			})

		case "/upload/v2/file/domain":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": []string{srvURL},
			})

		case "/upload/v2/file/single/create":
			_ = r.ParseMultipartForm(4 << 20)
			singleUploads++
			pid, _ := strconv.ParseInt(r.FormValue("parentFileID"), 10, 64)
			fname := r.FormValue("filename")
			etag := r.FormValue("etag")
			f, _, err := r.FormFile("file")
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			payload, _ := io.ReadAll(f)
			_ = f.Close()
			nextFID++
			id := nextFID
			storedPayload[id] = payload
			filesByParent[pid] = append(filesByParent[pid], map[string]any{
				"fileId":       id,
				"filename":     fname,
				"type":         0,
				"size":         int64(len(payload)),
				"etag":         etag,
				"parentFileId": pid,
				"trashed":      0,
				"updateAt":     "2026-10-01 14:00:00",
			})
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"fileID": id, "completed": true},
			})

		case "/upload/v2/file/create":
			var req struct {
				ParentFileID int64  `json:"parentFileID"`
				Filename     string `json:"filename"`
				Etag         string `json:"etag"`
				Size         int64  `json:"size"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Filename == "rapid.bin" {
				nextFID++
				id := nextFID
				filesByParent[req.ParentFileID] = append(filesByParent[req.ParentFileID], map[string]any{
					"fileId":       id,
					"filename":     req.Filename,
					"type":         0,
					"size":         req.Size,
					"etag":         req.Etag,
					"parentFileId": req.ParentFileID,
					"trashed":      0,
				})
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{"fileID": id, "reuse": true},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"preuploadID": "pre-multi-1",
					"reuse":       false,
					"sliceSize":   int64(512 << 10), // 512 KiB slices for test
					"servers":     []string{srvURL},
				},
			})

		case "/upload/v2/file/slice":
			_ = r.ParseMultipartForm(4 << 20)
			no, _ := strconv.ParseInt(r.FormValue("sliceNo"), 10, 64)
			wantMD5 := r.FormValue("sliceMD5")
			f, _, err := r.FormFile("slice")
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			chunk, _ := io.ReadAll(f)
			_ = f.Close()
			if md5Hex(chunk) != wantMD5 {
				http.Error(w, "slice md5 mismatch", http.StatusBadRequest)
				return
			}
			sliceUploads[no] = chunk
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{}})

		case "/upload/v2/file/upload_complete":
			completePolls++
			if completePolls == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{"completed": false, "fileID": 0},
				})
				return
			}
			nextFID++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"completed": true, "fileID": nextFID},
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	srvURL = srv.URL

	var tokenUpdates []string
	client, err := NewClient(Options{
		Protocol:       ProtocolOpenAPI,
		ClientID:       "cid",
		ClientSecret:   "csec",
		UseDirectLink:  true,
		DirectLinkKey:  "my-secret-key",
		DirectLinkTTL:  15,
		OpenAPIBaseURL: srv.URL,
		TPSLimit:       20,
		OnTokenUpdate: func(tok string, _ time.Time) {
			tokenUpdates = append(tokenUpdates, tok)
		},
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer client.Close()

	// 1. About triggers initial token fetch + 401 forced refresh retry.
	info, quota, err := client.AccountInfo(ctx)
	if err != nil {
		t.Fatalf("AccountInfo failed: %v", err)
	}
	if info.UID != 888001 || quota.Total != 7000 || quota.Free != 6000 {
		t.Fatalf("unexpected account/quota: %+v %+v", info, quota)
	}
	if len(tokenUpdates) < 2 {
		t.Fatalf("expected initial + refreshed token updates, got %v", tokenUpdates)
	}

	// 2. ListDir filters out trashed entries.
	entries, err := client.ListDir(ctx, "")
	if err != nil {
		t.Fatalf("ListDir failed: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "hello.txt" {
		t.Fatalf("expected only non-trashed hello.txt, got %+v", entries)
	}
	if entries[0].ModTime.UTC().Format("2006-01-02 15:04:05") != "2026-10-01 04:30:00" {
		t.Fatalf("expected UTC+8 conversion to 04:30:00 UTC, got %v", entries[0].ModTime)
	}

	// 3. DownloadStream uses direct link + Range passthrough.
	rc, _, status, err := client.DownloadStream(ctx, "hello.txt", "bytes=0-4", "")
	if err != nil {
		t.Fatalf("DownloadStream failed: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if status != http.StatusPartialContent || string(got) != "hello" {
		t.Fatalf("unexpected ranged download: status=%d body=%q", status, got)
	}
	if directLinkHits == 0 {
		t.Fatal("expected direct link endpoint to be used")
	}

	// 4. Small file upload uses V2 SingleCreate fast path.
	tmpDir := t.TempDir()
	smallPath := filepath.Join(tmpDir, "small.txt")
	smallBytes := []byte("tiny payload for single create")
	if err := os.WriteFile(smallPath, smallBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	sf, _ := os.Open(smallPath)
	res, err := client.UploadFile(ctx, "sub/small.txt", UploadFileOptions{
		File:      sf,
		Size:      int64(len(smallBytes)),
		Overwrite: true,
	})
	_ = sf.Close()
	if err != nil {
		t.Fatalf("small upload failed: %v", err)
	}
	if singleUploads != 1 || res.MD5 != md5Hex(smallBytes) {
		t.Fatalf("expected single upload path, hits=%d res=%+v", singleUploads, res)
	}

	// 5. Large file (> 1 MiB) rapid upload.
	rapidPath := filepath.Join(tmpDir, "rapid.bin")
	rapidBytes := bytes.Repeat([]byte("R"), (1<<20)+128)
	_ = os.WriteFile(rapidPath, rapidBytes, 0o600)
	rf, _ := os.Open(rapidPath)
	rRes, err := client.UploadFile(ctx, "rapid.bin", UploadFileOptions{
		File:      rf,
		Size:      int64(len(rapidBytes)),
		Overwrite: true,
	})
	_ = rf.Close()
	if err != nil || !rRes.Rapid {
		t.Fatalf("expected rapid upload, got res=%+v err=%v", rRes, err)
	}

	// 6. Large file multipart concurrent upload with pre-seeded resume state (skip slice 1).
	multiPath := filepath.Join(tmpDir, "multi.bin")
	multiBytes := bytes.Repeat([]byte("M"), (8<<20)+64<<10) // 17 slices of 512 KiB
	_ = os.WriteFile(multiPath, multiBytes, 0o600)
	resumePath := filepath.Join(tmpDir, "resume.json")
	saveResumeJSON(resumePath, openAPIResumeState{
		ParentID:    0,
		FileName:    "multi.bin",
		Size:        int64(len(multiBytes)),
		MD5:         md5Hex(multiBytes),
		PreuploadID: "pre-multi-1",
		SliceSize:   512 << 10,
		Servers:     []string{srv.URL},
		DoneSlices:  []int64{1},
	})
	mf, _ := os.Open(multiPath)
	mRes, err := client.UploadFile(ctx, "multi.bin", UploadFileOptions{
		File:            mf,
		Size:            int64(len(multiBytes)),
		Concurrency:     2,
		ResumeStatePath: resumePath,
		Overwrite:       true,
	})
	_ = mf.Close()
	if err != nil || mRes.FID <= 0 {
		t.Fatalf("multipart upload failed: res=%+v err=%v", mRes, err)
	}
	if _, uploaded1 := sliceUploads[1]; uploaded1 {
		t.Fatal("expected slice 1 to be skipped via resume state")
	}
	if len(sliceUploads) != 16 || sliceUploads[2] == nil || sliceUploads[17] == nil {
		t.Fatalf("expected slices 2 through 17 uploaded, got %d slices", len(sliceUploads))
	}
	if _, err := os.Stat(resumePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expected resume file removed after completion")
	}

	// Hold the first HTTP request until cancellation while the next slice waits
	// for the only concurrency slot.
	backend := client.backend.(*openAPIBackend)
	sliceStarted := make(chan struct{})
	releaseSlice := make(chan struct{})
	backend.streamClient = &http.Client{Transport: testRoundTripper(func(req *http.Request) (*http.Response, error) {
		defer req.Body.Close()
		close(sliceStarted)
		<-releaseSlice
		return nil, req.Context().Err()
	})}
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cancelFile, err := os.Open(multiPath)
	if err != nil {
		t.Fatal(err)
	}
	defer cancelFile.Close()
	uploadErr := make(chan error, 1)
	go func() {
		_, err := client.UploadFile(cancelCtx, "cancel.bin", UploadFileOptions{
			File: cancelFile, Size: int64(len(multiBytes)), Concurrency: 1,
		})
		uploadErr <- err
	}()
	select {
	case <-sliceStarted:
	case <-time.After(5 * time.Second):
		close(releaseSlice)
		t.Fatal("no in-flight slice to cancel")
	}
	cancel()
	// Let the cancelled scheduler leave the select before the worker completes.
	time.Sleep(10 * time.Millisecond)
	close(releaseSlice)
	select {
	case err := <-uploadErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled upload did not release its concurrency slot")
	}
}

type testRoundTripper func(*http.Request) (*http.Response, error)

func (f testRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestWeb_SignURLChallengeFailClosedListAndPresignedUpload(t *testing.T) {
	ctx := context.Background()

	// Verify deterministic Web URL signature format.
	signed, err := SignWebURL("https://api.123278.com/b/api/user/info", time.Unix(1700000000, 0), 123456)
	if err != nil {
		t.Fatalf("SignWebURL failed: %v", err)
	}
	if !strings.Contains(signed, "=1700000000-123456-") {
		t.Fatalf("unexpected signed URL: %s", signed)
	}

	// Verify auth challenge short-circuits without retry loops.
	var challengeCalls atomic.Int32
	challengeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		challengeCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":    400,
			"message": "当前账号存在安全风险，请使用短信验证码或者微信进行登录",
		})
	}))
	defer challengeSrv.Close()

	chClient, err := NewClient(Options{
		Protocol:    ProtocolWeb,
		Username:    "13800000000",
		Password:    "secret",
		WebLoginURL: challengeSrv.URL,
		WebAPIURL:   challengeSrv.URL + "/b/api",
		TPSLimit:    20,
	})
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	defer chClient.Close()

	if _, err := chClient.About(ctx); !errors.Is(err, ErrAuthChallenge) {
		t.Fatalf("expected ErrAuthChallenge, got %v", err)
	}
	// Calling again must immediately return cached challenge without hitting login again.
	if _, err := chClient.About(ctx); !errors.Is(err, ErrAuthChallenge) {
		t.Fatalf("expected cached ErrAuthChallenge, got %v", err)
	}
	if challengeCalls.Load() != 1 {
		t.Fatalf("expected exactly 1 login call before circuit-break, got %d", challengeCalls.Load())
	}

	// Full Web flow: login, fail-closed list drift recovery, base64 params download,
	// zero-ID rapid upload verification, and presigned multipart upload with 403 refresh.
	var (
		mu             sync.Mutex
		listDriftOnce  = true
		zeroIDCreated  bool
		zeroIDSize     int64
		zeroIDMD5      string
		part1Forbidden = true
		presignedRev   = 0
		uploadedParts  = map[int64][]byte{}
		sawAuthOnData  bool
		dlContent      = []byte("web-download-content-0123456789")
	)

	var srvURL string
	webSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.URL.Path == "/api/user/sign_in":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{"token": "web-jwt-token"},
			})
			return

		case r.URL.Path == "/first-dl":
			w.Header().Set("Location", srvURL+"/final-cdn")
			w.WriteHeader(http.StatusFound)
			return

		case r.URL.Path == "/final-cdn":
			if r.Header.Get("Authorization") != "" {
				sawAuthOnData = true
			}
			if r.Header.Get("Range") == "bytes=4-11" {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes 4-11/%d", len(dlContent)))
				w.Header().Set("Content-Length", "8")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(dlContent[4:12])
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(dlContent)))
			_, _ = w.Write(dlContent)
			return

		case strings.HasPrefix(r.URL.Path, "/s3-part/"):
			if r.Header.Get("Authorization") != "" {
				sawAuthOnData = true
			}
			partStr := strings.TrimPrefix(r.URL.Path, "/s3-part/")
			partNo, _ := strconv.ParseInt(partStr, 10, 64)
			if partNo == 1 && part1Forbidden && r.URL.Query().Get("rev") == "1" {
				part1Forbidden = false
				w.WriteHeader(http.StatusForbidden)
				return
			}
			data, _ := io.ReadAll(r.Body)
			uploadedParts[partNo] = data
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Header.Get("Authorization") != "Bearer web-jwt-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 401, "message": "unauthorized"})
			return
		}

		switch r.URL.Path {
		case "/b/api/user/info":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"UID":            int64(777),
					"Nickname":       "Web测试号",
					"SpaceUsed":      int64(512),
					"SpacePermanent": int64(2048),
					"SpaceTemp":      int64(0),
				},
			})

		case "/b/api/file/list/new":
			page := r.URL.Query().Get("Page")
			if listDriftOnce {
				listDriftOnce = false
				// Report Total=2 but Next=-1 with only 1 item -> must trigger fail-closed retry.
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{
						"Next":  "-1",
						"Total": 2,
						"InfoList": []map[string]any{{
							"FileId": 10, "FileName": "doc.txt", "Size": len(dlContent), "Type": 0,
							"Etag": md5Hex(dlContent), "S3KeyFlag": "123-0", "ParentFileId": 0,
							"UpdateAt": time.Now().UTC(),
						}},
					},
				})
				return
			}
			_ = page
			items := []map[string]any{{
				"FileId": 10, "FileName": "doc.txt", "Size": len(dlContent), "Type": 0,
				"Etag": md5Hex(dlContent), "S3KeyFlag": "123-0", "ParentFileId": 0,
				"UpdateAt": time.Now().UTC(),
			}}
			if zeroIDCreated {
				items = append(items, map[string]any{
					"FileId": 99, "FileName": "zero-rapid.bin", "Size": zeroIDSize, "Type": 0,
					"Etag": zeroIDMD5, "S3KeyFlag": "123-0", "ParentFileId": 0,
					"UpdateAt": time.Now().UTC(),
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"Next":     "-1",
					"Total":    len(items),
					"InfoList": items,
				},
			})

		case "/b/api/file/download_info":
			encoded := base64.StdEncoding.EncodeToString([]byte(srvURL + "/first-dl"))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"DownloadUrl": srvURL + "/wrap?params=" + encoded},
			})

		case "/b/api/file/upload_request":
			var req struct {
				FileName string `json:"fileName"`
				Size     int64  `json:"size"`
				ETag     string `json:"etag"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.FileName == "zero-rapid.bin" {
				zeroIDCreated = true
				zeroIDSize = req.Size
				zeroIDMD5 = req.ETag
				_ = json.NewEncoder(w).Encode(map[string]any{
					"code": 0,
					"data": map[string]any{"FileId": 0, "Reuse": true},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"FileId":      int64(500),
					"Reuse":       false,
					"Bucket":      "bkt",
					"Key":         "obj/key",
					"StorageNode": "node1",
					"UploadId":    "up-web-1",
					"SliceSize":   strconv.Itoa(4096), // 4 KiB slices for test
				},
			})

		case "/b/api/file/s3_list_upload_parts":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"Parts": nil},
			})

		case "/b/api/file/s3_repare_upload_parts_batch":
			var req struct {
				PartNumberStart int64 `json:"partNumberStart"`
				PartNumberEnd   int64 `json:"partNumberEnd"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			presignedRev++
			urls := map[string]string{}
			for no := req.PartNumberStart; no < req.PartNumberEnd; no++ {
				urls[strconv.FormatInt(no, 10)] = fmt.Sprintf("%s/s3-part/%d?rev=%d", srvURL, no, presignedRev)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{"presignedUrls": urls},
			})

		case "/b/api/file/upload_complete/v2":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"file_info": map[string]any{
						"FileId": 500, "FileName": "multi-web.bin", "Size": 10000, "Type": 0,
					},
				},
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer webSrv.Close()
	srvURL = webSrv.URL

	var persistedWebTok string
	wc, err := NewClient(Options{
		Protocol:    ProtocolWeb,
		Username:    "user@example.com",
		Password:    "pass123",
		WebLoginURL: webSrv.URL,
		WebAPIURL:   webSrv.URL + "/b/api",
		WebOrigin:   webSrv.URL,
		TPSLimit:    20,
		OnTokenUpdate: func(tok string, _ time.Time) {
			persistedWebTok = tok
		},
	})
	if err != nil {
		t.Fatalf("NewClient web failed: %v", err)
	}
	defer wc.Close()

	if _, err := wc.About(ctx); err != nil {
		t.Fatalf("web About failed: %v", err)
	}
	if persistedWebTok != "web-jwt-token" {
		t.Fatalf("expected persisted web token, got %q", persistedWebTok)
	}

	// ListDir recovers from first inconsistent page and returns complete listing.
	entries, err := wc.ListDir(ctx, "")
	if err != nil || len(entries) != 1 || entries[0].Name != "doc.txt" {
		t.Fatalf("web ListDir failed: entries=%+v err=%v", entries, err)
	}

	// DownloadStream decodes params base64, follows 302, supports Range, and omits Authorization.
	rc, _, status, err := wc.DownloadStream(ctx, "doc.txt", "bytes=4-11", "")
	if err != nil {
		t.Fatalf("web DownloadStream failed: %v", err)
	}
	got, _ := io.ReadAll(rc)
	_ = rc.Close()
	if status != http.StatusPartialContent || string(got) != "download" {
		t.Fatalf("unexpected web download: status=%d got=%q", status, got)
	}

	// Zero-ID rapid upload verifies visibility through listing.
	tmpDir := t.TempDir()
	zPath := filepath.Join(tmpDir, "zero-rapid.bin")
	zData := []byte("zero-id-rapid-content")
	_ = os.WriteFile(zPath, zData, 0o600)
	zf, _ := os.Open(zPath)
	zRes, err := wc.UploadFile(ctx, "zero-rapid.bin", UploadFileOptions{File: zf, Size: int64(len(zData)), Overwrite: true})
	_ = zf.Close()
	if err != nil || !zRes.Rapid || zRes.FID != 99 {
		t.Fatalf("zero-ID rapid upload failed: res=%+v err=%v", zRes, err)
	}

	// Multipart presigned upload handles 403 URL refresh and uploads all 3 slices without Authorization.
	mPath := filepath.Join(tmpDir, "multi-web.bin")
	mData := bytes.Repeat([]byte("W"), 10000) // 4096 + 4096 + 1808 = 3 parts
	_ = os.WriteFile(mPath, mData, 0o600)
	mf, _ := os.Open(mPath)
	mRes, err := wc.UploadFile(ctx, "multi-web.bin", UploadFileOptions{
		File:        mf,
		Size:        int64(len(mData)),
		Concurrency: 3,
		Overwrite:   true,
	})
	_ = mf.Close()
	if err != nil || mRes.FID != 500 {
		t.Fatalf("web multipart upload failed: res=%+v err=%v", mRes, err)
	}
	if len(uploadedParts) != 3 || presignedRev < 2 {
		t.Fatalf("expected 3 parts and presigned refresh on 403, got parts=%d rev=%d", len(uploadedParts), presignedRev)
	}
	if sawAuthOnData {
		t.Fatal("data-plane request must never send control-plane Authorization header")
	}
}
