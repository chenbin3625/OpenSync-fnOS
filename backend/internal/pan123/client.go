package pan123

import (
	"context"
	"crypto/md5"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type protocolBackend interface {
	About(ctx context.Context) (Quota, AccountInfo, error)
	ListDir(ctx context.Context, parentID int64) ([]Entry, error)
	Mkdir(ctx context.Context, parentID int64, name string) (int64, error)
	Move(ctx context.Context, entry Entry, dstParentID int64) error
	Rename(ctx context.Context, entry Entry, newName string) error
	Delete(ctx context.Context, entry Entry) error
	DownloadURL(ctx context.Context, entry Entry) (string, http.Header, time.Duration, error)
	UploadFile(ctx context.Context, parentID int64, name string, opt UploadFileOptions, existing *Entry) (*UploadResult, error)
}

// Client is a unified 123Pan filesystem client supporting both the official
// Open Platform API ("open_api") and the personal Web API ("web").
type Client struct {
	protocol          string
	rootFID           int64
	userAgent         string
	uploadConcurrency int

	httpClient   *http.Client
	streamClient *http.Client
	backend      protocolBackend

	cache   *dirCache
	mkdirMu sync.Mutex

	dlCacheMu  sync.Mutex
	dlCache    map[int64]cachedDownload
	dlInFlight map[int64]*dlFlight
	dlTTL      time.Duration
}

// NewClient constructs a 123Pan client configured for either open_api or web.
func NewClient(opts Options) (*Client, error) {
	protocol := NormalizeProtocol(opts.Protocol)
	if protocol != ProtocolOpenAPI && protocol != ProtocolWeb {
		return nil, fmt.Errorf("不支持的 123云盘协议类型 %q（仅支持 open_api 或 web）", opts.Protocol)
	}
	rootFID, err := ParseRootFolderID(opts.RootFolderID)
	if err != nil {
		return nil, err
	}
	ua := strings.TrimSpace(opts.UserAgent)
	if ua == "" {
		ua = DefaultUserAgent
	}
	conc := opts.UploadConcurrency
	if conc <= 0 {
		conc = DefaultUploadConcurrency
	}
	if conc > MaxUploadConcurrency {
		conc = MaxUploadConcurrency
	}
	cacheTTL := opts.CacheTTL
	if cacheTTL <= 0 {
		cacheTTL = DefaultCacheTTL
	}
	dlTTL := opts.DownloadURLTTL
	if dlTTL <= 0 {
		dlTTL = DefaultDownloadURLTTL
	}

	controlTr, err := newHTTPTransport(opts.ProxyURL, opts.InsecureSkipVerify)
	if err != nil {
		return nil, err
	}
	streamTr, err := newHTTPTransport(opts.ProxyURL, opts.InsecureSkipVerify)
	if err != nil {
		return nil, err
	}

	httpClient := &http.Client{
		Transport: controlTr,
		Timeout:   60 * time.Second,
	}
	streamClient := &http.Client{
		Transport: streamTr,
	}

	c := &Client{
		protocol:          protocol,
		rootFID:           rootFID,
		userAgent:         ua,
		uploadConcurrency: conc,
		httpClient:        httpClient,
		streamClient:      streamClient,
		cache:             newDirCache(rootFID, cacheTTL),
		dlCache:           make(map[int64]cachedDownload),
		dlInFlight:        make(map[int64]*dlFlight),
		dlTTL:             dlTTL,
	}

	switch protocol {
	case ProtocolOpenAPI:
		b, err := newOpenAPIBackend(opts, ua, conc, httpClient, streamClient)
		if err != nil {
			c.Close()
			return nil, err
		}
		c.backend = b
	case ProtocolWeb:
		b, err := newWebBackend(opts, ua, conc, httpClient, streamClient)
		if err != nil {
			c.Close()
			return nil, err
		}
		c.backend = b
	}
	return c, nil
}

// Protocol returns the active protocol ("open_api" or "web").
func (c *Client) Protocol() string {
	return c.protocol
}

// Close releases idle HTTP connections held by the client.
func (c *Client) Close() error {
	if c.httpClient != nil {
		c.httpClient.CloseIdleConnections()
	}
	if c.streamClient != nil {
		c.streamClient.CloseIdleConnections()
	}
	return nil
}

func newHTTPTransport(proxyURL string, insecureSkipVerify bool) (*http.Transport, error) {
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 2 * time.Second,
	}
	if pu := strings.TrimSpace(proxyURL); pu != "" {
		parsed, err := url.Parse(pu)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("无效的代理地址：%s", proxyURL)
		}
		tr.Proxy = http.ProxyURL(parsed)
	}
	if insecureSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // user-configured TLS skip
	}
	return tr, nil
}

// --- Shared upload helpers (hashing, progress/pause/throttle, resume state) ---

func hashFileMD5(ctx context.Context, f *os.File, size int64, waitIfPaused func(context.Context) error) (string, error) {
	hMD5 := md5.New()
	buf := make([]byte, 256<<10)
	var offset int64
	for offset < size {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if waitIfPaused != nil {
			if err := waitIfPaused(ctx); err != nil {
				return "", err
			}
		}
		toRead := int64(len(buf))
		if size-offset < toRead {
			toRead = size - offset
		}
		n, readErr := f.ReadAt(buf[:toRead], offset)
		if n > 0 {
			_, _ = hMD5.Write(buf[:n])
			offset += int64(n)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) && offset == size {
				break
			}
			return "", readErr
		}
	}
	return hex.EncodeToString(hMD5.Sum(nil)), nil
}

type uploadProgressTracker struct {
	total      int64
	uploaded   atomic.Int64
	startTime  time.Time
	lastEmitNs atomic.Int64
	onProgress func(doneBytes int64, speed float64, rapid bool)
}

func newUploadProgressTracker(total, initial int64, onProgress func(int64, float64, bool)) *uploadProgressTracker {
	t := &uploadProgressTracker{
		total:      total,
		startTime:  time.Now(),
		onProgress: onProgress,
	}
	t.uploaded.Store(initial)
	return t
}

func (t *uploadProgressTracker) add(n int64) {
	if n == 0 {
		return
	}
	cur := t.uploaded.Add(n)
	if cur < 0 {
		cur = 0
		t.uploaded.Store(0)
	}
	if t.onProgress == nil {
		return
	}
	now := time.Now().UnixNano()
	prev := t.lastEmitNs.Load()
	if cur < t.total && now-prev < int64(150*time.Millisecond) {
		return
	}
	if !t.lastEmitNs.CompareAndSwap(prev, now) && cur < t.total {
		return
	}
	elapsed := time.Since(t.startTime).Seconds()
	speed := 0.0
	if elapsed > 0.05 {
		speed = float64(cur) / elapsed
	}
	t.onProgress(min(cur, t.total), speed, false)
}

type pacedReader struct {
	ctx           context.Context
	r             io.Reader
	waitIfPaused  func(context.Context) error
	throttleBytes func(context.Context, int) error
	tracker       *uploadProgressTracker
	sent          int64
}

func (p *pacedReader) Read(buf []byte) (int, error) {
	if p.ctx.Err() != nil {
		return 0, p.ctx.Err()
	}
	if p.waitIfPaused != nil {
		if err := p.waitIfPaused(p.ctx); err != nil {
			return 0, err
		}
	}
	// Cap read chunk so pause/throttle react smoothly even on low bandwidth limits.
	if len(buf) > 64<<10 {
		buf = buf[:64<<10]
	}
	n, err := p.r.Read(buf)
	if n > 0 {
		if p.throttleBytes != nil {
			if tErr := p.throttleBytes(p.ctx, n); tErr != nil {
				return n, tErr
			}
		}
		p.sent += int64(n)
		if p.tracker != nil {
			p.tracker.add(int64(n))
		}
	}
	return n, err
}

func (p *pacedReader) rollback() {
	if p.sent > 0 && p.tracker != nil {
		p.tracker.add(-p.sent)
		p.sent = 0
	}
}

func loadResumeJSON(filePath string, out any) bool {
	if strings.TrimSpace(filePath) == "" {
		return false
	}
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return false
	}
	return json.Unmarshal(raw, out) == nil
}

func saveResumeJSON(filePath string, val any) {
	if strings.TrimSpace(filePath) == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		return
	}
	raw, err := json.Marshal(val)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(filePath), "."+filepath.Base(filePath)+".*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(raw)
	closeErr := tmp.Close()
	if writeErr == nil && closeErr == nil {
		_ = os.Rename(tmp.Name(), filePath)
	}
}

func isExpiredUploadSession(err error) bool {
	var apiErr *openAPIError
	var webErr *webAPIError
	var message string
	switch {
	case errors.As(err, &apiErr):
		message = apiErr.Message
	case errors.As(err, &webErr):
		message = webErr.Message
	default:
		return false
	}
	message = strings.ToLower(message)
	if strings.Contains(message, "nosuchupload") || strings.Contains(message, "no such upload") {
		return true
	}
	session := strings.Contains(message, "preupload") || strings.Contains(message, "uploadid") ||
		strings.Contains(message, "upload id") || strings.Contains(message, "upload session") || strings.Contains(message, "上传会话")
	return session && (strings.Contains(message, "expired") || strings.Contains(message, "invalid") ||
		strings.Contains(message, "not found") || strings.Contains(message, "不存在") ||
		strings.Contains(message, "过期") || strings.Contains(message, "失效") || strings.Contains(message, "无效"))
}

func removeResumeJSON(filePath string) {
	if strings.TrimSpace(filePath) != "" {
		_ = os.Remove(filePath)
	}
}

// --- Secret redaction ---

var (
	bearerRE = regexp.MustCompile(`(?i)bearer\s+[a-z0-9._~+/=-]+`)
	secretRE = regexp.MustCompile(`(?i)(access_token|client_secret|token|authorization|cookie|password|pass|signature|sign|auth_key|auth-key)=([^\s&]+)`)
	urlRE    = regexp.MustCompile(`https?://[^\s"']+`)
)

func scrubSecrets(input string) string {
	input = bearerRE.ReplaceAllString(input, "Bearer REDACTED")
	input = secretRE.ReplaceAllString(input, "$1=REDACTED")
	return urlRE.ReplaceAllStringFunc(input, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil || u.RawQuery == "" {
			return raw
		}
		u.RawQuery = "REDACTED"
		return u.String()
	})
}

func normalizeMD5(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	if len(s) != 32 {
		return ""
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return ""
		}
	}
	return s
}

func validateEntryName(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	if strings.ContainsAny(name, `/\:*?"<>|`+"\x00\r\n") {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	return nil
}
