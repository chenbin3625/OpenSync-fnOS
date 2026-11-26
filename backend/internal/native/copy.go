package native

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// Only pass --no-traverse for small file lists (e.g. targeted retries or small
	// diff sets). On larger trees, letting rclone traverse matching directories
	// uses concurrent directory listings (PROPFIND Depth: 1, S3 ListObjects, SFTP
	// Readdir) and streams transfers immediately, avoiding sequential per-file
	// source PROPFINDs and per-file destination probes.
	noTraverseFileThreshold = 32

	maxPendingEventFiles = 20000
)

type PendingFile struct {
	Path string
	Size int64
}

type CopyOptions struct {
	Transfers, Checkers, Retries, Compare int
	SkipExisting, DryRun                  bool
	Include                               func(string, os.FileInfo) bool

	// Conflict & soft-delete policy
	ConflictPolicy int    // 0: overwrite, 1: skip existing, 2: keep both (--suffix)
	ConflictSuffix string // e.g. ".conflict-20261125-153000"
	BackupDir      string // relative path under dst root for --backup-dir

	// Targeted file list (when non-empty, skips source prescan)
	OnlyFiles []PendingFile

	// ForceOverwrite skips deep check-download and passes --ignore-times directly
	// when the caller already decided which files must be transferred.
	ForceOverwrite bool

	// ReportSkipped emits dry-run skip/mkdir events with reasons (used by service dry-run)
	ReportSkipped bool

	// Performance & transfer tuning
	BwLimit              string
	TPSLimit             float64
	TPSLimitBurst        int
	ModifyWindow         string
	FastList             bool
	MultiThreadStreams   int
	MultiThreadCutoff    string
	MultiThreadChunkSize string
	BufferSize           string
	UseMmap              bool
	MaxTransfer          string
	CutoffMode           string
	MaxDuration          string
	OrderBy              string
	MaxBacklog           int
	UserAgent            string
	IOTimeout            string
	ConTimeout           string
	LowLevelRetries      int
	RetriesSleep         string
	IgnoreSize           bool
	CreateEmptySrcDirs   bool
	FollowSymlinks       bool
	NoUpdateModtime      bool
	Inplace              bool
	CheckFirst           bool
	S3NoHead             bool
	ProxyURL             string

	// Resumable transfer, direct streaming & live runtime controls
	ResumeEnable    bool
	ResumeMinSizeMB int
	ResumeMinBytes  int64
	ResumeStateDir  string
	DirectStream    bool
	FilterLines     []string
	MinSize         int64
	MaxSize         int64
	MinAgeMinutes   int
	OlderThanDays   int
	NewerThanDays   int
	Controller      *RCController
}

type Event struct {
	File         string
	Size         int64
	Updated      bool
	Skipped      bool
	IsDir        bool
	Failed       bool
	Error        string
	Reason       string
	Stats        *Stats
	ScanDirs     int64
	ScanFiles    int64
	ScanDone     bool
	PendingFiles []PendingFile
}

type TransferItem struct {
	Name       string  `json:"name"`
	Size       int64   `json:"size"`
	Bytes      int64   `json:"bytes"`
	Percentage float64 `json:"percentage"`
	Speed      float64 `json:"speed"`
}

type Stats struct {
	Bytes          int64          `json:"bytes"`
	TotalBytes     int64          `json:"totalBytes"`
	TotalTransfers int64          `json:"totalTransfers"`
	Transfers      int64          `json:"transfers"`
	Checks         int64          `json:"checks"`
	TotalChecks    int64          `json:"totalChecks"`
	Listed         int64          `json:"listed"`
	Errors         int64          `json:"errors"`
	Speed          float64        `json:"speed"`
	LastError      string         `json:"lastError"`
	Transferring   []TransferItem `json:"transferring"`
}

func parts(v string) (string, string) {
	n, sub, _ := strings.Cut(strings.Trim(v, "/"), "/")
	return n, sub
}

func (c Config) isLocalName(name string) bool {
	if strings.EqualFold(name, Local) {
		return true
	}
	if canonical, ok := c.FindKey(name); ok {
		return c[canonical]["type"] == "local"
	}
	return false
}

// Pair resolves and validates any supported sync direction:
// local->local, local->remote, remote->local, or remote->remote.
func (c Config) Pair(src, dst string) (Endpoint, Endpoint, error) {
	a, e := c.Resolve(src)
	if e != nil {
		return a, Endpoint{}, e
	}
	b, e := c.Resolve(dst)
	if e != nil {
		return a, b, e
	}
	if b.ReadOnly {
		return a, b, errors.New("只读存储（如 HTTP / Archive）不能作为同步目标")
	}
	if EndpointsConflict(a, b) {
		return a, b, errors.New("同步源与目标实际目录重叠")
	}
	return a, b, nil
}

func within(a, b string) bool {
	rel, e := filepath.Rel(a, b)
	return e == nil && (rel == "." || filepath.IsLocal(rel))
}

func (ep Endpoint) SupportsNativeMD5() bool {
	return endpointSupportsNativeMD5(ep)
}

func (ep Endpoint) HasReliableModTime() bool {
	return endpointHasReliableModTime(ep)
}

func endpointSupportsNativeMD5(ep Endpoint) bool {
	if ep.IsCrypt {
		return false
	}
	switch ep.Backend {
	case "local", "s3", "azureblob", "googlecloudstorage", "swift", "drive", "pcloud":
		return true
	case "webdav":
		return ep.Vendor == "owncloud" || ep.Vendor == "nextcloud"
	case "sftp":
		return ep.AllowHashCheck
	default:
		return false
	}
}

func webdavVendorHasReliableModTime(vendor string) bool {
	return slices.Contains([]string{"owncloud", "nextcloud", "rclone", "fastmail", "sharepoint"}, strings.ToLower(strings.TrimSpace(vendor)))
}

func endpointHasReliableModTime(ep Endpoint) bool {
	if ep.UnreliableModTime {
		return false
	}
	if ep.Backend != "webdav" {
		return true
	}
	return webdavVendorHasReliableModTime(ep.Vendor)
}

// EffectiveCompareMode resolves the runtime comparison mode for a transfer pair.
// Standard WebDAV endpoints (such as OpenList / AList with vendor=other) lack
// reliable modification times and native hashes, so default mode (0) uses
// size-only (1) to avoid downloading remote files locally for comparison.
// Explicit content comparison (2) still performs checksum / read-back checks.
func EffectiveCompareMode(srcEp, destEp Endpoint, compareMode int) int {
	if compareMode == 0 && (!endpointHasReliableModTime(srcEp) || !endpointHasReliableModTime(destEp)) {
		return 1
	}
	return compareMode
}

func joinRcloneRemotePath(baseArg, rel string) string {
	rel = strings.Trim(path.Clean("/"+rel), "/")
	if rel == "" {
		return baseArg
	}
	if strings.HasSuffix(baseArg, ":") {
		return baseArg + rel
	}
	return strings.TrimRight(baseArg, "/") + "/" + rel
}

func appendTuningFlags(args []string, opt CopyOptions) ([]string, error) {
	if bw := strings.TrimSpace(opt.BwLimit); bw != "" {
		if err := ValidateBwLimit(bw); err != nil {
			return nil, err
		}
		args = append(args, "--bwlimit", bw)
	}
	if opt.TPSLimit > 0 {
		args = append(args, "--tpslimit", strconv.FormatFloat(opt.TPSLimit, 'f', -1, 64))
		if opt.TPSLimitBurst > 0 {
			args = append(args, "--tpslimit-burst", strconv.Itoa(opt.TPSLimitBurst))
		}
	}
	if mw := strings.TrimSpace(opt.ModifyWindow); mw != "" {
		if err := ValidateModifyWindow(mw); err != nil {
			return nil, err
		}
		args = append(args, "--modify-window", mw)
	}
	if opt.FastList {
		args = append(args, "--fast-list")
	}
	if opt.MultiThreadStreams > 0 {
		args = append(args, "--multi-thread-streams", strconv.Itoa(opt.MultiThreadStreams))
	}
	if mtc := strings.TrimSpace(opt.MultiThreadCutoff); mtc != "" {
		if err := ValidateRcloneSize(mtc, false, "多线程分片阈值格式无效，如 256M、1G"); err != nil {
			return nil, err
		}
		args = append(args, "--multi-thread-cutoff", mtc)
	}
	if mtcs := strings.TrimSpace(opt.MultiThreadChunkSize); mtcs != "" {
		if err := ValidateRcloneSize(mtcs, false, "多线程分块大小格式无效，如 64M、128M"); err != nil {
			return nil, err
		}
		args = append(args, "--multi-thread-chunk-size", mtcs)
	}
	if bs := strings.TrimSpace(opt.BufferSize); bs != "" {
		if err := ValidateRcloneSize(bs, true, "读取缓冲区大小格式无效，如 16M、64M 或 0"); err != nil {
			return nil, err
		}
		args = append(args, "--buffer-size", bs)
	}
	if opt.UseMmap {
		args = append(args, "--use-mmap")
	}
	if mt := strings.TrimSpace(opt.MaxTransfer); mt != "" {
		if err := ValidateRcloneSize(mt, false, "单次最大传输量格式无效，如 10G、500M"); err != nil {
			return nil, err
		}
		args = append(args, "--max-transfer", mt)
	}
	if cm := strings.TrimSpace(opt.CutoffMode); cm != "" {
		if err := ValidateCutoffMode(cm); err != nil {
			return nil, err
		}
		args = append(args, "--cutoff-mode", strings.ToUpper(cm))
	}
	if md := strings.TrimSpace(opt.MaxDuration); md != "" {
		if err := ValidateDurationSpec(md, 365*24*time.Hour, "最大传输时长格式无效，如 2h、30m"); err != nil {
			return nil, err
		}
		args = append(args, "--max-duration", md)
	}
	if ob := strings.TrimSpace(opt.OrderBy); ob != "" {
		if err := ValidateOrderBy(ob); err != nil {
			return nil, err
		}
		args = append(args, "--order-by", ob)
	}
	if opt.MaxBacklog > 0 {
		args = append(args, "--max-backlog", strconv.Itoa(opt.MaxBacklog))
	}
	if ua := strings.TrimSpace(opt.UserAgent); ua != "" {
		if err := ValidateUserAgent(ua); err != nil {
			return nil, err
		}
		args = append(args, "--user-agent", ua)
	}
	if iot := strings.TrimSpace(opt.IOTimeout); iot != "" {
		if err := ValidateDurationSpec(iot, 24*time.Hour, "I/O 超时格式无效，如 5m、30s"); err != nil {
			return nil, err
		}
		args = append(args, "--timeout", iot)
	}
	if ct := strings.TrimSpace(opt.ConTimeout); ct != "" {
		if err := ValidateDurationSpec(ct, 24*time.Hour, "连接超时格式无效，如 1m、15s"); err != nil {
			return nil, err
		}
		args = append(args, "--contimeout", ct)
	}
	if opt.NoUpdateModtime {
		args = append(args, "--no-update-modtime")
	}
	if opt.Inplace {
		args = append(args, "--inplace")
	}
	if opt.CheckFirst {
		args = append(args, "--check-first")
	}
	if opt.S3NoHead {
		args = append(args, "--s3-no-head")
	}
	return args, nil
}

type syntheticFileInfo struct {
	name    string
	size    int64
	modTime time.Time
	isDir   bool
}

func (f syntheticFileInfo) Name() string { return f.name }
func (f syntheticFileInfo) Size() int64  { return f.size }
func (f syntheticFileInfo) Mode() fs.FileMode {
	if f.isDir {
		return fs.ModeDir | 0755
	}
	return 0644
}
func (f syntheticFileInfo) ModTime() time.Time { return f.modTime }
func (f syntheticFileInfo) IsDir() bool        { return f.isDir }
func (f syntheticFileInfo) Sys() any           { return nil }

func (c Config) Copy(ctx context.Context, src, dst string, opt CopyOptions, emit func(Event)) error {
	if emit == nil {
		emit = func(Event) {}
	}
	srcEp, destEp, e := c.Pair(src, dst)
	if e != nil {
		return e
	}
	skipExisting := opt.SkipExisting || opt.ConflictPolicy == 1
	effectiveCompare := EffectiveCompareMode(srcEp, destEp, opt.Compare)
	// Prefer native checksum comparison (--checksum) when both endpoints expose
	// MD5 hashes, avoiding full downloads. Fall back to `rclone check --download`
	// only when explicit content comparison (Compare == 2) is requested on a
	// hashless backend; default mode (Compare == 0) on standard WebDAV (such as
	// OpenList / AList) uses --size-only instead of pulling files locally.
	useChecksum := !skipExisting && !opt.ForceOverwrite && effectiveCompare == 2 && endpointSupportsNativeMD5(srcEp) && endpointSupportsNativeMD5(destEp)
	deep := !skipExisting && !opt.ForceOverwrite && effectiveCompare == 2 && !useChecksum

	srcName, srcSub := parts(src)
	dstName, dstSub := parts(dst)
	dstMode := diskReadOnly
	if !opt.DryRun {
		dstMode = diskAppendWrite
		if opt.ConflictPolicy == 2 || strings.TrimSpace(opt.BackupDir) != "" {
			// Conflict rename (--suffix) and trash soft-delete (--backup-dir)
			// issue WebDAV MOVE on local destinations.
			dstMode = diskFullWrite
		}
	}

	var specs []SessionSpec
	if !srcEp.IsLocal {
		specs = append(specs, SessionSpec{Name: srcName, Mode: diskReadOnly, Checksum: useChecksum})
	}
	if !destEp.IsLocal {
		specs = append(specs, SessionSpec{Name: dstName, Mode: dstMode, Checksum: useChecksum})
	}
	s, e := c.SessionFor(specs, SessionOptions{ProxyURL: opt.ProxyURL})
	if e != nil {
		return e
	}
	defer s.Close()

	sourceArg := srcEp.Path
	if srcEp.IsLocal {
		sourceArg, e = s.diskWithOptions(srcSub, diskReadOnly, useChecksum, opt.FollowSymlinks)
		if e != nil {
			return e
		}
	}
	destArg := destEp.Path
	if destEp.IsLocal {
		destArg, e = s.diskWithMode(dstSub, dstMode, useChecksum)
		if e != nil {
			return e
		}
	}

	listPath := filepath.Join(s.dir, "files")
	fileSizes := map[string]int64{}
	fileMeta := map[string]resumeFileMeta{}
	var emptyDirs []string
	var count int
	var scannedDirs int64
	var pending []PendingFile

	useDirectStream := opt.DirectStream && len(opt.OnlyFiles) == 0 && !opt.ResumeEnable && !deep && !opt.DryRun && !opt.CreateEmptySrcDirs
	if useDirectStream {
		emit(Event{ScanDirs: 1, ScanFiles: 0, ScanDone: true})
	} else if len(opt.OnlyFiles) > 0 {
		f, err := os.OpenFile(listPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		buf := bufio.NewWriter(f)
		for _, pf := range opt.OnlyFiles {
			rel := strings.TrimPrefix(path.Clean("/"+pf.Path), "/")
			if rel == "" || strings.ContainsAny(rel, "\r\n") {
				_ = f.Close()
				return errors.New("文件清单包含无效路径")
			}
			if _, err := fmt.Fprintln(buf, rel); err != nil {
				_ = f.Close()
				return err
			}
			fileSizes[rel] = pf.Size
			fileMeta[rel] = resumeFileMeta{Rel: rel, Size: pf.Size}
			if len(pending) < maxPendingEventFiles {
				pending = append(pending, PendingFile{Path: rel, Size: pf.Size})
			}
			count++
		}
		if err := errors.Join(buf.Flush(), f.Close()); err != nil {
			return err
		}
		emit(Event{ScanDirs: 1, ScanFiles: int64(count), ScanDone: true, PendingFiles: pending})
	} else if srcEp.IsLocal {
		var walkErr error
		count, scannedDirs, emptyDirs, pending, walkErr = prescanLocalSource(ctx, srcSub, listPath, opt, fileSizes, fileMeta, emit)
		if walkErr != nil {
			return walkErr
		}
		emit(Event{ScanDirs: scannedDirs, ScanFiles: int64(count), ScanDone: true, PendingFiles: pending})
	} else {
		var walkErr error
		count, scannedDirs, emptyDirs, pending, walkErr = prescanRemoteSource(ctx, s, srcEp.Backend, sourceArg, listPath, opt, fileSizes, fileMeta, emit)
		if walkErr != nil {
			return walkErr
		}
		emit(Event{ScanDirs: scannedDirs, ScanFiles: int64(count), ScanDone: true, PendingFiles: pending})
	}

	if !useDirectStream && count == 0 {
		if opt.CreateEmptySrcDirs && len(emptyDirs) > 0 {
			return handleEmptyDirs(ctx, s, destEp, dstSub, destArg, emptyDirs, opt, emit)
		}
		return nil
	}

	// Partition large files into byte/part-level resumable transfers when enabled.
	var resumeErr error
	copyCount := count
	if opt.ResumeEnable && !opt.DryRun && !deep && !useDirectStream && count > 0 {
		threshold := effectiveResumeThreshold(opt)
		canResumeDownload := destEp.IsLocal && !srcEp.IsLocal
		var s3Canonical string
		canResumeS3Upload := false
		if srcEp.IsLocal && !destEp.IsLocal && destEp.Backend == "s3" {
			if canonical, ok := c.FindKey(dstName); ok && canUseS3ResumableUpload(c[canonical]) {
				s3Canonical = canonical
				canResumeS3Upload = true
			}
		}
		if canResumeDownload || canResumeS3Upload {
			var resumeFiles []resumeFileMeta
			var remainPaths []string
			for _, rel := range slices.Sorted(func(yield func(string) bool) {
				for k := range fileMeta {
					if !yield(k) {
						return
					}
				}
			}) {
				fm := fileMeta[rel]
				if fm.Size >= threshold {
					resumeFiles = append(resumeFiles, fm)
				} else {
					remainPaths = append(remainPaths, rel)
				}
			}
			if len(resumeFiles) > 0 {
				resumeOpt := opt
				resumeOpt.Compare = effectiveCompare
				if canResumeDownload {
					resumeErr = resumeDownloadFilesToLocal(ctx, s, sourceArg, dstSub, resumeFiles, resumeOpt, emit)
				} else {
					resumeErr = resumeUploadFilesToS3(ctx, srcSub, s3Canonical, c[s3Canonical], dstSub, resumeFiles, resumeOpt, emit)
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if len(remainPaths) == 0 {
					if resumeErr == nil && opt.CreateEmptySrcDirs && len(emptyDirs) > 0 {
						return handleEmptyDirs(ctx, s, destEp, dstSub, destArg, emptyDirs, opt, emit)
					}
					return resumeErr
				}
				f, rewriteErr := os.OpenFile(listPath, os.O_TRUNC|os.O_WRONLY, 0600)
				if rewriteErr != nil {
					return rewriteErr
				}
				bw := bufio.NewWriter(f)
				for _, rel := range remainPaths {
					if _, wErr := fmt.Fprintln(bw, rel); wErr != nil {
						_ = f.Close()
						return wErr
					}
				}
				if err := errors.Join(bw.Flush(), f.Close()); err != nil {
					return err
				}
				copyCount = len(remainPaths)
			}
		}
	}

	lowLevelRetries := opt.LowLevelRetries
	if lowLevelRetries <= 0 {
		lowLevelRetries = 10
	}
	common := []string{
		"--checkers", strconv.Itoa(max(1, opt.Checkers)),
		"--retries", strconv.Itoa(max(1, opt.Retries+1)),
		"--low-level-retries", strconv.Itoa(lowLevelRetries),
	}
	if rs := strings.TrimSpace(opt.RetriesSleep); rs != "" {
		if err := ValidateDurationSpec(rs, time.Hour, "重试间隔格式无效，如 5s、30s、1m（最大 1h）"); err != nil {
			return err
		}
		common = append(common, "--retries-sleep", rs)
	}
	common, e = appendTuningFlags(common, opt)
	if e != nil {
		return e
	}
	if deep {
		diff := filepath.Join(s.dir, "different")
		missing := filepath.Join(s.dir, "missing")
		bad := filepath.Join(s.dir, "unreadable")
		args := append([]string{"check", "--download", "--one-way", "--use-json-log", "--log-level", "INFO", "--stats", "1s", "--files-from-raw", listPath, "--differ", diff, "--missing-on-dst", missing, "--error", bad}, common...)
		cmd, e := s.Cmd(ctx, append(args, "--", sourceArg, destArg)...)
		if e != nil {
			return e
		}
		checkLogs, e := cmd.StderrPipe()
		if e != nil {
			return e
		}
		if e = cmd.Start(); e != nil {
			return e
		}
		var detachCheck func()
		var stopCheckWake func() bool
		if opt.Controller != nil && cmd.Process != nil {
			detachCheck = opt.Controller.Attach(cmd.Process.Pid, "", "", "")
			stopCheckWake = context.AfterFunc(ctx, func() { opt.Controller.ResumeAllProcesses() })
		}
		checkErrBuf := &tailBuffer{limit: maxStderrCapture}
		checkScan := bufio.NewScanner(checkLogs)
		checkScan.Buffer(make([]byte, 4096), 4<<20)
		for checkScan.Scan() {
			raw := checkScan.Bytes()
			var m struct {
				Msg, Level string
				Stats      *Stats
			}
			if json.Unmarshal(raw, &m) != nil {
				_, _ = checkErrBuf.Write(raw)
				_, _ = checkErrBuf.Write([]byte{'\n'})
				continue
			}
			if m.Stats != nil {
				emit(Event{Stats: m.Stats})
				continue
			}
			if m.Level == "error" || m.Level == "critical" {
				if msg := strings.TrimSpace(m.Msg); msg != "" {
					_, _ = checkErrBuf.Write([]byte(msg + "\n"))
				}
			}
		}
		if checkScan.Err() != nil {
			if cmd.Process != nil {
				_ = resumeProcess(cmd.Process.Pid)
				_ = cmd.Process.Kill()
			}
		}
		runErr := cmd.Wait()
		if stopCheckWake != nil {
			stopCheckWake()
		}
		if detachCheck != nil {
			detachCheck()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if checkScan.Err() != nil {
			return checkScan.Err()
		}
		// Exit 1 is expected when differences are present. Operational errors must
		// never turn a partial comparison into a successful backup.
		if runErr != nil && (cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 1) {
			if detail := checkErrBuf.String(); detail != "" {
				log.Printf("native rclone check failed (%v): %s", runErr, sanitizeRcloneError(detail))
			}
			return fmt.Errorf("读取目标内容失败：%w", runErr)
		}
		if st, e := os.Stat(bad); e != nil || st.Size() != 0 {
			if detail := checkErrBuf.String(); detail != "" {
				log.Printf("native rclone check unreadable (%v): %s", e, sanitizeRcloneError(detail))
			}
			return errors.New("目标文件无法读取，内容比对未完成")
		}
		out, e := os.OpenFile(listPath, os.O_TRUNC|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		outBuf := bufio.NewWriter(out)
		var total int64
		copyCount = 0
		for _, file := range []string{diff, missing} {
			lines, n, err := appendFileListLines(outBuf, file)
			copyCount += lines
			total += n
			if err != nil {
				out.Close()
				return err
			}
		}
		if e = errors.Join(outBuf.Flush(), out.Close()); e != nil {
			return e
		}
		if total == 0 {
			if runErr != nil {
				if detail := checkErrBuf.String(); detail != "" {
					log.Printf("native rclone check error with empty diff: %s", sanitizeRcloneError(detail))
				}
				return errors.New("目标内容比对失败")
			}
			if opt.CreateEmptySrcDirs && len(emptyDirs) > 0 {
				return handleEmptyDirs(ctx, s, destEp, dstSub, destArg, emptyDirs, opt, emit)
			}
			return nil
		}
	}

	args := []string{"copy", "--use-json-log", "--log-level", "INFO", "--stats", "1s", "--transfers", strconv.Itoa(max(1, opt.Transfers))}
	if useDirectStream {
		filterFile := filepath.Join(s.dir, "filters")
		lines := make([]string, 0, len(opt.FilterLines)+2)
		lines = append(lines, "- "+PartialDirName+"/**", "- .opensync-*.partial")
		lines = append(lines, opt.FilterLines...)
		if err := os.WriteFile(filterFile, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
			return err
		}
		args = append(args, "--filter-from", filterFile)
		args = appendFilterCriteriaFlags(args, opt.MinSize, opt.MaxSize, opt.MinAgeMinutes, opt.OlderThanDays, opt.NewerThanDays)
	} else {
		args = append(args, "--files-from-raw", listPath)
		if copyCount <= noTraverseFileThreshold {
			args = append(args, "--no-traverse")
		}
	}
	args = append(args, common...)
	var rcAddr, rcUser, rcPass string
	var rcEnv []string
	if opt.Controller != nil && !opt.DryRun {
		var rcArgs []string
		var ok bool
		if rcAddr, rcUser, rcPass, rcArgs, rcEnv, ok = prepareRCArgsAndEnv(); ok {
			args = append(args, rcArgs...)
		}
	}
	previewFile := filepath.Join(s.dir, "preview")
	if opt.DryRun {
		args = append(args, "--dry-run", "--combined", previewFile)
	}
	if deep || opt.ForceOverwrite {
		args = append(args, "--ignore-times")
	} else if useChecksum {
		args = append(args, "--checksum")
	} else if effectiveCompare == 1 {
		args = append(args, "--size-only")
	}
	if opt.IgnoreSize && effectiveCompare != 1 {
		args = append(args, "--ignore-size")
	}
	if skipExisting {
		args = append(args, "--ignore-existing")
	} else if opt.ConflictPolicy == 2 {
		suffix := strings.TrimSpace(opt.ConflictSuffix)
		if suffix == "" {
			suffix = ".conflict-" + time.Now().Format("20060102-150405")
		}
		args = append(args, "--suffix", suffix, "--suffix-keep-extension")
	} else if bd := strings.TrimSpace(opt.BackupDir); bd != "" {
		args = append(args, "--backup-dir", joinRcloneRemotePath(destArg, bd))
	}

	cmd, e := s.Cmd(ctx, append(args, "--", sourceArg, destArg)...)
	if e != nil {
		return e
	}
	if len(rcEnv) > 0 {
		cmd.Env = append(cmd.Env, rcEnv...)
	}
	logs, e := cmd.StderrPipe()
	if e != nil {
		return e
	}
	if e = cmd.Start(); e != nil {
		return e
	}
	if opt.Controller != nil && cmd.Process != nil {
		detachRC := opt.Controller.Attach(cmd.Process.Pid, rcAddr, rcUser, rcPass)
		defer detachRC()
		stopWake := context.AfterFunc(ctx, func() { opt.Controller.ResumeAllProcesses() })
		defer stopWake()
	}
	scan := bufio.NewScanner(logs)
	scan.Buffer(make([]byte, 4096), 4<<20)
	type failedInfo struct {
		size    int64
		msg     string
		emitted bool
	}
	failed := map[string]*failedInfo{}
	var skippedExisting []string
	var lastErrMsg string
	for scan.Scan() {
		var m struct {
			Msg, Object, Skipped, Level string
			Size                        int64
			Stats                       *Stats
		}
		if json.Unmarshal(scan.Bytes(), &m) != nil {
			continue
		}
		if m.Level == "error" || m.Level == "critical" {
			if msg := strings.TrimSpace(m.Msg); msg != "" {
				lastErrMsg = msg
			}
			if m.Object != "" && cleanRelative(m.Object) {
				obj := path.Clean(m.Object)
				sz := m.Size
				if sz <= 0 {
					sz = fileSizes[obj]
				}
				info := &failedInfo{size: sz, msg: strings.TrimSpace(m.Msg)}
				// When retries are disabled, emit per-file failure immediately
				// so the UI shows failed items in real time without waiting for
				// the whole transfer batch to end.
				if !opt.DryRun && opt.Retries == 0 {
					info.emitted = true
					emit(Event{File: obj, Size: sz, Failed: true, Error: sanitizeRcloneError(info.msg)})
				}
				failed[obj] = info
			}
		}
		if m.Stats != nil {
			if msg := strings.TrimSpace(m.Stats.LastError); msg != "" {
				lastErrMsg = msg
			}
			emit(Event{Stats: m.Stats})
			continue
		}
		if opt.DryRun && opt.ReportSkipped && skipExisting && m.Object != "" && strings.Contains(m.Msg, "Destination exists, skipping") {
			if cleanRelative(m.Object) {
				skippedExisting = append(skippedExisting, path.Clean(m.Object))
			}
		}
		if !opt.DryRun && m.Object != "" && strings.HasPrefix(m.Msg, "Copied (") {
			obj := path.Clean(m.Object)
			delete(failed, obj)
			sz := m.Size
			if sz <= 0 {
				sz = fileSizes[obj]
			}
			emit(Event{File: obj, Size: sz, Updated: strings.Contains(m.Msg, "replaced")})
		}
	}
	if scan.Err() != nil {
		_ = cmd.Process.Kill()
	}
	e = cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if scan.Err() != nil {
		return scan.Err()
	}
	if e != nil {
		if lastErrMsg != "" {
			log.Printf("native rclone copy failed (%v): %s", e, sanitizeRcloneError(lastErrMsg))
		}
		for file, info := range failed {
			if !info.emitted {
				emit(Event{File: file, Size: info.size, Failed: true, Error: sanitizeRcloneError(info.msg)})
			}
		}
		if detail := sanitizeRcloneError(lastErrMsg); detail != "" {
			return errors.Join(resumeErr, fmt.Errorf("rclone 复制失败：%s", detail))
		}
		return errors.Join(resumeErr, fmt.Errorf("rclone 复制失败（%v），请检查连接、空间和写入权限", e))
	}
	if opt.DryRun {
		plan, err := os.Open(previewFile)
		if err != nil {
			return err
		}
		defer plan.Close()
		lines := bufio.NewScanner(plan)
		lines.Buffer(make([]byte, 4096), 4<<20)
		for lines.Scan() {
			op, name, ok := strings.Cut(lines.Text(), " ")
			if !ok || (op != "+" && op != "*") {
				continue
			}
			reason := ""
			if op == "*" {
				if opt.ConflictPolicy == 2 {
					reason = "冲突保留两者（目标旧文件将重命名备份）"
				} else if strings.TrimSpace(opt.BackupDir) != "" {
					reason = "覆盖前将旧文件移入回收站"
				} else {
					reason = "内容/元数据变化"
				}
			}
			emit(Event{File: name, Size: fileSizes[name], Updated: op == "*", Reason: reason})
		}
		if err := lines.Err(); err != nil {
			return err
		}
		if opt.ReportSkipped {
			for _, name := range skippedExisting {
				emit(Event{File: name, Size: fileSizes[name], Skipped: true, Reason: "目标已存在（冲突策略：跳过）"})
			}
		}
		if opt.CreateEmptySrcDirs && len(emptyDirs) > 0 {
			return handleEmptyDirs(ctx, s, destEp, dstSub, destArg, emptyDirs, opt, emit)
		}
		return nil
	}
	if opt.CreateEmptySrcDirs && len(emptyDirs) > 0 {
		if err := handleEmptyDirs(ctx, s, destEp, dstSub, destArg, emptyDirs, opt, emit); err != nil {
			return errors.Join(resumeErr, err)
		}
	}
	return resumeErr
}

func prescanLocalSource(
	ctx context.Context,
	srcSub, listPath string,
	opt CopyOptions,
	fileSizes map[string]int64,
	fileMeta map[string]resumeFileMeta,
	emit func(Event),
) (count int, scannedDirs int64, emptyDirs []string, pending []PendingFile, err error) {
	volume, relative, e := openDisk(srcSub)
	if e != nil {
		return 0, 0, nil, nil, e
	}
	defer volume.Close()
	rootStat, e := volume.Stat(".")
	if e != nil {
		return 0, 0, nil, nil, errors.New("无法读取备份源目录挂载状态")
	}
	rootDev := uint64(rootStat.Sys().(*syscall.Stat_t).Dev)
	source, e := volume.OpenRoot(relative)
	if e != nil {
		return 0, 0, nil, nil, errors.New("无法打开备份源目录")
	}
	defer source.Close()

	f, e := os.OpenFile(listPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if e != nil {
		return 0, 0, nil, nil, e
	}
	buf := bufio.NewWriter(f)

	dirHasChild := map[string]bool{}
	var allDirs []string
	type devIno struct{ dev, ino uint64 }
	visitedDirs := map[devIno]bool{}

	var walkDir func(relDir string) error
	walkDir = func(relDir string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := opt.Controller.WaitIfPaused(ctx); err != nil {
			return err
		}
		dirFile, openErr := source.Open(relDir)
		if openErr != nil {
			return errors.New("无法完整读取备份源目录")
		}
		entries, readErr := dirFile.ReadDir(-1)
		_ = dirFile.Close()
		if readErr != nil {
			return errors.New("无法完整读取备份源目录")
		}
		scannedDirs++
		if relDir != "." {
			allDirs = append(allDirs, relDir)
		}
		emit(Event{ScanDirs: scannedDirs, ScanFiles: int64(count)})

		slices.SortFunc(entries, func(a, b fs.DirEntry) int {
			return strings.Compare(a.Name(), b.Name())
		})
		for _, d := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := opt.Controller.WaitIfPaused(ctx); err != nil {
				return err
			}
			if isInternalPartialEntry(d.Name()) {
				continue
			}
			childRel := d.Name()
			if relDir != "." {
				childRel = relDir + "/" + d.Name()
			}
			var st os.FileInfo
			var infoErr error
			if d.Type()&os.ModeSymlink != 0 {
				if !opt.FollowSymlinks {
					continue
				}
				// source.Stat uses os.Root confinement so escaping symlinks fail safely.
				st, infoErr = source.Stat(childRel)
				if infoErr != nil {
					continue
				}
			} else {
				st, infoErr = d.Info()
				if infoErr != nil {
					return infoErr
				}
			}
			if sys, ok := st.Sys().(*syscall.Stat_t); ok && uint64(sys.Dev) != rootDev {
				continue
			}
			if opt.Include != nil && !opt.Include(childRel, st) {
				continue
			}
			if st.IsDir() {
				if opt.FollowSymlinks {
					if sys, ok := st.Sys().(*syscall.Stat_t); ok {
						key := devIno{dev: uint64(sys.Dev), ino: sys.Ino}
						if visitedDirs[key] {
							continue
						}
						visitedDirs[key] = true
					}
				}
				dirHasChild[relDir] = true
				if err := walkDir(childRel); err != nil {
					return err
				}
				continue
			}
			if !st.Mode().IsRegular() {
				continue
			}
			if strings.ContainsAny(childRel, "\r\n") {
				return errors.New("备份源含有换行符文件名，无法生成 rclone 文件清单")
			}
			dirHasChild[relDir] = true
			if _, wErr := fmt.Fprintln(buf, childRel); wErr != nil {
				return wErr
			}
			fileSizes[childRel] = st.Size()
			fileMeta[childRel] = resumeFileMeta{
				Rel:     childRel,
				Size:    st.Size(),
				ModTime: st.ModTime().Unix(),
			}
			if len(pending) < maxPendingEventFiles {
				pending = append(pending, PendingFile{Path: childRel, Size: st.Size()})
			}
			count++
			if count%500 == 0 {
				emit(Event{ScanDirs: scannedDirs, ScanFiles: int64(count)})
			}
		}
		return nil
	}

	if st, statErr := source.Stat("."); statErr == nil {
		if sys, ok := st.Sys().(*syscall.Stat_t); ok {
			visitedDirs[devIno{dev: uint64(sys.Dev), ino: sys.Ino}] = true
		}
	}
	walkErr := walkDir(".")
	flushErr := buf.Flush()
	closeErr := f.Close()
	if err = errors.Join(walkErr, flushErr, closeErr); err != nil {
		return 0, 0, nil, nil, err
	}
	if opt.CreateEmptySrcDirs {
		for _, d := range allDirs {
			if !dirHasChild[d] {
				emptyDirs = append(emptyDirs, d)
			}
		}
	}
	return count, scannedDirs, emptyDirs, pending, nil
}

func prescanRemoteSource(
	ctx context.Context,
	s *Session,
	backend, sourceArg, listPath string,
	opt CopyOptions,
	fileSizes map[string]int64,
	fileMeta map[string]resumeFileMeta,
	emit func(Event),
) (count int, scannedDirs int64, emptyDirs []string, pending []PendingFile, err error) {
	lsArgs := []string{"lsjson", "-R", "--no-mimetype"}
	if opt.FastList {
		lsArgs = append(lsArgs, "--fast-list")
	}
	if opt.ResumeEnable && opt.Compare == 2 {
		lsArgs = append(lsArgs, "--hash")
	}
	lsArgs = append(lsArgs, "--", sourceArg)
	cmd, e := s.Cmd(ctx, lsArgs...)
	if e != nil {
		return 0, 0, nil, nil, e
	}
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		return 0, 0, nil, nil, e
	}
	errBuf := &tailBuffer{limit: maxStderrCapture}
	cmd.Stderr = errBuf
	if e = cmd.Start(); e != nil {
		return 0, 0, nil, nil, e
	}
	if opt.Controller != nil && cmd.Process != nil {
		detachScan := opt.Controller.Attach(cmd.Process.Pid, "", "", "")
		defer detachScan()
		stopScanWake := context.AfterFunc(ctx, func() { opt.Controller.ResumeAllProcesses() })
		defer stopScanWake()
	}

	f, e := os.OpenFile(listPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if e != nil {
		if cmd.Process != nil {
			_ = resumeProcess(cmd.Process.Pid)
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
		return 0, 0, nil, nil, e
	}
	buf := bufio.NewWriter(f)

	scannedDirs = 1
	dirAllowed := make(map[string]bool)
	checkDirAllowed := func(dirRel string) bool {
		if dirRel == "" || dirRel == "." || opt.Include == nil {
			return true
		}
		if ok, seen := dirAllowed[dirRel]; seen {
			return ok
		}
		segs := strings.Split(dirRel, "/")
		cur := ""
		for _, seg := range segs {
			if cur == "" {
				cur = seg
			} else {
				cur += "/" + seg
			}
			if ok, seen := dirAllowed[cur]; seen {
				if !ok {
					dirAllowed[dirRel] = false
					return false
				}
				continue
			}
			allowed := opt.Include(cur, syntheticFileInfo{name: seg, isDir: true})
			dirAllowed[cur] = allowed
			if !allowed {
				dirAllowed[dirRel] = false
				return false
			}
		}
		return true
	}

	dirHasChild := map[string]bool{}
	var allDirs []string
	var streamErr error
	killed := false

	dec := json.NewDecoder(bufio.NewReaderSize(stdout, 64<<10))
	if tok, tokErr := dec.Token(); tokErr != nil {
		streamErr = tokErr
	} else if delim, ok := tok.(json.Delim); !ok || delim != '[' {
		streamErr = errors.New("无法解析远端源目录文件清单")
	} else {
		for dec.More() {
			if ctx.Err() != nil {
				streamErr = ctx.Err()
				break
			}
			if err := opt.Controller.WaitIfPaused(ctx); err != nil {
				streamErr = err
				break
			}
			var r struct {
				Path    string            `json:"Path"`
				Name    string            `json:"Name"`
				Size    int64             `json:"Size"`
				ModTime string            `json:"ModTime"`
				IsDir   bool              `json:"IsDir"`
				Hashes  map[string]string `json:"Hashes"`
			}
			if decErr := dec.Decode(&r); decErr != nil {
				streamErr = errors.New("无法解析远端源目录文件清单")
				break
			}
			rel := strings.TrimPrefix(path.Clean("/"+r.Path), "/")
			if rel == "" || rel == "." || strings.HasPrefix(rel, "../") {
				continue
			}
			parentDir := path.Dir(rel)
			if !checkDirAllowed(parentDir) {
				if r.IsDir {
					dirAllowed[rel] = false
				}
				continue
			}
			modTime := parseRcloneTime(r.ModTime)
			fi := syntheticFileInfo{
				name:    path.Base(rel),
				size:    r.Size,
				modTime: modTime,
				isDir:   r.IsDir,
			}
			if r.IsDir {
				if allowed, seen := dirAllowed[rel]; seen {
					if !allowed {
						continue
					}
				} else if opt.Include != nil {
					allowed = opt.Include(rel, fi)
					dirAllowed[rel] = allowed
					if !allowed {
						continue
					}
				}
				if opt.CreateEmptySrcDirs {
					if parentDir != "." {
						dirHasChild[parentDir] = true
					}
					allDirs = append(allDirs, rel)
				}
				scannedDirs++
				continue
			}
			if opt.Include != nil && !opt.Include(rel, fi) {
				continue
			}
			if opt.CreateEmptySrcDirs && parentDir != "." {
				dirHasChild[parentDir] = true
			}
			if strings.ContainsAny(rel, "\r\n") {
				streamErr = errors.New("远端源目录含有换行符文件名，无法生成 rclone 文件清单")
				break
			}
			if _, wErr := fmt.Fprintln(buf, rel); wErr != nil {
				streamErr = wErr
				break
			}
			fileSizes[rel] = r.Size
			md5Sum := ""
			if r.Hashes != nil {
				md5Sum = strings.ToLower(strings.TrimSpace(r.Hashes["md5"]))
				if md5Sum == "" {
					md5Sum = strings.ToLower(strings.TrimSpace(r.Hashes["MD5"]))
				}
			}
			fileMeta[rel] = resumeFileMeta{
				Rel:     rel,
				Size:    r.Size,
				ModTime: modTime.Unix(),
				MD5:     md5Sum,
			}
			if len(pending) < maxPendingEventFiles {
				pending = append(pending, PendingFile{Path: rel, Size: r.Size})
			}
			count++
			if count%500 == 0 {
				emit(Event{ScanDirs: scannedDirs, ScanFiles: int64(count)})
			}
		}
		if streamErr == nil {
			if tok, tokErr := dec.Token(); tokErr != nil {
				streamErr = errors.New("无法解析远端源目录文件清单")
			} else if delim, ok := tok.(json.Delim); !ok || delim != ']' {
				streamErr = errors.New("无法解析远端源目录文件清单")
			}
		}
	}

	if streamErr != nil && !errors.Is(streamErr, io.EOF) {
		if cmd.Process != nil {
			_ = resumeProcess(cmd.Process.Pid)
			_ = cmd.Process.Kill()
			killed = true
		}
		_ = stdout.Close()
	}
	waitErr := cmd.Wait()
	stderr := errBuf.String()
	flushCloseErr := errors.Join(buf.Flush(), f.Close())

	if ctx.Err() != nil {
		return 0, 0, nil, nil, ctx.Err()
	}
	if waitErr != nil && !killed {
		return 0, 0, nil, nil, classifyRcloneError(backend, stderr)
	}
	if streamErr != nil {
		if errors.Is(streamErr, io.EOF) && strings.TrimSpace(stderr) != "" {
			return 0, 0, nil, nil, classifyRcloneError(backend, stderr)
		}
		return 0, 0, nil, nil, streamErr
	}
	if flushCloseErr != nil {
		return 0, 0, nil, nil, flushCloseErr
	}
	if opt.CreateEmptySrcDirs {
		for _, d := range allDirs {
			if !dirHasChild[d] {
				emptyDirs = append(emptyDirs, d)
			}
		}
	}
	return count, scannedDirs, emptyDirs, pending, nil
}

func handleEmptyDirs(
	ctx context.Context,
	s *Session,
	destEp Endpoint,
	dstSub, destArg string,
	emptyDirs []string,
	opt CopyOptions,
	emit func(Event),
) error {
	if opt.DryRun {
		if opt.ReportSkipped {
			for _, d := range emptyDirs {
				emit(Event{File: d, IsDir: true, Reason: "同步空目录"})
			}
		}
		return nil
	}
	if destEp.IsLocal {
		root, rel, err := openDisk(dstSub)
		if err != nil {
			return err
		}
		defer root.Close()
		for _, d := range emptyDirs {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := opt.Controller.WaitIfPaused(ctx); err != nil {
				return err
			}
			target := path.Join(rel, d)
			if err := mkdirAllPerm(root, target, backupDirPerm); err != nil {
				return err
			}
		}
		return nil
	}
	for _, d := range emptyDirs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := opt.Controller.WaitIfPaused(ctx); err != nil {
			return err
		}
		cmd, err := s.Cmd(ctx, "mkdir", "--", joinRcloneRemotePath(destArg, d))
		if err != nil {
			return err
		}
		if _, stderr, err := limitedOutputWithStderr(cmd, 64<<10); err != nil {
			return classifyRcloneError(destEp.Backend, stderr)
		}
	}
	return nil
}

func parseRcloneTime(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t
	}
	return time.Time{}
}

// appendFileListLines copies non-empty path entries from srcPath into out,
// returning the line count and total bytes written.
func appendFileListLines(out io.Writer, srcPath string) (int, int64, error) {
	in, err := os.Open(srcPath)
	if err != nil {
		return 0, 0, err
	}
	defer in.Close()
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	var lines int
	var written int64
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		n, err := fmt.Fprintln(out, line)
		written += int64(n)
		if err != nil {
			return lines, written, err
		}
		lines++
	}
	return lines, written, scanner.Err()
}

// sanitizeRcloneError strips internal loopback WebDAV URLs (which contain the
// per-session random prefix) and bounds message length before surfacing errors.
func sanitizeRcloneError(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	for {
		idx := strings.Index(s, "http://127.0.0.1:")
		if idx < 0 {
			break
		}
		end := idx + len("http://127.0.0.1:")
		for end < len(s) && !strings.ContainsRune(" \t\r\n\"'<>", rune(s[end])) {
			end++
		}
		s = s[:idx] + "[local]" + s[end:]
	}
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}
