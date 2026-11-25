package native

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type davRequestCounter struct {
	mu       sync.Mutex
	rootDir  string
	prop0    atomic.Int64
	prop1    atomic.Int64
	puts     atomic.Int64
	reqPaths []string
}

func newTestWebDAVServer(t *testing.T, rootDir string) (*httptest.Server, *davRequestCounter) {
	t.Helper()
	counter := &davRequestCounter{rootDir: rootDir}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		counter.mu.Lock()
		counter.reqPaths = append(counter.reqPaths, r.Method+" "+r.URL.Path+" depth="+r.Header.Get("Depth"))
		counter.mu.Unlock()
		full := filepath.Join(rootDir, filepath.FromSlash(rel))

		switch r.Method {
		case "OPTIONS":
			w.Header().Set("Allow", "OPTIONS, PROPFIND, GET, HEAD, PUT, MKCOL")
			w.Header().Set("DAV", "1")
			w.WriteHeader(http.StatusOK)
		case "MKCOL":
			if err := os.MkdirAll(full, 0755); err != nil {
				w.WriteHeader(http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusCreated)
		case "PUT":
			counter.puts.Add(1)
			if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
				w.WriteHeader(http.StatusConflict)
				return
			}
			data, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if err := os.WriteFile(full, data, 0644); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusCreated)
		case "GET", "HEAD":
			http.ServeFile(w, r, full)
		case "PROPFIND":
			depth := strings.TrimSpace(r.Header.Get("Depth"))
			if depth == "0" {
				counter.prop0.Add(1)
			} else {
				counter.prop1.Add(1)
			}
			st, err := os.Stat(full)
			if err != nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var sb strings.Builder
			sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?><D:multistatus xmlns:D="DAV:">`)
			writeEntry := func(p string, info os.FileInfo) {
				href := (&url.URL{Path: p}).EscapedPath()
				if info.IsDir() && !strings.HasSuffix(href, "/") {
					href += "/"
				}
				sb.WriteString(`<D:response><D:href>`)
				sb.WriteString(strings.ReplaceAll(href, "&", "&amp;"))
				sb.WriteString(`</D:href><D:propstat><D:prop>`)
				if info.IsDir() {
					sb.WriteString(`<D:resourcetype><D:collection/></D:resourcetype>`)
				} else {
					sb.WriteString(`<D:resourcetype/><D:getcontentlength>`)
					sb.WriteString(strconv.FormatInt(info.Size(), 10))
					sb.WriteString(`</D:getcontentlength>`)
				}
				sb.WriteString(`<D:getlastmodified>`)
				sb.WriteString(info.ModTime().UTC().Format(http.TimeFormat))
				sb.WriteString(`</D:getlastmodified></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>`)
			}
			basePath := "/"
			if rel != "" && rel != "." {
				basePath = "/" + rel
			}
			writeEntry(basePath, st)
			if depth != "0" && st.IsDir() {
				entries, _ := os.ReadDir(full)
				for _, entry := range entries {
					info, err := entry.Info()
					if err != nil {
						continue
					}
					childPath := path.Join(basePath, entry.Name())
					writeEntry(childPath, info)
				}
			}
			sb.WriteString(`</D:multistatus>`)
			body := sb.String()
			w.Header().Set("Content-Type", "text/xml; charset=utf-8")
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, body)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, counter
}

func TestBulkCopyUsesDirectoryTraversalInsteadOfPerFilePropfind(t *testing.T) {
	if os.Getenv("OPENSYNC_RCLONE_BIN") == "" {
		t.Skip("real rclone required")
	}
	base := t.TempDir()
	src := filepath.Join(base, "src")
	dst := filepath.Join(base, "dst")
	if err := os.MkdirAll(dst, 0755); err != nil {
		t.Fatal(err)
	}
	// Create 80 included files across 4 dirs (> noTraverseFileThreshold) plus
	// an excluded dir that must never be traversed on destination.
	const dirCount = 4
	const filesPerDir = 20
	for d := 0; d < dirCount; d++ {
		dirPath := filepath.Join(src, fmt.Sprintf("dir%d", d))
		if err := os.MkdirAll(dirPath, 0755); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < filesPerDir; i++ {
			name := fmt.Sprintf("file-%02d.txt", i)
			if err := os.WriteFile(filepath.Join(dirPath, name), []byte("payload"), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	excluded := filepath.Join(src, "excluded")
	if err := os.MkdirAll(excluded, 0755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := os.WriteFile(filepath.Join(excluded, fmt.Sprintf("skip-%d.txt", i)), []byte("skip"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	srv, counter := newTestWebDAVServer(t, dst)
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", src)
	t.Setenv("TRIM_PKGTMP", "")
	sv, err := virtualLocal(src)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		"dav": {
			"type":   "webdav",
			"vendor": "other",
			"url":    srv.URL,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	includeFn := func(p string, info os.FileInfo) bool {
		return p != "excluded" && !strings.HasPrefix(p, "excluded/")
	}

	var copied int
	var maxScanFiles int64
	if err := cfg.Copy(ctx, sv, "/dav/backup", CopyOptions{
		Transfers:    4,
		Checkers:     4,
		SkipExisting: true,
		Include:      includeFn,
	}, func(v Event) {
		if v.ScanFiles > maxScanFiles {
			maxScanFiles = v.ScanFiles
		}
		if v.File != "" && !v.Failed {
			copied++
		}
	}); err != nil {
		t.Fatalf("initial bulk copy failed: %v", err)
	}
	if copied != dirCount*filesPerDir {
		t.Fatalf("expected %d copied files, got %d", dirCount*filesPerDir, copied)
	}
	if maxScanFiles != dirCount*filesPerDir {
		t.Fatalf("expected ScanFiles=%d, got %d", dirCount*filesPerDir, maxScanFiles)
	}
	if _, err := os.Stat(filepath.Join(dst, "backup", "excluded")); !os.IsNotExist(err) {
		t.Fatalf("excluded dir should not exist on destination")
	}

	// Second run: all 80 files already exist on destination. Without --no-traverse,
	// rclone must check them using <= ~6 directory PROPFIND Depth:1 requests
	// instead of 80 individual PROPFIND Depth:0 probes.
	counter.prop0.Store(0)
	counter.prop1.Store(0)
	counter.puts.Store(0)
	var repeatCopied int
	var maxChecks int64
	if err := cfg.Copy(ctx, sv, "/dav/backup", CopyOptions{
		Transfers:    4,
		Checkers:     4,
		SkipExisting: true,
		Include:      includeFn,
	}, func(v Event) {
		if v.Stats != nil && v.Stats.Checks > maxChecks {
			maxChecks = v.Stats.Checks
		}
		if v.File != "" && !v.Failed {
			repeatCopied++
		}
	}); err != nil {
		t.Fatalf("repeat bulk copy failed: %v", err)
	}
	if repeatCopied != 0 || counter.puts.Load() != 0 {
		t.Fatalf("expected 0 copied files on repeat run, got copied=%d puts=%d", repeatCopied, counter.puts.Load())
	}
	if p0 := counter.prop0.Load(); p0 > 4 {
		t.Fatalf("expected directory traversal (minimal PROPFIND Depth:0), got prop0=%d prop1=%d", p0, counter.prop1.Load())
	}
	if p1 := counter.prop1.Load(); p1 == 0 || p1 > 10 {
		t.Fatalf("expected directory PROPFIND Depth:1 traversal (1..10), got prop1=%d prop0=%d", p1, counter.prop0.Load())
	}
	if maxChecks < dirCount*filesPerDir {
		t.Fatalf("expected Stats.Checks >= %d, got %d", dirCount*filesPerDir, maxChecks)
	}
}

func TestSmallCopyUsesNoTraverse(t *testing.T) {
	if os.Getenv("OPENSYNC_RCLONE_BIN") == "" {
		t.Skip("real rclone required")
	}
	base := t.TempDir()
	src := filepath.Join(base, "src")
	dst := filepath.Join(base, "dst")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dst, "backup", "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	// Put 2 files in source (<= noTraverseFileThreshold) and verify destination
	// is checked via Depth:0 instead of Depth:1 directory listing.
	if err := os.WriteFile(filepath.Join(src, "sub", "one.txt"), []byte("1"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dst, "backup", "sub", "one.txt"), []byte("1"), 0644); err != nil {
		t.Fatal(err)
	}

	srv, counter := newTestWebDAVServer(t, dst)
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", src)
	t.Setenv("TRIM_PKGTMP", "")
	sv, err := virtualLocal(src)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		"dav": {
			"type":   "webdav",
			"vendor": "other",
			"url":    srv.URL,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := cfg.Copy(ctx, sv, "/dav/backup", CopyOptions{
		Transfers:    2,
		Checkers:     2,
		SkipExisting: true,
	}, func(Event) {}); err != nil {
		t.Fatalf("small copy failed: %v", err)
	}
	if counter.prop1.Load() != 0 {
		t.Fatalf("expected --no-traverse (0 Depth:1 requests) for small list, got prop1=%d prop0=%d", counter.prop1.Load(), counter.prop0.Load())
	}
	if counter.prop0.Load() == 0 {
		t.Fatalf("expected Depth:0 probe for small list, got prop0=0")
	}
}

func TestDiskPropfindSpecialFilenamesAndSymlinks(t *testing.T) {
	if os.Getenv("OPENSYNC_RCLONE_BIN") == "" {
		t.Skip("real rclone required")
	}
	base := t.TempDir()
	src := filepath.Join(base, "src")
	dst := filepath.Join(base, "dst")
	secretOutside := filepath.Join(base, "outside.txt")
	if err := os.WriteFile(secretOutside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(src, "dir & #1")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		t.Fatal(err)
	}

	specialNames := []string{
		"a & b #1 ? 100%20 'q' \"d\" 照片 🔥.txt",
		"percent %25 literal.bin",
		"normal.txt",
	}
	for _, name := range specialNames {
		if err := os.WriteFile(filepath.Join(sub, name), []byte("data:"+name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// Add > 32 regular files so directory traversal (PROPFIND Depth:1 on disk.go)
	// is exercised end-to-end along with special filenames and symlinks.
	for i := 0; i < 35; i++ {
		if err := os.WriteFile(filepath.Join(sub, fmt.Sprintf("extra-%02d.dat", i)), []byte("extra"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// Symlinks must be skipped by both WalkDir and custom servePropfind.
	if err := os.Symlink(secretOutside, filepath.Join(sub, "link-file.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(base, filepath.Join(src, "link-dir")); err != nil {
		t.Fatal(err)
	}

	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", src+":"+dst)
	t.Setenv("TRIM_PKGTMP", "")
	sv, err := virtualLocal(src)
	if err != nil {
		t.Fatal(err)
	}
	dv, err := virtualLocal(dst)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := Config{}

	var copied int
	if err := c.Copy(ctx, sv, dv+"/out", CopyOptions{Transfers: 4, Checkers: 4, Compare: 2}, func(v Event) {
		if v.File != "" && !v.Failed {
			copied++
		}
	}); err != nil {
		t.Fatalf("copy with special filenames & Compare==2 failed: %v", err)
	}
	wantTotal := len(specialNames) + 35
	if copied != wantTotal {
		t.Fatalf("expected %d copied files, got %d", wantTotal, copied)
	}
	for _, name := range specialNames {
		got, err := os.ReadFile(filepath.Join(dst, "out", "dir & #1", name))
		if err != nil || string(got) != "data:"+name {
			t.Fatalf("special file %q mismatch: got %q (%v)", name, string(got), err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dst, "out", "dir & #1", "link-file.txt")); !os.IsNotExist(err) {
		t.Fatalf("symlink file must not be copied")
	}
	if _, err := os.Lstat(filepath.Join(dst, "out", "link-dir")); !os.IsNotExist(err) {
		t.Fatalf("symlink dir must not be copied")
	}

	// Repeat run under Compare==2 (MD5 checksum via custom PROPFIND <oc:checksums>)
	// must skip every unchanged file.
	var repeatCopied int
	if err := c.Copy(ctx, sv, dv+"/out", CopyOptions{Transfers: 4, Checkers: 4, Compare: 2}, func(v Event) {
		if v.File != "" && !v.Failed {
			repeatCopied++
		}
	}); err != nil {
		t.Fatalf("repeat Compare==2 failed: %v", err)
	}
	if repeatCopied != 0 {
		t.Fatalf("expected 0 recopied files under Compare==2, got %d", repeatCopied)
	}
}

func TestLegacyRootIDBackwardCompatibility(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "X")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", root)
	t.Setenv("TRIM_PKGTMP", "")
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	legacy := legacyRootID(root, info)
	resolved, err := resolveLocal(legacy)
	if err != nil || resolved != root {
		t.Fatalf("expected legacy rootID %q to resolve to %q, got %q (%v)", legacy, root, resolved, err)
	}
	r, rel, err := openDisk(legacy)
	if err != nil {
		t.Fatalf("openDisk with legacy rootID failed: %v", err)
	}
	_ = r.Close()
	if rel != "." {
		t.Fatalf("expected rel='.', got %q", rel)
	}
}

