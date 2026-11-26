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
	"sync"
	"time"

	"opensync/internal/config"
	"opensync/internal/mapper"
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
	LowLevelRetries      int    `json:"lowLevelRetries,omitempty"`
	RetriesSleep         string `json:"retriesSleep,omitempty"`
	ResumeEnable         bool   `json:"resumeEnable,omitempty"`
	ResumeMinSizeMB      int    `json:"resumeMinSizeMB,omitempty"`
	WatchEnable          bool   `json:"watchEnable,omitempty"`
	WatchDebounceSec     int    `json:"watchDebounceSec,omitempty"`
	SyncPollIntervalSec  int    `json:"syncPollIntervalSec,omitempty"`
	SyncAuditIntervalMin int    `json:"syncAuditIntervalMin,omitempty"`
	SyncDirection        string `json:"syncDirection,omitempty"`
	AutoRetryMax         int    `json:"autoRetryMax,omitempty"`
	AutoRetryDelayMin    int    `json:"autoRetryDelayMin,omitempty"`
	PostSyncJobID        int64  `json:"postSyncJobId,omitempty"`
	CheckSchedule        string `json:"checkSchedule,omitempty"`
	MissedCatchUp        bool   `json:"missedCatchUp,omitempty"`
	DirectStream         bool   `json:"directStream,omitempty"`
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
		case "noUpdateModtime", "inplace", "checkFirst", "s3NoHead", "ignoreSize", "trackRenames", "useMmap", "bisyncForce", "resumeEnable", "watchEnable", "missedCatchUp", "directStream":
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
			case "resumeEnable":
				out.ResumeEnable = b
			case "watchEnable":
				out.WatchEnable = b
			case "missedCatchUp":
				out.MissedCatchUp = b
			case "directStream":
				out.DirectStream = b
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
		case "lowLevelRetries":
			if val == nil || val == "" {
				continue
			}
			n, ok := strictJobInt(val)
			if !ok || n < 0 || n > 50 {
				return nativeExtraFlags{}, "", publicError("底层分片重试次数（--low-level-retries）需为 0–50 的整数")
			}
			out.LowLevelRetries = n
		case "retriesSleep":
			s, ok := extraFlagString(val)
			if !ok || native.ValidateDurationSpec(s, time.Hour, "invalid") != nil {
				return nativeExtraFlags{}, "", publicError("重试间隔（--retries-sleep）格式无效，如 5s、30s、1m（最大 1h）")
			}
			out.RetriesSleep = s
		case "resumeMinSizeMB":
			if val == nil || val == "" {
				continue
			}
			n, ok := strictJobInt(val)
			if !ok || n < 0 || n > 10240 {
				return nativeExtraFlags{}, "", publicError("断点续传阈值需为 0–10240 MB 的整数")
			}
			out.ResumeMinSizeMB = n
		case "watchDebounceSec":
			if val == nil || val == "" {
				continue
			}
			n, ok := strictJobInt(val)
			if !ok || n < 0 || n > 3600 {
				return nativeExtraFlags{}, "", publicError("文件变动防抖等待时间需为 0–3600 秒")
			}
			out.WatchDebounceSec = n
		case "syncPollIntervalSec", "syncAuditIntervalMin":
			n, ok := strictJobInt(val)
			if !ok || (k == "syncPollIntervalSec" && n != 0 && (n < 10 || n > 86400)) ||
				(k == "syncAuditIntervalMin" && n != 0 && (n < 1 || n > 10080)) {
				return nativeExtraFlags{}, "", publicError("远端检测间隔需为 10–86400 秒，完整核对间隔需为 1–10080 分钟（0 使用默认值）")
			}
			if k == "syncPollIntervalSec" {
				out.SyncPollIntervalSec = n
			} else {
				out.SyncAuditIntervalMin = n
			}
		case "syncDirection":
			s, ok := extraFlagString(val)
			if !ok || (s != "" && s != "upload" && s != "download" && s != "custom") {
				return nativeExtraFlags{}, "", publicError("同步方向无效")
			}
			out.SyncDirection = s
		case "autoRetryMax":
			if val == nil || val == "" {
				continue
			}
			n, ok := strictJobInt(val)
			if !ok || n < 0 || n > 10 {
				return nativeExtraFlags{}, "", publicError("失败自动重试次数需为 0–10 的整数")
			}
			out.AutoRetryMax = n
		case "autoRetryDelayMin":
			if val == nil || val == "" {
				continue
			}
			n, ok := strictJobInt(val)
			if !ok || n < 0 || n > 1440 {
				return nativeExtraFlags{}, "", publicError("失败自动重试间隔需为 0–1440 分钟")
			}
			out.AutoRetryDelayMin = n
		case "postSyncJobId":
			if val == nil || val == "" {
				continue
			}
			n, ok := strictJobInt(val)
			if !ok || n < 0 {
				return nativeExtraFlags{}, "", publicError("后置串联任务 ID 无效")
			}
			out.PostSyncJobID = int64(n)
		case "checkSchedule":
			s, ok := extraFlagString(val)
			if !ok {
				return nativeExtraFlags{}, "", publicError("定时一致性校验周期格式无效")
			}
			switch strings.ToLower(s) {
			case "", "daily", "weekly", "monthly":
				out.CheckSchedule = strings.ToLower(s)
			default:
				return nativeExtraFlags{}, "", publicError("定时一致性校验周期仅支持 daily、weekly 或 monthly")
			}
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
			if !ok || n < 0 || n > 100 {
				return nativeExtraFlags{}, "", publicError("双向删除保护百分比（--max-delete）需为 0–100 的整数（0 表示使用 rclone 默认 50%）")
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

func globalDefaultBwLimit() string {
	return strings.TrimSpace(config.GetSystemSettings().DefaultBwLimit)
}

func effectiveJobBwLimit(job map[string]interface{}) string {
	if job != nil {
		if bw := strings.TrimSpace(util.StringValue(job["bwLimit"])); bw != "" {
			return bw
		}
	}
	return globalDefaultBwLimit()
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
		LowLevelRetries:      extra.LowLevelRetries,
		RetriesSleep:         extra.RetriesSleep,
		IgnoreSize:           extra.IgnoreSize,
		CreateEmptySrcDirs:   util.ToInt(job["createEmptySrcDirs"]) == 1,
		FollowSymlinks:       util.ToInt(job["followSymlinks"]) == 1,
		NoUpdateModtime:      extra.NoUpdateModtime,
		Inplace:              extra.Inplace,
		CheckFirst:           extra.CheckFirst,
		S3NoHead:             extra.S3NoHead,
		ProxyURL:             globalNativeProxyURL(),
		ResumeEnable:         extra.ResumeEnable,
		ResumeMinSizeMB:      extra.ResumeMinSizeMB,
		ResumeStateDir:       filepath.Join(config.DataDir(), "resume"),
		DirectStream:         extra.DirectStream,
		FilterLines:          buildNativeRcloneFilterLines(job),
		MinSize:              util.ToInt64(job["minFileSize"]),
		MaxSize:              util.ToInt64(job["maxFileSize"]),
		MinAgeMinutes:        util.ToInt(job["minFileAgeMinutes"]),
		OlderThanDays:        util.ToInt(job["olderThanDays"]),
		NewerThanDays:        util.ToInt(job["newerThanDays"]),
		Include: func(p string, info os.FileInfo) bool {
			if exclude != nil && exclude.matchesListed(p, info.IsDir()) {
				return false
			}
			return info.IsDir() || (include.matchesListed(p) && jobAllowsFileMeta(job, FileMetadata{Size: info.Size(), Modified: info.ModTime().Unix()}, now))
		},
	}
}

// rcloneCaseInsensitiveExtGlob converts ASCII letters in an extension glob
// (e.g. "*.jpg") into case-folding character classes ("*.[jJ][pP][gG]") so
// rclone --filter-from matches extensions case-insensitively at every depth
// while keeping file-name and directory rules case-sensitive.
func rcloneCaseInsensitiveExtGlob(pattern string) string {
	var b strings.Builder
	b.Grow(len(pattern) * 4)
	for _, r := range pattern {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteByte('[')
			b.WriteRune(r)
			b.WriteRune(r - 'a' + 'A')
			b.WriteByte(']')
		case r >= 'A' && r <= 'Z':
			b.WriteByte('[')
			b.WriteRune(r - 'A' + 'a')
			b.WriteRune(r)
			b.WriteByte(']')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
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
				// Unanchored directory rule in rclone filter syntax matches both
				// at the root and at any nested depth.
				dir := strings.TrimSuffix(rule.value, "/")
				lines = append(lines, "- "+dir+"/**")
			case excludeFileExtension:
				lines = append(lines, "- "+rcloneCaseInsensitiveExtGlob(rule.value))
			case excludeFileName:
				lines = append(lines, "- "+rule.value)
			}
		}
	}
	if includeM := buildJobIncludeMatcher(job); includeM != nil && len(includeM.rules) > 0 {
		for _, rule := range includeM.rules {
			if rule.kind == excludeFileExtension {
				lines = append(lines, "+ "+rcloneCaseInsensitiveExtGlob(rule.value))
			} else {
				lines = append(lines, "+ "+rule.value)
			}
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
	for _, p := range writablePathsForJob(job) {
		dstRoots[normalizeDirPath(p)] = struct{}{}
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
	forceHash bool,
	now time.Time,
	ctrl *native.RCController,
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

	extraFlags, _, _ := parseNativeExtraFlags(job["extraFlags"])
	treeOpt := native.TreeOptions{
		FastList:       util.ToInt(job["fastList"]) == 1,
		FollowSymlinks: isSrc && util.ToInt(job["followSymlinks"]) == 1,
		IncludeHash:    forceHash || util.ToInt(job["compareMode"]) == 2 || extraFlags.TrackRenames,
		ProxyURL:       globalNativeProxyURL(),
		Controller:     ctrl,
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

	protectConflicts := !isSrc && util.ToInt(job["method"]) != 2 && util.ToInt(job["conflictPolicy"]) == 2
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
		// Move must see every existing destination file before deciding whether
		// it is safe to copy and delete the source. Size/age filters select
		// source files; they must not hide conflicting destination contents.
		if !e.IsDir && (isSrc || util.ToInt(job["method"]) != 2) && !jobAllowsFileMeta(job, meta, now) {
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

func nativePairEffectiveCompareMode(cfg native.Config, src, dst string, compareMode int) int {
	if compareMode != 0 {
		return compareMode
	}
	srcEp, dstEp, err := cfg.Pair(src, dst)
	if err != nil {
		return compareMode
	}
	return native.EffectiveCompareMode(srcEp, dstEp, compareMode)
}

func nativePairNeedsDeepCompare(cfg native.Config, src, dst string, compareMode int) bool {
	effective := nativePairEffectiveCompareMode(cfg, src, dst, compareMode)
	if effective != 2 {
		return false
	}
	srcEp, dstEp, err := cfg.Pair(src, dst)
	if err != nil {
		return false
	}
	return !srcEp.SupportsNativeMD5() || !dstEp.SupportsNativeMD5()
}

// moveExistingFileIsIdentical decides conservatively whether an already-existing
// destination file can be treated as identical to the source so move mode may
// safely remove the source file without re-copying.
func moveExistingFileIsIdentical(srcMeta, dstMeta FileMetadata, compareMode int, srcEp, dstEp native.Endpoint) (bool, string) {
	if srcMeta.Size != dstMeta.Size {
		return false, "目标文件已存在且大小不一致，已保留源文件"
	}
	if compareMode == 1 {
		return true, ""
	}
	if isMD5Digest(srcMeta.MD5) && isMD5Digest(dstMeta.MD5) {
		if strings.EqualFold(srcMeta.MD5, dstMeta.MD5) {
			return true, ""
		}
		return false, "目标文件已存在且内容不一致，已保留源文件"
	}
	if compareMode == 2 {
		return false, "目标文件已存在但当前存储不支持双端 MD5 校验，无法确认内容一致，已保留源文件"
	}
	if !srcEp.HasReliableModTime() || !dstEp.HasReliableModTime() {
		return false, "目标文件已存在但当前存储不支持可靠修改时间校验，无法确认内容一致，已保留源文件"
	}
	if srcMeta.Modified <= 0 || dstMeta.Modified <= 0 {
		return false, "目标文件已存在且缺少修改时间，无法确认内容一致，已保留源文件"
	}
	diff := srcMeta.Modified - dstMeta.Modified
	if diff < 0 {
		diff = -diff
	}
	if diff <= fileModifiedTolerance {
		return true, ""
	}
	return false, "目标文件已存在且修改时间不一致，已保留源文件"
}

func filterCompletedTransferring(items []native.TransferItem, completed map[string]struct{}) []native.TransferItem {
	if len(items) == 0 || len(completed) == 0 {
		return items
	}
	filtered := make([]native.TransferItem, 0, len(items))
	for _, item := range items {
		if _, done := completed[path.Clean(item.Name)]; done {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

// runNativeFullSync executes full mirror sync (method=1) using native rclone
// while reusing OpenSync's fullSyncPlan safety guards, same-side relocations,
// trash soft-delete, conflict backup, and pending deletion approval queue.
func (jt *JobTask) runNativeFullSync(selected *nativeRetryFilter) error {
	defer jt.clearNativePlanned()

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

	type plannedFullSyncPair struct {
		src               string
		dst               string
		effectiveCompare  int
		deepCheckSameSize bool
		plan              *fullSyncPlan
		relocations       []fullSyncRelocation
		writes            fullSyncWriteIndex
		guardReason       string
		deletesUnsafe     bool
		estCopyFiles      int64
		estCopyBytes      int64
	}

	// Phase 0: Scan & plan all pairs upfront so total files and bytes across
	// every pair are known before transferring begins, avoiding multi-pair
	// progress jumps and rclone's default 10,000 backlog cap.
	var plannedPairs []plannedFullSyncPair
	var totalPlannedFiles, totalPlannedBytes int64
	for _, src := range parsePathList(job["srcPath"]) {
		for _, base := range parsePathList(job["dstPath"]) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := jt.controller().WaitIfPaused(ctx); err != nil {
				return err
			}
			dst := jobDstForSrc(job, src, base)
			effectiveCompare := nativePairEffectiveCompareMode(cfg, src, dst, util.ToInt(job["compareMode"]))
			wantHash := effectiveCompare == 2
			srcSnap, _, err := buildNativeFullSyncSnapshot(ctx, cfg, src, job, spec, includeM, true, wantHash, now, jt.controller(), func(d int64) {
				totalScannedDirs += d
				jt.ScanTotalDirs.Store(totalScannedDirs)
				jt.ScanDoneDirs.Store(totalScannedDirs)
				jt.notifyProgressChange()
			})
			if err != nil {
				errs = append(errs, fmt.Errorf("扫描源目录 %s 失败: %w", src, err))
				continue
			}
			dstSnap, _, err := buildNativeFullSyncSnapshot(ctx, cfg, dst, job, spec, includeM, false, wantHash, now, jt.controller(), func(d int64) {
				totalScannedDirs += d
				jt.ScanTotalDirs.Store(totalScannedDirs)
				jt.ScanDoneDirs.Store(totalScannedDirs)
				jt.notifyProgressChange()
			})
			if err != nil {
				errs = append(errs, fmt.Errorf("扫描目标目录 %s 失败: %w", dst, err))
				continue
			}

			deepCheckSameSize := nativePairNeedsDeepCompare(cfg, src, dst, effectiveCompare)
			plan := newFullSyncPlan(srcSnap, dstSnap, nil)
			plan.compareMode = effectiveCompare
			plan.hasCompareMode = true
			plan.deepCheckSameSize = deepCheckSameSize
			if len(jt.SyncChangedFiles) > 0 {
				plan.forceSourcePaths = make(map[string]bool, len(jt.SyncChangedFiles))
				for _, p := range jt.SyncChangedFiles {
					plan.forceSourcePaths[path.Clean(p)] = true
				}
			}
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

			var estFiles, estBytes int64
			if selected == nil {
				estBlocked := make(map[string]struct{})
				for _, key := range sortedDeleteKeys(plan.blockers, false) {
					item := plan.blockers[key]
					if item.protected || repairOnly || deletesUnsafe || jt.skipCollidingDelete(writes, item) || deferDeletes {
						estBlocked[normalizeBlockedPath(item.blocksPath)] = struct{}{}
					}
				}
				estMoved := make(map[string]struct{}, len(relocations))
				for _, rel := range relocations {
					if !fullSyncPathBlocked(rel.target.target, estBlocked) {
						estMoved[rel.target.target] = struct{}{}
					}
				}
				for _, file := range plan.changed {
					if fullSyncPathBlocked(file.target, estBlocked) {
						continue
					}
					estFiles++
					estBytes += max(0, file.metadata.Size)
				}
				for target, file := range plan.newFiles {
					if _, moved := estMoved[target]; moved {
						continue
					}
					if fullSyncPathBlocked(file.target, estBlocked) {
						continue
					}
					estFiles++
					estBytes += max(0, file.metadata.Size)
				}
			}
			totalPlannedFiles += estFiles
			totalPlannedBytes += estBytes
			plannedPairs = append(plannedPairs, plannedFullSyncPair{
				src:               src,
				dst:               dst,
				effectiveCompare:  effectiveCompare,
				deepCheckSameSize: deepCheckSameSize,
				plan:              plan,
				relocations:       relocations,
				writes:            writes,
				guardReason:       guardReason,
				deletesUnsafe:     deletesUnsafe,
				estCopyFiles:      estFiles,
				estCopyBytes:      estBytes,
			})
		}
	}

	jt.initNativePlanned(totalPlannedFiles, totalPlannedBytes)
	jt.ScanFinish.Store(true)
	jt.notifyProgressChange()

	for _, pair := range plannedPairs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := jt.controller().WaitIfPaused(ctx); err != nil {
			return err
		}
		src := pair.src
		dst := pair.dst
		effectiveCompare := pair.effectiveCompare
		deepCheckSameSize := pair.deepCheckSameSize
		plan := pair.plan
		relocations := pair.relocations
		writes := pair.writes
		guardReason := pair.guardReason
		deletesUnsafe := pair.deletesUnsafe

		blocked := make(map[string]struct{})
		writeBlocked := make(map[string]struct{})
		var deferredItems []fullSyncDelete

		// Phase 1: Type-conflict blockers
		for _, key := range sortedDeleteKeys(plan.blockers, false) {
			if err := jt.controller().WaitIfPaused(ctx); err != nil {
				return err
			}
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
			if err := jt.controller().WaitIfPaused(ctx); err != nil {
				return err
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
			if err := jt.controller().WaitIfPaused(ctx); err != nil {
				return err
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
			movedName := rel.target.name
			if rel.source.name != rel.target.name {
				movedName = rel.source.name + " → " + rel.target.name
			}
			jt.CopyHook(rel.source.dir, rel.target.dstDir, movedName, rel.source.metadata.Size, "", taskStatusSuccess, nil, taskItemFile, taskItemTypeMove, time.Now().Unix())
			movedTargets[rel.target.target] = struct{}{}
			if rel.source.deleteRoot == rel.source.object {
				consumedFileDeletes[rel.source.deleteRoot] = struct{}{}
			}
		}

		// Phase 4: High-speed rclone Copy for new + changed files
		var onlyFiles []native.PendingFile
		dstRootNorm := normalizeDirPath(dst)
		if selected != nil {
			selected.markPairMatch(src, dst)
		}
		addFileIfAllowed := func(file *fullSyncFile) {
			if fullSyncPathBlocked(file.target, blocked) {
				return
			}
			rel := strings.TrimPrefix(strings.TrimRight(file.dstDir, "/")+"/"+file.name, dstRootNorm)
			rel = strings.TrimPrefix(rel, "/")
			if selected != nil && !selected.matchesAndConsume(path.Join(src, rel), path.Join(dst, rel)) {
				return
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

		actualPairFiles := int64(len(onlyFiles))
		var actualPairBytes int64
		plannedSizeByRel := make(map[string]int64, len(onlyFiles))
		for _, pf := range onlyFiles {
			sz := max(0, pf.Size)
			actualPairBytes += sz
			plannedSizeByRel[path.Clean(pf.Path)] = sz
		}
		jt.adjustNativePlanned(actualPairFiles-pair.estCopyFiles, actualPairBytes-pair.estCopyBytes)

		writesFailed := false
		if len(onlyFiles) > 0 {
			jt.populateNativeWaiting(src, dst, onlyFiles)
			backupDir := nativeTrashBackupSubdir(jt, job)
			copyOpts := buildNativeCopyOptions(job, false, backupDir)
			copyOpts.Compare = effectiveCompare
			copyOpts.OnlyFiles = onlyFiles
			copyOpts.Controller = jt.controller()
			// In full sync we already evaluated change decisions; force overwrite of changed files
			// unless conflictPolicy == 1 (skip) or the pair requires deep content check on
			// same-size files (explicit compareMode=2 on a hashless backend).
			if util.ToInt(job["conflictPolicy"]) != 1 && !deepCheckSameSize {
				copyOpts.ForceOverwrite = true
			}
			var (
				pairCopyMu       sync.Mutex
				pairCompleted    = make(map[string]struct{}, len(onlyFiles))
				pairEmittedFiles int64
				pairEmittedBytes int64
			)
			copyErr := cfg.Copy(ctx, src, dst, copyOpts, func(v native.Event) {
				if v.Stats != nil {
					if v.Stats.Bytes > 0 || v.Stats.Transfers > 0 || len(v.Stats.Transferring) > 0 {
						jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
					}
					statsCopy := *v.Stats
					pairCopyMu.Lock()
					statsCopy.Transferring = filterCompletedTransferring(v.Stats.Transferring, pairCompleted)
					pairCopyMu.Unlock()
					jt.nativeStats.Store(&nativeProgressSnapshot{src: src, dst: dst, stats: &statsCopy})
					jt.notifyProgressChange()
					return
				}
				if v.File == "" {
					return
				}
				jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
				rel := path.Clean(v.File)
				srcDir := path.Join(src, path.Dir(rel))
				dstDir := path.Join(dst, path.Dir(rel))
				base := path.Base(rel)
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
				}
				pairCopyMu.Lock()
				if _, seen := pairCompleted[rel]; !seen {
					pairCompleted[rel] = struct{}{}
					plannedSz, ok := plannedSizeByRel[rel]
					if !ok {
						plannedSz = max(0, v.Size)
					}
					pairEmittedFiles++
					pairEmittedBytes += plannedSz
					jt.recordNativePlannedFileDone(plannedSz, !v.Failed)
				}
				if prev := jt.nativeStats.Load(); prev != nil && prev.stats != nil && len(prev.stats.Transferring) > 0 {
					filtered := filterCompletedTransferring(prev.stats.Transferring, pairCompleted)
					if len(filtered) != len(prev.stats.Transferring) {
						statsCopy := *prev.stats
						statsCopy.Transferring = filtered
						jt.nativeStats.Store(&nativeProgressSnapshot{src: prev.src, dst: prev.dst, stats: &statsCopy})
					}
				}
				pairCopyMu.Unlock()
				jt.appendFinish(NewCopyJobTaskItem(jt.TaskID, srcDir, dstDir, base, v.Size, "", status, itemErr, taskItemFile, taskItemTypeCopy, time.Now().Unix()))
			})
			if copyErr != nil {
				jt.recordInterruptedPendingItems([][2]string{{src, dst}})
				writesFailed = true
				errs = append(errs, copyErr)
			}
			pairCopyMu.Lock()
			unemittedFiles := actualPairFiles - pairEmittedFiles
			unemittedBytes := actualPairBytes - pairEmittedBytes
			pairCopyMu.Unlock()
			jt.reconcileNativePlannedPair(unemittedFiles, unemittedBytes)
			jt.Waiting.reset()
			jt.nativeStats.Store(nil)
			jt.notifyProgressChange()
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

		useBatchHardDelete := !deferDeletes && !trashEnabled(job)
		var batchFileItems []fullSyncDelete
		var batchFileRels []string
		var dirOrTrashItems []fullSyncDelete

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
			isDir := item.subtree != ""
			if useBatchHardDelete && !isDir && isSafeDeleteTargetName(item.name) {
				rel := strings.TrimPrefix(strings.TrimRight(item.dir, "/")+"/"+item.name, dstRootNorm)
				rel = strings.TrimPrefix(rel, "/")
				if rel != "" && rel != "." {
					batchFileItems = append(batchFileItems, item)
					batchFileRels = append(batchFileRels, rel)
					continue
				}
			}
			dirOrTrashItems = append(dirOrTrashItems, item)
		}

		if len(batchFileItems) > 0 {
			if err := jt.controller().WaitIfPaused(ctx); err != nil {
				return err
			}
			delErrs, batchErr := cfg.DeleteFilesBatch(ctx, plan.dst.root, batchFileRels, sessOpts)
			for i, item := range batchFileItems {
				rel := batchFileRels[i]
				delErr := batchErr
				if delErr == nil && delErrs != nil {
					delErr = delErrs[rel]
				}
				if delErr != nil {
					s := delErr.Error()
					jt.DelHook(item.dir, item.name, nil, taskStatusFailed, &s, taskItemFile, time.Now().Unix())
				} else {
					jt.DelHook(item.dir, item.name, plan.dst.deleteSize(item), taskStatusSuccess, nil, taskItemFile, time.Now().Unix())
				}
			}
		}

		for _, item := range dirOrTrashItems {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := jt.controller().WaitIfPaused(ctx); err != nil {
				return err
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

type nativeMoveCandidate struct {
	rel     string
	srcDir  string
	dstDir  string
	name    string
	srcMeta FileMetadata
}

// runNativeMove executes move mode (method=2) for native rclone:
// scan src & dst -> copy non-conflicting files -> re-verify dst & src unchanged (cached per dir) -> batch delete src -> optional clean empty src dirs.
func (jt *JobTask) runNativeMove(selected *nativeRetryFilter) error {
	defer jt.clearNativePlanned()

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

	type plannedMoveConflict struct {
		cand   nativeMoveCandidate
		reason string
	}
	type plannedMovePair struct {
		src              string
		dst              string
		srcRoot          string
		verifyWithHash   bool
		toCopy           []nativeMoveCandidate
		alreadyIdentical []nativeMoveCandidate
		conflicts        []plannedMoveConflict
		srcSubdirs       []string
	}

	var plannedPairs []plannedMovePair
	var totalPlannedFiles, totalPlannedBytes int64
	for _, src := range parsePathList(job["srcPath"]) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		dst := jobDstForSrc(job, src, baseDst)
		srcEp, dstEp, pairErr := cfg.Pair(src, dst)
		if pairErr != nil {
			hadAnyFailure = true
			errs = append(errs, pairErr)
			continue
		}
		if selected != nil {
			selected.markPairMatch(src, dst)
		}
		if err := jt.controller().WaitIfPaused(ctx); err != nil {
			return err
		}
		effectiveCompare := native.EffectiveCompareMode(srcEp, dstEp, util.ToInt(job["compareMode"]))
		wantHash := effectiveCompare == 2
		srcSnap, _, err := buildNativeFullSyncSnapshot(ctx, cfg, src, job, spec, includeM, true, wantHash, now, jt.controller(), func(d int64) {
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
		dstSnap, _, err := buildNativeFullSyncSnapshot(ctx, cfg, dst, job, spec, includeM, false, wantHash, now, jt.controller(), nil)
		if err != nil {
			hadAnyFailure = true
			errs = append(errs, fmt.Errorf("扫描目标目录 %s 失败: %w", dst, err))
			continue
		}

		var toCopy []nativeMoveCandidate
		var alreadyIdentical []nativeMoveCandidate
		var conflicts []plannedMoveConflict
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
				if selected != nil && !selected.matchesAndConsume(path.Join(src, rel), path.Join(dst, rel)) {
					continue
				}
				cand := nativeMoveCandidate{rel: rel, srcDir: srcDir, dstDir: dstDir, name: name, srcMeta: srcMeta}
				if dstItems, ok := dstSnap.dirs[relDir]; ok {
					if dstMeta, exists := dstItems[name]; exists {
						if identical, conflictReason := moveExistingFileIsIdentical(srcMeta, dstMeta, compareMode, srcEp, dstEp); identical {
							alreadyIdentical = append(alreadyIdentical, cand)
							continue
						} else {
							// Refuse overwriting or deleting a conflicting/unverified destination file during move; retain source.
							conflicts = append(conflicts, plannedMoveConflict{cand: cand, reason: conflictReason})
							continue
						}
					}
				}
				toCopy = append(toCopy, cand)
				totalPlannedFiles++
				totalPlannedBytes += max(0, srcMeta.Size)
			}
		}

		plannedPairs = append(plannedPairs, plannedMovePair{
			src:              src,
			dst:              dst,
			srcRoot:          srcSnap.root,
			verifyWithHash:   compareMode == 2 && srcEp.SupportsNativeMD5() && dstEp.SupportsNativeMD5(),
			toCopy:           toCopy,
			alreadyIdentical: alreadyIdentical,
			conflicts:        conflicts,
			srcSubdirs:       srcSubdirs,
		})
	}

	jt.initNativePlanned(totalPlannedFiles, totalPlannedBytes)
	jt.ScanFinish.Store(true)
	jt.notifyProgressChange()

	for _, pair := range plannedPairs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := jt.controller().WaitIfPaused(ctx); err != nil {
			return err
		}
		src := pair.src
		dst := pair.dst
		verifyWithHash := pair.verifyWithHash
		for _, conflict := range pair.conflicts {
			hadAnyFailure = true
			reason := conflict.reason
			jt.CopyHook(conflict.cand.srcDir, conflict.cand.dstDir, conflict.cand.name, conflict.cand.srcMeta.Size, "", taskStatusFailed, &reason, taskItemFile, taskItemTypeTransfer, time.Now().Unix())
		}

		// Delete sources that are already verified identical on destination (cached dir listings + single batch delete).
		if len(pair.alreadyIdentical) > 0 {
			failed, verifyErr := verifyAndBatchDeleteMovedSources(ctx, cfg, jt, pair.srcRoot, pair.alreadyIdentical, verifyWithHash, sessOpts, nil)
			if verifyErr != nil {
				return verifyErr
			}
			if failed {
				hadAnyFailure = true
			}
		}

		// Copy new files, then verify & batch delete copied sources.
		if len(pair.toCopy) > 0 {
			candByRel := make(map[string]nativeMoveCandidate, len(pair.toCopy))
			pendingFiles := make([]native.PendingFile, 0, len(pair.toCopy))
			var pairPlannedBytes int64
			for _, cand := range pair.toCopy {
				candByRel[cand.rel] = cand
				sz := max(0, cand.srcMeta.Size)
				pairPlannedBytes += sz
				pendingFiles = append(pendingFiles, native.PendingFile{Path: cand.rel, Size: cand.srcMeta.Size})
			}
			pairPlannedFiles := int64(len(pair.toCopy))
			jt.populateNativeWaiting(src, dst, pendingFiles)
			copyOpts := buildNativeCopyOptions(job, false, "")
			copyOpts.OnlyFiles = pendingFiles
			copyOpts.Controller = jt.controller()
			copyOpts.ConflictPolicy = 0
			copyOpts.SkipExisting = false
			copyOpts.ForceOverwrite = true

			var (
				pairCopyMu       sync.Mutex
				pairCompleted    = make(map[string]struct{}, len(pair.toCopy))
				pairEmittedFiles int64
				pairEmittedBytes int64
			)
			copiedRels := make([]string, 0, len(pair.toCopy))
			copyErr := cfg.Copy(ctx, src, dst, copyOpts, func(v native.Event) {
				if v.Stats != nil {
					if v.Stats.Bytes > 0 || v.Stats.Transfers > 0 || len(v.Stats.Transferring) > 0 {
						jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
					}
					statsCopy := *v.Stats
					pairCopyMu.Lock()
					statsCopy.Transferring = filterCompletedTransferring(v.Stats.Transferring, pairCompleted)
					pairCopyMu.Unlock()
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
				plannedSz := max(0, v.Size)
				if ok {
					srcDir = cand.srcDir
					dstDir = cand.dstDir
					base = cand.name
					plannedSz = max(0, cand.srcMeta.Size)
				}
				jt.Waiting.removeMatching(srcDir, dstDir, base)
				pairCopyMu.Lock()
				if _, seen := pairCompleted[rel]; !seen {
					pairCompleted[rel] = struct{}{}
					pairEmittedFiles++
					pairEmittedBytes += plannedSz
					jt.recordNativePlannedFileCopied(plannedSz, !v.Failed)
				}
				if prev := jt.nativeStats.Load(); prev != nil && prev.stats != nil && len(prev.stats.Transferring) > 0 {
					filtered := filterCompletedTransferring(prev.stats.Transferring, pairCompleted)
					if len(filtered) != len(prev.stats.Transferring) {
						statsCopy := *prev.stats
						statsCopy.Transferring = filtered
						jt.nativeStats.Store(&nativeProgressSnapshot{src: prev.src, dst: prev.dst, stats: &statsCopy})
					}
				}
				pairCopyMu.Unlock()
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
			if copyErr != nil {
				jt.recordInterruptedPendingItems([][2]string{{src, dst}})
				hadAnyFailure = true
				errs = append(errs, copyErr)
			}
			pairCopyMu.Lock()
			unemittedFiles := pairPlannedFiles - pairEmittedFiles
			unemittedBytes := pairPlannedBytes - pairEmittedBytes
			pairCopyMu.Unlock()
			jt.reconcileNativePlannedPair(unemittedFiles, unemittedBytes)
			jt.Waiting.reset()
			jt.nativeStats.Store(nil)
			jt.notifyProgressChange()

			copiedCands := make([]nativeMoveCandidate, 0, len(copiedRels))
			for _, rel := range copiedRels {
				if cand, ok := candByRel[rel]; ok {
					copiedCands = append(copiedCands, cand)
				}
			}
			if len(copiedCands) > 0 {
				failed, verifyErr := verifyAndBatchDeleteMovedSources(ctx, cfg, jt, pair.srcRoot, copiedCands, verifyWithHash, sessOpts, jt.recordNativePlannedFileVerified)
				if verifyErr != nil {
					return verifyErr
				}
				if failed {
					hadAnyFailure = true
				}
			}
		}

		// Clean up empty source subdirectories deepest-first only when move succeeded cleanly.
		if deleteEmptyDirs && !hadAnyFailure && ctx.Err() == nil && len(pair.srcSubdirs) > 0 {
			srcSubdirs := pair.srcSubdirs
			sort.Slice(srcSubdirs, func(i, j int) bool {
				di := strings.Count(srcSubdirs[i], "/")
				dj := strings.Count(srcSubdirs[j], "/")
				if di != dj {
					return di > dj
				}
				return srcSubdirs[i] > srcSubdirs[j]
			})
			srcRootTrim := strings.TrimRight(pair.srcRoot, "/")
			for _, dir := range srcSubdirs {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err := jt.controller().WaitIfPaused(ctx); err != nil {
					return err
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

// verifyAndBatchDeleteMovedSources caches directory listings per directory to
// verify all copied/identical move candidates without N+1 subprocesses, then
// removes verified source files in a single DeleteFilesBatch call.
func verifyAndBatchDeleteMovedSources(
	ctx context.Context,
	cfg native.Config,
	jt *JobTask,
	srcRoot string,
	candidates []nativeMoveCandidate,
	verifyHash bool,
	opts native.SessionOptions,
	onProcessed func(),
) (hadFailure bool, fatalErr error) {
	markProcessed := func() {
		if onProcessed != nil {
			onProcessed()
		}
	}
	type cachedDirList struct {
		byName map[string]native.DirEntry
		exists bool
		err    error
	}
	dstCache := make(map[string]cachedDirList)
	srcCache := make(map[string]cachedDirList)
	loadDir := func(cache map[string]cachedDirList, dir string, includeHash bool) cachedDirList {
		if c, ok := cache[dir]; ok {
			return c
		}
		entries, exists, err := cfg.ListDirEntriesWithHash(ctx, dir, includeHash, opts)
		res := cachedDirList{exists: exists, err: err}
		if err == nil && exists {
			res.byName = make(map[string]native.DirEntry, len(entries))
			for _, e := range entries {
				if !e.IsDir {
					res.byName[e.Name] = e
				}
			}
		}
		cache[dir] = res
		return res
	}

	var toDelete []nativeMoveCandidate
	var toDeleteRels []string

	for _, cand := range candidates {
		if ctx.Err() != nil {
			return hadFailure, ctx.Err()
		}
		if err := jt.controller().WaitIfPaused(ctx); err != nil {
			return hadFailure, err
		}
		dstList := loadDir(dstCache, cand.dstDir, verifyHash)
		if dstList.err != nil {
			hadFailure = true
			s := fmt.Sprintf("校验目标文件失败，已保留源文件：%v", dstList.err)
			markProcessed()
			jt.CopyHook(cand.srcDir, cand.dstDir, cand.name, cand.srcMeta.Size, "", taskStatusFailed, &s, taskItemFile, taskItemTypeTransfer, time.Now().Unix())
			continue
		}
		if !dstList.exists {
			hadFailure = true
			s := "目标目录不存在，已保留源文件"
			markProcessed()
			jt.CopyHook(cand.srcDir, cand.dstDir, cand.name, cand.srcMeta.Size, "", taskStatusFailed, &s, taskItemFile, taskItemTypeTransfer, time.Now().Unix())
			continue
		}
		dstEntry, dstFound := dstList.byName[cand.name]
		if !dstFound {
			hadFailure = true
			s := "目标端未找到已复制文件，已保留源文件"
			markProcessed()
			jt.CopyHook(cand.srcDir, cand.dstDir, cand.name, cand.srcMeta.Size, "", taskStatusFailed, &s, taskItemFile, taskItemTypeTransfer, time.Now().Unix())
			continue
		}
		if dstEntry.Size != cand.srcMeta.Size {
			hadFailure = true
			s := fmt.Sprintf("目标文件大小不一致（源 %d，目标 %d），已保留源文件", cand.srcMeta.Size, dstEntry.Size)
			markProcessed()
			jt.CopyHook(cand.srcDir, cand.dstDir, cand.name, cand.srcMeta.Size, "", taskStatusFailed, &s, taskItemFile, taskItemTypeTransfer, time.Now().Unix())
			continue
		}
		if verifyHash {
			if !isMD5Digest(cand.srcMeta.MD5) || !isMD5Digest(dstEntry.MD5) {
				hadFailure = true
				s := "目标文件缺少 MD5 校验和，无法完成内容核验，已保留源文件"
				markProcessed()
				jt.CopyHook(cand.srcDir, cand.dstDir, cand.name, cand.srcMeta.Size, "", taskStatusFailed, &s, taskItemFile, taskItemTypeTransfer, time.Now().Unix())
				continue
			}
			if !strings.EqualFold(cand.srcMeta.MD5, dstEntry.MD5) {
				hadFailure = true
				s := "目标文件校验和不匹配，已保留源文件"
				markProcessed()
				jt.CopyHook(cand.srcDir, cand.dstDir, cand.name, cand.srcMeta.Size, "", taskStatusFailed, &s, taskItemFile, taskItemTypeTransfer, time.Now().Unix())
				continue
			}
		} else if cand.srcMeta.MD5 != "" && dstEntry.MD5 != "" && !strings.EqualFold(cand.srcMeta.MD5, dstEntry.MD5) {
			hadFailure = true
			s := "目标文件校验和不匹配，已保留源文件"
			markProcessed()
			jt.CopyHook(cand.srcDir, cand.dstDir, cand.name, cand.srcMeta.Size, "", taskStatusFailed, &s, taskItemFile, taskItemTypeTransfer, time.Now().Unix())
			continue
		}

		srcList := loadDir(srcCache, cand.srcDir, false)
		if srcList.err != nil {
			hadFailure = true
			s := fmt.Sprintf("复核源文件状态失败，已保留源文件：%v", srcList.err)
			markProcessed()
			jt.CopyHook(cand.srcDir, cand.dstDir, cand.name, cand.srcMeta.Size, "", taskStatusFailed, &s, taskItemFile, taskItemTypeTransfer, time.Now().Unix())
			continue
		}
		if !srcList.exists {
			jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
			markProcessed()
			jt.CopyHook(cand.srcDir, cand.dstDir, cand.name, cand.srcMeta.Size, "", taskStatusSuccess, nil, taskItemFile, taskItemTypeTransfer, time.Now().Unix())
			continue
		}
		srcEntry, srcFound := srcList.byName[cand.name]
		if !srcFound {
			jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
			markProcessed()
			jt.CopyHook(cand.srcDir, cand.dstDir, cand.name, cand.srcMeta.Size, "", taskStatusSuccess, nil, taskItemFile, taskItemTypeTransfer, time.Now().Unix())
			continue
		}
		if srcEntry.Size != cand.srcMeta.Size || (cand.srcMeta.ModTime() > 0 && srcEntry.ModTime > 0 && srcEntry.ModTime != cand.srcMeta.ModTime()) {
			hadFailure = true
			s := "源文件在传输期间发生变化，已保留源文件"
			markProcessed()
			jt.CopyHook(cand.srcDir, cand.dstDir, cand.name, cand.srcMeta.Size, "", taskStatusFailed, &s, taskItemFile, taskItemTypeTransfer, time.Now().Unix())
			continue
		}

		toDelete = append(toDelete, cand)
		toDeleteRels = append(toDeleteRels, cand.rel)
	}

	if len(toDelete) == 0 {
		return hadFailure, nil
	}

	delErrs, batchErr := cfg.DeleteFilesBatch(ctx, srcRoot, toDeleteRels, opts)
	if ctx.Err() != nil {
		return hadFailure, ctx.Err()
	}
	for i, cand := range toDelete {
		rel := toDeleteRels[i]
		delErr := batchErr
		if delErr == nil && delErrs != nil {
			delErr = delErrs[rel]
		}
		markProcessed()
		if delErr != nil {
			hadFailure = true
			s := delErr.Error()
			jt.CopyHook(cand.srcDir, cand.dstDir, cand.name, cand.srcMeta.Size, "", taskStatusFailed, &s, taskItemFile, taskItemTypeTransfer, time.Now().Unix())
		} else {
			jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
			jt.CopyHook(cand.srcDir, cand.dstDir, cand.name, cand.srcMeta.Size, "", taskStatusSuccess, nil, taskItemFile, taskItemTypeTransfer, time.Now().Unix())
		}
	}
	return hadFailure, nil
}

func (m FileMetadata) ModTime() int64 { return m.Modified }

func buildNativeBisyncOptions(job map[string]interface{}, jt *JobTask, dry bool) native.BisyncOptions {
	limits := taskRuntimeLimitsForJob(job)
	extra, _, _ := parseNativeExtraFlags(job["extraFlags"])
	backupDir := ""
	if !dry {
		backupDir = nativeTrashBackupSubdir(jt, job)
	}
	return native.BisyncOptions{
		WorkDir:              bisyncWorkDir(util.ToInt64(job["id"])),
		ResyncMode:           extra.BisyncResyncMode,
		MaxDelete:            extra.BisyncMaxDelete,
		Force:                extra.BisyncForce,
		TrackRenames:         extra.TrackRenames,
		DryRun:               dry,
		BackupDir:            backupDir,
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
		LowLevelRetries:      extra.LowLevelRetries,
		RetriesSleep:         extra.RetriesSleep,
		IgnoreSize:           extra.IgnoreSize,
		CreateEmptySrcDirs:   util.ToInt(job["createEmptySrcDirs"]) == 1,
		FollowSymlinks:       util.ToInt(job["followSymlinks"]) == 1,
		NoUpdateModtime:      extra.NoUpdateModtime,
		Inplace:              extra.Inplace,
		CheckFirst:           extra.CheckFirst,
		S3NoHead:             extra.S3NoHead,
		ProxyURL:             globalNativeProxyURL(),
		Controller:           jt.controller(),
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
	opts := buildNativeBisyncOptions(job, jt, false)
	if err := mapper.EnsureSyncState(util.ToInt64(job["id"]), syncFingerprint(job, cfg)); err != nil {
		return err
	}
	state, err := mapper.GetSyncState(util.ToInt64(job["id"]))
	if err != nil {
		return err
	}
	opts.RequireExistingState = state.BisyncInitialized
	if state.BisyncNeedsRebuild && !native.BisyncResyncRequested(opts.WorkDir) {
		return errors.New("存储或任务配置已改变，请明确重建双向同步基线")
	}
	_, err = cfg.Bisync(ctx, src, dst, opts, func(v native.Event) {
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
	if err == nil && ctx.Err() == nil {
		cleanupNativeExpiredTrash(ctx, cfg, jt.TaskID, job)
	}
	if err == nil {
		err = mapper.MarkBisyncInitialized(util.ToInt64(job["id"]))
	}
	return err
}

func (jt *JobTask) populateNativeWaiting(src, dst string, files []native.PendingFile) {
	if jt == nil || jt.Waiting == nil || len(files) == 0 {
		return
	}
	if len(files) > defaultCopyQueueCapacity {
		jt.nativePendingOverflow.Store(true)
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
		if !jt.Waiting.tryPush(ci) {
			// Preserve a directory-level retry on interruption for files that
			// could not fit in the bounded progress queue.
			jt.nativePendingOverflow.Store(true)
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
			effectiveCompare := nativePairEffectiveCompareMode(cfg, src, dst, util.ToInt(job["compareMode"]))
			wantHash := effectiveCompare == 2
			srcSnap, _, err := buildNativeFullSyncSnapshot(ctx, cfg, src, job, spec, includeM, true, wantHash, now, nil, func(d int64) {
				sess.scannedDirs.Add(d)
			})
			if err != nil {
				return fmt.Errorf("扫描源目录 %s 失败: %w", src, err)
			}
			dstSnap, _, err := buildNativeFullSyncSnapshot(ctx, cfg, dst, job, spec, includeM, false, wantHash, now, nil, func(d int64) {
				sess.scannedDirs.Add(d)
			})
			if err != nil {
				return fmt.Errorf("扫描目标目录 %s 失败: %w", dst, err)
			}

			deepCheckSameSize := nativePairNeedsDeepCompare(cfg, src, dst, effectiveCompare)
			plan := newFullSyncPlan(srcSnap, dstSnap, nil)
			plan.compareMode = effectiveCompare
			plan.hasCompareMode = true
			plan.deepCheckSameSize = deepCheckSameSize
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
				moveName := rel.source.name
				moveReason := "目标端同内容移动复用"
				if rel.source.name != rel.target.name {
					moveName = rel.source.name + " → " + rel.target.name
					moveReason = "目标端同内容重命名复用"
				}
				sess.addItem(DryRunItem{
					Action:   "move",
					SrcPath:  rel.source.dir,
					DstPath:  rel.target.dstDir,
					FileName: moveName,
					FileSize: rel.source.metadata.Size,
					Reason:   moveReason,
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
				if deepCheckSameSize {
					reason = "目标端不支持元数据/MD5 校验，执行时将内容比对确认是否更新"
				} else if util.ToInt(job["conflictPolicy"]) == 2 {
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
		srcEp, dstEp, pairErr := cfg.Pair(src, dst)
		if pairErr != nil {
			return pairErr
		}
		effectiveCompare := native.EffectiveCompareMode(srcEp, dstEp, compareMode)
		wantHash := effectiveCompare == 2
		srcSnap, _, err := buildNativeFullSyncSnapshot(ctx, cfg, src, job, spec, includeM, true, wantHash, now, nil, func(d int64) {
			sess.scannedDirs.Add(d)
		})
		if err != nil {
			return fmt.Errorf("扫描源目录 %s 失败: %w", src, err)
		}
		dstSnap, _, err := buildNativeFullSyncSnapshot(ctx, cfg, dst, job, spec, includeM, false, wantHash, now, nil, nil)
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
					if dstMeta, exists := dstItems[name]; exists {
						if identical, conflictReason := moveExistingFileIsIdentical(srcMeta, dstMeta, compareMode, srcEp, dstEp); !identical {
							sess.addItem(DryRunItem{
								Action:   "skip",
								SrcPath:  srcDir,
								DstPath:  dstDir,
								FileName: name,
								FileSize: srcMeta.Size,
								Reason:   conflictReason,
							})
							continue
						}
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
	opts := buildNativeBisyncOptions(job, nil, true)
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
