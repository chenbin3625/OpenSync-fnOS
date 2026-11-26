package native

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSplitBwRateAndDirectionalParsing(t *testing.T) {
	if got := splitBwRate("off", 3); got != "off" {
		t.Fatalf("splitBwRate(off, 3) = %q, want off", got)
	}
	if got := splitBwRate("5.5M:off", 1); got != "5.5M:off" {
		t.Fatalf("splitBwRate(5.5M:off, 1) = %q, want 5.5M:off", got)
	}
	if got := splitBwRate("5.5M:off", 2); got != "2883584B:off" {
		t.Fatalf("splitBwRate(5.5M:off, 2) = %q, want 2883584B:off", got)
	}
	if got := splitBwRate("10M", 4); got != "2621440B" {
		t.Fatalf("splitBwRate(10M, 4) = %q, want 2621440B", got)
	}
	if err := ValidateBwLimit("2883584B:off"); err != nil {
		t.Fatalf("ValidateBwLimit(2883584B:off) failed: %v", err)
	}

	up, down := parseDirectionalRateBytes("5.5M:off")
	if up != int64(5.5*1024*1024) || down != 0 {
		t.Fatalf("parseDirectionalRateBytes(5.5M:off) = (%d, %d), want (%d, 0)", up, down, int64(5.5*1024*1024))
	}
	up, down = parseDirectionalRateBytes("off:10M")
	if up != 0 || down != 10*1024*1024 {
		t.Fatalf("parseDirectionalRateBytes(off:10M) = (%d, %d), want (0, %d)", up, down, 10*1024*1024)
	}
}

func TestGlobalBwLimitDynamicRebalanceAcrossRcloneAndGo(t *testing.T) {
	ResetGlobalBwLimitForTest()
	defer ResetGlobalBwLimitForTest()

	makeMockRC := func() (*httptest.Server, func() []string) {
		var mu sync.Mutex
		var rates []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/core/bwlimit" {
				http.NotFound(w, r)
				return
			}
			var body struct {
				Rate string `json:"rate"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			rates = append(rates, body.Rate)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"rate":"` + body.Rate + `"}`))
		}))
		return srv, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), rates...)
		}
	}

	waitLastRate := func(label string, getter func() []string, want string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			got := getter()
			if len(got) > 0 && got[len(got)-1] == want {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("%s: rates=%v, want last=%q", label, getter(), want)
	}

	srv1, rates1 := makeMockRC()
	defer srv1.Close()
	srv2, rates2 := makeMockRC()
	defer srv2.Close()

	if err := SetGlobalBwLimit("10M:off"); err != nil {
		t.Fatalf("SetGlobalBwLimit failed: %v", err)
	}

	c1 := NewGlobalRCController()
	defer c1.Close()
	c2 := NewGlobalRCController()
	defer c2.Close()

	// Estimate before attaching 1st -> full 10M:off.
	if init1 := c1.EffectiveInitialRcloneBwLimit(""); init1 != "10M:off" {
		t.Fatalf("initial rate for 1st stream = %q, want 10M:off", init1)
	}
	detach1 := c1.Attach(0, strings.TrimPrefix(srv1.URL, "http://"), "", "")
	waitLastRate("c1 single", rates1, "10M:off")

	// Estimate before attaching 2nd -> half = 5242880B:off.
	if init2 := c2.EffectiveInitialRcloneBwLimit(""); init2 != "5242880B:off" {
		t.Fatalf("initial rate for 2nd stream = %q, want 5242880B:off", init2)
	}
	detach2 := c2.Attach(0, strings.TrimPrefix(srv2.URL, "http://"), "", "")
	waitLastRate("c1 split-2", rates1, "5242880B:off")
	waitLastRate("c2 split-2", rates2, "5242880B:off")

	// Enter a Go transfer (e.g. Quark upload) on a 3rd controller -> 3-way split!
	c3 := NewGlobalRCController()
	defer c3.Close()
	leaveGo := c3.EnterGoTransfer()
	want3 := splitBwRate("10M:off", 3)
	waitLastRate("c1 split-3", rates1, want3)
	waitLastRate("c2 split-3", rates2, want3)
	if goUp := defaultGlobalBw.effectiveGoBytesPerSec(time.Now(), throttleUpload); goUp != (10*1024*1024)/3 {
		t.Fatalf("go upload budget = %d, want %d", goUp, (10*1024*1024)/3)
	}
	if goDown := defaultGlobalBw.effectiveGoBytesPerSec(time.Now(), throttleDownload); goDown != 0 {
		t.Fatalf("go download budget for 10M:off = %d, want 0 (unlimited)", goDown)
	}

	// Live global rate update -> immediately pushes to all active streams.
	if err := SetGlobalBwLimit("6M:off"); err != nil {
		t.Fatalf("SetGlobalBwLimit(6M:off) failed: %v", err)
	}
	want6Split3 := splitBwRate("6M:off", 3)
	waitLastRate("c1 live-6M-split-3", rates1, want6Split3)
	waitLastRate("c2 live-6M-split-3", rates2, want6Split3)

	// Leaving Go transfer -> back to 2-way split (3M each).
	leaveGo()
	want6Split2 := splitBwRate("6M:off", 2)
	waitLastRate("c1 split-2 after go exit", rates1, want6Split2)
	waitLastRate("c2 split-2 after go exit", rates2, want6Split2)

	// Manual live override on c2 detaches it from global pool -> c1 gets full 6M:off!
	if err := c2.SetBwLimit(context.Background(), "20M"); err != nil {
		t.Fatalf("c2.SetBwLimit failed: %v", err)
	}
	waitLastRate("c2 manual 20M", rates2, "20M")
	waitLastRate("c1 reclaimed full 6M:off", rates1, "6M:off")

	detach2()
	detach1()
	r, g := GlobalActiveStreamCount()
	if r != 0 || g != 0 {
		t.Fatalf("expected 0 active streams after detach, got rclone=%d go=%d", r, g)
	}
}

func TestResolveExternalAuthURLAddsGooglePromptConsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://accounts.google.com/o/oauth2/auth?access_type=offline&client_id=abc&redirect_uri=http%3A%2F%2F127.0.0.1%3A53682%2F&response_type=code&scope=drive&state=xyz")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	authURL, redirectURI, err := resolveExternalAuthURL(srv.URL)
	if err != nil {
		t.Fatalf("resolveExternalAuthURL failed: %v", err)
	}
	if redirectURI != "http://127.0.0.1:53682/" {
		t.Fatalf("redirectURI = %q", redirectURI)
	}
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("url.Parse failed: %v", err)
	}
	if got := u.Query().Get("prompt"); got != "consent" {
		t.Fatalf("Google OAuth URL prompt = %q, want consent (url=%s)", got, authURL)
	}
}
