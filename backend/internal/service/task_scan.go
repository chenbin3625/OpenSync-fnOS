package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"path"
	"strings"
	"sync"
	"time"
)

type scanWorkMode int

const (
	scanWorkCompare scanWorkMode = iota
	scanWorkMissingDst
)

type scanWork struct {
	SrcPath     string
	DstPath     string
	SrcRootPath string
	DstRootPath string
	FirstDst    bool
	Mode        scanWorkMode
	Counted     bool
}

func (jt *JobTask) acquireScanSlot() bool {
	if jt.isBreak() {
		return false
	}
	jt.initRuntime()
	// Block on the semaphore instead of polling every 50ms. The context is
	// cancelled on break/timeout (requestBreak / task timeout), so the wait
	// still propagates cancellation promptly.
	select {
	case jt.scanSem <- struct{}{}:
		if jt.isBreak() {
			<-jt.scanSem
			return false
		}
		return true
	case <-jt.context().Done():
		return false
	}
}

func (jt *JobTask) releaseScanSlot() {
	<-jt.scanSem
}

func (jt *JobTask) tryAcquireScanBranchSlot() bool {
	jt.initRuntime()
	if jt.isBreak() {
		return false
	}
	// Never wait for a slot. The caller falls back to scanning the branch
	// inline, which is what keeps the recursion deadlock-free (a parent holding
	// a slot never blocks on its own children). Any wait here is paid once per
	// subdirectory while the budget is saturated, so even a short timeout turns
	// a directory with thousands of children into minutes of idle sleeping.
	select {
	case jt.scanBranchSem <- struct{}{}:
		return true
	default:
		return false
	}
}

func (jt *JobTask) releaseScanBranchSlot() {
	<-jt.scanBranchSem
}

func (jt *JobTask) beginScanWork(work scanWork) {
	if !work.Counted {
		jt.ScanTotalDirs.Add(1)
	}
}

func (jt *JobTask) addChildScanWork(children *[]scanWork, work scanWork) {
	work.Counted = true
	jt.ScanTotalDirs.Add(1)
	*children = append(*children, work)
}

func (jt *JobTask) finishScanWork() {
	jt.ScanDoneDirs.Add(1)
	jt.notifyProgressChange()
}

func (jt *JobTask) sync() {
	if jt.hasRetrySource() {
		jt.syncRetryItems()
		return
	}

	srcPaths := parsePathList(jt.Job["srcPath"])
	jobExclude := jt.Job["exclude"]

	var spec *excludeMatcher
	if jobExclude != nil {
		excludeStr := fmt.Sprintf("%v", jobExclude)
		if excludeStr != "" {
			spec = newExcludeMatcher(excludeStr)
		}
	}

	dstPaths := parsePathList(jt.Job["dstPath"])
	if util.ToInt(jt.Job["method"]) == 2 && len(dstPaths) != 1 {
		errMsg := "移动模式只允许一个目标目录"
		jt.CopyHook("", "", "", nil, "", taskStatusFailed, &errMsg, taskItemFile, taskItemTypeTransfer, time.Now().Unix())
		jt.markScanFinished()
		return
	}
	fullSync := util.ToInt(jt.Job["method"]) == 1

	// Collect all top-level (src, dst) pairs.
	var works []scanWork
	for _, srcItem := range srcPaths {
		srcItem = normalizeDirPath(srcItem)
		for i, dstItem := range dstPaths {
			dstItem = normalizeDirPath(dstItem)
			resolvedDstPath := dstPathForSrcSelection(dstItem, srcItem, srcPaths)
			works = append(works, scanWork{
				SrcPath:     srcItem,
				DstPath:     resolvedDstPath,
				SrcRootPath: srcItem,
				DstRootPath: resolvedDstPath,
				FirstDst:    i == 0,
				Mode:        scanWorkCompare,
			})
		}
	}

	// Two phases. The first destination of every source refreshes the source
	// listing; the other destinations reuse it (listDir's useCache), so they
	// may only start once those scans have returned. Starting every pair at
	// once made "not the first destination ⇒ read the cache" a race against the
	// refresh: a stale cached listing decides which destination entries are
	// extra and, in mirror mode, gets them deleted.
	var firstDstWorks, laterWorks []scanWork
	for _, work := range works {
		if work.FirstDst {
			firstDstWorks = append(firstDstWorks, work)
		} else {
			laterWorks = append(laterWorks, work)
		}
	}
	jt.runScanRoots(firstDstWorks, spec, fullSync)
	if !jt.isBreak() {
		jt.runScanRoots(laterWorks, spec, fullSync)
	}
	jt.markScanFinished()
}

// runScanRoots runs top-level pairs concurrently so independent source and
// destination trees overlap their network I/O instead of waiting in sequence.
func (jt *JobTask) runScanRoots(works []scanWork, spec *excludeMatcher, fullSync bool) {
	var wg sync.WaitGroup
	for _, work := range works {
		work := work
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer jt.recoverWorkerPanic("scan-root", nil)
			if fullSync {
				jt.syncFull(work, spec)
			} else {
				jt.runScanWork(work, spec)
			}
		}()
	}
	wg.Wait()
}

func (jt *JobTask) hasRetrySource() bool {
	return jt.RetrySourceTaskID > 0
}

func (jt *JobTask) markScanFinished() {
	jt.ScanFinish.Store(true)
	jt.notifyProgressChange()
}

func (jt *JobTask) syncRetryItems() {
	statuses := taskStatusValues(jt.RetryStatuses...)
	// Snapshot how many items the source task advertises before replaying. A
	// concurrent deletion of the source task's history makes
	// ForEachJobTaskItemsByStatuses stop early, and the replayed prefix used to
	// look exactly like "everything was retried" — no error, no log, and a task
	// status that claimed the failures were fixed.
	expected, countErr := jobDeps.CountJobTaskItemsByStatuses(jt.RetrySourceTaskID, statuses)
	replayed := 0
	err := jobDeps.ForEachJobTaskItemsByStatuses(jt.RetrySourceTaskID, statuses, retryTaskItemBatchSize, func(items []map[string]interface{}) error {
		for _, item := range items {
			if jt.isBreak() {
				return errScanAborted
			}
			replayed++
			func() {
				defer func() {
					if r := recover(); r != nil {
						errMsg := fmt.Sprintf("retry item panic: %v", r)
						log.Printf("Task %d %s", jt.TaskID, errMsg)
						srcPath := util.StringValue(item["srcPath"])
						dstPath := util.StringValue(item["dstPath"])
						fileName := util.StringValue(item["fileName"])
						jt.CopyHook(srcPath, dstPath, fileName, item["fileSize"], "", taskStatusFailed, &errMsg, taskItemPath, taskItemTypeCopy, time.Now().Unix())
					}
				}()
				jt.retryTaskItem(item)
			}()
		}
		return nil
	})
	if err != nil && !errors.Is(err, errScanAborted) {
		errMsg := err.Error()
		jt.CopyHook("", "", "", nil, "", taskStatusFailed, &errMsg, taskItemPath, taskItemTypeCopy, time.Now().Unix())
	}
	if countErr != nil {
		log.Printf("Task %d could not count the retry source items of task %d: %v", jt.TaskID, jt.RetrySourceTaskID, countErr)
	} else if err == nil && int64(replayed) < expected {
		errMsg := fmt.Sprintf("retry source task %d lost items while this retry was running: %d of %d replayed (its history was deleted concurrently)",
			jt.RetrySourceTaskID, replayed, expected)
		log.Printf("Task %d %s", jt.TaskID, errMsg)
		jt.CopyHook("", "", "", nil, "", taskStatusFailed, &errMsg, taskItemPath, taskItemTypeCopy, time.Now().Unix())
	}
	jt.markScanFinished()
}

func (jt *JobTask) retryTaskItem(item map[string]interface{}) {
	copyType := taskItemTypeFromValue(item["type"])
	if copyType == taskItemTypeMove && util.ToInt(jt.Job["method"]) == 2 {
		copyType = taskItemTypeTransfer
	}
	isPath := taskItemObjectFromValue(item["isPath"]) == taskItemPath
	srcPath := util.StringValue(item["srcPath"])
	dstPath := util.StringValue(item["dstPath"])
	fileName := util.StringValue(item["fileName"])
	fileSize := item["fileSize"]

	switch copyType {
	case taskItemTypeDelete:
		// A delete record is only re-executable when it names one concrete entry.
		// The full-sync safety valve records its "delete phase skipped" notice as
		// a failed delete of the destination root with no name; replaying that as
		// DeleteFileContext(root, [""]) would ask AList to remove the root itself.
		// Re-record it, keeping the original explanation, instead of deleting.
		if !isSafeDeleteTargetName(fileName) {
			reason := taskItemErrMsg(item["errMsg"])
			if reason == "" {
				reason = unsafeDeleteTargetMessage(dstPath, fileName)
			}
			log.Printf("Task %d refused to retry delete of %q in %q: not a single entry name", jt.TaskID, fileName, dstPath)
			jt.DelHook(dstPath, fileName, nil, taskStatusFailed, &reason, boolToTaskItemObject(isPath), time.Now().Unix())
			return
		}
		// Even a well-formed delete record is not replayed. "This entry is extra"
		// was decided by the mirror run that recorded it; if the source has since
		// regained that file, replaying the stale record would delete a file the
		// destination is supposed to have. The next mirror run re-decides
		// deletions from a fresh source snapshot.
		reason := "delete record is not replayed: the next mirror run re-decides deletions from a fresh source scan"
		log.Printf("Task %d refused to replay delete of %q in %q", jt.TaskID, fileName, dstPath)
		jt.DelHook(dstPath, fileName, nil, taskStatusFailed, &reason, boolToTaskItemObject(isPath), time.Now().Unix())
	case taskItemTypeCopy, taskItemTypeMove, taskItemTypeTransfer:
		if isPath {
			jt.retryMkdir(srcPath, dstPath, copyType)
			return
		}
		jt.queueCopyFile(srcPath, dstPath, fileName, fileSize, copyType)
	default:
		errMsg := fmt.Sprintf("unsupported retry task type: %d", copyType)
		jt.CopyHook(srcPath, dstPath, fileName, fileSize, "", taskStatusFailed, &errMsg, boolToTaskItemObject(isPath), copyType, time.Now().Unix())
	}
}

func (jt *JobTask) retryMkdir(srcPath, dstPath string, copyType taskItemType) {
	if dstPath == "" {
		errMsg := "missing destination path for directory retry"
		jt.CopyHook(srcPath, dstPath, "", nil, "", taskStatusFailed, &errMsg, taskItemPath, copyType, time.Now().Unix())
		return
	}
	jt.mkdirAndRecord(srcPath, dstPath, copyType)
}

func (jt *JobTask) mkdirAndRecord(srcPath, dstPath string, copyType taskItemType) taskStatus {
	status := taskStatusSuccess
	var errMsg *string
	scanIntervalT := util.ToInt(jt.Job["scanIntervalT"])
	if err := jt.AlistClient.MkdirContext(jt.context(), dstPath, scanIntervalT); err != nil {
		status = taskStatusFailed
		e := err.Error()
		errMsg = &e
	}
	jt.CopyHook(srcPath, dstPath, "", nil, "", status, errMsg, taskItemPath, copyType, time.Now().Unix())
	return status
}

func dstPathForSrcSelection(dstPath, srcPath string, srcPaths []string) string {
	dstPath = normalizeDirPath(dstPath)
	if len(srcPaths) <= 1 {
		return dstPath
	}

	suffix := srcSelectionSuffix(srcPath, srcPaths)
	if suffix == "." || suffix == "/" || suffix == "" {
		return dstPath
	}
	return normalizeDirPath(dstPath + suffix)
}

func srcSelectionSuffix(srcPath string, srcPaths []string) string {
	cleanSrc := path.Clean(strings.TrimSpace(srcPath))
	// A partially checked tree branch arrives as sibling paths rather than its
	// parent, so the shared parent is the highest segment the selection already
	// accounts for: everything above it is stripped, everything below it is kept
	// so nested branches still reach the destination with their hierarchy.
	commonParent := commonSrcSelectionParent(srcPaths)
	if commonParent == "" || commonParent == "." || commonParent == "/" {
		// Sources with no shared parent keep the historical base-name layout,
		// but only while the base names stay unique. Two selections that share
		// one (/a/docs and /b/docs) would otherwise collapse into the same
		// destination directory and, in mirror mode, delete each other's files
		// on every run. Those fall back to the full path instead.
		if srcSelectionBaseNameIsAmbiguous(cleanSrc, srcPaths) {
			return strings.TrimPrefix(cleanSrc, "/")
		}
		return path.Base(cleanSrc)
	}

	// A selected path that *is* the shared parent has no suffix of its own.
	// Without this the TrimPrefix below misses (the prefix carries a trailing
	// slash the path lacks) and the leftover leading slash produces "dst//src".
	if cleanSrc == commonParent {
		return ""
	}

	return strings.TrimPrefix(cleanSrc, normalizeDirPath(commonParent))
}

// srcSelectionBaseNameIsAmbiguous reports whether another selected source path
// resolves to the same base name as cleanSrc.
func srcSelectionBaseNameIsAmbiguous(cleanSrc string, srcPaths []string) bool {
	base := path.Base(cleanSrc)
	for _, other := range srcPaths {
		cleanOther := path.Clean(strings.TrimSpace(other))
		if cleanOther == cleanSrc {
			continue
		}
		if path.Base(cleanOther) == base {
			return true
		}
	}
	return false
}

// commonSrcSelectionParent returns the deepest directory that contains every
// selected source path. Callers subtract exactly this prefix, so the value has
// to be the common parent of the paths themselves — not of their parent
// directories, which would leave one extra level in the destination path.
func commonSrcSelectionParent(srcPaths []string) string {
	if len(srcPaths) < 2 {
		return ""
	}

	common := path.Clean(strings.TrimSpace(srcPaths[0]))
	for _, srcPath := range srcPaths[1:] {
		selected := path.Clean(strings.TrimSpace(srcPath))
		for common != "." && common != "/" && selected != common && !strings.HasPrefix(selected, normalizeDirPath(common)) {
			common = path.Dir(common)
		}
	}
	return common
}

func normalizeDirPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	return path
}

func (jt *JobTask) copyFile(srcPath, dstPath, fileName string, fileSize interface{}) {
	if jt.isBreak() {
		return
	}
	method := util.ToInt(jt.Job["method"])
	copyType := taskItemTypeCopy
	if method == 2 {
		copyType = taskItemTypeTransfer
	}
	jt.queueCopyFile(srcPath, dstPath, fileName, fileSize, copyType)
}

func (jt *JobTask) queueCopyFile(srcPath, dstPath, fileName string, fileSize interface{}, copyType taskItemType) {
	if jt.isBreak() {
		return
	}
	ci := newCopyItem(jt, jt.AlistClient, srcPath, dstPath, fileName, fileSize, copyType)
	if !jt.Waiting.pushWait(jt.context(), ci) {
		ci.setStatus(taskStatusStopped)
		jt.CopyHook(ci.SrcPath, ci.DstPath, ci.FileName, ci.FileSize, ci.AlistTaskID,
			ci.Status, ci.ErrMsg, taskItemFile, ci.CopyType, ci.CreateTime)
	}
}

// isSafeDeleteTargetName reports whether fileName (optionally carrying the
// directory suffix) names exactly one entry inside its directory. AList's remove
// API joins dir and name, so an empty name, "." or ".." addresses the directory
// itself or its parent, and a separator reaches into another directory.
func isSafeDeleteTargetName(fileName string) bool {
	return isSafeListedName(strings.TrimSuffix(fileName, "/"))
}

// taskItemErrMsg reads a stored errMsg column. Rows loaded from the database
// carry a string; in-memory item maps (JobTaskItem.ToMap) carry a *string.
func taskItemErrMsg(value interface{}) string {
	switch v := value.(type) {
	case *string:
		if v == nil {
			return ""
		}
		return *v
	case nil:
		return ""
	default:
		return util.StringValue(v)
	}
}

// unsafeDeleteTargetMessage explains a refused delete.
func unsafeDeleteTargetMessage(dir, fileName string) string {
	return msg.UnsafeDeleteTarget(fileName, dir)
}

func (jt *JobTask) delFile(path, fileName string, size interface{}) taskStatus {
	if jt.isBreak() {
		return taskStatusStopped
	}
	isPath := strings.HasSuffix(fileName, "/")
	status := taskStatusSuccess
	var errMsg *string
	createTime := time.Now().Unix()
	if !isSafeDeleteTargetName(fileName) {
		reason := unsafeDeleteTargetMessage(path, fileName)
		log.Printf("Task %d %s", jt.TaskID, reason)
		jt.DelHook(path, fileName, nil, taskStatusFailed, &reason, boolToTaskItemObject(isPath), createTime)
		return taskStatusFailed
	}

	name := fileName
	if isPath {
		name = fileName[:len(fileName)-1]
	}
	scanIntervalT := util.ToInt(jt.Job["scanIntervalT"])
	err := jt.AlistClient.DeleteFileContext(jt.context(), path, []string{name}, scanIntervalT)
	if err != nil {
		status = taskStatusFailed
		e := err.Error()
		errMsg = &e
	}

	var delSize interface{}
	if !isPath {
		delSize = size
	}
	jt.DelHook(path, fileName, delSize, status, errMsg, boolToTaskItemObject(isPath), createTime)
	return status
}

// queueDelFile enqueues a delete operation into the waiting queue so the
// executor goroutine runs it concurrently, just like copy/move items.
func (jt *JobTask) queueDelFile(path, fileName string, size interface{}, isPath bool) {
	if jt.isBreak() {
		return
	}
	if !isSafeDeleteTargetName(fileName) {
		reason := unsafeDeleteTargetMessage(path, fileName)
		log.Printf("Task %d %s", jt.TaskID, reason)
		jt.DelHook(path, fileName, nil, taskStatusFailed, &reason, boolToTaskItemObject(isPath), time.Now().Unix())
		return
	}
	name := fileName
	if isPath {
		name = strings.TrimSuffix(fileName, "/")
	}
	var delSize interface{}
	if !isPath {
		delSize = size
	}
	ci := newCopyItem(jt, jt.AlistClient, "", path, name, delSize, taskItemTypeDelete)
	ci.IsPath = boolToTaskItemObject(isPath)
	if !jt.Waiting.pushWait(jt.context(), ci) {
		ci.setStatus(taskStatusStopped)
		jt.DelHook(path, fileName, delSize, taskStatusStopped, nil, boolToTaskItemObject(isPath), ci.CreateTime)
	}
}

func (jt *JobTask) listDir(path string, firstDst bool, spec *excludeMatcher, rootPath string, isSrc bool) (FileListResult, error) {
	var useCache int
	switch {
	case isSrc && util.ToInt(jt.Job["method"]) == 1:
		// A mirror run derives its deletes from this listing, so a stale
		// server-side cache would delete newly added files or recreate removed
		// ones. Never read the cache for the source of a full sync.
		useCache = 0
	case isSrc && !firstDst:
		// Reuse the listing the first destination refreshed. sync() starts the
		// first-destination scans first and waits for them, so the refresh is
		// already done by the time a later destination runs.
		useCache = 1
	case isSrc:
		useCache = util.ToInt(jt.Job["useCacheS"])
	default:
		useCache = util.ToInt(jt.Job["useCacheT"])
	}

	var scanInterval int
	if isSrc {
		scanInterval = util.ToInt(jt.Job["scanIntervalS"])
	} else {
		scanInterval = util.ToInt(jt.Job["scanIntervalT"])
	}

	if !jt.acquireScanSlot() {
		return nil, errScanAborted
	}
	defer jt.releaseScanSlot()

	var result FileListResult
	var err error
	for attempt := 0; attempt <= maxScanListRetries; attempt++ {
		result, err = jt.AlistClient.FileListApiContext(jt.context(), path, useCache, scanInterval)
		if err == nil || !shouldRetryScanList(jt.context(), err) || attempt == maxScanListRetries {
			break
		}
		log.Printf("Directory scan failed for %q; retrying (%d/%d): %v", path, attempt+1, maxScanListRetries, err)
		if !jt.waitForBreak(jobDeps.ScanListRetryDelay(attempt)) {
			// Keep both causes: returning a bare context.Canceled hid the
			// original failure and made a task timeout look like a user stop.
			return nil, errors.Join(jt.context().Err(), err)
		}
	}
	if err != nil {
		if jt.isBreak() && errors.Is(err, context.Canceled) {
			return nil, err
		}
		// A missing destination root is not a failure yet: the caller creates it
		// and retries (ensureDstDirAfterListError, which records the failure if
		// that does not work). Recording it here made every first mirror run end
		// as "partial failure" with a misleading scan error.
		if !isSrc && isAlistObjectNotFound(err) && sameListedDir(path, rootPath) {
			return nil, err
		}
		jt.recordScanFailure(isSrc, path, err)
		return nil, err
	}

	// Apply exclude rules
	if spec != nil && len(result) > 0 {
		filtered := make(FileListResult, len(result))
		for key, val := range result {
			checkPath := excludeMatchPath(rootPath, path, key)
			if !spec.matchesListed(checkPath, strings.HasSuffix(key, "/")) {
				filtered[key] = val
			}
		}
		return filtered, nil
	}

	return result, nil
}

func shouldRetryScanList(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return false
	}
	// "The object does not exist" is repaired by creating the destination
	// directory, never by waiting: retrying only delayed the first sync by the
	// whole backoff, and could not succeed.
	if isAlistObjectNotFound(err) {
		return false
	}
	errMsg := err.Error()
	return errMsg != msg.T(msg.AlistUnAuth) && errMsg != msg.T(msg.AddressIncorrect)
}

func defaultScanListRetryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	return time.Duration(1<<attempt) * time.Second
}

func excludeMatchPath(rootPath, currentPath, name string) string {
	relDir := strings.TrimPrefix(currentPath, rootPath)
	relDir = strings.Trim(relDir, "/")
	if relDir == "" {
		return name
	}
	return relDir + "/" + name
}

// recordScanFailure records one failed path item for a directory that could not
// be listed (or created).
func (jt *JobTask) recordScanFailure(isSrc bool, path string, err error) {
	srcOrDst := msg.T(msg.Src)
	if !isSrc {
		srcOrDst = msg.T(msg.Dst)
	}
	errMsg := msg.ScanError(srcOrDst, err.Error())
	log.Printf("%s", errMsg)
	jt.CopyHook(pathIfTrue(isSrc, path), pathIfTrue(!isSrc, path), "", nil, "", taskStatusFailed, &errMsg, taskItemPath, taskItemTypeCopy, time.Now().Unix())
}

// sameListedDir reports whether path is the root the caller listed for, which is
// the only listing whose "does not exist" error the caller can repair itself.
func sameListedDir(path, rootPath string) bool {
	return normalizeDirPath(path) == normalizeDirPath(rootPath)
}

func pathIfTrue(cond bool, path string) string {
	if cond {
		return path
	}
	return ""
}

func (jt *JobTask) listSrcAndDst(srcPath, dstPath string, spec *excludeMatcher, srcRootPath, dstRootPath string, firstDst bool) (FileListResult, FileListResult, error) {
	var srcFiles, dstFiles FileListResult
	var srcErr, dstErr error

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer jt.recoverWorkerPanic("source scan", &srcErr)
		srcFiles, srcErr = jt.listDir(srcPath, firstDst, spec, srcRootPath, true)
	}()
	go func() {
		defer wg.Done()
		defer jt.recoverWorkerPanic("destination scan", &dstErr)
		dstFiles, dstErr = jt.listDir(dstPath, firstDst, spec, dstRootPath, false)
	}()
	wg.Wait()

	if srcErr != nil {
		return nil, nil, srcErr
	}
	if dstErr != nil {
		// A destination directory that does not exist yet is not a failure: the
		// job's whole purpose is to populate it. Create it and retry once so the
		// run proceeds instead of reporting "destination scan failed".
		if !jt.ensureDstDirAfterListError(dstPath, dstErr) {
			return nil, nil, dstErr
		}
		dstFiles, dstErr = jt.listDir(dstPath, firstDst, spec, dstRootPath, false)
		if dstErr != nil {
			return nil, nil, dstErr
		}
	}
	if srcFiles == nil {
		srcFiles = make(FileListResult)
	}
	if dstFiles == nil {
		dstFiles = make(FileListResult)
	}
	return srcFiles, dstFiles, nil
}

// ensureDstDirAfterListError creates dstPath when listErr says it does not
// exist. It reports whether the caller should retry the listing. Any other error
// (auth, network, permissions) is left alone so real failures still surface.
func (jt *JobTask) ensureDstDirAfterListError(dstPath string, listErr error) bool {
	if listErr == nil || !isAlistObjectNotFound(listErr) || jt.isBreak() {
		return false
	}
	scanIntervalT := util.ToInt(jt.Job["scanIntervalT"])
	if err := jt.AlistClient.MkdirContext(jt.context(), dstPath, scanIntervalT); err != nil {
		log.Printf("Failed to create missing destination directory %q: %v", dstPath, err)
		// listDir suppresses the "root does not exist" failure because this
		// repair may succeed. Now that it did not, the failure has to be
		// recorded here or the run would end as "nothing to sync" with no error.
		jt.recordScanFailure(false, dstPath, fmt.Errorf("create missing destination directory: %w", err))
		return false
	}
	return true
}

func (jt *JobTask) runScanWork(work scanWork, spec *excludeMatcher) {
	if work.Mode == scanWorkMissingDst {
		jt.syncWithoutHave(work, spec)
		return
	}
	jt.syncWithHave(work, spec)
}

func (jt *JobTask) runChildScanWorks(children []scanWork, spec *excludeMatcher) {
	if len(children) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, child := range children {
		child := child
		if jt.tryAcquireScanBranchSlot() {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer jt.releaseScanBranchSlot()
				defer jt.recoverWorkerPanic("child scan", nil)
				jt.runScanWork(child, spec)
			}()
			continue
		}
		jt.runScanWork(child, spec)
	}
	wg.Wait()
}

func (jt *JobTask) syncWithHave(work scanWork, spec *excludeMatcher) {
	jt.beginScanWork(work)
	if jt.isBreak() {
		jt.finishScanWork()
		return
	}

	srcFiles, dstFiles, err := jt.listSrcAndDst(work.SrcPath, work.DstPath, spec, work.SrcRootPath, work.DstRootPath, work.FirstDst)
	if err != nil {
		jt.finishScanWork()
		return
	}

	children := make([]scanWork, 0)
	dstIndex := newDstNameMatchIndex(dstFiles)
	srcIndex := newSrcNameMatchIndex(srcFiles)
	for key, srcVal := range srcFiles {
		if jt.isBreak() {
			break
		}
		if !strings.HasSuffix(key, "/") {
			// File
			srcSize := fileSize(srcVal)
			if !jobAllowsFileSize(jt.Job, srcSize) {
				continue
			}
			_, dstVal, exists := dstIndex.find(key, srcIndex)
			if util.ToInt(jt.Job["method"]) == 2 || !exists || fileChanged(srcVal, dstVal) {
				if dstKey, conflict := dstIndex.findDirForFile(key); conflict {
					// The destination holds a directory under the file's name. A
					// copy cannot replace it, so report the type conflict instead
					// of queueing a transfer that is bound to fail.
					jt.recordScanConflict(work.SrcPath+key, work.DstPath+dstKey, key, false,
						fmt.Sprintf("destination %q is a directory but the source entry is a file", work.DstPath+dstKey))
					continue
				}
				jt.copyFile(work.SrcPath, work.DstPath, key, srcSize)
			}
		} else {
			// Directory
			dstKey, _, exists := dstIndex.find(key, srcIndex)
			if !exists {
				if conflictKey, conflict := dstIndex.findFileForDir(key); conflict {
					// The destination already holds a *file* under this directory
					// name. Creating the directory fails, and the old code then
					// skipped the whole subtree with only a mkdir error. Report the
					// conflict explicitly; the next mirror run (which owns deletion
					// authority) resolves it.
					jt.recordScanConflict(work.SrcPath+key, work.DstPath+conflictKey, "", true,
						fmt.Sprintf("destination %q is a file but the source entry is a directory", work.DstPath+conflictKey))
					continue
				}
				jt.addChildScanWork(&children, scanWork{
					SrcPath:     work.SrcPath + key,
					DstPath:     work.DstPath + key,
					SrcRootPath: work.SrcRootPath,
					DstRootPath: work.DstRootPath,
					FirstDst:    work.FirstDst,
					Mode:        scanWorkMissingDst,
				})
			} else {
				jt.addChildScanWork(&children, scanWork{
					SrcPath:     work.SrcPath + key,
					DstPath:     work.DstPath + dstKey,
					SrcRootPath: work.SrcRootPath,
					DstRootPath: work.DstRootPath,
					FirstDst:    work.FirstDst,
					Mode:        scanWorkCompare,
				})
			}
		}
	}

	if jt.isBreak() {
		jt.finishScanWork()
		jt.runChildScanWorks(children, spec)
		return
	}

	jt.finishScanWork()
	jt.runChildScanWorks(children, spec)
}

// recordScanConflict records one explicit failed item for a source/destination
// type mismatch (a directory where a file is expected, or the other way round).
// The incremental scanner has no delete authority — that belongs to the
// full-sync plan — so the branch is reported and skipped instead of being failed
// later with a misleading mkdir/copy error.
func (jt *JobTask) recordScanConflict(srcPath, dstPath, fileName string, isPath bool, reason string) {
	errMsg := fmt.Sprintf("scan conflict: %s", reason)
	log.Printf("Task %d %s (src %q, dst %q)", jt.TaskID, errMsg, srcPath, dstPath)
	jt.CopyHook(srcPath, dstPath, fileName, nil, "", taskStatusFailed, &errMsg, boolToTaskItemObject(isPath), taskItemTypeCopy, time.Now().Unix())
}

// findDirForFile reports the destination *directory* that occupies the path of a
// source file ("a.txt/" for "a.txt"). canonicalFileKey keeps the trailing slash,
// so find() never pairs a file with a directory.
func (i dstNameMatchIndex) findDirForFile(fileKey string) (string, bool) {
	name := strings.TrimSuffix(fileKey, "/")
	if _, ok := i.items[name+"/"]; ok {
		return name + "/", true
	}
	entry, ok := i.canonical[canonicalFileName(name)+"/"]
	if !ok || entry.ambiguous || !strings.HasSuffix(entry.key, "/") {
		return "", false
	}
	return entry.key, true
}

// findFileForDir reports the destination *file* that occupies the path of a
// source directory ("foo" for "foo/").
func (i dstNameMatchIndex) findFileForDir(dirKey string) (string, bool) {
	name := strings.TrimSuffix(dirKey, "/")
	if _, ok := i.items[name]; ok {
		return name, true
	}
	entry, ok := i.canonical[canonicalFileName(name)]
	if !ok || entry.ambiguous || strings.HasSuffix(entry.key, "/") {
		return "", false
	}
	return entry.key, true
}

func (jt *JobTask) syncWithoutHave(work scanWork, spec *excludeMatcher) {
	jt.beginScanWork(work)
	if jt.isBreak() {
		jt.finishScanWork()
		return
	}

	status := jt.mkdirAndRecord(work.SrcPath, work.DstPath, taskItemTypeCopy)
	if status != taskStatusSuccess {
		jt.finishScanWork()
		return
	}

	srcFiles, err := jt.listDir(work.SrcPath, work.FirstDst, spec, work.SrcRootPath, true)
	if err != nil {
		jt.finishScanWork()
		return
	}

	children := make([]scanWork, 0)
	for key, srcVal := range srcFiles {
		if jt.isBreak() {
			break
		}
		if strings.HasSuffix(key, "/") {
			jt.addChildScanWork(&children, scanWork{
				SrcPath:     work.SrcPath + key,
				DstPath:     work.DstPath + key,
				SrcRootPath: work.SrcRootPath,
				DstRootPath: work.DstRootPath,
				FirstDst:    work.FirstDst,
				Mode:        scanWorkMissingDst,
			})
		} else {
			srcSize := fileSize(srcVal)
			if !jobAllowsFileSize(jt.Job, srcSize) {
				continue
			}
			jt.copyFile(work.SrcPath, work.DstPath, key, srcSize)
		}
	}
	jt.finishScanWork()
	jt.runChildScanWorks(children, spec)
}

func jobAllowsFileSize(job map[string]interface{}, size int64) bool {
	minSize := util.ToInt64(job["minFileSize"])
	maxSize := util.ToInt64(job["maxFileSize"])
	if minSize > 0 && size < minSize {
		return false
	}
	if maxSize > 0 && size > maxSize {
		return false
	}
	return true
}

func fileChanged(srcVal, dstVal interface{}) bool {
	src := toFileMetadata(srcVal)
	dst := toFileMetadata(dstVal)
	// Size is checked first and unconditionally: differing sizes always mean the
	// file changed, whatever the hashes claim. Only then is a hash comparison
	// allowed to declare same-size files identical, and only for well-formed
	// digests -- some drivers return a constant placeholder for every file, which
	// would otherwise make every changed file look unchanged.
	if src.Size != dst.Size {
		return true
	}
	if isMD5Digest(src.MD5) && isMD5Digest(dst.MD5) {
		return src.MD5 != dst.MD5
	}
	// No usable digest on at least one side. Without a further signal a
	// same-size in-place edit is invisible, and many drivers never return a
	// hash, so the file would never be re-synced. Fall back to the modification
	// time: only when both sides report one is it meaningful, and only a source
	// strictly newer than the destination counts as changed. A destination that
	// is newer (or equal) is left alone, so a copy whose mtime the driver set to
	// the transfer time does not re-copy on every run.
	if src.Modified > 0 && dst.Modified > 0 {
		return src.Modified > dst.Modified
	}
	return false
}

// isMD5Digest reports whether value looks like a real MD5 hex digest. AList
// normalizes case but does not validate the shape.
func isMD5Digest(value string) bool {
	if len(value) != 32 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func fileSize(val interface{}) int64 {
	return toFileMetadata(val).Size
}

func toFileMetadata(val interface{}) FileMetadata {
	switch v := val.(type) {
	case FileMetadata:
		v.MD5 = normalizeMD5(v.MD5)
		return v
	case *FileMetadata:
		if v == nil {
			return FileMetadata{}
		}
		metadata := *v
		metadata.MD5 = normalizeMD5(metadata.MD5)
		return metadata
	case map[string]interface{}:
		return FileMetadata{
			Size:     util.ToInt64(v["size"]),
			MD5:      normalizeMD5(util.StringValue(v["md5"])),
			Modified: util.ToInt64(v["modified"]),
		}
	default:
		return FileMetadata{Size: util.ToInt64(val)}
	}
}
