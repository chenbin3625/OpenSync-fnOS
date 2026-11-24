package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"path"
	"sort"
	"strings"
	"time"
)

const (
	defaultTrashDir       = ".opensync-trash"
	defaultTrashRetention = 30
	maxTrashRetentionDays = 3650
)

// normalizeTrashDir validates and normalizes a job's relative trash directory name.
func normalizeTrashDir(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return defaultTrashDir, nil
	}
	trimmed = strings.TrimSuffix(trimmed, "/")
	if trimmed == "" ||
		strings.HasPrefix(trimmed, "/") ||
		strings.Contains(trimmed, "//") ||
		strings.ContainsAny(trimmed, `\*?[]{}!`) ||
		containsControl(trimmed) {
		return "", publicError(msg.T(msg.TrashDirInvalid))
	}
	for _, seg := range strings.Split(trimmed, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", publicError(msg.T(msg.TrashDirInvalid))
		}
	}
	return trimmed, nil
}

func trashEnabled(job map[string]interface{}) bool {
	return job != nil && util.ToInt(job["trashEnable"]) == 1
}

func jobTrashDir(job map[string]interface{}) string {
	if job == nil {
		return defaultTrashDir
	}
	dir, err := normalizeTrashDir(util.StringValue(job["trashDir"]))
	if err != nil || dir == "" {
		return defaultTrashDir
	}
	return dir
}

func jobTrashRetentionDays(job map[string]interface{}) int {
	if job == nil {
		return defaultTrashRetention
	}
	raw, ok := job["trashRetentionDays"]
	if !ok || raw == nil {
		return defaultTrashRetention
	}
	days := util.ToInt(raw)
	if days < 0 {
		return 0
	}
	if days > maxTrashRetentionDays {
		return maxTrashRetentionDays
	}
	return days
}

// conflictBackupName builds a timestamped rename target for conflictPolicy=2 (keep both).
func conflictBackupName(fileName string, now time.Time) string {
	suffix := ".conflict-" + now.In(schedulerLocation()).Format("20060102-150405")
	ext := path.Ext(fileName)
	base := strings.TrimSuffix(fileName, ext)
	// Preserve dot-files like ".gitignore" where ext == fileName.
	if ext == "" || base == "" {
		return fileName + suffix
	}
	return base + suffix + ext
}

type remoteDirCreator interface {
	MkdirContext(context.Context, string, int) error
}

func ensureRemoteDirHierarchy(ctx context.Context, client remoteDirCreator, baseRoot, relPath string, scanInterval int, created map[string]struct{}, markCreated func(string) bool) error {
	baseRoot = normalizeDirPath(baseRoot)
	relPath = strings.Trim(relPath, "/")
	if relPath == "" {
		return nil
	}
	cur := strings.TrimRight(baseRoot, "/")
	for _, seg := range strings.Split(relPath, "/") {
		if seg == "" {
			continue
		}
		cur = cur + "/" + seg
		dirKey := cur + "/"
		if markCreated != nil {
			if !markCreated(dirKey) {
				continue
			}
		} else if created != nil {
			if _, ok := created[dirKey]; ok {
				continue
			}
			created[dirKey] = struct{}{}
		}
		if err := client.MkdirContext(ctx, dirKey, scanInterval); err != nil {
			// Ignore "already exists" responses from storage drivers.
			lower := strings.ToLower(err.Error())
			if strings.Contains(lower, "exist") || strings.Contains(lower, "已存在") {
				continue
			}
			return err
		}
	}
	return nil
}

// waitForAlistRemoteMove waits for an asynchronous /api/fs/move task to finish
// when OpenList/AList returns a task ID.
func waitForAlistRemoteMove(ctx context.Context, client copyItemClient, taskID string) error {
	if taskID == "" {
		return nil
	}
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Minute)
	defer deadline.Stop()
	for {
		info, err := client.TaskInfoContext(ctx, taskID, taskItemTypeMove)
		if err != nil {
			if isAlistObjectNotFound(err) {
				return nil
			}
			return err
		}
		state := util.ToInt(info["state"])
		switch state {
		case 2: // succeeded
			return nil
		case 3, 4, 7: // cancelled / errored / failed
			errMsg := util.StringValue(info["error"])
			if errMsg == "" {
				errMsg = fmt.Sprintf("trash move task %s failed (state=%d)", taskID, state)
			}
			return errors.New(errMsg)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("trash move task %s timed out", taskID)
		case <-ticker.C:
		}
	}
}

func (jt *JobTask) markTrashDirCreated(dirKey string) bool {
	jt.trashDirMu.Lock()
	defer jt.trashDirMu.Unlock()
	if jt.trashDirsCreated == nil {
		jt.trashDirsCreated = make(map[string]struct{})
	}
	if _, exists := jt.trashDirsCreated[dirKey]; exists {
		return false
	}
	jt.trashDirsCreated[dirKey] = struct{}{}
	return true
}

// moveItemToTrash moves a destination file or directory into
// <dstRoot>/<trashDir>/<YYYY-MM-DD>/<relDir>. It NEVER falls back to hard delete.
func (jt *JobTask) moveItemToTrash(dstRoot, itemDir, itemName string) error {
	if jt.AlistClient == nil {
		return errors.New("alist client unavailable for trash move")
	}
	dstRoot = normalizeDirPath(dstRoot)
	itemDir = normalizeDirPath(itemDir)
	cleanName := strings.TrimSuffix(itemName, "/")
	if !isSafeDeleteTargetName(cleanName) {
		return errors.New(unsafeDeleteTargetMessage(itemDir, itemName))
	}
	trashDir := jobTrashDir(jt.Job)
	dateBucket := time.Now().In(schedulerLocation()).Format("2006-01-02")
	relDir := ""
	if strings.HasPrefix(itemDir, dstRoot) {
		relDir = strings.Trim(strings.TrimPrefix(itemDir, dstRoot), "/")
	}
	relTarget := trashDir + "/" + dateBucket
	if relDir != "" {
		relTarget += "/" + relDir
	}
	scanIntervalT := util.ToInt(jt.Job["scanIntervalT"])
	if err := ensureRemoteDirHierarchy(jt.context(), jt.AlistClient, dstRoot, relTarget, scanIntervalT, nil, jt.markTrashDirCreated); err != nil {
		return fmt.Errorf("create trash directory %q: %w", dstRoot+relTarget+"/", err)
	}
	targetDir := normalizeDirPath(dstRoot + relTarget)
	taskID, err := jt.AlistClient.MoveFileContext(jt.context(), itemDir, targetDir, cleanName)
	if err != nil {
		if isAlistObjectNotFound(err) {
			return nil
		}
		return fmt.Errorf("move %q to trash %q: %w", cleanName, targetDir, err)
	}
	if taskID != "" {
		if err := waitForAlistRemoteMove(jt.context(), jt.AlistClient, taskID); err != nil {
			return fmt.Errorf("wait trash move %q: %w", cleanName, err)
		}
	}
	return nil
}

// cleanupExpiredTrash removes date buckets under <dstRoot>/<trashDir>/ older
// than retentionDays. Errors are logged and do not fail an otherwise clean sync.
func (jt *JobTask) cleanupExpiredTrash() {
	if !trashEnabled(jt.Job) || jt.AlistClient == nil {
		return
	}
	retentionDays := jobTrashRetentionDays(jt.Job)
	if retentionDays <= 0 {
		return
	}
	if jt.isBreak() || jt.context().Err() != nil {
		return
	}
	trashDir := jobTrashDir(jt.Job)
	scanIntervalT := util.ToInt(jt.Job["scanIntervalT"])
	today := time.Now().In(schedulerLocation())
	cutoffDate := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, schedulerLocation()).AddDate(0, 0, -retentionDays)

	dstRoots := make(map[string]struct{})
	for _, work := range jt.configuredScanWorks(parsePathList(jt.Job["dstPath"])) {
		dstRoots[normalizeDirPath(work.DstRootPath)] = struct{}{}
	}
	for dstRoot := range dstRoots {
		if jt.isBreak() || jt.context().Err() != nil {
			return
		}
		trashRoot := normalizeDirPath(dstRoot + trashDir)
		entries, err := jt.AlistClient.FileListApiContext(jt.context(), trashRoot, 0, scanIntervalT)
		if err != nil {
			if !isAlistObjectNotFound(err) {
				log.Printf("Task %d trash cleanup list %q failed: %v", jt.TaskID, trashRoot, err)
			}
			continue
		}
		for name := range entries {
			if !strings.HasSuffix(name, "/") {
				continue
			}
			dirName := strings.TrimSuffix(name, "/")
			bucketTime, parseErr := time.ParseInLocation("2006-01-02", dirName, schedulerLocation())
			if parseErr != nil {
				continue
			}
			if bucketTime.Before(cutoffDate) {
				if err := jt.AlistClient.DeleteFileContext(jt.context(), trashRoot, []string{dirName}, scanIntervalT); err != nil {
					log.Printf("Task %d failed to purge expired trash bucket %q: %v", jt.TaskID, trashRoot+dirName, err)
				} else {
					log.Printf("Task %d purged expired trash bucket %q", jt.TaskID, trashRoot+dirName)
				}
			}
		}
	}
}

// recordMoveSrcSubdir tracks a visited source subdirectory during move mode
// so cleanupEmptyMoveSrcDirs can remove empty subdirectories deepest-first.
func (jt *JobTask) recordMoveSrcSubdir(dirPath, rootPath string) {
	if util.ToInt(jt.Job["method"]) != 2 || util.ToInt(jt.Job["deleteEmptySrcDirs"]) != 1 {
		return
	}
	dirPath = normalizeDirPath(dirPath)
	rootPath = normalizeDirPath(rootPath)
	if dirPath == "" || rootPath == "" || dirPath == rootPath || !strings.HasPrefix(dirPath, rootPath) {
		return
	}
	jt.moveSrcDirMu.Lock()
	if jt.moveSrcSubdirs == nil {
		jt.moveSrcSubdirs = make(map[string]string)
	}
	jt.moveSrcSubdirs[dirPath] = rootPath
	jt.moveSrcDirMu.Unlock()
}

func (jt *JobTask) hasAnyFailedItem() bool {
	jt.FinishMu.Lock()
	defer jt.FinishMu.Unlock()
	if jt.FinishedCounts[taskStatusFailed] > 0 || jt.FinishedCounts[taskStatusSystemFailed] > 0 {
		return true
	}
	for s, count := range jt.FinishedCounts {
		if count > 0 && isOtherTaskStatus(s) {
			return true
		}
	}
	return false
}

// cleanupEmptyMoveSrcDirs removes empty source subdirectories deepest-first
// after a clean move run. Never removes any configured source root directory.
func (jt *JobTask) cleanupEmptyMoveSrcDirs() {
	if util.ToInt(jt.Job["method"]) != 2 || util.ToInt(jt.Job["deleteEmptySrcDirs"]) != 1 {
		return
	}
	if jt.isBreak() || jt.context().Err() != nil || jt.AlistClient == nil || jt.hasAnyFailedItem() {
		return
	}

	jt.moveSrcDirMu.Lock()
	if len(jt.moveSrcSubdirs) == 0 {
		jt.moveSrcDirMu.Unlock()
		return
	}
	dirs := make([]string, 0, len(jt.moveSrcSubdirs))
	roots := make(map[string]string, len(jt.moveSrcSubdirs))
	for dir, root := range jt.moveSrcSubdirs {
		dirs = append(dirs, dir)
		roots[dir] = root
	}
	jt.moveSrcDirMu.Unlock()

	// Deepest-first order so emptying a nested folder lets its parent become empty too.
	sort.Slice(dirs, func(i, j int) bool {
		di := strings.Count(dirs[i], "/")
		dj := strings.Count(dirs[j], "/")
		if di != dj {
			return di > dj
		}
		return dirs[i] > dirs[j]
	})

	scanIntervalS := util.ToInt(jt.Job["scanIntervalS"])
	for _, dir := range dirs {
		if jt.isBreak() || jt.context().Err() != nil {
			return
		}
		root := roots[dir]
		if dir == root || !strings.HasPrefix(dir, root) {
			continue
		}
		entries, err := jt.AlistClient.FileListApiContext(jt.context(), dir, 0, scanIntervalS)
		if err != nil {
			if !isAlistObjectNotFound(err) {
				log.Printf("Task %d empty-src-dir check failed for %q: %v", jt.TaskID, dir, err)
			}
			continue
		}
		if len(entries) > 0 {
			continue
		}
		clean := strings.TrimRight(dir, "/")
		parent := normalizeDirPath(path.Dir(clean))
		base := path.Base(clean)
		if !isSafeDeleteTargetName(base) || !strings.HasPrefix(parent, root) {
			continue
		}
		delErr := jt.AlistClient.DeleteFileContext(jt.context(), parent, []string{base}, scanIntervalS)
		status := taskStatusSuccess
		var errMsg *string
		if delErr != nil {
			status = taskStatusFailed
			s := delErr.Error()
			errMsg = &s
		}
		jt.DelHook(parent, base+"/", nil, status, errMsg, taskItemPath, time.Now().Unix())
	}
}
