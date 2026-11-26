package quark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func isSafeEntryName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, `/\`) {
		return false
	}
	return strings.IndexFunc(name, unicode.IsControl) < 0
}

func parseQuarkTimestamp(raw int64) time.Time {
	if raw <= 0 {
		return time.Time{}
	}
	// Quark returns 13-digit milliseconds; guard against 10-digit seconds.
	if raw > 1e11 {
		return time.UnixMilli(raw).UTC()
	}
	return time.Unix(raw, 0).UTC()
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

func apiItemToEntry(item apiFileItem, fallbackParent string) (Entry, bool) {
	name := html.UnescapeString(item.FileName)
	if !isSafeEntryName(name) || strings.TrimSpace(item.FID) == "" {
		return Entry{}, false
	}
	isDir := item.Dir || (!item.File && item.FileType == 0)
	ts := item.UpdatedAt
	if ts <= 0 {
		ts = item.LUpdatedAt
	}
	if ts <= 0 {
		ts = item.CreatedAt
	}
	parent := strings.TrimSpace(item.PdirFID)
	if parent == "" {
		parent = fallbackParent
	}
	size := item.Size
	if isDir {
		size = 0
	}
	return Entry{
		FID:       item.FID,
		ParentFID: parent,
		Name:      name,
		IsDir:     isDir,
		Size:      size,
		ModTime:   parseQuarkTimestamp(ts),
		MD5:       normalizeMD5(item.MD5),
	}, true
}

// ListDirByFID fetches all immediate children of pdirFID with TTL caching and
// in-flight deduplication so concurrent walkers don't stampede /file/sort.
func (c *Client) ListDirByFID(ctx context.Context, pdirFID string) ([]Entry, error) {
	return c.listDirByFIDWithPath(ctx, pdirFID, "\x00")
}

func (c *Client) listDirByFIDWithPath(ctx context.Context, pdirFID, dirRel string) ([]Entry, error) {
	if pdirFID == "" {
		pdirFID = c.rootFID
	}
	if cached, ok := c.cache.getDir(pdirFID); ok {
		return cached, nil
	}

	c.cache.mu.Lock()
	if flight, exists := c.cache.inFlight[pdirFID]; exists {
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
	c.cache.inFlight[pdirFID] = flight
	c.cache.mu.Unlock()

	entries, err := c.fetchDirPages(ctx, pdirFID)

	c.cache.mu.Lock()
	delete(c.cache.inFlight, pdirFID)
	c.cache.mu.Unlock()

	flight.entries = entries
	flight.err = err
	close(flight.done)

	if err != nil {
		return nil, err
	}
	c.cache.setDir(pdirFID, dirRel, entries)
	out := make([]Entry, len(entries))
	copy(out, entries)
	return out, nil
}

func (c *Client) fetchDirPages(ctx context.Context, pdirFID string) ([]Entry, error) {
	const pageSize = 100
	var all []Entry
	seenFIDs := make(map[string]struct{})
	seenNames := make(map[string]int) // name -> index in all (keep newest if Quark has duplicate names)

	for page := 1; ; page++ {
		q := url.Values{
			"pdir_fid":        {pdirFID},
			"_page":           {strconv.Itoa(page)},
			"_size":           {strconv.Itoa(pageSize)},
			"_fetch_total":    {"1"},
			"_fetch_sub_dirs": {"0"},
			"_sort":           {"file_type:asc,file_name:asc"},
		}
		var resp sortResp
		if err := c.doJSON(ctx, http.MethodGet, "/file/sort", q, nil, &resp); err != nil {
			return nil, err
		}
		for _, item := range resp.Data.List {
			entry, ok := apiItemToEntry(item, pdirFID)
			if !ok {
				continue
			}
			if _, dup := seenFIDs[entry.FID]; dup {
				continue
			}
			seenFIDs[entry.FID] = struct{}{}
			if prevIdx, dupName := seenNames[entry.Name]; dupName {
				// Quark allows duplicate names; keep the more recently updated one
				// (or prefer directory over file on tie) for deterministic path resolution.
				if entry.ModTime.After(all[prevIdx].ModTime) {
					all[prevIdx] = entry
				}
				continue
			}
			seenNames[entry.Name] = len(all)
			all = append(all, entry)
		}
		if len(resp.Data.List) < pageSize {
			break
		}
		if resp.Metadata.Total > 0 && page*pageSize >= resp.Metadata.Total {
			break
		}
		if page >= 2000 {
			break
		}
	}
	return all, nil
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

	parentRel := path.Dir(clean)
	if parentRel == "." || parentRel == "/" {
		parentRel = ""
	}
	baseName := path.Base(clean)

	parentFID, err := c.resolveDirFID(ctx, parentRel)
	if err != nil {
		return Entry{}, err
	}
	if entry, found, hit := c.cache.getChild(parentFID, baseName); hit {
		if !found {
			return Entry{}, os.ErrNotExist
		}
		return entry, nil
	}
	entries, err := c.listDirByFIDWithPath(ctx, parentFID, parentRel)
	if err != nil {
		return Entry{}, err
	}
	for _, e := range entries {
		if e.Name == baseName {
			return e, nil
		}
	}
	return Entry{}, os.ErrNotExist
}

func (c *Client) resolveDirFID(ctx context.Context, relDir string) (string, error) {
	clean := cleanRelPath(relDir)
	if clean == "" {
		return c.rootFID, nil
	}
	if fid, ok := c.cache.getPathFID(clean); ok {
		return fid, nil
	}

	segs := strings.Split(clean, "/")
	curFID := c.rootFID
	curRel := ""
	for _, seg := range segs {
		nextRel := seg
		if curRel != "" {
			nextRel = curRel + "/" + seg
		}
		if fid, ok := c.cache.getPathFID(nextRel); ok {
			curFID = fid
			curRel = nextRel
			continue
		}
		entries, err := c.listDirByFIDWithPath(ctx, curFID, curRel)
		if err != nil {
			return "", err
		}
		found := false
		for _, e := range entries {
			if e.Name == seg {
				if !e.IsDir {
					return "", os.ErrNotExist
				}
				curFID = e.FID
				curRel = nextRel
				found = true
				break
			}
		}
		if !found {
			return "", os.ErrNotExist
		}
	}
	return curFID, nil
}

// ListDir returns all immediate children of the directory at relPath.
func (c *Client) ListDir(ctx context.Context, relPath string) ([]Entry, error) {
	clean := cleanRelPath(relPath)
	if clean == "" {
		return c.listDirByFIDWithPath(ctx, c.rootFID, "")
	}
	st, err := c.Stat(ctx, clean)
	if err != nil {
		return nil, err
	}
	if !st.IsDir {
		return nil, errors.New("目标路径不是目录")
	}
	return c.listDirByFIDWithPath(ctx, st.FID, clean)
}

// MkdirAll creates relPath and any missing parent directories, returning the
// final directory's FID. Concurrent calls serialize creation via mkdirMu so
// Quark (which permits duplicate folder names) never receives duplicate folders.
func (c *Client) MkdirAll(ctx context.Context, relPath string) (string, error) {
	clean := cleanRelPath(relPath)
	if clean == "" {
		return c.rootFID, nil
	}
	if fid, ok := c.cache.getPathFID(clean); ok {
		return fid, nil
	}
	c.mkdirMu.Lock()
	defer c.mkdirMu.Unlock()
	if fid, ok := c.cache.getPathFID(clean); ok {
		return fid, nil
	}
	segs := strings.Split(clean, "/")
	curFID := c.rootFID
	curRel := ""
	for _, seg := range segs {
		if !isSafeEntryName(seg) {
			return "", ErrInvalidName
		}
		nextRel := seg
		if curRel != "" {
			nextRel = curRel + "/" + seg
		}
		if fid, ok := c.cache.getPathFID(nextRel); ok {
			curFID = fid
			curRel = nextRel
			continue
		}
		entries, err := c.listDirByFIDWithPath(ctx, curFID, curRel)
		if err != nil {
			return "", err
		}
		var existing *Entry
		for i := range entries {
			if entries[i].Name == seg {
				existing = &entries[i]
				break
			}
		}
		if existing != nil {
			if !existing.IsDir {
				return "", fmt.Errorf("目录路径与已有文件冲突: %s", nextRel)
			}
			curFID = existing.FID
			curRel = nextRel
			continue
		}
		newFID, err := c.mkdirOne(ctx, curFID, seg)
		if err != nil {
			return "", err
		}
		c.cache.invalidateDir(curFID, nextRel)
		if newFID != "" {
			c.cache.setPathFID(nextRel, newFID)
			curFID = newFID
		} else {
			// Fallback: re-list parent to locate newly created folder FID.
			refreshed, listErr := c.listDirByFIDWithPath(ctx, curFID, curRel)
			if listErr != nil {
				return "", listErr
			}
			found := false
			for _, e := range refreshed {
				if e.Name == seg && e.IsDir {
					curFID = e.FID
					found = true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("创建目录 %q 后未能确认目录 ID", nextRel)
			}
		}
		curRel = nextRel
	}
	return curFID, nil
}

func (c *Client) mkdirOne(ctx context.Context, pdirFID, name string) (string, error) {
	payload := map[string]any{
		"pdir_fid":      pdirFID,
		"file_name":     name,
		"dir_path":      "",
		"dir_init_lock": false,
	}
	var resp mkdirResp
	if err := c.doJSON(ctx, http.MethodPost, "/file", nil, payload, &resp); err != nil {
		return "", err
	}
	return resp.Data.FID, nil
}

// RenameByFID renames a file or directory identified by fid.
func (c *Client) RenameByFID(ctx context.Context, fid, newName string) error {
	if !isSafeEntryName(newName) {
		return ErrInvalidName
	}
	payload := map[string]any{
		"fid":       fid,
		"file_name": newName,
	}
	return c.doJSON(ctx, http.MethodPost, "/file/rename", nil, payload, nil)
}

// MoveByFID moves one or more FIDs into toPdirFID, waiting for any async task.
func (c *Client) MoveByFID(ctx context.Context, fids []string, toPdirFID string) error {
	if len(fids) == 0 {
		return nil
	}
	payload := map[string]any{
		"action_type":  1,
		"filelist":     fids,
		"to_pdir_fid":  toPdirFID,
		"exclude_fids": []string{},
	}
	var resp taskSubmitResp
	if err := c.doJSON(ctx, http.MethodPost, "/file/move", nil, payload, &resp); err != nil {
		return err
	}
	if resp.Data.TaskID != "" && !resp.Data.Finish {
		return c.waitTask(ctx, resp.Data.TaskID)
	}
	return nil
}

// DeleteByFIDs removes one or more FIDs (moving them to Quark recycle bin).
func (c *Client) DeleteByFIDs(ctx context.Context, fids []string) error {
	if len(fids) == 0 {
		return nil
	}
	payload := map[string]any{
		"action_type":  2,
		"filelist":     fids,
		"exclude_fids": []string{},
	}
	var resp taskSubmitResp
	if err := c.doJSON(ctx, http.MethodPost, "/file/delete", nil, payload, &resp); err != nil {
		return err
	}
	if resp.Data.TaskID != "" && !resp.Data.Finish {
		return c.waitTask(ctx, resp.Data.TaskID)
	}
	return nil
}

func (c *Client) waitTask(ctx context.Context, taskID string) error {
	for retry := 0; retry < 60; retry++ {
		q := url.Values{
			"task_id":     {taskID},
			"retry_index": {strconv.Itoa(retry)},
		}
		var resp taskStatusResp
		if err := c.doJSON(ctx, http.MethodGet, "/task", q, nil, &resp); err != nil {
			return err
		}
		switch resp.Data.Status {
		case 2:
			return nil
		case 3:
			msg := strings.TrimSpace(resp.Data.Message)
			if msg == "" {
				msg = "后台任务执行失败"
			}
			return fmt.Errorf("夸克异步操作失败: %s", msg)
		}
		delay := 300 * time.Millisecond
		if retry >= 5 {
			delay = time.Second
		}
		if err := sleepCtx(ctx, delay); err != nil {
			return err
		}
	}
	return errors.New("等待夸克网盘异步操作完成超时")
}

// Delete deletes the file or directory at relPath.
func (c *Client) Delete(ctx context.Context, relPath string) error {
	clean := cleanRelPath(relPath)
	if clean == "" {
		return os.ErrPermission
	}
	st, err := c.Stat(ctx, clean)
	if err != nil {
		return err
	}
	if err := c.DeleteByFIDs(ctx, []string{st.FID}); err != nil {
		return err
	}
	c.evictDownloadURL(st.FID)
	c.cache.invalidateSubtree(st.ParentFID, clean)
	return nil
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
	srcSt, err := c.Stat(ctx, srcClean)
	if err != nil {
		return err
	}
	dstParentRel := path.Dir(dstClean)
	if dstParentRel == "." || dstParentRel == "/" {
		dstParentRel = ""
	}
	dstName := path.Base(dstClean)
	if !isSafeEntryName(dstName) {
		return ErrInvalidName
	}

	dstParentFID, err := c.MkdirAll(ctx, dstParentRel)
	if err != nil {
		return err
	}

	// Check existing target entry.
	if dstSt, statErr := c.Stat(ctx, dstClean); statErr == nil {
		if !overwrite {
			return os.ErrExist
		}
		if dstSt.FID != srcSt.FID {
			if err := c.DeleteByFIDs(ctx, []string{dstSt.FID}); err != nil {
				return err
			}
			c.evictDownloadURL(dstSt.FID)
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}

	sameParent := srcSt.ParentFID == dstParentFID
	sameName := srcSt.Name == dstName

	switch {
	case sameParent && !sameName:
		if err := c.RenameByFID(ctx, srcSt.FID, dstName); err != nil {
			return err
		}
	case !sameParent && sameName:
		if err := c.MoveByFID(ctx, []string{srcSt.FID}, dstParentFID); err != nil {
			return err
		}
	case !sameParent && !sameName:
		if err := c.MoveByFID(ctx, []string{srcSt.FID}, dstParentFID); err != nil {
			return err
		}
		if err := c.RenameByFID(ctx, srcSt.FID, dstName); err != nil {
			return err
		}
	}

	c.cache.invalidateSubtree(srcSt.ParentFID, srcClean)
	c.cache.invalidateSubtree(dstParentFID, dstClean)
	return nil
}

// About queries Quark/UC total and used storage capacity via /member.
func (c *Client) About(ctx context.Context) (*Quota, error) {
	q := url.Values{
		"fetch_subscribe": {"true"},
		"fetch_identity":  {"true"},
	}
	var resp memberResp
	if err := c.doJSON(ctx, http.MethodGet, "/member", q, nil, &resp); err != nil {
		return nil, err
	}
	free := resp.Data.TotalCapacity - resp.Data.UseCapacity
	if free < 0 {
		free = 0
	}
	return &Quota{
		Total:      resp.Data.TotalCapacity,
		Used:       resp.Data.UseCapacity,
		Free:       free,
		MemberType: resp.Data.MemberType,
	}, nil
}

// AccountInfo fetches account nickname/avatar from pan.quark.cn/account/info.
func (c *Client) AccountInfo(ctx context.Context) (*AccountInfo, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	rawURL := c.accountURL + "/account/info?fr=pc&platform=pc"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	c.applyCommonHeaders(req)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	c.MergeCookies(resp.Cookies())
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, ErrUnauthorized
	}
	var parsed accountInfoResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	if !parsed.Success && parsed.Data.Nickname == "" {
		return nil, ErrUnauthorized
	}
	return &AccountInfo{
		Nickname: strings.TrimSpace(parsed.Data.Nickname),
		Avatar:   strings.TrimSpace(parsed.Data.AvatarURI),
	}, nil
}

// GetDownloadURL resolves and short-term caches the direct CDN URL for fid
// with in-flight deduplication so multi-threaded Range downloads share one call.
func (c *Client) GetDownloadURL(ctx context.Context, fid string) (string, error) {
	if fid == "" {
		return "", os.ErrNotExist
	}
	c.dlCacheMu.Lock()
	if cached, ok := c.dlCache[fid]; ok && time.Now().Before(cached.ExpireAt) && cached.URL != "" {
		c.dlCacheMu.Unlock()
		return cached.URL, nil
	}
	if fl, exists := c.dlInFlight[fid]; exists {
		c.dlCacheMu.Unlock()
		select {
		case <-fl.done:
			return fl.url, fl.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	fl := &dlFlight{done: make(chan struct{})}
	c.dlInFlight[fid] = fl
	c.dlCacheMu.Unlock()

	dlURL, err := c.fetchDownloadURL(ctx, fid)

	c.dlCacheMu.Lock()
	delete(c.dlInFlight, fid)
	if err == nil && dlURL != "" {
		c.dlCache[fid] = cachedDownloadURL{
			URL:      dlURL,
			ExpireAt: time.Now().Add(c.dlTTL),
		}
	}
	c.dlCacheMu.Unlock()

	fl.url = dlURL
	fl.err = err
	close(fl.done)
	return dlURL, err
}

func (c *Client) evictDownloadURL(fid string) {
	if fid == "" {
		return
	}
	c.dlCacheMu.Lock()
	delete(c.dlCache, fid)
	c.dlCacheMu.Unlock()
}

func (c *Client) fetchDownloadURL(ctx context.Context, fid string) (string, error) {
	payload := map[string]any{
		"fids": []string{fid},
	}
	var resp downloadResp
	if err := c.doJSON(ctx, http.MethodPost, "/file/download", nil, payload, &resp); err != nil {
		return "", err
	}
	for _, item := range resp.Data {
		if item.DownloadURL != "" {
			return item.DownloadURL, nil
		}
	}
	return "", errors.New("未能获取夸克网盘文件下载直链")
}

// DownloadStream opens a streaming HTTP GET against the CDN download link for
// relPath, forwarding optional Range / If-Range headers and retrying once if a
// cached download URL returns HTTP 403/401.
func (c *Client) DownloadStream(ctx context.Context, relPath, rangeHeader, ifRange string) (io.ReadCloser, http.Header, int, error) {
	entry, err := c.Stat(ctx, relPath)
	if err != nil {
		return nil, nil, 0, err
	}
	if entry.IsDir {
		return nil, nil, http.StatusMethodNotAllowed, errors.New("无法下载目录")
	}
	if entry.Size == 0 {
		h := http.Header{}
		h.Set("Content-Length", "0")
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Accept-Ranges", "bytes")
		return io.NopCloser(strings.NewReader("")), h, http.StatusOK, nil
	}

	for attempt := 0; attempt < 2; attempt++ {
		dlURL, err := c.GetDownloadURL(ctx, entry.FID)
		if err != nil {
			return nil, nil, 0, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
		if err != nil {
			return nil, nil, 0, err
		}
		c.applyCommonHeaders(req)
		if rangeHeader != "" {
			req.Header.Set("Range", rangeHeader)
		}
		if ifRange != "" {
			req.Header.Set("If-Range", ifRange)
		}

		resp, err := c.streamClient.Do(req)
		if err != nil {
			return nil, nil, 0, err
		}
		c.MergeCookies(resp.Cookies())

		if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized) && attempt == 0 {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			c.evictDownloadURL(entry.FID)
			continue
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusNotFound {
				return nil, nil, http.StatusNotFound, os.ErrNotExist
			}
			return nil, nil, resp.StatusCode, fmt.Errorf("夸克 CDN 下载失败 (HTTP %d)", resp.StatusCode)
		}

		outHeaders := make(http.Header)
		for _, k := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
			if v := resp.Header.Get(k); v != "" {
				outHeaders.Set(k, v)
			}
		}
		if outHeaders.Get("Accept-Ranges") == "" {
			outHeaders.Set("Accept-Ranges", "bytes")
		}
		return resp.Body, outHeaders, resp.StatusCode, nil
	}
	return nil, nil, http.StatusForbidden, errors.New("夸克 CDN 直链鉴权失败")
}
