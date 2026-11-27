package service

import (
	"strings"
	"testing"
)

func TestRedactSensitiveErrorMasksQueryTokens(t *testing.T) {
	raw := `http request [https://tenant-my.sharepoint.com/personal/u/_layouts/15/download.aspx?UniqueId=123&tempauth=eyJhbGciOi&sig=abc123&token=tok999&access_token=act888&Authorization=BearerXYZ&X-Amz-Signature=deadbeef&X-Amz-Security-Token=sec123&X-Amz-Credential=cred456&ApiVersion=2.0] failure, status: 400`

	redacted := redactSensitiveError(raw)
	for _, leaked := range []string{
		"eyJhbGciOi", "abc123", "tok999", "act888", "BearerXYZ", "deadbeef", "sec123", "cred456",
	} {
		if strings.Contains(redacted, leaked) {
			t.Fatalf("redacted error still leaked %q: %s", leaked, redacted)
		}
	}
	for _, kept := range []string{
		"UniqueId=123", "ApiVersion=2.0", "tempauth=****", "sig=****", "token=****", "access_token=****", "Authorization=****",
	} {
		if !strings.Contains(redacted, kept) {
			t.Fatalf("redacted error missing expected fragment %q: %s", kept, redacted)
		}
	}
}

func TestJobTaskItemConstructorsRedactSensitiveError(t *testing.T) {
	raw := "download failed: https://example.com/file?tempauth=SECRET_TOKEN&ApiVersion=2.0"

	copyItem := NewCopyJobTaskItem(1, "/src", "/dst", "a.txt", int64(0), "", taskStatusFailed, &raw, taskItemFile, taskItemTypeCopy, 100)
	if copyItem.ErrMsg == nil || strings.Contains(*copyItem.ErrMsg, "SECRET_TOKEN") || !strings.Contains(*copyItem.ErrMsg, "tempauth=****") {
		t.Fatalf("copyItem.ErrMsg = %v, want redacted tempauth", copyItem.ErrMsg)
	}

	delItem := NewDeleteJobTaskItem(1, "/dst", "a.txt", int64(0), taskStatusFailed, &raw, taskItemFile, 100)
	if delItem.ErrMsg == nil || strings.Contains(*delItem.ErrMsg, "SECRET_TOKEN") || !strings.Contains(*delItem.ErrMsg, "tempauth=****") {
		t.Fatalf("delItem.ErrMsg = %v, want redacted tempauth", delItem.ErrMsg)
	}

	// Plain error without sensitive query params preserves original pointer and text.
	plain := "permission denied"
	plainItem := NewCopyJobTaskItem(1, "/src", "/dst", "b.txt", int64(10), "", taskStatusFailed, &plain, taskItemFile, taskItemTypeCopy, 100)
	if plainItem.ErrMsg != &plain || *plainItem.ErrMsg != plain {
		t.Fatalf("plainItem.ErrMsg = %v, want original pointer and text", plainItem.ErrMsg)
	}
}
