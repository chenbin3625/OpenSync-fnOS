package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseAuthCallbackQuery(t *testing.T) {
	q, err := ParseAuthCallbackQuery("http://localhost:53682/?code=abc123&state=st1&hostname=eapi.pcloud.com", "st1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q.Get("code") != "abc123" || q.Get("state") != "st1" || q.Get("hostname") != "eapi.pcloud.com" {
		t.Fatalf("unexpected parsed query: %v", q)
	}

	// Bare query without state gets expectedState injected.
	q2, err := ParseAuthCallbackQuery("?code=xyz", "expected-st")
	if err != nil {
		t.Fatalf("unexpected error for bare query: %v", err)
	}
	if q2.Get("code") != "xyz" || q2.Get("state") != "expected-st" {
		t.Fatalf("unexpected query2: %v", q2)
	}

	// Arbitrary external host in pasted URL is ignored (only query is extracted).
	q3, err := ParseAuthCallbackQuery("https://evil.example.invalid/steal?code=ok&state=st2", "st2")
	if err != nil || q3.Get("code") != "ok" {
		t.Fatalf("expected safe query extraction, got %v / %v", q3, err)
	}

	// State mismatch rejected.
	if _, err := ParseAuthCallbackQuery("http://localhost:53682/?code=abc&state=wrong", "right"); err == nil {
		t.Fatal("expected state mismatch error")
	}

	// Provider error surfaced.
	if _, err := ParseAuthCallbackQuery("http://localhost:53682/?error=access_denied&error_description=denied", "st"); err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("expected provider error, got %v", err)
	}

	// Missing code rejected.
	if _, err := ParseAuthCallbackQuery("http://localhost:53682/?state=st", "st"); err == nil {
		t.Fatal("expected missing code error")
	}

	// Control characters rejected.
	if _, err := ParseAuthCallbackQuery("http://localhost:53682/?code=abc\r\n&state=st", "st"); err == nil {
		t.Fatal("expected control char rejection")
	}
}

func TestExtendedCloudDriveConfigFieldsValidate(t *testing.T) {
	cfg := Config{
		"jotta": {
			"type":          "jottacloud",
			"token":         `{"access_token":"a","refresh_token":"r"}`,
			"tokenURL":      "https://id.jottacloud.com/auth/realms/jottacloud/protocol/openid-connect/token",
			"configVersion": "1",
			"device":        "Jotta",
			"mountpoint":    "Archive",
		},
		"od": {
			"type":           "onedrive",
			"token":          `{"access_token":"a","refresh_token":"r"}`,
			"region":         "cn",
			"tenant":         "contoso-tenant",
			"drive_id":       "b!abc",
			"drive_type":     "business",
			"root_folder_id": "root123",
		},
		"sf": {
			"type":       "seafile",
			"url":        "https://cloud.seafile.com",
			"user":       "alice@example.com",
			"2fa":        "true",
			"auth_token": "tok123",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected extended cloud drive config to validate, got: %v", err)
	}
	if got := canonicalRemoteType("google cloud storage"); got != "googlecloudstorage" {
		t.Fatalf("canonicalRemoteType(google cloud storage) = %q, want googlecloudstorage", got)
	}
}

func TestCompletedAuthSessionLookupAndConsume(t *testing.T) {
	defer ShutdownAuthSessions()
	sess := &authSession{
		id:           "test-completed-1",
		typ:          "onedrive",
		status:       "completed",
		finalParams:  map[string]string{"type": "onedrive", "token": `{"access_token":"t"}`, "drive_id": "d1"},
		obscuredKeys: map[string]bool{},
		expiresAt:    time.Now().Add(time.Minute),
	}
	globalAuthManager.register(sess)

	// Wrong type rejected.
	if _, err := LookupCompletedAuthSession("test-completed-1", "dropbox"); err == nil {
		t.Fatal("expected type mismatch error")
	}

	// Correct type returns cloned params without consuming.
	data, err := LookupCompletedAuthSession("test-completed-1", "onedrive")
	if err != nil {
		t.Fatalf("unexpected lookup error: %v", err)
	}
	if data.Params["drive_id"] != "d1" || data.Params["token"] != `{"access_token":"t"}` {
		t.Fatalf("unexpected params: %v", data.Params)
	}
	// Mutating returned map must not affect stored session.
	data.Params["drive_id"] = "mutated"
	data2, err := LookupCompletedAuthSession("test-completed-1", "onedrive")
	if err != nil || data2.Params["drive_id"] != "d1" {
		t.Fatalf("expected second lookup to return original value, got %v / %v", data2, err)
	}

	ConsumeAuthSession("test-completed-1")
	if _, err := LookupCompletedAuthSession("test-completed-1", "onedrive"); err == nil {
		t.Fatal("expected consumed session lookup to fail")
	}
}

func TestStartAuthSessionRealRcloneFlows(t *testing.T) {
	bin, err := filepath.Abs(filepath.Join("..", "..", "..", ".tools", "rclone", "bin", "rclone"))
	if err != nil {
		t.Skip("cannot resolve rclone path")
	}
	if _, statErr := os.Stat(bin); statErr != nil {
		t.Skip("bundled rclone binary not present")
	}
	t.Setenv("OPENSYNC_RCLONE_BIN", bin)
	defer ShutdownAuthSessions()

	ctx := context.Background()

	// 1. Non-interactive backend rejected.
	if _, err := StartAuthSession(ctx, "s3", nil, nil, ""); err == nil {
		t.Fatal("expected s3 to reject interactive auth")
	}

	// 2. iCloud requires apple_id and password.
	if _, err := StartAuthSession(ctx, "iclouddrive", map[string]string{"apple_id": "a@b.c"}, nil, ""); err == nil {
		t.Fatal("expected iclouddrive missing password error")
	}

	// 3. OneDrive with region=cn returns China OAuth URL and keeps session alive on state mismatch.
	odSnap, err := StartAuthSession(ctx, "onedrive", map[string]string{"region": "cn"}, nil, "")
	if err != nil {
		t.Fatalf("StartAuthSession(onedrive) failed: %v", err)
	}
	if odSnap.Status != "oauth" || !strings.HasPrefix(odSnap.AuthURL, "https://login.chinacloudapi.cn/") {
		t.Fatalf("unexpected onedrive oauth snapshot: %+v", odSnap)
	}
	if odSnap.RedirectURI != "http://localhost:53682/" {
		t.Fatalf("unexpected onedrive redirectUri: %q", odSnap.RedirectURI)
	}
	if _, err := SubmitAuthCallback(ctx, odSnap.SessionID, "http://localhost:53682/?code=abc&state=wrong-state"); err == nil {
		t.Fatal("expected mismatched state to fail")
	}
	// Session must remain active after bad callback paste.
	if statusSnap, err := GetAuthSessionStatus(odSnap.SessionID); err != nil || statusSnap.Status != "oauth" {
		t.Fatalf("expected session to remain oauth after mismatched state, got %+v / %v", statusSnap, err)
	}
	CancelAuthSession(odSnap.SessionID)

	// 4. Google Drive preserves custom scope=drive.readonly and goes straight to Google OAuth.
	driveSnap, err := StartAuthSession(ctx, "drive", map[string]string{"scope": "drive.readonly"}, nil, "")
	if err != nil {
		t.Fatalf("StartAuthSession(drive) failed: %v", err)
	}
	if driveSnap.Status != "oauth" || !strings.HasPrefix(driveSnap.AuthURL, "https://accounts.google.com/") || !strings.Contains(driveSnap.AuthURL, "drive.readonly") {
		t.Fatalf("unexpected drive oauth snapshot: %+v", driveSnap)
	}
	CancelAuthSession(driveSnap.SessionID)

	// 4b. HiDrive preserves scope_access=ro in OAuth URL.
	hiSnap, err := StartAuthSession(ctx, "hidrive", map[string]string{"scope_access": "ro"}, nil, "")
	if err != nil {
		t.Fatalf("StartAuthSession(hidrive) failed: %v", err)
	}
	if hiSnap.Status != "oauth" || !strings.HasPrefix(hiSnap.AuthURL, "https://my.hidrive.com/") || !strings.Contains(hiSnap.AuthURL, "scope=ro") {
		t.Fatalf("unexpected hidrive oauth snapshot: %+v", hiSnap)
	}
	CancelAuthSession(hiSnap.SessionID)

	// 5. Jottacloud uses non-interactive personal login token flow (not broken rclone authorize).
	jottaSnap, err := StartAuthSession(ctx, "jottacloud", nil, nil, "")
	if err != nil {
		t.Fatalf("StartAuthSession(jottacloud) failed: %v", err)
	}
	if jottaSnap.Status != "question" || jottaSnap.Question == nil || jottaSnap.Question.Name != "config_type" {
		t.Fatalf("unexpected jottacloud step1: %+v", jottaSnap)
	}
	jottaStep2, err := ContinueAuthSession(ctx, jottaSnap.SessionID, jottaSnap.Question.State, "standard")
	if err != nil {
		t.Fatalf("ContinueAuthSession(jottacloud standard) failed: %v", err)
	}
	if jottaStep2.Status != "question" || jottaStep2.Question == nil || jottaStep2.Question.Name != "config_login_token" {
		t.Fatalf("unexpected jottacloud step2: %+v", jottaStep2)
	}
	CancelAuthSession(jottaSnap.SessionID)

	// 6. Seafile 2FA enters 2fa_do question with obscured password written to temp conf.
	sfSnap, err := StartAuthSession(ctx, "seafile", map[string]string{
		"url":  "https://cloud.seafile.com",
		"user": "alice@example.com",
		"pass": "super-secret-pass",
	}, nil, "")
	if err != nil {
		t.Fatalf("StartAuthSession(seafile) failed: %v", err)
	}
	if sfSnap.Status != "question" || sfSnap.Question == nil || sfSnap.Question.Name != "config_2fa" {
		t.Fatalf("unexpected seafile 2fa step: %+v", sfSnap)
	}
	CancelAuthSession(sfSnap.SessionID)
}
