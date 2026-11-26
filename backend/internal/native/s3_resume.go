package native

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
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
	s3MinPartSize     int64 = 5 << 20  // 5 MiB (S3 minimum part size)
	s3DefaultPartSize int64 = 8 << 20  // 8 MiB
	s3MaxParts              = 10000
	s3HeadHashBytes   int64 = 64 << 10 // 64 KiB head digest for file identity
)

type s3ResumeRecord struct {
	Remote      string `json:"remote"`
	Bucket      string `json:"bucket"`
	Key         string `json:"key"`
	UploadID    string `json:"uploadId"`
	PartSize    int64  `json:"partSize"`
	FileSize    int64  `json:"fileSize"`
	ModTimeNano int64  `json:"modTimeNano"`
	HeadHash    string `json:"headHash"`
	UpdatedAt   int64  `json:"updatedAt"`
}

type s3Client struct {
	endpoint       string
	region         string
	accessKey      string
	secretKey      string
	sessionToken   string
	forcePathStyle bool
	acl            string
	storageClass   string
	sse            string
	httpClient     *http.Client
}

// canUseS3ResumableUpload reports whether the target S3 remote configuration is
// compatible with our pure-Go SigV4 resumable multipart uploader.
func canUseS3ResumableUpload(p map[string]string) bool {
	if p == nil || p["type"] != "s3" {
		return false
	}
	if p["access_key_id"] == "" || p["secret_access_key"] == "" {
		return false
	}
	if p["v2_auth"] == "true" {
		return false
	}
	// Require explicit endpoint or standard AWS region so endpoint resolution is deterministic.
	if strings.TrimSpace(p["endpoint"]) == "" && strings.TrimSpace(p["region"]) == "" {
		return false
	}
	return true
}

func newS3ClientFromParams(p map[string]string, globalProxy string) (*s3Client, error) {
	region := strings.TrimSpace(p["region"])
	if region == "" {
		region = "us-east-1"
	}
	endpoint := strings.TrimSpace(p["endpoint"])
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://s3.%s.amazonaws.com", region)
	} else if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	endpoint = strings.TrimRight(endpoint, "/")
	if _, err := url.Parse(endpoint); err != nil {
		return nil, err
	}

	proxyStr := strings.TrimSpace(p["proxy_url"])
	if proxyStr == "" {
		proxyStr = strings.TrimSpace(globalProxy)
	}
	tr := &http.Transport{
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     60 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: p["insecure_skip_verify"] == "true", //nolint:gosec // user-explicit per-remote option
		},
	}
	if proxyStr != "" {
		if pu, err := url.Parse(proxyStr); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	forcePath := p["force_path_style"] == "true"
	if !forcePath && p["force_path_style"] != "false" {
		// Default to path-style for MinIO / custom IP or localhost endpoints.
		if strings.EqualFold(p["provider"], "Minio") || strings.Contains(endpoint, "127.0.0.1") || strings.Contains(endpoint, "localhost") {
			forcePath = true
		}
	}
	return &s3Client{
		endpoint:       endpoint,
		region:         region,
		accessKey:      p["access_key_id"],
		secretKey:      p["secret_access_key"],
		sessionToken:   p["session_token"],
		forcePathStyle: forcePath,
		acl:            strings.TrimSpace(p["acl"]),
		storageClass:   strings.TrimSpace(p["storage_class"]),
		sse:            strings.TrimSpace(p["server_side_encryption"]),
		httpClient:     &http.Client{Transport: tr},
	}, nil
}

func (c *s3Client) objectURL(bucket, key string, query url.Values) (string, error) {
	base, err := url.Parse(c.endpoint)
	if err != nil {
		return "", err
	}
	escapedKey := escapeS3Key(key)
	u := *base
	if c.forcePathStyle {
		u.Path = "/" + url.PathEscape(bucket) + "/" + escapedKey
		u.RawPath = "/" + url.PathEscape(bucket) + "/" + escapedKey
	} else {
		u.Host = bucket + "." + base.Host
		u.Path = "/" + escapedKey
		u.RawPath = "/" + escapedKey
	}
	if len(query) > 0 {
		u.RawQuery = encodeS3Query(query)
	}
	return u.String(), nil
}

func escapeS3Key(key string) string {
	key = strings.TrimPrefix(key, "/")
	segs := strings.Split(key, "/")
	for i, seg := range segs {
		segs[i] = awsURIEncode(seg)
	}
	return strings.Join(segs, "/")
}

func awsURIEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') ||
			ch == '-' || ch == '_' || ch == '.' || ch == '~' {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

func encodeS3Query(q url.Values) string {
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := append([]string(nil), q[k]...)
		sort.Strings(vals)
		ek := awsURIEncode(k)
		for _, v := range vals {
			parts = append(parts, ek+"="+awsURIEncode(v))
		}
	}
	return strings.Join(parts, "&")
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (c *s3Client) signAndDo(ctx context.Context, method, bucket, key string, query url.Values, payload []byte, extraHeaders map[string]string) (*http.Response, error) {
	rawURL, err := c.objectURL(bucket, key, query)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if len(payload) > 0 {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	if len(payload) > 0 {
		req.ContentLength = int64(len(payload))
	}

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")
	payloadHash := sha256Hex(payload)

	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if c.sessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", c.sessionToken)
	}
	for k, v := range extraHeaders {
		if v != "" {
			req.Header.Set(k, v)
		}
	}

	// Build canonical headers (lowercase, sorted).
	headerMap := map[string]string{
		"host":                 req.URL.Host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           amzDate,
	}
	if c.sessionToken != "" {
		headerMap["x-amz-security-token"] = c.sessionToken
	}
	for k, v := range extraHeaders {
		if v != "" {
			headerMap[strings.ToLower(k)] = strings.TrimSpace(v)
		}
	}
	headerKeys := make([]string, 0, len(headerMap))
	for k := range headerMap {
		headerKeys = append(headerKeys, k)
	}
	sort.Strings(headerKeys)
	var canonicalHeaders strings.Builder
	for _, k := range headerKeys {
		canonicalHeaders.WriteString(k)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(headerMap[k])
		canonicalHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(headerKeys, ";")

	canonicalURI := req.URL.RawPath
	if canonicalURI == "" {
		canonicalURI = req.URL.Path
	}
	canonicalQuery := encodeS3Query(query)
	canonicalReq := strings.Join([]string{
		method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", dateStamp, c.region)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256Hex([]byte(canonicalReq)),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+c.secretKey), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(c.region))
	kService := hmacSHA256(kRegion, []byte("s3"))
	kSigning := hmacSHA256(kService, []byte("aws4_request"))
	signature := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))

	auth := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.accessKey, credentialScope, signedHeaders, signature)
	req.Header.Set("Authorization", auth)

	return c.httpClient.Do(req)
}

func (c *s3Client) headObject(ctx context.Context, bucket, key string) (exists bool, size int64, modTime int64, etag string, err error) {
	resp, err := c.signAndDo(ctx, http.MethodHead, bucket, key, nil, nil, nil)
	if err != nil {
		return false, 0, 0, "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode == http.StatusNotFound {
		return false, 0, 0, "", nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, 0, 0, "", fmt.Errorf("S3 HEAD 失败（HTTP %d）", resp.StatusCode)
	}
	var mt int64
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		if t, parseErr := http.ParseTime(lm); parseErr == nil {
			mt = t.Unix()
		}
	}
	return true, resp.ContentLength, mt, strings.Trim(strings.TrimSpace(resp.Header.Get("ETag")), `"`), nil
}

func (c *s3Client) createMultipartUpload(ctx context.Context, bucket, key string) (string, error) {
	headers := map[string]string{}
	if c.acl != "" {
		headers["x-amz-acl"] = c.acl
	}
	if c.storageClass != "" {
		headers["x-amz-storage-class"] = c.storageClass
	}
	if c.sse != "" {
		headers["x-amz-server-side-encryption"] = c.sse
	}
	resp, err := c.signAndDo(ctx, http.MethodPost, bucket, key, url.Values{"uploads": {""}}, nil, headers)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("S3 初始化分片上传失败（HTTP %d）", resp.StatusCode)
	}
	var parsed struct {
		UploadID string `xml:"UploadId"`
	}
	if err := xml.Unmarshal(body, &parsed); err != nil || parsed.UploadID == "" {
		return "", errors.New("S3 初始化分片上传返回无效 UploadId")
	}
	return parsed.UploadID, nil
}

func (c *s3Client) listParts(ctx context.Context, bucket, key, uploadID string) (map[int]string, bool, error) {
	parts := map[int]string{}
	marker := ""
	for {
		q := url.Values{"uploadId": {uploadID}}
		if marker != "" {
			q.Set("part-number-marker", marker)
		}
		resp, err := c.signAndDo(ctx, http.MethodGet, bucket, key, q, nil, nil)
		if err != nil {
			return nil, false, err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, false, nil
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, false, fmt.Errorf("S3 查询已上传分片失败（HTTP %d）", resp.StatusCode)
		}
		var parsed struct {
			IsTruncated          bool   `xml:"IsTruncated"`
			NextPartNumberMarker string `xml:"NextPartNumberMarker"`
			Parts                []struct {
				PartNumber int    `xml:"PartNumber"`
				ETag       string `xml:"ETag"`
			} `xml:"Part"`
		}
		if err := xml.Unmarshal(body, &parsed); err != nil {
			return nil, false, err
		}
		for _, p := range parsed.Parts {
			if p.PartNumber > 0 && p.ETag != "" {
				parts[p.PartNumber] = p.ETag
			}
		}
		if !parsed.IsTruncated || parsed.NextPartNumberMarker == "" {
			break
		}
		marker = parsed.NextPartNumberMarker
	}
	return parts, true, nil
}

func (c *s3Client) uploadPart(ctx context.Context, bucket, key, uploadID string, partNumber int, data []byte) (string, error) {
	q := url.Values{
		"partNumber": {strconv.Itoa(partNumber)},
		"uploadId":   {uploadID},
	}
	sum := md5.Sum(data)
	headers := map[string]string{
		"content-md5": base64.StdEncoding.EncodeToString(sum[:]),
	}
	resp, err := c.signAndDo(ctx, http.MethodPut, bucket, key, q, data, headers)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("S3 分片 #%d 上传失败（HTTP %d）", partNumber, resp.StatusCode)
	}
	etag := strings.TrimSpace(resp.Header.Get("ETag"))
	if etag == "" {
		return "", fmt.Errorf("S3 分片 #%d 缺少 ETag", partNumber)
	}
	return etag, nil
}

func (c *s3Client) completeMultipartUpload(ctx context.Context, bucket, key, uploadID string, parts map[int]string) error {
	nums := make([]int, 0, len(parts))
	for n := range parts {
		nums = append(nums, n)
	}
	sort.Ints(nums)
	type xmlPart struct {
		PartNumber int    `xml:"PartNumber"`
		ETag       string `xml:"ETag"`
	}
	type completeReq struct {
		XMLName xml.Name  `xml:"CompleteMultipartUpload"`
		Parts   []xmlPart `xml:"Part"`
	}
	payloadStruct := completeReq{Parts: make([]xmlPart, 0, len(nums))}
	for _, n := range nums {
		payloadStruct.Parts = append(payloadStruct.Parts, xmlPart{PartNumber: n, ETag: parts[n]})
	}
	payload, err := xml.Marshal(payloadStruct)
	if err != nil {
		return err
	}
	q := url.Values{"uploadId": {uploadID}}
	resp, err := c.signAndDo(ctx, http.MethodPost, bucket, key, q, payload, map[string]string{"content-type": "application/xml"})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("S3 合并分片失败（HTTP %d）", resp.StatusCode)
	}
	if bytes.Contains(body, []byte("<Error>")) {
		return errors.New("S3 合并分片返回错误")
	}
	return nil
}

func (c *s3Client) abortMultipartUpload(ctx context.Context, bucket, key, uploadID string) {
	if uploadID == "" {
		return
	}
	abortCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := c.signAndDo(abortCtx, http.MethodDelete, bucket, key, url.Values{"uploadId": {uploadID}}, nil, nil)
	if err == nil && resp != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
	}
}

func computeLocalFileHeadHash(f *os.File) (string, error) {
	buf := make([]byte, s3HeadHashBytes)
	n, err := f.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return sha256Hex(buf[:n]), nil
}

func s3StateFilePath(stateDir, remote, bucket, key string) string {
	sum := sha256.Sum256([]byte(remote + "\x00" + bucket + "\x00" + key))
	return filepath.Join(stateDir, "s3-"+hex.EncodeToString(sum[:16])+".json")
}

func loadS3ResumeRecord(path string) (*s3ResumeRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rec s3ResumeRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

func saveS3ResumeRecord(statePath string, rec s3ResumeRecord) error {
	if err := os.MkdirAll(filepath.Dir(statePath), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	tmp := statePath + "." + randomName() + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, statePath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func chooseS3PartSize(fileSize int64, configuredChunk string) int64 {
	partSize := s3DefaultPartSize
	if fileSize >= 1<<30 {
		partSize = 16 << 20
	}
	if configuredChunk != "" {
		if parsed := ParseBwLimitBytesPerSec(configuredChunk); parsed >= s3MinPartSize {
			partSize = parsed
		}
	}
	minForCount := (fileSize + s3MaxParts - 1) / s3MaxParts
	if minForCount > partSize {
		partSize = minForCount
	}
	if partSize < s3MinPartSize {
		partSize = s3MinPartSize
	}
	return partSize
}

func splitS3BucketAndPrefix(dstSub string) (bucket, prefix string, ok bool) {
	trimmed := strings.Trim(strings.TrimSpace(dstSub), "/")
	if trimmed == "" {
		return "", "", false
	}
	bucket, prefix, _ = strings.Cut(trimmed, "/")
	if bucket == "" {
		return "", "", false
	}
	return bucket, prefix, true
}

// resumeUploadFilesToS3 uploads large local files to an S3-compatible remote
// using persistent multipart upload state + ListParts resume across interruptions.
func resumeUploadFilesToS3(
	ctx context.Context,
	srcSub string,
	remoteName string,
	remoteParams map[string]string,
	dstSub string,
	files []resumeFileMeta,
	opt CopyOptions,
	emit func(Event),
) error {
	if len(files) == 0 {
		return nil
	}
	bucket, keyPrefix, ok := splitS3BucketAndPrefix(dstSub)
	if !ok {
		return errors.New("S3 断点续传需要指定目标存储桶路径")
	}
	client, err := newS3ClientFromParams(remoteParams, opt.ProxyURL)
	if err != nil {
		return err
	}
	root, baseRel, err := openDisk(srcSub)
	if err != nil {
		return err
	}
	defer root.Close()

	stateDir := strings.TrimSpace(opt.ResumeStateDir)
	if stateDir == "" {
		stateDir = filepath.Join(nativeTempDir(), "opensync-s3-resume")
	}
	_ = os.MkdirAll(stateDir, 0700)

	partConcurrency := 4
	if uc, err := strconv.Atoi(strings.TrimSpace(remoteParams["upload_concurrency"])); err == nil && uc >= 1 && uc <= 16 {
		partConcurrency = uc
	}

	var totalBytes int64
	for _, f := range files {
		totalBytes += f.Size
	}
	var (
		completedBytes atomic.Int64
		completedCount atomic.Int64
		failedBytes    atomic.Int64
		errorCount     atomic.Int64
		activeMu       sync.Mutex
		activeItems    = map[string]TransferItem{}
		errsMu         sync.Mutex
		errs           []error
	)
	publishStats := func() {
		activeMu.Lock()
		transferring := make([]TransferItem, 0, len(activeItems))
		var inFlight int64
		var speedSum float64
		for _, item := range activeItems {
			transferring = append(transferring, item)
			inFlight += item.Bytes
			speedSum += item.Speed
		}
		activeMu.Unlock()
		emit(Event{Stats: &Stats{
			Bytes:          completedBytes.Load() + inFlight,
			TotalBytes:     max(0, totalBytes-failedBytes.Load()),
			Transfers:      completedCount.Load(),
			TotalTransfers: max(0, int64(len(files))-errorCount.Load()),
			Errors:         errorCount.Load(),
			Speed:          speedSum,
			Transferring:   transferring,
		}})
	}

	for _, fm := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := opt.Controller.WaitIfPaused(ctx); err != nil {
			return err
		}
		objKey := fm.Rel
		if keyPrefix != "" {
			objKey = path.Join(keyPrefix, fm.Rel)
		}
		skipExisting := opt.SkipExisting || opt.ConflictPolicy == 1
		if !opt.ForceOverwrite && !opt.S3NoHead {
			if exists, dstSize, dstMod, _, headErr := client.headObject(ctx, bucket, objKey); headErr == nil && exists {
				if skipExisting {
					completedBytes.Add(fm.Size)
					completedCount.Add(1)
					publishStats()
					continue
				}
				if dstSize == fm.Size {
					if opt.Compare == 1 || (opt.Compare == 0 && fm.ModTime > 0 && dstMod >= fm.ModTime-1) {
						completedBytes.Add(fm.Size)
						completedCount.Add(1)
						publishStats()
						continue
					}
				}
			}
		}
		upErr := resumeUploadOneS3File(ctx, client, root, joinInRoot(baseRel, fm.Rel), stateDir, remoteName, bucket, objKey, remoteParams["chunk_size"], partConcurrency, opt, func(doneBytes int64, speed float64) {
			pct := 0.0
			if fm.Size > 0 {
				pct = float64(doneBytes) * 100.0 / float64(fm.Size)
				if pct > 100 {
					pct = 100
				}
			}
			activeMu.Lock()
			activeItems[fm.Rel] = TransferItem{
				Name:       fm.Rel,
				Size:       fm.Size,
				Bytes:      doneBytes,
				Percentage: pct,
				Speed:      speed,
			}
			activeMu.Unlock()
			publishStats()
		})
		activeMu.Lock()
		delete(activeItems, fm.Rel)
		activeMu.Unlock()
		if upErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			errorCount.Add(1)
			failedBytes.Add(fm.Size)
			errText := sanitizeRcloneError(upErr.Error())
			emit(Event{File: fm.Rel, Size: fm.Size, Failed: true, Error: errText})
			errsMu.Lock()
			errs = append(errs, upErr)
			errsMu.Unlock()
			publishStats()
			continue
		}
		completedBytes.Add(fm.Size)
		completedCount.Add(1)
		emit(Event{File: fm.Rel, Size: fm.Size})
		publishStats()
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func resumeUploadOneS3File(
	ctx context.Context,
	client *s3Client,
	root *os.Root,
	srcRel, stateDir, remoteName, bucket, key, chunkSize string,
	partConcurrency int,
	opt CopyOptions,
	onProgress func(doneBytes int64, speed float64),
) error {
	f, err := root.Open(srcRel)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	fileSize := st.Size()
	if fileSize <= 0 {
		return errors.New("空文件不适用分片续传")
	}
	headHash, err := computeLocalFileHeadHash(f)
	if err != nil {
		return err
	}
	modNano := st.ModTime().UnixNano()
	statePath := s3StateFilePath(stateDir, remoteName, bucket, key)

	var (
		uploadID      string
		partSize      int64
		existingParts map[int]string
	)
	if rec, loadErr := loadS3ResumeRecord(statePath); loadErr == nil {
		if rec.FileSize == fileSize && rec.ModTimeNano == modNano && rec.HeadHash == headHash && rec.UploadID != "" && rec.PartSize >= s3MinPartSize {
			if serverParts, found, listErr := client.listParts(ctx, bucket, key, rec.UploadID); listErr == nil && found {
				uploadID = rec.UploadID
				partSize = rec.PartSize
				existingParts = serverParts
			}
		} else if rec.UploadID != "" {
			// File changed since previous interrupted upload: abort old session to avoid orphan charges.
			client.abortMultipartUpload(ctx, bucket, key, rec.UploadID)
			_ = os.Remove(statePath)
		}
	}

	if uploadID == "" {
		partSize = chooseS3PartSize(fileSize, chunkSize)
		id, err := client.createMultipartUpload(ctx, bucket, key)
		if err != nil {
			return err
		}
		uploadID = id
		existingParts = map[int]string{}
		if err := saveS3ResumeRecord(statePath, s3ResumeRecord{
			Remote:      remoteName,
			Bucket:      bucket,
			Key:         key,
			UploadID:    uploadID,
			PartSize:    partSize,
			FileSize:    fileSize,
			ModTimeNano: modNano,
			HeadHash:    headHash,
			UpdatedAt:   time.Now().Unix(),
		}); err != nil {
			client.abortMultipartUpload(ctx, bucket, key, uploadID)
			return err
		}
	}

	numParts := int((fileSize + partSize - 1) / partSize)
	partETags := make(map[int]string, numParts)
	var alreadyBytes int64
	var missing []int
	for partNum := 1; partNum <= numParts; partNum++ {
		if etag, ok := existingParts[partNum]; ok {
			partETags[partNum] = etag
			alreadyBytes += s3PartByteLen(fileSize, partSize, partNum)
		} else {
			missing = append(missing, partNum)
		}
	}

	var uploadedBytes atomic.Int64
	uploadedBytes.Store(alreadyBytes)
	start := time.Now()
	var sessionBytes atomic.Int64
	onProgress(alreadyBytes, 0)

	if len(missing) > 0 {
		var (
			mu      sync.Mutex
			partErr error
			partCh  = make(chan int, len(missing))
			wg      sync.WaitGroup
		)
		for _, pn := range missing {
			partCh <- pn
		}
		close(partCh)

		maxPartRetries := max(3, min(opt.LowLevelRetries, 10))
		workers := min(partConcurrency, len(missing))
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for partNum := range partCh {
					if ctx.Err() != nil {
						return
					}
					if err := opt.Controller.WaitIfPaused(ctx); err != nil {
						return
					}
					offset := int64(partNum-1) * partSize
					length := s3PartByteLen(fileSize, partSize, partNum)
					buf := make([]byte, length)
					if _, err := io.ReadFull(io.NewSectionReader(f, offset, length), buf); err != nil {
						mu.Lock()
						if partErr == nil {
							partErr = err
						}
						mu.Unlock()
						return
					}
					var etag string
					var uErr error
					for attempt := 0; attempt < maxPartRetries; attempt++ {
						if ctx.Err() != nil {
							return
						}
						if err := opt.Controller.WaitIfPaused(ctx); err != nil {
							return
						}
						if attempt > 0 {
							timer := time.NewTimer(time.Duration(min(attempt*2, 10)) * time.Second)
							select {
							case <-ctx.Done():
								timer.Stop()
								return
							case <-timer.C:
							}
						}
						etag, uErr = client.uploadPart(ctx, bucket, key, uploadID, partNum, buf)
						if uErr == nil {
							break
						}
					}
					if uErr != nil {
						mu.Lock()
						if partErr == nil {
							partErr = uErr
						}
						mu.Unlock()
						return
					}
					if err := opt.Controller.ThrottleBytes(ctx, int(length)); err != nil {
						return
					}
					mu.Lock()
					partETags[partNum] = etag
					mu.Unlock()
					done := uploadedBytes.Add(length)
					sent := sessionBytes.Add(length)
					elapsed := time.Since(start).Seconds()
					speed := 0.0
					if elapsed > 0 {
						speed = float64(sent) / elapsed
					}
					onProgress(done, speed)
				}
			}()
		}
		wg.Wait()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if partErr != nil {
			return partErr
		}
	}

	if len(partETags) != numParts {
		return fmt.Errorf("S3 分片不完整（%d/%d），已保留上传断点", len(partETags), numParts)
	}
	if err := client.completeMultipartUpload(ctx, bucket, key, uploadID, partETags); err != nil {
		return err
	}
	_ = os.Remove(statePath)
	return nil
}

func s3PartByteLen(fileSize, partSize int64, partNum int) int64 {
	offset := int64(partNum-1) * partSize
	rem := fileSize - offset
	if rem > partSize {
		return partSize
	}
	if rem < 0 {
		return 0
	}
	return rem
}
