package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"opensync/internal/model"
	"opensync/internal/msg"
	"sort"
	"strings"
	"sync"
	"testing"
)

func validJobWithPaths(src, dst string) map[string]interface{} {
	return map[string]interface{}{
		"srcPath": src,
		"dstPath": dst,
		"alistId": 1,
		"method":  1,
		"isCron":  2,
	}
}

func requireDstNestedError(t *testing.T, job map[string]interface{}) {
	t.Helper()
	err := ValidateJobInput(job)
	if err == nil {
		t.Fatalf("ValidateJobInput(%v -> %v) = nil, want nested-destination rejection", job["srcPath"], job["dstPath"])
	}
	publicErr, ok := err.(model.PublicError)
	if !ok {
		t.Fatalf("error = %#v, want model.PublicError", err)
	}
	if string(publicErr) != msg.T(msg.DstPathNested) {
		t.Fatalf("message = %q, want %q", string(publicErr), msg.T(msg.DstPathNested))
	}
}

// Mirroring into /d deletes /d/x as "extra" on every round, while the second
// work keeps copying into it.
func TestValidateJobInputRejectsNestedDestinationEntries(t *testing.T) {
	requireDstNestedError(t, validJobWithPaths(`["/src"]`, `["/d","/d/x"]`))
	requireDstNestedError(t, validJobWithPaths(`["/src"]`, `["/d/x","/d"]`))
	requireDstNestedError(t, validJobWithPaths(`["/src"]`, `["/D","/d/x"]`)) // case folds
}

// Three sources without a shared parent keep their base names, except /x/a and
// /y/a which collide and fall back to full paths: D/x/a/, D/y/a/ and D/x/. The
// last contains the first.
func TestValidateJobInputRejectsResolvedDestinationsThatNest(t *testing.T) {
	srcs := []string{"/x/a", "/y/a", "/z/x"}
	resolved := resolvedDstPaths(srcs, []string{"/D"})
	want := map[string]bool{"/D/x/a/": true, "/D/y/a/": true, "/D/x/": true}
	for _, got := range resolved {
		if !want[got] {
			t.Fatalf("resolvedDstPaths = %v, want exactly %v", resolved, want)
		}
	}
	requireDstNestedError(t, validJobWithPaths(`["/x/a","/y/a","/z/x"]`, `["/D"]`))
}

func TestValidateJobInputAcceptsDisjointMultiDestination(t *testing.T) {
	for name, job := range map[string]map[string]interface{}{
		"two unrelated destinations":         validJobWithPaths(`["/src"]`, `["/backup1","/backup2"]`),
		"sibling-prefix destinations":        validJobWithPaths(`["/src"]`, `["/d","/dx"]`),
		"several sources into two targets":   validJobWithPaths(`["/media/photos","/archive/videos"]`, `["/b1","/b2"]`),
		"colliding base names disambiguated": validJobWithPaths(`["/a/docs","/b/docs"]`, `["/backup"]`),
		"siblings under a shared parent":     validJobWithPaths(`["/data/a","/data/b"]`, `["/backup"]`),
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateJobInput(job); err != nil {
				t.Fatalf("ValidateJobInput() error: %v", err)
			}
		})
	}
}

// resolvedDstPaths must describe exactly the destination roots the sync walks.
// Run the real JobTask.sync against a fake AList and compare the destination
// directories it lists at depth 1 with what validation computed.
func TestResolvedDstPathsMatchRuntimeScanRoots(t *testing.T) {
	var persisted []map[string]interface{}
	restorePersist := stubPersistJobTaskItems(t, &persisted, nil)
	defer restorePersist()

	srcs := []string{"/media/photos", "/archive/videos", "/media/music/rock"}
	dsts := []string{"/b1", "/b2/sub"}

	var mu sync.Mutex
	listed := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/fs/list" {
			_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{}}`))
			return
		}
		var req struct {
			Path string `json:"path"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		listed[req.Path] = true
		mu.Unlock()
		// Empty directories on both sides: only the top-level works run.
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"content":[]}}`))
	}))
	defer server.Close()

	srcJSON, _ := json.Marshal(srcs)
	dstJSON, _ := json.Marshal(dsts)
	jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{
		"method":        0,
		"srcPath":       string(srcJSON),
		"dstPath":       string(dstJSON),
		"useCacheS":     0,
		"useCacheT":     0,
		"scanIntervalS": 0,
		"scanIntervalT": 0,
	})
	jt.sync()

	var runtimeDst []string
	mu.Lock()
	for p := range listed {
		if strings.HasPrefix(p, "/b1") || strings.HasPrefix(p, "/b2") {
			runtimeDst = append(runtimeDst, p)
		}
	}
	mu.Unlock()
	expected := resolvedDstPaths(srcs, dsts)
	sort.Strings(runtimeDst)
	sort.Strings(expected)
	if len(expected) != len(srcs)*len(dsts) {
		t.Fatalf("resolvedDstPaths = %v, want one root per (src, dst) pair", expected)
	}
	if strings.Join(runtimeDst, ",") != strings.Join(expected, ",") {
		t.Fatalf("runtime destination roots = %v, validation resolved %v", runtimeDst, expected)
	}
}

// The nested-destination rule is newer than the data: a stored job saved
// before it existed must still load at startup (and keep being scheduled)
// instead of silently dropping out while the task list keeps showing it.
func TestNestedDestinationRuleOnlyAppliesToInteractiveChanges(t *testing.T) {
	job := validJobWithPaths(`["/src"]`, `["/d","/d/x"]`)
	if err := validateJobInput(job, false); err != nil {
		t.Fatalf("validateJobInput(init) error = %v, want the stored job accepted", err)
	}
	requireDstNestedError(t, validJobWithPaths(`["/src"]`, `["/d","/d/x"]`))
}
