package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestDiscoverOpenListProbesLoopbackAndBlocksSSRF(t *testing.T) {
	davMux := http.NewServeMux()
	davMux.HandleFunc("/dav/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PROPFIND" {
			w.Header().Set("Server", "OpenList/v4.0")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	srv := httptest.NewServer(davMux)
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("url.Parse(%s): %v", srv.URL, err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("Atoi(%s): %v", u.Port(), err)
	}

	res, err := DiscoverOpenList(context.Background(), NativeOpenListDiscoverInput{
		Ports: []int{port},
	})
	if err != nil {
		t.Fatalf("DiscoverOpenList() error: %v", err)
	}
	if !res.Found || res.SuggestedConfig == nil {
		t.Fatalf("expected OpenList endpoint to be discovered, got %+v", res)
	}
	if res.SuggestedConfig.Vendor != "other" || res.SuggestedConfig.Type != "webdav" {
		t.Fatalf("unexpected suggested config: %+v", res.SuggestedConfig)
	}
	if res.SuggestedConfig.CompareMode != 1 {
		t.Fatalf("compareMode = %d, want 1 (size-only)", res.SuggestedConfig.CompareMode)
	}
	if !strings.Contains(res.SuggestedConfig.URL, "/dav") {
		t.Fatalf("expected suggested URL to target /dav, got %q", res.SuggestedConfig.URL)
	}
	if len(res.Candidates) != 1 || !res.Candidates[0].Reachable || !res.Candidates[0].AuthRequired {
		t.Fatalf("unexpected candidates: %+v", res.Candidates)
	}

	// Public API fallback detection when /dav returns 404.
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/public/settings" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 200,
				"data": map[string]any{"version": "v3.40.0"},
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiSrv.Close()
	apiURL, _ := url.Parse(apiSrv.URL)
	apiPort, _ := strconv.Atoi(apiURL.Port())
	apiRes, err := DiscoverOpenList(context.Background(), NativeOpenListDiscoverInput{Ports: []int{apiPort}})
	if err != nil || !apiRes.Found {
		t.Fatalf("expected /api/public/settings fallback to detect OpenList/AList, got res=%+v err=%v", apiRes, err)
	}

	// SSRF defense: non-loopback targets must be rejected immediately.
	for _, bad := range []string{
		"http://192.168.1.50:5244/dav",
		"http://169.254.169.254/latest/meta-data/",
		"http://example.com:5244/dav",
		"ftp://127.0.0.1:21/dav",
		"http://user:pass@127.0.0.1:5244/dav",
	} {
		if _, err := DiscoverOpenList(context.Background(), NativeOpenListDiscoverInput{
			Endpoints: []string{bad},
		}); err == nil {
			t.Fatalf("expected SSRF target %q to be rejected", bad)
		}
	}
}

func TestBuildCryptRecoveryBundleAndPasswordVerification(t *testing.T) {
	defer setupNativeTestDB(t)()
	ctx := context.Background()

	if _, err := EnsureNativeEngine(); err != nil {
		t.Fatalf("EnsureNativeEngine() error: %v", err)
	}
	if err := SaveNativeStorage(ctx, NativeStorageInput{
		Name: "oss",
		Options: map[string]string{
			"type":              "s3",
			"provider":          "Alibaba",
			"endpoint":          "oss-cn-hangzhou.aliyuncs.com",
			"access_key_id":     "ak",
			"secret_access_key": "sk",
		},
	}); err != nil {
		t.Fatalf("SaveNativeStorage(oss) error: %v", err)
	}
	if err := SaveNativeStorage(ctx, NativeStorageInput{
		Name: "job_crypt",
		Options: map[string]string{
			"type":                      "crypt",
			"remote":                    "oss:bucket/encrypted",
			"password":                  "testsecret",
			"password2":                 "saltsalt",
			"filename_encryption":       "standard",
			"filename_encoding":         "base32",
			"directory_name_encryption": "true",
			"managed_by_job":            "true",
			"managed_job_label":         "照片加密同步",
		},
	}); err != nil {
		t.Fatalf("SaveNativeStorage(job_crypt) error: %v", err)
	}

	state, err := GetNativeStorageState(ctx)
	if err != nil {
		t.Fatalf("GetNativeStorageState() error: %v", err)
	}
	var cryptItem *NativeRemoteItem
	for i := range state.Remotes {
		if state.Remotes[i].Name == "job_crypt" {
			cryptItem = &state.Remotes[i]
			break
		}
	}
	if cryptItem == nil || cryptItem.Options["managed_by_job"] != "true" || cryptItem.Options["managed_job_label"] != "照片加密同步" {
		t.Fatalf("expected GetNativeStorageState to expose managed_by_job metadata, got %+v", cryptItem)
	}

	// Export INI must strip managed_by_job & managed_job_label.
	exported, err := ExportNativeConfig(ctx, NativeExportInput{Names: []string{"job_crypt"}})
	if err != nil {
		t.Fatalf("ExportNativeConfig() error: %v", err)
	}
	if strings.Contains(exported.Content, "managed_by_job") || strings.Contains(exported.Content, "managed_job_label") {
		t.Fatalf("exported rclone.conf leaked OpenSync metadata:\n%s", exported.Content)
	}

	// Non-crypt remote rejected cleanly.
	if _, err := BuildCryptRecoveryBundle(ctx, NativeCryptRecoveryInput{Name: "oss"}); err == nil {
		t.Fatal("expected non-crypt remote recovery request to fail")
	}

	// Matching password & salt verification succeeds without leaking secret.
	okRes, err := BuildCryptRecoveryBundle(ctx, NativeCryptRecoveryInput{
		Name:            "job_crypt",
		VerifyPassword:  "testsecret",
		VerifyPassword2: "saltsalt",
	})
	if err != nil {
		t.Fatalf("BuildCryptRecoveryBundle() error: %v", err)
	}
	if !okRes.PasswordChecked || okRes.PasswordVerified == nil || !*okRes.PasswordVerified {
		t.Fatalf("expected VerifyPassword=testsecret to succeed, got %+v", okRes)
	}
	if !okRes.Password2Checked || okRes.Password2Verified == nil || !*okRes.Password2Verified {
		t.Fatalf("expected VerifyPassword2=saltsalt to succeed, got %+v", okRes)
	}
	if strings.Contains(okRes.RecoveryText, "testsecret") || strings.Contains(okRes.RecoveryText, "saltsalt") {
		t.Fatal("recovery output must never echo plaintext secrets")
	}
	if !okRes.Bundle.HasPassword || !okRes.Bundle.HasPassword2 || okRes.Bundle.UnderlyingRemote != "oss" || okRes.Bundle.Remote != "oss:bucket/encrypted" || !okRes.Bundle.ManagedByJob || okRes.Bundle.ManagedJobLabel != "照片加密同步" {
		t.Fatalf("unexpected bundle metadata: %+v", okRes.Bundle)
	}

	// Wrong password returns false rather than error.
	badRes, err := BuildCryptRecoveryBundle(ctx, NativeCryptRecoveryInput{
		Name:           "job_crypt",
		VerifyPassword: "wrongpassword",
	})
	if err != nil {
		t.Fatalf("BuildCryptRecoveryBundle(wrongpassword) error: %v", err)
	}
	if !badRes.PasswordChecked || badRes.PasswordVerified == nil || *badRes.PasswordVerified {
		t.Fatalf("expected wrongpassword verification to report false, got %+v", badRes)
	}
}
