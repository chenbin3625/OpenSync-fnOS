package pan123

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	webSignInPath        = "/api/user/sign_in"
	webUserInfoPath      = "/user/info"
	webFileListPath      = "/file/list/new"
	webDownloadInfoPath  = "/file/download_info"
	webUploadRequestPath = "/file/upload_request"
	webMovePath          = "/file/mod_pid"
	webRenamePath        = "/file/rename"
	webTrashPath         = "/file/trash"
	webSingleAuthPath    = "/file/s3_upload_object/auth"
	webPresignedParts    = "/file/s3_repare_upload_parts_batch"
	webListUploadParts   = "/file/s3_list_upload_parts"
	webUploadCompleteV2  = "/file/upload_complete/v2"

	webListPageSize          = 100
	webListConsistencyTrials = 3
	webMaxListPages          = 100_000
	webUploadBatchSize       = int64(10)
	webDefaultSliceSize      = int64(16 << 20) // 16 MiB fallback
	webMaxRetries            = 3
)

var (
	webDigitTable = [...]byte{'a', 'd', 'e', 'f', 'g', 'h', 'l', 'm', 'y', 'i', 'j', 'n', 'o', 'p', 'k', 'q', 'r', 's', 't', 'u', 'b', 'c', 'v', 'w', 's', 'z'}

	webDefaultQPS = map[string]float64{
		webFileListPath:      2.5,
		webDownloadInfoPath:  3.0,
		webUploadRequestPath: 2.0,
		webMovePath:          2.0,
		webRenamePath:        2.0,
		webTrashPath:         2.0,
		webPresignedParts:    3.0,
		webSingleAuthPath:    3.0,
		webUploadCompleteV2:  3.0,
	}

	authChallengeMarkers = []string{
		"短信验证码",
		"短信验证",
		"微信进行登录",
		"微信登录",
		"安全风险",
		"账号风险",
		"帐号风险",
		"设备风险",
		"陌生设备",
		"验证码",
		"verification code",
		"sms verification",
		"wechat login",
		"security risk",
	}
)

type webBackend struct {
	loginRoot   string
	apiRoot     string
	origin      string
	platform    string
	username    string
	password    string
	userAgent   string
	concurrency int

	httpClient    *http.Client
	streamClient  *http.Client
	noRedirClient *http.Client
	limiter       *endpointLimiter
	onTokenUpdate func(string, time.Time)

	nowFn  func() time.Time
	randFn func() (uint32, error)

	tokenMu      sync.RWMutex
	token        string
	challengeErr error
	refreshMu    sync.Mutex
}

func newWebBackend(opts Options, ua string, conc int, httpClient, streamClient *http.Client) (*webBackend, error) {
	token := strings.TrimSpace(opts.AccessToken)
	user := strings.TrimSpace(opts.Username)
	pass := opts.Password
	if token == "" && (user == "" || pass == "") {
		return nil, errors.New("123云盘个人盘 Web 模式需要填写账号密码，或直接提供 Web Access Token")
	}
	loginRoot := strings.TrimRight(strings.TrimSpace(opts.WebLoginURL), "/")
	if loginRoot == "" {
		loginRoot = DefaultWebLoginURL
	}
	apiRoot := strings.TrimRight(strings.TrimSpace(opts.WebAPIURL), "/")
	if apiRoot == "" {
		apiRoot = DefaultWebAPIURL
	}
	origin := strings.TrimRight(strings.TrimSpace(opts.WebOrigin), "/")
	if origin == "" {
		origin = DefaultWebOrigin
	}
	platform := strings.TrimSpace(opts.Platform)
	if platform == "" {
		platform = DefaultWebPlatform
	}

	noRedir := *streamClient
	noRedir.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	return &webBackend{
		loginRoot:     loginRoot,
		apiRoot:       apiRoot,
		origin:        origin,
		platform:      platform,
		username:      user,
		password:      pass,
		userAgent:     ua,
		concurrency:   conc,
		httpClient:    httpClient,
		streamClient:  streamClient,
		noRedirClient: &noRedir,
		limiter:       newEndpointLimiter(opts.TPSLimit, webDefaultQPS, 3.0),
		onTokenUpdate: opts.OnTokenUpdate,
		nowFn:         time.Now,
		randFn:        cryptoNonce,
		token:         token,
	}, nil
}

func cryptoNonce() (uint32, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b[:]) % 10_000_001, nil
}

// SignWebURL appends the 123Pan web CRC32 time/path signature query parameter.
func SignWebURL(rawURL string, now time.Time, nonce uint32) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return "", fmt.Errorf("无效的 123云盘 Web API 地址")
	}
	ts := strconv.FormatInt(now.Unix(), 10)
	digits := now.In(cstZone).Format("200601021504")
	mapped := make([]byte, len(digits))
	for i := range digits {
		mapped[i] = webDigitTable[digits[i]-'0']
	}
	timeSign := strconv.FormatUint(uint64(crc32.ChecksumIEEE(mapped)), 10)
	nonceStr := strconv.FormatUint(uint64(nonce), 10)
	material := strings.Join([]string{ts, nonceStr, u.Path, DefaultWebPlatform, DefaultWebAppVersion, timeSign}, "|")
	dataSign := strconv.FormatUint(uint64(crc32.ChecksumIEEE([]byte(material))), 10)
	q := u.Query()
	q.Add(timeSign, strings.Join([]string{ts, nonceStr, dataSign}, "-"))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

type webEnvelope struct {
	Code      *int            `json:"code"`
	Message   string          `json:"message"`
	RequestID string          `json:"request_id,omitempty"`
	Data      json.RawMessage `json:"data"`
}

type webAPIError struct {
	HTTPStatus int
	Code       int
	Message    string
	RequestID  string
}

func (e *webAPIError) Error() string {
	msg := scrubSecrets(strings.TrimSpace(e.Message))
	if msg == "" {
		msg = fmt.Sprintf("code %d", e.Code)
	}
	return fmt.Sprintf("123云盘 Web 接口错误 (HTTP %d, code=%d): %s", e.HTTPStatus, e.Code, msg)
}

func isAuthChallengeMessage(msg string) bool {
	low := strings.ToLower(msg)
	for _, m := range authChallengeMarkers {
		if strings.Contains(low, m) {
			return true
		}
	}
	return false
}

func (b *webBackend) canLogin() bool {
	return b.username != "" && b.password != ""
}

func (b *webBackend) ensureToken(ctx context.Context, forceRefresh bool, badToken string) (string, error) {
	b.tokenMu.RLock()
	tok := b.token
	cErr := b.challengeErr
	b.tokenMu.RUnlock()

	if cErr != nil {
		return "", cErr
	}
	if !forceRefresh && tok != "" {
		return tok, nil
	}
	if !b.canLogin() {
		return "", ErrUnauthorized
	}

	b.refreshMu.Lock()
	defer b.refreshMu.Unlock()

	b.tokenMu.RLock()
	curTok := b.token
	cErr = b.challengeErr
	b.tokenMu.RUnlock()
	if cErr != nil {
		return "", cErr
	}
	if curTok != "" && curTok != badToken && (!forceRefresh || badToken != "") {
		return curTok, nil
	}

	body := map[string]any{"password": b.password}
	if strings.Contains(b.username, "@") {
		body["mail"] = b.username
		body["type"] = 2
	} else {
		body["passport"] = b.username
		body["remember"] = true
	}
	rawBody, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	nonce, err := b.randFn()
	if err != nil {
		return "", err
	}
	signedURL, err := SignWebURL(b.loginRoot+webSignInPath, b.nowFn(), nonce)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, signedURL, bytes.NewReader(rawBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", b.origin)
	req.Header.Set("Referer", b.origin+"/")
	req.Header.Set("Platform", b.platform)
	req.Header.Set("App-Version", DefaultWebAppVersion)
	req.Header.Set("User-Agent", b.userAgent)

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	rawResp, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", err
	}
	var env webEnvelope
	if err := json.Unmarshal(rawResp, &env); err != nil {
		return "", fmt.Errorf("解析 123云盘登录响应失败 (HTTP %d): %w", resp.StatusCode, err)
	}
	if env.Code == nil {
		return "", fmt.Errorf("123云盘登录响应缺少 code 字段")
	}
	if *env.Code != 200 {
		if isAuthChallengeMessage(env.Message) {
			chErr := fmt.Errorf("%w（服务端提示：%s）", ErrAuthChallenge, scrubSecrets(strings.TrimSpace(env.Message)))
			b.tokenMu.Lock()
			b.challengeErr = chErr
			b.tokenMu.Unlock()
			return "", chErr
		}
		if *env.Code == 401 || resp.StatusCode == http.StatusUnauthorized {
			return "", fmt.Errorf("%w: %s", ErrUnauthorized, scrubSecrets(env.Message))
		}
		return "", &webAPIError{HTTPStatus: resp.StatusCode, Code: *env.Code, Message: env.Message, RequestID: env.RequestID}
	}
	var loginData struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(env.Data, &loginData); err != nil || strings.TrimSpace(loginData.Token) == "" {
		return "", fmt.Errorf("%w: 登录响应未包含 token", ErrUnauthorized)
	}

	b.tokenMu.Lock()
	b.token = strings.TrimSpace(loginData.Token)
	b.tokenMu.Unlock()

	if b.onTokenUpdate != nil {
		b.onTokenUpdate(strings.TrimSpace(loginData.Token), time.Time{})
	}
	return strings.TrimSpace(loginData.Token), nil
}

func (b *webBackend) doJSON(ctx context.Context, method, endpoint string, query url.Values, body any, out any, retryable bool, allowEmptyData bool) error {
	refreshedAuth := false
	for attempt := 0; ; attempt++ {
		tok, err := b.ensureToken(ctx, false, "")
		if err != nil {
			return err
		}
		retryAfter, err := b.doJSONOnce(ctx, method, endpoint, query, body, out, tok, allowEmptyData)
		if err == nil {
			return nil
		}
		var apiErr *webAPIError
		if errors.As(err, &apiErr) {
			if isAuthChallengeMessage(apiErr.Message) {
				chErr := fmt.Errorf("%w（服务端提示：%s）", ErrAuthChallenge, scrubSecrets(strings.TrimSpace(apiErr.Message)))
				b.tokenMu.Lock()
				b.challengeErr = chErr
				b.tokenMu.Unlock()
				return chErr
			}
			if apiErr.HTTPStatus == http.StatusUnauthorized || apiErr.Code == 401 {
				if !refreshedAuth && b.canLogin() {
					refreshedAuth = true
					if _, lErr := b.ensureToken(ctx, true, tok); lErr != nil {
						return lErr
					}
					continue
				}
				return fmt.Errorf("%w: %v", ErrUnauthorized, apiErr)
			}
			if apiErr.HTTPStatus == http.StatusTooManyRequests || apiErr.Code == 429 {
				if attempt < webMaxRetries {
					if sErr := sleepContext(ctx, retryBackoff(attempt, retryAfter)); sErr != nil {
						return sErr
					}
					continue
				}
				return fmt.Errorf("%w: %v", ErrRateLimited, apiErr)
			}
			if retryable && isRetryableHTTPStatus(apiErr.HTTPStatus) && attempt < webMaxRetries {
				if sErr := sleepContext(ctx, retryBackoff(attempt, retryAfter)); sErr != nil {
					return sErr
				}
				continue
			}
			return apiErr
		}
		if retryable && ctx.Err() == nil && attempt < webMaxRetries {
			if sErr := sleepContext(ctx, retryBackoff(attempt, retryAfter)); sErr != nil {
				return sErr
			}
			continue
		}
		return err
	}
}

func (b *webBackend) doJSONOnce(ctx context.Context, method, endpoint string, query url.Values, body any, out any, token string, allowEmptyData bool) (string, error) {
	if err := b.limiter.Wait(ctx, endpoint); err != nil {
		return "", err
	}
	rawURL := b.apiRoot + endpoint
	if len(query) > 0 {
		rawURL += "?" + query.Encode()
	}
	nonce, err := b.randFn()
	if err != nil {
		return "", err
	}
	signedURL, err := SignWebURL(rawURL, b.nowFn(), nonce)
	if err != nil {
		return "", err
	}
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, signedURL, reqBody)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Origin", b.origin)
	req.Header.Set("Referer", b.origin+"/")
	req.Header.Set("Platform", b.platform)
	req.Header.Set("App-Version", DefaultWebAppVersion)
	req.Header.Set("User-Agent", b.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	retryAfter := resp.Header.Get("Retry-After")
	rawResp, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return retryAfter, err
	}
	var env webEnvelope
	if err := json.Unmarshal(rawResp, &env); err != nil {
		if resp.StatusCode >= 300 {
			return retryAfter, &webAPIError{HTTPStatus: resp.StatusCode, Code: resp.StatusCode, Message: string(rawResp)}
		}
		return retryAfter, fmt.Errorf("解析 123云盘 Web 响应失败: %w", err)
	}
	if env.Code == nil {
		return retryAfter, &webAPIError{HTTPStatus: resp.StatusCode, Code: -1, Message: "响应缺少 code 字段", RequestID: env.RequestID}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || *env.Code != 0 {
		return retryAfter, &webAPIError{HTTPStatus: resp.StatusCode, Code: *env.Code, Message: env.Message, RequestID: env.RequestID}
	}
	if out != nil {
		if len(env.Data) == 0 || string(env.Data) == "null" {
			if allowEmptyData {
				return retryAfter, nil
			}
			return retryAfter, &webAPIError{HTTPStatus: resp.StatusCode, Code: *env.Code, Message: "响应缺少必需 data 字段", RequestID: env.RequestID}
		}
		if err := json.Unmarshal(env.Data, out); err != nil {
			return retryAfter, fmt.Errorf("解析 123云盘 Web 数据失败: %w", err)
		}
	}
	return retryAfter, nil
}

type webFileWire struct {
	FileName     string    `json:"FileName"`
	Size         int64     `json:"Size"`
	UpdateAt     time.Time `json:"UpdateAt"`
	FileID       int64     `json:"FileId"`
	ParentFileID int64     `json:"ParentFileId"`
	Type         int       `json:"Type"`
	ETag         string    `json:"Etag"`
	S3KeyFlag    string    `json:"S3KeyFlag"`
	DownloadURL  string    `json:"DownloadUrl"`
}

func (w webFileWire) toEntry(fallbackParent int64) Entry {
	isDir := w.Type == 1
	sz := w.Size
	if isDir {
		sz = 0
	}
	pFID := w.ParentFileID
	if pFID == 0 && fallbackParent != 0 {
		pFID = fallbackParent
	}
	return Entry{
		FID:       w.FileID,
		ParentFID: pFID,
		Name:      w.FileName,
		IsDir:     isDir,
		Size:      sz,
		ModTime:   w.UpdateAt.UTC(),
		MD5:       normalizeMD5(w.ETag),
		S3KeyFlag: w.S3KeyFlag,
		Type:      w.Type,
	}
}

func (b *webBackend) About(ctx context.Context) (Quota, AccountInfo, error) {
	var data struct {
		UID            int64  `json:"UID"`
		Nickname       string `json:"Nickname"`
		SpaceUsed      int64  `json:"SpaceUsed"`
		SpacePermanent int64  `json:"SpacePermanent"`
		SpaceTemp      int64  `json:"SpaceTemp"`
	}
	if err := b.doJSON(ctx, http.MethodGet, webUserInfoPath, nil, nil, &data, true, false); err != nil {
		return Quota{}, AccountInfo{}, err
	}
	total := data.SpacePermanent + data.SpaceTemp
	free := total - data.SpaceUsed
	if free < 0 {
		free = 0
	}
	return Quota{
			Total: total,
			Used:  data.SpaceUsed,
			Free:  free,
		}, AccountInfo{
			UID:      data.UID,
			Nickname: strings.TrimSpace(data.Nickname),
		}, nil
}

// ListDir implements strict fail-closed pagination (adapted from rclone-123pan):
// Total drift, premature empty pages, duplicate IDs, or missing Next markers
// retry up to 3 full scans and fail closed instead of returning truncated data.
func (b *webBackend) ListDir(ctx context.Context, parentID int64) ([]Entry, error) {
	var lastErr error
	for attempt := 0; attempt < webListConsistencyTrials; attempt++ {
		entries, err := b.listDirOnce(ctx, parentID)
		if err == nil {
			return entries, nil
		}
		lastErr = err
		if !errors.Is(err, ErrInconsistentListing) || ctx.Err() != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func (b *webBackend) listDirOnce(ctx context.Context, parentID int64) ([]Entry, error) {
	var (
		lockedTotal  int64 = -1
		page               = 1
		previousNext string
		entries      []Entry
		seenIDs      = make(map[int64]struct{})
		seenNames    = make(map[string]int)
	)
	for {
		if page > webMaxListPages {
			return nil, fmt.Errorf("%w: 分页数超出安全上限", ErrInconsistentListing)
		}
		q := url.Values{
			"driveId":              {"0"},
			"limit":                {strconv.Itoa(webListPageSize)},
			"next":                 {"0"},
			"orderBy":              {"file_id"},
			"orderDirection":       {"desc"},
			"parentFileId":         {strconv.FormatInt(parentID, 10)},
			"trashed":              {"false"},
			"SearchData":           {""},
			"Page":                 {strconv.Itoa(page)},
			"OnlyLookAbnormalFile": {"0"},
			"event":                {"homeListFile"},
			"operateType":          {"4"},
			"inDirectSpace":        {"false"},
		}
		var data struct {
			Next     string        `json:"Next"`
			Total    int64         `json:"Total"`
			InfoList []webFileWire `json:"InfoList"`
		}
		if err := b.doJSON(ctx, http.MethodGet, webFileListPath, q, nil, &data, true, false); err != nil {
			return nil, err
		}
		if data.Total < 0 {
			return nil, fmt.Errorf("%w: 服务端返回负数 Total", ErrInconsistentListing)
		}
		if lockedTotal < 0 {
			lockedTotal = data.Total
		} else if data.Total != lockedTotal {
			return nil, fmt.Errorf("%w: 目录总数在翻页期间由 %d 变为 %d", ErrInconsistentListing, lockedTotal, data.Total)
		}
		if len(data.InfoList) == 0 && int64(len(entries)) < lockedTotal {
			return nil, fmt.Errorf("%w: 第 %d 页提前返回空列表（已收 %d/%d）", ErrInconsistentListing, page, len(entries), lockedTotal)
		}
		for _, item := range data.InfoList {
			if item.FileID <= 0 || item.Size < 0 || (item.Type != 0 && item.Type != 1) {
				return nil, fmt.Errorf("%w: 无效文件记录 FileId=%d", ErrInconsistentListing, item.FileID)
			}
			if _, dup := seenIDs[item.FileID]; dup {
				return nil, fmt.Errorf("%w: 出现重复 FileId=%d", ErrInconsistentListing, item.FileID)
			}
			seenIDs[item.FileID] = struct{}{}
			if validateEntryName(item.FileName) != nil {
				continue
			}
			e := item.toEntry(parentID)
			if prevIdx, dupName := seenNames[e.Name]; dupName {
				if e.ModTime.After(entries[prevIdx].ModTime) {
					entries[prevIdx] = e
				}
				continue
			}
			seenNames[e.Name] = len(entries)
			entries = append(entries, e)
		}
		if int64(len(seenIDs)) > lockedTotal {
			return nil, fmt.Errorf("%w: 收到 %d 条记录超过 Total=%d", ErrInconsistentListing, len(seenIDs), lockedTotal)
		}
		if data.Next == "-1" {
			if int64(len(seenIDs)) != lockedTotal {
				return nil, fmt.Errorf("%w: 结束时仅收到 %d/%d 条记录", ErrInconsistentListing, len(seenIDs), lockedTotal)
			}
			return entries, nil
		}
		if data.Next == "" || data.Next == previousNext {
			return nil, fmt.Errorf("%w: 游标 Next=%q 异常", ErrInconsistentListing, data.Next)
		}
		previousNext = data.Next
		page++
	}
}

func (b *webBackend) Mkdir(ctx context.Context, parentID int64, name string) (int64, error) {
	var data struct {
		FileID int64 `json:"FileId"`
	}
	err := b.doJSON(ctx, http.MethodPost, webUploadRequestPath, nil, map[string]any{
		"driveId":      0,
		"etag":         "",
		"fileName":     name,
		"parentFileId": parentID,
		"size":         0,
		"type":         1,
	}, &data, false, true)
	if err != nil {
		return 0, err
	}
	return data.FileID, nil
}

func (b *webBackend) Move(ctx context.Context, entry Entry, dstParentID int64) error {
	return b.doJSON(ctx, http.MethodPost, webMovePath, nil, map[string]any{
		"fileIdList":   []map[string]any{{"FileId": entry.FID}},
		"parentFileId": dstParentID,
	}, nil, false, true)
}

func (b *webBackend) Rename(ctx context.Context, entry Entry, newName string) error {
	return b.doJSON(ctx, http.MethodPost, webRenamePath, nil, map[string]any{
		"driveId":  0,
		"fileId":   entry.FID,
		"fileName": newName,
	}, nil, false, true)
}

func (b *webBackend) Delete(ctx context.Context, entry Entry) error {
	itemType := entry.Type
	if entry.IsDir {
		itemType = 1
	}
	return b.doJSON(ctx, http.MethodPost, webTrashPath, nil, map[string]any{
		"driveId":   0,
		"operation": true,
		"fileTrashInfoList": []map[string]any{{
			"FileId":       entry.FID,
			"FileName":     entry.Name,
			"Size":         entry.Size,
			"Type":         itemType,
			"Etag":         entry.MD5,
			"S3KeyFlag":    entry.S3KeyFlag,
			"ParentFileId": entry.ParentFID,
		}},
	}, nil, false, true)
}

func (b *webBackend) DownloadURL(ctx context.Context, entry Entry) (string, http.Header, time.Duration, error) {
	var data struct {
		DownloadURL string `json:"DownloadUrl"`
	}
	err := b.doJSON(ctx, http.MethodPost, webDownloadInfoPath, nil, map[string]any{
		"driveId":   0,
		"etag":      entry.MD5,
		"fileId":    entry.FID,
		"fileName":  entry.Name,
		"s3keyFlag": entry.S3KeyFlag,
		"size":      entry.Size,
		"type":      entry.Type,
	}, &data, true, false)
	if err != nil {
		return "", nil, 0, err
	}
	if strings.TrimSpace(data.DownloadURL) == "" {
		return "", nil, 0, errors.New("123云盘 Web 未返回下载地址")
	}
	firstURL, err := unwrapWebDownloadURL(data.DownloadURL)
	if err != nil {
		return "", nil, 0, err
	}
	finalURL, referer, err := b.resolveWebFinalDownloadURL(ctx, firstURL)
	if err != nil {
		return "", nil, 0, err
	}
	headers := http.Header{}
	if referer != "" {
		headers.Set("Referer", referer)
	}
	return finalURL.String(), headers, DefaultDownloadURLTTL, nil
}

func unwrapWebDownloadURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("123云盘下载链接格式无效")
	}
	if encoded := u.Query().Get("params"); encoded != "" {
		decoded, decErr := base64.StdEncoding.DecodeString(encoded)
		if decErr != nil {
			return nil, fmt.Errorf("解析 123云盘下载 params 失败: %w", decErr)
		}
		inner, parseErr := url.Parse(strings.TrimSpace(string(decoded)))
		if parseErr != nil || inner.Host == "" || (inner.Scheme != "http" && inner.Scheme != "https") {
			return nil, errors.New("123云盘解码后的下载链接无效")
		}
		return inner, nil
	}
	return u, nil
}

func (b *webBackend) resolveWebFinalDownloadURL(ctx context.Context, first *url.URL) (*url.URL, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, first.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Referer", b.origin+"/")
	req.Header.Set("User-Agent", b.userAgent)

	resp, err := b.noRedirClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	referer := first.Scheme + "://" + first.Host + "/"
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently || resp.StatusCode == http.StatusTemporaryRedirect {
		loc := strings.TrimSpace(resp.Header.Get("Location"))
		if loc == "" {
			return nil, "", errors.New("123云盘下载重定向缺少 Location")
		}
		ref, err := url.Parse(loc)
		if err != nil {
			return nil, "", fmt.Errorf("123云盘下载重定向地址无效: %w", err)
		}
		final := first.ResolveReference(ref)
		if final.Host == "" || (final.Scheme != "http" && final.Scheme != "https") {
			return nil, "", errors.New("123云盘最终下载链接无效")
		}
		return final, referer, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("解析 123云盘下载跳转失败: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, "", err
	}
	var payload struct {
		Data struct {
			RedirectURL string `json:"redirect_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && strings.TrimSpace(payload.Data.RedirectURL) != "" {
		final, err := url.Parse(strings.TrimSpace(payload.Data.RedirectURL))
		if err != nil || final.Host == "" {
			return nil, "", errors.New("123云盘 redirect_url 无效")
		}
		return final, referer, nil
	}
	// Some CDN links serve directly without an intermediate JSON redirect.
	return first, referer, nil
}

type webUploadData struct {
	FileID           int64        `json:"FileId"`
	Reuse            bool         `json:"Reuse"`
	Bucket           string       `json:"Bucket"`
	Key              string       `json:"Key"`
	StorageNode      string       `json:"StorageNode"`
	UploadID         string       `json:"UploadId"`
	SliceSize        string       `json:"SliceSize"`
	UploadFileStatus int          `json:"UploadFileStatus"`
	Info             *webFileWire `json:"Info,omitempty"`
}

type webResumeState struct {
	ParentID    int64   `json:"parent_id"`
	FileName    string  `json:"file_name"`
	Size        int64   `json:"size"`
	MD5         string  `json:"md5"`
	FileID      int64   `json:"file_id"`
	Bucket      string  `json:"bucket"`
	Key         string  `json:"key"`
	StorageNode string  `json:"storage_node"`
	UploadID    string  `json:"upload_id"`
	SliceSize   int64   `json:"slice_size"`
	DoneParts   []int64 `json:"done_parts"`
}

type webPresignedBatch struct {
	mu         sync.RWMutex
	urls       map[string]string
	refreshMu  sync.Mutex
	batchStart int64
	batchEnd   int64
	single     bool
	upload     webUploadData
	backend    *webBackend
}

func (pb *webPresignedBatch) get(partNo int64) string {
	pb.mu.RLock()
	defer pb.mu.RUnlock()
	return pb.urls[strconv.FormatInt(partNo, 10)]
}

func (pb *webPresignedBatch) refresh(ctx context.Context, partNo int64, failedURL string) error {
	if pb.get(partNo) != failedURL {
		return nil
	}
	pb.refreshMu.Lock()
	defer pb.refreshMu.Unlock()
	if pb.get(partNo) != failedURL {
		return nil
	}
	urls, err := pb.backend.fetchPresignedURLs(ctx, pb.upload, pb.batchStart, pb.batchEnd, pb.single)
	if err != nil {
		return err
	}
	pb.mu.Lock()
	pb.urls = urls
	pb.mu.Unlock()
	return nil
}

func (b *webBackend) UploadFile(ctx context.Context, parentID int64, name string, opt UploadFileOptions, _ *Entry) (*UploadResult, error) {
	md5Hex, err := hashFileMD5(ctx, opt.File, opt.Size, opt.WaitIfPaused)
	if err != nil {
		return nil, fmt.Errorf("计算本地文件摘要失败: %w", err)
	}

	var (
		upload    webUploadData
		sliceSize int64
		doneSet   = make(map[int64]bool)
		resumed   bool
	)

	if opt.ResumeStatePath != "" {
		var saved webResumeState
		if loadResumeJSON(opt.ResumeStatePath, &saved) &&
			saved.ParentID == parentID &&
			saved.FileName == name &&
			saved.Size == opt.Size &&
			saved.MD5 == md5Hex &&
			saved.FileID > 0 &&
			saved.Bucket != "" &&
			saved.Key != "" &&
			saved.StorageNode != "" &&
			saved.UploadID != "" &&
			saved.SliceSize > 0 {
			upload = webUploadData{
				FileID:      saved.FileID,
				Bucket:      saved.Bucket,
				Key:         saved.Key,
				StorageNode: saved.StorageNode,
				UploadID:    saved.UploadID,
				SliceSize:   strconv.FormatInt(saved.SliceSize, 10),
			}
			sliceSize = saved.SliceSize
			for _, no := range saved.DoneParts {
				doneSet[no] = true
			}
			resumed = true
		}
	}

	if !resumed {
		duplicate := 1
		if opt.Overwrite {
			duplicate = 2
		}
		err = b.doJSON(ctx, http.MethodPost, webUploadRequestPath, nil, map[string]any{
			"RequestSource": nil,
			"driveId":       0,
			"duplicate":     duplicate,
			"etag":          md5Hex,
			"fileName":      name,
			"parentFileId":  parentID,
			"size":          opt.Size,
			"type":          0,
		}, &upload, false, false)
		if err != nil {
			return nil, err
		}
		if upload.Reuse {
			verifiedID, vErr := b.verifyRapidUpload(ctx, parentID, name, upload, opt.Size, md5Hex)
			if vErr != nil {
				return nil, vErr
			}
			removeResumeJSON(opt.ResumeStatePath)
			if opt.OnProgress != nil {
				opt.OnProgress(opt.Size, 0, true)
			}
			return &UploadResult{
				FID:   verifiedID,
				Name:  name,
				Size:  opt.Size,
				MD5:   md5Hex,
				Rapid: true,
			}, nil
		}
		if upload.FileID <= 0 || upload.Bucket == "" || upload.Key == "" || upload.StorageNode == "" || upload.UploadID == "" {
			return nil, errors.New("123云盘 Web 预上传返回缺少必要分片参数")
		}
		sliceSize = parseWebSliceSize(upload.SliceSize)
	}

	parts := (opt.Size + sliceSize - 1) / sliceSize
	if parts == 0 {
		parts = 1
	}

	if upload.UploadFileStatus == 255 {
		fid, err := b.completeWebUpload(ctx, parentID, name, upload, opt.Size, md5Hex, parts > 1)
		if err != nil {
			return nil, err
		}
		removeResumeJSON(opt.ResumeStatePath)
		if opt.OnProgress != nil {
			opt.OnProgress(opt.Size, 0, false)
		}
		return &UploadResult{FID: fid, Name: name, Size: opt.Size, MD5: md5Hex}, nil
	}

	// Query server-side uploaded parts on multipart uploads (official Web flow).
	if parts > 1 {
		if serverParts, err := b.listWebUploadedParts(ctx, upload); err == nil {
			for _, p := range serverParts {
				if p.PartNumber > 0 {
					expectedLen := min(sliceSize, max(opt.Size-(p.PartNumber-1)*sliceSize, 0))
					if p.Size == 0 || p.Size == expectedLen {
						doneSet[p.PartNumber] = true
					}
				}
			}
		}
	}

	var initialDoneBytes int64
	for no := int64(1); no <= parts; no++ {
		if doneSet[no] {
			initialDoneBytes += min(sliceSize, max(opt.Size-(no-1)*sliceSize, 0))
		}
	}
	tracker := newUploadProgressTracker(opt.Size, initialDoneBytes, opt.OnProgress)

	var stateMu sync.Mutex
	persistState := func() {
		if opt.ResumeStatePath == "" {
			return
		}
		stateMu.Lock()
		defer stateMu.Unlock()
		doneList := make([]int64, 0, len(doneSet))
		for no := range doneSet {
			doneList = append(doneList, no)
		}
		sort.Slice(doneList, func(i, j int) bool { return doneList[i] < doneList[j] })
		saveResumeJSON(opt.ResumeStatePath, webResumeState{
			ParentID:    parentID,
			FileName:    name,
			Size:        opt.Size,
			MD5:         md5Hex,
			FileID:      upload.FileID,
			Bucket:      upload.Bucket,
			Key:         upload.Key,
			StorageNode: upload.StorageNode,
			UploadID:    upload.UploadID,
			SliceSize:   sliceSize,
			DoneParts:   doneList,
		})
	}
	persistState()

	conc := opt.Concurrency
	if conc <= 0 {
		conc = b.concurrency
	}
	if conc < 1 {
		conc = 1
	}

	single := parts == 1
	batchStep := webUploadBatchSize
	if single {
		batchStep = 1
	}

	for start := int64(1); start <= parts; start += batchStep {
		end := min(start+batchStep-1, parts)
		pending := make([]int64, 0, end-start+1)
		for no := start; no <= end; no++ {
			if !doneSet[no] {
				pending = append(pending, no)
			}
		}
		if len(pending) == 0 {
			continue
		}

		urls, err := b.fetchPresignedURLs(ctx, upload, start, end, single)
		if err != nil {
			if isExpiredUploadSession(err) {
				removeResumeJSON(opt.ResumeStatePath)
			}
			return nil, err
		}
		batch := &webPresignedBatch{
			urls:       urls,
			batchStart: start,
			batchEnd:   end,
			single:     single,
			upload:     upload,
			backend:    b,
		}

		batchCtx, cancel := context.WithCancel(ctx)
		sem := make(chan struct{}, min(conc, len(pending)))
		var (
			wg       sync.WaitGroup
			errMu    sync.Mutex
			firstErr error
		)
	scheduleParts:
		for _, no := range pending {
			select {
			case sem <- struct{}{}:
			case <-batchCtx.Done():
				break scheduleParts
			}
			if batchCtx.Err() != nil {
				<-sem
				break
			}
			wg.Add(1)
			go func(partNo int64) {
				defer wg.Done()
				defer func() { <-sem }()
				offset := (partNo - 1) * sliceSize
				length := min(sliceSize, max(opt.Size-offset, 0))
				if err := b.putPresignedPart(batchCtx, batch, partNo, opt.File, offset, length, opt, tracker); err != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("上传分片 %d 失败: %w", partNo, err)
					}
					errMu.Unlock()
					cancel()
					return
				}
				stateMu.Lock()
				doneSet[partNo] = true
				stateMu.Unlock()
				persistState()
			}(no)
		}
		wg.Wait()
		cancel()
		if firstErr != nil {
			if isExpiredUploadSession(firstErr) {
				removeResumeJSON(opt.ResumeStatePath)
			}
			return nil, firstErr
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}

	fid, err := b.completeWebUpload(ctx, parentID, name, upload, opt.Size, md5Hex, parts > 1)
	if err != nil {
		if isExpiredUploadSession(err) {
			removeResumeJSON(opt.ResumeStatePath)
		}
		return nil, err
	}
	removeResumeJSON(opt.ResumeStatePath)
	if opt.OnProgress != nil {
		opt.OnProgress(opt.Size, 0, false)
	}
	return &UploadResult{
		FID:   fid,
		Name:  name,
		Size:  opt.Size,
		MD5:   md5Hex,
		Rapid: false,
	}, nil
}

func (b *webBackend) verifyRapidUpload(ctx context.Context, parentID int64, name string, upload webUploadData, size int64, md5Hex string) (int64, error) {
	if upload.Info != nil && upload.Info.FileID > 0 {
		info := upload.Info
		if info.FileName == name && info.Size == size && (normalizeMD5(info.ETag) == "" || normalizeMD5(info.ETag) == md5Hex) && info.Type == 0 {
			return info.FileID, nil
		}
	}
	if upload.FileID > 0 {
		return upload.FileID, nil
	}
	// Official Web API can return Reuse=true with FileId=0 after materializing
	// the file; confirm visibility via a consistency-checked parent listing.
	entries, err := b.ListDir(ctx, parentID)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		if e.Name == name && !e.IsDir && e.Size == size && (e.MD5 == "" || e.MD5 == md5Hex) && e.FID > 0 {
			return e.FID, nil
		}
	}
	return 0, errors.New("123云盘秒传返回成功但未在目录中核验到目标文件")
}

type webUploadedPart struct {
	PartNumber int64 `json:"PartNumber"`
	Size       int64 `json:"Size"`
}

func (b *webBackend) listWebUploadedParts(ctx context.Context, upload webUploadData) ([]webUploadedPart, error) {
	var data struct {
		Parts []webUploadedPart `json:"Parts"`
	}
	err := b.doJSON(ctx, http.MethodPost, webListUploadParts, nil, map[string]any{
		"StorageNode": upload.StorageNode,
		"bucket":      upload.Bucket,
		"key":         upload.Key,
		"uploadId":    upload.UploadID,
	}, &data, true, true)
	if err != nil {
		return nil, err
	}
	return data.Parts, nil
}

func (b *webBackend) fetchPresignedURLs(ctx context.Context, upload webUploadData, start, end int64, single bool) (map[string]string, error) {
	endpoint := webPresignedParts
	if single {
		endpoint = webSingleAuthPath
	}
	var data struct {
		PresignedURLs map[string]string `json:"presignedUrls"`
	}
	err := b.doJSON(ctx, http.MethodPost, endpoint, nil, map[string]any{
		"StorageNode":     upload.StorageNode,
		"bucket":          upload.Bucket,
		"key":             upload.Key,
		"partNumberStart": start,
		"partNumberEnd":   end + 1, // exclusive upper bound
		"uploadId":        upload.UploadID,
	}, &data, true, false)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, end-start+1)
	for no := start; no <= end; no++ {
		key := strconv.FormatInt(no, 10)
		raw := strings.TrimSpace(data.PresignedURLs[key])
		u, pErr := url.Parse(raw)
		if pErr != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return nil, fmt.Errorf("123云盘未返回分片 %d 的有效预签上传地址", no)
		}
		out[key] = u.String()
	}
	return out, nil
}

func (b *webBackend) putPresignedPart(
	ctx context.Context,
	batch *webPresignedBatch,
	partNo int64,
	f io.ReaderAt,
	offset, length int64,
	opt UploadFileOptions,
	tracker *uploadProgressTracker,
) error {
	var lastErr error
	for attempt := 0; attempt < webMaxRetries; attempt++ {
		rawURL := batch.get(partNo)
		pr := &pacedReader{
			ctx:           ctx,
			r:             io.NewSectionReader(f, offset, length),
			waitIfPaused:  opt.WaitIfPaused,
			throttleBytes: opt.ThrottleBytes,
			tracker:       tracker,
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, rawURL, pr)
		if err != nil {
			return err
		}
		req.ContentLength = length
		req.Header.Set("Referer", b.origin+"/")
		req.Header.Set("User-Agent", b.userAgent)
		// Intentionally omit Authorization header on data-plane presigned PUT.

		resp, err := b.streamClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			pr.rollback()
			lastErr = fmt.Errorf("预签上传返回 HTTP %d", resp.StatusCode)
			if resp.StatusCode == http.StatusForbidden {
				if rErr := batch.refresh(ctx, partNo, rawURL); rErr != nil {
					return fmt.Errorf("刷新分片 %d 预签地址失败: %w", partNo, rErr)
				}
				continue
			}
			if !isRetryableHTTPStatus(resp.StatusCode) {
				return lastErr
			}
			if attempt < webMaxRetries-1 {
				if sErr := sleepContext(ctx, retryBackoff(attempt, resp.Header.Get("Retry-After"))); sErr != nil {
					return sErr
				}
			}
			continue
		}
		pr.rollback()
		lastErr = err
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt < webMaxRetries-1 {
			if sErr := sleepContext(ctx, retryBackoff(attempt, "")); sErr != nil {
				return sErr
			}
		}
	}
	return lastErr
}

func (b *webBackend) completeWebUpload(ctx context.Context, parentID int64, name string, upload webUploadData, size int64, md5Hex string, isMultipart bool) (int64, error) {
	var data struct {
		FileInfo *webFileWire `json:"file_info"`
	}
	err := b.doJSON(ctx, http.MethodPost, webUploadCompleteV2, nil, map[string]any{
		"StorageNode": upload.StorageNode,
		"bucket":      upload.Bucket,
		"fileId":      upload.FileID,
		"fileSize":    size,
		"isMultipart": isMultipart,
		"key":         upload.Key,
		"uploadId":    upload.UploadID,
	}, &data, false, true)
	if err != nil {
		return 0, err
	}
	if data.FileInfo != nil && data.FileInfo.FileID > 0 {
		return data.FileInfo.FileID, nil
	}
	// Fallback verification if complete returned empty data.
	if upload.FileID > 0 {
		return upload.FileID, nil
	}
	entries, listErr := b.ListDir(ctx, parentID)
	if listErr == nil {
		for _, e := range entries {
			if e.Name == name && !e.IsDir && e.Size == size && (e.MD5 == "" || e.MD5 == md5Hex) && e.FID > 0 {
				return e.FID, nil
			}
		}
	}
	return 0, errors.New("123云盘 Web 完成上传后未确认到目标文件")
}

func parseWebSliceSize(raw string) int64 {
	if n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64); err == nil && n > 0 {
		return n
	}
	return webDefaultSliceSize
}
