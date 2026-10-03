package main

import (
	"context"
	"encoding/json"
	"fmt"
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

// "检查更新" 按钮要求真实的检查结果，所以手动刷新必须绕过 30 分钟的缓存；
// 缓存本身不能变短到每次页面加载都打 GitHub（未认证 60 次/小时）。
func TestVersionCheckerSuccessCacheLivesThirtyMinutes(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"tag_name":"v0.0.27"}`))
	}))
	defer upstream.Close()
	checker := newVersionChecker(upstream.URL, upstream.Client())
	if _, err := checker.check(context.Background(), "0.0.26"); err != nil {
		t.Fatalf("check: %v", err)
	}
	if lifetime := time.Until(checker.expiresAt); lifetime < successTTL-time.Minute || lifetime > successTTL {
		t.Fatalf("success cache lifetime = %v, want %v", lifetime, successTTL)
	}
	if _, err := checker.check(context.Background(), "0.0.26"); err != nil {
		t.Fatalf("cached check: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls inside the cache window = %d, want 1", got)
	}
	checker.expiresAt = time.Now().Add(-time.Second)
	if _, err := checker.check(context.Background(), "0.0.26"); err != nil {
		t.Fatalf("expired check: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls after expiry = %d, want 2", got)
	}
}

func TestVersionCheckerForcedRefreshBypassesCache(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fmt.Sprintf(`{"tag_name":"v0.0.%d"}`, 26+calls.Add(1))))
	}))
	defer upstream.Close()
	checker := newVersionChecker(upstream.URL, upstream.Client())
	ctx := context.Background()
	first, err := checker.check(ctx, "0.0.26")
	if err != nil || first.LatestVersion != "v0.0.27" {
		t.Fatalf("first check = %+v, err = %v", first, err)
	}
	// 缓存仍新鲜，但用户点了检查更新：必须重新询问 GitHub。
	second, err := checker.checkWithRefresh(ctx, "0.0.26", true)
	if err != nil || second.LatestVersion != "v0.0.28" || !second.HasUpdate {
		t.Fatalf("forced check = %+v, err = %v", second, err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls = %d, want 2", got)
	}
}

func TestVersionCheckerForcedRefreshIsThrottled(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fmt.Sprintf(`{"tag_name":"v0.0.%d"}`, 26+calls.Add(1))))
	}))
	defer upstream.Close()
	checker := newVersionChecker(upstream.URL, upstream.Client())
	ctx := context.Background()
	if _, err := checker.checkWithRefresh(ctx, "0.0.26", true); err != nil {
		t.Fatalf("forced check: %v", err)
	}
	// 双击按钮不该产生第二次上游请求，也不该报错。
	result, err := checker.checkWithRefresh(ctx, "0.0.26", true)
	if err != nil || result.LatestVersion != "v0.0.27" {
		t.Fatalf("throttled check = %+v, err = %v", result, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls inside the throttle window = %d, want 1", got)
	}
	checker.lastForcedRefresh = time.Now().Add(-forcedRefreshInterval - time.Second)
	result, err = checker.checkWithRefresh(ctx, "0.0.26", true)
	if err != nil || result.LatestVersion != "v0.0.28" {
		t.Fatalf("check after the throttle window = %+v, err = %v", result, err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls after the throttle window = %d, want 2", got)
	}
}

// 手动检查失败只报给点按钮的人，不能把仍然有效的成功缓存替换成失败，
// 否则所有人的自动检查都会在 failureTTL 内丢失升级角标。
func TestVersionCheckerFailedForcedRefreshKeepsCachedSuccess(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 2 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"v0.0.27"}`))
	}))
	defer upstream.Close()
	checker := newVersionChecker(upstream.URL, upstream.Client())
	ctx := context.Background()
	if _, err := checker.check(ctx, "0.0.26"); err != nil {
		t.Fatalf("first check: %v", err)
	}
	expiresAt := checker.expiresAt
	if _, err := checker.checkWithRefresh(ctx, "0.0.26", true); err == nil {
		t.Fatal("failed forced check should report its error")
	}
	result, err := checker.check(ctx, "0.0.26")
	if err != nil || result.LatestVersion != "v0.0.27" || !result.HasUpdate {
		t.Fatalf("cached result after failed forced check = %+v, err = %v", result, err)
	}
	if !checker.expiresAt.Equal(expiresAt) {
		t.Fatalf("expiresAt changed from %v to %v", expiresAt, checker.expiresAt)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls = %d, want 2", got)
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

// 前端「检查更新」按钮带 ?refresh=1：handler 必须把它透传成一次真实的检查，
// 而不是继续返回缓存里的旧版本号。
func TestVersionHandlerRefreshQueryForcesCheck(t *testing.T) {
	// 已安装版本由构建注入，这里固定成 v0.0.27 才能断言 hasUpdate 的变化。
	originalVersion := appVersion
	appVersion = "0.0.27"
	defer func() { appVersion = originalVersion }()
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fmt.Sprintf(`{"tag_name":"v0.0.%d"}`, 26+calls.Add(1))))
	}))
	defer upstream.Close()
	checker := newVersionChecker(upstream.URL, upstream.Client())
	router := newRouterWithVersionChecker(true, nil, checker)
	get := func(target string) versionResult {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, body = %s", target, recorder.Code, recorder.Body.String())
		}
		var response struct {
			Data versionResult `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Data
	}
	path := "/app/opensync/svr/version/latest"
	if first := get(path); first.LatestVersion != "v0.0.27" || first.HasUpdate {
		t.Fatalf("first = %+v", first)
	}
	// 缓存新鲜时普通请求仍然命中缓存。
	if cached := get(path); cached.LatestVersion != "v0.0.27" {
		t.Fatalf("cached = %+v", cached)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d, want 1", got)
	}
	// 即使普通缓存刚写入，首次手动检查也必须立即绕过它。
	if refreshed := get(path + "?refresh=1"); refreshed.LatestVersion != "v0.0.28" || !refreshed.HasUpdate {
		t.Fatalf("refreshed = %+v", refreshed)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("upstream calls after refresh = %d, want 2", got)
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
