package quark

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	uploadResumeMaxAge = 20 * time.Hour
	throttleChunkBytes = 64 << 10 // 64 KiB for smooth live bwlimit & progress reporting
)

// UploadFileOptions configures a direct local-file upload to Quark/UC with
// rapid-upload (MD5+SHA1), concurrent OSS multipart transfer, and resume state.
type UploadFileOptions struct {
	File            *os.File
	Size            int64
	ModTime         time.Time
	Concurrency     int
	ResumeStatePath string
	Overwrite       bool
	WaitIfPaused    func(context.Context) error
	ThrottleBytes   func(context.Context, int) error
	OnProgress      func(doneBytes int64, speed float64, rapid bool)
}

type uploadResumeState struct {
	RelPath      string         `json:"relPath"`
	UploadName   string         `json:"uploadName"`
	PdirFID      string         `json:"pdirFid"`
	FileSize     int64          `json:"fileSize"`
	MD5          string         `json:"md5"`
	SHA1         string         `json:"sha1"`
	FormatType   string         `json:"formatType"`
	TaskID       string         `json:"taskId"`
	UploadID     string         `json:"uploadId"`
	ObjKey       string         `json:"objKey"`
	UploadURL    string         `json:"uploadUrl"`
	Bucket       string         `json:"bucket"`
	AuthInfo     string         `json:"authInfo"`
	Callback     upCallback     `json:"callback"`
	PartSize     int64          `json:"partSize"`
	ETags        map[int]string `json:"etags"`
	UpdatedAtSec int64          `json:"updatedAt"`
}

func detectMimeType(fileName string) string {
	ext := strings.ToLower(path.Ext(fileName))
	if ext != "" {
		if mt := mime.TypeByExtension(ext); mt != "" {
			if idx := strings.IndexByte(mt, ';'); idx >= 0 {
				mt = mt[:idx]
			}
			mt = strings.TrimSpace(mt)
			if mt != "" {
				return mt
			}
		}
	}
	return "application/octet-stream"
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func escapeOSSObjectKey(key string) string {
	key = strings.TrimPrefix(key, "/")
	segs := strings.Split(key, "/")
	for i, seg := range segs {
		segs[i] = url.PathEscape(seg)
	}
	return strings.Join(segs, "/")
}

// buildOSSPartURL constructs the Alibaba Cloud OSS upload URL. Real OSS hosts
// use virtual-hosted style (`https://<bucket>.<host>/<objKey>`); loopback/IP
// test endpoints use path style (`http://127.0.0.1:<port>/<bucket>/<objKey>`).
func buildOSSURL(uploadURL, bucket, objKey string, query url.Values) (string, error) {
	raw := strings.TrimSpace(uploadURL)
	if raw == "" {
		return "", errors.New("上传地址 upload_url 为空")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	escapedKey := escapeOSSObjectKey(objKey)
	host := u.Hostname()
	isLocalOrIP := host == "localhost" || net.ParseIP(host) != nil
	if isLocalOrIP {
		u.Path = "/" + url.PathEscape(bucket) + "/" + escapedKey
		u.RawPath = u.Path
	} else {
		if bucket != "" && !strings.HasPrefix(u.Host, bucket+".") {
			u.Host = bucket + "." + u.Host
		}
		u.Path = "/" + escapedKey
		u.RawPath = u.Path
	}
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return u.String(), nil
}

func (c *Client) upPre(ctx context.Context, pdirFID, fileName, mimeType string, size int64, modTime time.Time) (*upPreResp, error) {
	tsMs := time.Now().UnixMilli()
	if !modTime.IsZero() && modTime.UnixMilli() > 0 {
		tsMs = modTime.UnixMilli()
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	payload := map[string]any{
		"ccp_hash_update": true,
		"dir_name":        "",
		"file_name":       fileName,
		"format_type":     mimeType,
		"l_created_at":    tsMs,
		"l_updated_at":    tsMs,
		"pdir_fid":        pdirFID,
		"size":            size,
	}
	var resp upPreResp
	if err := c.doJSON(ctx, http.MethodPost, "/file/upload/pre", nil, payload, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) upHash(ctx context.Context, taskID, md5Hex, sha1Hex string) (*upHashResp, error) {
	payload := map[string]any{
		"md5":     strings.ToLower(md5Hex),
		"sha1":    strings.ToLower(sha1Hex),
		"task_id": taskID,
	}
	var resp upHashResp
	if err := c.doJSON(ctx, http.MethodPost, "/file/update/hash", nil, payload, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) upAuth(ctx context.Context, taskID, authInfo, authMeta string) (string, error) {
	payload := map[string]any{
		"auth_info": authInfo,
		"auth_meta": authMeta,
		"task_id":   taskID,
	}
	var resp upAuthResp
	if err := c.doJSON(ctx, http.MethodPost, "/file/upload/auth", nil, payload, &resp); err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.Data.AuthKey) == "" {
		return "", errors.New("获取夸克分片上传签名失败：auth_key 为空")
	}
	return resp.Data.AuthKey, nil
}

func (c *Client) upFinish(ctx context.Context, objKey, taskID string) (string, error) {
	payload := map[string]any{
		"obj_key": objKey,
		"task_id": taskID,
	}
	var resp upFinishResp
	if err := c.doJSON(ctx, http.MethodPost, "/file/upload/finish", nil, payload, &resp); err != nil {
		return "", err
	}
	return resp.Data.FID, nil
}

func buildPartAuthMeta(mimeType, timeStr, bucket, objKey string, partNumber int, uploadID string) string {
	return fmt.Sprintf("PUT\n\n%s\n%s\nx-oss-date:%s\nx-oss-user-agent:%s\n/%s/%s?partNumber=%d&uploadId=%s",
		mimeType, timeStr, timeStr, ossUserAgent, bucket, objKey, partNumber, uploadID)
}

func buildCommitAuthMeta(contentMD5, timeStr, callbackB64, bucket, objKey, uploadID string) string {
	if callbackB64 != "" {
		return fmt.Sprintf("POST\n%s\napplication/xml\n%s\nx-oss-callback:%s\nx-oss-date:%s\nx-oss-user-agent:%s\n/%s/%s?uploadId=%s",
			contentMD5, timeStr, callbackB64, timeStr, ossUserAgent, bucket, objKey, uploadID)
	}
	return fmt.Sprintf("POST\n%s\napplication/xml\n%s\nx-oss-date:%s\nx-oss-user-agent:%s\n/%s/%s?uploadId=%s",
		contentMD5, timeStr, timeStr, ossUserAgent, bucket, objKey, uploadID)
}

type uploadSessionParams struct {
	TaskID     string
	UploadID   string
	ObjKey     string
	UploadURL  string
	Bucket     string
	AuthInfo   string
	FormatType string
	Callback   upCallback
	PartSize   int64
}

func (c *Client) uploadPartBytes(
	ctx context.Context,
	sess uploadSessionParams,
	partNumber int,
	data []byte,
	waitIfPaused func(context.Context) error,
	throttle func(context.Context, int) error,
	onChunk func(n int),
) (string, error) {
	q := url.Values{
		"partNumber": {strconv.Itoa(partNumber)},
		"uploadId":   {sess.UploadID},
	}
	partURL, err := buildOSSURL(sess.UploadURL, sess.Bucket, sess.ObjKey, q)
	if err != nil {
		return "", err
	}
	mimeType := sess.FormatType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if waitIfPaused != nil {
			if err := waitIfPaused(ctx); err != nil {
				return "", err
			}
		}
		if attempt > 0 {
			if err := sleepCtx(ctx, time.Duration(attempt*2)*time.Second); err != nil {
				return "", err
			}
		}

		timeStr := time.Now().UTC().Format(http.TimeFormat)
		authMeta := buildPartAuthMeta(mimeType, timeStr, sess.Bucket, sess.ObjKey, partNumber, sess.UploadID)
		authKey, err := c.upAuth(ctx, sess.TaskID, sess.AuthInfo, authMeta)
		if err != nil {
			lastErr = err
			if errors.Is(err, ErrUnauthorized) {
				return "", err
			}
			continue
		}

		var sentInAttempt int64
		bodyReader := &pacedBytesReader{
			ctx:          ctx,
			data:         data,
			waitIfPaused: waitIfPaused,
			throttle:     throttle,
			onRead: func(n int) {
				sentInAttempt += int64(n)
				if onChunk != nil {
					onChunk(n)
				}
			},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, partURL, bodyReader)
		if err != nil {
			return "", err
		}
		req.ContentLength = int64(len(data))
		req.Header.Set("Authorization", authKey)
		req.Header.Set("Content-Type", mimeType)
		req.Header.Set("X-Oss-Date", timeStr)
		req.Header.Set("X-Oss-User-Agent", ossUserAgent)
		req.Header.Set("Referer", c.profile.Referer)
		req.Header.Set("Origin", c.profile.Origin)

		resp, err := c.streamClient.Do(req)
		if err != nil {
			if sentInAttempt > 0 && onChunk != nil {
				onChunk(-int(sentInAttempt))
			}
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			lastErr = err
			continue
		}
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			if sentInAttempt > 0 && onChunk != nil {
				onChunk(-int(sentInAttempt))
			}
			lastErr = fmt.Errorf("夸克 OSS 分片 #%d 上传失败 (HTTP %d: %s)", partNumber, resp.StatusCode, strings.TrimSpace(string(respBody)))
			if resp.StatusCode == http.StatusForbidden || resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
				continue
			}
			return "", lastErr
		}
		etag := strings.TrimSpace(resp.Header.Get("ETag"))
		if etag == "" {
			etag = fmt.Sprintf(`"%x"`, md5.Sum(data))
		}
		return etag, nil
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("夸克 OSS 分片 #%d 上传失败", partNumber)
}

type pacedBytesReader struct {
	ctx          context.Context
	data         []byte
	offset       int
	waitIfPaused func(context.Context) error
	throttle     func(context.Context, int) error
	onRead       func(int)
}

func (r *pacedBytesReader) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	if r.ctx != nil && r.ctx.Err() != nil {
		return 0, r.ctx.Err()
	}
	if r.waitIfPaused != nil {
		if err := r.waitIfPaused(r.ctx); err != nil {
			return 0, err
		}
	}
	maxChunk := len(p)
	if maxChunk > throttleChunkBytes {
		maxChunk = throttleChunkBytes
	}
	rem := len(r.data) - r.offset
	if maxChunk > rem {
		maxChunk = rem
	}
	n := copy(p[:maxChunk], r.data[r.offset:r.offset+maxChunk])
	r.offset += n
	if r.throttle != nil && n > 0 {
		if err := r.throttle(r.ctx, n); err != nil {
			return n, err
		}
	}
	if r.onRead != nil && n > 0 {
		r.onRead(n)
	}
	return n, nil
}

func (c *Client) upCommit(ctx context.Context, sess uploadSessionParams, etags map[int]string) error {
	nums := make([]int, 0, len(etags))
	for num := range etags {
		nums = append(nums, num)
	}
	sort.Ints(nums)

	type xmlPart struct {
		PartNumber int    `xml:"PartNumber"`
		ETag       string `xml:"ETag"`
	}
	type completeMultipartUpload struct {
		XMLName xml.Name  `xml:"CompleteMultipartUpload"`
		Parts   []xmlPart `xml:"Part"`
	}
	doc := completeMultipartUpload{Parts: make([]xmlPart, 0, len(nums))}
	for _, num := range nums {
		doc.Parts = append(doc.Parts, xmlPart{PartNumber: num, ETag: etags[num]})
	}
	xmlBody, err := xml.Marshal(doc)
	if err != nil {
		return err
	}
	fullXML := append([]byte(xml.Header), xmlBody...)
	sum := md5.Sum(fullXML)
	contentMD5 := base64.StdEncoding.EncodeToString(sum[:])

	callbackB64 := ""
	if sess.Callback.CallbackURL != "" || sess.Callback.CallbackBody != "" {
		cbBytes, err := json.Marshal(sess.Callback)
		if err != nil {
			return err
		}
		callbackB64 = base64.StdEncoding.EncodeToString(cbBytes)
	}

	timeStr := time.Now().UTC().Format(http.TimeFormat)
	authMeta := buildCommitAuthMeta(contentMD5, timeStr, callbackB64, sess.Bucket, sess.ObjKey, sess.UploadID)
	authKey, err := c.upAuth(ctx, sess.TaskID, sess.AuthInfo, authMeta)
	if err != nil {
		return err
	}

	commitURL, err := buildOSSURL(sess.UploadURL, sess.Bucket, sess.ObjKey, url.Values{"uploadId": {sess.UploadID}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, commitURL, bytes.NewReader(fullXML))
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(fullXML))
	req.Header.Set("Authorization", authKey)
	req.Header.Set("Content-MD5", contentMD5)
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("X-Oss-Date", timeStr)
	req.Header.Set("X-Oss-User-Agent", ossUserAgent)
	if callbackB64 != "" {
		req.Header.Set("X-Oss-Callback", callbackB64)
	}
	req.Header.Set("Referer", c.profile.Referer)
	req.Header.Set("Origin", c.profile.Origin)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("提交夸克分片合并请求失败: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("夸克分片合并失败 (HTTP %d: %s)", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if bytes.Contains(body, []byte("<Error>")) {
		return fmt.Errorf("夸克分片合并返回错误: %s", strings.TrimSpace(string(body)))
	}
	return nil
}

func computeFileMD5SHA1(ctx context.Context, f *os.File, waitIfPaused func(context.Context) error) (md5Hex, sha1Hex string, err error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	hMD5 := md5.New()
	hSHA1 := sha1.New()
	buf := make([]byte, 256<<10)
	for {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		if waitIfPaused != nil {
			if err := waitIfPaused(ctx); err != nil {
				return "", "", err
			}
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			_, _ = hMD5.Write(buf[:n])
			_, _ = hSHA1.Write(buf[:n])
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return "", "", readErr
		}
	}
	return hex.EncodeToString(hMD5.Sum(nil)), hex.EncodeToString(hSHA1.Sum(nil)), nil
}

func loadUploadResumeState(statePath string) (*uploadResumeState, error) {
	if statePath == "" {
		return nil, os.ErrNotExist
	}
	raw, err := os.ReadFile(statePath)
	if err != nil {
		return nil, err
	}
	var st uploadResumeState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	if st.ETags == nil {
		st.ETags = map[int]string{}
	}
	return &st, nil
}

func saveUploadResumeState(statePath string, st uploadResumeState) error {
	if statePath == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0700); err != nil {
		return err
	}
	st.UpdatedAtSec = time.Now().Unix()
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := statePath + "." + randomHex(6) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, statePath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// UploadFile uploads a local file directly to relPath on Quark/UC with single-pass
// MD5+SHA1 calculation, rapid upload (秒传), concurrent OSS multipart transfer,
// persistent part-level resume state, and atomic overwrite replacement.
func (c *Client) UploadFile(ctx context.Context, relPath string, opt UploadFileOptions) (*UploadResult, error) {
	if opt.File == nil {
		return nil, errors.New("缺少待上传的本地文件句柄")
	}
	clean := cleanRelPath(relPath)
	if clean == "" {
		return nil, ErrInvalidName
	}
	parentRel := path.Dir(clean)
	if parentRel == "." || parentRel == "/" {
		parentRel = ""
	}
	targetName := path.Base(clean)
	if !isSafeEntryName(targetName) {
		return nil, ErrInvalidName
	}

	parentFID, err := c.MkdirAll(ctx, parentRel)
	if err != nil {
		return nil, err
	}

	// Check for an existing file at the destination so overwrites are safe and
	// never produce duplicate-name copies or data loss on mid-transfer failure.
	var oldFID string
	if existing, statErr := c.Stat(ctx, clean); statErr == nil {
		if existing.IsDir {
			return nil, fmt.Errorf("目标路径 %q 已存在同名目录", clean)
		}
		if !opt.Overwrite {
			return nil, os.ErrExist
		}
		oldFID = existing.FID
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}

	md5Hex, sha1Hex, err := computeFileMD5SHA1(ctx, opt.File, opt.WaitIfPaused)
	if err != nil {
		return nil, err
	}

	mimeType := detectMimeType(targetName)
	uploadName := targetName
	usingTempName := oldFID != ""
	if usingTempName {
		uploadName = ".opensync-up-" + randomHex(6) + "-" + targetName
	}

	var (
		sess          uploadSessionParams
		etags         map[int]string
		resumed       bool
		rapidFinished bool
		finalFID      string
	)

	// Attempt to resume a matching interrupted upload session.
	if opt.ResumeStatePath != "" {
		if st, loadErr := loadUploadResumeState(opt.ResumeStatePath); loadErr == nil {
			age := time.Since(time.Unix(st.UpdatedAtSec, 0))
			if age >= 0 && age < uploadResumeMaxAge &&
				st.PdirFID == parentFID &&
				st.FileSize == opt.Size &&
				st.MD5 == md5Hex &&
				st.SHA1 == sha1Hex &&
				st.TaskID != "" &&
				st.UploadID != "" &&
				st.PartSize > 0 {
				sess = uploadSessionParams{
					TaskID:     st.TaskID,
					UploadID:   st.UploadID,
					ObjKey:     st.ObjKey,
					UploadURL:  st.UploadURL,
					Bucket:     st.Bucket,
					AuthInfo:   st.AuthInfo,
					FormatType: st.FormatType,
					Callback:   st.Callback,
					PartSize:   st.PartSize,
				}
				etags = st.ETags
				if st.UploadName != "" {
					uploadName = st.UploadName
					usingTempName = uploadName != targetName
				}
				resumed = true
			} else {
				_ = os.Remove(opt.ResumeStatePath)
			}
		}
	}

	if !resumed {
		pre, err := c.upPre(ctx, parentFID, uploadName, mimeType, opt.Size, opt.ModTime)
		if err != nil {
			return nil, err
		}
		if pre.Data.Finish {
			rapidFinished = true
			finalFID = pre.Data.FID
		} else {
			hashResp, err := c.upHash(ctx, pre.Data.TaskID, md5Hex, sha1Hex)
			if err != nil {
				return nil, err
			}
			if hashResp.Data.Finish {
				rapidFinished = true
				finalFID = hashResp.Data.FID
				if finalFID == "" {
					finalFID = pre.Data.FID
				}
			} else {
				partSize := pre.Metadata.PartSize
				if partSize <= 0 {
					partSize = DefaultPartSize
				}
				if c.partSizeOverride > 0 && c.partSizeOverride < partSize {
					partSize = c.partSizeOverride
				}
				fmtType := pre.Data.FormatType
				if fmtType == "" {
					fmtType = mimeType
				}
				sess = uploadSessionParams{
					TaskID:     pre.Data.TaskID,
					UploadID:   pre.Data.UploadID,
					ObjKey:     pre.Data.ObjKey,
					UploadURL:  pre.Data.UploadURL,
					Bucket:     pre.Data.Bucket,
					AuthInfo:   pre.Data.AuthInfo,
					FormatType: fmtType,
					Callback:   pre.Data.Callback,
					PartSize:   partSize,
				}
				etags = map[int]string{}
				_ = saveUploadResumeState(opt.ResumeStatePath, uploadResumeState{
					RelPath:    clean,
					UploadName: uploadName,
					PdirFID:    parentFID,
					FileSize:   opt.Size,
					MD5:        md5Hex,
					SHA1:       sha1Hex,
					FormatType: sess.FormatType,
					TaskID:     sess.TaskID,
					UploadID:   sess.UploadID,
					ObjKey:     sess.ObjKey,
					UploadURL:  sess.UploadURL,
					Bucket:     sess.Bucket,
					AuthInfo:   sess.AuthInfo,
					Callback:   sess.Callback,
					PartSize:   sess.PartSize,
					ETags:      etags,
				})
			}
		}
	}

	if rapidFinished {
		if opt.OnProgress != nil {
			opt.OnProgress(opt.Size, 0, true)
		}
		finalFID, err = c.finalizeOverwriteSwap(ctx, parentFID, parentRel, clean, targetName, uploadName, oldFID, finalFID, usingTempName)
		if err != nil {
			return nil, err
		}
		if opt.ResumeStatePath != "" {
			_ = os.Remove(opt.ResumeStatePath)
		}
		return &UploadResult{
			FID:   finalFID,
			Name:  targetName,
			Size:  opt.Size,
			MD5:   md5Hex,
			SHA1:  sha1Hex,
			Rapid: true,
		}, nil
	}

	// Upload missing parts concurrently to OSS.
	totalParts := 1
	if opt.Size > 0 {
		totalParts = int((opt.Size + sess.PartSize - 1) / sess.PartSize)
	}
	var alreadyBytes int64
	var missing []int
	for partNum := 1; partNum <= totalParts; partNum++ {
		if etag, ok := etags[partNum]; ok && etag != "" {
			alreadyBytes += partLength(opt.Size, sess.PartSize, partNum)
		} else {
			missing = append(missing, partNum)
		}
	}

	var uploadedBytes atomic.Int64
	uploadedBytes.Store(alreadyBytes)
	var sessionBytes atomic.Int64
	startTime := time.Now()
	var lastReportNano atomic.Int64
	if opt.OnProgress != nil {
		opt.OnProgress(alreadyBytes, 0, false)
	}

	concurrency := opt.Concurrency
	if concurrency <= 0 {
		concurrency = c.uploadConcurrency
	}
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > 8 {
		concurrency = 8
	}
	if concurrency > len(missing) && len(missing) > 0 {
		concurrency = len(missing)
	}

	if len(missing) > 0 {
		workCtx, cancelWork := context.WithCancel(ctx)
		defer cancelWork()

		partCh := make(chan int, len(missing))
		for _, pn := range missing {
			partCh <- pn
		}
		close(partCh)

		var (
			etagMu   sync.Mutex
			firstErr error
			errOnce  sync.Once
			wg       sync.WaitGroup
		)
		setErr := func(e error) {
			errOnce.Do(func() {
				firstErr = e
				cancelWork()
			})
		}

		for w := 0; w < concurrency; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for partNum := range partCh {
					if workCtx.Err() != nil {
						return
					}
					c.MaybeKeepalive(workCtx)
					offset := int64(partNum-1) * sess.PartSize
					pLen := partLength(opt.Size, sess.PartSize, partNum)
					buf := make([]byte, pLen)
					if pLen > 0 {
						n, readErr := opt.File.ReadAt(buf, offset)
						if readErr != nil && !errors.Is(readErr, io.EOF) {
							setErr(fmt.Errorf("读取本地文件分片 #%d 失败: %w", partNum, readErr))
							return
						}
						if int64(n) != pLen {
							setErr(fmt.Errorf("本地文件在上传期间大小发生变化（分片 #%d 期望 %d 字节，读取 %d 字节）", partNum, pLen, n))
							return
						}
					}

					etag, upErr := c.uploadPartBytes(workCtx, sess, partNum, buf, opt.WaitIfPaused, opt.ThrottleBytes, func(delta int) {
						cur := uploadedBytes.Add(int64(delta))
						sent := sessionBytes.Add(int64(delta))
						if opt.OnProgress == nil {
							return
						}
						now := time.Now()
						prev := lastReportNano.Load()
						if now.UnixNano()-prev >= int64(250*time.Millisecond) && lastReportNano.CompareAndSwap(prev, now.UnixNano()) {
							elapsed := now.Sub(startTime).Seconds()
							speed := 0.0
							if elapsed > 0 && sent > 0 {
								speed = float64(sent) / elapsed
							}
							if cur < 0 {
								cur = 0
							}
							opt.OnProgress(cur, speed, false)
						}
					})
					if upErr != nil {
						setErr(upErr)
						return
					}

					etagMu.Lock()
					etags[partNum] = etag
					if opt.ResumeStatePath != "" {
						snapshot := make(map[int]string, len(etags))
						for k, v := range etags {
							snapshot[k] = v
						}
						_ = saveUploadResumeState(opt.ResumeStatePath, uploadResumeState{
							RelPath:    clean,
							UploadName: uploadName,
							PdirFID:    parentFID,
							FileSize:   opt.Size,
							MD5:        md5Hex,
							SHA1:       sha1Hex,
							FormatType: sess.FormatType,
							TaskID:     sess.TaskID,
							UploadID:   sess.UploadID,
							ObjKey:     sess.ObjKey,
							UploadURL:  sess.UploadURL,
							Bucket:     sess.Bucket,
							AuthInfo:   sess.AuthInfo,
							Callback:   sess.Callback,
							PartSize:   sess.PartSize,
							ETags:      snapshot,
						})
					}
					etagMu.Unlock()
				}
			}()
		}
		wg.Wait()
		if firstErr != nil {
			return nil, firstErr
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}

	if err := c.upCommit(ctx, sess, etags); err != nil {
		return nil, err
	}
	finalFID, err = c.upFinish(ctx, sess.ObjKey, sess.TaskID)
	if err != nil {
		return nil, err
	}
	if opt.OnProgress != nil {
		elapsed := time.Since(startTime).Seconds()
		speed := 0.0
		if elapsed > 0 && sessionBytes.Load() > 0 {
			speed = float64(sessionBytes.Load()) / elapsed
		}
		opt.OnProgress(opt.Size, speed, false)
	}

	finalFID, err = c.finalizeOverwriteSwap(ctx, parentFID, parentRel, clean, targetName, uploadName, oldFID, finalFID, usingTempName)
	if err != nil {
		return nil, err
	}
	if opt.ResumeStatePath != "" {
		_ = os.Remove(opt.ResumeStatePath)
	}
	return &UploadResult{
		FID:   finalFID,
		Name:  targetName,
		Size:  opt.Size,
		MD5:   md5Hex,
		SHA1:  sha1Hex,
		Rapid: false,
	}, nil
}

func partLength(fileSize, partSize int64, partNum int) int64 {
	if fileSize <= 0 {
		return 0
	}
	offset := int64(partNum-1) * partSize
	rem := fileSize - offset
	if rem < 0 {
		return 0
	}
	if rem > partSize {
		return partSize
	}
	return rem
}

func (c *Client) finalizeOverwriteSwap(
	ctx context.Context,
	parentFID, parentRel, cleanTarget, targetName, uploadName, oldFID, newFID string,
	usingTempName bool,
) (string, error) {
	c.cache.invalidateSubtree(parentFID, cleanTarget)
	if !usingTempName {
		if newFID != "" {
			c.cache.setPathFID(cleanTarget, newFID)
		}
		return newFID, nil
	}

	// Locate newly uploaded temp file if upFinish/upHash omitted its FID.
	if newFID == "" {
		entries, err := c.listDirByFIDWithPath(ctx, parentFID, parentRel)
		if err != nil {
			return "", err
		}
		for _, e := range entries {
			if e.Name == uploadName {
				newFID = e.FID
				break
			}
		}
	}
	if newFID == "" {
		return "", fmt.Errorf("上传临时文件 %q 后未能定位新文件 ID", uploadName)
	}
	if oldFID != "" && oldFID != newFID {
		if err := c.DeleteByFIDs(ctx, []string{oldFID}); err != nil {
			// Best-effort cleanup of temp file to avoid leaving orphans if old cannot be removed.
			_ = c.DeleteByFIDs(ctx, []string{newFID})
			return "", fmt.Errorf("替换旧文件失败: %w", err)
		}
		c.evictDownloadURL(oldFID)
	}
	if err := c.RenameByFID(ctx, newFID, targetName); err != nil {
		return "", fmt.Errorf("重命名替换文件失败: %w", err)
	}
	c.cache.invalidateSubtree(parentFID, cleanTarget)
	c.cache.setPathFID(cleanTarget, newFID)
	return newFID, nil
}
