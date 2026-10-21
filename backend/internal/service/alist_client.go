package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"opensync/internal/msg"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// defaultMaxListResponseBytes is the cap for a single AList list/API
	// response. Overridden by OPENSYNC_MAX_LIST_BYTES (must be >= 1MB).
	defaultMaxListResponseBytes = 32 << 20 // 32MB
	maxFileListEntries          = 100000
	maxFileListPages            = 10000
	maxAlistWaitBuckets         = 1024
	alistValidationTimeout      = 30 * time.Second
	// maxListResponseBytesCap bounds OPENSYNC_MAX_LIST_BYTES. The helpers add
	// one byte to the limit to detect oversized responses, so a value near
	// math.MaxInt64 would overflow and turn every response into an empty body.
	maxListResponseBytesCap = 1 << 40 // 1TB
)

func loadMaxListResponseBytes() int64 {
	if raw := strings.TrimSpace(os.Getenv("OPENSYNC_MAX_LIST_BYTES")); raw != "" {
		if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n >= (1<<20) {
			if n > maxListResponseBytesCap {
				return maxListResponseBytesCap
			}
			return n
		}
	}
	return defaultMaxListResponseBytes
}

// AlistClient represents an AList HTTP client
type AlistClient struct {
	URL     string
	Token   string
	User    string
	AlistID int64
	waits   map[string]alistWaitSlot
	mu      sync.Mutex
	client  *http.Client
	// longClient shares client's transport but has a longer overall timeout;
	// it is used for /api/fs/copy and /api/fs/move, which can block for
	// minutes on same-storage or synchronous drivers. nil falls back to client.
	longClient *http.Client
	// baseURL is parsed once when the client is created. The URL is immutable
	// for the lifetime of a cached client, so requestURL can copy this small
	// value instead of reparsing the base string on every AList call.
	baseURL url.URL
}

// alistResponse represents AList API response
type alistResponse struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// NewAlistClient creates a new AList client
func NewAlistClient(alistURL string, token string, alistID int64) (*AlistClient, error) {
	return NewAlistClientContext(context.Background(), alistURL, token, alistID)
}

// NewAlistClientContext creates a new AList client and validates it with ctx.
func NewAlistClientContext(ctx context.Context, alistURL string, token string, alistID int64) (*AlistClient, error) {
	normalizedURL, err := normalizeAlistBaseURL(alistURL)
	if err != nil {
		return nil, err
	}
	parsedURL, err := url.Parse(normalizedURL)
	if err != nil {
		return nil, errors.New(msg.T(msg.AlistURLInvalid))
	}
	c := &AlistClient{
		URL:     normalizedURL,
		Token:   token,
		AlistID: alistID,
		waits:   make(map[string]alistWaitSlot),
		baseURL: *parsedURL,
	}
	c.client, c.longClient = newAlistHTTPClients()
	if err := c.getUserContext(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Close releases idle HTTP connections held by this client, including any
// HTTP/3 UDP sockets created after an Alt-Svc upgrade.
func (c *AlistClient) Close() {
	if c == nil || c.client == nil || c.client.Transport == nil {
		return
	}
	switch transport := c.client.Transport.(type) {
	case io.Closer:
		_ = transport.Close()
	case interface{ CloseIdleConnections() }:
		transport.CloseIdleConnections()
	}
}

func (c *AlistClient) doRequestContext(ctx context.Context, method, apiPath string, data interface{}, params map[string]string) (json.RawMessage, error) {
	return c.doRequestContextLimit(ctx, method, apiPath, data, params, alistDeps.MaxResponseBytes)
}

func (c *AlistClient) startRequest(ctx context.Context, method, apiPath string, data interface{}, params map[string]string) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	var body io.Reader
	if data != nil {
		jsonData, err := json.Marshal(data)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(jsonData)
	}

	reqURL, err := c.requestURL(apiPath, params)
	if err != nil {
		return nil, errors.New(msg.T(msg.AddressIncorrect))
	}

	req, err := http.NewRequestWithContext(ctx, method, reqURL, body)
	if err != nil {
		return nil, errors.New(msg.T(msg.AddressIncorrect))
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", c.Token)
	}

	httpClient := c.client
	if c.longClient != nil && isAlistLongOperation(ctx) {
		httpClient = c.longClient
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "no such host") {
			return nil, errors.New(msg.T(msg.AlistConnectFail))
		}
		return nil, err
	}
	return resp, nil
}

// alistStatusError carries the HTTP status and AList business code of a failed
// request so callers can branch on machine-readable codes instead of matching
// error text. Error() keeps the historical message format.
type alistStatusError struct {
	httpStatus int
	alistCode  int
	message    string
}

func (e *alistStatusError) Error() string {
	if e.alistCode != 0 {
		return msg.AlistFailCodeReason(e.alistCode, e.message)
	}
	return fmt.Sprintf("%s (HTTP %d)", msg.T(msg.CodeNot200), e.httpStatus)
}

// isAlistObjectNotFound reports whether err means "the requested path does not
// exist": an HTTP 404, an AList business code 404, or AList's historical
// 500-with-"object not found" response for a missing object. Deliberately
// narrower than matching any "not found" text — infrastructure failures like
// "storage not found" or a proxy 404 page must surface as real errors.
func isAlistObjectNotFound(err error) bool {
	var statusErr *alistStatusError
	if !errors.As(err, &statusErr) {
		return false
	}
	if statusErr.httpStatus == http.StatusNotFound || statusErr.alistCode == http.StatusNotFound {
		return true
	}
	return statusErr.alistCode == 500 && strings.Contains(strings.ToLower(statusErr.message), "object not found")
}

func (c *AlistClient) doRequestContextLimit(ctx context.Context, method, apiPath string, data interface{}, params map[string]string, responseLimit int64) (json.RawMessage, error) {
	resp, err := c.startRequest(ctx, method, apiPath, data, params)
	if err != nil {
		return nil, err
	}
	defer closeBody(resp.Body)

	// The status is checked before the body is read: a large non-200 error
	// page (e.g. a reverse proxy's HTML) would otherwise surface as a
	// "response body exceeds" error and hide the HTTP status that callers
	// branch on (isAlistObjectNotFound, isAmbiguousTransferError).
	if resp.StatusCode != http.StatusOK {
		return nil, c.httpStatusError(resp)
	}

	respBody, err := readAllWithLimit(resp.Body, responseLimit)
	if err != nil {
		return nil, err
	}

	var res alistResponse
	if err := json.Unmarshal(respBody, &res); err != nil {
		return nil, err
	}
	if err := c.checkAlistCode(res.Code, res.Message); err != nil {
		return nil, err
	}
	return res.Data, nil
}

// maxErrorBodyBytes bounds how much of a non-200 response is read to extract
// an AList business code; the rest is drained by closeBody.
const maxErrorBodyBytes = 4 << 10

// httpStatusError builds the error for a non-200 response. HTTP 401 means the
// token was rejected, exactly like AList's business code 401, so both take the
// same eviction path. Only a small prefix of the body is read.
func (c *AlistClient) httpStatusError(resp *http.Response) error {
	if resp.StatusCode == http.StatusUnauthorized {
		return c.unauthorized()
	}
	// Only an embedded 401 is taken from the body. Other business codes stay
	// out of the error on purpose: the HTTP status is the authoritative answer
	// here, and isAlistObjectNotFound must not reinterpret, say, an HTTP 500
	// as "object missing" because its body mentions one.
	if prefix, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes)); err == nil {
		var res alistResponse
		if json.Unmarshal(prefix, &res) == nil && res.Code == http.StatusUnauthorized {
			return c.unauthorized()
		}
	}
	return &alistStatusError{httpStatus: resp.StatusCode}
}

// unauthorized evicts this exact client from the cache (never a newer one
// stored for the same id) and returns the public "unauthorized" error.
func (c *AlistClient) unauthorized() error {
	if c.AlistID > 0 {
		removeCachedAlistClientIf(c.AlistID, c)
	}
	return errors.New(msg.T(msg.AlistUnAuth))
}

func (c *AlistClient) checkAlistCode(code int, message string) error {
	if code == 401 {
		return c.unauthorized()
	}
	if code != 200 {
		return &alistStatusError{alistCode: code, message: message}
	}
	return nil
}

func normalizeAlistBaseURL(rawURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New(msg.T(msg.AlistURLInvalid))
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New(msg.T(msg.AlistURLInvalid))
	}
	u.Scheme = scheme
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

func (c *AlistClient) requestURL(apiPath string, params map[string]string) (string, error) {
	base := c.baseURL
	if base.Scheme == "" || base.Host == "" {
		parsed, err := url.Parse(c.URL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return "", errors.New(msg.T(msg.AddressIncorrect))
		}
		base = *parsed
	}
	endpoint, err := url.Parse(apiPath)
	if err != nil || endpoint.IsAbs() || endpoint.Host != "" {
		return "", errors.New(msg.T(msg.AddressIncorrect))
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/" + strings.TrimLeft(endpoint.Path, "/")
	base.RawPath = ""
	query := base.Query()
	for key, value := range endpoint.Query() {
		for _, item := range value {
			query.Add(key, item)
		}
	}
	for key, value := range params {
		query.Set(key, value)
	}
	base.RawQuery = query.Encode()
	return base.String(), nil
}

func (c *AlistClient) PostContext(ctx context.Context, apiPath string, data interface{}, params map[string]string) (json.RawMessage, error) {
	return c.doRequestContext(ctx, "POST", apiPath, data, params)
}

func (c *AlistClient) GetContext(ctx context.Context, apiPath string, params map[string]string) (json.RawMessage, error) {
	return c.doRequestContext(ctx, "GET", apiPath, nil, params)
}

func (c *AlistClient) getUserContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, alistValidationTimeout)
	defer cancel()
	data, err := c.GetContext(ctx, "/api/me", nil)
	if err != nil {
		return err
	}
	var result struct {
		Username string `json:"username"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	c.User = result.Username
	return nil
}

func (c *AlistClient) CheckWaitContext(ctx context.Context, path string, scanInterval int) error {
	return c.checkWaitContext(ctx, path, scanInterval)
}

// checkWaitContext spaces AList requests for the same top-level mount by
// scanInterval: the value is a request-rate limit for the mount, so concurrent
// callers start their requests at 0, interval, 2*interval, … while the requests
// themselves still run concurrently.
//
// The previous implementation divided the interval by the scan concurrency,
// which multiplied the effective request rate by the number of concurrent
// scanners and contradicted both the config and the comment claiming total
// throughput stayed the same. Callers must not expect burst capacity from
// scanInterval. A cancelled caller rolls its reservation back so it does not
// delay later requests.
func (c *AlistClient) checkWaitContext(ctx context.Context, path string, scanInterval int) error {
	if scanInterval <= 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	pathFirst := firstAlistPathSegment(path)
	if pathFirst == "" {
		return nil
	}

	interval := time.Duration(scanInterval) * time.Second
	if interval < time.Millisecond {
		interval = time.Millisecond
	}

	now := time.Now()
	waitUntil := now

	c.mu.Lock()
	if c.waits == nil {
		c.waits = make(map[string]alistWaitSlot)
	}
	// Prune by each bucket's own reserved interval: a reservation made with a
	// longer interval (an older config value, or a mkdir/remove racing a
	// listing) must not be dropped early, which would let requests exceed the
	// configured rate.
	c.pruneWaitsLocked(now)
	previous, hadPrevious := c.waits[pathFirst]
	if hadPrevious && now.Sub(previous.at) < interval {
		waitUntil = previous.at.Add(interval)
	}
	slot := alistWaitSlot{at: waitUntil, interval: interval}
	if hadPrevious && previous.interval > slot.interval {
		slot.interval = previous.interval
	}
	c.waits[pathFirst] = slot
	c.enforceMaxWaitBucketsLocked()
	c.mu.Unlock()

	if waitUntil.After(now) {
		timer := time.NewTimer(time.Until(waitUntil))
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			c.releaseWaitSlot(pathFirst, slot, previous, hadPrevious)
			return ctx.Err()
		}
	}
	return nil
}

// alistWaitSlot is one rate-limit reservation: the time the request may run
// and the spacing it was reserved with (used to decide when it is stale).
type alistWaitSlot struct {
	at       time.Time
	interval time.Duration
}

// releaseWaitSlot rolls back a reservation whose caller gave up waiting, so a
// cancelled request does not push every later caller further out. It only
// rolls back when nobody reserved after it; otherwise the later reservation
// already accounts for the spacing and must stay.
func (c *AlistClient) releaseWaitSlot(pathFirst string, slot, previous alistWaitSlot, hadPrevious bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current, ok := c.waits[pathFirst]
	if !ok || current != slot {
		return
	}
	if hadPrevious {
		c.waits[pathFirst] = previous
	} else {
		delete(c.waits, pathFirst)
	}
}

// firstAlistPathSegment preserves the historical SplitN(path, "/", 3)[1]
// behavior while avoiding a temporary slice on every rate-limited AList
// operation. AList paths normally start with '/', but the no-leading-slash
// form is kept compatible for callers and tests.
func firstAlistPathSegment(path string) string {
	firstSlash := strings.IndexByte(path, '/')
	if firstSlash < 0 {
		return ""
	}
	rest := path[firstSlash+1:]
	if nextSlash := strings.IndexByte(rest, '/'); nextSlash >= 0 {
		return rest[:nextSlash]
	}
	return rest
}

// pruneWaitsLocked drops reservations that no longer constrain anyone: a slot
// is stale once its own interval has elapsed since its reserved time.
func (c *AlistClient) pruneWaitsLocked(now time.Time) {
	for pathFirst, slot := range c.waits {
		if !slot.at.Add(slot.interval).After(now) {
			delete(c.waits, pathFirst)
		}
	}
}

func (c *AlistClient) enforceMaxWaitBucketsLocked() {
	for len(c.waits) > maxAlistWaitBuckets {
		var oldestPath string
		var oldestTime time.Time
		first := true
		for pathFirst, slot := range c.waits {
			if first || slot.at.Before(oldestTime) {
				oldestPath = pathFirst
				oldestTime = slot.at
				first = false
			}
		}
		if oldestPath == "" {
			return
		}
		delete(c.waits, oldestPath)
	}
}

// FileListResponse represents a file list entry
type FileListEntry struct {
	Name     string      `json:"name"`
	IsDir    bool        `json:"is_dir"`
	Size     int64       `json:"size"`
	Modified int64       `json:"modified"`
	HashInfo interface{} `json:"hash_info"`
	Hashinfo string      `json:"hashinfo"`
}

func (e *FileListEntry) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name     string          `json:"name"`
		IsDir    bool            `json:"is_dir"`
		Size     int64           `json:"size"`
		Modified json.RawMessage `json:"modified"`
		HashInfo json.RawMessage `json:"hash_info"`
		Hashinfo string          `json:"hashinfo"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	e.Name, e.IsDir, e.Size, e.Modified, e.Hashinfo = raw.Name, raw.IsDir, raw.Size, parseModified(raw.Modified), raw.Hashinfo
	if len(raw.HashInfo) > 0 && string(raw.HashInfo) != "null" {
		e.HashInfo = raw.HashInfo
	}
	return nil
}

// parseModified accepts both the numeric timestamps used by older AList
// versions and the quoted RFC3339 timestamps returned by newer versions.
// Modified is only advisory metadata, so an unknown value is treated as zero
// instead of rejecting the entire directory listing.
func parseModified(raw json.RawMessage) int64 {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0
	}
	if raw[0] == '"' {
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return 0
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return 0
		}
		if timestamp, err := strconv.ParseInt(value, 10, 64); err == nil {
			return timestamp
		}
		if timestamp, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return timestamp.Unix()
		}
		return 0
	}
	timestamp, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return 0
	}
	return timestamp
}

// FileMetadata contains lightweight comparison data from AList list results.
type FileMetadata struct {
	Size     int64
	MD5      string
	Modified int64
}

func (e FileListEntry) metadata() FileMetadata {
	return FileMetadata{
		Size:     e.Size,
		MD5:      normalizeMD5(md5FromHashFields(e.HashInfo, e.Hashinfo)),
		Modified: e.Modified,
	}
}

func md5FromHashFields(hashInfo interface{}, hashinfo string) string {
	switch value := hashInfo.(type) {
	case json.RawMessage:
		if md5 := md5FromRaw(value); md5 != "" {
			return md5
		}
	case map[string]interface{}:
		if md5, ok := value["md5"].(string); ok {
			return md5
		}
		if md5, ok := value["MD5"].(string); ok {
			return md5
		}
	}
	if hashinfo != "" {
		return md5FromRaw([]byte(hashinfo))
	}
	return ""
}

// md5FromRaw pulls the md5/MD5 string out of a small JSON object without
// allocating a map[string]interface{} per file. AList list pages are 500
// entries; the old decoder boxed every hash key on the scan hot path.
func md5FromRaw(raw []byte) string {
	if value := jsonQuotedString(raw, `"md5"`); value != "" {
		return normalizeMD5(value)
	}
	return normalizeMD5(jsonQuotedString(raw, `"MD5"`))
}

func jsonQuotedString(raw []byte, quotedKey string) string {
	idx := bytes.Index(raw, []byte(quotedKey))
	if idx < 0 {
		return ""
	}
	rest := bytes.TrimSpace(raw[idx+len(quotedKey):])
	if len(rest) == 0 || rest[0] != ':' {
		return ""
	}
	rest = bytes.TrimSpace(rest[1:])
	if len(rest) < 2 || rest[0] != '"' {
		return ""
	}
	rest = rest[1:]
	end := 0
	for end < len(rest) {
		if rest[end] == '\\' {
			end += 2
			if end > len(rest) {
				return ""
			}
			continue
		}
		if rest[end] == '"' {
			return string(rest[:end])
		}
		end++
	}
	return ""
}

func normalizeMD5(md5 string) string {
	return strings.ToLower(strings.TrimSpace(md5))
}

// FileListResult maps filename -> comparison metadata.
// Directories use a key ending with "/" and a zero FileMetadata value so a
// 10k-entry listing does not allocate 10k empty maps or box every file in
// interface{}.
type FileListResult = map[string]FileMetadata

// FilePathList gets subdirectory list for path selector
func (c *AlistClient) FilePathList(ctx context.Context, path string) ([]map[string]string, error) {
	files, err := c.FileListApiContext(ctx, path, 0, 0)
	if err != nil {
		return nil, err
	}

	result := make([]map[string]string, 0, len(files))
	for name := range files {
		if strings.HasSuffix(name, "/") {
			result = append(result, map[string]string{"path": strings.TrimSuffix(name, "/")})
		}
	}
	return result, nil
}

// FileExistsContext reports whether a direct child named `name` exists in `dir`.
func (c *AlistClient) FileExistsContext(ctx context.Context, dir, name string) (bool, error) {
	return c.FileGetContext(ctx, dir, name)
}

// FileGetContext checks a single file via /api/fs/get (cheaper than listing the directory).
func (c *AlistClient) FileGetContext(ctx context.Context, dir, name string) (bool, error) {
	stat, err := c.FileStatContext(ctx, dir, name)
	if err != nil {
		return false, err
	}
	return stat.Exists, nil
}

// FileStat is the subset of /api/fs/get metadata used to verify transfers.
type FileStat struct {
	Exists bool
	IsDir  bool
	// SizeKnown is false when the response carried no "size" field (some
	// drivers/proxies omit it); callers must not compare Size then.
	SizeKnown bool
	Size      int64
	Modified  int64
}

// FileStatContext returns metadata for dir/name via /api/fs/get. A missing
// object is reported as Exists=false with a nil error.
//
// Only trailing slashes are trimmed from dir and name is used verbatim: file
// names may legitimately start or end with spaces, and trimming them made the
// probe look at a different path than the one being copied.
func (c *AlistClient) FileStatContext(ctx context.Context, dir, name string) (FileStat, error) {
	filePath := strings.TrimRight(dir, "/") + "/" + name
	data, err := c.PostContext(ctx, "/api/fs/get", alistPathRequest{Path: filePath}, nil)
	if err != nil {
		if isAlistObjectNotFound(err) {
			return FileStat{}, nil
		}
		return FileStat{}, err
	}
	stat := FileStat{Exists: true}
	if len(data) > 0 && string(data) != "null" {
		var raw struct {
			IsDir    bool            `json:"is_dir"`
			Size     *int64          `json:"size"`
			Modified json.RawMessage `json:"modified"`
		}
		// An undecodable payload must be an error, not "exists with unknown
		// size": verifyDstComplete treats SizeKnown=false as complete, so a
		// garbled response would otherwise turn a failed transfer into success.
		if err := json.Unmarshal(data, &raw); err != nil {
			return FileStat{}, fmt.Errorf("/api/fs/get response decode failed: %w", err)
		}
		stat.IsDir = raw.IsDir
		stat.Modified = parseModified(raw.Modified)
		if raw.Size != nil {
			stat.SizeKnown, stat.Size = true, *raw.Size
		}
	}
	return stat, nil
}

func (c *AlistClient) MkdirContext(ctx context.Context, path string, scanInterval int) error {
	if err := c.CheckWaitContext(ctx, path, scanInterval); err != nil {
		return err
	}
	_, err := c.PostContext(ctx, "/api/fs/mkdir", alistPathRequest{Path: path}, nil)
	return err
}

// validateRemoveRequest is the last line of defence before /api/fs/remove.
// Some AList/OpenList versions join dir with each name without validation, so
// an empty name (or ".", "..", or a name carrying a separator) removes the
// directory itself or a sibling tree. A trailing "/" is tolerated because
// directory names are sometimes passed in their listing form ("sub/").
func validateRemoveRequest(dir string, names []string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("alist remove rejected: empty directory")
	}
	if len(names) == 0 {
		return errors.New("alist remove rejected: no names")
	}
	for _, name := range names {
		trimmed := strings.TrimSuffix(name, "/")
		if trimmed == "" || trimmed == "." || trimmed == ".." || strings.ContainsAny(trimmed, "/\\") {
			return fmt.Errorf("alist remove rejected: unsafe name %q in %q", name, dir)
		}
	}
	return nil
}

func (c *AlistClient) DeleteFileContext(ctx context.Context, path string, names []string, scanInterval int) error {
	if err := validateRemoveRequest(path, names); err != nil {
		return err
	}
	if err := c.CheckWaitContext(ctx, path, scanInterval); err != nil {
		return err
	}
	_, err := c.PostContext(ctx, "/api/fs/remove", alistRemoveRequest{Names: names, Dir: path}, nil)
	// Removing an object that is already gone is success. Without this a
	// delete whose response was lost is retried, the retry hits "object not
	// found", and a delete that actually happened is reported as failed. The
	// mirror phase also asks to remove entries the destination listing showed,
	// which another actor may have removed already.
	if err != nil && isAlistObjectNotFound(err) {
		return nil
	}
	return err
}

func (c *AlistClient) copyOrMoveFileContext(ctx context.Context, apiPath, srcDir, dstDir, name string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	data, err := c.PostContext(withAlistLongOperation(ctx), apiPath, alistCopyMoveRequest{
		SrcDir:    srcDir,
		DstDir:    dstDir,
		Overwrite: true,
		Names:     []string{name},
	}, nil)
	if err != nil {
		return "", fmt.Errorf("%s request failed: %w", apiPath, err)
	}
	var result struct {
		Tasks []struct {
			ID string `json:"id"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("%s response decode failed: %w", apiPath, err)
	}
	if len(result.Tasks) > 0 {
		return result.Tasks[0].ID, nil
	}
	return "", nil
}

func (c *AlistClient) CopyFileContext(ctx context.Context, srcDir, dstDir, name string) (string, error) {
	return c.copyOrMoveFileContext(ctx, "/api/fs/copy", srcDir, dstDir, name)
}

func (c *AlistClient) MoveFileContext(ctx context.Context, srcDir, dstDir, name string) (string, error) {
	return c.copyOrMoveFileContext(ctx, "/api/fs/move", srcDir, dstDir, name)
}

func alistTaskGroup(copyType taskItemType) string {
	if copyType == taskItemTypeMove {
		return "move"
	}
	return "copy"
}

func (c *AlistClient) taskActionContext(ctx context.Context, taskID string, copyType taskItemType, action string) (json.RawMessage, error) {
	apiPath := fmt.Sprintf("/api/admin/task/%s/%s", alistTaskGroup(copyType), action)
	return c.PostContext(ctx, apiPath, nil, map[string]string{"tid": taskID})
}

// TaskUndoneListContext lists the copy/move tasks AList has not finished yet.
//
// This endpoint is GET: AList (and OpenList) register
// /api/admin/task/{copy,move}/undone as GET only, so a POST falls through to the
// SPA NoRoute handler and answers HTTP 200 with HTML. The JSON decode then
// failed and both callers swallowed the error, which silently disabled task
// adoption in reconcileAmbiguousTransfer and the monitor's undone fast path.
func (c *AlistClient) TaskUndoneListContext(ctx context.Context, copyType taskItemType) ([]map[string]interface{}, error) {
	apiPath := fmt.Sprintf("/api/admin/task/%s/undone", alistTaskGroup(copyType))
	data, err := c.GetContext(ctx, apiPath, nil)
	if err != nil {
		return nil, err
	}
	var tasks []map[string]interface{}
	if err := json.Unmarshal(data, &tasks); err != nil {
		return nil, err
	}
	if tasks == nil {
		tasks = []map[string]interface{}{}
	}
	return tasks, nil
}

func (c *AlistClient) TaskInfoContext(ctx context.Context, taskID string, copyType taskItemType) (map[string]interface{}, error) {
	data, err := c.taskActionContext(ctx, taskID, copyType, "info")
	if err != nil {
		return nil, err
	}
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *AlistClient) TaskDeleteContext(ctx context.Context, taskID string, copyType taskItemType) error {
	_, err := c.taskActionContext(ctx, taskID, copyType, "delete")
	return err
}

func (c *AlistClient) TaskCancelContext(ctx context.Context, taskID string, copyType taskItemType) error {
	_, err := c.taskActionContext(ctx, taskID, copyType, "cancel")
	return err
}
