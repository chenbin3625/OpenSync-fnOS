package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Exclusion must survive the canonical spelling a destination driver reports:
// source and destination entries are paired by canonicalFileName, so a rule
// that only matched the raw name left the escaped destination entry unmatched
// and mirror mode deleted it as extra.
func TestExcludeMatchesCanonicalSpellingOfListedName(t *testing.T) {
	matcher := newExcludeMatcher("a:b/\nnotes:1.txt")
	cases := []struct {
		path  string
		isDir bool
	}{
		{"a:b/", true},
		{"a\uFF1Ab/", true},   // fullwidth colon
		{"a%3Ab/", true},      // URL-escaped
		{"x/a\uFF1Ab/", true}, // nested
		{"notes\uFF1A1.txt", false},
		{"dir/notes%3A1.txt", false},
	}
	for _, tc := range cases {
		if !matcher.matchesListed(tc.path, tc.isDir) {
			t.Errorf("matchesListed(%q) = false, want excluded", tc.path)
		}
	}
	if matcher.matchesListed("a-b/", true) {
		t.Fatal("unrelated directory excluded")
	}
}

// A macOS/SMB source reports NFD names while the destination stores NFC; the
// two spellings are the same file and must pair instead of being re-copied and
// (in mirror mode) deleted.
func TestCanonicalFileNameFoldsUnicodeNormalization(t *testing.T) {
	nfc := "caf\u00e9.jpg"
	nfd := "cafe\u0301.jpg"
	if canonicalFileName(nfc) != canonicalFileName(nfd) {
		t.Fatalf("canonicalFileName(NFC) = %q, (NFD) = %q, want equal", canonicalFileName(nfc), canonicalFileName(nfd))
	}

	src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{"": {nfd: {Size: 5}}}}
	dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{"": {nfc: {Size: 5}}}}
	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{"method": 1})
	if len(plan.newFiles) != 0 || len(plan.extraDeletes) != 0 {
		t.Fatalf("newFiles=%d extraDeletes=%d, want the NFD/NFC pair matched", len(plan.newFiles), len(plan.extraDeletes))
	}
}

// listCacheRecorder answers fs/list and records the "refresh" flag of each
// request by path.
type listCacheRecorder struct {
	mu      sync.Mutex
	refresh map[string][]bool
}

func (rec *listCacheRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			Path    string `json:"path"`
			Refresh bool   `json:"refresh"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		rec.mu.Lock()
		rec.refresh[req.Path] = append(rec.refresh[req.Path], req.Refresh)
		rec.mu.Unlock()
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"content":[],"total":0}}`))
	}))
}

// Full sync derives its deletes from the source listing, so a non-first
// destination must not read a (possibly stale) cached source listing.
func TestFullSyncSourceListingNeverUsesCacheForLaterDestinations(t *testing.T) {
	rec := &listCacheRecorder{refresh: map[string][]bool{}}
	server := rec.server(t)
	defer server.Close()

	for _, tc := range []struct {
		method    int
		wantCache bool
	}{
		{method: 1, wantCache: false},
		{method: 0, wantCache: true},
	} {
		jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{
			"method":    tc.method,
			"useCacheS": 0,
		})
		if _, err := jt.listDir("/src/", false, nil, "/src/", true); err != nil {
			t.Fatalf("method %d: listDir() error: %v", tc.method, err)
		}
		rec.mu.Lock()
		calls := rec.refresh["/src/"]
		last := calls[len(calls)-1]
		rec.mu.Unlock()
		// useCache=1 means "do not refresh".
		if gotCache := !last; gotCache != tc.wantCache {
			t.Fatalf("method %d: later-destination source listing used cache = %v, want %v", tc.method, gotCache, tc.wantCache)
		}
	}
}
