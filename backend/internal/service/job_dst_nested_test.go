package service

import (
	"encoding/json"
	"opensync/internal/model"
	"opensync/internal/msg"
	"sort"
	"strings"
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

func TestValidateJobInputRejectsNestedDestinationEntries(t *testing.T) {
	requireDstNestedError(t, validJobWithPaths(`["/src"]`, `["/d","/d/x"]`))
	requireDstNestedError(t, validJobWithPaths(`["/src"]`, `["/d/x","/d"]`))
	requireDstNestedError(t, validJobWithPaths(`["/src"]`, `["/D","/d/x"]`)) // case folds
}

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

// resolvedDstPaths must match jobDstForSrc for every (src, dst) pair.
func TestResolvedDstPathsMatchJobDstForSrc(t *testing.T) {
	srcs := []string{"/media/photos", "/archive/videos", "/media/music/rock"}
	dsts := []string{"/b1", "/b2/sub"}
	srcJSON, _ := json.Marshal(srcs)
	dstJSON, _ := json.Marshal(dsts)
	job := map[string]interface{}{
		"method":  0,
		"srcPath": string(srcJSON),
		"dstPath": string(dstJSON),
	}

	var runtimeDst []string
	for _, s := range srcs {
		for _, d := range dsts {
			runtimeDst = append(runtimeDst, normalizeDirPath(jobDstForSrc(job, s, d)))
		}
	}
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

func TestNestedDestinationRuleOnlyAppliesToInteractiveChanges(t *testing.T) {
	job := validJobWithPaths(`["/src"]`, `["/d","/d/x"]`)
	if err := validateJobInput(job, false); err != nil {
		t.Fatalf("validateJobInput(init) error = %v, want the stored job accepted", err)
	}
	requireDstNestedError(t, validJobWithPaths(`["/src"]`, `["/d","/d/x"]`))
}
