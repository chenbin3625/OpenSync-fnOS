package service

import (
	"context"
	"fmt"
	"opensync/internal/mapper"
	"opensync/internal/msg"
	"opensync/internal/native"
	"opensync/pkg/util"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	dryRunItemLimit   = 500
	dryRunRunTimeout  = 15 * time.Minute
	dryRunMaxTracked  = 64
	dryRunScanWorkers = 8
)

// DryRunItem represents a single planned action in a read-only dry-run preview.
type DryRunItem struct {
	Action   string `json:"action"` // "copy" | "update" | "delete" | "move" | "mkdir" | "skip"
	SrcPath  string `json:"srcPath"`
	DstPath  string `json:"dstPath"`
	FileName string `json:"fileName"`
	FileSize int64  `json:"fileSize"`
	IsPath   bool   `json:"isPath"`
	Reason   string `json:"reason,omitempty"`
}

// DryRunResult is the snapshot returned by POST/GET /svr/job/dry-run.
type DryRunResult struct {
	JobID          int64        `json:"jobId"`
	Status         string       `json:"status"` // "running" | "completed" | "failed" | "cancelled"
	Method         int          `json:"method"`
	StartedAt      int64        `json:"startedAt"`
	FinishedAt     int64        `json:"finishedAt"`
	ScannedDirs    int64        `json:"scannedDirs"`
	CopyCount      int          `json:"copyCount"`
	CopySize       int64        `json:"copySize"`
	UpdateCount    int          `json:"updateCount"`
	UpdateSize     int64        `json:"updateSize"`
	DeleteCount    int          `json:"deleteCount"`
	DeleteSize     int64        `json:"deleteSize"`
	MoveCount      int          `json:"moveCount"`
	MoveSize       int64        `json:"moveSize"`
	MkdirCount     int          `json:"mkdirCount"`
	SkipCount      int          `json:"skipCount"`
	TotalItems     int          `json:"totalItems"`
	ItemsTruncated bool         `json:"itemsTruncated"`
	Items          []DryRunItem `json:"items"`
	Error          string       `json:"error,omitempty"`
}

type dryRunSession struct {
	mu          sync.Mutex
	result      DryRunResult
	scannedDirs atomic.Int64
	cancel      context.CancelFunc
}

func (s *dryRunSession) snapshot() *DryRunResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := s.result
	cp.ScannedDirs = s.scannedDirs.Load()
	cp.Items = append([]DryRunItem(nil), s.result.Items...)
	if cp.Items == nil {
		cp.Items = []DryRunItem{}
	}
	return &cp
}

func (s *dryRunSession) addItem(item DryRunItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.result.TotalItems++
	size := item.FileSize
	if size < 0 {
		size = 0
	}
	switch item.Action {
	case "copy":
		s.result.CopyCount++
		s.result.CopySize += size
	case "update":
		s.result.UpdateCount++
		s.result.UpdateSize += size
	case "delete":
		s.result.DeleteCount++
		s.result.DeleteSize += size
	case "move":
		s.result.MoveCount++
		s.result.MoveSize += size
	case "mkdir":
		s.result.MkdirCount++
	case "skip":
		s.result.SkipCount++
	}
	if len(s.result.Items) < dryRunItemLimit {
		s.result.Items = append(s.result.Items, item)
	} else {
		s.result.ItemsTruncated = true
	}
}

func (s *dryRunSession) finish(status, errText string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.result.Status != "running" {
		return
	}
	s.result.Status = status
	s.result.Error = errText
	s.result.FinishedAt = time.Now().Unix()
	s.result.ScannedDirs = s.scannedDirs.Load()
}

var (
	dryRunMu       sync.Mutex
	dryRunSessions = make(map[int64]*dryRunSession)
)

// StartDryRun launches an asynchronous read-only preview scan for jobID.
// It never mutates any remote file or directory and creates no job_task rows.
func StartDryRun(jobID int64) (*DryRunResult, error) {
	job, err := mapper.GetJobByID(jobID)
	if err != nil {
		return nil, publicErrorIf(err, msg.T(msg.JobNotFound))
	}
	if err := CleanJobInput(job); err != nil {
		return nil, err
	}
	alistID := util.ToInt64(job["alistId"])
	cfg, err := loadNative(alistID)
	if err != nil {
		return nil, err
	}
	var client *AlistClient
	if cfg == nil {
		client, err = GetClientByIDContext(context.Background(), alistID)
	}
	if err != nil {
		return nil, err
	}

	dryRunMu.Lock()
	if existing, ok := dryRunSessions[jobID]; ok {
		if existing.snapshot().Status == "running" {
			dryRunMu.Unlock()
			return nil, publicError(msg.T(msg.DryRunAlreadyRunning))
		}
	}
	// Bound stored finished sessions to avoid unbounded memory growth.
	if len(dryRunSessions) >= dryRunMaxTracked {
		var oldestID int64
		var oldestAt int64
		for id, sess := range dryRunSessions {
			snap := sess.snapshot()
			if snap.Status == "running" {
				continue
			}
			if oldestID == 0 || snap.StartedAt < oldestAt {
				oldestID = id
				oldestAt = snap.StartedAt
			}
		}
		if oldestID != 0 {
			delete(dryRunSessions, oldestID)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), dryRunRunTimeout)
	sess := &dryRunSession{
		result: DryRunResult{
			JobID:     jobID,
			Status:    "running",
			Method:    util.ToInt(job["method"]),
			StartedAt: time.Now().Unix(),
			Items:     make([]DryRunItem, 0, 64),
		},
		cancel: cancel,
	}
	dryRunSessions[jobID] = sess
	dryRunMu.Unlock()

	if cfg != nil {
		go func() {
			defer cancel()
			var prevDirs, pairDirs int64
			var lastPair string
			outcome := runNativePairs(ctx, cfg, job, true, nil, func(src, dst string, v native.Event) {
				pairKey := src + "\x00" + dst
				if pairKey != lastPair {
					prevDirs += pairDirs
					pairDirs = 0
					lastPair = pairKey
				}
				if v.ScanDirs > 0 {
					if v.ScanDirs > pairDirs {
						pairDirs = v.ScanDirs
					}
					sess.scannedDirs.Store(prevDirs + pairDirs)
				}
				if v.File != "" {
					action := "copy"
					if v.Updated {
						action = "update"
					}
					sess.addItem(DryRunItem{Action: action, SrcPath: src, DstPath: dst, FileName: v.File, FileSize: v.Size})
				}
			})
			if outcome.err != nil {
				if ctx.Err() != nil {
					sess.finish("cancelled", "")
				} else {
					sess.finish("failed", outcome.err.Error())
				}
			} else {
				sess.finish("completed", "")
			}
		}()
	} else {
		go runDryRunSession(ctx, sess, job, client)
	}
	return sess.snapshot(), nil
}

// GetDryRun returns the latest dry-run result snapshot for jobID.
func GetDryRun(jobID int64) (*DryRunResult, error) {
	dryRunMu.Lock()
	sess, ok := dryRunSessions[jobID]
	dryRunMu.Unlock()
	if !ok || sess == nil {
		return nil, publicError(msg.T(msg.DryRunNotFound))
	}
	return sess.snapshot(), nil
}

// CancelDryRun cancels an in-progress dry-run for jobID.
func CancelDryRun(jobID int64) error {
	dryRunMu.Lock()
	sess, ok := dryRunSessions[jobID]
	dryRunMu.Unlock()
	if !ok || sess == nil {
		return nil
	}
	if sess.cancel != nil {
		sess.cancel()
	}
	sess.finish("cancelled", "")
	return nil
}

func runDryRunSession(ctx context.Context, sess *dryRunSession, job map[string]interface{}, client *AlistClient) {
	defer func() {
		if sess.cancel != nil {
			sess.cancel()
		}
		if r := recover(); r != nil {
			sess.finish("failed", fmt.Sprintf("dry-run panic: %v", r))
		}
	}()

	method := util.ToInt(job["method"])
	dstPaths := parsePathList(job["dstPath"])
	if method == 2 && len(dstPaths) != 1 {
		sess.finish("failed", "移动模式只允许一个目标目录")
		return
	}

	// Build configured scan pairs without mutating remote storage.
	jt := &JobTask{
		Job:         job,
		AlistClient: client,
	}
	works := jt.configuredScanWorks(dstPaths)
	excludeM := buildJobExcludeMatcher(job)
	includeM := buildJobIncludeMatcher(job)
	compareMode := util.ToInt(job["compareMode"])
	conflictPolicy := util.ToInt(job["conflictPolicy"])
	now := time.Now()
	sem := make(chan struct{}, dryRunScanWorkers)

	listRO := func(dir string, isSrc bool) (FileListResult, bool, error) {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
		useCache := util.ToInt(job["useCacheT"])
		interval := util.ToInt(job["scanIntervalT"])
		if isSrc {
			useCache = util.ToInt(job["useCacheS"])
			if method == 1 {
				useCache = 0
			}
			interval = util.ToInt(job["scanIntervalS"])
		}
		entries, err := client.FileListApiContext(ctx, dir, useCache, interval)
		if err != nil {
			if !isSrc && isAlistObjectNotFound(err) {
				return make(FileListResult), false, nil
			}
			return nil, false, err
		}
		return entries, true, nil
	}

	for _, work := range works {
		if ctx.Err() != nil {
			sess.finish("cancelled", "")
			return
		}
		if method == 1 {
			if err := dryRunFullSyncWork(ctx, sess, job, work, excludeM, includeM, listRO); err != nil {
				if ctx.Err() != nil {
					sess.finish("cancelled", "")
				} else {
					sess.finish("failed", err.Error())
				}
				return
			}
		} else {
			if err := dryRunIncrementalWork(ctx, sess, job, work, excludeM, includeM, compareMode, conflictPolicy, method == 2, now, listRO); err != nil {
				if ctx.Err() != nil {
					sess.finish("cancelled", "")
				} else {
					sess.finish("failed", err.Error())
				}
				return
			}
		}
	}

	if ctx.Err() != nil {
		sess.finish("cancelled", "")
		return
	}
	sess.finish("completed", "")
}

type dryRunListFn func(dir string, isSrc bool) (FileListResult, bool, error)

func dryRunIncrementalWork(
	ctx context.Context,
	sess *dryRunSession,
	job map[string]interface{},
	work scanWork,
	excludeM *excludeMatcher,
	includeM *includeMatcher,
	compareMode, conflictPolicy int,
	isMove bool,
	now time.Time,
	listRO dryRunListFn,
) error {
	var walk func(srcDir, dstDir string, dstMissing bool) error
	walk = func(srcDir, dstDir string, dstMissing bool) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		sess.scannedDirs.Add(1)
		srcRaw, _, err := listRO(srcDir, true)
		if err != nil {
			return fmt.Errorf("扫描源目录 %s 失败: %w", srcDir, err)
		}
		srcFiles := make(FileListResult, len(srcRaw))
		for name, meta := range srcRaw {
			rel := excludeMatchPath(work.SrcRootPath, srcDir, name)
			if excludeM != nil && excludeM.matchesListed(rel, strings.HasSuffix(name, "/")) {
				continue
			}
			srcFiles[name] = meta
		}

		dstFiles := make(FileListResult)
		if !dstMissing {
			dstRaw, exists, dstErr := listRO(dstDir, false)
			if dstErr != nil {
				return fmt.Errorf("扫描目标目录 %s 失败: %w", dstDir, dstErr)
			}
			if !exists {
				dstMissing = true
			} else {
				for name, meta := range dstRaw {
					rel := excludeMatchPath(work.DstRootPath, dstDir, name)
					if excludeM != nil && excludeM.matchesListed(rel, strings.HasSuffix(name, "/")) {
						continue
					}
					dstFiles[name] = meta
				}
			}
		}

		dstIndex := newDstNameMatchIndex(dstFiles)
		srcIndex := newSrcNameMatchIndex(srcFiles)

		for _, key := range sortedFileListKeys(srcFiles) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			srcVal := srcFiles[key]
			if !strings.HasSuffix(key, "/") {
				rel := excludeMatchPath(work.SrcRootPath, srcDir, key)
				if includeM != nil && !includeM.matchesListed(rel) {
					continue
				}
				if !jobAllowsFileMeta(job, srcVal, now) {
					continue
				}
				_, dstVal, exists := dstIndex.find(key, srcIndex)
				if !isMove && exists && conflictPolicy == 1 {
					sess.addItem(DryRunItem{
						Action:   "skip",
						SrcPath:  srcDir,
						DstPath:  dstDir,
						FileName: key,
						FileSize: srcVal.Size,
						Reason:   "目标已存在（冲突策略：跳过）",
					})
					continue
				}
				if isMove {
					sess.addItem(DryRunItem{
						Action:   "move",
						SrcPath:  srcDir,
						DstPath:  dstDir,
						FileName: key,
						FileSize: srcVal.Size,
					})
					continue
				}
				if !exists {
					sess.addItem(DryRunItem{
						Action:   "copy",
						SrcPath:  srcDir,
						DstPath:  dstDir,
						FileName: key,
						FileSize: srcVal.Size,
					})
				} else if fileChangedWithMode(srcVal, dstVal, compareMode) {
					reason := "内容/元数据变化"
					if conflictPolicy == 2 {
						reason = "冲突保留两者（目标旧文件将重命名备份）"
					} else if trashEnabled(job) {
						reason = "覆盖前将旧文件移入回收站"
					}
					sess.addItem(DryRunItem{
						Action:   "update",
						SrcPath:  srcDir,
						DstPath:  dstDir,
						FileName: key,
						FileSize: srcVal.Size,
						Reason:   reason,
					})
				}
				continue
			}

			// Directory
			dstKey, _, exists := dstIndex.find(key, srcIndex)
			if !exists {
				sess.addItem(DryRunItem{
					Action:   "mkdir",
					SrcPath:  srcDir + key,
					DstPath:  dstDir + key,
					FileName: strings.TrimSuffix(key, "/"),
					IsPath:   true,
				})
				if err := walk(srcDir+key, dstDir+key, true); err != nil {
					return err
				}
			} else {
				if err := walk(srcDir+key, dstDir+dstKey, false); err != nil {
					return err
				}
			}
		}
		return nil
	}

	return walk(work.SrcPath, work.DstPath, false)
}

func dryRunFullSyncWork(
	ctx context.Context,
	sess *dryRunSession,
	job map[string]interface{},
	work scanWork,
	excludeM *excludeMatcher,
	includeM *includeMatcher,
	listRO dryRunListFn,
) error {
	now := time.Now()
	scanTree := func(root string, isSrc bool) (*fullSyncSnapshot, error) {
		root = normalizeDirPath(root)
		snap := &fullSyncSnapshot{
			root:          root,
			dirs:          make(map[string]FileListResult),
			excluded:      make(map[string]struct{}),
			protectedDirs: make(map[string]struct{}),
			specExcluded:  make(map[string]struct{}),
			sizeExcluded:  make(map[string]struct{}),
		}
		var walk func(relDir string) error
		walk = func(relDir string) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			sess.scannedDirs.Add(1)
			items, _, err := listRO(root+relDir, isSrc)
			if err != nil {
				return err
			}
			children := make([]string, 0)
			for name, meta := range items {
				isDir := strings.HasSuffix(name, "/")
				relPath := relDir + name
				if excludeM != nil && excludeM.matchesListed(relPath, isDir) {
					if isSrc {
						delete(items, name)
					} else {
						snap.excluded[relPath] = struct{}{}
						snap.specExcluded[relPath] = struct{}{}
					}
					continue
				}
				if !isDir && includeM != nil && !includeM.matchesListed(relPath) {
					if isSrc {
						delete(items, name)
					} else {
						snap.excluded[relPath] = struct{}{}
						snap.specExcluded[relPath] = struct{}{}
					}
					continue
				}
				if !isSrc && !isDir && !jobAllowsFileMeta(job, meta, now) {
					snap.excluded[relPath] = struct{}{}
					snap.sizeExcluded[relPath] = struct{}{}
					continue
				}
				if isDir {
					children = append(children, relPath)
				}
			}
			snap.dirs[relDir] = items
			if !isSrc && len(snap.excluded) > 0 {
				for entry := range snap.excluded {
					if strings.HasPrefix(entry, relDir) {
						for ancestor := relDir; ancestor != ""; {
							snap.protectedDirs[ancestor] = struct{}{}
							ancestor = ancestor[:strings.LastIndex(strings.TrimSuffix(ancestor, "/"), "/")+1]
						}
						break
					}
				}
			}
			sort.Strings(children)
			for _, child := range children {
				if err := walk(child); err != nil {
					return err
				}
			}
			return nil
		}
		if err := walk(""); err != nil {
			return nil, err
		}
		return snap, nil
	}

	srcSnap, err := scanTree(work.SrcPath, true)
	if err != nil {
		return fmt.Errorf("扫描源目录 %s 失败: %w", work.SrcPath, err)
	}
	dstSnap, err := scanTree(work.DstPath, false)
	if err != nil {
		return fmt.Errorf("扫描目标目录 %s 失败: %w", work.DstPath, err)
	}

	plan := newFullSyncPlan(srcSnap, dstSnap, nil)
	plan.build(job)

	relocations := plan.relocations()
	writes := plan.writeIndex()
	guardReason, deletesUnsafe := plan.unsafeDeleteVolume(plan.plannedDeleteWeight(relocations, writes))
	suspiciousCounts := plan.suspiciousEmptySourceDirs()
	suspicious := make(map[string]struct{}, len(suspiciousCounts))
	for dir := range suspiciousCounts {
		suspicious[dir] = struct{}{}
	}

	delReason := "目标端多余项"
	if trashEnabled(job) {
		delReason = "移入目标回收站"
	}

	blocked := make(map[string]struct{})
	for _, key := range sortedDeleteKeys(plan.blockers, false) {
		item := plan.blockers[key]
		if item.protected {
			blocked[normalizeBlockedPath(item.blocksPath)] = struct{}{}
			continue
		}
		if _, col := writes.collision(item.dir, item.name); col {
			blocked[normalizeBlockedPath(item.blocksPath)] = struct{}{}
			continue
		}
		if deletesUnsafe {
			blocked[normalizeBlockedPath(item.blocksPath)] = struct{}{}
			sess.addItem(DryRunItem{
				Action:   "skip",
				DstPath:  item.dir,
				FileName: item.name,
				FileSize: plan.dst.deleteSize(item),
				IsPath:   strings.HasSuffix(item.name, "/"),
				Reason:   "触发批量删除保护，已保留目标项（" + guardReason + "）",
			})
			continue
		}
		sess.addItem(DryRunItem{
			Action:   "delete",
			DstPath:  item.dir,
			FileName: item.name,
			FileSize: plan.dst.deleteSize(item),
			IsPath:   strings.HasSuffix(item.name, "/"),
			Reason:   "类型冲突清理（" + delReason + "）",
		})
	}

	movedTargets := make(map[string]struct{}, len(relocations))
	consumedDeletes := make(map[string]struct{}, len(relocations))
	for _, rel := range relocations {
		if fullSyncPathBlocked(rel.target.target, blocked) {
			continue
		}
		movedTargets[rel.target.target] = struct{}{}
		if rel.source.deleteRoot == rel.source.object {
			consumedDeletes[rel.source.deleteRoot] = struct{}{}
		}
		sess.addItem(DryRunItem{
			Action:   "move",
			SrcPath:  rel.source.dir,
			DstPath:  rel.target.dstDir,
			FileName: rel.source.name,
			FileSize: rel.source.metadata.Size,
			Reason:   "目标端同内容重命名/移动复用",
		})
	}
	for _, key := range sortedDirKeys(plan.newDirs) {
		dir := plan.newDirs[key]
		if fullSyncPathBlocked(dir.dstDir, blocked) {
			continue
		}
		sess.addItem(DryRunItem{
			Action:   "mkdir",
			SrcPath:  dir.srcDir,
			DstPath:  dir.dstDir,
			FileName: strings.TrimSuffix(strings.TrimPrefix(dir.dstDir, work.DstPath), "/"),
			IsPath:   true,
		})
	}
	for _, target := range sortedNewFileTargets(plan.newFiles) {
		if _, moved := movedTargets[target]; moved {
			continue
		}
		file := plan.newFiles[target]
		if fullSyncPathBlocked(file.target, blocked) {
			continue
		}
		sess.addItem(DryRunItem{
			Action:   "copy",
			SrcPath:  file.srcDir,
			DstPath:  file.dstDir,
			FileName: file.name,
			FileSize: file.metadata.Size,
		})
	}
	for _, file := range sortedFullSyncFiles(plan.changed) {
		if fullSyncPathBlocked(file.target, blocked) {
			continue
		}
		reason := "内容/元数据变化"
		if util.ToInt(job["conflictPolicy"]) == 2 {
			reason = "冲突保留两者（目标旧文件将重命名备份）"
		} else if trashEnabled(job) {
			reason = "覆盖前将旧文件移入回收站"
		}
		sess.addItem(DryRunItem{
			Action:   "update",
			SrcPath:  file.srcDir,
			DstPath:  file.dstDir,
			FileName: file.name,
			FileSize: file.metadata.Size,
			Reason:   reason,
		})
	}
	for _, key := range sortedDeleteKeys(plan.extraDeletes, true) {
		if _, moved := consumedDeletes[key]; moved {
			continue
		}
		item := plan.extraDeletes[key]
		if _, col := writes.collision(item.dir, item.name); col {
			continue
		}
		if deletesUnsafe {
			sess.addItem(DryRunItem{
				Action:   "skip",
				DstPath:  item.dir,
				FileName: item.name,
				FileSize: plan.dst.deleteSize(item),
				IsPath:   strings.HasSuffix(item.name, "/"),
				Reason:   "触发批量删除保护，已保留目标项（" + guardReason + "）",
			})
			continue
		}
		if fullSyncPathBlocked(key, suspicious) {
			sess.addItem(DryRunItem{
				Action:   "skip",
				DstPath:  item.dir,
				FileName: item.name,
				FileSize: plan.dst.deleteSize(item),
				IsPath:   strings.HasSuffix(item.name, "/"),
				Reason:   "源目录为空触发空源保护，已保留目标项",
			})
			continue
		}
		sess.addItem(DryRunItem{
			Action:   "delete",
			DstPath:  item.dir,
			FileName: item.name,
			FileSize: plan.dst.deleteSize(item),
			IsPath:   strings.HasSuffix(item.name, "/"),
			Reason:   delReason,
		})
	}
	return nil
}
