package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

const fileListPageWorkers = 8

type alistListRequest struct {
	Path    string `json:"path"`
	Refresh bool   `json:"refresh"`
	Page    int    `json:"page"`
	PerPage int    `json:"per_page"`
}

type alistPathRequest struct {
	Path string `json:"path"`
}

type alistRemoveRequest struct {
	Names []string `json:"names"`
	Dir   string   `json:"dir"`
}

type alistCopyMoveRequest struct {
	SrcDir    string   `json:"src_dir"`
	DstDir    string   `json:"dst_dir"`
	Overwrite bool     `json:"overwrite"`
	Names     []string `json:"names"`
}

func (c *AlistClient) FileListApiContext(ctx context.Context, path string, useCache int, scanInterval int) (FileListResult, error) {
	req := alistListRequest{
		Path:    path,
		Refresh: useCache != 1,
		Page:    1,
		PerPage: alistDeps.FileListPageSize,
	}
	result := make(FileListResult, alistDeps.FileListPageSize)
	// The rate limiter is applied per request inside fetchFileListPage: every
	// page is a separate AList request, so limiting only the first one let a
	// multi-page listing bypass scanInterval entirely.
	page, err := c.fetchFileListPage(ctx, req, result, scanInterval)
	if err != nil {
		return nil, err
	}
	if page.dropped > 0 {
		return nil, unsafeListedNameError(page.dropped)
	}
	n, total := page.n, page.total
	if !page.totalKnown {
		// Some AList-compatible servers omit total. A short first page is the
		// whole directory; a full one cannot be verified without paging, and the
		// page arithmetic needs the server's total — refuse instead of silently
		// truncating the directory (which in mirror mode deletes the rest).
		if n >= alistDeps.FileListPageSize {
			return nil, fmt.Errorf("AList directory listing did not report a total; refusing to trust a full first page of %d entries", n)
		}
		return result, nil
	}
	// An empty first page while the server reports entries is the same
	// inconsistency the emptyPage check below rejects for later pages: the
	// listing cannot be trusted. Accepting it as "directory is empty" is the
	// most dangerous possible reading — in mirror mode every destination entry
	// then looks extra and gets queued for deletion.
	if n == 0 && total > 0 {
		return nil, fmt.Errorf("AList directory listing is incomplete: page 1 of %d entries returned no content", total)
	}
	// Fail closed on a self-contradicting total. Treating "total <= n" as
	// "everything fit on the first page" silently truncates the directory when
	// the server understates total, and in mirror mode every destination entry
	// past the first page then looks extra and is queued for deletion.
	if total < 0 || total < n || (total == 0 && n > 0) {
		return nil, fmt.Errorf("AList directory listing is inconsistent: server reported total=%d but page 1 returned %d entries", total, n)
	}
	// A total above the entry cap is rejected before any page arithmetic: the
	// page count and the worker count are derived from total, and an absurd
	// total (a buggy or hostile server answering math.MaxInt64) overflowed that
	// arithmetic into a negative WaitGroup counter.
	if total > maxFileListEntries {
		return nil, fmt.Errorf("AList directory contains more than %d entries", maxFileListEntries)
	}
	if n == 0 || total <= n {
		// total == n normally means one page holds the whole directory. When
		// that page is exactly full, a server that understates total looks
		// identical, so confirm with one extra request before trusting it.
		if total == n && n >= alistDeps.FileListPageSize {
			probe := req
			probe.Page = 2
			probe.Refresh = false
			probeResult := make(FileListResult, alistDeps.FileListPageSize)
			probePage, probeErr := c.fetchFileListPage(ctx, probe, probeResult, scanInterval)
			if probeErr != nil {
				return nil, probeErr
			}
			if probePage.dropped > 0 {
				return nil, unsafeListedNameError(probePage.dropped)
			}
			if probePage.n > 0 {
				return nil, fmt.Errorf("AList directory listing is incomplete: server reported total=%d but page 2 returned %d entries", total, probePage.n)
			}
		}
		return result, nil
	}

	// Some AList drivers clamp the page size below what was requested. When the
	// first page comes back short while more entries exist, the clamp is the
	// observed page size and the page count must be derived from it — deriving
	// pages from the requested size would silently drop every later page.
	listPageSize := alistDeps.FileListPageSize
	if n < alistDeps.FileListPageSize {
		listPageSize = n
	}
	if fileListLimitExceeded(len(result)) {
		return nil, fmt.Errorf("AList directory contains more than %d entries", maxFileListEntries)
	}
	// The first response reveals the total entry count. Reserve the final map
	// capacity once for multi-page listings so merging pages does not repeatedly
	// grow and rehash the result map.
	resultCapacity := total
	if resultCapacity > maxFileListEntries {
		resultCapacity = maxFileListEntries
	}
	if resultCapacity > len(result)*2 {
		resized := make(FileListResult, resultCapacity)
		for name, meta := range result {
			resized[name] = meta
		}
		result = resized
	}

	pages := (total + listPageSize - 1) / listPageSize
	if pages > maxFileListPages {
		return nil, fmt.Errorf("AList directory listing exceeded %d pages", maxFileListPages)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var mu sync.Mutex
	var fetchErr error
	// emptyPage records the first page that came back with zero entries. With an
	// accurate total that can only happen on the final page; an empty page
	// earlier means the server's total does not match its content, and merging
	// what arrived would silently drop files.
	emptyPage := 0
	// fetchedTotal counts every entry the server returned across all pages, and
	// duplicateName records an entry that showed up on more than one page. Pages
	// are separate requests, so a driver whose ordering is not stable between
	// them can shift an entry across a page boundary: one page repeats it and
	// another never returns its neighbour. The map merge would hide both, and in
	// mirror mode the missing entry would then be deleted at the destination.
	// A shifted entry is repeated verbatim; a repeat with different metadata is
	// a second object under the same name (Google Drive and similar drivers
	// allow that), which is accepted just as it is within a single page.
	fetchedTotal := n
	duplicateName := ""
	var wg sync.WaitGroup
	fail := func(err error) {
		mu.Lock()
		if fetchErr == nil {
			fetchErr = err
			cancel()
		}
		mu.Unlock()
	}

	remainingPages := pages - 1
	workerCount := fileListPageWorkers
	if workerCount > remainingPages {
		workerCount = remainingPages
	}
	pageJobs := make(chan int)
	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case page, ok := <-pageJobs:
					if !ok {
						return
					}
					if ctx.Err() != nil {
						return
					}

					pageReq := req
					pageReq.Page = page
					// Only page 1 asks the driver to refresh. A refresh on every
					// page re-reads the storage backend per request, so each page
					// is cut from a different snapshot of the directory and entries
					// can slip between pages. Later pages read the cache page 1
					// just rebuilt, which keeps all pages on one listing.
					pageReq.Refresh = false
					// Request the page size the page count was derived from.
					// Asking for the original size again relies on the driver
					// clamping identically every time; if it does not, the
					// offsets shift and the listing is reported incomplete.
					pageReq.PerPage = listPageSize
					pageResult := make(FileListResult, listPageSize)
					fetched, pageErr := c.fetchFileListPage(ctx, pageReq, pageResult, scanInterval)
					if pageErr != nil {
						fail(pageErr)
						return
					}
					if fetched.dropped > 0 {
						fail(unsafeListedNameError(fetched.dropped))
						return
					}
					mu.Lock()
					fetchedTotal += fetched.n
					for name, meta := range pageResult {
						if prev, dup := result[name]; dup && prev == meta && duplicateName == "" {
							duplicateName = name
						}
						result[name] = meta
					}
					if fetched.n == 0 && page < pages && (emptyPage == 0 || page < emptyPage) {
						emptyPage = page
					}
					overCap := fileListLimitExceeded(len(result))
					mu.Unlock()
					if overCap {
						fail(fmt.Errorf("AList directory contains more than %d entries", maxFileListEntries))
						return
					}
				}
			}
		}()
	}

sendPages:
	for page := 2; page <= pages; page++ {
		select {
		case pageJobs <- page:
		case <-ctx.Done():
			break sendPages
		}
	}
	close(pageJobs)
	wg.Wait()
	if fetchErr != nil {
		return nil, fetchErr
	}
	// The parent context (task break/timeout) can cancel the workers between
	// requests; returning the partial map with a nil error would present an
	// incomplete directory listing as a successful one.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if emptyPage > 0 {
		return nil, fmt.Errorf("AList directory listing is incomplete: page %d of %d returned no entries (total=%d)", emptyPage, pages, total)
	}
	if duplicateName != "" {
		return nil, fmt.Errorf("AList directory listing is incomplete: entry %q was returned on more than one page (total=%d)", duplicateName, total)
	}
	if fetchedTotal != total {
		return nil, fmt.Errorf("AList directory listing is incomplete: fetched %d entries across %d pages, server reported total=%d", fetchedTotal, pages, total)
	}
	return result, nil
}

func fileListLimitExceeded(count int) bool {
	return count > maxFileListEntries
}

// fileListPage is one decoded /api/fs/list response.
type fileListPage struct {
	// n is how many entries the server returned. It deliberately includes the
	// entries counted by dropped: the merged FileListResult is then short of n,
	// and the completeness checks must see the server's answer, not the map.
	n     int
	total int
	// totalKnown is false when the response carried no "total" field. Such
	// servers cannot be paged safely (the page count is derived from total), so
	// a full first page is refused instead of silently truncating the listing.
	totalKnown bool
	code       int
	// message is AList's error message, kept so checkAlistCode can report it.
	message string
	// dropped counts entries whose names are not usable path components. They
	// are not merged into the result, so a non-zero value means the listing is
	// incomplete and callers must fail closed instead of syncing it.
	dropped int
}

func unsafeListedNameError(dropped int) error {
	if dropped == 1 {
		return fmt.Errorf("AList directory listing contains 1 entry whose name cannot be a path component; refusing to sync an incomplete listing")
	}
	return fmt.Errorf("AList directory listing contains %d entries whose name cannot be a path component; refusing to sync an incomplete listing", dropped)
}

func (c *AlistClient) fetchFileListPage(ctx context.Context, req alistListRequest, result FileListResult, scanInterval int) (fileListPage, error) {
	// Rate limit every page: AList sees each page as a separate request.
	if err := c.checkWaitContext(ctx, req.Path, scanInterval); err != nil {
		return fileListPage{}, err
	}
	resp, err := c.startRequest(ctx, http.MethodPost, "/api/fs/list", req, nil)
	if err != nil {
		return fileListPage{}, err
	}
	defer closeBody(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fileListPage{}, c.httpStatusError(resp)
	}
	page, err := decodeFileListResponse(resp.Body, alistDeps.MaxResponseBytes, result)
	if err != nil {
		return fileListPage{}, err
	}
	if err := c.checkAlistCode(page.code, page.message); err != nil {
		return fileListPage{}, err
	}
	return page, nil
}

func decodeFileListResponse(r io.Reader, limit int64, result FileListResult) (fileListPage, error) {
	var page fileListPage
	dec := json.NewDecoder(&capReader{r: r, limit: limit})
	if err := consumeDelim(dec, '{'); err != nil {
		return fileListPage{}, err
	}
	for dec.More() {
		key, keyErr := decodeObjectKey(dec)
		if keyErr != nil {
			return fileListPage{}, keyErr
		}
		switch key {
		case "code":
			if err := dec.Decode(&page.code); err != nil {
				return fileListPage{}, err
			}
		case "message":
			if err := dec.Decode(&page.message); err != nil {
				return fileListPage{}, err
			}
		case "data":
			data, err := decodeFileListData(dec, result)
			if err != nil {
				return fileListPage{}, err
			}
			page.n, page.total, page.dropped = data.n, data.total, data.dropped
			page.totalKnown = data.totalKnown
		default:
			if err := skipJSONValue(dec); err != nil {
				return fileListPage{}, err
			}
		}
	}
	if err := consumeDelim(dec, '}'); err != nil {
		return fileListPage{}, err
	}
	return page, nil
}

type fileListData struct {
	n          int
	total      int
	totalKnown bool
	dropped    int
}

func decodeFileListData(dec *json.Decoder, result FileListResult) (fileListData, error) {
	var data fileListData
	tok, err := dec.Token()
	if err != nil {
		return fileListData{}, err
	}
	if tok == nil {
		return fileListData{}, nil
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '{' {
		return fileListData{}, fmt.Errorf("AList list data is not an object")
	}
	for dec.More() {
		key, keyErr := decodeObjectKey(dec)
		if keyErr != nil {
			return fileListData{}, keyErr
		}
		switch key {
		case "content":
			data.n, data.dropped, err = decodeFileListContent(dec, result)
			if err != nil {
				return fileListData{}, err
			}
		case "total":
			if err = dec.Decode(&data.total); err != nil {
				return fileListData{}, err
			}
			data.totalKnown = true
		default:
			if err = skipJSONValue(dec); err != nil {
				return fileListData{}, err
			}
		}
	}
	return data, consumeDelim(dec, '}')
}

func decodeFileListContent(dec *json.Decoder, result FileListResult) (n, dropped int, err error) {
	tok, err := dec.Token()
	if err != nil {
		return 0, 0, err
	}
	if tok == nil {
		return 0, 0, nil
	}
	delim, ok := tok.(json.Delim)
	if !ok || delim != '[' {
		return 0, 0, fmt.Errorf("AList list content is not an array")
	}
	for dec.More() {
		var entry FileListEntry
		if err := dec.Decode(&entry); err != nil {
			return n, dropped, err
		}
		if addFileListEntry(result, entry) {
			dropped++
		}
		n++
	}
	return n, dropped, consumeDelim(dec, ']')
}

// addFileListEntry merges one listed entry into result. It reports whether the
// name had to be dropped: silently dropping used to let the completeness
// checks pass while the returned map was short of the server's total.
func addFileListEntry(result FileListResult, item FileListEntry) (dropped bool) {
	if !isSafeListedName(item.Name) {
		return true
	}
	if item.IsDir {
		result[item.Name+"/"] = FileMetadata{}
		return false
	}
	result[item.Name] = item.metadata()
	return false
}

// isSafeListedName rejects entry names that are not plain file names. Names come
// from the AList server, which may itself be proxying untrusted third-party
// storage, and they are concatenated onto directory paths when building scan
// work. A name carrying a separator or a parent reference would address a
// different directory than the one being scanned.
func isSafeListedName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, "/\\")
}

func consumeDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	got, ok := tok.(json.Delim)
	if !ok || got != want {
		return fmt.Errorf("expected JSON delimiter %q, got %v", want, tok)
	}
	return nil
}

func decodeObjectKey(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	key, ok := tok.(string)
	if !ok {
		return "", fmt.Errorf("expected JSON object key, got %v", tok)
	}
	return key, nil
}

const maxJSONSkipDepth = 64

func skipJSONValue(dec *json.Decoder) error {
	return skipJSONValueDepth(dec, 0)
}

func skipJSONValueDepth(dec *json.Decoder, depth int) error {
	if depth > maxJSONSkipDepth {
		return fmt.Errorf("JSON nesting exceeds max depth %d", maxJSONSkipDepth)
	}
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		for dec.More() {
			if _, err := dec.Token(); err != nil {
				return err
			}
			if err := skipJSONValueDepth(dec, depth+1); err != nil {
				return err
			}
		}
		return consumeDelim(dec, '}')
	case '[':
		for dec.More() {
			if err := skipJSONValueDepth(dec, depth+1); err != nil {
				return err
			}
		}
		return consumeDelim(dec, ']')
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delim)
	}
}
