package pan123

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	openAPIPlatformHeader   = "open_platform"
	openAPISingleFastCutoff = 1 << 20 // <= 1 MiB uses single-step upload directly
	openAPIMaxRetries       = 3
	openAPICompleteMaxPolls = 120
)

var (
	cstZone = time.FixedZone("UTC+8", 8*3600)

	// Conservative practical QPS limits combining official OpenAPI docs and
	// OpenList production experience to prevent 429 bursts.
	openAPIDefaultQPS = map[string]float64{
		"/api/v1/access_token":       2,
		"/api/v1/user/info":          2,
		"/api/v2/file/list":          4,
		"/api/v1/file/download_info": 5,
		"/api/v1/direct-link/url":    5,
		"/upload/v1/file/mkdir":      3,
		"/api/v1/file/move":          2,
		"/api/v1/file/name":          2,
		"/api/v1/file/trash":         2,
		"/upload/v2/file/create":     2,
		"/upload/v2/file/domain":     2,
	}
)

type openAPIBackend struct {
	baseURL       string
	clientID      string
	clientSecret  string
	useDirectLink bool
	directLinkKey string
	directLinkTTL time.Duration
	userAgent     string
	concurrency   int

	httpClient    *http.Client
	streamClient  *http.Client
	limiter       *endpointLimiter
	onTokenUpdate func(string, time.Time)

	tokenMu        sync.RWMutex
	token          string
	tokenExpiresAt time.Time
	refreshMu      sync.Mutex

	uidMu     sync.RWMutex
	cachedUID int64

	domainMu        sync.Mutex
	cachedDomains   []string
	domainsExpireAt time.Time
}

func newOpenAPIBackend(opts Options, ua string, conc int, httpClient, streamClient *http.Client) (*openAPIBackend, error) {
	clientID := strings.TrimSpace(opts.ClientID)
	clientSecret := strings.TrimSpace(opts.ClientSecret)
	accessToken := strings.TrimSpace(opts.AccessToken)
	if accessToken == "" && (clientID == "" || clientSecret == "") {
		return nil, errors.New("123云盘开放平台模式需要填写 Client ID + Client Secret，或直接提供 Access Token")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(opts.OpenAPIBaseURL), "/")
	if baseURL == "" {
		baseURL = DefaultOpenAPIBaseURL
	}
	ttlMin := opts.DirectLinkTTL
	if ttlMin <= 0 {
		ttlMin = DefaultDirectLinkTTL
	}
	return &openAPIBackend{
		baseURL:        baseURL,
		clientID:       clientID,
		clientSecret:   clientSecret,
		useDirectLink:  opts.UseDirectLink,
		directLinkKey:  strings.TrimSpace(opts.DirectLinkKey),
		directLinkTTL:  time.Duration(ttlMin) * time.Minute,
		userAgent:      ua,
		concurrency:    conc,
		httpClient:     httpClient,
		streamClient:   streamClient,
		limiter:        newEndpointLimiter(opts.TPSLimit, openAPIDefaultQPS, 4),
		onTokenUpdate:  opts.OnTokenUpdate,
		token:          accessToken,
		tokenExpiresAt: opts.TokenExpiresAt,
	}, nil
}

type openAPIEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
	TraceID string          `json:"x-traceID"`
}

type openAPIError struct {
	HTTPStatus int
	Code       int
	Message    string
	TraceID    string
}

func (e *openAPIError) Error() string {
	msg := scrubSecrets(strings.TrimSpace(e.Message))
	if msg == "" {
		msg = fmt.Sprintf("code %d", e.Code)
	}
	if e.TraceID != "" {
		return fmt.Sprintf("123云盘开放平台错误 (code=%d, trace=%s): %s", e.Code, e.TraceID, msg)
	}
	return fmt.Sprintf("123云盘开放平台错误 (code=%d): %s", e.Code, msg)
}

func (e *openAPIError) isUnauthorized() bool {
	return e.HTTPStatus == http.StatusUnauthorized || e.Code == 401
}

func (e *openAPIError) isRateLimited() bool {
	return e.HTTPStatus == http.StatusTooManyRequests || e.Code == 429
}

func (b *openAPIBackend) canRefreshToken() bool {
	return b.clientID != "" && b.clientSecret != ""
}

func (b *openAPIBackend) getToken(ctx context.Context, forceRefresh bool, failedToken string) (string, error) {
	if !forceRefresh {
		b.tokenMu.RLock()
		tok := b.token
		exp := b.tokenExpiresAt
		b.tokenMu.RUnlock()
		if tok != "" {
			if !b.canRefreshToken() || exp.IsZero() || time.Until(exp) > TokenSafetyMargin {
				return tok, nil
			}
		}
	}
	if !b.canRefreshToken() {
		if forceRefresh {
			return "", ErrUnauthorized
		}
		b.tokenMu.RLock()
		tok := b.token
		b.tokenMu.RUnlock()
		if tok == "" {
			return "", ErrUnauthorized
		}
		return tok, nil
	}

	b.refreshMu.Lock()
	defer b.refreshMu.Unlock()

	b.tokenMu.RLock()
	curTok := b.token
	curExp := b.tokenExpiresAt
	b.tokenMu.RUnlock()
	if curTok != "" && curTok != failedToken {
		if !forceRefresh || (failedToken != "" && curTok != failedToken) {
			if curExp.IsZero() || time.Until(curExp) > TokenSafetyMargin {
				return curTok, nil
			}
		}
	}

	var out struct {
		AccessToken string `json:"accessToken"`
		ExpiredAt   string `json:"expiredAt"`
	}
	err := b.doJSONOnce(ctx, http.MethodPost, "/api/v1/access_token", nil, map[string]string{
		"clientID":     b.clientID,
		"clientSecret": b.clientSecret,
	}, &out, false, "")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return "", fmt.Errorf("%w: 开放平台未返回有效的 accessToken", ErrUnauthorized)
	}
	exp := parseOpenAPITime(out.ExpiredAt)
	if exp.IsZero() {
		// OpenAPI tokens typically remain valid for ~30 days; fall back to a
		// conservative 24h refresh horizon if expiredAt is unparseable.
		exp = time.Now().Add(24 * time.Hour)
	}

	b.tokenMu.Lock()
	b.token = out.AccessToken
	b.tokenExpiresAt = exp
	b.tokenMu.Unlock()

	if b.onTokenUpdate != nil {
		b.onTokenUpdate(out.AccessToken, exp)
	}
	return out.AccessToken, nil
}

func (b *openAPIBackend) doJSON(ctx context.Context, method, apiPath string, query url.Values, body any, out any, retryable bool) error {
	refreshedAuth := false
	for attempt := 0; ; attempt++ {
		tok, err := b.getToken(ctx, false, "")
		if err != nil {
			return err
		}
		err = b.doJSONOnce(ctx, method, apiPath, query, body, out, true, tok)
		if err == nil {
			return nil
		}
		var apiErr *openAPIError
		if errors.As(err, &apiErr) {
			if apiErr.isUnauthorized() {
				if !refreshedAuth && b.canRefreshToken() {
					refreshedAuth = true
					if _, rErr := b.getToken(ctx, true, tok); rErr != nil {
						return fmt.Errorf("%w: %v", ErrUnauthorized, rErr)
					}
					continue
				}
				return fmt.Errorf("%w: %v", ErrUnauthorized, apiErr)
			}
			if apiErr.isRateLimited() {
				if attempt < openAPIMaxRetries {
					if sErr := sleepContext(ctx, retryBackoff(attempt, "")); sErr != nil {
						return sErr
					}
					continue
				}
				return fmt.Errorf("%w: %v", ErrRateLimited, apiErr)
			}
			if retryable && isRetryableHTTPStatus(apiErr.HTTPStatus) && attempt < openAPIMaxRetries {
				if sErr := sleepContext(ctx, retryBackoff(attempt, "")); sErr != nil {
					return sErr
				}
				continue
			}
			return apiErr
		}
		if retryable && ctx.Err() == nil && attempt < openAPIMaxRetries {
			if sErr := sleepContext(ctx, retryBackoff(attempt, "")); sErr != nil {
				return sErr
			}
			continue
		}
		return err
	}
}

func (b *openAPIBackend) doJSONOnce(ctx context.Context, method, apiPath string, query url.Values, body any, out any, withAuth bool, token string) error {
	if err := b.limiter.Wait(ctx, apiPath); err != nil {
		return err
	}
	fullURL := b.baseURL + apiPath
	if len(query) > 0 {
		fullURL += "?" + query.Encode()
	}
	var reqBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, fullURL, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Platform", openAPIPlatformHeader)
	req.Header.Set("User-Agent", b.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if withAuth {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	rawResp, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return &openAPIError{HTTPStatus: resp.StatusCode, Code: 429, Message: "请求过于频繁"}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return &openAPIError{HTTPStatus: resp.StatusCode, Code: 401, Message: "access_token 无效或已过期"}
	}
	var env openAPIEnvelope
	if err := json.Unmarshal(rawResp, &env); err != nil {
		if resp.StatusCode >= 300 {
			return &openAPIError{HTTPStatus: resp.StatusCode, Code: resp.StatusCode, Message: string(rawResp)}
		}
		return fmt.Errorf("解析 123云盘开放平台响应失败: %w", err)
	}
	if env.Code != 0 {
		return &openAPIError{
			HTTPStatus: resp.StatusCode,
			Code:       env.Code,
			Message:    env.Message,
			TraceID:    env.TraceID,
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &openAPIError{HTTPStatus: resp.StatusCode, Code: resp.StatusCode, Message: env.Message, TraceID: env.TraceID}
	}
	if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("解析 123云盘开放平台数据失败: %w", err)
		}
	}
	return nil
}

func (b *openAPIBackend) About(ctx context.Context) (Quota, AccountInfo, error) {
	var data struct {
		UID            int64  `json:"uid"`
		Nickname       string `json:"nickname"`
		SpaceUsed      int64  `json:"spaceUsed"`
		SpacePermanent int64  `json:"spacePermanent"`
		SpaceTemp      int64  `json:"spaceTemp"`
	}
	if err := b.doJSON(ctx, http.MethodGet, "/api/v1/user/info", nil, nil, &data, true); err != nil {
		return Quota{}, AccountInfo{}, err
	}
	if data.UID > 0 {
		b.uidMu.Lock()
		b.cachedUID = data.UID
		b.uidMu.Unlock()
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

func (b *openAPIBackend) listDirPage(ctx context.Context, parentID, lastFileID int64) ([]Entry, int64, error) {
	const pageSize = 100
	q := url.Values{
		"parentFileId": {strconv.FormatInt(parentID, 10)},
		"limit":        {strconv.Itoa(pageSize)},
	}
	if lastFileID > 0 {
		q.Set("lastFileId", strconv.FormatInt(lastFileID, 10))
	}
	var data struct {
		LastFileID int64 `json:"lastFileId"`
		FileList   []struct {
			FileID       int64  `json:"fileId"`
			Filename     string `json:"filename"`
			Type         int    `json:"type"`
			Size         int64  `json:"size"`
			Etag         string `json:"etag"`
			Status       int    `json:"status"`
			ParentFileID int64  `json:"parentFileId"`
			Trashed      int    `json:"trashed"`
			CreateAt     string `json:"createAt"`
			UpdateAt     string `json:"updateAt"`
		} `json:"fileList"`
	}
	if err := b.doJSON(ctx, http.MethodGet, "/api/v2/file/list", q, nil, &data, true); err != nil {
		return nil, 0, err
	}
	entries := make([]Entry, 0, len(data.FileList))
	for _, item := range data.FileList {
		// OpenAPI trashed query param is unreliable on 123Pan; filter client-side.
		if item.Trashed != 0 {
			continue
		}
		if item.FileID <= 0 || validateEntryName(item.Filename) != nil {
			continue
		}
		isDir := item.Type == 1
		sz := item.Size
		if isDir {
			sz = 0
		}
		mt := parseOpenAPITime(item.UpdateAt)
		if mt.IsZero() {
			mt = parseOpenAPITime(item.CreateAt)
		}
		pFID := item.ParentFileID
		if pFID == 0 && parentID != 0 {
			pFID = parentID
		}
		e := Entry{
			FID:       item.FileID,
			ParentFID: pFID,
			Name:      item.Filename,
			IsDir:     isDir,
			Size:      sz,
			ModTime:   mt,
			MD5:       normalizeMD5(item.Etag),
			Type:      item.Type,
		}
		entries = append(entries, e)
	}
	if data.LastFileID == -1 || (lastFileID == 0 && data.LastFileID == 0 && len(data.FileList) == 0) {
		return entries, 0, nil
	}
	if len(data.FileList) == 0 {
		return nil, 0, fmt.Errorf("%w: OpenAPI 在分页结束前返回空页（游标 %d）", ErrInconsistentListing, data.LastFileID)
	}
	if data.LastFileID <= 0 || data.LastFileID == lastFileID {
		return nil, 0, fmt.Errorf("%w: OpenAPI 分页游标停滞于 %d", ErrInconsistentListing, lastFileID)
	}
	return entries, data.LastFileID, nil
}

func (b *openAPIBackend) ListDir(ctx context.Context, parentID int64) ([]Entry, error) {
	var all []Entry
	var lastFileID int64
	seenIDs := make(map[int64]struct{})
	seenNames := make(map[string]int)
	for page := 0; page < 20000; page++ {
		entries, next, err := b.listDirPage(ctx, parentID, lastFileID)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if _, dup := seenIDs[e.FID]; dup {
				continue
			}
			seenIDs[e.FID] = struct{}{}
			if prevIdx, dupName := seenNames[e.Name]; dupName {
				if e.ModTime.After(all[prevIdx].ModTime) {
					all[prevIdx] = e
				}
				continue
			}
			seenNames[e.Name] = len(all)
			all = append(all, e)
		}
		if next == 0 {
			return all, nil
		}
		lastFileID = next
	}
	return nil, fmt.Errorf("%w: OpenAPI 目录分页超过上限", ErrInconsistentListing)
}

func (b *openAPIBackend) Mkdir(ctx context.Context, parentID int64, name string) (int64, error) {
	var data struct {
		DirID int64 `json:"dirID"`
	}
	err := b.doJSON(ctx, http.MethodPost, "/upload/v1/file/mkdir", nil, map[string]any{
		"name":     name,
		"parentID": parentID,
	}, &data, false)
	if err != nil {
		return 0, err
	}
	return data.DirID, nil
}

func (b *openAPIBackend) Move(ctx context.Context, entry Entry, dstParentID int64) error {
	return b.doJSON(ctx, http.MethodPost, "/api/v1/file/move", nil, map[string]any{
		"fileIDs":        []int64{entry.FID},
		"toParentFileID": dstParentID,
	}, nil, false)
}

func (b *openAPIBackend) Rename(ctx context.Context, entry Entry, newName string) error {
	return b.doJSON(ctx, http.MethodPut, "/api/v1/file/name", nil, map[string]any{
		"fileId":   entry.FID,
		"fileName": newName,
	}, nil, false)
}

func (b *openAPIBackend) Delete(ctx context.Context, entry Entry) error {
	return b.doJSON(ctx, http.MethodPost, "/api/v1/file/trash", nil, map[string]any{
		"fileIDs": []int64{entry.FID},
	}, nil, false)
}

func (b *openAPIBackend) DownloadURL(ctx context.Context, entry Entry) (string, http.Header, time.Duration, error) {
	if b.useDirectLink {
		q := url.Values{"fileID": {strconv.FormatInt(entry.FID, 10)}}
		var dl struct {
			URL string `json:"url"`
		}
		if err := b.doJSON(ctx, http.MethodGet, "/api/v1/direct-link/url", q, nil, &dl, true); err == nil && strings.TrimSpace(dl.URL) != "" {
			rawURL := strings.TrimSpace(dl.URL)
			if b.directLinkKey == "" {
				return rawURL, nil, 30 * time.Minute, nil
			}
			uid, uidErr := b.ensureUID(ctx)
			if uidErr == nil && uid > 0 {
				signed, signErr := signDirectLinkURL(rawURL, b.directLinkKey, uid, b.directLinkTTL)
				if signErr == nil {
					ttl := b.directLinkTTL - time.Minute
					if ttl < time.Minute {
						ttl = time.Minute
					}
					return signed, nil, ttl, nil
				}
			}
		}
	}

	q := url.Values{"fileId": {strconv.FormatInt(entry.FID, 10)}}
	var data struct {
		DownloadURL string `json:"downloadUrl"`
	}
	if err := b.doJSON(ctx, http.MethodGet, "/api/v1/file/download_info", q, nil, &data, true); err != nil {
		return "", nil, 0, err
	}
	if strings.TrimSpace(data.DownloadURL) == "" {
		return "", nil, 0, errors.New("123云盘开放平台未返回有效下载地址")
	}
	return strings.TrimSpace(data.DownloadURL), nil, DefaultDownloadURLTTL, nil
}

func (b *openAPIBackend) ensureUID(ctx context.Context) (int64, error) {
	b.uidMu.RLock()
	uid := b.cachedUID
	b.uidMu.RUnlock()
	if uid > 0 {
		return uid, nil
	}
	_, info, err := b.About(ctx)
	if err != nil {
		return 0, err
	}
	return info.UID, nil
}

// signDirectLinkURL signs a 123Pan direct link with auth_key=ts-rand-uid-md5.
func signDirectLinkURL(rawURL, privateKey string, uid int64, validDuration time.Duration) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("无效的直链地址: %w", err)
	}
	if validDuration <= 0 {
		validDuration = DefaultDirectLinkTTL * time.Minute
	}
	ts := time.Now().Add(validDuration).Unix()
	var rb [16]byte
	if _, err := rand.Read(rb[:]); err != nil {
		return "", err
	}
	randHex := hex.EncodeToString(rb[:])
	payload := fmt.Sprintf("%s-%d-%s-%d-%s", u.Path, ts, randHex, uid, privateKey)
	sum := md5.Sum([]byte(payload))
	authKey := fmt.Sprintf("%d-%s-%d-%s", ts, randHex, uid, hex.EncodeToString(sum[:]))
	q := u.Query()
	q.Set("auth_key", authKey)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func (b *openAPIBackend) uploadDomains(ctx context.Context) ([]string, error) {
	b.domainMu.Lock()
	if len(b.cachedDomains) > 0 && time.Now().Before(b.domainsExpireAt) {
		out := append([]string(nil), b.cachedDomains...)
		b.domainMu.Unlock()
		return out, nil
	}
	b.domainMu.Unlock()

	var domains []string
	if err := b.doJSON(ctx, http.MethodGet, "/upload/v2/file/domain", nil, nil, &domains, true); err != nil {
		return nil, err
	}
	cleaned := make([]string, 0, len(domains))
	for _, d := range domains {
		d = strings.TrimRight(strings.TrimSpace(d), "/")
		if d != "" {
			cleaned = append(cleaned, d)
		}
	}
	if len(cleaned) == 0 {
		return nil, errors.New("123云盘未返回上传域名")
	}
	b.domainMu.Lock()
	b.cachedDomains = cleaned
	b.domainsExpireAt = time.Now().Add(10 * time.Minute)
	b.domainMu.Unlock()
	return append([]string(nil), cleaned...), nil
}

type openAPIResumeState struct {
	ParentID    int64    `json:"parent_id"`
	FileName    string   `json:"file_name"`
	Size        int64    `json:"size"`
	MD5         string   `json:"md5"`
	PreuploadID string   `json:"preupload_id"`
	SliceSize   int64    `json:"slice_size"`
	Servers     []string `json:"servers"`
	DoneSlices  []int64  `json:"done_slices"`
}

func (b *openAPIBackend) UploadFile(ctx context.Context, parentID int64, name string, opt UploadFileOptions, _ *Entry) (*UploadResult, error) {
	md5Hex, err := hashFileMD5(ctx, opt.File, opt.Size, opt.WaitIfPaused)
	if err != nil {
		return nil, fmt.Errorf("计算本地文件摘要失败: %w", err)
	}

	// Fast path for tiny files (<= 1 MiB): single-step upload avoids create + slice + complete RTTs.
	if opt.Size > 0 && opt.Size <= openAPISingleFastCutoff {
		if res, ok := b.trySingleUpload(ctx, parentID, name, md5Hex, opt); ok {
			removeResumeJSON(opt.ResumeStatePath)
			return res, nil
		}
	}

	duplicate := 1
	if opt.Overwrite {
		duplicate = 2
	}

	// Check if a persisted multipart upload session matches this exact file.
	var (
		preuploadID string
		sliceSize   int64
		servers     []string
		doneSet     = make(map[int64]bool)
		resumed     bool
	)
	if opt.ResumeStatePath != "" {
		var saved openAPIResumeState
		if loadResumeJSON(opt.ResumeStatePath, &saved) &&
			saved.ParentID == parentID &&
			saved.FileName == name &&
			saved.Size == opt.Size &&
			saved.MD5 == md5Hex &&
			saved.PreuploadID != "" &&
			saved.SliceSize > 0 &&
			len(saved.Servers) > 0 {
			preuploadID = saved.PreuploadID
			sliceSize = saved.SliceSize
			servers = saved.Servers
			for _, no := range saved.DoneSlices {
				doneSet[no] = true
			}
			resumed = true
		}
	}

	if !resumed {
		var created struct {
			FileID      int64    `json:"fileID"`
			PreuploadID string   `json:"preuploadID"`
			Reuse       bool     `json:"reuse"`
			SliceSize   int64    `json:"sliceSize"`
			Servers     []string `json:"servers"`
		}
		err = b.doJSON(ctx, http.MethodPost, "/upload/v2/file/create", nil, map[string]any{
			"parentFileID": parentID,
			"filename":     name,
			"etag":         md5Hex,
			"size":         opt.Size,
			"duplicate":    duplicate,
		}, &created, false)
		if err != nil {
			return nil, err
		}
		if created.Reuse && created.FileID > 0 {
			removeResumeJSON(opt.ResumeStatePath)
			if opt.OnProgress != nil {
				opt.OnProgress(opt.Size, 0, true)
			}
			return &UploadResult{
				FID:   created.FileID,
				Name:  name,
				Size:  opt.Size,
				MD5:   md5Hex,
				Rapid: true,
			}, nil
		}
		if created.PreuploadID == "" || created.SliceSize <= 0 || len(created.Servers) == 0 {
			return nil, errors.New("123云盘开放平台预上传返回不完整")
		}
		preuploadID = created.PreuploadID
		sliceSize = created.SliceSize
		for _, s := range created.Servers {
			s = strings.TrimRight(strings.TrimSpace(s), "/")
			if s != "" {
				servers = append(servers, s)
			}
		}
		if len(servers) == 0 {
			return nil, errors.New("123云盘开放平台未返回有效上传节点")
		}
	}

	numSlices := (opt.Size + sliceSize - 1) / sliceSize
	if numSlices == 0 {
		numSlices = 1
	}

	var initialDoneBytes int64
	var pending []int64
	for no := int64(1); no <= numSlices; no++ {
		if doneSet[no] {
			offset := (no - 1) * sliceSize
			initialDoneBytes += min(sliceSize, max(opt.Size-offset, 0))
		} else {
			pending = append(pending, no)
		}
	}
	tracker := newUploadProgressTracker(opt.Size, initialDoneBytes, opt.OnProgress)

	var (
		stateMu  sync.Mutex
		errMu    sync.Mutex
		firstErr error
	)
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
		saveResumeJSON(opt.ResumeStatePath, openAPIResumeState{
			ParentID:    parentID,
			FileName:    name,
			Size:        opt.Size,
			MD5:         md5Hex,
			PreuploadID: preuploadID,
			SliceSize:   sliceSize,
			Servers:     servers,
			DoneSlices:  doneList,
		})
	}
	persistState()

	conc := opt.Concurrency
	if conc <= 0 {
		conc = b.concurrency
	}
	if int64(conc) > numSlices {
		conc = int(numSlices)
	}
	if conc < 1 {
		conc = 1
	}

	uploadCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
scheduleSlices:
	for _, no := range pending {
		select {
		case sem <- struct{}{}:
		case <-uploadCtx.Done():
			break scheduleSlices
		}
		if uploadCtx.Err() != nil {
			<-sem
			break
		}
		wg.Add(1)
		go func(sliceNo int64) {
			defer wg.Done()
			defer func() { <-sem }()
			offset := (sliceNo - 1) * sliceSize
			length := min(sliceSize, max(opt.Size-offset, 0))
			if err := b.uploadSliceWithRetry(uploadCtx, servers, preuploadID, sliceNo, opt.File, offset, length, opt, tracker); err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("上传分片 %d 失败: %w", sliceNo, err)
				}
				errMu.Unlock()
				cancel()
				return
			}
			stateMu.Lock()
			doneSet[sliceNo] = true
			stateMu.Unlock()
			persistState()
		}(no)
	}
	wg.Wait()
	if firstErr != nil {
		// If a resumed session became invalid on the server, clear stale state so the next retry starts fresh.
		if isExpiredUploadSession(firstErr) {
			removeResumeJSON(opt.ResumeStatePath)
		}
		return nil, firstErr
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// Poll /upload/v2/file/upload_complete until merged.
	for poll := 0; poll < openAPICompleteMaxPolls; poll++ {
		var comp struct {
			Completed bool  `json:"completed"`
			FileID    int64 `json:"fileID"`
		}
		if err := b.doJSON(ctx, http.MethodPost, "/upload/v2/file/upload_complete", nil, map[string]string{
			"preuploadID": preuploadID,
		}, &comp, true); err != nil {
			if isExpiredUploadSession(err) {
				removeResumeJSON(opt.ResumeStatePath)
			}
			return nil, err
		}
		if comp.Completed && comp.FileID > 0 {
			removeResumeJSON(opt.ResumeStatePath)
			if opt.OnProgress != nil {
				opt.OnProgress(opt.Size, 0, false)
			}
			return &UploadResult{
				FID:   comp.FileID,
				Name:  name,
				Size:  opt.Size,
				MD5:   md5Hex,
				Rapid: false,
			}, nil
		}
		if err := sleepContext(ctx, time.Second); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("123云盘开放平台等待文件合并超时")
}

func (b *openAPIBackend) trySingleUpload(ctx context.Context, parentID int64, name, md5Hex string, opt UploadFileOptions) (*UploadResult, bool) {
	domains, err := b.uploadDomains(ctx)
	if err != nil || len(domains) == 0 {
		return nil, false
	}
	duplicate := "1"
	if opt.Overwrite {
		duplicate = "2"
	}
	tracker := newUploadProgressTracker(opt.Size, 0, opt.OnProgress)
	fields := map[string]string{
		"parentFileID": strconv.FormatInt(parentID, 10),
		"filename":     name,
		"etag":         md5Hex,
		"size":         strconv.FormatInt(opt.Size, 10),
		"duplicate":    duplicate,
	}
	pr := &pacedReader{
		ctx:           ctx,
		r:             io.NewSectionReader(opt.File, 0, opt.Size),
		waitIfPaused:  opt.WaitIfPaused,
		throttleBytes: opt.ThrottleBytes,
		tracker:       tracker,
	}
	var out struct {
		FileID    int64 `json:"fileID"`
		Completed bool  `json:"completed"`
	}
	if err := b.postMultipart(ctx, domains[0]+"/upload/v2/file/single/create", fields, "file", name, pr, &out); err != nil || !out.Completed || out.FileID <= 0 {
		pr.rollback()
		return nil, false
	}
	if opt.OnProgress != nil {
		opt.OnProgress(opt.Size, 0, false)
	}
	return &UploadResult{
		FID:   out.FileID,
		Name:  name,
		Size:  opt.Size,
		MD5:   md5Hex,
		Rapid: false,
	}, true
}

func (b *openAPIBackend) uploadSliceWithRetry(
	ctx context.Context,
	servers []string,
	preuploadID string,
	sliceNo int64,
	f io.ReaderAt,
	offset, length int64,
	opt UploadFileOptions,
	tracker *uploadProgressTracker,
) error {
	h := md5.New()
	if _, err := io.Copy(h, io.NewSectionReader(f, offset, length)); err != nil {
		return err
	}
	sliceMD5 := hex.EncodeToString(h.Sum(nil))

	var lastErr error
	for attempt := 0; attempt < openAPIMaxRetries; attempt++ {
		server := servers[(int(sliceNo-1)+attempt)%len(servers)]
		pr := &pacedReader{
			ctx:           ctx,
			r:             io.NewSectionReader(f, offset, length),
			waitIfPaused:  opt.WaitIfPaused,
			throttleBytes: opt.ThrottleBytes,
			tracker:       tracker,
		}
		fields := map[string]string{
			"preuploadID": preuploadID,
			"sliceNo":     strconv.FormatInt(sliceNo, 10),
			"sliceMD5":    sliceMD5,
		}
		err := b.postMultipart(ctx, server+"/upload/v2/file/slice", fields, "slice", fmt.Sprintf("slice-%d", sliceNo), pr, nil)
		if err == nil {
			return nil
		}
		pr.rollback()
		lastErr = err
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var apiErr *openAPIError
		if errors.As(err, &apiErr) && apiErr.isUnauthorized() && b.canRefreshToken() {
			tok, _ := b.getToken(ctx, false, "")
			if _, rErr := b.getToken(ctx, true, tok); rErr != nil {
				return fmt.Errorf("%w: %v", ErrUnauthorized, rErr)
			}
			continue
		}
		if attempt < openAPIMaxRetries-1 {
			if sErr := sleepContext(ctx, retryBackoff(attempt, "")); sErr != nil {
				return sErr
			}
		}
	}
	return lastErr
}

func (b *openAPIBackend) postMultipart(ctx context.Context, fullURL string, fields map[string]string, fileField, fileName string, fileReader io.Reader, out any) error {
	tok, err := b.getToken(ctx, false, "")
	if err != nil {
		return err
	}
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		var wErr error
		for k, v := range fields {
			if wErr = mw.WriteField(k, v); wErr != nil {
				break
			}
		}
		if wErr == nil {
			var part io.Writer
			part, wErr = mw.CreateFormFile(fileField, fileName)
			if wErr == nil {
				_, wErr = io.Copy(part, fileReader)
			}
		}
		if cErr := mw.Close(); wErr == nil {
			wErr = cErr
		}
		_ = pw.CloseWithError(wErr)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, pr)
	if err != nil {
		_ = pr.Close()
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Platform", openAPIPlatformHeader)
	req.Header.Set("User-Agent", b.userAgent)
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := b.streamClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return &openAPIError{HTTPStatus: resp.StatusCode, Code: 401, Message: "access_token 无效或已过期"}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return &openAPIError{HTTPStatus: resp.StatusCode, Code: 429, Message: "上传接口触发限流"}
	}
	var env openAPIEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		if resp.StatusCode >= 300 {
			return &openAPIError{HTTPStatus: resp.StatusCode, Code: resp.StatusCode, Message: string(raw)}
		}
		return fmt.Errorf("解析上传响应失败: %w", err)
	}
	if env.Code != 0 {
		return &openAPIError{HTTPStatus: resp.StatusCode, Code: env.Code, Message: env.Message, TraceID: env.TraceID}
	}
	if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("解析上传结果失败: %w", err)
		}
	}
	return nil
}

func parseOpenAPITime(raw string) time.Time {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", s, cstZone); err == nil {
		return t.UTC()
	}
	return time.Time{}
}
