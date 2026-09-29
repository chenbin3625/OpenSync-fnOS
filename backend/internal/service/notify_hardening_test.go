package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"opensync/internal/model"
	"opensync/internal/msg"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A cross-host redirect must not carry the webhook's custom auth header to the
// second host, and the send has to be reported as a failure.
func TestNotifyHTTPClientDoesNotFollowRedirects(t *testing.T) {
	var secondHostHits atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHostHits.Add(1)
		if r.Header.Get("X-Token") != "" {
			t.Errorf("custom auth header forwarded to redirect target")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer second.Close()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 127.0.0.1 vs localhost makes this a different host for net/http.
		target := strings.Replace(second.URL, "127.0.0.1", "localhost", 1)
		http.Redirect(w, r, target+"/collect", http.StatusFound)
	}))
	defer first.Close()

	err := sendWebhook(notifyHTTPClient, map[string]interface{}{
		"url":     first.URL,
		"headers": map[string]interface{}{"X-Token": "very-secret"},
	}, "title", "content")
	if err == nil {
		t.Fatal("sendWebhook() = nil, want a redirect reported as failure")
	}
	if !strings.Contains(err.Error(), "302") {
		t.Fatalf("error = %v, want the 3xx status", err)
	}
	if hits := secondHostHits.Load(); hits != 0 {
		t.Fatalf("redirect target was contacted %d times, want 0", hits)
	}
}

func TestCloudMetadataBlockCoversLinkLocalAlibabaAndMappedForms(t *testing.T) {
	for _, addr := range []string{
		"169.254.169.254",
		"169.254.170.2", // ECS task metadata
		"169.254.0.1",
		"100.100.100.200",
		"::ffff:169.254.169.254",
		"::ffff:100.100.100.200",
		"fe80::1",
		"fd00:ec2::254",
	} {
		if !isCloudMetadataIP(net.ParseIP(addr)) {
			t.Errorf("%s is not blocked", addr)
		}
	}
	// LAN and loopback receivers are an intentional, supported setup.
	for _, addr := range []string{
		"127.0.0.1", "::1", "10.0.0.5", "192.168.1.10", "172.16.0.1",
		"100.100.100.199", "::ffff:192.168.1.10", "fd00::1",
	} {
		if isCloudMetadataIP(net.ParseIP(addr)) {
			t.Errorf("%s is blocked, want LAN/loopback allowed", addr)
		}
	}
}

func TestValidateWebhookDestinationBlocksLiteralAndResolvedMetadata(t *testing.T) {
	old := webhookLookupIPAddr
	defer func() { webhookLookupIPAddr = old }()
	webhookLookupIPAddr = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Errorf("lookup for %s has no deadline", host)
		}
		return []net.IPAddr{{IP: net.ParseIP("::ffff:169.254.169.254")}}, nil
	}
	if err := validateWebhookDestination("http://metadata.example/latest"); err == nil {
		t.Fatal("resolved metadata address was not blocked")
	}
	if err := validateWebhookDestination("http://[::ffff:100.100.100.200]/"); err == nil {
		t.Fatal("mapped Alibaba metadata literal was not blocked")
	}
	webhookLookupIPAddr = func(context.Context, string) ([]net.IPAddr, error) {
		return nil, errors.New("no such host")
	}
	if err := validateWebhookDestination("http://unresolvable.example/"); err != nil {
		t.Fatalf("DNS failure = %v, want it left to the dial-time check", err)
	}
}

// A transient read failure used to return the incoming params unchanged — and
// they still hold the "****" placeholders, so the save overwrote the stored
// secrets with the mask.
func TestResolveNotifyParamsFailsWhenStoredConfigCannotBeRead(t *testing.T) {
	old := loadStoredNotify
	defer func() { loadStoredNotify = old }()
	loadStoredNotify = func(int64) (map[string]interface{}, error) {
		return nil, errors.New("database is locked")
	}
	_, err := resolveNotifyParams(map[string]interface{}{
		"id":     7,
		"method": 1,
		"params": map[string]interface{}{"sendKey": "****abcd"},
	})
	if err == nil {
		t.Fatal("resolveNotifyParams() = nil error, want the read failure propagated")
	}

	loadStoredNotify = func(int64) (map[string]interface{}, error) { return nil, nil }
	got, err := resolveNotifyParams(map[string]interface{}{
		"id":     7,
		"method": 1,
		"params": map[string]interface{}{"sendKey": "new-key"},
	})
	if err != nil || got["sendKey"] != "new-key" {
		t.Fatalf("not-found case = %#v, %v; want incoming params", got, err)
	}

	loadStoredNotify = func(int64) (map[string]interface{}, error) {
		return map[string]interface{}{"params": `{"sendKey":"stored-key"}`}, nil
	}
	got, err = resolveNotifyParams(map[string]interface{}{
		"id":     7,
		"method": 1,
		"params": map[string]interface{}{"sendKey": "****abcd"},
	})
	if err != nil || got["sendKey"] != "stored-key" {
		t.Fatalf("masked merge = %#v, %v; want stored secret restored", got, err)
	}
}

func TestEditNotifyRequiresEnable(t *testing.T) {
	old := loadStoredNotify
	defer func() { loadStoredNotify = old }()
	loadStoredNotify = func(int64) (map[string]interface{}, error) {
		t.Fatal("stored config read before enable was validated")
		return nil, nil
	}
	err := EditNotify(map[string]interface{}{
		"id":     7,
		"method": 1,
		"params": map[string]interface{}{"sendKey": "k"},
	})
	var pub model.PublicError
	if !errors.As(err, &pub) || string(pub) != msg.T(msg.LostPart) {
		t.Fatalf("EditNotify() error = %v, want LostPart public error", err)
	}
}

// A title containing "{content}" (e.g. from a job remark) must not be expanded
// a second time by the content substitution.
func TestSendWebhookBodyTemplateSubstitutesInOnePass(t *testing.T) {
	var got map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("request body is invalid JSON: %v\n%s", err, body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := sendWebhook(server.Client(), map[string]interface{}{
		"url":  server.URL,
		"body": `{"t":"{title}","c":"{content}"}`,
	}, `job {content} "x"`, `body {title}`)
	if err != nil {
		t.Fatalf("sendWebhook() error: %v", err)
	}
	if got["t"] != `job {content} "x"` {
		t.Fatalf("title = %q, want the literal title", got["t"])
	}
	if got["c"] != `body {title}` {
		t.Fatalf("content = %q, want the literal content", got["c"])
	}
}

// Provider responses can echo request data (a webhook receiver reflecting the
// Authorization header, a token in an error URL). Only the status, code and
// message field may reach logs and lastSendError.
func TestNotifyProviderErrorOmitsRawBody(t *testing.T) {
	err := notifyProviderError([]byte(`{"errcode":40001,"errmsg":"invalid credential","echo":"Bearer SUPERSECRET"}`), "errcode")
	if err == nil {
		t.Fatal("notifyProviderError() = nil, want failure")
	}
	if strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatalf("error leaked echoed body: %v", err)
	}
	if !strings.Contains(err.Error(), "40001") || !strings.Contains(err.Error(), "invalid credential") {
		t.Fatalf("error = %v, want errcode and errmsg kept", err)
	}
}

func TestNotifyResponseSummaryTruncatesAndMasksNonJSON(t *testing.T) {
	body := "error at https://hook.example/path?token=SUPERSECRET " + strings.Repeat("x", 500)
	got := notifyResponseSummary([]byte(body))
	if strings.Contains(got, "SUPERSECRET") {
		t.Fatalf("summary leaked URL credential: %q", got)
	}
	if len(got) > maxNotifyResponseExcerpt+len("…") {
		t.Fatalf("summary length = %d, want at most %d", len(got), maxNotifyResponseExcerpt)
	}
	if !strings.Contains(got, "hook.example") {
		t.Fatalf("summary = %q, want host kept for diagnosis", got)
	}
	json := notifyResponseSummary([]byte(`{"code":1,"msg":"bad","echo":"SUPERSECRET"}`))
	if strings.Contains(json, "SUPERSECRET") || !strings.Contains(json, "code=1") || !strings.Contains(json, "bad") {
		t.Fatalf("JSON summary = %q, want only code and message", json)
	}
}

func TestSendNotifyRequestLogsSummaryNotBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"denied","received_headers":{"Authorization":"Bearer SUPERSECRET"}}`))
	}))
	defer server.Close()
	req, err := http.NewRequest(http.MethodPost, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	logs := captureLogs(t)
	err = sendNotifyRequest(server.Client(), req)
	if err == nil || strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatalf("error = %v, want status-only failure", err)
	}
	if strings.Contains(logs.String(), "SUPERSECRET") {
		t.Fatalf("log leaked response body: %s", logs.String())
	}
}

// wecomServer fakes gettoken and message/send. The first issued token is
// rejected by the send API with rejectCode; later tokens are accepted.
type wecomServer struct {
	mu          sync.Mutex
	tokenCalls  int
	sendCalls   int
	rejectCode  int
	tokenDelay  time.Duration
	lastTokenOK string
}

func (s *wecomServer) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gettoken":
			if s.tokenDelay > 0 {
				time.Sleep(s.tokenDelay)
			}
			s.mu.Lock()
			s.tokenCalls++
			n := s.tokenCalls
			s.mu.Unlock()
			token := "token-" + r.URL.Query().Get("corpid") + "-" + string(rune('0'+n))
			_, _ = w.Write([]byte(`{"errcode":0,"access_token":"` + token + `","expires_in":7200}`))
		case "/send":
			s.mu.Lock()
			s.sendCalls++
			token := r.URL.Query().Get("access_token")
			reject := s.rejectCode != 0 && strings.HasSuffix(token, "-1")
			if !reject {
				s.lastTokenOK = token
			}
			s.mu.Unlock()
			if reject {
				_, _ = w.Write([]byte(`{"errcode":` + itoa(s.rejectCode) + `,"errmsg":"access_token expired"}`))
				return
			}
			_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// captureLogs redirects the standard logger for the rest of the test. Service
// tests do not run in parallel, so swapping the global writer is safe.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(old) })
	return &buf
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func useWeComServer(t *testing.T, server *httptest.Server) {
	t.Helper()
	oldToken, oldSend := wecomTokenURL, wecomMessageSendURL
	wecomTokenURL = server.URL + "/gettoken"
	wecomMessageSendURL = server.URL + "/send"
	resetWeComCache := func() {
		wecomTokenCache.Lock()
		wecomTokenCache.entries = nil
		wecomTokenCache.Unlock()
	}
	resetWeComCache()
	t.Cleanup(func() {
		wecomTokenURL, wecomMessageSendURL = oldToken, oldSend
		resetWeComCache()
	})
}

func TestSendWeComRefreshesTokenRejectedAsExpired(t *testing.T) {
	for _, code := range []int{40014, 41001, 42001} {
		t.Run(itoa(code), func(t *testing.T) {
			fake := &wecomServer{rejectCode: code}
			server := httptest.NewServer(fake.handler(t))
			defer server.Close()
			useWeComServer(t, server)

			params := map[string]interface{}{"corpid": "corp", "corpsecret": "secret", "agentid": "1"}
			if err := sendWeCom(server.Client(), params, "t", "c"); err != nil {
				t.Fatalf("sendWeCom() error = %v, want retry with a fresh token to succeed", err)
			}
			if fake.tokenCalls != 2 || fake.sendCalls != 2 {
				t.Fatalf("token calls=%d send calls=%d, want 2 and 2", fake.tokenCalls, fake.sendCalls)
			}
			// The fresh token is cached: the next send needs no gettoken call.
			if err := sendWeCom(server.Client(), params, "t", "c"); err != nil {
				t.Fatal(err)
			}
			if fake.tokenCalls != 2 {
				t.Fatalf("token calls=%d after a cached send, want 2", fake.tokenCalls)
			}
		})
	}
}

func TestSendWeComDoesNotRetryOtherErrors(t *testing.T) {
	fake := &wecomServer{rejectCode: 60020} // IP not allowed: not a token problem
	server := httptest.NewServer(fake.handler(t))
	defer server.Close()
	useWeComServer(t, server)

	err := sendWeCom(server.Client(), map[string]interface{}{"corpid": "corp", "corpsecret": "s", "agentid": "1"}, "t", "c")
	if err == nil {
		t.Fatal("sendWeCom() = nil, want failure")
	}
	if fake.sendCalls != 1 || fake.tokenCalls != 1 {
		t.Fatalf("send calls=%d token calls=%d, want 1 and 1", fake.sendCalls, fake.tokenCalls)
	}
}

// The cache lock is no longer held across the gettoken request, so a slow
// fetch for one corp does not stall another corp's cached send.
func TestWeComTokenFetchDoesNotHoldCacheLock(t *testing.T) {
	fake := &wecomServer{tokenDelay: 300 * time.Millisecond}
	server := httptest.NewServer(fake.handler(t))
	defer server.Close()
	useWeComServer(t, server)

	wecomTokenCache.Lock()
	wecomTokenCache.entries = map[wecomCredentials]wecomTokenEntry{
		{"cached", "s"}: {token: "cached-token", expires: time.Now().Add(time.Hour)},
	}
	wecomTokenCache.Unlock()

	slowDone := make(chan struct{})
	go func() {
		defer close(slowDone)
		_, _ = getWeComAccessToken(server.Client(), "slow", "s")
	}()
	time.Sleep(50 * time.Millisecond) // let the slow fetch start

	start := time.Now()
	token, err := getWeComAccessToken(server.Client(), "cached", "s")
	if err != nil || token != "cached-token" {
		t.Fatalf("cached token = %q, %v", token, err)
	}
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Fatalf("cached lookup took %v while another fetch was in flight, want no lock contention", elapsed)
	}
	<-slowDone
}

func TestWeComTokenErrorOmitsRawBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"errcode":40001,"errmsg":"invalid credential","corpsecret_echo":"SUPERSECRET"}`))
	}))
	defer server.Close()
	useWeComServer(t, server)

	_, err := getWeComAccessToken(server.Client(), "corp", "SUPERSECRET")
	if err == nil {
		t.Fatal("getWeComAccessToken() = nil, want failure")
	}
	if strings.Contains(err.Error(), "SUPERSECRET") || !strings.Contains(err.Error(), "40001") {
		t.Fatalf("error = %v, want errcode without echoed body", err)
	}
}
