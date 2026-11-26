package quark

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// tokenBucket implements a lightweight, dependency-free token bucket limiter.
type tokenBucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newTokenBucket(rate float64) *tokenBucket {
	if rate <= 0 {
		return nil
	}
	burst := math.Ceil(rate)
	if burst < 2 {
		burst = 2
	}
	return &tokenBucket{
		rate:   rate,
		burst:  burst,
		tokens: burst,
		last:   time.Now(),
	}
}

func (tb *tokenBucket) Wait(ctx context.Context) error {
	if tb == nil || tb.rate <= 0 {
		return ctx.Err()
	}
	for {
		tb.mu.Lock()
		now := time.Now()
		elapsed := now.Sub(tb.last).Seconds()
		if elapsed > 0 {
			tb.tokens += elapsed * tb.rate
			if tb.tokens > tb.burst {
				tb.tokens = tb.burst
			}
			tb.last = now
		}
		if tb.tokens >= 1 {
			tb.tokens -= 1
			tb.mu.Unlock()
			return nil
		}
		waitSec := (1 - tb.tokens) / tb.rate
		tb.mu.Unlock()

		waitDur := time.Duration(waitSec * float64(time.Second))
		if waitDur < 5*time.Millisecond {
			waitDur = 5 * time.Millisecond
		}
		timer := time.NewTimer(waitDur)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Client manages authenticated HTTP requests, rate limiting, cookie/__puus
// refresh tracking, path-to-FID caching, and download URL caching for Quark/UC.
type Client struct {
	profile           ProviderProfile
	apiURL            string
	accountURL        string
	qrAuthURL         string
	userAgent         string
	rootFID           string
	uploadConcurrency int
	partSizeOverride  int64

	httpClient     *http.Client
	streamClient   *http.Client
	limiter        *tokenBucket
	onCookieUpdate func(string)

	cookieMu   sync.RWMutex
	cookieList []cookieKV
	cookieStr  string

	lastMetaReqUnix atomic.Int64

	cache      *dirCache
	mkdirMu    sync.Mutex
	dlCacheMu  sync.Mutex
	dlCache   map[string]cachedDownloadURL
	dlInFlight map[string]*dlFlight
	dlTTL     time.Duration
}

type cookieKV struct {
	Name  string
	Value string
}

type cachedDownloadURL struct {
	URL      string
	ExpireAt time.Time
}

type dlFlight struct {
	done chan struct{}
	url  string
	err  error
}

// NewClient constructs a Quark/UC protocol client from opts.
func NewClient(opts Options) (*Client, error) {
	profile := ProfileFor(opts.Provider)
	apiURL := strings.TrimRight(strings.TrimSpace(opts.APIURL), "/")
	if apiURL == "" {
		apiURL = profile.APIURL
	}
	accountURL := strings.TrimRight(strings.TrimSpace(opts.AccountURL), "/")
	if accountURL == "" {
		accountURL = profile.AccountURL
	}
	qrAuthURL := strings.TrimRight(strings.TrimSpace(opts.QRAuthURL), "/")
	if qrAuthURL == "" {
		qrAuthURL = profile.QRAuthURL
	}
	ua := strings.TrimSpace(opts.UserAgent)
	if ua == "" {
		ua = profile.DefaultUA
	}
	rootFID := strings.TrimSpace(opts.RootFolderID)
	if rootFID == "" {
		rootFID = DefaultRootFolderID
	}
	tps := opts.TPSLimit
	if tps <= 0 {
		tps = DefaultTPSLimit
	}
	conc := opts.UploadConcurrency
	if conc <= 0 {
		conc = DefaultUploadConcurrency
	}
	if conc > 8 {
		conc = 8
	}
	cacheTTL := opts.CacheTTL
	if cacheTTL <= 0 {
		cacheTTL = DefaultCacheTTL
	}
	dlTTL := opts.DownloadURLTTL
	if dlTTL <= 0 {
		dlTTL = DefaultDownloadURLTTL
	}

	tr, err := newHTTPTransport(opts.ProxyURL, opts.InsecureSkipVerify)
	if err != nil {
		return nil, err
	}
	streamTr, err := newHTTPTransport(opts.ProxyURL, opts.InsecureSkipVerify)
	if err != nil {
		return nil, err
	}

	c := &Client{
		profile:           profile,
		apiURL:            apiURL,
		accountURL:        accountURL,
		qrAuthURL:         qrAuthURL,
		userAgent:         ua,
		rootFID:           rootFID,
		uploadConcurrency: conc,
		partSizeOverride:  opts.PartSize,
		httpClient: &http.Client{
			Transport: tr,
			Timeout:   45 * time.Second,
		},
		streamClient: &http.Client{
			Transport: streamTr,
		},
		limiter:        newTokenBucket(tps),
		onCookieUpdate: opts.OnCookieUpdate,
		cache:          newDirCache(rootFID, cacheTTL),
		dlCache:        make(map[string]cachedDownloadURL),
		dlInFlight:     make(map[string]*dlFlight),
		dlTTL:          dlTTL,
	}
	c.SetCookie(opts.Cookie)
	return c, nil
}

func newHTTPTransport(proxyURL string, insecureSkipVerify bool) (*http.Transport, error) {
	tr := &http.Transport{
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: insecureSkipVerify, //nolint:gosec // user-explicit per-remote option
		},
	}
	if trimmed := strings.TrimSpace(proxyURL); trimmed != "" {
		pu, err := url.Parse(trimmed)
		if err != nil {
			return nil, fmt.Errorf("代理地址无效: %w", err)
		}
		tr.Proxy = http.ProxyURL(pu)
	}
	return tr, nil
}

// Close releases idle HTTP connections.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
	if c.streamClient != nil {
		c.streamClient.CloseIdleConnections()
	}
	return nil
}

// RootFID returns the configured root folder ID (defaults to "0").
func (c *Client) RootFID() string {
	return c.rootFID
}

// UploadConcurrency returns the configured multipart upload concurrency (1..8).
func (c *Client) UploadConcurrency() int {
	return c.uploadConcurrency
}

// Cookie returns the current serialized Cookie header value.
func (c *Client) Cookie() string {
	c.cookieMu.RLock()
	defer c.cookieMu.RUnlock()
	return c.cookieStr
}

// SetCookie parses and replaces the client's active Cookie header value.
func (c *Client) SetCookie(raw string) {
	pairs := parseCookieString(raw)
	serialized := formatCookiePairs(pairs)
	c.cookieMu.Lock()
	c.cookieList = pairs
	c.cookieStr = serialized
	c.cookieMu.Unlock()
}

// MergeCookies merges incoming cookies (e.g. from Set-Cookie response headers)
// into the active Cookie string and invokes OnCookieUpdate if anything changed.
func (c *Client) MergeCookies(incoming []*http.Cookie) bool {
	if len(incoming) == 0 {
		return false
	}
	c.cookieMu.Lock()
	changed := false
	for _, ck := range incoming {
		if ck == nil {
			continue
		}
		name := strings.TrimSpace(ck.Name)
		val := strings.TrimSpace(ck.Value)
		if name == "" || val == "" || strings.EqualFold(val, "deleted") {
			continue
		}
		found := false
		for i := range c.cookieList {
			if c.cookieList[i].Name == name {
				found = true
				if c.cookieList[i].Value != val {
					c.cookieList[i].Value = val
					changed = true
				}
				break
			}
		}
		if !found {
			c.cookieList = append(c.cookieList, cookieKV{Name: name, Value: val})
			changed = true
		}
	}
	var updated string
	if changed {
		updated = formatCookiePairs(c.cookieList)
		c.cookieStr = updated
	}
	cb := c.onCookieUpdate
	c.cookieMu.Unlock()

	if changed && cb != nil && updated != "" {
		cb(updated)
	}
	return changed
}

func parseCookieString(raw string) []cookieKV {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// Accept optional leading "Cookie:" prefix copied from browser DevTools.
	if len(raw) > 7 && strings.EqualFold(raw[:7], "cookie:") {
		raw = strings.TrimSpace(raw[7:])
	}
	parts := strings.Split(raw, ";")
	out := make([]cookieKV, 0, len(parts))
	indexByName := make(map[string]int, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if !ok || k == "" {
			continue
		}
		if idx, exists := indexByName[k]; exists {
			out[idx].Value = v
		} else {
			indexByName[k] = len(out)
			out = append(out, cookieKV{Name: k, Value: v})
		}
	}
	return out
}

func formatCookiePairs(pairs []cookieKV) string {
	if len(pairs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(pairs))
	for _, kv := range pairs {
		if kv.Name == "" {
			continue
		}
		parts = append(parts, kv.Name+"="+kv.Value)
	}
	return strings.Join(parts, "; ")
}

// ValidateCookieFormat checks that raw looks like a valid Quark/UC cookie string.
func ValidateCookieFormat(raw string) bool {
	pairs := parseCookieString(raw)
	if len(pairs) == 0 {
		return false
	}
	hasCore := false
	for _, kv := range pairs {
		switch kv.Name {
		case "__puus", "__pus", "b-user-id", "__kp", "__kps", "__ktd", "__uid":
			if kv.Value != "" {
				hasCore = true
			}
		}
	}
	return hasCore || len(pairs) >= 2
}

// MaybeKeepalive sends a lightweight /member request when KeepaliveInterval has
// elapsed since the last metadata call so __puus rotates cleanly during long uploads.
func (c *Client) MaybeKeepalive(ctx context.Context) {
	last := c.lastMetaReqUnix.Load()
	now := time.Now().Unix()
	if last > 0 && time.Duration(now-last)*time.Second < KeepaliveInterval {
		return
	}
	if !c.lastMetaReqUnix.CompareAndSwap(last, now) {
		return
	}
	kaCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, _ = c.About(kaCtx)
}

func (c *Client) applyCommonHeaders(req *http.Request) {
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Referer", c.profile.Referer)
	req.Header.Set("Origin", c.profile.Origin)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if ck := c.Cookie(); ck != "" {
		req.Header.Set("Cookie", ck)
	}
}

func (c *Client) buildAPIURL(endpoint string, query url.Values) string {
	q := url.Values{}
	q.Set("pr", c.profile.PR)
	q.Set("fr", "pc")
	q.Set("uc_param_str", "")
	for k, vals := range query {
		q[k] = vals
	}
	if !strings.HasPrefix(endpoint, "/") {
		endpoint = "/" + endpoint
	}
	return c.apiURL + endpoint + "?" + q.Encode()
}

// doJSON performs a rate-limited, retry-aware JSON API call against Quark/UC,
// capturing any refreshed Set-Cookie headers (especially __puus).
func (c *Client) doJSON(ctx context.Context, method, endpoint string, query url.Values, reqBody any, out any) error {
	var bodyBytes []byte
	if reqBody != nil {
		var err error
		bodyBytes, err = json.Marshal(reqBody)
		if err != nil {
			return err
		}
	}
	rawURL := c.buildAPIURL(endpoint, query)
	backoffs := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}

	var lastErr error
	for attempt := 0; attempt <= len(backoffs); attempt++ {
		if err := c.limiter.Wait(ctx); err != nil {
			return err
		}
		var bodyReader io.Reader
		if len(bodyBytes) > 0 {
			bodyReader = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, rawURL, bodyReader)
		if err != nil {
			return err
		}
		c.applyCommonHeaders(req)
		if len(bodyBytes) > 0 {
			req.Header.Set("Content-Type", "application/json;charset=UTF-8")
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = err
			if attempt < len(backoffs) {
				if sleepErr := sleepCtx(ctx, backoffs[attempt]); sleepErr != nil {
					return sleepErr
				}
				continue
			}
			return fmt.Errorf("请求夸克网盘接口失败: %w", err)
		}

		c.MergeCookies(resp.Cookies())
		c.lastMetaReqUnix.Store(time.Now().Unix())
		respBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if attempt < len(backoffs) {
				if sleepErr := sleepCtx(ctx, backoffs[attempt]); sleepErr != nil {
					return sleepErr
				}
				continue
			}
			return fmt.Errorf("读取夸克网盘响应失败: %w", readErr)
		}

		if resp.StatusCode == http.StatusUnauthorized {
			return ErrUnauthorized
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusBadGateway ||
			resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusGatewayTimeout {
			lastErr = fmt.Errorf("夸克接口返回 HTTP %d", resp.StatusCode)
			if resp.StatusCode == http.StatusTooManyRequests {
				lastErr = ErrRateLimited
			}
			if attempt < len(backoffs) {
				wait := backoffs[attempt]
				if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
					if secs, convErr := strconv.Atoi(ra); convErr == nil && secs > 0 && secs <= 30 {
						wait = time.Duration(secs) * time.Second
					}
				}
				if sleepErr := sleepCtx(ctx, wait); sleepErr != nil {
					return sleepErr
				}
				continue
			}
			return lastErr
		}

		var envelope baseResp
		_ = json.Unmarshal(respBytes, &envelope)

		if resp.StatusCode < 200 || resp.StatusCode >= 300 || !envelope.isOK() {
			apiErr := classifyAPIError(resp.StatusCode, envelope)
			if errors.Is(apiErr, ErrRateLimited) && attempt < len(backoffs) {
				lastErr = apiErr
				if sleepErr := sleepCtx(ctx, backoffs[attempt]); sleepErr != nil {
					return sleepErr
				}
				continue
			}
			return apiErr
		}

		if out != nil {
			if err := json.Unmarshal(respBytes, out); err != nil {
				return fmt.Errorf("解析夸克网盘响应失败: %w", err)
			}
		}
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("夸克网盘接口请求失败")
}

func classifyAPIError(httpStatus int, env baseResp) error {
	msg := strings.TrimSpace(env.Message)
	low := strings.ToLower(msg)
	switch {
	case httpStatus == http.StatusUnauthorized || env.Status == 401 || env.Code == 10001 || env.Code == 31001 ||
		strings.Contains(low, "not login") || strings.Contains(low, "need login") || strings.Contains(low, "cookie") && strings.Contains(low, "invalid") ||
		strings.Contains(msg, "未登录") || strings.Contains(msg, "登录失效"):
		return ErrUnauthorized
	case httpStatus == http.StatusTooManyRequests || env.Status == 429 ||
		strings.Contains(low, "too many") || strings.Contains(low, "rate limit") || strings.Contains(low, "frequent") ||
		strings.Contains(msg, "频繁") || strings.Contains(msg, "限流"):
		return ErrRateLimited
	case msg != "":
		return fmt.Errorf("夸克接口错误 (code=%d): %s", env.Code, msg)
	default:
		return fmt.Errorf("夸克接口返回异常 (HTTP %d, code=%d)", httpStatus, env.Code)
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
