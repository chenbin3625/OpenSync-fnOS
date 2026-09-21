package service

import "testing"

// Lark is checked with ("code", "StatusCode") and can answer code=0 next to a
// non-zero StatusCode. Every named field has to be inspected, or that failure
// is reported as a success.
func TestNotifyProviderErrorChecksEveryField(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		fields  []string
		wantErr bool
	}{
		{"all zero is success", `{"code":0,"StatusCode":0}`, []string{"code", "StatusCode"}, false},
		{"second field non-zero fails", `{"code":0,"StatusCode":9499}`, []string{"code", "StatusCode"}, true},
		{"first field non-zero fails", `{"code":19001,"StatusCode":0}`, []string{"code", "StatusCode"}, true},
		{"only later field present and failing", `{"StatusCode":500}`, []string{"code", "StatusCode"}, true},
		{"no recognized field", `{"ok":true}`, []string{"code", "errcode"}, false},
		{"non-JSON body is not an error", `plain webhook accepted`, []string{"code"}, false},
		{"dingtalk errcode", `{"errcode":300001,"errmsg":"token invalid"}`, []string{"errcode"}, true},
		{"string coded failure", `{"code":"1"}`, []string{"code"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := notifyProviderError([]byte(tc.body), tc.fields...)
			if tc.wantErr && err == nil {
				t.Fatalf("notifyProviderError(%s) = nil, want error", tc.body)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("notifyProviderError(%s) = %v, want nil", tc.body, err)
			}
		})
	}
}
