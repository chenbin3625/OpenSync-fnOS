package service

import (
	"context"
	"encoding/json"
	"io"
	"math"
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
	page, err := decodeFileListResponse(strings.NewReader(raw), 1<<20, result)
	if err != nil {
		t.Fatalf("decodeFileListResponse() error: %v", err)
	}
	if page.code != 200 || page.message != "ok" || page.n != 2 || page.total != 2 || page.dropped != 0 {
		t.Fatalf("page = %+v, want code=200 message=ok n=2 total=2 dropped=0", page)
	}
	if _, ok := result["dir/"]; !ok {
		t.Fatalf("missing dir/, got %#v", result)
	}
	if result["a.mkv"].Size != 9 || result["a.mkv"].MD5 != "ab" {
		t.Fatalf("file metadata = %#v", result["a.mkv"])
	}
}

// Unsafe names are no longer dropped silently while still counting towards the
// server's total: that let an "incomplete" listing pass every completeness check.
func TestDecodeFileListResponseCountsDroppedUnsafeNames(t *testing.T) {
	raw := `{"code":200,"message":"ok","data":{"content":[{"name":"ok.txt","size":1},{"name":"../escape","size":2},{"name":"a/b","size":3}],"total":3}}`
	result := FileListResult{}
	page, err := decodeFileListResponse(strings.NewReader(raw), 1<<20, result)
	if err != nil {
		t.Fatalf("decodeFileListResponse() error: %v", err)
	}
	if page.n != 3 || page.dropped != 2 {
		t.Fatalf("page = %+v, want n=3 dropped=2", page)
	}
	if len(result) != 1 {
		t.Fatalf("result = %#v, want only the safe entry", result)
	}
}

func TestCapReaderRejectsOversizedStream(t *testing.T) {
	r := &capReader{r: strings.NewReader(strings.Repeat("a", 32)), limit: 8}
	buf := make([]byte, 32)
	n, err := r.Read(buf)
	if n != 0 || err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Read n=%d err=%v, want an immediate exceeds error", n, err)
	}

	result := FileListResult{}
	if _, err := decodeFileListResponse(strings.NewReader(`{"code":200,"data":{"content":[],"total":0}}`), 8, result); err == nil {
		t.Fatal("decodeFileListResponse() error = nil, want size cap on oversized envelope")
	}
}

// A body of exactly the configured limit is legal: it used to be rejected
// because the reader errored as soon as the limit was reached instead of
// probing for EOF.
func TestCapReaderAllowsBodyExactlyAtLimit(t *testing.T) {
	r := &capReader{r: strings.NewReader(strings.Repeat("a", 8)), limit: 8}
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("io.ReadAll() error: %v", err)
	}
	if len(data) != 8 {
		t.Fatalf("read %d bytes, want 8", len(data))
	}
}

// limit+1 overflowed to a negative LimitReader length for math.MaxInt64 and
// turned every response into an empty body.
func TestReadAllWithLimitHandlesMaxInt64(t *testing.T) {
	data, err := readAllWithLimit(strings.NewReader("hello"), math.MaxInt64)
	if err != nil {
		t.Fatalf("readAllWithLimit() error: %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("data = %q, want hello", data)
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
		if !addFileListEntry(result, FileListEntry{Name: name, Size: 10}) {
			t.Errorf("addFileListEntry(%q) reported a safe name", name)
		}
		if len(result) != 0 {
			t.Errorf("addFileListEntry(%q) kept %#v, want dropped", name, result)
		}
	}

	result := make(FileListResult)
	if addFileListEntry(result, FileListEntry{Name: "normal file.txt", Size: 10}) {
		t.Error("plain file name reported as dropped")
	}
	if addFileListEntry(result, FileListEntry{Name: "dir", IsDir: true}) {
		t.Error("plain directory name reported as dropped")
	}
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

// listServer returns a fixed body for every page request.
func listServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

// A server total below the number of entries it returned is self-contradicting.
// Accepting it as "everything fit on page 1" silently truncated the directory,
// and mirror mode then deleted every destination entry past page 1 as extra.
func TestFileListApiContextRejectsInconsistentTotal(t *testing.T) {
	d := *alistDeps
	d.FileListPageSize = 2
	restore := SetAlistDepsForTest(&d)
	defer restore()

	for name, total := range map[string]int{"zero total": 0, "total below count": 1} {
		t.Run(name, func(t *testing.T) {
			server := listServer(t, `{"code":200,"message":"ok","data":{"content":[{"name":"a","size":1},{"name":"b","size":1}],"total":`+strconv.Itoa(total)+`}}`)
			defer server.Close()
			client := &AlistClient{URL: server.URL, client: server.Client()}
			files, err := client.FileListApiContext(context.Background(), "/tv", 0, 0)
			if err == nil || !strings.Contains(err.Error(), "inconsistent") {
				t.Fatalf("FileListApiContext() = %d entries, err %v; want an inconsistent-total error", len(files), err)
			}
		})
	}
}

// total == n normally means one full page holds the directory. A server that
// understates total looks identical, so the client confirms with one extra
// request instead of trusting it.
func TestFileListApiContextProbesWhenSingleFullPage(t *testing.T) {
	d := *alistDeps
	d.FileListPageSize = 2
	restore := SetAlistDepsForTest(&d)
	defer restore()

	pages := map[int]string{
		1: `[{"name":"a","size":1},{"name":"b","size":1}]`,
		2: `[{"name":"c","size":1}]`,
	}
	var mu sync.Mutex
	probes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req alistListRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		if req.Page == 2 {
			probes++
		}
		mu.Unlock()
		content, ok := pages[req.Page]
		if !ok {
			content = `[]`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"content":` + content + `,"total":2}}`))
	}))
	defer server.Close()

	client := &AlistClient{URL: server.URL, client: server.Client()}
	files, err := client.FileListApiContext(context.Background(), "/tv", 1, 0)
	if err == nil || !strings.Contains(err.Error(), "page 2 returned 1 entries") {
		t.Fatalf("FileListApiContext() = %d entries, err %v; want the understated-total error", len(files), err)
	}
	mu.Lock()
	got := probes
	mu.Unlock()
	if got != 1 {
		t.Fatalf("page 2 probes = %d, want exactly 1", got)
	}
}

func TestFileListApiContextAcceptsConfirmedSingleFullPage(t *testing.T) {
	d := *alistDeps
	d.FileListPageSize = 2
	restore := SetAlistDepsForTest(&d)
	defer restore()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req alistListRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		content := `[]`
		if req.Page == 1 {
			content = `[{"name":"a","size":1},{"name":"b","size":1}]`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"content":` + content + `,"total":2}}`))
	}))
	defer server.Close()

	client := &AlistClient{URL: server.URL, client: server.Client()}
	files, err := client.FileListApiContext(context.Background(), "/tv", 1, 0)
	if err != nil || len(files) != 2 {
		t.Fatalf("FileListApiContext() = %d entries, err %v; want 2 entries", len(files), err)
	}
}

// Dropping an unusable name silently used to leave the map short of the
// server's total while every completeness check still passed.
func TestFileListApiContextRejectsUnsafeListedName(t *testing.T) {
	d := *alistDeps
	d.FileListPageSize = 2
	restore := SetAlistDepsForTest(&d)
	defer restore()

	server := listServer(t, `{"code":200,"message":"ok","data":{"content":[{"name":"ok.txt","size":1},{"name":"../escape","size":2}],"total":2}}`)
	defer server.Close()

	client := &AlistClient{URL: server.URL, client: server.Client()}
	files, err := client.FileListApiContext(context.Background(), "/tv", 0, 0)
	if err == nil || !strings.Contains(err.Error(), "cannot be a path component") {
		t.Fatalf("FileListApiContext() = %d entries, err %v; want the unsafe-name error", len(files), err)
	}
	if len(files) != 0 {
		t.Fatalf("FileListApiContext() returned %d entries with an incomplete listing", len(files))
	}
}

// scanInterval is a per-mount request-rate limit; every page is a separate
// AList request, so pages must be throttled too (previously only page 1 was).
func TestFileListPagesHonorScanInterval(t *testing.T) {
	d := *alistDeps
	d.FileListPageSize = 1
	restore := SetAlistDepsForTest(&d)
	defer restore()

	var mu sync.Mutex
	var started []time.Time
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req alistListRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		started = append(started, time.Now())
		mu.Unlock()
		content := `{"name":"a","size":1}`
		if req.Page == 2 {
			content = `{"name":"b","size":1}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"content":[` + content + `],"total":2}}`))
	}))
	defer server.Close()

	client := &AlistClient{URL: server.URL, client: server.Client()}
	files, err := client.FileListApiContext(context.Background(), "/mnt/tv", 1, 1)
	if err != nil || len(files) != 2 {
		t.Fatalf("FileListApiContext() = %d entries, err %v; want 2 entries", len(files), err)
	}
	mu.Lock()
	times := append([]time.Time(nil), started...)
	mu.Unlock()
	if len(times) != 2 {
		t.Fatalf("page requests = %d, want 2", len(times))
	}
	if gap := times[1].Sub(times[0]); gap < 800*time.Millisecond {
		t.Fatalf("page requests were %v apart, want the configured 1s scan interval", gap)
	}
}

// A hostile/buggy server answering math.MaxInt64 for total used to overflow the
// page arithmetic into a negative worker count (sync.WaitGroup panic). The cap
// check rejects it before any arithmetic.
func TestFileListApiContextRejectsAbsurdTotalWithoutOverflow(t *testing.T) {
	d := *alistDeps
	d.FileListPageSize = 2
	restore := SetAlistDepsForTest(&d)
	defer restore()

	server := listServer(t, `{"code":200,"message":"ok","data":{"content":[{"name":"a","size":1},{"name":"b","size":1}],"total":`+
		strconv.FormatInt(math.MaxInt64, 10)+`}}`)
	defer server.Close()

	client := &AlistClient{URL: server.URL, client: server.Client()}
	_, err := client.FileListApiContext(context.Background(), "/tv", 0, 0)
	if err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("FileListApiContext() error = %v, want the entry-cap error", err)
	}
}

// Some AList-compatible servers omit total. A short first page is the whole
// directory and is accepted; a full page cannot be paged safely, so it is
// refused instead of being silently truncated.
func TestFileListApiContextHandlesMissingTotal(t *testing.T) {
	d := *alistDeps
	d.FileListPageSize = 2
	restore := SetAlistDepsForTest(&d)
	defer restore()

	short := listServer(t, `{"code":200,"message":"ok","data":{"content":[{"name":"a","size":1}]}}`)
	defer short.Close()
	client := &AlistClient{URL: short.URL, client: short.Client()}
	files, err := client.FileListApiContext(context.Background(), "/tv", 0, 0)
	if err != nil || len(files) != 1 {
		t.Fatalf("FileListApiContext() = %d entries, err %v; want 1 entry for a short page", len(files), err)
	}

	full := listServer(t, `{"code":200,"message":"ok","data":{"content":[{"name":"a","size":1},{"name":"b","size":1}]}}`)
	defer full.Close()
	fullClient := &AlistClient{URL: full.URL, client: full.Client()}
	if _, err := fullClient.FileListApiContext(context.Background(), "/tv", 0, 0); err == nil {
		t.Fatal("FileListApiContext() error = nil, want a refusal for a full page without a total")
	}
}
