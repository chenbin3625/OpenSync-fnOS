package pan123

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

// About queries account storage capacity.
func (c *Client) About(ctx context.Context) (Quota, error) {
	q, _, err := c.backend.About(ctx)
	return q, err
}

// AccountInfo returns user identity and quota.
func (c *Client) AccountInfo(ctx context.Context) (AccountInfo, Quota, error) {
	q, info, err := c.backend.About(ctx)
	return info, q, err
}

// TestConnection verifies credentials and root folder accessibility.
func (c *Client) TestConnection(ctx context.Context) (AccountInfo, Quota, error) {
	q, info, err := c.backend.About(ctx)
	if err != nil {
		return AccountInfo{}, Quota{}, err
	}
	if _, err := c.ListDirByFID(ctx, c.rootFID); err != nil {
		return AccountInfo{}, Quota{}, err
	}
	return info, q, nil
}

// ListDirByFID fetches all immediate children of parentFID with TTL caching
// and in-flight deduplication.
func (c *Client) ListDirByFID(ctx context.Context, parentFID int64) ([]Entry, error) {
	return c.listDirByFIDWithPath(ctx, parentFID, "\x00")
}

func (c *Client) listDirByFIDWithPath(ctx context.Context, parentFID int64, dirRel string) ([]Entry, error) {
	if cached, ok := c.cache.getDir(parentFID); ok {
		return cached, nil
	}

	c.cache.mu.Lock()
	if flight, exists := c.cache.inFlight[parentFID]; exists {
		c.cache.mu.Unlock()
		select {
		case <-flight.done:
			if flight.err != nil {
				return nil, flight.err
			}
			out := make([]Entry, len(flight.entries))
			copy(out, flight.entries)
			return out, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	flight := &dirFlight{done: make(chan struct{})}
	c.cache.inFlight[parentFID] = flight
	c.cache.mu.Unlock()

	entries, err := c.backend.ListDir(ctx, parentFID)

	c.cache.mu.Lock()
	delete(c.cache.inFlight, parentFID)
	c.cache.mu.Unlock()

	flight.entries = entries
	flight.err = err
	close(flight.done)

	if err != nil {
		return nil, err
	}
	c.cache.setDir(parentFID, dirRel, entries)
	out := make([]Entry, len(entries))
	copy(out, entries)
	return out, nil
}

// Stat resolves relPath (relative to RootFolderID) into an Entry.
// Returns os.ErrNotExist when the path does not exist.
func (c *Client) Stat(ctx context.Context, relPath string) (Entry, error) {
	clean := cleanRelPath(relPath)
	if clean == "" {
		return Entry{
			FID:   c.rootFID,
			Name:  "",
			IsDir: true,
		}, nil
	}
	parts := strings.Split(clean, "/")
	curFID := c.rootFID
	curRel := ""
	for i, seg := range parts {
		if child, found, ok := c.cache.getChild(curFID, seg); ok {
			if !found {
				return Entry{}, os.ErrNotExist
			}
			if i == len(parts)-1 {
				return child, nil
			}
			if !child.IsDir {
				return Entry{}, os.ErrNotExist
			}
			curFID = child.FID
			if curRel == "" {
				curRel = seg
			} else {
				curRel += "/" + seg
			}
			continue
		}

		entries, err := c.listDirByFIDWithPath(ctx, curFID, curRel)
		if err != nil {
			return Entry{}, err
		}
		var matched *Entry
		for idx := range entries {
			if entries[idx].Name == seg {
				matched = &entries[idx]
				break
			}
		}
		if matched == nil {
			return Entry{}, os.ErrNotExist
		}
		if i == len(parts)-1 {
			return *matched, nil
		}
		if !matched.IsDir {
			return Entry{}, os.ErrNotExist
		}
		curFID = matched.FID
		if curRel == "" {
			curRel = seg
		} else {
			curRel += "/" + seg
		}
	}
	return Entry{}, os.ErrNotExist
}

// ListDir resolves relPath (must be a directory) and returns its children.
func (c *Client) ListDir(ctx context.Context, relPath string) ([]Entry, error) {
	clean := cleanRelPath(relPath)
	if clean == "" {
		return c.listDirByFIDWithPath(ctx, c.rootFID, "")
	}
	if fid, ok := c.cache.getPathFID(clean); ok {
		if entries, hit := c.cache.getDir(fid); hit {
			return entries, nil
		}
	}
	st, err := c.Stat(ctx, clean)
	if err != nil {
		return nil, err
	}
	if !st.IsDir {
		return nil, fmt.Errorf("%s 不是目录", clean)
	}
	return c.listDirByFIDWithPath(ctx, st.FID, clean)
}

// ListDirPage uses the OpenAPI cursor. The Web protocol retains its complete
// listing validation across pages.
func (c *Client) ListDirPage(ctx context.Context, relPath string, cursor int64) ([]Entry, int64, error) {
	backend, ok := c.backend.(*openAPIBackend)
	if !ok {
		return nil, 0, errors.New("当前 123 云盘协议不支持游标浏览")
	}
	dir, err := c.Stat(ctx, relPath)
	if err != nil {
		return nil, 0, err
	}
	if !dir.IsDir {
		return nil, 0, os.ErrNotExist
	}
	return backend.listDirPage(ctx, dir.FID, cursor)
}

// MkdirAll creates relPath and all missing parent directories, returning the
// final directory's FID.
func (c *Client) MkdirAll(ctx context.Context, relPath string) (int64, error) {
	clean := cleanRelPath(relPath)
	if clean == "" {
		return c.rootFID, nil
	}
	if st, err := c.Stat(ctx, clean); err == nil {
		if !st.IsDir {
			return 0, fmt.Errorf("%w: %s 已存在同名文件", os.ErrExist, clean)
		}
		return st.FID, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}

	c.mkdirMu.Lock()
	defer c.mkdirMu.Unlock()

	parts := strings.Split(clean, "/")
	curFID := c.rootFID
	curRel := ""
	for _, seg := range parts {
		if err := validateEntryName(seg); err != nil {
			return 0, err
		}
		nextRel := seg
		if curRel != "" {
			nextRel = curRel + "/" + seg
		}
		entries, err := c.listDirByFIDWithPath(ctx, curFID, curRel)
		if err != nil {
			return 0, err
		}
		var found *Entry
		for i := range entries {
			if entries[i].Name == seg {
				found = &entries[i]
				break
			}
		}
		if found != nil {
			if !found.IsDir {
				return 0, fmt.Errorf("%w: %s 已存在同名文件", os.ErrExist, nextRel)
			}
			curFID = found.FID
			curRel = nextRel
			continue
		}

		newFID, err := c.backend.Mkdir(ctx, curFID, seg)
		c.cache.invalidateDir(curFID, curRel)
		if err != nil {
			// Re-check in case another concurrent writer created the same dir.
			if retryEntries, listErr := c.listDirByFIDWithPath(ctx, curFID, curRel); listErr == nil {
				for i := range retryEntries {
					if retryEntries[i].Name == seg && retryEntries[i].IsDir {
						newFID = retryEntries[i].FID
						err = nil
						break
					}
				}
			}
			if err != nil {
				return 0, err
			}
		}
		if newFID <= 0 {
			// Verify and pick up the created directory ID from a fresh listing.
			retryEntries, listErr := c.listDirByFIDWithPath(ctx, curFID, curRel)
			if listErr != nil {
				return 0, listErr
			}
			for i := range retryEntries {
				if retryEntries[i].Name == seg && retryEntries[i].IsDir {
					newFID = retryEntries[i].FID
					break
				}
			}
			if newFID <= 0 {
				return 0, fmt.Errorf("创建目录 %q 成功但未在父目录中查到有效 ID", nextRel)
			}
		}
		c.cache.setPathFID(nextRel, newFID)
		curFID = newFID
		curRel = nextRel
	}
	return curFID, nil
}

// Move moves and/or renames srcRel to dstRel.
func (c *Client) Move(ctx context.Context, srcRel, dstRel string, overwrite bool) error {
	srcClean := cleanRelPath(srcRel)
	dstClean := cleanRelPath(dstRel)
	if srcClean == "" || dstClean == "" {
		return os.ErrPermission
	}
	if srcClean == dstClean {
		return nil
	}

	srcEntry, err := c.Stat(ctx, srcClean)
	if err != nil {
		return err
	}

	srcParentRel := parentRelPath(srcClean)
	dstParentRel := parentRelPath(dstClean)
	dstName := path.Base(dstClean)
	if err := validateEntryName(dstName); err != nil {
		return err
	}

	var dstParentFID int64
	if dstParentRel == srcParentRel {
		dstParentFID = srcEntry.ParentFID
	} else {
		dstParentFID, err = c.MkdirAll(ctx, dstParentRel)
		if err != nil {
			return err
		}
	}

	if existing, statErr := c.Stat(ctx, dstClean); statErr == nil {
		if existing.FID == srcEntry.FID {
			return nil
		}
		if !overwrite {
			return os.ErrExist
		}
		if err := c.backend.Delete(ctx, existing); err != nil {
			return err
		}
		c.cache.invalidateSubtree(dstParentFID, dstClean)
		c.invalidateDownloadCache(existing.FID)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}

	sameParent := srcEntry.ParentFID == dstParentFID
	sameName := srcEntry.Name == dstName

	switch {
	case sameParent && !sameName:
		if err := c.backend.Rename(ctx, srcEntry, dstName); err != nil {
			return err
		}
	case !sameParent && sameName:
		if err := c.backend.Move(ctx, srcEntry, dstParentFID); err != nil {
			return err
		}
	case !sameParent && !sameName:
		if err := c.backend.Move(ctx, srcEntry, dstParentFID); err != nil {
			return err
		}
		moved := srcEntry
		moved.ParentFID = dstParentFID
		if err := c.backend.Rename(ctx, moved, dstName); err != nil {
			c.cache.invalidateSubtree(srcEntry.ParentFID, srcClean)
			c.cache.invalidateSubtree(dstParentFID, dstClean)
			return err
		}
	}

	c.cache.invalidateSubtree(srcEntry.ParentFID, srcClean)
	c.cache.invalidateSubtree(dstParentFID, dstClean)
	c.invalidateDownloadCache(srcEntry.FID)
	return nil
}

// Delete moves the file or folder at relPath into the 123Pan recycle bin.
func (c *Client) Delete(ctx context.Context, relPath string) error {
	clean := cleanRelPath(relPath)
	if clean == "" {
		return os.ErrPermission
	}
	entry, err := c.Stat(ctx, clean)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := c.backend.Delete(ctx, entry); err != nil {
		return err
	}
	c.cache.invalidateSubtree(entry.ParentFID, clean)
	c.invalidateDownloadCache(entry.FID)
	return nil
}

// DownloadStream opens a file for reading with Range / If-Range passthrough
// and single-flight download URL caching.
func (c *Client) DownloadStream(ctx context.Context, relPath, rangeHeader, ifRange string) (io.ReadCloser, http.Header, int, error) {
	entry, err := c.Stat(ctx, relPath)
	if err != nil {
		return nil, nil, 0, err
	}
	if entry.IsDir {
		return nil, nil, http.StatusMethodNotAllowed, fmt.Errorf("%s 是目录", relPath)
	}
	if entry.Size == 0 {
		h := http.Header{
			"Content-Type":   {"application/octet-stream"},
			"Content-Length": {"0"},
			"Accept-Ranges":  {"bytes"},
		}
		if !entry.ModTime.IsZero() {
			h.Set("Last-Modified", entry.ModTime.UTC().Format(http.TimeFormat))
		}
		if entry.MD5 != "" {
			h.Set("ETag", `"`+entry.MD5+`"`)
		}
		return io.NopCloser(bytes.NewReader(nil)), h, http.StatusOK, nil
	}

	for attempt := 0; attempt < 2; attempt++ {
		forceRefresh := attempt > 0
		dlURL, dlHeaders, err := c.resolveDownloadURL(ctx, entry, forceRefresh)
		if err != nil {
			return nil, nil, 0, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
		if err != nil {
			return nil, nil, 0, err
		}
		req.Header.Set("User-Agent", c.userAgent)
		for k, vals := range dlHeaders {
			for _, v := range vals {
				req.Header.Add(k, v)
			}
		}
		if strings.TrimSpace(rangeHeader) != "" {
			req.Header.Set("Range", rangeHeader)
		}
		if strings.TrimSpace(ifRange) != "" {
			req.Header.Set("If-Range", ifRange)
		}

		resp, err := c.streamClient.Do(req)
		if err != nil {
			return nil, nil, 0, err
		}
		if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusGone) && attempt == 0 {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			c.invalidateDownloadCache(entry.FID)
			continue
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
			snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusTooManyRequests {
				return nil, nil, resp.StatusCode, ErrRateLimited
			}
			return nil, nil, resp.StatusCode, fmt.Errorf("123云盘下载返回 HTTP %d: %s", resp.StatusCode, scrubSecrets(strings.TrimSpace(string(snippet))))
		}

		outHeaders := make(http.Header)
		for _, k := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
			if v := resp.Header.Get(k); v != "" {
				outHeaders.Set(k, v)
			}
		}
		if outHeaders.Get("Content-Type") == "" {
			outHeaders.Set("Content-Type", "application/octet-stream")
		}
		if outHeaders.Get("Accept-Ranges") == "" {
			outHeaders.Set("Accept-Ranges", "bytes")
		}
		if outHeaders.Get("Content-Length") == "" && resp.StatusCode == http.StatusOK {
			outHeaders.Set("Content-Length", strconv.FormatInt(entry.Size, 10))
		}
		if outHeaders.Get("ETag") == "" && entry.MD5 != "" {
			outHeaders.Set("ETag", `"`+entry.MD5+`"`)
		}
		return resp.Body, outHeaders, resp.StatusCode, nil
	}
	return nil, nil, http.StatusBadGateway, errors.New("123云盘下载链接重试失败")
}

func (c *Client) resolveDownloadURL(ctx context.Context, entry Entry, forceRefresh bool) (string, http.Header, error) {
	if !forceRefresh {
		c.dlCacheMu.Lock()
		if cached, ok := c.dlCache[entry.FID]; ok && time.Now().Before(cached.ExpireAt) {
			c.dlCacheMu.Unlock()
			return cached.URL, cached.Headers.Clone(), nil
		}
		if flight, exists := c.dlInFlight[entry.FID]; exists {
			c.dlCacheMu.Unlock()
			select {
			case <-flight.done:
				return flight.url, flight.headers.Clone(), flight.err
			case <-ctx.Done():
				return "", nil, ctx.Err()
			}
		}
		flight := &dlFlight{done: make(chan struct{})}
		c.dlInFlight[entry.FID] = flight
		c.dlCacheMu.Unlock()

		dlURL, headers, ttl, err := c.backend.DownloadURL(ctx, entry)
		if ttl <= 0 {
			ttl = c.dlTTL
		}

		c.dlCacheMu.Lock()
		delete(c.dlInFlight, entry.FID)
		if err == nil && dlURL != "" {
			c.dlCache[entry.FID] = cachedDownload{
				URL:      dlURL,
				Headers:  headers.Clone(),
				ExpireAt: time.Now().Add(ttl),
			}
		}
		c.dlCacheMu.Unlock()

		flight.url = dlURL
		flight.headers = headers
		flight.err = err
		close(flight.done)
		return dlURL, headers.Clone(), err
	}

	dlURL, headers, ttl, err := c.backend.DownloadURL(ctx, entry)
	if err != nil {
		return nilURL(), nil, err
	}
	if ttl <= 0 {
		ttl = c.dlTTL
	}
	c.dlCacheMu.Lock()
	c.dlCache[entry.FID] = cachedDownload{
		URL:      dlURL,
		Headers:  headers.Clone(),
		ExpireAt: time.Now().Add(ttl),
	}
	c.dlCacheMu.Unlock()
	return dlURL, headers.Clone(), nil
}

func nilURL() string { return "" }

func (c *Client) invalidateDownloadCache(fid int64) {
	c.dlCacheMu.Lock()
	delete(c.dlCache, fid)
	c.dlCacheMu.Unlock()
}

// UploadFile uploads a local *os.File to relPath using the active protocol's
// rapid-upload + high-throughput multipart pipeline.
func (c *Client) UploadFile(ctx context.Context, relPath string, opt UploadFileOptions) (*UploadResult, error) {
	if opt.File == nil {
		return nil, errors.New("上传源文件为空")
	}
	clean := cleanRelPath(relPath)
	if clean == "" {
		return nil, ErrInvalidName
	}
	parentRel := parentRelPath(clean)
	fileName := path.Base(clean)
	if err := validateEntryName(fileName); err != nil {
		return nil, err
	}

	parentFID, err := c.MkdirAll(ctx, parentRel)
	if err != nil {
		return nil, err
	}

	var existing *Entry
	if st, statErr := c.Stat(ctx, clean); statErr == nil {
		if st.IsDir {
			return nil, fmt.Errorf("%w: 目标路径 %q 已存在同名目录", os.ErrExist, clean)
		}
		if !opt.Overwrite {
			return nil, os.ErrExist
		}
		copied := st
		existing = &copied
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}

	if opt.Concurrency <= 0 {
		opt.Concurrency = c.uploadConcurrency
	}
	if opt.Concurrency > MaxUploadConcurrency {
		opt.Concurrency = MaxUploadConcurrency
	}

	res, err := c.backend.UploadFile(ctx, parentFID, fileName, opt, existing)
	if err != nil {
		return nil, err
	}
	c.cache.invalidateDir(parentFID, parentRel)
	if existing != nil {
		c.invalidateDownloadCache(existing.FID)
	}
	return res, nil
}

// UploadStream uploads an io.Reader (used by BridgeFS.Put for remote-to-remote
// or crypt transfers) by spooling to a temporary file and delegating to UploadFile.
func (c *Client) UploadStream(ctx context.Context, relPath string, body io.Reader, size int64, modTime *time.Time) (*UploadResult, error) {
	tmp, err := os.CreateTemp("", "opensync-pan123-stream-*")
	if err != nil {
		return nil, fmt.Errorf("创建临时上传缓存失败: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	written, err := io.Copy(tmp, body)
	if err != nil {
		return nil, fmt.Errorf("缓冲上传流失败: %w", err)
	}
	if size >= 0 && written != size {
		return nil, fmt.Errorf("上传流大小不匹配：声明 %d 字节，实际读取 %d 字节", size, written)
	}
	mt := time.Now()
	if modTime != nil && !modTime.IsZero() {
		mt = *modTime
	}
	return c.UploadFile(ctx, relPath, UploadFileOptions{
		File:        tmp,
		Size:        written,
		ModTime:     mt,
		Concurrency: c.uploadConcurrency,
		Overwrite:   true,
	})
}

func parentRelPath(clean string) string {
	dir := path.Dir(clean)
	if dir == "." || dir == "/" {
		return ""
	}
	return strings.Trim(dir, "/")
}
