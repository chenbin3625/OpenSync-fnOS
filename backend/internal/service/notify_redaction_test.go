package service

import (
	"encoding/json"
	"strings"
	"testing"
)

// headers is stored as a JSON object, so redaction must key off the field name
// rather than the value's type — otherwise Authorization is echoed in cleartext.
func TestRedactNotifyParamsMasksHeadersObject(t *testing.T) {
	in := `{"url":"https://oapi.example.com/robot/send?access_token=SECRETTOKEN1234","headers":{"Authorization":"Bearer SUPERSECRET"},"body":"{\"text\":\"hi\"}"}`

	out := redactNotifyParams(0, in)

	for _, secret := range []string{"SUPERSECRET", "SECRETTOKEN1234"} {
		if strings.Contains(out, secret) {
			t.Fatalf("redactNotifyParams leaked %q:\n%s", secret, out)
		}
	}
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(out), &params); err != nil {
		t.Fatalf("redacted output is not JSON: %v", err)
	}
	if got := params["headers"]; got != notifyRedactionMarker {
		t.Fatalf("headers = %#v, want %q", got, notifyRedactionMarker)
	}
	// The marker has to be recognizable so an edit restores the stored value.
	if !isMaskedSecretValue(notifyRedactionMarker) {
		t.Fatal("headers marker is not recognized by isMaskedSecretValue")
	}
}

func TestRedactNotifyParamsLeavesEmptyHeadersAlone(t *testing.T) {
	out := redactNotifyParams(0, `{"url":"https://example.test/hook","headers":{}}`)
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(out), &params); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	headers, ok := params["headers"].(map[string]interface{})
	if !ok || len(headers) != 0 {
		t.Fatalf("headers = %#v, want an empty object", params["headers"])
	}
}

func TestRedactNotifyParamsMasksNonStringSecret(t *testing.T) {
	out := redactNotifyParams(1, `{"sendKey":{"unexpected":"SUPERSECRET"}}`)
	if strings.Contains(out, "SUPERSECRET") {
		t.Fatalf("non-string secret leaked:\n%s", out)
	}
}

// A changed delivery target must not inherit the stored credential.
func TestNotifyTargetChangedGuardsSecretMerge(t *testing.T) {
	existing := map[string]interface{}{"url": "https://good.example/hook"}

	cases := []struct {
		name     string
		incoming map[string]interface{}
		want     bool
	}{
		{"attacker url", map[string]interface{}{"url": "https://attacker.example/hook"}, true},
		{"same url", map[string]interface{}{"url": "https://good.example/hook"}, false},
		{"masked url round-trip", map[string]interface{}{"url": "https://good.example/****1234"}, false},
		{"absent url", map[string]interface{}{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := notifyTargetChanged(tc.incoming, existing); got != tc.want {
				t.Fatalf("notifyTargetChanged(%#v) = %v, want %v", tc.incoming, got, tc.want)
			}
		})
	}
}

// A name-based list of sensitive parameters can never be complete. Real webhook
// URLs carry credentials under k, sig, sign, auth, pwd and other short names,
// all of which used to be returned in cleartext.
func TestMaskNotifyURLMasksEveryQueryValue(t *testing.T) {
	cases := []struct {
		name   string
		rawURL string
		secret string
	}{
		{"short key name", "https://sctapi.example/send?k=SUPERSECRETVALUE", "SUPERSECRETVALUE"},
		{"signature", "https://oapi.example/robot?sig=SUPERSECRETVALUE", "SUPERSECRETVALUE"},
		{"auth", "https://hook.example/push?auth=SUPERSECRETVALUE", "SUPERSECRETVALUE"},
		{"sign", "https://hook.example/push?sign=SUPERSECRETVALUE", "SUPERSECRETVALUE"},
		{"password", "https://hook.example/push?pwd=SUPERSECRETVALUE", "SUPERSECRETVALUE"},
		{"opaque single letter", "https://hook.example/push?u=SUPERSECRETVALUE", "SUPERSECRETVALUE"},
		{"known name still masked", "https://oapi.example/robot?access_token=SUPERSECRETVALUE", "SUPERSECRETVALUE"},
		{"second parameter", "https://hook.example/push?id=7&t=SUPERSECRETVALUE", "SUPERSECRETVALUE"},
		{"percent-encoded value", "https://hook.example/push?k=SUPER%2FSECRETVALUE", "SECRETVALUE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := maskNotifyURL(tc.rawURL)
			if strings.Contains(got, tc.secret) {
				t.Fatalf("maskNotifyURL(%q) = %q, still contains %q", tc.rawURL, got, tc.secret)
			}
			if !strings.Contains(got, "hook.example") &&
				!strings.Contains(got, "oapi.example") &&
				!strings.Contains(got, "sctapi.example") {
				t.Fatalf("maskNotifyURL(%q) = %q, host should stay visible", tc.rawURL, got)
			}
		})
	}
}

// A query string url.ParseQuery rejects used to be written back verbatim,
// because nothing was recognised as maskable and RawQuery was left untouched.
func TestMaskNotifyURLMasksUnparseableQuery(t *testing.T) {
	got := maskNotifyURL("https://hook.example/push?token=SUPERSECRETVALUE&%zz")
	if strings.Contains(got, "SUPERSECRETVALUE") {
		t.Fatalf("maskNotifyURL leaked a secret from an unparseable query: %q", got)
	}
}

// Masking must not turn the summary into something unrecognisable: the host and
// the parameter names stay, only values are replaced.
func TestMaskNotifyURLKeepsParameterNamesAndOrder(t *testing.T) {
	got := maskNotifyURL("https://hook.example/push?id=7&access_token=SUPERSECRETVALUE&debug")
	for _, want := range []string{"hook.example", "id=", "access_token=", "debug"} {
		if !strings.Contains(got, want) {
			t.Fatalf("maskNotifyURL() = %q, want it to keep %q", got, want)
		}
	}
	if strings.Index(got, "id=") > strings.Index(got, "access_token=") {
		t.Fatalf("maskNotifyURL() = %q, parameter order changed", got)
	}
}

func TestRedactNotifyParamsMasksShortQueryParamNames(t *testing.T) {
	out := redactNotifyParams(2, `{"url":"https://oapi.example.com/robot/send?k=SUPERSECRETVALUE"}`)
	if strings.Contains(out, "SUPERSECRETVALUE") {
		t.Fatalf("redactNotifyParams leaked a short-named query secret:\n%s", out)
	}
}
