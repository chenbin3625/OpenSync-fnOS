package service

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"opensync/internal/mapper"
	"opensync/internal/msg"
	"opensync/internal/native"
	"opensync/pkg/util"
)

type NativeRenameInput struct {
	OldName string `json:"oldName"`
	NewName string `json:"newName"`
}

type NativeRenameResult struct {
	OldName     string `json:"oldName"`
	NewName     string `json:"newName"`
	UpdatedJobs int    `json:"updatedJobs"`
}

type NativeExportInput struct {
	Names    []string `json:"names,omitempty"`
	Password string   `json:"password,omitempty"`
}

type NativeExportResult struct {
	Content   string `json:"content"`
	Encrypted bool   `json:"encrypted"`
	Filename  string `json:"filename"`
}

type NativeAboutInput struct {
	Name string `json:"name"`
}

type NativeBrowseInput struct {
	Path   string `json:"path"`
	Paged  bool   `json:"paged"`
	Cursor string `json:"cursor"`
	Fresh  bool   `json:"fresh"`
}

type NativeBrowseResult struct {
	Path       string            `json:"path"`
	Entries    []native.DirEntry `json:"entries"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

type NativeRestoreInput struct {
	SrcPath     string `json:"srcPath"`
	DstLocalDir string `json:"dstLocalDir"`
	IsDir       bool   `json:"isDir"`
	Overwrite   bool   `json:"overwrite"`
}

type NativePathInput struct {
	Path string `json:"path"`
}

type NativeMovePathInput struct {
	SrcPath string `json:"srcPath"`
	DstPath string `json:"dstPath"`
	IsDir   bool   `json:"isDir"`
}

type NativeDeletePathInput struct {
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
}

// RenameNativeStorage atomically renames a remote and cascades the new name
// across composite remotes (crypt/chunker/compress/hasher/union) and all
// native sync jobs (srcPath, dstPath, dstMap).
func RenameNativeStorage(ctx context.Context, in NativeRenameInput) (*NativeRenameResult, error) {
	return renameNativeStorage(in, nil, nil)
}

func renameNativeStorage(in NativeRenameInput, oldParams, newParams map[string]string) (*NativeRenameResult, error) {
	oldName := strings.TrimSpace(in.OldName)
	newName := strings.TrimSpace(in.NewName)
	if oldName == "" || newName == "" {
		return nil, publicError("请提供原存储名称和新存储名称")
	}

	// Retry up to 3 times if a concurrent job creation adds a new loaded
	// JobClient referencing oldName between snapshot and acquiring alistRefMu.
	for attempt := 0; attempt < 3; attempt++ {
		// 1. Snapshot candidate affected job IDs outside alistRefMu so we can
		// acquire JobClient.opMu in ascending jobID order BEFORE alistRefMu
		// (matching EditJobClient's lock order and preventing AB-BA deadlocks).
		snapID, _, _, snapCfg, err := loadExistingNativeRowFull()
		if err != nil {
			return nil, err
		}
		if snapID == 0 {
			return nil, publicError("未找到内置存储引擎")
		}
		snapOld, ok := snapCfg.FindKey(oldName)
		if !ok {
			return nil, publicError(fmt.Sprintf("存储 %q 不存在", oldName))
		}
		snapJobs, err := mapper.GetJobListAll()
		if err != nil {
			return nil, err
		}
		var candIDs []int64
		for _, job := range snapJobs {
			if util.ToInt64(job["alistId"]) != snapID {
				continue
			}
			for _, p := range append(parsePathList(job["srcPath"]), parsePathList(job["dstPath"])...) {
				seg, _, _ := strings.Cut(strings.TrimPrefix(p, "/"), "/")
				if strings.EqualFold(seg, snapOld) {
					candIDs = append(candIDs, util.ToInt64(job["id"]))
					break
				}
			}
		}
		slices.Sort(candIDs)

		// Lock existing loaded JobClients in ascending ID order and reserve
		// JobDoing so neither edits nor manual/scheduled runs can race rename.
		type lockedClient struct {
			id      int64
			jc      *JobClient
			claimed bool
		}
		var locked []lockedClient
		lockedMap := make(map[int64]*JobClient, len(candIDs))
		releaseLocked := func() {
			for i := len(locked) - 1; i >= 0; i-- {
				if locked[i].claimed {
					locked[i].jc.markDone()
				}
				locked[i].jc.opMu.Unlock()
			}
		}

		var busyJobLabel string
		for _, jid := range candIDs {
			jobClientListMu.RLock()
			jc := jobClientList[jid]
			jobClientListMu.RUnlock()
			if jc == nil {
				continue
			}
			if !jc.lockOp() {
				jc.opMu.Unlock()
				continue
			}
			jc.mu.Lock()
			busy := jc.JobDoing || jc.CurrentJobTask != nil
			if !busy {
				jc.JobDoing = true
				jc.signalStateChangeLocked()
			}
			snap := jc.Job
			jc.mu.Unlock()
			if busy {
				busyJobLabel = jobDisplayName(snap)
				jc.opMu.Unlock()
				releaseLocked()
				return nil, publicError(fmt.Sprintf("关联任务「%s」正在执行中，请待任务结束后再重命名存储", busyJobLabel))
			}
			locked = append(locked, lockedClient{id: jid, jc: jc, claimed: true})
			lockedMap[jid] = jc
		}

		alistRefMu.Lock()
		alistMutationMu.Lock()

		id, remark, _, c, err := loadExistingNativeRowFull()
		if err != nil {
			alistMutationMu.Unlock()
			alistRefMu.Unlock()
			releaseLocked()
			return nil, err
		}
		if id == 0 {
			alistMutationMu.Unlock()
			alistRefMu.Unlock()
			releaseLocked()
			return nil, publicError("未找到内置存储引擎")
		}
		actualOld, ok := c.FindKey(oldName)
		if !ok {
			alistMutationMu.Unlock()
			alistRefMu.Unlock()
			releaseLocked()
			return nil, publicError(fmt.Sprintf("存储 %q 不存在", oldName))
		}
		if newParams != nil && (actualOld != oldName || !maps.Equal(c[actualOld], oldParams)) {
			alistMutationMu.Unlock()
			alistRefMu.Unlock()
			releaseLocked()
			return nil, publicError("存储配置已变更，请重新打开编辑后再保存")
		}
		if newParams != nil {
			c[actualOld] = newParams
		}
		renamedCfg, err := c.Rename(actualOld, newName)
		if err != nil {
			err = publicError(err.Error())
		} else if newParams != nil {
			// The persisted jobs still use the old name until the transaction commits.
			err = validateExistingNativeJobs(id, c, []string{actualOld})
		}
		if err != nil {
			alistMutationMu.Unlock()
			alistRefMu.Unlock()
			releaseLocked()
			return nil, err
		}

		jobs, err := mapper.GetJobListAll()
		if err != nil {
			alistMutationMu.Unlock()
			alistRefMu.Unlock()
			releaseLocked()
			return nil, err
		}
		rewriteVirtualPath := func(p string) (string, bool) {
			trimmed := strings.TrimPrefix(p, "/")
			seg, rest, hasRest := strings.Cut(trimmed, "/")
			if !strings.EqualFold(seg, actualOld) {
				return p, false
			}
			if hasRest {
				return "/" + newName + "/" + rest, true
			}
			return "/" + newName, true
		}
		rewriteList := func(raw interface{}) ([]string, bool) {
			paths := parsePathList(raw)
			changed := false
			for i, p := range paths {
				if nextP, ok := rewriteVirtualPath(p); ok {
					paths[i] = nextP
					changed = true
				}
			}
			return paths, changed
		}

		type jobUpdate struct {
			id  int64
			job map[string]interface{}
		}
		var updatedJobs []jobUpdate
		needRetry := false
		for _, job := range jobs {
			if util.ToInt64(job["alistId"]) != id {
				continue
			}
			newSrc, srcChanged := rewriteList(job["srcPath"])
			newDst, dstChanged := rewriteList(job["dstPath"])
			if !srcChanged && !dstChanged {
				continue
			}
			jobID := util.ToInt64(job["id"])
			jobClientListMu.RLock()
			liveJC := jobClientList[jobID]
			jobClientListMu.RUnlock()
			if liveJC != nil && lockedMap[jobID] != liveJC {
				// A new JobClient was loaded/created after our snapshot; retry locking.
				needRetry = true
				break
			}

			cloned := cloneJobConfig(job)
			if srcChanged {
				enc, _ := json.Marshal(newSrc)
				cloned["srcPath"] = string(enc)
			}
			if dstChanged {
				enc, _ := json.Marshal(newDst)
				cloned["dstPath"] = string(enc)
			}
			rewriteDirPath := func(p string) string {
				trimmed := strings.TrimRight(p, "/")
				if nextP, ok := rewriteVirtualPath(trimmed); ok {
					return normalizeDirPath(nextP)
				}
				return p
			}
			if stored := parseStoredJobDstMap(cloned["dstMap"]); stored != nil {
				nextBase := make([]string, len(stored.Base))
				for i, b := range stored.Base {
					nextBase[i] = rewriteDirPath(b)
				}
				nextMap := make(map[string]string, len(stored.Map))
				for k, v := range stored.Map {
					srcPart, dstPart, hasSep := strings.Cut(k, "\x00")
					nextKey := k
					if hasSep {
						nextKey = jobDstMapKey(rewriteDirPath(srcPart), rewriteDirPath(dstPart))
					}
					nextMap[nextKey] = rewriteDirPath(v)
				}
				if enc, encErr := json.Marshal(jobDstMap{Version: stored.Version, Base: nextBase, Map: nextMap}); encErr == nil {
					cloned["dstMap"] = string(enc)
				}
			}
			updatedJobs = append(updatedJobs, jobUpdate{id: jobID, job: cloned})
		}
		if needRetry {
			alistMutationMu.Unlock()
			alistRefMu.Unlock()
			releaseLocked()
			continue
		}

		// Commit engine config and all affected jobs atomically in one transaction.
		data, err := json.Marshal(renamedCfg)
		if err != nil {
			alistMutationMu.Unlock()
			alistRefMu.Unlock()
			releaseLocked()
			return nil, err
		}
		jobPayloads := make([]map[string]interface{}, len(updatedJobs))
		for i, u := range updatedJobs {
			jobPayloads[i] = u.job
		}
		if err := mapper.UpdateAlistAndJobPathsTx(id, remark, native.EngineURL, string(data), jobPayloads); err != nil {
			alistMutationMu.Unlock()
			alistRefMu.Unlock()
			releaseLocked()
			return nil, err
		}
		for _, u := range updatedJobs {
			if jc := lockedMap[u.id]; jc != nil {
				jc.mu.Lock()
				jc.Job = cloneJobConfig(u.job)
				jc.mu.Unlock()
			}
			if util.ToInt(u.job["method"]) == 3 {
				clearBisyncRuntimeAndState(u.id)
			}
		}
		refreshNativeStorageConcurrencyLimits(renamedCfg)
		RemoveEngineHealth(id)
		alistMutationMu.Unlock()
		alistRefMu.Unlock()
		releaseLocked()
		return &NativeRenameResult{
			OldName:     actualOld,
			NewName:     newName,
			UpdatedJobs: len(updatedJobs),
		}, nil
	}
	return nil, publicError("关联任务正在变更，请稍后重试重命名")
}

// ExportNativeConfig exports stored rclone remotes to plain or encrypted rclone.conf format.
func ExportNativeConfig(ctx context.Context, in NativeExportInput) (*NativeExportResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	_, c, err := loadExistingNativeRow()
	if err != nil {
		return nil, err
	}
	exportCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	content, err := c.ExportINI(exportCtx, in.Names, in.Password)
	if err != nil {
		return nil, publicError(err.Error())
	}
	return &NativeExportResult{
		Content:   content,
		Encrypted: in.Password != "",
		Filename:  fmt.Sprintf("rclone-%s.conf", time.Now().Format("20060102-150405")),
	}, nil
}

// AboutNativeStorage queries total/used/free quota for a remote or local authorized roots.
func AboutNativeStorage(ctx context.Context, in NativeAboutInput) (*native.AboutInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	_, c, err := loadExistingNativeRow()
	if err != nil {
		return nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	info, err := c.About(probeCtx, in.Name, native.SessionOptions{ProxyURL: globalNativeProxyURL()})
	if err != nil {
		return nil, publicError(err.Error())
	}
	return info, nil
}

// BrowseNativeStorage lists files and directories at virtualPath (including decrypted crypt).
func BrowseNativeStorage(ctx context.Context, in NativeBrowseInput) (*NativeBrowseResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	_, c, err := loadExistingNativeRow()
	if err != nil {
		return nil, err
	}
	vPath := strings.TrimSpace(in.Path)
	if vPath == "" {
		vPath = "/"
	}
	browseCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	entries, next, exists, err := c.BrowseDirEntries(browseCtx, vPath, in.Paged, in.Cursor, in.Fresh, native.SessionOptions{ProxyURL: globalNativeProxyURL()})
	if err != nil {
		return nil, publicError(err.Error())
	}
	if !exists {
		return nil, publicError("目录不存在")
	}
	if entries == nil {
		entries = []native.DirEntry{}
	}
	return &NativeBrowseResult{
		Path:       vPath,
		Entries:    entries,
		NextCursor: next,
	}, nil
}

// MkdirNativeStorage creates a directory at in.Path.
func MkdirNativeStorage(ctx context.Context, in NativePathInput) error {
	if ctx == nil {
		ctx = context.Background()
	}
	vPath := strings.TrimSpace(in.Path)
	if vPath == "" {
		return publicError("请提供要创建的目录路径")
	}
	_, c, err := loadExistingNativeRow()
	if err != nil {
		return err
	}
	opCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := c.Mkdir(opCtx, vPath, native.SessionOptions{ProxyURL: globalNativeProxyURL()}); err != nil {
		return publicError(err.Error())
	}
	return nil
}

// MoveNativeStoragePath moves or renames a file/directory within native storage.
func MoveNativeStoragePath(ctx context.Context, in NativeMovePathInput) error {
	if ctx == nil {
		ctx = context.Background()
	}
	src := strings.TrimSpace(in.SrcPath)
	dst := strings.TrimSpace(in.DstPath)
	if src == "" || dst == "" {
		return publicError("请提供源路径与目标路径")
	}
	_, c, err := loadExistingNativeRow()
	if err != nil {
		return err
	}
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if err := c.MovePath(opCtx, src, dst, in.IsDir, native.SessionOptions{ProxyURL: globalNativeProxyURL()}); err != nil {
		return publicError(err.Error())
	}
	return nil
}

// DeleteNativeStoragePath deletes a file or directory subtree within native storage.
func DeleteNativeStoragePath(ctx context.Context, in NativeDeletePathInput) error {
	if ctx == nil {
		ctx = context.Background()
	}
	vPath := strings.TrimSpace(in.Path)
	if vPath == "" {
		return publicError("请提供要删除的路径")
	}
	_, c, err := loadExistingNativeRow()
	if err != nil {
		return err
	}
	opCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := c.DeletePath(opCtx, vPath, in.IsDir, native.SessionOptions{ProxyURL: globalNativeProxyURL()}); err != nil {
		return publicError(err.Error())
	}
	return nil
}

// SizeNativeStorage computes recursive object count and byte total for in.Path.
func SizeNativeStorage(ctx context.Context, in NativePathInput) (*native.SizeInfo, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	vPath := strings.TrimSpace(in.Path)
	if vPath == "" {
		return nil, publicError("请提供要统计的目录路径")
	}
	_, c, err := loadExistingNativeRow()
	if err != nil {
		return nil, err
	}
	opCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	info, err := c.Size(opCtx, vPath, native.SessionOptions{ProxyURL: globalNativeProxyURL()})
	if err != nil {
		return nil, publicError(err.Error())
	}
	return info, nil
}

// --- Async Consistency Check (`rclone check`) Manager ---

const (
	jobCheckTimeout    = 30 * time.Minute
	jobCheckMaxTracked = 64
)

type JobCheckResult struct {
	JobID             int64    `json:"jobId"`
	Status            string   `json:"status"` // "running" | "completed" | "failed" | "cancelled"
	Mode              string   `json:"mode"`   // "check" | "download" | "cryptcheck"
	Download          bool     `json:"download"`
	CryptCheck        bool     `json:"cryptCheck"`
	StartedAt         int64    `json:"startedAt"`
	FinishedAt        int64    `json:"finishedAt"`
	CheckedFiles      int64    `json:"checkedFiles"`
	TotalFiles        int64    `json:"totalFiles"`
	MatchedCount      int      `json:"matchedCount"`
	DifferCount       int      `json:"differCount"`
	MissingOnSrcCount int      `json:"missingOnSrcCount"`
	MissingOnDstCount int      `json:"missingOnDstCount"`
	ErrorCount        int      `json:"errorCount"`
	Differ            []string `json:"differ"`
	MissingOnSrc      []string `json:"missingOnSrc"`
	MissingOnDst      []string `json:"missingOnDst"`
	Errors            []string `json:"errors"`
	Truncated         bool     `json:"truncated"`
	Error             string   `json:"error,omitempty"`
}

type jobCheckSession struct {
	mu     sync.Mutex
	result JobCheckResult
	cancel context.CancelFunc
}

func (s *jobCheckSession) snapshot() *JobCheckResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := s.result
	cp.Differ = append([]string{}, s.result.Differ...)
	cp.MissingOnSrc = append([]string{}, s.result.MissingOnSrc...)
	cp.MissingOnDst = append([]string{}, s.result.MissingOnDst...)
	cp.Errors = append([]string{}, s.result.Errors...)
	return &cp
}

func (s *jobCheckSession) finish(status, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.result.Status != "running" {
		return
	}
	s.result.Status = status
	s.result.Error = errMsg
	s.result.FinishedAt = time.Now().Unix()
}

var (
	jobCheckMu       sync.Mutex
	jobCheckSessions = make(map[int64]*jobCheckSession)
)

// StartJobCheck launches an asynchronous `rclone check` / `rclone cryptcheck` verification for a native job.
func StartJobCheck(jobID int64, download, cryptCheck bool) (*JobCheckResult, error) {
	job, err := mapper.GetJobByID(jobID)
	if err != nil {
		return nil, publicErrorIf(err, msg.T(msg.JobNotFound))
	}
	if err := CleanJobInput(job); err != nil {
		return nil, err
	}
	cfg, err := loadNative(util.ToInt64(job["alistId"]))
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, publicError(legacyOpenListJobDisabledMsg)
	}
	if download && cryptCheck {
		cryptCheck = false
	}
	mode := "check"
	if download {
		mode = "download"
	} else if cryptCheck {
		mode = "cryptcheck"
		hasCrypt := false
		for _, p := range append(parsePathList(job["srcPath"]), parsePathList(job["dstPath"])...) {
			if ep, resErr := cfg.Resolve(p); resErr == nil && ep.IsCrypt {
				hasCrypt = true
				break
			}
		}
		if !hasCrypt {
			return nil, publicError("cryptcheck 仅支持包含 Crypt 加密存储的任务")
		}
	}

	jobCheckMu.Lock()
	if existing, ok := jobCheckSessions[jobID]; ok && existing.snapshot().Status == "running" {
		jobCheckMu.Unlock()
		return nil, publicError(msg.T(msg.CheckAlreadyRunning))
	}
	if len(jobCheckSessions) >= jobCheckMaxTracked {
		var oldestID, oldestAt int64
		for id, sess := range jobCheckSessions {
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
			delete(jobCheckSessions, oldestID)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), jobCheckTimeout)
	sess := &jobCheckSession{
		result: JobCheckResult{
			JobID:        jobID,
			Status:       "running",
			Mode:         mode,
			Download:     download,
			CryptCheck:   cryptCheck,
			StartedAt:    time.Now().Unix(),
			Differ:       []string{},
			MissingOnSrc: []string{},
			MissingOnDst: []string{},
			Errors:       []string{},
		},
		cancel: cancel,
	}
	jobCheckSessions[jobID] = sess
	jobCheckMu.Unlock()

	go func() {
		defer cancel()
		limits := taskRuntimeLimitsForJob(job)
		filters := buildNativeRcloneFilterLines(job)
		extra, _, _ := parseNativeExtraFlags(job["extraFlags"])
		method := util.ToInt(job["method"])
		srcPaths := parsePathList(job["srcPath"])
		dstPaths := parsePathList(job["dstPath"])
		multiPair := len(srcPaths)*len(dstPaths) > 1
		const capLimit = 500
		appendCapped := func(dst *[]string, prefix string, items []string) bool {
			truncated := false
			for _, item := range items {
				if len(*dst) >= capLimit {
					truncated = true
					break
				}
				if prefix != "" {
					*dst = append(*dst, path.Join(prefix, item))
				} else {
					*dst = append(*dst, item)
				}
			}
			return truncated
		}

		var baseChecked, baseTotal int64
		for _, src := range srcPaths {
			for _, base := range dstPaths {
				if ctx.Err() != nil {
					sess.finish("cancelled", "")
					return
				}
				dst := jobDstForSrc(job, src, base)
				var pairChecked, pairTotal int64
				res, err := cfg.Check(ctx, src, dst, native.CheckOptions{
					Download:        download,
					CryptCheck:      cryptCheck,
					Compare:         util.ToInt(job["compareMode"]),
					OneWay:          method == 0,
					Checkers:        limits.ScanConcurrency,
					Retries:         limits.MaxRetries,
					FilterLines:     filters,
					MinSize:         util.ToInt64(job["minFileSize"]),
					MaxSize:         util.ToInt64(job["maxFileSize"]),
					MinAgeMinutes:   util.ToInt(job["minFileAgeMinutes"]),
					OlderThanDays:   util.ToInt(job["olderThanDays"]),
					NewerThanDays:   util.ToInt(job["newerThanDays"]),
					BwLimit:         effectiveJobBwLimit(job),
					TPSLimit:        jobFloat64(job["tpsLimit"]),
					TPSLimitBurst:   util.ToInt(job["tpsLimitBurst"]),
					ModifyWindow:    strings.TrimSpace(util.StringValue(job["modifyWindow"])),
					FastList:        util.ToInt(job["fastList"]) == 1,
					FollowSymlinks:  util.ToInt(job["followSymlinks"]) == 1,
					UserAgent:       extra.UserAgent,
					IOTimeout:       extra.IOTimeout,
					ConTimeout:      extra.ConTimeout,
					LowLevelRetries: extra.LowLevelRetries,
					RetriesSleep:    extra.RetriesSleep,
					ProxyURL:        globalNativeProxyURL(),
					MaxItems:        capLimit,
				}, func(st *native.Stats) {
					if st == nil {
						return
					}
					if st.Checks > pairChecked {
						pairChecked = st.Checks
					}
					if st.TotalChecks > pairTotal {
						pairTotal = st.TotalChecks
					}
					sess.mu.Lock()
					sess.result.CheckedFiles = baseChecked + pairChecked
					sess.result.TotalFiles = baseTotal + pairTotal
					sess.mu.Unlock()
				})
				if err != nil {
					if ctx.Err() != nil {
						sess.finish("cancelled", "")
					} else {
						sess.finish("failed", err.Error())
					}
					return
				}
				baseChecked += pairChecked
				baseTotal += pairTotal
				prefix := ""
				if multiPair {
					prefix = path.Base(strings.TrimRight(src, "/"))
				}
				sess.mu.Lock()
				sess.result.MatchedCount += res.MatchedCount
				sess.result.DifferCount += res.DifferCount
				sess.result.MissingOnSrcCount += res.MissingOnSrcCount
				sess.result.MissingOnDstCount += res.MissingOnDstCount
				sess.result.ErrorCount += res.ErrorCount
				if res.Truncated {
					sess.result.Truncated = true
				}
				if appendCapped(&sess.result.Differ, prefix, res.Differ) {
					sess.result.Truncated = true
				}
				if appendCapped(&sess.result.MissingOnSrc, prefix, res.MissingOnSrc) {
					sess.result.Truncated = true
				}
				if appendCapped(&sess.result.MissingOnDst, prefix, res.MissingOnDst) {
					sess.result.Truncated = true
				}
				if appendCapped(&sess.result.Errors, prefix, res.Errors) {
					sess.result.Truncated = true
				}
				sess.mu.Unlock()
			}
		}
		sess.finish("completed", "")
	}()

	return sess.snapshot(), nil
}

func GetJobCheck(jobID int64) (*JobCheckResult, error) {
	jobCheckMu.Lock()
	sess, ok := jobCheckSessions[jobID]
	jobCheckMu.Unlock()
	if !ok || sess == nil {
		return nil, nil
	}
	return sess.snapshot(), nil
}

func CancelJobCheck(jobID int64) error {
	jobCheckMu.Lock()
	sess, ok := jobCheckSessions[jobID]
	jobCheckMu.Unlock()
	if !ok || sess == nil {
		return nil
	}
	if sess.cancel != nil {
		sess.cancel()
	}
	sess.finish("cancelled", "")
	return nil
}

// IsJobCheckRunning reports whether an asynchronous consistency check is active for jobID.
func IsJobCheckRunning(jobID int64) bool {
	jobCheckMu.Lock()
	sess, ok := jobCheckSessions[jobID]
	jobCheckMu.Unlock()
	if !ok || sess == nil {
		return false
	}
	return sess.snapshot().Status == "running"
}

// ExportJobCheckReport formats the latest consistency check result for jobID as CSV.
func ExportJobCheckReport(jobID int64, format string) (string, string, []byte, error) {
	if format := strings.ToLower(strings.TrimSpace(format)); format != "" && format != "csv" {
		return "", "", nil, publicError("仅支持 CSV 导出")
	}
	res, err := GetJobCheck(jobID)
	if err != nil {
		return "", "", nil, err
	}
	if res == nil {
		return "", "", nil, publicError("暂无可导出的一致性校验报告")
	}
	ts := time.Now().Format("20060102-150405")
	var buf bytes.Buffer
	buf.WriteString("\xEF\xBB\xBF") // UTF-8 BOM for Excel compatibility
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"分类", "路径/详情"})
	writeRows := func(cat string, items []string) {
		for _, item := range items {
			_ = w.Write([]string{cat, csvSafeCell(item)})
		}
	}
	writeRows("内容差异", res.Differ)
	writeRows("目标缺失", res.MissingOnDst)
	writeRows("源端缺失", res.MissingOnSrc)
	writeRows("校验错误", res.Errors)
	w.Flush()
	return fmt.Sprintf("check-report-job-%d-%s.csv", jobID, ts), "text/csv; charset=utf-8", buf.Bytes(), nil
}

// RepairJobCheckResult triggers a targeted force-overwrite sync for all differing
// and destination-missing files reported by the latest completed consistency check.
func RepairJobCheckResult(jobID int64) (int, error) {
	res, err := GetJobCheck(jobID)
	if err != nil {
		return 0, err
	}
	if res == nil {
		return 0, publicError("请先执行一致性校验后再修复差异")
	}
	if res.Status == "running" {
		return 0, publicError("一致性校验仍在进行中，请待校验完成后再修复")
	}
	job, err := mapper.GetJobByID(jobID)
	if err != nil {
		return 0, publicErrorIf(err, msg.T(msg.JobNotFound))
	}
	if util.ToInt(job["method"]) == 3 {
		return 0, publicError("双向同步暂不支持局部差异修复，请直接执行任务同步")
	}
	srcPaths := parsePathList(job["srcPath"])
	dstPaths := parsePathList(job["dstPath"])
	multiPair := len(srcPaths)*len(dstPaths) > 1

	seen := make(map[string]struct{})
	targets := make([]string, 0, len(res.Differ)+len(res.MissingOnDst))
	addPath := func(raw string) {
		p := strings.TrimPrefix(path.Clean("/"+strings.TrimSpace(raw)), "/")
		if p == "" || p == "." || strings.HasPrefix(p, "../") {
			return
		}
		if multiPair {
			// Multi-pair check results prefix items with the source base folder name;
			// strip the first segment if it matches a source root basename.
			seg, rest, hasRest := strings.Cut(p, "/")
			if hasRest {
				for _, s := range srcPaths {
					if path.Base(strings.TrimRight(s, "/")) == seg {
						p = rest
						break
					}
				}
			}
		}
		if _, dup := seen[p]; dup {
			return
		}
		seen[p] = struct{}{}
		targets = append(targets, p)
	}
	for _, p := range res.Differ {
		addPath(p)
	}
	for _, p := range res.MissingOnDst {
		addPath(p)
	}
	if len(targets) == 0 {
		return 0, publicError("当前校验结果中没有需要修复的差异或目标缺失文件")
	}
	client, err := GetJobClientByID(jobID)
	if err != nil {
		return 0, err
	}
	if err := client.DoRepairFiles(targets); err != nil {
		return 0, err
	}
	return len(targets), nil
}

// StartPeriodicCheckScheduler periodically launches consistency checks for jobs
// configured with extraFlags.checkSchedule (daily / weekly / monthly) and alerts
// when mismatches or missing files are detected.
func StartPeriodicCheckScheduler() func() {
	ctx, cancel := context.WithCancel(context.Background())
	var (
		mu      sync.Mutex
		lastRun = make(map[int64]time.Time)
	)
	checkInterval := func(schedule string) time.Duration {
		switch schedule {
		case "daily":
			return 24 * time.Hour
		case "weekly":
			return 7 * 24 * time.Hour
		case "monthly":
			return 30 * 24 * time.Hour
		default:
			return 0
		}
	}
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				jobs, err := mapper.GetJobListAll()
				if err != nil {
					continue
				}
				for _, job := range jobs {
					if ctx.Err() != nil {
						return
					}
					if util.ToInt(job["enable"]) != 1 {
						continue
					}
					extra, _, parseErr := parseNativeExtraFlags(job["extraFlags"])
					if parseErr != nil || extra.CheckSchedule == "" {
						continue
					}
					interval := checkInterval(extra.CheckSchedule)
					if interval <= 0 {
						continue
					}
					jobID := util.ToInt64(job["id"])
					if jobID <= 0 || IsJobCheckRunning(jobID) {
						continue
					}
					mu.Lock()
					prev := lastRun[jobID]
					due := prev.IsZero() || now.Sub(prev) >= interval
					if due {
						lastRun[jobID] = now
					}
					mu.Unlock()
					if !due {
						continue
					}
					if _, startErr := StartJobCheck(jobID, false, false); startErr != nil {
						continue
					}
					go watchPeriodicCheckAlert(ctx, jobID, jobDisplayName(job))
				}
			}
		}
	}()
	return cancel
}

func watchPeriodicCheckAlert(ctx context.Context, jobID int64, jobName string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	timeout := time.NewTimer(jobCheckTimeout + time.Minute)
	defer timeout.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timeout.C:
			return
		case <-ticker.C:
			res, err := GetJobCheck(jobID)
			if err != nil || res == nil {
				return
			}
			if res.Status == "running" {
				continue
			}
			if res.Status == "completed" && (res.DifferCount > 0 || res.MissingOnDstCount > 0 || res.ErrorCount > 0) {
				title := fmt.Sprintf("[OpenSync] 定时一致性校验发现差异：%s", jobName)
				content := fmt.Sprintf("任务「%s」(#%d) 一致性校验完成：\n一致：%d\n内容差异：%d\n目标缺失：%d\n源端缺失：%d\n错误：%d\n请在「同步任务」中打开该任务查看校验报告或一键修复。",
					jobName, jobID, res.MatchedCount, res.DifferCount, res.MissingOnDstCount, res.MissingOnSrcCount, res.ErrorCount)
				QueueSystemAlertNotification("check_alert", title, content, map[string]string{
					"jobName": jobName,
					"jobId":   fmt.Sprintf("%d", jobID),
				})
			} else if res.Status == "failed" {
				log.Printf("Periodic consistency check failed for job %d: %s", jobID, res.Error)
			}
			return
		}
	}
}
