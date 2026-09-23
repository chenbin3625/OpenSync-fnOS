package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"opensync/internal/model"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"strings"
	"testing"
)

func TestNotifyBlocksRedirectToCloudMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://[fd00:ec2::254]/latest/meta-data/", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	req, err := buildNotifyRequest(http.MethodGet, server.URL, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := notifyHTTPClient.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "blocked address") {
		t.Fatalf("redirect error = %v, want blocked address before dialing", err)
	}
}

func TestNotifyDialsOnlyResolvedValidatedIP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Host, "rebinding.example:") {
			t.Errorf("unexpected Host: %s", r.Host)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	dialer := &net.Dialer{}
	client := &http.Client{Transport: &http.Transport{
		DialContext: validatedNotifyDialContext(lookup, dialer.DialContext),
	}}
	req, err := http.NewRequest(http.MethodGet, "http://rebinding.example:"+port+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestNotifyNeverDialsBlockedResolvedIP(t *testing.T) {
	dialed := false
	dial := validatedNotifyDialContext(func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("fd00:ec2::254")}}, nil
	}, func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("should not dial")
	})
	_, err := dial(context.Background(), "tcp", "cloud.example:80")
	if err == nil || !strings.Contains(err.Error(), "blocked address") || dialed {
		t.Fatalf("dialed=%v err=%v, want blocked before connection", dialed, err)
	}
}

func TestNotifyBlocksIPv6CloudMetadataLiteral(t *testing.T) {
	if !isCloudMetadataIP(net.ParseIP("fd00:ec2::254")) {
		t.Fatal("IPv6 cloud metadata literal is not blocked")
	}
}

type wecomTokenTransport struct {
	requests int
}

func (transport *wecomTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	transport.requests++
	corp := req.URL.Query().Get("corpid")
	secret := req.URL.Query().Get("corpsecret")
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"access_token":"` + corp + "-" + secret + `","expires_in":7200,"errcode":0}`)),
		Header:     make(http.Header),
	}, nil
}

func TestWeComTokenCacheIsScopedToCredentials(t *testing.T) {
	wecomTokenCache.Lock()
	wecomTokenCache.entries = nil
	wecomTokenCache.Unlock()
	t.Cleanup(func() {
		wecomTokenCache.Lock()
		wecomTokenCache.entries = nil
		wecomTokenCache.Unlock()
	})
	transport := &wecomTokenTransport{}
	client := &http.Client{Transport: transport}
	for _, tc := range []struct{ corp, secret string }{
		{"alpha", "first"}, {"beta", "second"}, {"alpha", "changed"}, {"alpha", "first"},
	} {
		token, err := getWeComAccessToken(client, tc.corp, tc.secret)
		if err != nil || token != tc.corp+"-"+tc.secret {
			t.Fatalf("getWeComAccessToken(%q)=%q, err=%v", tc.corp, token, err)
		}
	}
	if transport.requests != 3 {
		t.Fatalf("token fetches=%d, want 3", transport.requests)
	}
}

type notifyErrorTransport struct{}

func (notifyErrorTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return nil, &url.Error{
		Op:  "Post",
		URL: req.URL.String(),
		Err: errors.New("dial tcp timeout"),
	}
}

func TestParamStringSupportsLegacyAndRefactorKeys(t *testing.T) {
	params := map[string]interface{}{
		"webhook": "https://example.test/hook",
	}

	if got := paramString(params, "url", "webhook"); got != "https://example.test/hook" {
		t.Fatalf("paramString() = %q, want legacy webhook value", got)
	}
}

func TestToBoolSupportsStoredIntegerFlags(t *testing.T) {
	if !util.ToBool(float64(1)) {
		t.Fatalf("util.ToBool(float64(1)) = false, want true")
	}
	if util.ToBool(float64(0)) {
		t.Fatalf("util.ToBool(float64(0)) = true, want false")
	}
}

func TestBuildNotifyRequestRejectsMissingURL(t *testing.T) {
	_, err := buildNotifyRequest(http.MethodPost, "", nil, "application/json")
	if err == nil {
		t.Fatalf("buildNotifyRequest() error = nil, want missing URL error")
	}
	if !strings.Contains(err.Error(), "url") {
		t.Fatalf("error = %q, want URL context", err)
	}
}

func TestBuildNotifyRequestRejectsInvalidMethod(t *testing.T) {
	_, err := buildNotifyRequest("BAD METHOD", "https://example.test/hook", nil, "application/json")
	if err == nil {
		t.Fatalf("buildNotifyRequest() error = nil, want invalid method error")
	}
}

func TestParseNotifyParamsRejectsInvalidJSON(t *testing.T) {
	_, err := parseNotifyParams("{invalid-json")
	if err == nil {
		t.Fatalf("parseNotifyParams() error = nil, want invalid JSON error")
	}
}

func TestValidateNotifyParamsRejectsIncompleteConfigs(t *testing.T) {
	cases := []struct {
		name   string
		method int
		params map[string]interface{}
	}{
		{
			name:   "unknown method",
			method: 99,
			params: map[string]interface{}{},
		},
		{
			name:   "serverchan missing sendKey",
			method: 1,
			params: map[string]interface{}{},
		},
		{
			name:   "wecom missing secret",
			method: 3,
			params: map[string]interface{}{"corpid": "corp", "agentid": "agent"},
		},
		{
			name:   "webhook unsupported method",
			method: 0,
			params: map[string]interface{}{"url": "https://example.test/hook", "method": "PATCH"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateNotifyParams(tc.method, tc.params); err == nil {
				t.Fatalf("validateNotifyParams() error = nil, want error")
			}
		})
	}
}

func TestValidateNotifyParamsAcceptsCompleteConfigs(t *testing.T) {
	cases := []struct {
		name   string
		method int
		params map[string]interface{}
	}{
		{
			name:   "custom webhook",
			method: 0,
			params: map[string]interface{}{"url": "https://example.test/hook", "method": "PUT"},
		},
		{
			name:   "serverchan",
			method: 1,
			params: map[string]interface{}{"sendKey": "send-key"},
		},
		{
			name:   "wecom",
			method: 3,
			params: map[string]interface{}{"corpid": "corp", "corpsecret": "secret", "agentid": "agent"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateNotifyParams(tc.method, tc.params); err != nil {
				t.Fatalf("validateNotifyParams() error = %v, want nil", err)
			}
		})
	}
}

func TestValidateNotifyParamsAcceptsInternalHTTPSWebhook(t *testing.T) {
	// A reverse proxy can expose an HTTPS hostname that resolves to a LAN
	// address. Such targets are valid for self-hosted services and must not be
	// rejected solely because of the resolved address.
	if err := validateNotifyParams(0, map[string]interface{}{
		"url":    "https://ntfy.lan.example:8443/publish",
		"method": "POST",
	}); err != nil {
		t.Fatalf("validateNotifyParams() error = %v, want nil for internal HTTPS webhook", err)
	}
}

func TestValidateNotifyParamsAcceptsHTTPWebhook(t *testing.T) {
	if err := validateNotifyParams(0, map[string]interface{}{
		"url":    "http://127.0.0.1:8080/notify",
		"method": "POST",
	}); err != nil {
		t.Fatalf("validateNotifyParams() error = %v, want nil for HTTP webhook", err)
	}
}

func TestSendWebhookCustomBodyEscapesPlaceholderValues(t *testing.T) {
	var got map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("request body is invalid JSON: %v\n%s", err, string(body))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := sendWebhook(server.Client(), map[string]interface{}{
		"url":  server.URL,
		"body": `{"text":"{title}: {content}"}`,
	}, `title "quoted"`, `content with "quotes"`)
	if err != nil {
		t.Fatalf("sendWebhook() error = %v, want escaped JSON body", err)
	}

	want := `title "quoted": content with "quotes"`
	if got["text"] != want {
		t.Fatalf("text = %q, want %q", got["text"], want)
	}
}

func TestNotifyHTTPClientAllowsLoopbackWebhook(t *testing.T) {
	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	err := sendWebhook(notifyHTTPClient, map[string]interface{}{"url": server.URL}, "title", "content")
	if err != nil {
		t.Fatalf("sendWebhook() error = %v, want loopback target to be reachable", err)
	}
	select {
	case <-received:
	default:
		t.Fatal("webhook handler was not called")
	}
}

func TestMaskNotifyURLRedactsUserInfo(t *testing.T) {
	got := maskNotifyURL("https://bot:secret@example.test/hook")
	if strings.Contains(got, "bot") || strings.Contains(got, "secret") {
		t.Fatalf("maskNotifyURL leaked userinfo: %s", got)
	}
}

func TestSendNotifyRequestDoesNotPanicWithSecretURL(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.test/hook?access_token=very-secret-token", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	client := &http.Client{Transport: notifyErrorTransport{}}

	sendErr := sendNotifyRequest(client, req)
	if sendErr == nil {
		t.Fatalf("sendNotifyRequest() error = nil, want public notify failure")
	}
	publicErr, ok := sendErr.(model.PublicError)
	if !ok {
		t.Fatalf("error type = %T, want model.PublicError", sendErr)
	}
	if string(publicErr) != msg.T(msg.NotifySendFail) {
		t.Fatalf("error = %q, want generic notify failure", publicErr)
	}
	if strings.Contains(string(publicErr), "very-secret-token") || strings.Contains(string(publicErr), "access_token") {
		t.Fatalf("error leaked secret URL: %q", publicErr)
	}
}
