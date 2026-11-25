package native

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestServeGetHeadPermissionDeniedReturns403Not404(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "locked.mp4")
	if err := os.WriteFile(target, []byte("secret-payload"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	st, err := root.Stat(".")
	if err != nil {
		t.Fatal(err)
	}
	rootDev := uint64(st.Sys().(*syscall.Stat_t).Dev)

	fs := diskFS{
		root: root,
		base: ".",
		openRead: func(rel string) (*os.File, error) {
			return nil, fmt.Errorf("open %s: %w", rel, os.ErrPermission)
		},
	}
	prefix := "/tok"

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		req := httptest.NewRequest(method, prefix+"/locked.mp4", nil)
		rec := httptest.NewRecorder()
		serveGetHead(rec, req, fs, prefix, rootDev, true)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s on EACCES file: got status %d, want %d (404 masks ACL/permission errors)", method, rec.Code, http.StatusForbidden)
		}
	}
}

func TestServeGetHeadNormalRangeMissingDirAndSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	content := "0123456789abcdef"
	if err := os.WriteFile(filepath.Join(dir, "sample.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sample.txt", filepath.Join(dir, "link.txt")); err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	st, err := root.Stat(".")
	if err != nil {
		t.Fatal(err)
	}
	rootDev := uint64(st.Sys().(*syscall.Stat_t).Dev)
	prefix := "/tok"

	fsNoFollow := diskFS{root: root, base: ".", followSymlinks: false}

	// Normal GET
	rec := httptest.NewRecorder()
	serveGetHead(rec, httptest.NewRequest(http.MethodGet, prefix+"/sample.txt", nil), fsNoFollow, prefix, rootDev, true)
	if rec.Code != http.StatusOK || rec.Body.String() != content {
		t.Fatalf("GET sample.txt: code=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") == "" {
		t.Fatal("expected ETag header on GET")
	}

	// HEAD returns headers and empty body
	rec = httptest.NewRecorder()
	serveGetHead(rec, httptest.NewRequest(http.MethodHead, prefix+"/sample.txt", nil), fsNoFollow, prefix, rootDev, true)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "16" {
		t.Fatalf("HEAD sample.txt: code=%d bodyLen=%d cl=%q", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Length"))
	}

	// Range GET
	rangeReq := httptest.NewRequest(http.MethodGet, prefix+"/sample.txt", nil)
	rangeReq.Header.Set("Range", "bytes=4-7")
	rec = httptest.NewRecorder()
	serveGetHead(rec, rangeReq, fsNoFollow, prefix, rootDev, true)
	if rec.Code != http.StatusPartialContent || rec.Body.String() != "4567" {
		t.Fatalf("Range GET: code=%d body=%q", rec.Code, rec.Body.String())
	}

	// Missing file -> 404
	rec = httptest.NewRecorder()
	serveGetHead(rec, httptest.NewRequest(http.MethodGet, prefix+"/missing.txt", nil), fsNoFollow, prefix, rootDev, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET missing.txt: got %d, want 404", rec.Code)
	}

	// Directory -> 405
	rec = httptest.NewRecorder()
	serveGetHead(rec, httptest.NewRequest(http.MethodGet, prefix+"/sub", nil), fsNoFollow, prefix, rootDev, true)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET dir: got %d, want 405", rec.Code)
	}

	// Symlink blocked when followSymlinks=false
	rec = httptest.NewRecorder()
	serveGetHead(rec, httptest.NewRequest(http.MethodGet, prefix+"/link.txt", nil), fsNoFollow, prefix, rootDev, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET symlink (no follow): got %d, want 404", rec.Code)
	}

	// Symlink allowed within root when followSymlinks=true
	fsFollow := diskFS{root: root, base: ".", followSymlinks: true}
	rec = httptest.NewRecorder()
	serveGetHead(rec, httptest.NewRequest(http.MethodGet, prefix+"/link.txt", nil), fsFollow, prefix, rootDev, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET symlink (follow): got %d, want 200", rec.Code)
	}
	got, _ := io.ReadAll(rec.Result().Body)
	if string(got) != content {
		t.Fatalf("GET symlink body = %q, want %q", string(got), content)
	}
}

func TestWriteDiskWriteErrorMapsPermissionTo403(t *testing.T) {
	rec := httptest.NewRecorder()
	writeDiskWriteError(rec, fmt.Errorf("create: %w", os.ErrPermission))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403", rec.Code)
	}

	rec = httptest.NewRecorder()
	writeDiskWriteError(rec, fmt.Errorf("conflict"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d, want 409", rec.Code)
	}
}

func TestServePropfindDepth0PermissionErrorReturns403(t *testing.T) {
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked.bin")
	if err := os.WriteFile(locked, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	readable := filepath.Join(dir, "ok.bin")
	if err := os.WriteFile(readable, []byte("xyz"), 0644); err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	st, err := root.Stat(".")
	if err != nil {
		t.Fatal(err)
	}
	rootDev := uint64(st.Sys().(*syscall.Stat_t).Dev)
	fs := diskFS{
		root:     root,
		base:     ".",
		checksum: true,
		openRead: func(rel string) (*os.File, error) {
			if strings.HasSuffix(rel, "locked.bin") {
				return nil, fmt.Errorf("open %s: %w", rel, os.ErrPermission)
			}
			return root.Open(rel)
		},
	}
	prefix := "/tok"

	// Depth 0 with checksum on unreadable file -> 403 Forbidden
	req := httptest.NewRequest("PROPFIND", prefix+"/locked.bin", strings.NewReader(""))
	req.Header.Set("Depth", "0")
	rec := httptest.NewRecorder()
	servePropfind(rec, req, fs, prefix, rootDev, true)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("PROPFIND depth=0 unreadable file: got %d, want 403", rec.Code)
	}

	// Depth 1 listing parent dir should still succeed (207), compute checksum for ok.bin, and omit checksum for locked.bin
	req = httptest.NewRequest("PROPFIND", prefix+"/", strings.NewReader(""))
	req.Header.Set("Depth", "1")
	rec = httptest.NewRecorder()
	servePropfind(rec, req, fs, prefix, rootDev, true)
	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("PROPFIND depth=1 with unreadable child: got %d, want 207", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "locked.bin") || !strings.Contains(body, "ok.bin") || !strings.Contains(body, "<oc:checksum>MD5:") {
		t.Fatalf("unexpected PROPFIND body: %s", body)
	}
}
