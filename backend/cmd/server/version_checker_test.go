package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestNewerRelease(t *testing.T) {
	for _, tc := range []struct {
		local, latest string
		want          bool
	}{
		{"0.0.26", "v0.0.27", true},
		{"0.0.26", "v0.1.0", true},
		{"0.0.26", "v0.0.26", false},
		{"0.0.26", "v0.0.25", false},
		{"dev", "v0.0.27", false},
		{"0.0.26", "v0.0.27-beta.1", false},
		{"0.0.26", "unexpected", false},
	} {
		t.Run(tc.local+"-"+tc.latest, func(t *testing.T) {
			if got := newerRelease(tc.local, tc.latest); got != tc.want {
				t.Fatalf("newerRelease(%q, %q) = %v, want %v", tc.local, tc.latest, got, tc.want)
			}
		})
	}
}

func TestVersionCheckerCachesConcurrentRequests(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/repos/chenbin3625/OpenSync-fnOS/releases/latest" {
			t.Errorf("upstream path = %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v0.0.27","html_url":"https://github.com/chenbin3625/OpenSync-fnOS/releases/tag/v0.0.27"}`))
	}))
	defer upstream.Close()
	checker := newVersionChecker(upstream.URL+"/repos/chenbin3625/OpenSync-fnOS/releases/latest", upstream.Client())
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := checker.check(context.Background(), "0.0.26")
			if err != nil {
				t.Errorf("check: %v", err)
			} else if result.LatestVersion != "v0.0.27" || !result.HasUpdate ||
				result.ReleaseURL != "https://github.com/chenbin3625/OpenSync-fnOS/releases/latest" {
				t.Errorf("result = %+v", result)
			}
		}()
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
}

func TestVersionCheckerFailureCanRetry(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"v0.0.27"}`))
	}))
	defer upstream.Close()
	checker := newVersionChecker(upstream.URL, upstream.Client())
	if _, err := checker.check(context.Background(), "0.0.26"); err == nil {
		t.Fatal("first check should fail")
	}
	if _, err := checker.check(context.Background(), "0.0.26"); err == nil {
		t.Fatal("cached failure should still fail")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("failure calls = %d, want 1", got)
	}
	checker.expiresAt = time.Now().Add(-time.Second)
	result, err := checker.check(context.Background(), "0.0.26")
	if err != nil || !result.HasUpdate || calls.Load() != 2 {
		t.Fatalf("retry result = %+v, err = %v, calls = %d", result, err, calls.Load())
	}
}

func TestVersionCheckerFallsBackToReleaseRedirectOnRateLimit(t *testing.T) {
	var apiCalls, pageCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/latest":
			apiCalls.Add(1)
			http.Error(w, "rate limit exceeded", http.StatusForbidden)
		case "/releases/latest":
			pageCalls.Add(1)
			if r.Method != http.MethodHead {
				t.Errorf("fallback method = %s, want HEAD", r.Method)
			}
			w.Header().Set("Location", "https://github.com/chenbin3625/OpenSync-fnOS/releases/tag/v0.0.27")
			w.WriteHeader(http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	client := upstream.Client()
	transport := client.Transport
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "github.com" {
			req := r.Clone(r.Context())
			req.URL, _ = url.Parse(upstream.URL + "/releases/latest")
			req.Host = req.URL.Host
			return transport.RoundTrip(req)
		}
		return transport.RoundTrip(r)
	})
	checker := newVersionChecker(upstream.URL+"/api/latest", client)
	for i := 0; i < 2; i++ {
		result, err := checker.check(context.Background(), "0.0.26")
		if err != nil || result.LatestVersion != "v0.0.27" || !result.HasUpdate {
			t.Fatalf("fallback result = %+v, err = %v", result, err)
		}
	}
	if apiCalls.Load() != 1 || pageCalls.Load() != 1 {
		t.Fatalf("upstream calls = API %d, redirect %d; want 1 each", apiCalls.Load(), pageCalls.Load())
	}
}

func TestVersionCheckerRequestCancellationDoesNotPoisonSharedCache(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"tag_name":"v0.0.27"}`))
	}))
	defer upstream.Close()
	checker := newVersionChecker(upstream.URL, upstream.Client())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = checker.check(ctx, "0.0.26")
	}()
	<-started
	cancel()
	close(release)
	<-done
	result, err := checker.check(context.Background(), "0.0.26")
	if err != nil || !result.HasUpdate {
		t.Fatalf("shared result after one client cancelled = %+v, %v", result, err)
	}
}

func TestVersionRouteRequiresGatewayIdentity(t *testing.T) {
	router := newRouter(false, nil)
	path := "/app/opensync/svr/version/latest"
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", recorder.Code)
	}
}

func TestVersionHandlerReturnsUpdateStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v0.0.27"}`))
	}))
	defer upstream.Close()
	checker := newVersionChecker(upstream.URL, upstream.Client())
	router := newRouterWithVersionChecker(true, nil, checker)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/app/opensync/svr/version/latest", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data versionResult `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.LatestVersion != "v0.0.27" {
		t.Fatalf("result = %+v", response.Data)
	}
}
