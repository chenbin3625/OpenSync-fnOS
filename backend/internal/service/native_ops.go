package service

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
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
	Path string `json:"path"`
}

type NativeBrowseResult struct {
	Path    string            `json:"path"`
	Entries []native.DirEntry `json:"entries"`
}

type NativeRestoreInput struct {
	SrcPath     string `json:"srcPath"`
	DstLocalDir string `json:"dstLocalDir"`
	IsDir       bool   `json:"isDir"`
	Overwrite   bool   `json:"overwrite"`
}

// RenameNativeStorage atomically renames a remote and cascades the new name
// across composite remotes (crypt/chunker/compress/hasher/union) and all
// native sync jobs (srcPath, dstPath, dstMap).
func RenameNativeStorage(ctx context.Context, in NativeRenameInput) (*NativeRenameResult, error) {
	oldName := strings.TrimSpace(in.OldName)
	newName := strings.TrimSpace(in.NewName)
	if oldName == "" || newName == "" {
		return nil, publicError("请提供原存储名称和新存储名称")
	}

	alistRefMu.Lock()
	defer alistRefMu.Unlock()
	alistMutationMu.Lock()
	defer alistMutationMu.Unlock()

	id, remark, _, c, err := loadExistingNativeRowFull()
	if err != nil {
		return nil, err
	}
	if id == 0 {
		return nil, publicError("未找到原生备份引擎")
	}
	actualOld, ok := c.FindKey(oldName)
	if !ok {
		return nil, publicError(fmt.Sprintf("存储 %q 不存在", oldName))
	}
	renamedCfg, err := c.Rename(actualOld, newName)
	if err != nil {
		return nil, publicError(err.Error())
	}

	// Find & rewrite all jobs belonging to the native engine that reference actualOld.
	jobs, err := mapper.GetJobListAll()
	if err != nil {
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
	for _, job := range jobs {
		if util.ToInt64(job["alistId"]) != id {
			continue
		}
		newSrc, srcChanged := rewriteList(job["srcPath"])
		newDst, dstChanged := rewriteList(job["dstPath"])
		if !srcChanged && !dstChanged {
			continue
		}
		// Reject rename while an affected job is actively executing.
		jobID := util.ToInt64(job["id"])
		jobClientListMu.RLock()
		jc := jobClientList[jobID]
		jobClientListMu.RUnlock()
		if jc != nil && jc.isBusy() {
			return nil, publicError(fmt.Sprintf("关联任务「%s」正在执行中，请待任务结束后再重命名存储", jobDisplayName(job)))
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
		// Update dstMap keys if srcPath changed so derived destination subdirs stay stable!
		if srcChanged {
			if rawMap := jobDstMapRaw(cloned["dstMap"]); rawMap != "" {
				var parsed map[string]string
				if json.Unmarshal([]byte(rawMap), &parsed) == nil && len(parsed) > 0 {
					nextMap := make(map[string]string, len(parsed))
					for k, v := range parsed {
						if nk, ok := rewriteVirtualPath(k); ok {
							nextMap[nk] = v
						} else {
							nextMap[k] = v
						}
					}
					enc, _ := json.Marshal(nextMap)
					cloned["dstMap"] = string(enc)
				}
			}
		}
		updatedJobs = append(updatedJobs, jobUpdate{id: jobID, job: cloned})
	}

	// Commit engine config first, then update affected jobs and refresh in-memory JobClients.
	data, err := json.Marshal(renamedCfg)
	if err != nil {
		return nil, err
	}
	secret := string(data)
	if err := mapper.UpdateAlist(id, remark, native.EngineURL, &secret); err != nil {
		return nil, err
	}
	for _, u := range updatedJobs {
		if err := mapper.UpdateJob(u.job); err != nil {
			return nil, err
		}
		jobClientListMu.RLock()
		jc := jobClientList[u.id]
		jobClientListMu.RUnlock()
		if jc != nil {
			jc.mu.Lock()
			jc.Job = cloneJobConfig(u.job)
			jc.mu.Unlock()
		}
		pendingDeleteApprovals.ClearForJob(u.id)
	}
	RemoveEngineHealth(id)
	return &NativeRenameResult{
		OldName:     actualOld,
		NewName:     newName,
		UpdatedJobs: len(updatedJobs),
	}, nil
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
	entries, exists, err := c.ListDirEntries(browseCtx, vPath, native.SessionOptions{ProxyURL: globalNativeProxyURL()})
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
		Path:    vPath,
		Entries: entries,
	}, nil
}

// RestoreNativeFile restores a remote file or folder into an fnOS-authorized local directory.
func RestoreNativeFile(ctx context.Context, in NativeRestoreInput) error {
	if ctx == nil {
		ctx = context.Background()
	}
	src := strings.TrimSpace(in.SrcPath)
	dst := strings.TrimSpace(in.DstLocalDir)
	if src == "" || dst == "" {
		return publicError("请提供要恢复的远端路径和本地目标目录")
	}
	_, c, err := loadExistingNativeRow()
	if err != nil {
		return err
	}
	restoreCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if err := c.Restore(restoreCtx, src, dst, in.IsDir, in.Overwrite, native.SessionOptions{ProxyURL: globalNativeProxyURL()}); err != nil {
		return publicError(err.Error())
	}
	return nil
}

// --- Async Consistency Check (`rclone check`) Manager ---

const (
	jobCheckTimeout    = 30 * time.Minute
	jobCheckMaxTracked = 64
)

type JobCheckResult struct {
	JobID             int64    `json:"jobId"`
	Status            string   `json:"status"` // "running" | "completed" | "failed" | "cancelled"
	Download          bool     `json:"download"`
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

// StartJobCheck launches an asynchronous `rclone check` verification for a native job.
func StartJobCheck(jobID int64, download bool) (*JobCheckResult, error) {
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
		return nil, publicError("一致性校验仅支持原生 rclone 引擎任务")
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
			Download:     download,
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
					Download:      download,
					Compare:       util.ToInt(job["compareMode"]),
					OneWay:        method == 0,
					Checkers:      limits.ScanConcurrency,
					Retries:       limits.MaxRetries,
					FilterLines:   filters,
					BwLimit:       effectiveJobBwLimit(job),
					TPSLimit:      jobFloat64(job["tpsLimit"]),
					TPSLimitBurst: util.ToInt(job["tpsLimitBurst"]),
					ModifyWindow:  strings.TrimSpace(util.StringValue(job["modifyWindow"])),
					FastList:      util.ToInt(job["fastList"]) == 1,
					ProxyURL:      globalNativeProxyURL(),
					MaxItems:      capLimit,
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
