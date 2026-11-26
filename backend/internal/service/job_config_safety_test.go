package service

import (
	"encoding/json"
	"testing"
)

// Adding a source must not re-map the sources that stay. The old algorithm
// derived each mapping from the whole selection, so one added source moved
// every existing destination subtree.
func TestJobDstMapIsStableWhenSourcesAreAdded(t *testing.T) {
	job := map[string]interface{}{
		"srcPath": []string{"/drive/a/x", "/drive/a/y"},
		"dstPath": []string{"/backup"},
	}
	if err := CleanJobInput(job); err != nil {
		t.Fatal(err)
	}
	if got := jobDstForSrc(job, "/drive/a/x", "/backup"); got != "/backup/x/" {
		t.Fatalf("x mapped to %q, want /backup/x/", got)
	}
	if got := jobDstForSrc(job, "/drive/a/y", "/backup"); got != "/backup/y/" {
		t.Fatalf("y mapped to %q, want /backup/y/", got)
	}

	job["srcPath"] = []string{"/drive/a/x", "/drive/a/y", "/drive/b"}
	if err := CleanJobInput(job); err != nil {
		t.Fatal(err)
	}
	if got := jobDstForSrc(job, "/drive/a/x", "/backup"); got != "/backup/x/" {
		t.Fatalf("x was re-mapped to %q after adding a source, want /backup/x/", got)
	}
	if got := jobDstForSrc(job, "/drive/a/y", "/backup"); got != "/backup/y/" {
		t.Fatalf("y was re-mapped to %q after adding a source, want /backup/y/", got)
	}
	// The new source is mapped with the selection it was added to.
	if got := jobDstForSrc(job, "/drive/b", "/backup"); got != "/backup/b/" {
		t.Fatalf("new source mapped to %q, want /backup/b/", got)
	}

	// Removing a source must be just as stable for the rest.
	job["srcPath"] = []string{"/drive/a/x"}
	if err := CleanJobInput(job); err != nil {
		t.Fatal(err)
	}
	if got := jobDstForSrc(job, "/drive/a/x", "/backup"); got != "/backup/x/" {
		t.Fatalf("x was re-mapped to %q after removing a source, want /backup/x/", got)
	}
}

func TestJobDstMapRecomputesWhenDestinationsChange(t *testing.T) {
	job := map[string]interface{}{
		"srcPath": []string{"/drive/a/x", "/drive/a/y"},
		"dstPath": []string{"/backup"},
	}
	if err := CleanJobInput(job); err != nil {
		t.Fatal(err)
	}
	job["dstPath"] = []string{"/backup2"}
	if err := CleanJobInput(job); err != nil {
		t.Fatal(err)
	}
	if got := jobDstForSrc(job, "/drive/a/x", "/backup2"); got != "/backup2/x/" {
		t.Fatalf("x mapped to %q under the new destination, want /backup2/x/", got)
	}
	if got := jobDstForSrc(job, "/drive/a/x", "/backup"); got != "" {
		t.Fatalf("removed destination still resolved to %q", got)
	}
}

func TestJobDstMapIgnoresEntriesOutsideTheirRoot(t *testing.T) {
	job := map[string]interface{}{
		"srcPath": []string{"/drive/a/x", "/drive/a/y"},
		"dstPath": []string{"/backup"},
	}
	stored, _ := json.Marshal(jobDstMap{
		Version: jobDstMapVersion,
		Base:    []string{"/backup/"},
		Map:     map[string]string{jobDstMapKey("/drive/a/x", "/backup"): "/etc/"},
	})
	job["dstMap"] = string(stored)
	if err := CleanJobInput(job); err != nil {
		t.Fatal(err)
	}
	if got := jobDstForSrc(job, "/drive/a/x", "/backup"); got != "/backup/x/" {
		t.Fatalf("mapping = %q, want the out-of-root stored entry ignored", got)
	}
}

func TestValidateJobDestinationConflicts(t *testing.T) {
	oldDeps := jobDeps
	jobDeps = &JobDeps{GetJobListAll: func() ([]map[string]interface{}, error) {
		return []map[string]interface{}{{
			"id": int64(1), "alistId": int64(7), "srcPath": "/a", "dstPath": "/backup/x",
		}}, nil
	}}
	defer func() { jobDeps = oldDeps }()

	job := map[string]interface{}{
		"id": int64(2), "alistId": int64(7),
		"srcPath": []string{"/b"}, "dstPath": []string{"/backup/x/sub"},
	}
	if err := CleanJobInput(job); err != nil {
		t.Fatal(err)
	}
	if err := validateJobDestinationConflicts(job); err == nil {
		t.Fatal("nested destination accepted for a second job on the same engine")
	}

	job["alistId"] = int64(8)
	if err := validateJobDestinationConflicts(job); err != nil {
		t.Fatalf("different engine rejected: %v", err)
	}

	job["alistId"] = int64(7)
	job["id"] = int64(1)
	if err := validateJobDestinationConflicts(job); err != nil {
		t.Fatalf("the job's own row was treated as a conflict: %v", err)
	}
}
