package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"opensync/internal/config"
	"opensync/internal/msg"
	"opensync/internal/native"
	"opensync/pkg/util"
)

const nativePairMaxConcurrency = 4

// nativeExtraFlags holds the strictly allowlisted rclone tuning and bisync
// flags persisted as JSON in job.extraFlags.
type nativeExtraFlags struct {
	NoUpdateModtime      bool   `json:"noUpdateModtime,omitempty"`
	Inplace              bool   `json:"inplace,omitempty"`
	CheckFirst           bool   `json:"checkFirst,omitempty"`
	S3NoHead             bool   `json:"s3NoHead,omitempty"`
	IgnoreSize           bool   `json:"ignoreSize,omitempty"`
	TrackRenames         bool   `json:"trackRenames,omitempty"`
	UseMmap              bool   `json:"useMmap,omitempty"`
	MaxTransfer          string `json:"maxTransfer,omitempty"`
	CutoffMode           string `json:"cutoffMode,omitempty"`
	MaxDuration          string `json:"maxDuration,omitempty"`
	OrderBy              string `json:"orderBy,omitempty"`
	MaxBacklog           int    `json:"maxBacklog,omitempty"`
	BufferSize           string `json:"bufferSize,omitempty"`
	MultiThreadCutoff    string `json:"multiThreadCutoff,omitempty"`
	MultiThreadChunkSize string `json:"multiThreadChunkSize,omitempty"`
	UserAgent            string `json:"userAgent,omitempty"`
	IOTimeout            string `json:"ioTimeout,omitempty"`
	ConTimeout           string `json:"conTimeout,omitempty"`
	BisyncResyncMode     string `json:"bisyncResyncMode,omitempty"`
	BisyncMaxDelete      int    `json:"bisyncMaxDelete,omitempty"`
	BisyncForce          bool   `json:"bisyncForce,omitempty"`
}

func extraFlagBool(val interface{}) (bool, bool) {
	if b, ok := val.(bool); ok {
		return b, true
	}
	if n, valid := strictJobEnable(val); valid {
		return n == 1, true
	}
	return false, false
}

func extraFlagString(val interface{}) (string, bool) {
	if val == nil {
		return "", true
	}
	s, ok := val.(string)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(s), true
}

func parseNativeExtraFlags(raw interface{}) (nativeExtraFlags, string, error) {
	if raw == nil {
		return nativeExtraFlags{}, "", nil
	}
	var bytesData []byte
	switch v := raw.(type) {
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" || trimmed == "{}" {
			return nativeExtraFlags{}, "", nil
		}
		bytesData = []byte(trimmed)
	case []byte:
		trimmed := strings.TrimSpace(string(v))
		if trimmed == "" || trimmed == "{}" {
			return nativeExtraFlags{}, "", nil
		}
		bytesData = []byte(trimmed)
	case map[string]interface{}:
		if len(v) == 0 {
			return nativeExtraFlags{}, "", nil
		}
		enc, err := json.Marshal(v)
		if err != nil {
			return nativeExtraFlags{}, "", publicError(msg.T(msg.ExtraFlagsInvalid))
		}
		bytesData = enc
	default:
		return nativeExtraFlags{}, "", publicError(msg.T(msg.ExtraFlagsInvalid))
	}

	var rawMap map[string]interface{}
	if err := json.Unmarshal(bytesData, &rawMap); err != nil {
		return nativeExtraFlags{}, "", publicError(msg.T(msg.ExtraFlagsInvalid))
	}
	var out nativeExtraFlags
	for k, val := range rawMap {
		switch k {
		case "noUpdateModtime", "inplace", "checkFirst", "s3NoHead", "ignoreSize", "trackRenames", "useMmap", "bisyncForce":
			b, ok := extraFlagBool(val)
			if !ok {
				return nativeExtraFlags{}, "", publicError(msg.T(msg.ExtraFlagsInvalid))
			}
			switch k {
			case "noUpdateModtime":
				out.NoUpdateModtime = b
			case "inplace":
				out.Inplace = b
			case "checkFirst":
				out.CheckFirst = b
			case "s3NoHead":
				out.S3NoHead = b
			case "ignoreSize":
				out.IgnoreSize = b
			case "trackRenames":
				out.TrackRenames = b
			case "useMmap":
				out.UseMmap = b
			case "bisyncForce":
				out.BisyncForce = b
			}
		case "maxTransfer":
			s, ok := extraFlagString(val)
			if !ok || native.ValidateRcloneSize(s, false, "invalid") != nil {
				return nativeExtraFlags{}, "", publicError("单次最大传输量（--max-transfer）格式无效，如 10G、500M")
			}
			out.MaxTransfer = s
		case "cutoffMode":
			s, ok := extraFlagString(val)
			if !ok || native.ValidateCutoffMode(s) != nil {
				return nativeExtraFlags{}, "", publicError("超限停止模式（--cutoff-mode）仅支持 hard、soft 或 cautious")
			}
			out.CutoffMode = strings.ToLower(s)
		case "maxDuration":
			s, ok := extraFlagString(val)
			if !ok || native.ValidateDurationSpec(s, 365*24*time.Hour, "invalid") != nil {
				return nativeExtraFlags{}, "", publicError("最大传输时长（--max-duration）格式无效，如 2h、30m")
			}
			out.MaxDuration = s
		case "orderBy":
			s, ok := extraFlagString(val)
			if !ok || native.ValidateOrderBy(s) != nil {
				return nativeExtraFlags{}, "", publicError("传输排序规则（--order-by）格式无效，如 size,asc、modtime,desc")
			}
			out.OrderBy = s
		case "maxBacklog":
			if val == nil || val == "" {
				continue
			}
			n, ok := strictJobInt(val)
			if !ok || n < 0 || n > 100000 {
				return nativeExtraFlags{}, "", publicError("扫描积压上限（--max-backlog）需为 0–100000 的整数")
			}
			out.MaxBacklog = n
		case "bufferSize":
			s, ok := extraFlagString(val)
			if !ok || native.ValidateRcloneSize(s, true, "invalid") != nil {
				return nativeExtraFlags{}, "", publicError("读取缓冲区大小（--buffer-size）格式无效，如 16M、64M 或 0")
			}
			out.BufferSize = s
		case "multiThreadCutoff":
			s, ok := extraFlagString(val)
			if !ok || native.ValidateRcloneSize(s, false, "invalid") != nil {
				return nativeExtraFlags{}, "", publicError("多线程分片阈值（--multi-thread-cutoff）格式无效，如 256M、1G")
			}
			out.MultiThreadCutoff = s
		case "multiThreadChunkSize":
			s, ok := extraFlagString(val)
			if !ok || native.ValidateRcloneSize(s, false, "invalid") != nil {
				return nativeExtraFlags{}, "", publicError("多线程分块大小（--multi-thread-chunk-size）格式无效，如 64M、128M")
			}
			out.MultiThreadChunkSize = s
		case "userAgent":
			s, ok := extraFlagString(val)
			if !ok || native.ValidateUserAgent(s) != nil {
				return nativeExtraFlags{}, "", publicError("User-Agent 格式无效")
			}
			out.UserAgent = s
		case "ioTimeout":
			s, ok := extraFlagString(val)
			if !ok || native.ValidateDurationSpec(s, 24*time.Hour, "invalid") != nil {
				return nativeExtraFlags{}, "", publicError("I/O 超时（--timeout）格式无效，如 5m、30s")
			}
			out.IOTimeout = s
		case "conTimeout":
			s, ok := extraFlagString(val)
			if !ok || native.ValidateDurationSpec(s, 24*time.Hour, "invalid") != nil {
				return nativeExtraFlags{}, "", publicError("连接超时（--contimeout）格式无效，如 1m、15s")
			}
			out.ConTimeout = s
		case "bisyncResyncMode":
			s, ok := extraFlagString(val)
			if !ok || native.ValidateBisyncResyncMode(s) != nil {
				return nativeExtraFlags{}, "", publicError("双向基线冲突策略仅支持 newer、older、path1、path2、larger 或 smaller")
			}
			out.BisyncResyncMode = strings.ToLower(s)
		case "bisyncMaxDelete":
			if val == nil || val == "" {
				continue
			}
			n, ok := strictJobInt(val)
			if !ok || n < 0 || n > 1000000 {
				return nativeExtraFlags{}, "", publicError("双向删除保护阈值（--max-delete）需为 0–1000000 的整数")
			}
			out.BisyncMaxDelete = n
		default:
			return nativeExtraFlags{}, "", publicError(msg.T(msg.ExtraFlagsInvalid))
		}
	}
	if out == (nativeExtraFlags{}) {
		return out, "", nil
	}
	canonical, err := json.Marshal(out)
	if err != nil {
		return out, "", err
	}
	return out, string(canonical), nil
}

func globalNativeProxyURL() string {
	return strings.TrimSpace(config.GetSystemSettings().ProxyURL)
}

func effectiveJobBwLimit(job map[string]interface{}) string {
	if job != nil {
		if bw := strings.TrimSpace(util.StringValue(job["bwLimit"])); bw != "" {
			return bw
		}
	}
	return strings.TrimSpace(config.GetSystemSettings().DefaultBwLimit)
}

func jobFloat64(val interface{}) float64 {
	switch v := val.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	default:
		return 0
	}
}

func buildNativeCopyOptions(job map[string]interface{}, dry bool, backupDir string) native.CopyOptions {
	limits := taskRuntimeLimitsForJob(job)
	exclude := buildJobExcludeMatcher(job)
	include := buildJobIncludeMatcher(job)
	extra, _, _ := parseNativeExtraFlags(job["extraFlags"])
	now := time.Now()
	conflictPolicy := util.ToInt(job["conflictPolicy"])
	return native.CopyOptions{
		Transfers:            limits.CopyConcurrency,
		Checkers:             limits.ScanConcurrency,
		Retries:              limits.MaxRetries,
		Compare:              util.ToInt(job["compareMode"]),
		SkipExisting:         conflictPolicy == 1,
		ConflictPolicy:       conflictPolicy,
		BackupDir:            backupDir,
		DryRun:               dry,
		ReportSkipped:        dry,
		BwLimit:              effectiveJobBwLimit(job),
		TPSLimit:             jobFloat64(job["tpsLimit"]),
		TPSLimitBurst:        util.ToInt(job["tpsLimitBurst"]),
		ModifyWindow:         strings.TrimSpace(util.StringValue(job["modifyWindow"])),
		FastList:             util.ToInt(job["fastList"]) == 1,
		MultiThreadStreams:   util.ToInt(job["multiThreadStreams"]),
		MultiThreadCutoff:    extra.MultiThreadCutoff,
		MultiThreadChunkSize: extra.MultiThreadChunkSize,
		BufferSize:           extra.BufferSize,
		UseMmap:              extra.UseMmap,
		MaxTransfer:          extra.MaxTransfer,
		CutoffMode:           extra.CutoffMode,
		MaxDuration:          extra.MaxDuration,
		OrderBy:              extra.OrderBy,
		MaxBacklog:           extra.MaxBacklog,
		UserAgent:            extra.UserAgent,
		IOTimeout:            extra.IOTimeout,
		ConTimeout:           extra.ConTimeout,
		IgnoreSize:           extra.IgnoreSize,
		CreateEmptySrcDirs:   util.ToInt(job["createEmptySrcDirs"]) == 1,
		FollowSymlinks:       util.ToInt(job["followSymlinks"]) == 1,
		NoUpdateModtime:      extra.NoUpdateModtime,
		Inplace:              extra.Inplace,
		CheckFirst:           extra.CheckFirst,
		S3NoHead:             extra.S3NoHead,
		ProxyURL:             globalNativeProxyURL(),
		Include: func(p string, info os.FileInfo) bool {
			if exclude != nil && exclude.matchesListed(p, info.IsDir()) {
				return false
			}
			return info.IsDir() || (include.matchesListed(p) && jobAllowsFileMeta(job, FileMetadata{Size: info.Size(), Modified: info.ModTime().Unix()}, now))
		},
	}
}

// buildNativeRcloneFilterLines converts OpenSync exclude/include rules into
// rclone --filter-from rules for `rclone check` and `rclone bisync`.
func buildNativeRcloneFilterLines(job map[string]interface{}) []string {
	var lines []string
	excludeM := buildJobExcludeMatcher(job)
	if excludeM != nil {
		for _, rule := range excludeM.rules {
			switch rule.kind {
			case excludeDirectory:
				dir := strings.TrimSuffix(rule.value, "/")
				if strings.Contains(dir, "/") {
					lines = append(lines, "- "+dir+"/**")
					lines = append(lines, "- **/"+dir+"/**")
				} else {
					lines = append(lines, "- "+dir+"/**")
					lines = append(lines, "- **/"+dir+"/**")
				}
			case excludeFileExtension:
				// Case-insensitive extension glob isn't needed for exact lowercase,
				// but rclone filter supports standard globs like *.tmp.
				lines = append(lines, "- "+rule.value)
			case excludeFileName:
				lines = append(lines, "- "+rule.value)
			}
		}
	}
	if includeM := buildJobIncludeMatcher(job); includeM != nil && len(includeM.rules) > 0 {
		for _, rule := range includeM.rules {
			lines = append(lines, "+ "+rule.value)
		}
		lines = append(lines, "- **")
	}
	return lines
}

func bisyncWorkDir(jobID int64) string {
	return filepath.Join(config.DataDir(), "bisync", fmt.Sprintf("job-%d", jobID))
}

func cleanupBisyncWorkDir(jobID int64) {
	if jobID <= 0 {
		return
	}
	_ = os.RemoveAll(bisyncWorkDir(jobID))
}

func nativeTrashBackupSubdir(jt *JobTask, job map[string]interface{}) string {
	if !trashEnabled(job) || util.ToInt(job["conflictPolicy"]) == 2 {
		return ""
	}
	trashDir := jobTrashDir(job)
	now := time.Now().In(schedulerLocation())
	dateBucket := now.Format("2006-01-02")
	var versionBucket string
	if jt != nil {
		versionBucket = jt.nextTrashVersionBucket(now, dateBucket, "", "overwrite")
	} else {
		versionBucket = fmt.Sprintf("%s-%s", now.Format("150405"), randomHexToken(4))
	}
	return path.Join(trashDir, dateBucket, versionBucket)
}

// nativeDeleteOrTrash deletes a target via native rclone or moves it into
// <dstRoot>/<trashDir>/<YYYY-MM-DD>/<versionBucket>/<relPath> when trash is enabled.
func nativeDeleteOrTrash(ctx context.Context, cfg native.Config, jt *JobTask, job map[string]interface{}, dstRoot, itemDir, itemName string, isDir bool) error {
	dstRoot = normalizeDirPath(dstRoot)
	itemDir = normalizeDirPath(itemDir)
	cleanName := strings.TrimSuffix(itemName, "/")
	if !isSafeDeleteTargetName(cleanName) {
		return errors.New(unsafeDeleteTargetMessage(itemDir, itemName))
	}
	srcVirtual := strings.TrimRight(itemDir, "/") + "/" + cleanName
	opts := native.SessionOptions{ProxyURL: globalNativeProxyURL()}
	if !trashEnabled(job) {
		return cfg.DeletePath(ctx, srcVirtual, isDir, opts)
	}
	trashDir := jobTrashDir(job)
	now := time.Now().In(schedulerLocation())
	dateBucket := now.Format("2006-01-02")
	relDir := ""
	if strings.HasPrefix(itemDir, dstRoot) {
		relDir = strings.Trim(strings.TrimPrefix(itemDir, dstRoot), "/")
	}
	var versionBucket string
	if jt != nil {
		versionBucket = jt.nextTrashVersionBucket(now, dateBucket, relDir, cleanName)
	} else {
		versionBucket = fmt.Sprintf("%s-%s", now.Format("150405"), randomHexToken(4))
	}
	targetRel := path.Join(trashDir, dateBucket, versionBucket, relDir, cleanName)
	dstVirtual := strings.TrimRight(dstRoot, "/") + "/" + strings.TrimPrefix(targetRel, "/")
	return cfg.MovePath(ctx, srcVirtual, dstVirtual, isDir, opts)
}

// cleanupNativeExpiredTrash purges date buckets under <dstRoot>/<trashDir>/ older
// than trashRetentionDays for a native engine job.
func cleanupNativeExpiredTrash(ctx context.Context, cfg native.Config, taskID int64, job map[string]interface{}) {
	if !trashEnabled(job) {
		return
	}
	retentionDays := jobTrashRetentionDays(job)
	if retentionDays <= 0 || ctx.Err() != nil {
		return
	}
	trashDir := jobTrashDir(job)
	today := time.Now().In(schedulerLocation())
	cutoffDate := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, schedulerLocation()).AddDate(0, 0, -retentionDays)
	opts := native.SessionOptions{ProxyURL: globalNativeProxyURL()}

	dstRoots := make(map[string]struct{})
	for _, src := range parsePathList(job["srcPath"]) {
		for _, base := range parsePathList(job["dstPath"]) {
			dstRoots[normalizeDirPath(jobDstForSrc(job, src, base))] = struct{}{}
		}
	}
	for dstRoot := range dstRoots {
		if ctx.Err() != nil {
			return
		}
		trashRoot := strings.TrimRight(dstRoot, "/") + "/" + trashDir
		entries, exists, err := cfg.ListDirEntries(ctx, trashRoot, opts)
		if err != nil {
			log.Printf("Task %d native trash cleanup list %q failed: %v", taskID, trashRoot, err)
			continue
		}
		if !exists {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir {
				continue
			}
			bucketTime, parseErr := time.ParseInLocation("2006-01-02", entry.Name, schedulerLocation())
			if parseErr != nil {
				continue
			}
			if bucketTime.Before(cutoffDate) {
				if err := cfg.DeletePath(ctx, entry.Path, true, opts); err != nil {
					log.Printf("Task %d failed to purge expired native trash bucket %q: %v", taskID, entry.Path, err)
				} else {
					log.Printf("Task %d purged expired native trash bucket %q", taskID, entry.Path)
				}
			}
		}
	}
}

// buildNativeFullSyncSnapshot builds a fullSyncSnapshot from native.ListTree
// with the exact same exclude/include/size/time/trash/conflict protection
// semantics as OpenList's scanFullSyncTree.
func buildNativeFullSyncSnapshot(
	ctx context.Context,
	cfg native.Config,
	rootPath string,
	job map[string]interface{},
	spec *excludeMatcher,
	includeM *includeMatcher,
	isSrc bool,
	now time.Time,
	onDirs func(int64),
) (*fullSyncSnapshot, bool, error) {
	root := normalizeDirPath(rootPath)
	snap := &fullSyncSnapshot{
		root:          root,
		dirs:          make(map[string]FileListResult),
		excluded:      make(map[string]struct{}),
		protectedDirs: make(map[string]struct{}),
		specExcluded:  make(map[string]struct{}),
		sizeExcluded:  make(map[string]struct{}),
	}
	snap.dirs[""] = make(FileListResult)

	treeOpt := native.TreeOptions{
		FastList:       util.ToInt(job["fastList"]) == 1,
		FollowSymlinks: isSrc && util.ToInt(job["followSymlinks"]) == 1,
		IncludeHash:    util.ToInt(job["compareMode"]) == 2,
		ProxyURL:       globalNativeProxyURL(),
	}
	entries, exists, err := cfg.ListTree(ctx, strings.TrimRight(root, "/"), treeOpt)
	if err != nil {
		return nil, false, err
	}
	if !exists {
		if isSrc {
			return nil, false, fmt.Errorf("源目录 %s 不存在或不可访问", root)
		}
		return snap, false, nil
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].RelPath < entries[j].RelPath
	})

	protectConflicts := !isSrc && util.ToInt(job["conflictPolicy"]) == 2
	excludedDirPrefixes := make([]string, 0)
	isUnderExcludedDir := func(rel string) bool {
		for _, prefix := range excludedDirPrefixes {
			if strings.HasPrefix(rel, prefix) {
				return true
			}
		}
		return false
	}
	markAncestorsProtected := func(relDir string) {
		for ancestor := relDir; ancestor != ""; {
			snap.protectedDirs[ancestor] = struct{}{}
			trimmed := strings.TrimSuffix(ancestor, "/")
			idx := strings.LastIndex(trimmed, "/")
			if idx < 0 {
				break
			}
			ancestor = trimmed[:idx+1]
		}
	}
	ensureDirMap := func(relDir string) FileListResult {
		m, ok := snap.dirs[relDir]
		if !ok {
			m = make(FileListResult)
			snap.dirs[relDir] = m
		}
		return m
	}

	var dirCount int64 = 1
	for _, e := range entries {
		rel := strings.Trim(path.Clean("/"+e.RelPath), "/")
		if rel == "" || rel == "." {
			continue
		}
		if isUnderExcludedDir(rel) {
			continue
		}
		parent := path.Dir(rel)
		relDir := ""
		if parent != "." && parent != "/" {
			relDir = parent + "/"
		}
		key := e.Name
		relPath := rel
		if e.IsDir {
			key = e.Name + "/"
			relPath = rel + "/"
		}
		meta := FileMetadata{
			Size:     e.Size,
			Modified: e.ModTime,
			MD5:      e.MD5,
		}

		// Ensure parent dir entries exist in snap.dirs for object stores (S3/B2)
		// that don't emit explicit directory marker objects.
		if relDir != "" {
			cur := ""
			for _, seg := range strings.Split(strings.TrimSuffix(relDir, "/"), "/") {
				parentMap := ensureDirMap(cur)
				segKey := seg + "/"
				if _, ok := parentMap[segKey]; !ok {
					parentMap[segKey] = FileMetadata{}
					dirCount++
				}
				cur += segKey
				ensureDirMap(cur)
			}
		}

		if spec != nil && spec.matchesListed(relPath, e.IsDir) {
			if e.IsDir {
				excludedDirPrefixes = append(excludedDirPrefixes, relPath)
			}
			if !isSrc {
				snap.excluded[relPath] = struct{}{}
				snap.specExcluded[relPath] = struct{}{}
				markAncestorsProtected(relDir)
			}
			continue
		}
		if !e.IsDir && includeM != nil && !includeM.matchesListed(relPath) {
			if !isSrc {
				snap.excluded[relPath] = struct{}{}
				snap.specExcluded[relPath] = struct{}{}
				markAncestorsProtected(relDir)
			}
			continue
		}
		if !e.IsDir && !jobAllowsFileMeta(job, meta, now) {
			if !isSrc {
				snap.excluded[relPath] = struct{}{}
				snap.sizeExcluded[relPath] = struct{}{}
				markAncestorsProtected(relDir)
			}
			continue
		}
		if protectConflicts && !e.IsDir && isConflictBackupName(e.Name) {
			snap.excluded[relPath] = struct{}{}
			snap.specExcluded[relPath] = struct{}{}
			markAncestorsProtected(relDir)
			continue
		}

		ensureDirMap(relDir)[key] = meta
		if e.IsDir {
			ensureDirMap(relPath)
			dirCount++
		}
	}
	if onDirs != nil {
		onDirs(dirCount)
	}
	return snap, true, nil
}

// buildNativePendingDeleteTargets converts deferred fullSyncDelete items into
// pendingDeleteTarget records with subtree digests for live re-verification.
func buildNativePendingDeleteTargets(plan *fullSyncPlan, items []fullSyncDelete) []pendingDeleteTarget {
	srcRootNonEmpty := len(plan.src.dirs[""]) > 0
	targets := make([]pendingDeleteTarget, 0, len(items))
	for _, item := range items {
		isDir := strings.HasSuffix(item.name, "/")
		target := pendingDeleteTarget{
			SrcRoot:         plan.src.root,
			DstRoot:         plan.dst.root,
			SrcDir:          item.srcDir,
			DstDir:          item.dir,
			Name:            item.name,
			IsDir:           isDir,
			IsBlocker:       item.isBlocker,
			Size:            item.metadata.Size,
			Count:           1,
			Modified:        item.metadata.Modified,
			SrcRootNonEmpty: srcRootNonEmpty,
		}
		if isDir && item.subtree != "" {
			var subEntries []subtreeDigestEntry
			var collect func(relPrefix, snapDir string)
			collect = func(relPrefix, snapDir string) {
				children := plan.dst.dirs[snapDir]
				for _, childName := range sortedFileListKeys(children) {
					childMeta := children[childName]
					childIsDir := strings.HasSuffix(childName, "/")
					subEntries = append(subEntries, subtreeDigestEntry{
						relPath:  relPrefix + childName,
						isDir:    childIsDir,
						size:     childMeta.Size,
						modified: childMeta.Modified,
					})
					if childIsDir {
						collect(relPrefix+childName, snapDir+childName)
					}
				}
			}
			collect("", item.subtree)
			target.Count, target.Size, target.SubtreeDigest = summarizeSubtreeEntries(subEntries)
		}
		targets = append(targets, target)
	}
	return targets
}

// runNativeFullSync executes full mirror sync (method=1) using native rclone
// while reusing OpenSync's fullSyncPlan safety guards, same-side relocations,
// trash soft-delete, conflict backup, and pending deletion approval queue.
func (jt *JobTask) runNativeFullSync(selected map[nativeItemKey]bool) error {
	ctx := jt.context()
	cfg := jt.nativeConfig
	job := jt.Job
	spec := buildJobExcludeMatcher(job)
	includeM := buildJobIncludeMatcher(job)
	now := time.Now()
	repairOnly := jt.hasRetrySource()
	deferDeletes := jt.RequireFullSyncApproval && !repairOnly
	sessOpts := native.SessionOptions{ProxyURL: globalNativeProxyURL()}

	var allDeferredTargets []pendingDeleteTarget
	var errs []error
	var totalScannedDirs int64

	for _, src := range parsePathList(job["srcPath"]) {
		for _, base := range parsePathList(job["dstPath"]) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			dst := jobDstForSrc(job, src, base)
			srcSnap, _, err := buildNativeFullSyncSnapshot(ctx, cfg, src, job, spec, includeM, true, now, func(d int64) {
				totalScannedDirs += d
				jt.ScanTotalDirs.Store(totalScannedDirs)
				jt.ScanDoneDirs.Store(totalScannedDirs)
				jt.notifyProgressChange()
			})
			if err != nil {
				errs = append(errs, fmt.Errorf("扫描源目录 %s 失败: %w", src, err))
				continue
			}
			dstSnap, _, err := buildNativeFullSyncSnapshot(ctx, cfg, dst, job, spec, includeM, false, now, func(d int64) {
				totalScannedDirs += d
				jt.ScanTotalDirs.Store(totalScannedDirs)
				jt.ScanDoneDirs.Store(totalScannedDirs)
				jt.notifyProgressChange()
			})
			if err != nil {
				errs = append(errs, fmt.Errorf("扫描目标目录 %s 失败: %w", dst, err))
				continue
			}

			plan := newFullSyncPlan(srcSnap, dstSnap, nil)
			plan.build(job)

			var relocations []fullSyncRelocation
			var writes fullSyncWriteIndex
			var guardReason string
			var deletesUnsafe bool
			if !repairOnly {
				relocations = plan.relocations()
				writes = plan.writeIndex()
				guardReason, deletesUnsafe = plan.unsafeDeleteVolume(plan.plannedDeleteWeight(relocations, writes))
			}

			blocked := make(map[string]struct{})
			writeBlocked := make(map[string]struct{})
			var deferredItems []fullSyncDelete

			// Phase 1: Type-conflict blockers
			for _, key := range sortedDeleteKeys(plan.blockers, false) {
				item := plan.blockers[key]
				if item.protected || repairOnly {
					reason := fmt.Sprintf("destination type conflict at %q requires a normal full sync", item.object)
					if item.protected {
						reason = fmt.Sprintf("destination type conflict at %q contains or matches an excluded entry", item.object)
					}
					errMsg := msg.MirrorDeleteGuard(reason)
					jt.DelHook(item.dir, item.name, nil, taskStatusFailed, &errMsg, boolToTaskItemObject(item.subtree != ""), time.Now().Unix())
				}
				if item.protected || repairOnly || deletesUnsafe || jt.skipCollidingDelete(writes, item) {
					p := normalizeBlockedPath(item.blocksPath)
					blocked[p] = struct{}{}
					writeBlocked[p] = struct{}{}
					continue
				}
				if deferDeletes {
					blocked[normalizeBlockedPath(item.blocksPath)] = struct{}{}
					deferredItems = append(deferredItems, item)
					continue
				}
				delErr := nativeDeleteOrTrash(ctx, cfg, jt, job, plan.dst.root, item.dir, item.name, item.subtree != "")
				if delErr != nil {
					s := delErr.Error()
					jt.DelHook(item.dir, item.name, nil, taskStatusFailed, &s, boolToTaskItemObject(item.subtree != ""), time.Now().Unix())
					p := normalizeBlockedPath(item.blocksPath)
					blocked[p] = struct{}{}
					writeBlocked[p] = struct{}{}
				} else {
					jt.DelHook(item.dir, item.name, plan.dst.deleteSize(item), taskStatusSuccess, nil, boolToTaskItemObject(item.subtree != ""), time.Now().Unix())
				}
			}

			// Phase 2: Create new directories
			for _, key := range sortedDirKeys(plan.newDirs) {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				item := plan.newDirs[key]
				if fullSyncPathBlocked(item.dstDir, blocked) {
					continue
				}
				if err := cfg.Mkdir(ctx, strings.TrimRight(item.dstDir, "/"), sessOpts); err != nil {
					s := err.Error()
					jt.CopyHook(item.srcDir, item.dstDir, path.Base(strings.TrimRight(item.dstDir, "/")), nil, "", taskStatusFailed, &s, taskItemPath, taskItemTypeCopy, time.Now().Unix())
					p := normalizeBlockedPath(item.dstDir)
					blocked[p] = struct{}{}
					writeBlocked[p] = struct{}{}
				}
			}

			// Phase 3: Same-side relocations
			movedTargets := make(map[string]struct{})
			consumedFileDeletes := make(map[string]struct{})
			protectedDeletes := make(map[string]struct{})
			for _, rel := range relocations {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if fullSyncPathBlocked(rel.target.target, blocked) {
					protectedDeletes[rel.source.deleteRoot] = struct{}{}
					continue
				}
				srcObj := strings.TrimRight(rel.source.dir, "/") + "/" + rel.source.name
				dstObj := strings.TrimRight(rel.target.dstDir, "/") + "/" + rel.target.name
				if err := cfg.MovePath(ctx, srcObj, dstObj, false, sessOpts); err != nil {
					s := err.Error()
					jt.CopyHook(rel.source.dir, rel.target.dstDir, rel.source.name, rel.source.metadata.Size, "", taskStatusFailed, &s, taskItemFile, taskItemTypeMove, time.Now().Unix())
					protectedDeletes[rel.source.deleteRoot] = struct{}{}
					continue
				}
				jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
				jt.CopyHook(rel.source.dir, rel.target.dstDir, rel.source.name, rel.source.metadata.Size, "", taskStatusSuccess, nil, taskItemFile, taskItemTypeMove, time.Now().Unix())
				movedTargets[rel.target.target] = struct{}{}
				if rel.source.deleteRoot == rel.source.object {
					consumedFileDeletes[rel.source.deleteRoot] = struct{}{}
				}
			}

			// Phase 4: High-speed rclone Copy for new + changed files
			var onlyFiles []native.PendingFile
			dstRootNorm := normalizeDirPath(dst)
			addFileIfAllowed := func(file *fullSyncFile) {
				if fullSyncPathBlocked(file.target, blocked) {
					return
				}
				rel := strings.TrimPrefix(strings.TrimRight(file.dstDir, "/")+"/"+file.name, dstRootNorm)
				rel = strings.TrimPrefix(rel, "/")
				if selected != nil {
					k := nativeItemKey{path.Join(src, rel), path.Join(dst, rel)}
					if !selected[k] {
						return
					}
					delete(selected, k)
				}
				onlyFiles = append(onlyFiles, native.PendingFile{Path: rel, Size: file.metadata.Size})
			}
			for _, file := range sortedFullSyncFiles(plan.changed) {
				addFileIfAllowed(file)
			}
			for _, target := range sortedNewFileTargets(plan.newFiles) {
				if _, moved := movedTargets[target]; moved {
					continue
				}
				addFileIfAllowed(plan.newFiles[target])
			}

			writesFailed := false
			if len(onlyFiles) > 0 {
				jt.populateNativeWaiting(src, dst, onlyFiles)
				backupDir := nativeTrashBackupSubdir(jt, job)
				copyOpts := buildNativeCopyOptions(job, false, backupDir)
				copyOpts.OnlyFiles = onlyFiles
				// In full sync we already evaluated change decisions; force overwrite of changed files
				// unless conflictPolicy == 1 (skip) or 2 (keep both).
				var completedBytes int64
				copyErr := cfg.Copy(ctx, src, dst, copyOpts, func(v native.Event) {
					if v.Stats != nil {
						if v.Stats.Bytes > 0 || v.Stats.Transfers > 0 || len(v.Stats.Transferring) > 0 {
							jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
						}
						statsCopy := *v.Stats
						jt.nativeStats.Store(&nativeProgressSnapshot{src: src, dst: dst, stats: &statsCopy})
						jt.notifyProgressChange()
						return
					}
					if v.File == "" {
						return
					}
					jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
					srcDir := path.Join(src, path.Dir(v.File))
					dstDir := path.Join(dst, path.Dir(v.File))
					base := path.Base(v.File)
					jt.Waiting.removeMatching(srcDir, dstDir, base)
					status := taskStatusSuccess
					var itemErr *string
					if v.Failed {
						writesFailed = true
						text := strings.TrimSpace(v.Error)
						if text == "" {
							text = "rclone 未完成此文件的传输"
						}
						itemErr = &text
						status = taskStatusFailed
					} else {
						completedBytes += v.Size
					}
					jt.appendFinish(NewCopyJobTaskItem(jt.TaskID, srcDir, dstDir, base, v.Size, "", status, itemErr, taskItemFile, taskItemTypeCopy, time.Now().Unix()))
				})
				jt.Waiting.reset()
				jt.nativeStats.Store(nil)
				if copyErr != nil {
					writesFailed = true
					errs = append(errs, copyErr)
				}
				_ = completedBytes
			}

			if repairOnly {
				continue
			}

			// Phase 5: Extra deletions (guarded by safety valves & optional approval)
			suspiciousCounts := plan.suspiciousEmptySourceDirs()
			suspicious := make(map[string]struct{}, len(suspiciousCounts))
			for dir := range suspiciousCounts {
				suspicious[dir] = struct{}{}
			}
			if deletesUnsafe {
				jt.fullSyncDeleteGuardSkipped.Store(true)
				errMsg := msg.MirrorDeleteGuard(guardReason)
				jt.CopyHook("", plan.dst.root, "", nil, "", taskStatusFailed, &errMsg, taskItemPath, taskItemTypeDelete, time.Now().Unix())
				continue
			}
			for _, dir := range sortedBlockedKeys(suspicious) {
				jt.fullSyncDeleteGuardSkipped.Store(true)
				errMsg := msg.MirrorDeleteGuard(fmt.Sprintf("source directory %q listed empty while destination holds %d entries", dir, suspiciousCounts[dir]))
				jt.CopyHook("", plan.dst.root, "", nil, "", taskStatusFailed, &errMsg, taskItemPath, taskItemTypeDelete, time.Now().Unix())
			}

			for _, key := range sortedDeleteKeys(plan.extraDeletes, true) {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if _, moved := consumedFileDeletes[key]; moved {
					continue
				}
				if _, prot := protectedDeletes[key]; prot {
					continue
				}
				if writesFailed || len(writeBlocked) > 0 {
					continue
				}
				if fullSyncPathBlocked(key, suspicious) {
					continue
				}
				item := plan.extraDeletes[key]
				if jt.skipCollidingDelete(writes, item) {
					continue
				}
				if deferDeletes {
					deferredItems = append(deferredItems, item)
					continue
				}
				delErr := nativeDeleteOrTrash(ctx, cfg, jt, job, plan.dst.root, item.dir, item.name, item.subtree != "")
				if delErr != nil {
					s := delErr.Error()
					jt.DelHook(item.dir, item.name, nil, taskStatusFailed, &s, boolToTaskItemObject(item.subtree != ""), time.Now().Unix())
				} else {
					jt.DelHook(item.dir, item.name, plan.dst.deleteSize(item), taskStatusSuccess, nil, boolToTaskItemObject(item.subtree != ""), time.Now().Unix())
				}
			}

			if len(deferredItems) > 0 {
				allDeferredTargets = append(allDeferredTargets, buildNativePendingDeleteTargets(plan, deferredItems)...)
			}
		}
	}

	if deferDeletes && ctx.Err() == nil {
		if len(allDeferredTargets) > 0 {
			pendingDeleteApprovals.Stage(jt, allDeferredTargets)
		} else {
			pendingDeleteApprovals.ClearIfPending(jt.approvalJobID())
		}
	}
	if ctx.Err() == nil {
		cleanupNativeExpiredTrash(ctx, cfg, jt.TaskID, job)
	}
	return errors.Join(errs...)
}

// runNativeMove executes move mode (method=2) for native rclone:
// scan src & dst -> copy non-conflicting files -> re-verify dst & src unchanged -> delete src -> optional clean empty src dirs.
func (jt *JobTask) runNativeMove(selected map[nativeItemKey]bool) error {
	ctx := jt.context()
	cfg := jt.nativeConfig
	job := jt.Job
	spec := buildJobExcludeMatcher(job)
	includeM := buildJobIncludeMatcher(job)
	compareMode := util.ToInt(job["compareMode"])
	deleteEmptyDirs := util.ToInt(job["deleteEmptySrcDirs"]) == 1
	now := time.Now()
	sessOpts := native.SessionOptions{ProxyURL: globalNativeProxyURL()}

	dstPaths := parsePathList(job["dstPath"])
	if len(dstPaths) != 1 {
		return errors.New("移动模式只允许一个目标目录")
	}
	baseDst := dstPaths[0]

	var errs []error
	hadAnyFailure := false
	var totalDirs int64

	for _, src := range parsePathList(job["srcPath"]) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		dst := jobDstForSrc(job, src, baseDst)
		srcSnap, _, err := buildNativeFullSyncSnapshot(ctx, cfg, src, job, spec, includeM, true, now, func(d int64) {
			totalDirs += d
			jt.ScanTotalDirs.Store(totalDirs)
			jt.ScanDoneDirs.Store(totalDirs)
			jt.notifyProgressChange()
		})
		if err != nil {
			hadAnyFailure = true
			errs = append(errs, fmt.Errorf("扫描源目录 %s 失败: %w", src, err))
			continue
		}
		dstSnap, _, err := buildNativeFullSyncSnapshot(ctx, cfg, dst, job, spec, includeM, false, now, nil)
		if err != nil {
			hadAnyFailure = true
			errs = append(errs, fmt.Errorf("扫描目标目录 %s 失败: %w", dst, err))
			continue
		}

		type moveCandidate struct {
			rel     string
			srcDir  string
			dstDir  string
			name    string
			srcMeta FileMetadata
		}
		var toCopy []moveCandidate
		var alreadyIdentical []moveCandidate
		var srcSubdirs []string

		for relDir, items := range srcSnap.dirs {
			if relDir != "" {
				srcSubdirs = append(srcSubdirs, strings.TrimRight(srcSnap.root+relDir, "/"))
			}
			for _, name := range sortedFileListKeys(items) {
				if strings.HasSuffix(name, "/") {
					continue
				}
				srcMeta := items[name]
				rel := relDir + name
				srcDir := strings.TrimRight(srcSnap.root+relDir, "/")
				dstDir := strings.TrimRight(dstSnap.root+relDir, "/")
				if selected != nil {
					k := nativeItemKey{path.Join(src, rel), path.Join(dst, rel)}
					if !selected[k] {
						continue
					}
					delete(selected, k)
				}
				cand := moveCandidate{rel: rel, srcDir: srcDir, dstDir: dstDir, name: name, srcMeta: srcMeta}
				if dstItems, ok := dstSnap.dirs[relDir]; ok {
					if dstMeta, exists := dstItems[name]; exists {
						if !fileChangedWithMode(srcMeta, dstMeta, compareMode) {
							alreadyIdentical = append(alreadyIdentical, cand)
							continue
						}
						// Refuse overwriting a different destination file during move; retain source.
						hadAnyFailure = true
						reason := "目标文件已存在且内容不一致，已保留源文件"
						jt.CopyHook(srcDir, dstDir, name, srcMeta.Size, "", taskStatusFailed, &reason, taskItemFile, taskItemTypeTransfer, time.Now().Unix())
						continue
					}
				}
				toCopy = append(toCopy, cand)
			}
		}

		// Delete sources that are already verified identical on destination.
		for _, cand := range alreadyIdentical {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := verifyAndDeleteMovedSource(ctx, cfg, cand.srcDir, cand.dstDir, cand.name, cand.srcMeta, sessOpts); err != nil {
				hadAnyFailure = true
				s := err.Error()
				jt.CopyHook(cand.srcDir, cand.dstDir, cand.name, cand.srcMeta.Size, "", taskStatusFailed, &s, taskItemFile, taskItemTypeTransfer, time.Now().Unix())
			} else {
				jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
				jt.CopyHook(cand.srcDir, cand.dstDir, cand.name, cand.srcMeta.Size, "", taskStatusSuccess, nil, taskItemFile, taskItemTypeTransfer, time.Now().Unix())
			}
		}

		// Copy new files, then verify & delete each copied source.
		if len(toCopy) > 0 {
			candByRel := make(map[string]moveCandidate, len(toCopy))
			pendingFiles := make([]native.PendingFile, 0, len(toCopy))
			for _, cand := range toCopy {
				candByRel[cand.rel] = cand
				pendingFiles = append(pendingFiles, native.PendingFile{Path: cand.rel, Size: cand.srcMeta.Size})
			}
			jt.populateNativeWaiting(src, dst, pendingFiles)
			copyOpts := buildNativeCopyOptions(job, false, "")
			copyOpts.OnlyFiles = pendingFiles
			copyOpts.ConflictPolicy = 0
			copyOpts.SkipExisting = false

			copiedRels := make([]string, 0, len(toCopy))
			copyErr := cfg.Copy(ctx, src, dst, copyOpts, func(v native.Event) {
				if v.Stats != nil {
					if v.Stats.Bytes > 0 || v.Stats.Transfers > 0 || len(v.Stats.Transferring) > 0 {
						jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
					}
					statsCopy := *v.Stats
					jt.nativeStats.Store(&nativeProgressSnapshot{src: src, dst: dst, stats: &statsCopy})
					jt.notifyProgressChange()
					return
				}
				if v.File == "" {
					return
				}
				jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
				rel := path.Clean(v.File)
				cand, ok := candByRel[rel]
				srcDir := path.Join(src, path.Dir(rel))
				dstDir := path.Join(dst, path.Dir(rel))
				base := path.Base(rel)
				if ok {
					srcDir = cand.srcDir
					dstDir = cand.dstDir
					base = cand.name
				}
				jt.Waiting.removeMatching(srcDir, dstDir, base)
				if v.Failed {
					hadAnyFailure = true
					text := strings.TrimSpace(v.Error)
					if text == "" {
						text = "rclone 未完成此文件的传输"
					}
					jt.CopyHook(srcDir, dstDir, base, v.Size, "", taskStatusFailed, &text, taskItemFile, taskItemTypeTransfer, time.Now().Unix())
				} else {
					copiedRels = append(copiedRels, rel)
				}
			})
			jt.Waiting.reset()
			jt.nativeStats.Store(nil)
			if copyErr != nil {
				hadAnyFailure = true
				errs = append(errs, copyErr)
			}

			for _, rel := range copiedRels {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				cand, ok := candByRel[rel]
				if !ok {
					continue
				}
				if err := verifyAndDeleteMovedSource(ctx, cfg, cand.srcDir, cand.dstDir, cand.name, cand.srcMeta, sessOpts); err != nil {
					hadAnyFailure = true
					s := err.Error()
					jt.CopyHook(cand.srcDir, cand.dstDir, cand.name, cand.srcMeta.Size, "", taskStatusFailed, &s, taskItemFile, taskItemTypeTransfer, time.Now().Unix())
				} else {
					jt.CopyHook(cand.srcDir, cand.dstDir, cand.name, cand.srcMeta.Size, "", taskStatusSuccess, nil, taskItemFile, taskItemTypeTransfer, time.Now().Unix())
				}
			}
		}

		// Clean up empty source subdirectories deepest-first only when move succeeded cleanly.
		if deleteEmptyDirs && !hadAnyFailure && ctx.Err() == nil && len(srcSubdirs) > 0 {
			sort.Slice(srcSubdirs, func(i, j int) bool {
				di := strings.Count(srcSubdirs[i], "/")
				dj := strings.Count(srcSubdirs[j], "/")
				if di != dj {
					return di > dj
				}
				return srcSubdirs[i] > srcSubdirs[j]
			})
			srcRootTrim := strings.TrimRight(srcSnap.root, "/")
			for _, dir := range srcSubdirs {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if dir == srcRootTrim || !strings.HasPrefix(dir, srcRootTrim+"/") {
					continue
				}
				// RemoveEmptyDir fails safely if non-empty, never deleting newly arrived files.
				if err := cfg.RemoveEmptyDir(ctx, dir, sessOpts); err == nil {
					jt.DelHook(path.Dir(dir)+"/", path.Base(dir)+"/", nil, taskStatusSuccess, nil, taskItemPath, time.Now().Unix())
				}
			}
		}
	}
	return errors.Join(errs...)
}

func verifyAndDeleteMovedSource(ctx context.Context, cfg native.Config, srcDir, dstDir, name string, expectedSrc FileMetadata, opts native.SessionOptions) error {
	// 1. Re-verify destination exists and matches expected size.
	dstEntries, dstExists, err := cfg.ListDirEntries(ctx, dstDir, opts)
	if err != nil {
		return fmt.Errorf("校验目标文件失败，已保留源文件：%w", err)
	}
	if !dstExists {
		return errors.New("目标目录不存在，已保留源文件")
	}
	var dstEntry *native.DirEntry
	for i := range dstEntries {
		if !dstEntries[i].IsDir && dstEntries[i].Name == name {
			dstEntry = &dstEntries[i]
			break
		}
	}
	if dstEntry == nil {
		return errors.New("目标端未找到已复制文件，已保留源文件")
	}
	if dstEntry.Size != expectedSrc.Size {
		return fmt.Errorf("目标文件大小不一致（源 %d，目标 %d），已保留源文件", expectedSrc.Size, dstEntry.Size)
	}
	if expectedSrc.MD5 != "" && dstEntry.MD5 != "" && !strings.EqualFold(expectedSrc.MD5, dstEntry.MD5) {
		return errors.New("目标文件校验和不匹配，已保留源文件")
	}

	// 2. Re-verify source has not changed since scan.
	srcEntries, srcExists, err := cfg.ListDirEntries(ctx, srcDir, opts)
	if err != nil {
		return fmt.Errorf("复核源文件状态失败，已保留源文件：%w", err)
	}
	if !srcExists {
		return nil
	}
	var srcEntry *native.DirEntry
	for i := range srcEntries {
		if !srcEntries[i].IsDir && srcEntries[i].Name == name {
			srcEntry = &srcEntries[i]
			break
		}
	}
	if srcEntry == nil {
		return nil
	}
	if srcEntry.Size != expectedSrc.Size || (expectedSrc.ModTime() > 0 && srcEntry.ModTime > 0 && srcEntry.ModTime != expectedSrc.ModTime()) {
		return errors.New("源文件在传输期间发生变化，已保留源文件")
	}

	// 3. Delete verified source file.
	return cfg.DeletePath(ctx, strings.TrimRight(srcDir, "/")+"/"+name, false, opts)
}

func (m FileMetadata) ModTime() int64 { return m.Modified }

func buildNativeBisyncOptions(job map[string]interface{}, dry bool) native.BisyncOptions {
	limits := taskRuntimeLimitsForJob(job)
	extra, _, _ := parseNativeExtraFlags(job["extraFlags"])
	return native.BisyncOptions{
		WorkDir:              bisyncWorkDir(util.ToInt64(job["id"])),
		ResyncMode:           extra.BisyncResyncMode,
		MaxDelete:            extra.BisyncMaxDelete,
		Force:                extra.BisyncForce,
		TrackRenames:         extra.TrackRenames,
		DryRun:               dry,
		Transfers:            limits.CopyConcurrency,
		Checkers:             limits.ScanConcurrency,
		Retries:              limits.MaxRetries,
		Compare:              util.ToInt(job["compareMode"]),
		ConflictPolicy:       util.ToInt(job["conflictPolicy"]),
		FilterLines:          buildNativeRcloneFilterLines(job),
		MinSize:              util.ToInt64(job["minFileSize"]),
		MaxSize:              util.ToInt64(job["maxFileSize"]),
		MinAgeMinutes:        util.ToInt(job["minFileAgeMinutes"]),
		OlderThanDays:        util.ToInt(job["olderThanDays"]),
		NewerThanDays:        util.ToInt(job["newerThanDays"]),
		BwLimit:              effectiveJobBwLimit(job),
		TPSLimit:             jobFloat64(job["tpsLimit"]),
		TPSLimitBurst:        util.ToInt(job["tpsLimitBurst"]),
		ModifyWindow:         strings.TrimSpace(util.StringValue(job["modifyWindow"])),
		FastList:             util.ToInt(job["fastList"]) == 1,
		MultiThreadStreams:   util.ToInt(job["multiThreadStreams"]),
		MultiThreadCutoff:    extra.MultiThreadCutoff,
		MultiThreadChunkSize: extra.MultiThreadChunkSize,
		BufferSize:           extra.BufferSize,
		UseMmap:              extra.UseMmap,
		MaxTransfer:          extra.MaxTransfer,
		CutoffMode:           extra.CutoffMode,
		MaxDuration:          extra.MaxDuration,
		OrderBy:              extra.OrderBy,
		MaxBacklog:           extra.MaxBacklog,
		UserAgent:            extra.UserAgent,
		IOTimeout:            extra.IOTimeout,
		ConTimeout:           extra.ConTimeout,
		IgnoreSize:           extra.IgnoreSize,
		CreateEmptySrcDirs:   util.ToInt(job["createEmptySrcDirs"]) == 1,
		FollowSymlinks:       util.ToInt(job["followSymlinks"]) == 1,
		NoUpdateModtime:      extra.NoUpdateModtime,
		Inplace:              extra.Inplace,
		CheckFirst:           extra.CheckFirst,
		S3NoHead:             extra.S3NoHead,
		ProxyURL:             globalNativeProxyURL(),
	}
}

// runNativeBisync executes bidirectional sync (method=3) using `rclone bisync`.
func (jt *JobTask) runNativeBisync() error {
	ctx := jt.context()
	cfg := jt.nativeConfig
	job := jt.Job
	srcPaths := parsePathList(job["srcPath"])
	dstPaths := parsePathList(job["dstPath"])
	if len(srcPaths) != 1 || len(dstPaths) != 1 {
		return errors.New("双向同步仅支持单个源目录与单个目标目录")
	}
	src, dst := srcPaths[0], dstPaths[0]
	opts := buildNativeBisyncOptions(job, false)
	_, err := cfg.Bisync(ctx, src, dst, opts, func(v native.Event) {
		if v.Stats != nil {
			if v.Stats.Bytes > 0 || v.Stats.Transfers > 0 || len(v.Stats.Transferring) > 0 {
				jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
			}
			statsCopy := *v.Stats
			jt.nativeStats.Store(&nativeProgressSnapshot{src: src, dst: dst, stats: &statsCopy})
			jt.notifyProgressChange()
			return
		}
		if v.File == "" {
			return
		}
		jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
		srcDir := path.Join(src, path.Dir(v.File))
		dstDir := path.Join(dst, path.Dir(v.File))
		base := path.Base(v.File)
		status := taskStatusSuccess
		var itemErr *string
		if v.Failed {
			text := strings.TrimSpace(v.Error)
			if text == "" {
				text = "双向同步未完成此项"
			}
			itemErr = &text
			status = taskStatusFailed
		}
		if v.Reason == "delete" {
			jt.DelHook(dstDir, base, v.Size, status, itemErr, taskItemFile, time.Now().Unix())
			return
		}
		itemType := taskItemTypeCopy
		if v.Reason == "move" {
			itemType = taskItemTypeMove
		}
		jt.appendFinish(NewCopyJobTaskItem(jt.TaskID, srcDir, dstDir, base, v.Size, "", status, itemErr, taskItemFile, itemType, time.Now().Unix()))
	})
	jt.nativeStats.Store(nil)
	return err
}

func (jt *JobTask) populateNativeWaiting(src, dst string, files []native.PendingFile) {
	if jt == nil || jt.Waiting == nil || len(files) == 0 {
		return
	}
	limit := min(len(files), defaultCopyQueueCapacity)
	for i := 0; i < limit; i++ {
		pf := files[i]
		rel := path.Clean(pf.Path)
		ci := &CopyItem{
			SrcPath:    path.Join(src, path.Dir(rel)),
			DstPath:    path.Join(dst, path.Dir(rel)),
			FileName:   path.Base(rel),
			FileSize:   pf.Size,
			IsPath:     taskItemFile,
			CopyType:   taskItemTypeCopy,
			Status:     taskStatusWaiting,
			CreateTime: time.Now().Unix(),
		}
		if !jt.Waiting.pushWait(jt.context(), ci) {
			break
		}
	}
}

// runNativeDryRunSession executes read-only dry-run previews for all 4 native sync modes.
func runNativeDryRunSession(ctx context.Context, sess *dryRunSession, cfg native.Config, job map[string]interface{}) {
	method := util.ToInt(job["method"])
	switch method {
	case 0:
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
			if v.File == "" {
				return
			}
			action := "copy"
			if v.IsDir {
				action = "mkdir"
			} else if v.Skipped {
				action = "skip"
			} else if v.Updated {
				action = "update"
			}
			sess.addItem(DryRunItem{
				Action:   action,
				SrcPath:  src,
				DstPath:  dst,
				FileName: v.File,
				FileSize: v.Size,
				IsPath:   v.IsDir,
				Reason:   v.Reason,
			})
		})
		if outcome.err != nil {
			if ctx.Err() != nil {
				sess.finish("cancelled", "")
			} else {
				sess.finish("failed", outcome.err.Error())
			}
			return
		}
		sess.finish("completed", "")

	case 1:
		if err := dryRunNativeFullSync(ctx, sess, cfg, job); err != nil {
			if ctx.Err() != nil {
				sess.finish("cancelled", "")
			} else {
				sess.finish("failed", err.Error())
			}
			return
		}
		sess.finish("completed", "")

	case 2:
		if err := dryRunNativeMove(ctx, sess, cfg, job); err != nil {
			if ctx.Err() != nil {
				sess.finish("cancelled", "")
			} else {
				sess.finish("failed", err.Error())
			}
			return
		}
		sess.finish("completed", "")

	case 3:
		if err := dryRunNativeBisync(ctx, sess, cfg, job); err != nil {
			if ctx.Err() != nil {
				sess.finish("cancelled", "")
			} else {
				sess.finish("failed", err.Error())
			}
			return
		}
		sess.finish("completed", "")

	default:
		sess.finish("failed", "不支持的同步模式")
	}
}

func dryRunNativeFullSync(ctx context.Context, sess *dryRunSession, cfg native.Config, job map[string]interface{}) error {
	spec := buildJobExcludeMatcher(job)
	includeM := buildJobIncludeMatcher(job)
	now := time.Now()
	delReason := "目标端多余项"
	if trashEnabled(job) {
		delReason = "移入目标回收站"
	}

	for _, src := range parsePathList(job["srcPath"]) {
		for _, base := range parsePathList(job["dstPath"]) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			dst := jobDstForSrc(job, src, base)
			srcSnap, _, err := buildNativeFullSyncSnapshot(ctx, cfg, src, job, spec, includeM, true, now, func(d int64) {
				sess.scannedDirs.Add(d)
			})
			if err != nil {
				return fmt.Errorf("扫描源目录 %s 失败: %w", src, err)
			}
			dstSnap, _, err := buildNativeFullSyncSnapshot(ctx, cfg, dst, job, spec, includeM, false, now, func(d int64) {
				sess.scannedDirs.Add(d)
			})
			if err != nil {
				return fmt.Errorf("扫描目标目录 %s 失败: %w", dst, err)
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
			dstRootNorm := normalizeDirPath(dst)
			for _, key := range sortedDirKeys(plan.newDirs) {
				dir := plan.newDirs[key]
				if fullSyncPathBlocked(dir.dstDir, blocked) {
					continue
				}
				sess.addItem(DryRunItem{
					Action:   "mkdir",
					SrcPath:  dir.srcDir,
					DstPath:  dir.dstDir,
					FileName: strings.TrimSuffix(strings.TrimPrefix(dir.dstDir, dstRootNorm), "/"),
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
		}
	}
	return nil
}

func dryRunNativeMove(ctx context.Context, sess *dryRunSession, cfg native.Config, job map[string]interface{}) error {
	spec := buildJobExcludeMatcher(job)
	includeM := buildJobIncludeMatcher(job)
	compareMode := util.ToInt(job["compareMode"])
	now := time.Now()
	dstPaths := parsePathList(job["dstPath"])
	if len(dstPaths) != 1 {
		return errors.New("移动模式只允许一个目标目录")
	}
	baseDst := dstPaths[0]
	for _, src := range parsePathList(job["srcPath"]) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		dst := jobDstForSrc(job, src, baseDst)
		srcSnap, _, err := buildNativeFullSyncSnapshot(ctx, cfg, src, job, spec, includeM, true, now, func(d int64) {
			sess.scannedDirs.Add(d)
		})
		if err != nil {
			return fmt.Errorf("扫描源目录 %s 失败: %w", src, err)
		}
		dstSnap, _, err := buildNativeFullSyncSnapshot(ctx, cfg, dst, job, spec, includeM, false, now, nil)
		if err != nil {
			return fmt.Errorf("扫描目标目录 %s 失败: %w", dst, err)
		}
		for relDir, items := range srcSnap.dirs {
			for _, name := range sortedFileListKeys(items) {
				if strings.HasSuffix(name, "/") {
					continue
				}
				srcMeta := items[name]
				srcDir := strings.TrimRight(srcSnap.root+relDir, "/")
				dstDir := strings.TrimRight(dstSnap.root+relDir, "/")
				if dstItems, ok := dstSnap.dirs[relDir]; ok {
					if dstMeta, exists := dstItems[name]; exists && fileChangedWithMode(srcMeta, dstMeta, compareMode) {
						sess.addItem(DryRunItem{
							Action:   "skip",
							SrcPath:  srcDir,
							DstPath:  dstDir,
							FileName: name,
							FileSize: srcMeta.Size,
							Reason:   "目标文件已存在且内容不一致，移动时将跳过并保留源文件",
						})
						continue
					}
				}
				sess.addItem(DryRunItem{
					Action:   "move",
					SrcPath:  srcDir,
					DstPath:  dstDir,
					FileName: name,
					FileSize: srcMeta.Size,
					Reason:   "复制到目标并校验后删除源文件",
				})
			}
		}
	}
	return nil
}

func dryRunNativeBisync(ctx context.Context, sess *dryRunSession, cfg native.Config, job map[string]interface{}) error {
	srcPaths := parsePathList(job["srcPath"])
	dstPaths := parsePathList(job["dstPath"])
	if len(srcPaths) != 1 || len(dstPaths) != 1 {
		return errors.New("双向同步仅支持单个源目录与单个目标目录")
	}
	src, dst := srcPaths[0], dstPaths[0]
	opts := buildNativeBisyncOptions(job, true)
	_, err := cfg.Bisync(ctx, src, dst, opts, func(v native.Event) {
		if v.Stats != nil && v.Stats.Listed > 0 {
			sess.scannedDirs.Store(v.Stats.Listed)
			return
		}
		if v.File == "" {
			return
		}
		action := "copy"
		switch {
		case v.Reason == "delete":
			action = "delete"
		case v.Reason == "move":
			action = "move"
		case v.Updated:
			action = "update"
		}
		sess.addItem(DryRunItem{
			Action:   action,
			SrcPath:  src,
			DstPath:  dst,
			FileName: v.File,
			FileSize: v.Size,
			Reason:   "双向同步预览",
		})
	})
	return err
}

// nativeDirListToFileList adapts native.ListDirEntries to FileListResult for
// live verification of pending deletion approvals.
func nativeDirListToFileList(ctx context.Context, cfg native.Config, dir string) (FileListResult, bool, error) {
	entries, exists, err := cfg.ListDirEntries(ctx, strings.TrimRight(dir, "/"), native.SessionOptions{ProxyURL: globalNativeProxyURL()})
	if err != nil {
		return nil, false, err
	}
	if !exists {
		return nil, true, nil
	}
	res := make(FileListResult, len(entries))
	for _, e := range entries {
		key := e.Name
		if e.IsDir {
			key += "/"
		}
		res[key] = FileMetadata{
			Size:     e.Size,
			Modified: e.ModTime,
			MD5:      e.MD5,
		}
	}
	return res, false, nil
}
