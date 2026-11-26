package handler

import (
	"net/http"
	"opensync/internal/msg"
	"testing"
)

func TestUpdateJobTaskPauseAndResumeDispatchAndValidation(t *testing.T) {
	origPause := pauseTaskService
	origResume := resumeTaskService
	t.Cleanup(func() {
		pauseTaskService = origPause
		resumeTaskService = origResume
	})

	var pausedID, resumedID int64
	pauseTaskService = func(id int64) error {
		pausedID = id
		return nil
	}
	resumeTaskService = func(id int64) error {
		resumedID = id
		return nil
	}

	if status, code, message := serveJSON(t, http.MethodPut, "/svr/job", UpdateJob, `{"taskId":"42","action":"pause"}`); status != http.StatusOK || code != 200 || pausedID != 42 {
		t.Fatalf("pause dispatch = %d/%d %q pausedID=%d, want 200/200 pausedID=42", status, code, message, pausedID)
	}
	if status, code, message := serveJSON(t, http.MethodPut, "/svr/job", UpdateJob, `{"taskId":42,"action":"resume"}`); status != http.StatusOK || code != 200 || resumedID != 42 {
		t.Fatalf("resume dispatch = %d/%d %q resumedID=%d, want 200/200 resumedID=42", status, code, message, resumedID)
	}

	for _, invalid := range []string{
		`{"taskId":42,"action":"pause","bwLimit":"10M"}`,
		`{"taskId":42,"action":"resume","itemIds":[1]}`,
		`{"taskId":42,"action":"pause","paths":["/a"]}`,
		`{"id":1,"action":"pause"}`,
	} {
		if status, code, message := serveJSON(t, http.MethodPut, "/svr/job", UpdateJob, invalid); status != http.StatusBadRequest || code != 500 || message != msg.T(msg.LostPart) {
			t.Fatalf("invalid pause/resume %s = %d/%d %q, want 400/500 LostPart", invalid, status, code, message)
		}
	}
}
