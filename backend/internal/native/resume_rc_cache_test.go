package native

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionTokenWritebackAndDisableFlag(t *testing.T) {
	origToken := `{"access_token":"old-access","token_type":"Bearer","refresh_token":"old-refresh","expiry":"2026-01-01T00:00:00Z"}`
	newToken := `{"access_token":"new-access","token_type":"Bearer","refresh_token":"new-refresh","expiry":"2026-12-31T00:00:00Z"}`

	cfg := Config{
		"od": {
			"type":  "onedrive",
			"token": origToken,
		},
	}

	// 1. Updated valid OAuth JSON token triggers OnTokenUpdate and config file has 0600 mode.
	var received map[string]TokenUpdate
	s, err := cfg.SessionFor([]SessionSpec{{Name: "od", Mode: diskReadOnly}}, SessionOptions{
		OnTokenUpdate: func(updates map[string]TokenUpdate) {
			received = updates
		},
	})
	if err != nil {
		t.Fatalf("SessionFor: %v", err)
	}
	if s.configPath == "" {
		s.Close()
		t.Fatalf("expected writable configPath for refreshable onedrive remote")
	}
	st, err := os.Stat(s.configPath)
	if err != nil {
		s.Close()
		t.Fatalf("stat configPath: %v", err)
	}
	if perm := st.Mode().Perm(); perm != 0600 {
		s.Close()
		t.Fatalf("expected 0600 permissions on rclone.conf, got %o", perm)
	}
	// The original configuration must be owned by the session, even if the
	// caller subsequently edits the map used to create it.
	cfg["od"]["drive_id"] = "changed-after-session-start"
	updatedINI := "[od]\ntype = onedrive\ntoken = " + newToken + "\n"
	if err := os.WriteFile(s.configPath, []byte(updatedINI), 0600); err != nil {
		s.Close()
		t.Fatalf("write refreshed token: %v", err)
	}
	confPath := s.configPath
	s.Close()

	if _, err := os.Stat(confPath); !os.IsNotExist(err) {
		t.Fatalf("expected session config dir to be cleaned up on Close()")
	}
	if received["od"].Fields["token"] != newToken || received["od"].Original["token"] != origToken || received["od"].Original["type"] != "onedrive" {
		t.Fatalf("expected refreshed token callback, got: %#v", received)
	}
	if received["od"].Original["drive_id"] != "" {
		t.Fatal("session's original configuration changed with its caller's map")
	}

	// 2. DisableTokenPersist suppresses process-wide hook and keeps fd-only config.
	var hookCalled atomic.Bool
	RegisterTokenUpdateHook(func(updates map[string]TokenUpdate) {
		hookCalled.Store(true)
	})
	defer RegisterTokenUpdateHook(nil)

	s2, err := cfg.SessionFor([]SessionSpec{{Name: "od", Mode: diskReadOnly}}, SessionOptions{
		DisableTokenPersist: true,
	})
	if err != nil {
		t.Fatalf("SessionFor(DisableTokenPersist): %v", err)
	}
	if s2.configPath != "" {
		// If configPath was created anyway, mutating it must still NOT invoke global hook.
		_ = os.WriteFile(s2.configPath, []byte(updatedINI), 0600)
	}
	s2.Close()
	if hookCalled.Load() {
		t.Fatalf("global token update hook should not be called when DisableTokenPersist is true")
	}
}

func TestStorageConcurrencyLimitsAndDependencyExpansion(t *testing.T) {
	cfg := Config{
		"oss": {
			"type":                "s3",
			"provider":            "Alibaba",
			"endpoint":            "oss-cn-hangzhou.aliyuncs.com",
			"access_key_id":       "ak",
			"secret_access_key":   "sk",
			"max_concurrent_jobs": "2",
		},
		"vault": {
			"type":                "crypt",
			"remote":              "oss:bucket/enc",
			"password":            "pw",
			"max_concurrent_jobs": "1",
		},
	}
	limits := cfg.StorageConcurrencyLimits()
	if limits["oss"] != 2 || limits["vault"] != 1 {
		t.Fatalf("unexpected limits: %#v", limits)
	}
	deps := cfg.ExpandRemoteDependencies("vault")
	foundOss, foundVault := false, false
	for _, d := range deps {
		if d == "oss" {
			foundOss = true
		}
		if d == "vault" {
			foundVault = true
		}
	}
	if !foundOss || !foundVault {
		t.Fatalf("expected vault dependencies to include both vault and oss, got %v", deps)
	}
}

func TestLocalMD5CacheHitInvalidationAndPersistence(t *testing.T) {
	ResetMD5CacheForTest()
	defer ResetMD5CacheForTest()

	tmp := t.TempDir()
	cachePath := filepath.Join(tmp, "md5cache.json")
	SetMD5CachePath(cachePath)

	dataDir := filepath.Join(tmp, "data")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(dataDir, "sample.bin")
	payload1 := []byte("alpha-payload-1234")
	if err := os.WriteFile(filePath, payload1, 0600); err != nil {
		t.Fatal(err)
	}
	fixedTime := time.Unix(1700000000, 0)
	if err := os.Chtimes(filePath, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	sum1, err := md5FileInRoot(root, "sample.bin")
	if err != nil {
		t.Fatalf("md5FileInRoot #1: %v", err)
	}
	want1 := md5Hex(payload1)
	if sum1 != want1 {
		t.Fatalf("got %s want %s", sum1, want1)
	}

	// Flush to disk, reset in-memory cache, reload from disk and verify cached entry loads.
	FlushMD5Cache()
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("expected persisted cache file at %s: %v", cachePath, err)
	}
	ResetMD5CacheForTest()
	SetMD5CachePath(cachePath)
	st, err := root.Stat("sample.bin")
	if err != nil {
		t.Fatal(err)
	}
	key, ok := md5CacheKeyFromFileInfo(st)
	if !ok {
		t.Fatalf("expected valid cache key")
	}
	if cached, hit := defaultMD5Cache.get(key); !hit || cached != want1 {
		t.Fatalf("expected persisted cache hit %s, got hit=%v val=%q", want1, hit, cached)
	}

	// Overwrite with same length AND restore exact mtime; CtimeNano must invalidate cache.
	time.Sleep(15 * time.Millisecond)
	payload2 := []byte("omega-payload-9876")
	if len(payload2) != len(payload1) {
		t.Fatalf("test payloads must have equal length")
	}
	if err := os.WriteFile(filePath, payload2, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filePath, fixedTime, fixedTime); err != nil {
		t.Fatal(err)
	}
	sum2, err := md5FileInRoot(root, "sample.bin")
	if err != nil {
		t.Fatalf("md5FileInRoot #2: %v", err)
	}
	want2 := md5Hex(payload2)
	if sum2 != want2 {
		t.Fatalf("expected cache invalidation on same-size + restored-mtime overwrite: got %s want %s", sum2, want2)
	}
}

func TestLocalResumableDownloadAndStalePrune(t *testing.T) {
	if os.Getenv("OPENSYNC_RCLONE_BIN") == "" {
		t.Skip("OPENSYNC_RCLONE_BIN required for rclone webdav resume test")
	}
	base := t.TempDir()
	srcBacking := filepath.Join(base, "remote-src")
	dstLocal := filepath.Join(base, "local-dst")
	for _, p := range []string{srcBacking, dstLocal} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", dstLocal)
	t.Setenv("TRIM_PKGTMP", "")

	payload := bytes.Repeat([]byte("RESUMABLE-LOCAL-DATA-BLOCK-"), 4096) // ~110 KiB
	srcFile := filepath.Join(srcBacking, "big.bin")
	if err := os.WriteFile(srcFile, payload, 0600); err != nil {
		t.Fatal(err)
	}
	srcStat, err := os.Stat(srcFile)
	if err != nil {
		t.Fatal(err)
	}

	// Spin up local rclone serve webdav on loopback.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srvCmd := exec.CommandContext(ctx, os.Getenv("OPENSYNC_RCLONE_BIN"), "serve", "webdav", srcBacking, "--addr", addr, "--config", os.DevNull)
	srvCmd.Dir = t.TempDir()
	srvCmd.Env = append(os.Environ(), "HOME="+t.TempDir(), "XDG_CONFIG_HOME="+t.TempDir())
	if err := srvCmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = srvCmd.Process.Kill()
		_ = srvCmd.Wait()
	}()
	for i := 0; i < 80; i++ {
		if conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	// Pre-seed the first half in .opensync-partial/ + one stale (>8d) orphan partial.
	partialDir := filepath.Join(dstLocal, PartialDirName)
	if err := os.MkdirAll(partialDir, 0700); err != nil {
		t.Fatal(err)
	}
	staleFile := filepath.Join(partialDir, "stale-old.part")
	if err := os.WriteFile(staleFile, []byte("orphan"), 0600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-8 * 24 * time.Hour)
	_ = os.Chtimes(staleFile, oldTime, oldTime)

	cfg := Config{
		"dav": {
			"type":   "webdav",
			"url":    "http://" + addr,
			"vendor": "other",
		},
	}
	dstVirtual, err := virtualLocal(dstLocal)
	if err != nil {
		t.Fatal(err)
	}

	// Compute deterministic partial key matching resumeDownloadOneFile(sourceArg="dav:", rel="big.bin").
	half := len(payload) / 2
	key := partialKeyHash("dav:", "big.bin")
	if err := os.WriteFile(filepath.Join(partialDir, key+".part"), payload[:half], 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dstLocal)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePartialMeta(root, path.Join(PartialDirName, key+".meta.json"), localPartialMeta{
		SrcPath:   "dav:",
		RelPath:   "big.bin",
		Size:      int64(len(payload)),
		ModTime:   srcStat.ModTime().Unix(),
		UpdatedAt: time.Now().Unix(),
	}); err != nil {
		root.Close()
		t.Fatal(err)
	}
	root.Close()

	var completedFiles []string
	err = cfg.Copy(ctx, "/dav", dstVirtual, CopyOptions{
		Transfers:      2,
		ResumeEnable:   true,
		ResumeMinBytes: 16 << 10, // 16 KiB threshold for test
	}, func(ev Event) {
		if ev.File != "" && !ev.Failed && !ev.Skipped {
			completedFiles = append(completedFiles, ev.File)
		}
	})
	if err != nil {
		t.Fatalf("resumable Copy failed: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dstLocal, "big.bin"))
	if err != nil {
		t.Fatalf("read resumed big.bin: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("resumed big.bin mismatch: got %d bytes want %d", len(got), len(payload))
	}
	if len(completedFiles) != 1 || completedFiles[0] != "big.bin" {
		t.Fatalf("expected big.bin event, got %v", completedFiles)
	}
	if _, err := os.Stat(staleFile); !os.IsNotExist(err) {
		t.Fatalf("expected stale partial file >7d to be pruned")
	}
}

func TestS3ResumableMultipartUploadAndResumeSkipParts(t *testing.T) {
	base := t.TempDir()
	srcDir := filepath.Join(base, "src")
	stateDir := filepath.Join(base, "state")
	if err := os.MkdirAll(srcDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Build a 6 MiB file so S3 multipart (min 5 MiB part size) splits into 2 parts: 5 MiB + 1 MiB.
	part1Data := bytes.Repeat([]byte("A"), int(s3MinPartSize))
	part2Data := bytes.Repeat([]byte("B"), 1<<20)
	fullData := append(append([]byte(nil), part1Data...), part2Data...)
	filePath := filepath.Join(srcDir, "movie.bin")
	if err := os.WriteFile(filePath, fullData, 0600); err != nil {
		t.Fatal(err)
	}

	var (
		mu              sync.Mutex
		uploadSeq       int
		partsByUploadID = map[string]map[int][]byte{}
		etagsByUploadID = map[string]map[int]string{}
		partUploadCount = map[int]int{}
		abortedIDs      []string
		completedObject []byte
		failPart2Once   = true
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		defer mu.Unlock()

		// Verify SigV4 Authorization header is always attached.
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			w.WriteHeader(http.StatusForbidden)
			return
		}

		switch {
		case r.Method == http.MethodPost && q.Has("uploads"):
			uploadSeq++
			uid := fmt.Sprintf("upload-%d", uploadSeq)
			partsByUploadID[uid] = map[int][]byte{}
			etagsByUploadID[uid] = map[int]string{}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `<InitiateMultipartUploadResult><UploadId>%s</UploadId></InitiateMultipartUploadResult>`, uid)

		case r.Method == http.MethodPut && q.Get("uploadId") != "" && q.Get("partNumber") != "":
			uid := q.Get("uploadId")
			pn, _ := strconv.Atoi(q.Get("partNumber"))
			body, _ := io.ReadAll(r.Body)
			if pn == 2 && failPart2Once {
				failPart2Once = false
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			partUploadCount[pn]++
			partsByUploadID[uid][pn] = body
			etag := `"` + md5Hex(body) + `"`
			etagsByUploadID[uid][pn] = etag
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusOK)

		case r.Method == http.MethodGet && q.Get("uploadId") != "":
			uid := q.Get("uploadId")
			etags, ok := etagsByUploadID[uid]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			type partXML struct {
				PartNumber int    `xml:"PartNumber"`
				ETag       string `xml:"ETag"`
			}
			type listXML struct {
				XMLName     xml.Name  `xml:"ListPartsResult"`
				IsTruncated bool      `xml:"IsTruncated"`
				Parts       []partXML `xml:"Part"`
			}
			out := listXML{}
			for pn, etag := range etags {
				out.Parts = append(out.Parts, partXML{PartNumber: pn, ETag: etag})
			}
			raw, _ := xml.Marshal(out)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(raw)

		case r.Method == http.MethodPost && q.Get("uploadId") != "":
			uid := q.Get("uploadId")
			parts := partsByUploadID[uid]
			var merged bytes.Buffer
			for pn := 1; pn <= len(parts); pn++ {
				merged.Write(parts[pn])
			}
			completedObject = merged.Bytes()
			delete(partsByUploadID, uid)
			delete(etagsByUploadID, uid)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<CompleteMultipartUploadResult/>`))

		case r.Method == http.MethodDelete && q.Get("uploadId") != "":
			uid := q.Get("uploadId")
			abortedIDs = append(abortedIDs, uid)
			delete(partsByUploadID, uid)
			delete(etagsByUploadID, uid)
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client, err := newS3ClientFromParams(map[string]string{
		"type":              "s3",
		"provider":          "Minio",
		"endpoint":          srv.URL,
		"region":            "us-east-1",
		"access_key_id":     "ak",
		"secret_access_key": "sk",
		"force_path_style":  "true",
	}, "")
	if err != nil {
		t.Fatalf("newS3ClientFromParams: %v", err)
	}

	root, err := os.OpenRoot(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	// Attempt 1: part 1 succeeds, part 2 fails once; cancel immediately after part 2's first 500
	// OR run with LowLevelRetries=1? Note maxPartRetries = max(3, ...), so force failure via context cancel after part 1!
	mu.Lock()
	failPart2Once = false
	mu.Unlock()

	ctx1, cancel1 := context.WithCancel(context.Background())
	// Upload with partConcurrency=1 so part 1 finishes deterministically before we cancel during progress callback.
	err1 := resumeUploadOneS3File(ctx1, client, root, "movie.bin", stateDir, "myoss", "mybucket", "dir/movie.bin", "5M", 1, CopyOptions{}, func(doneBytes int64, _ float64) {
		if doneBytes >= s3MinPartSize {
			cancel1()
		}
	})
	cancel1()
	if err1 == nil {
		t.Fatalf("expected attempt 1 to be cancelled after part 1")
	}

	// Verify state file exists and part 1 was uploaded exactly once.
	statePath := s3StateFilePath(stateDir, "myoss", "mybucket", "dir/movie.bin")
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("expected S3 resume state file to persist: %v", err)
	}

	// Attempt 2: must query ListParts, skip part 1, upload only part 2, complete and clean up state file.
	if err := resumeUploadOneS3File(context.Background(), client, root, "movie.bin", stateDir, "myoss", "mybucket", "dir/movie.bin", "5M", 1, CopyOptions{}, func(int64, float64) {}); err != nil {
		t.Fatalf("resumed S3 multipart upload failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if partUploadCount[1] != 1 {
		t.Fatalf("expected part 1 to be uploaded only once (resumed on attempt 2), got %d", partUploadCount[1])
	}
	if partUploadCount[2] != 1 {
		t.Fatalf("expected part 2 to be uploaded once on attempt 2, got %d", partUploadCount[2])
	}
	if !bytes.Equal(completedObject, fullData) {
		t.Fatalf("completed S3 object mismatch: got %d bytes want %d", len(completedObject), len(fullData))
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("expected resume state file to be removed upon completion")
	}
	_ = abortedIDs
}

func TestRCControllerPauseResumeAndDynamicBwLimit(t *testing.T) {
	ctrl := NewRCController("20M")
	defer ctrl.Close()

	if ctrl.BwLimit() != "20M" {
		t.Fatalf("expected initial bwLimit 20M, got %q", ctrl.BwLimit())
	}

	// Pause blocks WaitIfPaused until Resume is called.
	if err := ctrl.Pause(); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if !ctrl.IsPaused() {
		t.Fatalf("expected IsPaused() == true")
	}
	unblocked := make(chan error, 1)
	go func() {
		unblocked <- ctrl.WaitIfPaused(context.Background())
	}()
	select {
	case err := <-unblocked:
		t.Fatalf("WaitIfPaused returned early while paused: %v", err)
	case <-time.After(60 * time.Millisecond):
	}
	if err := ctrl.Resume(); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	select {
	case err := <-unblocked:
		if err != nil {
			t.Fatalf("WaitIfPaused error after Resume: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("WaitIfPaused did not unblock after Resume")
	}
	if ctrl.PausedDuration() <= 0 {
		t.Fatalf("expected positive PausedDuration after pause/resume")
	}

	// WaitIfPaused unblocks on context cancellation while paused.
	_ = ctrl.Pause()
	cctx, ccancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(40 * time.Millisecond)
		ccancel()
	}()
	if err := ctrl.WaitIfPaused(cctx); err == nil {
		t.Fatalf("expected context error from WaitIfPaused on cancel")
	}
	_ = ctrl.Resume()

	// SetBwLimit validates and pushes to an attached RC HTTP endpoint.
	var receivedRate atomic.Value
	rcSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/core/bwlimit" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		u, p, ok := r.BasicAuth()
		if !ok || u != "rcu" || p != "rcp" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(r.Body)
		receivedRate.Store(string(body))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"rate":"10M"}`))
	}))
	defer rcSrv.Close()

	addr := strings.TrimPrefix(rcSrv.URL, "http://")
	detach := ctrl.Attach(0, addr, "rcu", "rcp")
	defer detach()

	if err := ctrl.SetBwLimit(context.Background(), "10M"); err != nil {
		t.Fatalf("SetBwLimit(10M): %v", err)
	}
	if ctrl.BwLimit() != "10M" {
		t.Fatalf("expected bwLimit 10M, got %q", ctrl.BwLimit())
	}
	if got, _ := receivedRate.Load().(string); !strings.Contains(got, `"10M"`) {
		t.Fatalf("expected RC endpoint to receive 10M payload, got %q", got)
	}
	if err := ctrl.SetBwLimit(context.Background(), "off"); err != nil {
		t.Fatalf("SetBwLimit(off): %v", err)
	}
	if ctrl.BwLimit() != "" {
		t.Fatalf("expected empty bwLimit after off, got %q", ctrl.BwLimit())
	}
	if err := ctrl.SetBwLimit(context.Background(), "bad!!rate"); err == nil {
		t.Fatalf("expected invalid bwlimit to be rejected")
	}
}

func TestStreamUploadAndStreamDownloadLocalRoot(t *testing.T) {
	base := t.TempDir()
	vol := filepath.Join(base, "vol1")
	if err := os.MkdirAll(vol, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", vol)
	t.Setenv("TRIM_PKGTMP", "")

	dirVirtual, err := virtualLocal(vol)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{}
	ctx := context.Background()

	// Reject traversal & reserved partial names.
	if _, err := cfg.StreamUpload(ctx, dirVirtual, "../escape.txt", strings.NewReader("x"), SessionOptions{}); err == nil {
		t.Fatalf("expected path traversal upload to fail")
	}
	if _, err := cfg.StreamUpload(ctx, dirVirtual, PartialDirName, strings.NewReader("x"), SessionOptions{}); err == nil {
		t.Fatalf("expected reserved partial dir name upload to fail")
	}

	content := "hello opensync direct stream upload & download"
	written, err := cfg.StreamUpload(ctx, dirVirtual, "report.txt", strings.NewReader(content), SessionOptions{})
	if err != nil {
		t.Fatalf("StreamUpload: %v", err)
	}
	if written != int64(len(content)) {
		t.Fatalf("written=%d want %d", written, len(content))
	}

	rc, fileName, size, err := cfg.StreamDownload(ctx, dirVirtual+"/report.txt", SessionOptions{})
	if err != nil {
		t.Fatalf("StreamDownload: %v", err)
	}
	defer rc.Close()
	if fileName != "report.txt" || size != int64(len(content)) {
		t.Fatalf("unexpected download meta: name=%q size=%d", fileName, size)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Fatalf("downloaded content mismatch: got %q want %q", string(got), content)
	}

	// Overwriting an existing local file via StreamUpload must also work.
	updatedContent := "overwritten content v2"
	if _, err := cfg.StreamUpload(ctx, dirVirtual, "report.txt", strings.NewReader(updatedContent), SessionOptions{}); err != nil {
		t.Fatalf("StreamUpload overwrite: %v", err)
	}
	rc2, _, _, err := cfg.StreamDownload(ctx, dirVirtual+"/report.txt", SessionOptions{})
	if err != nil {
		t.Fatalf("StreamDownload after overwrite: %v", err)
	}
	got2, _ := io.ReadAll(rc2)
	_ = rc2.Close()
	if string(got2) != updatedContent {
		t.Fatalf("overwritten content mismatch: got %q want %q", string(got2), updatedContent)
	}
}

func md5Hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}
