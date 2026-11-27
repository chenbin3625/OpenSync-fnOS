package service

import (
	"context"
	"fmt"
	"opensync/internal/mapper"
	"opensync/internal/msg"
	"opensync/internal/native"
	"opensync/pkg/util"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

// effectiveOneWayDeleteMode returns the active one-way deletion policy for job:
//   - "none": incremental upload/copy without deleting destination extras
//   - "tracked": delete destination files only when previously synced in baseline
//     and now missing from source (preserving unrelated destination files)
//   - "mirror": mirror-sync deletion semantics
//
// Backward compatibility:
//   - method == 1 with empty oneWayDeleteMode => "mirror"
//   - method == 0 with empty oneWayDeleteMode => "none"
func effectiveOneWayDeleteMode(job map[string]interface{}, extra nativeExtraFlags) string {
	switch strings.ToLower(strings.TrimSpace(extra.OneWayDeleteMode)) {
	case "none", "tracked", "mirror":
		return strings.ToLower(strings.TrimSpace(extra.OneWayDeleteMode))
	}
	if util.ToInt(job["method"]) == 1 {
		return "mirror"
	}
	return "none"
}

func effectiveOneWayDeleteModeForJob(job map[string]interface{}) string {
	if job == nil {
		return "none"
	}
	extra, _, err := parseNativeExtraFlags(job["extraFlags"])
	if err != nil {
		if util.ToInt(job["method"]) == 1 {
			return "mirror"
		}
		return "none"
	}
	return effectiveOneWayDeleteMode(job, extra)
}

type trackedBaselineRecorder struct {
	jobID int64
	mu    sync.Mutex
	pairs map[[2]string]map[string]mapper.OnewayBaselineEntry
}

func newTrackedBaselineRecorder(jobID int64) *trackedBaselineRecorder {
	return &trackedBaselineRecorder{
		jobID: jobID,
		pairs: make(map[[2]string]map[string]mapper.OnewayBaselineEntry),
	}
}

func (r *trackedBaselineRecorder) record(srcRoot, dstRoot, rawRel string, size int64) {
	if r == nil || r.jobID <= 0 {
		return
	}
	rel := cleanTrackedRelPath(rawRel)
	if rel == "" {
		return
	}
	normSrc := normalizeDirPath(srcRoot)
	normDst := normalizeDirPath(dstRoot)
	key := [2]string{normSrc, normDst}

	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.pairs[key]
	if m == nil {
		m = make(map[string]mapper.OnewayBaselineEntry)
		r.pairs[key] = m
	}
	m[rel] = mapper.OnewayBaselineEntry{
		JobID:    r.jobID,
		SrcRoot:  normSrc,
		DstRoot:  normDst,
		RelPath:  rel,
		Size:     size,
		SyncedAt: time.Now().Unix(),
	}
}

func (r *trackedBaselineRecorder) flush() error {
	if r == nil || r.jobID <= 0 {
		return nil
	}
	r.mu.Lock()
	pairs := r.pairs
	r.pairs = make(map[[2]string]map[string]mapper.OnewayBaselineEntry)
	r.mu.Unlock()

	var firstErr error
	for key, entriesMap := range pairs {
		if len(entriesMap) == 0 {
			continue
		}
		batch := make([]mapper.OnewayBaselineEntry, 0, len(entriesMap))
		for _, entry := range entriesMap {
			batch = append(batch, entry)
		}
		sort.Slice(batch, func(i, j int) bool {
			return batch[i].RelPath < batch[j].RelPath
		})
		if err := mapper.UpsertOnewayBaselineEntries(r.jobID, key[0], key[1], batch); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func cleanTrackedRelPath(raw string) string {
	trimmed := strings.TrimSpace(strings.TrimSuffix(raw, "/"))
	if trimmed == "" || strings.ContainsAny(trimmed, "\r\n\\") {
		return ""
	}
	rel := strings.TrimPrefix(path.Clean("/"+trimmed), "/")
	if rel == "" || rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".opensync-") {
			return ""
		}
	}
	return rel
}

func isProtectedTrackedDeletePath(rel, trashDir string, exclude *excludeMatcher, include *includeMatcher) bool {
	if rel == "" {
		return true
	}
	if strings.HasPrefix(rel, ".opensync-") || rel == trashDir || strings.HasPrefix(rel, trashDir+"/") {
		return true
	}
	if isConflictBackupName(path.Base(rel)) {
		return true
	}
	if exclude != nil && exclude.matchesListed(rel, false) {
		return true
	}
	if include != nil && !include.matchesListed(rel) {
		return true
	}
	return false
}

type trackedDeletePlan struct {
	normSrc          string
	normDst          string
	deleteCandidates []string
	deleteSizes      map[string]int64
	staleBaseline    []string
	emptyGuardCount  int
}

func buildTrackedDeletePlanForPair(
	ctx context.Context,
	cfg native.Config,
	job map[string]interface{},
	jobID int64,
	normSrc, normDst string,
) (trackedDeletePlan, error) {
	plan := trackedDeletePlan{
		normSrc:     normSrc,
		normDst:     normDst,
		deleteSizes: make(map[string]int64),
	}
	baseline, err := mapper.ListOnewayBaseline(jobID, normSrc, normDst)
	if err != nil {
		return plan, err
	}
	if len(baseline) == 0 {
		return plan, nil
	}
	if _, err := cfg.Resolve(normSrc); err != nil {
		return plan, err
	}
	dstEp, err := cfg.Resolve(normDst)
	if err != nil {
		return plan, err
	}
	if dstEp.ReadOnly {
		return plan, nil
	}

	treeOpts := native.TreeOptions{
		FastList:       util.ToInt(job["fastList"]) == 1,
		FollowSymlinks: util.ToInt(job["followSymlinks"]) == 1,
		ProxyURL:       globalNativeProxyURL(),
	}
	srcEntries, srcExists, err := cfg.ListTree(ctx, strings.TrimRight(normSrc, "/"), treeOpts)
	if err != nil {
		return plan, fmt.Errorf("扫描源目录 %s 失败：%w", normSrc, err)
	}
	if !srcExists {
		return plan, fmt.Errorf("源目录 %s 不存在或不可访问，已停止追踪删除", normSrc)
	}

	trashDir := jobTrashDir(job)
	srcHas := make(map[string]struct{}, len(srcEntries))
	srcVisibleCount := 0
	for _, entry := range srcEntries {
		rel := cleanTrackedRelPath(entry.RelPath)
		if rel == "" {
			continue
		}
		if entry.IsDir {
			continue
		}
		srcVisibleCount++
		srcHas[rel] = struct{}{}
	}
	// Empty-source protection: when the source tree appears completely empty while
	// holding a large tracked baseline (>= 100 entries), treat as possible unmounted
	// share / storage glitch and retain all destination files.
	if srcVisibleCount == 0 && len(baseline) >= emptySourceSubdirMinEntries {
		plan.emptyGuardCount = len(baseline)
		return plan, nil
	}

	dstEntries, dstExists, err := cfg.ListTree(ctx, strings.TrimRight(normDst, "/"), treeOpts)
	if err != nil {
		return plan, fmt.Errorf("扫描目标目录 %s 失败：%w", normDst, err)
	}
	if !dstExists {
		return plan, nil
	}
	dstFiles := make(map[string]native.TreeEntry, len(dstEntries))
	for _, entry := range dstEntries {
		if entry.IsDir {
			continue
		}
		rel := cleanTrackedRelPath(entry.RelPath)
		if rel == "" || rel == trashDir || strings.HasPrefix(rel, trashDir+"/") {
			continue
		}
		dstFiles[rel] = entry
	}

	exclude := buildJobExcludeMatcher(job)
	include := buildJobIncludeMatcher(job)
	for _, entry := range baseline {
		rel := cleanTrackedRelPath(entry.RelPath)
		if rel == "" {
			continue
		}
		if _, onSrc := srcHas[rel]; onSrc {
			continue
		}
		if isProtectedTrackedDeletePath(rel, trashDir, exclude, include) {
			continue
		}
		if !isSafeDeleteTargetName(path.Base(rel)) {
			continue
		}
		dstEntry, onDst := dstFiles[rel]
		if !onDst {
			plan.staleBaseline = append(plan.staleBaseline, rel)
			continue
		}
		plan.deleteCandidates = append(plan.deleteCandidates, rel)
		plan.deleteSizes[rel] = dstEntry.Size
	}
	sort.Strings(plan.deleteCandidates)
	sort.Strings(plan.staleBaseline)
	return plan, nil
}

func runTrackedOneWayDeletes(
	ctx context.Context,
	jt *JobTask,
	cfg native.Config,
	job map[string]interface{},
	pairErrors map[[2]string]error,
) error {
	if jt == nil || jt.JobClient == nil {
		return nil
	}
	jobID := util.ToInt64(job["id"])
	if jobID <= 0 {
		return nil
	}
	srcPaths := parsePathList(job["srcPath"])
	dstPaths := parsePathList(job["dstPath"])
	if len(srcPaths) == 0 || len(dstPaths) == 0 {
		return nil
	}

	var firstErr error
	useBatchHardDelete := !trashEnabled(job)
	sessOpts := native.SessionOptions{ProxyURL: globalNativeProxyURL()}
	for _, src := range srcPaths {
		normSrc := normalizeDirPath(src)
		for _, base := range dstPaths {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			dst := jobDstForSrc(job, src, base)
			normDst := normalizeDirPath(dst)
			if pairErrors != nil {
				if pErr := pairErrors[[2]string{normSrc, normDst}]; pErr != nil {
					continue
				}
			}
			plan, err := buildTrackedDeletePlanForPair(ctx, cfg, job, jobID, normSrc, normDst)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				errMsg := err.Error()
				jt.appendFinish(NewDeleteJobTaskItem(jt.TaskID, normDst, "", nil, taskStatusFailed, &errMsg, taskItemPath, time.Now().Unix()))
				continue
			}
			if plan.emptyGuardCount > 0 {
				jt.fullSyncDeleteGuardSkipped.Store(true)
				errMsg := msg.MirrorDeleteGuard(fmt.Sprintf("source directory %q listed empty while tracked baseline holds %d entries", normSrc, plan.emptyGuardCount))
				jt.appendFinish(NewDeleteJobTaskItem(jt.TaskID, normDst, "", nil, taskStatusFailed, &errMsg, taskItemPath, time.Now().Unix()))
				continue
			}

			var baselineCleanup []string
			baselineCleanup = append(baselineCleanup, plan.staleBaseline...)

			if useBatchHardDelete && len(plan.deleteCandidates) > 0 {
				if err := jt.controller().WaitIfPaused(ctx); err != nil {
					return err
				}
				delErrs, batchErr := cfg.DeleteFilesBatch(ctx, normDst, plan.deleteCandidates, sessOpts)
				for _, rel := range plan.deleteCandidates {
					itemDir := normalizeDirPath(path.Join(strings.TrimRight(normDst, "/"), path.Dir(rel)))
					if path.Dir(rel) == "." {
						itemDir = normDst
					}
					itemName := path.Base(rel)
					size := plan.deleteSizes[rel]
					var delErr error
					if e, has := delErrs[rel]; has {
						delErr = e
					} else if batchErr != nil {
						delErr = batchErr
					}
					if delErr != nil {
						if firstErr == nil {
							firstErr = delErr
						}
						errText := delErr.Error()
						jt.appendFinish(NewDeleteJobTaskItem(jt.TaskID, itemDir, itemName, size, taskStatusFailed, &errText, taskItemFile, time.Now().Unix()))
						continue
					}
					jt.appendFinish(NewDeleteJobTaskItem(jt.TaskID, itemDir, itemName, size, taskStatusSuccess, nil, taskItemFile, time.Now().Unix()))
					baselineCleanup = append(baselineCleanup, rel)
				}
			} else {
				for _, rel := range plan.deleteCandidates {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					if err := jt.controller().WaitIfPaused(ctx); err != nil {
						return err
					}
					itemDir := normalizeDirPath(path.Join(strings.TrimRight(normDst, "/"), path.Dir(rel)))
					if path.Dir(rel) == "." {
						itemDir = normDst
					}
					itemName := path.Base(rel)
					size := plan.deleteSizes[rel]
					delErr := nativeDeleteOrTrash(ctx, cfg, jt, job, normDst, itemDir, itemName, false)
					if delErr != nil {
						if firstErr == nil {
							firstErr = delErr
						}
						errText := delErr.Error()
						jt.appendFinish(NewDeleteJobTaskItem(jt.TaskID, itemDir, itemName, size, taskStatusFailed, &errText, taskItemFile, time.Now().Unix()))
						continue
					}
					jt.appendFinish(NewDeleteJobTaskItem(jt.TaskID, itemDir, itemName, size, taskStatusSuccess, nil, taskItemFile, time.Now().Unix()))
					baselineCleanup = append(baselineCleanup, rel)
				}
			}

			if len(baselineCleanup) > 0 {
				if err := mapper.DeleteOnewayBaselinePaths(jobID, normSrc, normDst, baselineCleanup); err != nil && firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

func dryRunTrackedOneWayDeletes(ctx context.Context, sess *dryRunSession, cfg native.Config, job map[string]interface{}) error {
	jobID := util.ToInt64(job["id"])
	if jobID <= 0 {
		return nil
	}
	delReason := "源端已删除（单向基线追踪）"
	if trashEnabled(job) {
		delReason = fmt.Sprintf("源端已删除（单向基线追踪，移入回收站 %s）", jobTrashDir(job))
	}
	for _, src := range parsePathList(job["srcPath"]) {
		normSrc := normalizeDirPath(src)
		for _, base := range parsePathList(job["dstPath"]) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			normDst := normalizeDirPath(jobDstForSrc(job, src, base))
			plan, err := buildTrackedDeletePlanForPair(ctx, cfg, job, jobID, normSrc, normDst)
			if err != nil {
				return err
			}
			if plan.emptyGuardCount > 0 {
				sess.addItem(DryRunItem{
					Action:   "skip",
					DstPath:  normDst,
					FileName: "",
					Reason:   "源目录为空触发空源保护，已保留目标项",
				})
				continue
			}
			for _, rel := range plan.deleteCandidates {
				itemDir := normalizeDirPath(path.Join(strings.TrimRight(normDst, "/"), path.Dir(rel)))
				if path.Dir(rel) == "." {
					itemDir = normDst
				}
				sess.addItem(DryRunItem{
					Action:   "delete",
					DstPath:  itemDir,
					FileName: path.Base(rel),
					FileSize: plan.deleteSizes[rel],
					Reason:   delReason,
				})
			}
		}
	}
	return nil
}
