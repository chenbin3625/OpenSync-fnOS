package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDecodeFileListResponseStreamsEntriesAndSkipsEnvelopeNoise(t *testing.T) {
	raw := `{"code":200,"message":"ok","extra":{"nested":[1,2,{"k":"v"}]},"data":{"readme":"x","content":[{"name":"dir","is_dir":true},{"name":"a.mkv","is_dir":false,"size":9,"hash_info":{"sha1":"ff","md5":"AB"}}],"total":2}}`
	result := FileListResult{}
	n, total, code, message, err := decodeFileListResponse(strings.NewReader(raw), 1<<20, result)
	if err != nil {
		t.Fatalf("decodeFileListResponse() error: %v", err)
	}
	if code != 200 || message != "ok" || n != 2 || total != 2 {
		t.Fatalf("code=%d message=%q n=%d total=%d", code, message, n, total)
	}
	if _, ok := result["dir/"]; !ok {
		t.Fatalf("missing dir/, got %#v", result)
	}
	if result["a.mkv"].Size != 9 || result["a.mkv"].MD5 != "ab" {
		t.Fatalf("file metadata = %#v", result["a.mkv"])
	}
}

func TestCapReaderRejectsOversizedStream(t *testing.T) {
	r := &capReader{r: strings.NewReader(strings.Repeat("a", 32)), limit: 8}
	buf := make([]byte, 32)
	n, err := r.Read(buf)
	if n != 8 || err != nil {
		t.Fatalf("first Read n=%d err=%v, want 8 nil", n, err)
	}
	n, err = r.Read(buf)
	if n != 0 || err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("second Read n=%d err=%v, want exceeds", n, err)
	}

	result := FileListResult{}
	_, _, _, _, err = decodeFileListResponse(strings.NewReader(`{"code":200,"data":{"content":[],"total":0}}`), 8, result)
	if err == nil {
		t.Fatal("decodeFileListResponse() error = nil, want size cap on oversized envelope")
	}
}

func TestFileListApiContextPostsTypedBodyAndOverlapsRemainingPages(t *testing.T) {
	d := *alistDeps
	d.FileListPageSize = 2
	restore := SetAlistDepsForTest(&d)
	defer restore()

	var mu sync.Mutex
	inflight := 0
	maxInflight := 0
	seen := map[int]alistListRequest{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/list" {
			t.Fatalf("path = %s, want /api/fs/list", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Fatalf("Content-Type = %q", ct)
		}
		var req alistListRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode list request: %v", err)
		}
		// Only page 1 refreshes; later pages read the cache it rebuilt.
		if req.Path != "/tv" || req.PerPage != 2 || req.Refresh != (req.Page == 1) {
			t.Fatalf("list request = %#v", req)
		}

		mu.Lock()
		inflight++
		if inflight > maxInflight {
			maxInflight = inflight
		}
		seen[req.Page] = req
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		mu.Lock()
		inflight--
		mu.Unlock()

		base := (req.Page - 1) * 2
		items := `{"name":"a` + strconv.Itoa(base) + `.mkv","is_dir":false,"size":1,"hash_info":{"md5":"Aa"}},{"name":"a` + strconv.Itoa(base+1) + `.mkv","is_dir":false,"size":2}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"content":[` + items + `],"total":6}}`))
	}))
	defer server.Close()

	client := &AlistClient{URL: server.URL, client: server.Client()}
	files, err := client.FileListApiContext(context.Background(), "/tv", 0, 0)
	if err != nil {
		t.Fatalf("FileListApiContext() error: %v", err)
	}
	if len(files) != 6 {
		t.Fatalf("len(files) = %d, want 6 (%#v)", len(files), files)
	}
	if files["a0.mkv"].MD5 != "aa" || files["a1.mkv"].Size != 2 {
		t.Fatalf("metadata = %#v", files)
	}
	if len(seen) != 3 {
		t.Fatalf("pages = %#v, want 1,2,3", seen)
	}
	if maxInflight < 2 {
		t.Fatalf("max inflight = %d, want overlapping remaining-page fetches", maxInflight)
	}
}

func TestFileListLimitExceededAllowsExactLimit(t *testing.T) {
	if fileListLimitExceeded(maxFileListEntries - 1) {
		t.Fatal("entry count below limit was rejected")
	}
	if fileListLimitExceeded(maxFileListEntries) {
		t.Fatal("entry count exactly at limit was rejected")
	}
	if !fileListLimitExceeded(maxFileListEntries + 1) {
		t.Fatal("entry count above limit was accepted")
	}
}

// Entry names come from the AList server, which may proxy untrusted third-party
// storage. Names that are not plain file names are dropped rather than being
// concatenated onto a scan path.
func TestAddFileListEntryRejectsUnsafeNames(t *testing.T) {
	unsafe := []string{"", ".", "..", "../escape", "a/b", "a\\b", "/abs"}
	for _, name := range unsafe {
		result := make(FileListResult)
		addFileListEntry(result, FileListEntry{Name: name, Size: 10})
		if len(result) != 0 {
			t.Errorf("addFileListEntry(%q) kept %#v, want dropped", name, result)
		}
	}

	result := make(FileListResult)
	addFileListEntry(result, FileListEntry{Name: "normal file.txt", Size: 10})
	addFileListEntry(result, FileListEntry{Name: "dir", IsDir: true})
	if _, ok := result["normal file.txt"]; !ok {
		t.Errorf("plain file name was dropped: %#v", result)
	}
	if _, ok := result["dir/"]; !ok {
		t.Errorf("plain directory name was dropped: %#v", result)
	}
}

// pagedListServer serves a 6-entry directory in pages of 2. pages overrides the
// entry names returned for a page, simulating a driver whose order shifts
// between page requests.
func pagedListServer(t *testing.T, pages map[int][]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req alistListRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode list request: %v", err)
			return
		}
		names, ok := pages[req.Page]
		if !ok {
			base := (req.Page - 1) * 2
			names = []string{"f" + strconv.Itoa(base), "f" + strconv.Itoa(base+1)}
		}
		items := make([]string, 0, len(names))
		for _, name := range names {
			items = append(items, `{"name":"`+name+`","is_dir":false,"size":1}`)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"content":[` + strings.Join(items, ",") + `],"total":6}}`))
	}))
}

// An entry that shifts across a page boundary shows up twice while its
// neighbour is never returned. The merged map hides both, so the listing must
// be rejected rather than reported as a 5-entry directory.
func TestFileListApiContextRejectsEntryDuplicatedAcrossPages(t *testing.T) {
	d := *alistDeps
	d.FileListPageSize = 2
	restore := SetAlistDepsForTest(&d)
	defer restore()

	// f3 is lost; f1 is repeated on page 2. The fetched count still equals the
	// total, so only duplicate detection catches it.
	server := pagedListServer(t, map[int][]string{2: {"f1", "f2"}})
	defer server.Close()

	client := &AlistClient{URL: server.URL, client: server.Client()}
	files, err := client.FileListApiContext(context.Background(), "/tv", 0, 0)
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("FileListApiContext() = %d entries, err %v; want listing incomplete error", len(files), err)
	}
}

// A page that comes back short while the server still reports the full total
// means entries were dropped.
func TestFileListApiContextRejectsDroppedEntries(t *testing.T) {
	d := *alistDeps
	d.FileListPageSize = 2
	restore := SetAlistDepsForTest(&d)
	defer restore()

	server := pagedListServer(t, map[int][]string{3: {"f4"}})
	defer server.Close()

	client := &AlistClient{URL: server.URL, client: server.Client()}
	files, err := client.FileListApiContext(context.Background(), "/tv", 0, 0)
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("FileListApiContext() = %d entries, err %v; want listing incomplete error", len(files), err)
	}
}

func TestFileListApiContextAcceptsConsistentPages(t *testing.T) {
	d := *alistDeps
	d.FileListPageSize = 2
	restore := SetAlistDepsForTest(&d)
	defer restore()

	server := pagedListServer(t, nil)
	defer server.Close()

	client := &AlistClient{URL: server.URL, client: server.Client()}
	files, err := client.FileListApiContext(context.Background(), "/tv", 1, 0)
	if err != nil || len(files) != 6 {
		t.Fatalf("FileListApiContext() = %d entries, err %v; want 6 entries", len(files), err)
	}
}

// Drivers that allow several objects under one name return each of them; a
// repeat across pages with different metadata is such an object, not a page
// shift, and must not fail the whole listing.
func TestFileListApiContextAcceptsSameNameObjectsAcrossPages(t *testing.T) {
	d := *alistDeps
	d.FileListPageSize = 2
	restore := SetAlistDepsForTest(&d)
	defer restore()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req alistListRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		content := `{"name":"a","is_dir":false,"size":1},{"name":"dup","is_dir":false,"size":1}`
		if req.Page == 2 {
			content = `{"name":"dup","is_dir":false,"size":2},{"name":"b","is_dir":false,"size":1}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"content":[` + content + `],"total":4}}`))
	}))
	defer server.Close()

	client := &AlistClient{URL: server.URL, client: server.Client()}
	files, err := client.FileListApiContext(context.Background(), "/tv", 1, 0)
	if err != nil || len(files) != 3 {
		t.Fatalf("FileListApiContext() = %d entries, err %v; want 3 entries", len(files), err)
	}
}
