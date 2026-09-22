package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type recordedNotifyResult struct {
	notifyID int64
	status   int
	sentAt   int64
	errMsg   string
}

// captureNotifySendResults replaces the mapper seam for one test and returns the
// captured writes.
func captureNotifySendResults(t *testing.T) *[]recordedNotifyResult {
	t.Helper()
	var mu sync.Mutex
	recorded := make([]recordedNotifyResult, 0, 4)
	d := *notifyDeps
	d.RecordSendOutcome = func(notifyID int64, status int, sentAt int64, errMsg string) error {
		mu.Lock()
		defer mu.Unlock()
		recorded = append(recorded, recordedNotifyResult{notifyID, status, sentAt, errMsg})
		return nil
	}
	restore := SetNotifyDepsForTest(&d)
	t.Cleanup(restore)
	return &recorded
}

// A failed delivery used to leave nothing but a log line, so GET /notify kept
// reporting a config whose every message was being rejected as if it were fine.
func TestFanOutRecordsFailureForUnreachableDestination(t *testing.T) {
	recorded := captureNotifySendResults(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	sendNotifyFanOut([]map[string]interface{}{{
		"id":     int64(7),
		"method": 0,
		"params": `{"url":"` + server.URL + `/hook","httpMethod":"POST"}`,
	}}, "title", "content", false)

	if len(*recorded) != 1 {
		t.Fatalf("recorded %d results, want 1", len(*recorded))
	}
	got := (*recorded)[0]
	if got.notifyID != 7 {
		t.Fatalf("notifyID = %d, want 7", got.notifyID)
	}
	if got.status != notifySendStatusFailed {
		t.Fatalf("status = %d, want %d", got.status, notifySendStatusFailed)
	}
	if got.errMsg == "" {
		t.Fatalf("errMsg is empty, want a stored reason")
	}
	if got.sentAt <= 0 {
		t.Fatalf("sentAt = %d, want a unix timestamp", got.sentAt)
	}
}

func TestFanOutRecordsSuccess(t *testing.T) {
	recorded := captureNotifySendResults(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sendNotifyFanOut([]map[string]interface{}{{
		"id":     int64(3),
		"method": 0,
		"params": `{"url":"` + server.URL + `/hook","httpMethod":"POST"}`,
	}}, "title", "content", false)

	if len(*recorded) != 1 {
		t.Fatalf("recorded %d results, want 1", len(*recorded))
	}
	got := (*recorded)[0]
	if got.status != notifySendStatusSuccess {
		t.Fatalf("status = %d, want %d", got.status, notifySendStatusSuccess)
	}
	if got.errMsg != "" {
		t.Fatalf("errMsg = %q, want empty on success", got.errMsg)
	}
}

// One broken config must not hide the others: each send is recorded on its own
// row, and a failure next to a success has to produce both.
func TestFanOutRecordsEachConfigIndependently(t *testing.T) {
	recorded := captureNotifySendResults(t)

	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer okServer.Close()

	sendNotifyFanOut([]map[string]interface{}{
		{"id": int64(1), "method": 0, "params": `{"url":"` + okServer.URL + `/hook","httpMethod":"POST"}`},
		{"id": int64(2), "method": 0, "params": `{"url":"not-a-url","httpMethod":"POST"}`},
	}, "title", "content", false)

	if len(*recorded) != 2 {
		t.Fatalf("recorded %d results, want 2", len(*recorded))
	}
	byID := map[int64]recordedNotifyResult{}
	for _, result := range *recorded {
		byID[result.notifyID] = result
	}
	if byID[1].status != notifySendStatusSuccess {
		t.Fatalf("config 1 status = %d, want success", byID[1].status)
	}
	if byID[2].status != notifySendStatusFailed {
		t.Fatalf("config 2 status = %d, want failed", byID[2].status)
	}
}

// A send skipped by notSendNull is neither a success nor a failure. Recording it
// as a success would overwrite a real failure with a delivery that never
// happened.
func TestSkippedSendRecordsNothing(t *testing.T) {
	recorded := captureNotifySendResults(t)

	sendNotifyFanOut([]map[string]interface{}{{
		"id":     int64(9),
		"method": 0,
		"params": `{"url":"https://example.test/hook","httpMethod":"POST","notSendNull":true}`,
	}}, "title", "content", true)

	if len(*recorded) != 0 {
		t.Fatalf("recorded %d results, want none for a skipped send", len(*recorded))
	}
}

func TestRecordNotifySendResultIgnoresConfigWithoutID(t *testing.T) {
	recorded := captureNotifySendResults(t)

	recordNotifySendResult(map[string]interface{}{}, false, errors.New("boom"))

	if len(*recorded) != 0 {
		t.Fatalf("recorded %d results, want none without an id", len(*recorded))
	}
}

// The stored reason is returned by GET /notify, so a provider URL inside the
// error text would hand the token straight back to the client.
func TestSanitizeNotifyErrorMasksCredentialBearingURLs(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		keepSub string
		leaked  string
	}{
		{
			name:    "serverchan send key in path",
			input:   `Post "https://sctapi.ftqq.com/SCT123456secret.send": dial tcp: i/o timeout`,
			keepSub: "sctapi.ftqq.com",
			leaked:  "SCT123456secret",
		},
		{
			name:    "dingtalk token in query",
			input:   `Get "https://oapi.dingtalk.com/robot/send?access_token=abc123def456": context deadline exceeded`,
			keepSub: "oapi.dingtalk.com",
			leaked:  "abc123def456",
		},
		{
			name:    "wecom corpsecret in query",
			input:   `Get "https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=c1&corpsecret=super-secret": EOF`,
			keepSub: "qyapi.weixin.qq.com",
			leaked:  "super-secret",
		},
		{
			name:    "lark token in path",
			input:   `Post "https://open.feishu.cn/open-apis/bot/v2/hook/9f8e7d6c-token": no route to host`,
			keepSub: "open.feishu.cn",
			leaked:  "9f8e7d6c-token",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeNotifyError(tc.input)
			if strings.Contains(got, tc.leaked) {
				t.Fatalf("sanitizeNotifyError() = %q, still contains secret %q", got, tc.leaked)
			}
			if !strings.Contains(got, tc.keepSub) {
				t.Fatalf("sanitizeNotifyError() = %q, want host %q kept for diagnosis", got, tc.keepSub)
			}
		})
	}
}

func TestSanitizeNotifyErrorTruncatesAndFallsBack(t *testing.T) {
	long := sanitizeNotifyError(strings.Repeat("失", maxNotifyErrorLength+50))
	if runes := []rune(long); len(runes) != maxNotifyErrorLength+1 {
		t.Fatalf("truncated length = %d runes, want %d", len(runes), maxNotifyErrorLength+1)
	}
	if !strings.HasSuffix(long, "…") {
		t.Fatalf("truncated text = %q, want an ellipsis suffix", long)
	}
	if got := sanitizeNotifyError("   "); got == "" {
		t.Fatalf("sanitizeNotifyError(blank) = empty, want a fallback message")
	}
}
