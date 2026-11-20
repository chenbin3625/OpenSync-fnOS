package service

import (
	"encoding/json"
	"testing"
)

// A retry record must be replayed only when its operation still means the same
// thing under the current job configuration. Before this check, a move-type
// record from a job that had since been switched to incremental mode still
// deleted the source.
func TestRetryFileItemsHonorCurrentConfiguration(t *testing.T) {
	copyJob := map[string]interface{}{
		"method": 0, "srcPath": []string{"/src"}, "dstPath": []string{"/dst"},
	}
	mirrorJob := map[string]interface{}{
		"method": 1, "srcPath": []string{"/src"}, "dstPath": []string{"/dst"},
	}
	moveJob := map[string]interface{}{
		"method": 2, "srcPath": []string{"/src"}, "dstPath": []string{"/dst"},
	}
	copyItem := map[string]interface{}{"type": taskItemTypeCopy.Int(), "srcPath": "/src", "dstPath": "/dst", "fileName": "a.txt", "fileSize": int64(1)}
	transferItem := map[string]interface{}{"type": taskItemTypeTransfer.Int(), "srcPath": "/src", "dstPath": "/dst", "fileName": "a.txt", "fileSize": int64(1)}
	relocationItem := map[string]interface{}{"type": taskItemTypeMove.Int(), "srcPath": "/dst/old", "dstPath": "/dst/new", "fileName": "a.txt", "fileSize": int64(1)}
	removedSourceItem := map[string]interface{}{"type": taskItemTypeCopy.Int(), "srcPath": "/elsewhere", "dstPath": "/dst", "fileName": "a.txt", "fileSize": int64(1)}
	outsideDstItem := map[string]interface{}{"type": taskItemTypeCopy.Int(), "srcPath": "/src", "dstPath": "/other", "fileName": "a.txt", "fileSize": int64(1)}
	unsafeNameItem := map[string]interface{}{"type": taskItemTypeCopy.Int(), "srcPath": "/src", "dstPath": "/dst", "fileName": "../a.txt", "fileSize": int64(1)}
	transferOutsideDstItem := map[string]interface{}{"type": taskItemTypeTransfer.Int(), "srcPath": "/src", "dstPath": "/other", "fileName": "a.txt", "fileSize": int64(1)}
	transferRemovedSourceItem := map[string]interface{}{"type": taskItemTypeTransfer.Int(), "srcPath": "/elsewhere", "dstPath": "/dst", "fileName": "a.txt", "fileSize": int64(1)}

	for _, test := range []struct {
		name       string
		job        map[string]interface{}
		item       map[string]interface{}
		wantQueued bool
		wantType   taskItemType
	}{
		{"copy is still configured", copyJob, copyItem, true, taskItemTypeCopy},
		{"transfer after leaving move mode", copyJob, transferItem, false, 0},
		{"transfer in move mode", moveJob, transferItem, true, taskItemTypeTransfer},
		{"transfer outside the current destination", moveJob, transferOutsideDstItem, false, 0},
		{"transfer from a removed source", moveJob, transferRemovedSourceItem, false, 0},
		{"relocation after leaving mirror mode", copyJob, relocationItem, false, 0},
		{"relocation in mirror mode", mirrorJob, relocationItem, true, taskItemTypeMove},
		{"source is no longer selected", copyJob, removedSourceItem, false, 0},
		{"destination is no longer selected", copyJob, outsideDstItem, false, 0},
		{"unsafe file name", copyJob, unsafeNameItem, false, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			jt := &JobTask{Job: test.job, Waiting: newCopyQueue()}
			jt.initRuntime()
			// Refused records are written through CopyHook, which arms the
			// debounced persist timer. This test never runs an executor that
			// would seal it, so stop the timer here; otherwise it fires after
			// the test and reads jobDeps while another test swaps it.
			t.Cleanup(jt.cancelPersistFlush)
			jt.retryTaskItem(test.item)
			items := jt.Waiting.snapshot()
			if test.wantQueued {
				if len(items) != 1 || items[0].CopyType != test.wantType {
					t.Fatalf("queued = %#v, want one %d item", items, test.wantType)
				}
				return
			}
			if len(items) != 0 {
				t.Fatalf("queued = %#v, want the stale record refused", items)
			}
		})
	}
}

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
