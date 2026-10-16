package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Expected signatures were computed independently with Python's hmac module
// for secret "SEC123" at 2023-11-14T22:13:20Z.
const (
	signingTestSecret = "SEC123"
	dingTalkTestSign  = "lkcPI1uoxBY1gUnCnnPH1Kkru0Hqjo7rFpA3haIVhEQ="
	larkTestSign      = "j/tImR0k8vYXRsYw0+GHVQkV1v/J/8obOuMU7PE/KDo="
)

func fixNotifyNow(t *testing.T) {
	t.Helper()
	old := notifyNow
	notifyNow = func() time.Time { return time.Unix(1700000000, 0) }
	t.Cleanup(func() { notifyNow = old })
}

// captureServer records the last request URL and JSON body and answers with
// the given response.
func captureServer(t *testing.T, response string) (*httptest.Server, *url.URL, map[string]interface{}) {
	t.Helper()
	gotURL := &url.URL{}
	gotBody := map[string]interface{}{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotURL = *r.URL
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(server.Close)
	return server, gotURL, gotBody
}

func TestSendDingTalkSignsURLWhenSecretSet(t *testing.T) {
	fixNotifyNow(t)
	server, gotURL, _ := captureServer(t, `{"errcode":0}`)

	err := sendDingTalk(notifyHTTPClient, map[string]interface{}{
		"url":    server.URL + "/robot/send?access_token=TOKEN",
		"secret": signingTestSecret,
	}, "t", "c")
	if err != nil {
		t.Fatalf("sendDingTalk() error = %v", err)
	}
	q := gotURL.Query()
	if q.Get("access_token") != "TOKEN" {
		t.Fatalf("access_token lost: %s", gotURL.RawQuery)
	}
	if q.Get("timestamp") != "1700000000000" {
		t.Fatalf("timestamp = %q, want milliseconds", q.Get("timestamp"))
	}
	if q.Get("sign") != dingTalkTestSign {
		t.Fatalf("sign = %q, want %q", q.Get("sign"), dingTalkTestSign)
	}
}

func TestSendDingTalkWithoutSecretIsUnsigned(t *testing.T) {
	server, gotURL, _ := captureServer(t, `{"errcode":0}`)
	err := sendDingTalk(notifyHTTPClient, map[string]interface{}{
		"url": server.URL + "/robot/send?access_token=TOKEN",
	}, "t", "c")
	if err != nil {
		t.Fatalf("sendDingTalk() error = %v", err)
	}
	if q := gotURL.Query(); q.Has("sign") || q.Has("timestamp") {
		t.Fatalf("unexpected signing params: %s", gotURL.RawQuery)
	}
}

func TestSendLarkSignsBodyWhenSecretSet(t *testing.T) {
	fixNotifyNow(t)
	server, _, gotBody := captureServer(t, `{"code":0}`)

	err := sendLark(notifyHTTPClient, map[string]interface{}{
		"url":    server.URL + "/open-apis/bot/v2/hook/abc",
		"secret": signingTestSecret,
	}, "t", "c")
	if err != nil {
		t.Fatalf("sendLark() error = %v", err)
	}
	// Lark requires timestamp as a string of seconds.
	if gotBody["timestamp"] != "1700000000" {
		t.Fatalf("timestamp = %#v, want \"1700000000\"", gotBody["timestamp"])
	}
	if gotBody["sign"] != larkTestSign {
		t.Fatalf("sign = %#v, want %q", gotBody["sign"], larkTestSign)
	}
}

func TestSendLarkWithoutSecretIsUnsigned(t *testing.T) {
	server, _, gotBody := captureServer(t, `{"code":0}`)
	err := sendLark(notifyHTTPClient, map[string]interface{}{
		"url": server.URL + "/open-apis/bot/v2/hook/abc",
	}, "t", "c")
	if err != nil {
		t.Fatalf("sendLark() error = %v", err)
	}
	if _, ok := gotBody["sign"]; ok {
		t.Fatalf("unexpected sign in body: %#v", gotBody)
	}
	if _, ok := gotBody["timestamp"]; ok {
		t.Fatalf("unexpected timestamp in body: %#v", gotBody)
	}
}

func TestSendLarkReportsSignatureFailure(t *testing.T) {
	server, _, _ := captureServer(t, `{"code":19021,"msg":"sign match fail or timestamp is not within one hour from current time"}`)
	err := sendLark(notifyHTTPClient, map[string]interface{}{
		"url":    server.URL + "/hook",
		"secret": "wrong",
	}, "t", "c")
	if err == nil || !strings.Contains(err.Error(), "sign match fail") {
		t.Fatalf("sendLark() error = %v, want signature failure", err)
	}
}

func TestSendBarkPostsToPush(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = io.WriteString(w, `{"code":200,"message":"success"}`)
	}))
	defer server.Close()

	err := sendBark(notifyHTTPClient, map[string]interface{}{
		"server":    server.URL + "/",
		"deviceKey": "DEVKEY",
		"group":     "OpenSync",
		"sound":     "",
	}, "标题", "正文")
	if err != nil {
		t.Fatalf("sendBark() error = %v, want code=200 treated as success", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/push" {
		t.Fatalf("request = %s %s, want POST /push", gotMethod, gotPath)
	}
	if gotBody["device_key"] != "DEVKEY" || gotBody["title"] != "标题" || gotBody["body"] != "正文" {
		t.Fatalf("body = %#v", gotBody)
	}
	if gotBody["group"] != "OpenSync" {
		t.Fatalf("group = %#v", gotBody["group"])
	}
	if _, ok := gotBody["sound"]; ok {
		t.Fatalf("empty sound should be omitted: %#v", gotBody)
	}
}

func TestSendBarkReportsProviderError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Bark answers 400 for an unknown device key.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"code":400,"message":"failed to get device token"}`)
	}))
	defer server.Close()
	err := sendBark(notifyHTTPClient, map[string]interface{}{
		"server": server.URL, "deviceKey": "bad",
	}, "t", "c")
	if err == nil {
		t.Fatal("sendBark() = nil, want failure")
	}

	ok200 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":500,"message":"push failed"}`)
	}))
	defer ok200.Close()
	err = sendBark(notifyHTTPClient, map[string]interface{}{
		"server": ok200.URL, "deviceKey": "k",
	}, "t", "c")
	if err == nil || !strings.Contains(err.Error(), "push failed") {
		t.Fatalf("sendBark() error = %v, want code=500 reported", err)
	}
}

func TestValidateNotifyParamsBark(t *testing.T) {
	cases := []struct {
		name    string
		params  map[string]interface{}
		wantErr bool
	}{
		{"default server", map[string]interface{}{"deviceKey": "k"}, false},
		{"self-hosted http", map[string]interface{}{"deviceKey": "k", "server": "http://192.168.1.2:8080"}, false},
		{"missing key", map[string]interface{}{"server": "https://api.day.app"}, true},
		{"bad scheme", map[string]interface{}{"deviceKey": "k", "server": "ftp://x"}, true},
	}
	for _, tc := range cases {
		err := validateNotifyParams(5, tc.params)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tc.name, err, tc.wantErr)
		}
	}
}

func TestRedactNotifyParamsMasksSigningSecretsAndDeviceKey(t *testing.T) {
	cases := map[int]string{
		2: `{"url":"https://oapi.dingtalk.com/robot/send?access_token=T","secret":"SECabcdef0123456789"}`,
		4: `{"url":"https://open.feishu.cn/open-apis/bot/v2/hook/x","secret":"SECabcdef0123456789"}`,
		5: `{"server":"https://api.day.app","deviceKey":"SECabcdef0123456789"}`,
	}
	for method, in := range cases {
		out := redactNotifyParams(method, in)
		if strings.Contains(out, "SECabcdef0123456789") {
			t.Errorf("method %d leaked secret: %s", method, out)
		}
	}
}

func TestResolveNotifyParamsSigningSecret(t *testing.T) {
	old := loadStoredNotify
	defer func() { loadStoredNotify = old }()
	loadStoredNotify = func(int64) (map[string]interface{}, error) {
		return map[string]interface{}{
			"params": `{"url":"https://open.feishu.cn/hook/abc","secret":"stored-secret"}`,
		}, nil
	}

	got, err := resolveNotifyParams(map[string]interface{}{
		"id": 3, "method": 4,
		"params": map[string]interface{}{"url": "https://open.feishu.cn/hook/abc", "secret": "****cret"},
	})
	if err != nil || got["secret"] != "stored-secret" {
		t.Fatalf("masked secret = %#v, %v; want stored secret restored", got, err)
	}

	got, err = resolveNotifyParams(map[string]interface{}{
		"id": 3, "method": 4,
		"params": map[string]interface{}{"url": "https://open.feishu.cn/hook/abc", "secret": ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["secret"]; ok {
		t.Fatalf("empty secret = %#v; want signing turned off", got)
	}

	got, err = resolveNotifyParams(map[string]interface{}{
		"id": 3, "method": 4,
		"params": map[string]interface{}{"url": "https://evil.example/hook", "secret": "****cret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["secret"] == "stored-secret" {
		t.Fatal("stored secret merged into a changed target")
	}
}

func TestResolveNotifyParamsBarkServerChange(t *testing.T) {
	old := loadStoredNotify
	defer func() { loadStoredNotify = old }()
	loadStoredNotify = func(int64) (map[string]interface{}, error) {
		return map[string]interface{}{
			"params": `{"server":"https://api.day.app","deviceKey":"stored-key"}`,
		}, nil
	}

	got, err := resolveNotifyParams(map[string]interface{}{
		"id": 9, "method": 5,
		"params": map[string]interface{}{"server": "https://api.day.app", "deviceKey": "****-key"},
	})
	if err != nil || got["deviceKey"] != "stored-key" {
		t.Fatalf("same server = %#v, %v; want stored key restored", got, err)
	}

	got, err = resolveNotifyParams(map[string]interface{}{
		"id": 9, "method": 5,
		"params": map[string]interface{}{"server": "https://attacker.example", "deviceKey": "****-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["deviceKey"] == "stored-key" {
		t.Fatal("stored deviceKey merged into a changed Bark server")
	}
}
