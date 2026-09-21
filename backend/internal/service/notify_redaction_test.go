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
