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
	// Redirects are not followed at all, so the metadata address is never
	// dialed; the 3xx is handed back and reported as a failed send.
	resp, err := notifyHTTPClient.Do(req)
	if err != nil {
		t.Fatalf("Do() error = %v, want the redirect response returned unfollowed", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want the unfollowed 307", resp.StatusCode)
	}
	req2, err := buildNotifyRequest(http.MethodGet, server.URL, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := sendNotifyRequest(notifyHTTPClient, req2); err == nil {
		t.Fatal("sendNotifyRequest() = nil, want a redirect reported as failure")
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

// --- regression tests: placeholder persistence, enable/body normalisation,
// target fail-closed (P1-1, P2-1, P2-2, P3-5) ---

type capturedNotifyWrite struct {
	called bool
	notify map[string]interface{}
}

// stubNotifyWrites replaces the mapper write seams so the add/edit validation
// paths can be tested without a database.
func stubNotifyWrites(t *testing.T) (*capturedNotifyWrite, *capturedNotifyWrite) {
	t.Helper()
	add := &capturedNotifyWrite{}
	edit := &capturedNotifyWrite{}
	oldAdd, oldEdit := addNotifyRecord, editNotifyRecord
	addNotifyRecord = func(notify map[string]interface{}) (int64, error) {
		add.called = true
		add.notify = notify
		return 42, nil
	}
	editNotifyRecord = func(notify map[string]interface{}) error {
		edit.called = true
		edit.notify = notify
		return nil
	}
	t.Cleanup(func() { addNotifyRecord, editNotifyRecord = oldAdd, oldEdit })
	return add, edit
}

func stubStoredNotifyParams(t *testing.T, params string) {
	t.Helper()
	old := loadStoredNotify
	loadStoredNotify = func(int64) (map[string]interface{}, error) {
		return map[string]interface{}{"params": params}, nil
	}
	t.Cleanup(func() { loadStoredNotify = old })
}

func notifyWriteParams(t *testing.T, notify map[string]interface{}) map[string]interface{} {
	t.Helper()
	raw, ok := notify["params"].(string)
	if !ok {
		t.Fatalf("saved params = %#v, want a JSON string", notify["params"])
	}
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		t.Fatalf("saved params is invalid JSON: %v (%q)", err, raw)
	}
	return params
}

func wantPublicError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want a public rejection")
	}
	var public model.PublicError
	if !errors.As(err, &public) {
		t.Fatalf("error = %T %v, want model.PublicError", err, err)
	}
}

// A JSON boolean (or "true") used to be folded to 0 by util.ToInt, silently
// disabling the config the caller just enabled.
func TestAddNewNotifyNormalizesEnableShapes(t *testing.T) {
	cases := []struct {
		name   string
		enable interface{}
		want   int
		bad    bool
	}{
		{"bool true", true, 1, false},
		{"bool false", false, 0, false},
		{"number one", 1, 1, false},
		{"number zero", 0, 0, false},
		{"float zero", float64(0), 0, false},
		{"string true", "true", 1, false},
		{"string false", " FALSE ", 0, false},
		{"string one", "1", 1, false},
		{"string zero", "0", 0, false},
		{"out of range", 2, 0, true},
		{"garbage string", "yes", 0, true},
		{"nil", nil, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			add, _ := stubNotifyWrites(t)
			notify := map[string]interface{}{
				"method": 1,
				"params": map[string]interface{}{"sendKey": "send-key"},
			}
			if tc.enable != nil {
				notify["enable"] = tc.enable
			}
			_, err := AddNewNotify(notify)
			if tc.bad {
				wantPublicError(t, err)
				if add.called {
					t.Fatal("invalid enable reached the database")
				}
				return
			}
			if err != nil {
				t.Fatalf("AddNewNotify() error = %v", err)
			}
			if !add.called {
				t.Fatal("AddNewNotify() did not persist the config")
			}
			if got, _ := add.notify["enable"].(int); got != tc.want {
				t.Fatalf("stored enable = %#v, want %d", add.notify["enable"], tc.want)
			}
		})
	}
}

func TestEditNotifyNormalizesBooleanEnable(t *testing.T) {
	_, edit := stubNotifyWrites(t)
	stubStoredNotifyParams(t, `{"url":"https://example.test/hook"}`)

	err := EditNotify(map[string]interface{}{
		"id": 7, "method": 0, "enable": true,
		"params": map[string]interface{}{"url": "https://example.test/hook"},
	})
	if err != nil {
		t.Fatalf("EditNotify() error = %v", err)
	}
	if got, _ := edit.notify["enable"].(int); got != 1 {
		t.Fatalf("stored enable = %#v, want 1", edit.notify["enable"])
	}
}

// A placeholder is a display value; persisting it overwrites the stored
// credential with "****" and every later send silently uses the placeholder.
func TestAddNewNotifyRejectsRedactedPlaceholders(t *testing.T) {
	cases := map[string]map[string]interface{}{
		"headers": {"url": "https://example.test/hook", "headers": "****"},
		"body":    {"url": "https://example.test/hook", "body": "******"},
		"url":     {"url": "https://example.test/hook?access_token=****abcd"},
		"nested header": {
			"url":     "https://example.test/hook",
			"headers": map[string]interface{}{"Authorization": "****cdef"},
		},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			add, _ := stubNotifyWrites(t)
			_, err := AddNewNotify(map[string]interface{}{
				"method": 0, "enable": 1, "params": params,
			})
			wantPublicError(t, err)
			if add.called {
				t.Fatal("a redaction placeholder was written to the database")
			}
		})
	}
}

// Editing a config after changing its target URL used to persist the masked
// headers/body the list endpoint returned. The request must be refused until
// the user re-enters the secrets.
func TestEditNotifyRejectsPlaceholdersWhenTargetChanged(t *testing.T) {
	_, edit := stubNotifyWrites(t)
	stubStoredNotifyParams(t, `{"url":"https://old.example/hook","headers":{"Authorization":"Bearer REAL"},"body":"{\"text\":\"{content}\"}"}`)

	err := EditNotify(map[string]interface{}{
		"id": 7, "method": 0, "enable": 1,
		"params": map[string]interface{}{
			"url":     "https://new.example/hook",
			"headers": "****",
			"body":    "******",
		},
	})
	wantPublicError(t, err)
	if edit.called {
		t.Fatal("placeholder config was persisted")
	}
	if !strings.Contains(err.Error(), "占位符") {
		t.Fatalf("error = %q, want a re-enter prompt", err)
	}
}

// The round-trip case must keep working: target unchanged, masked values are
// replaced with the stored real ones before saving.
func TestEditNotifyRestoresPlaceholdersWhenTargetUnchanged(t *testing.T) {
	_, edit := stubNotifyWrites(t)
	stubStoredNotifyParams(t, `{"url":"https://same.example/hook?access_token=REALTOKEN","headers":{"Authorization":"Bearer REAL"},"body":"{\"text\":\"{content}\"}"}`)

	err := EditNotify(map[string]interface{}{
		"id": 7, "method": 0, "enable": 1,
		"params": map[string]interface{}{
			"url":     "https://same.example/hook?access_token=****OKEN",
			"headers": "****",
			"body":    "******",
		},
	})
	if err != nil {
		t.Fatalf("EditNotify() error = %v", err)
	}
	if !edit.called {
		t.Fatal("EditNotify() did not persist the config")
	}
	saved := notifyWriteParams(t, edit.notify)
	if saved["url"] != "https://same.example/hook?access_token=REALTOKEN" {
		t.Fatalf("saved url = %#v, want the stored URL", saved["url"])
	}
	headers, ok := saved["headers"].(map[string]interface{})
	if !ok || headers["Authorization"] != "Bearer REAL" {
		t.Fatalf("saved headers = %#v, want the stored object", saved["headers"])
	}
	if saved["body"] != `{"text":"{content}"}` {
		t.Fatalf("saved body = %#v, want the stored template", saved["body"])
	}
}

// When the user changes the target and re-enters real secrets, the edit is
// legitimate and must be saved.
func TestEditNotifyAllowsReenteredSecretsOnChangedTarget(t *testing.T) {
	_, edit := stubNotifyWrites(t)
	stubStoredNotifyParams(t, `{"url":"https://old.example/hook","headers":{"Authorization":"Bearer OLD"}}`)

	err := EditNotify(map[string]interface{}{
		"id": 7, "method": 0, "enable": 1,
		"params": map[string]interface{}{
			"url":     "https://new.example/hook",
			"headers": map[string]interface{}{"Authorization": "Bearer NEW"},
		},
	})
	if err != nil {
		t.Fatalf("EditNotify() error = %v", err)
	}
	saved := notifyWriteParams(t, edit.notify)
	headers, ok := saved["headers"].(map[string]interface{})
	if !ok || headers["Authorization"] != "Bearer NEW" {
		t.Fatalf("saved headers = %#v, want the re-entered object", saved["headers"])
	}
}

// A test send is the other network entry point; it must not send a placeholder
// and must not let a changed target inherit a stored device key.
func TestTestNotifyRejectsPlaceholdersOnChangedTarget(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	stubStoredNotifyParams(t, `{"url":"https://old.example/hook","secret":"REAL-SECRET"}`)

	err := TestNotify(map[string]interface{}{
		"id": 7, "method": 2,
		"params": map[string]interface{}{
			"url":    server.URL + "/hook",
			"secret": "****CRET",
		},
	})
	wantPublicError(t, err)
	if hits != 0 {
		t.Fatalf("test send reached the changed target %d times", hits)
	}
}

// Bark stores its target in server and allows omitting it. A stored row without
// a server must not merge the existing deviceKey into a caller-supplied server.
func TestTestNotifyBarkDoesNotShipDeviceKeyToNewServer(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	stubStoredNotifyParams(t, `{"deviceKey":"REAL-DEVICE-KEY"}`)

	err := TestNotify(map[string]interface{}{
		"id": 9, "method": 5,
		"params": map[string]interface{}{
			"server":    server.URL,
			"deviceKey": "****-KEY",
		},
	})
	wantPublicError(t, err)
	if hits != 0 {
		t.Fatalf("device key was sent to the caller-provided server")
	}
}

func TestNotifyTargetChangedFailsClosed(t *testing.T) {
	cases := []struct {
		name     string
		incoming map[string]interface{}
		existing map[string]interface{}
		want     bool
	}{
		{"unchanged url", map[string]interface{}{"url": "https://same.example/hook"}, map[string]interface{}{"url": "https://same.example/hook"}, false},
		{"changed url", map[string]interface{}{"url": "https://new.example/hook"}, map[string]interface{}{"url": "https://same.example/hook"}, true},
		{"masked url round-trip", map[string]interface{}{"url": "https://same.example/hook?token=****"}, map[string]interface{}{"url": "https://same.example/hook"}, false},
		{"absent incoming url", map[string]interface{}{}, map[string]interface{}{"url": "https://same.example/hook"}, false},
		{"stored target missing", map[string]interface{}{"server": "https://attacker.example"}, map[string]interface{}{"deviceKey": "k"}, true},
		{"wecom corp changed", map[string]interface{}{"corpid": "corp-b"}, map[string]interface{}{"corpid": "corp-a"}, true},
		{"wecom corp unchanged", map[string]interface{}{"corpid": "corp-a"}, map[string]interface{}{"corpid": "corp-a"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := notifyTargetChanged(tc.incoming, tc.existing); got != tc.want {
				t.Fatalf("notifyTargetChanged(%#v, %#v) = %v, want %v", tc.incoming, tc.existing, got, tc.want)
			}
		})
	}
}

// A REST client naturally submits body as a JSON object; it used to be stored
// and then fail every send with "Body 格式无效".
func TestAddNewNotifyNormalizesJSONObjectBody(t *testing.T) {
	add, _ := stubNotifyWrites(t)
	_, err := AddNewNotify(map[string]interface{}{
		"method": 0, "enable": true,
		"params": map[string]interface{}{
			"url":  "https://example.test/hook",
			"body": map[string]interface{}{"text": "{title}: {content}"},
		},
	})
	if err != nil {
		t.Fatalf("AddNewNotify() error = %v", err)
	}
	saved := notifyWriteParams(t, add.notify)
	body, ok := saved["body"].(string)
	if !ok {
		t.Fatalf("saved body = %#v, want a template string", saved["body"])
	}
	var delivered map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &delivered)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	if err := sendWebhook(notifyHTTPClient, map[string]interface{}{
		"url":  server.URL,
		"body": body,
	}, "title", "content"); err != nil {
		t.Fatalf("sendWebhook() error = %v, want the normalised body to be usable", err)
	}
	if delivered["text"] != "title: content" {
		t.Fatalf("delivered body = %#v", delivered)
	}
}

func TestValidateNotifyParamsRejectsUnsendableWebhookPayload(t *testing.T) {
	base := func(extra map[string]interface{}) map[string]interface{} {
		params := map[string]interface{}{"url": "https://example.test/hook"}
		for k, v := range extra {
			params[k] = v
		}
		return params
	}
	for _, tc := range []struct {
		name   string
		params map[string]interface{}
		wantOK bool
	}{
		{"valid template", base(map[string]interface{}{"body": `{"text":"{title}"}`}), true},
		{"empty template", base(map[string]interface{}{"body": "  "}), true},
		{"broken template", base(map[string]interface{}{"body": `{"n":{title}}`}), false},
		{"object body not normalised", base(map[string]interface{}{"body": map[string]interface{}{"text": "x"}}), false},
		{"string headers", base(map[string]interface{}{"headers": "Authorization: x"}), false},
		{"object headers", base(map[string]interface{}{"headers": map[string]interface{}{"Authorization": "x"}}), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNotifyParams(0, tc.params)
			if (err == nil) != tc.wantOK {
				t.Fatalf("validateNotifyParams() error = %v, wantOK %v", err, tc.wantOK)
			}
		})
	}
}

// Rows corrupted by the old bug must fail closed at send time instead of
// shipping "******" as if it were the configured body.
func TestSendNotifyRejectsLegacyRedactedRow(t *testing.T) {
	sent, err := sendNotify(map[string]interface{}{
		"method": 0,
		"params": `{"url":"https://example.test/hook","body":"******"}`,
	}, "t", "c", false)
	if sent || err == nil {
		t.Fatalf("sendNotify() = %v, %v; want a fail-closed rejection", sent, err)
	}
	var public model.PublicError
	if !errors.As(err, &public) {
		t.Fatalf("error = %T %v, want model.PublicError", err, err)
	}
}
