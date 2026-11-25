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

	// ReportSkipped emits dry-run skip/mkdir events with reasons (used by service dry-run)
	ReportSkipped bool

	// Performance & transfer tuning
	BwLimit            string
	TPSLimit           float64
	TPSLimitBurst      int
	ModifyWindow       string
	FastList           bool
	MultiThreadStreams int
	CreateEmptySrcDirs bool
	FollowSymlinks     bool
	NoUpdateModtime    bool
	Inplace            bool
	CheckFirst         bool
	S3NoHead           bool
	ProxyURL           string
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
	return name == Local || (c[name] != nil && c[name]["type"] == "local")
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
		return a, b, errors.New("HTTP 存储为只读源，不能作为同步目标")
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

func endpointHasReliableModTime(ep Endpoint) bool {
	if ep.Backend != "webdav" {
		return true
	}
	return slices.Contains([]string{"owncloud", "nextcloud", "rclone", "fastmail", "sharepoint"}, ep.Vendor)
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
	// Prefer native checksum comparison (--checksum) when both endpoints expose
	// MD5 hashes, avoiding full downloads. Fall back to `rclone check --download`
	// when content comparison is requested on a hashless backend or default mode
	// on standard WebDAV without reliable modtimes.
	useChecksum := !skipExisting && opt.Compare == 2 && endpointSupportsNativeMD5(srcEp) && endpointSupportsNativeMD5(destEp)
	deep := !skipExisting && ((opt.Compare == 2 && !useChecksum) || (opt.Compare == 0 && (!endpointHasReliableModTime(srcEp) || !endpointHasReliableModTime(destEp))))

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
	var emptyDirs []string
	var count int
	var scannedDirs int64
	var pending []PendingFile

	if len(opt.OnlyFiles) > 0 {
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
		count, scannedDirs, emptyDirs, pending, walkErr = prescanLocalSource(ctx, srcSub, listPath, opt, fileSizes, emit)
		if walkErr != nil {
			return walkErr
		}
		emit(Event{ScanDirs: scannedDirs, ScanFiles: int64(count), ScanDone: true, PendingFiles: pending})
	} else {
		var walkErr error
		count, scannedDirs, emptyDirs, pending, walkErr = prescanRemoteSource(ctx, s, srcEp.Backend, sourceArg, listPath, opt, fileSizes, emit)
		if walkErr != nil {
			return walkErr
		}
		emit(Event{ScanDirs: scannedDirs, ScanFiles: int64(count), ScanDone: true, PendingFiles: pending})
	}

	if count == 0 {
		if opt.CreateEmptySrcDirs && len(emptyDirs) > 0 {
			return handleEmptyDirs(ctx, s, destEp, dstSub, destArg, emptyDirs, opt, emit)
		}
		return nil
	}

	common := []string{"--checkers", strconv.Itoa(max(1, opt.Checkers)), "--retries", strconv.Itoa(max(1, opt.Retries+1)), "--low-level-retries", "3"}
	common, e = appendTuningFlags(common, opt)
	if e != nil {
		return e
	}

	copyCount := count
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
			_ = cmd.Process.Kill()
		}
		runErr := cmd.Wait()
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

	args := []string{"copy", "--use-json-log", "--log-level", "INFO", "--stats", "1s", "--transfers", strconv.Itoa(max(1, opt.Transfers)), "--files-from-raw", listPath}
	if copyCount <= noTraverseFileThreshold {
		args = append(args, "--no-traverse")
	}
	args = append(args, common...)
	previewFile := filepath.Join(s.dir, "preview")
	if opt.DryRun {
		args = append(args, "--dry-run", "--combined", previewFile)
	}
	if deep {
		args = append(args, "--ignore-times")
	} else if useChecksum {
		args = append(args, "--checksum")
	} else if opt.Compare == 1 {
		args = append(args, "--size-only")
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
	logs, e := cmd.StderrPipe()
	if e != nil {
		return e
	}
	if e = cmd.Start(); e != nil {
		return e
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
			return fmt.Errorf("rclone 复制失败：%s", detail)
		}
		return fmt.Errorf("rclone 复制失败（%v），请检查连接、空间和写入权限", e)
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
			return err
		}
	}
	return nil
}

func prescanLocalSource(
	ctx context.Context,
	srcSub, listPath string,
	opt CopyOptions,
	fileSizes map[string]int64,
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
	emit func(Event),
) (count int, scannedDirs int64, emptyDirs []string, pending []PendingFile, err error) {
	lsArgs := []string{"lsjson", "-R", "--no-mimetype"}
	if opt.FastList {
		lsArgs = append(lsArgs, "--fast-list")
	}
	lsArgs = append(lsArgs, "--", sourceArg)
	cmd, e := s.Cmd(ctx, lsArgs...)
	if e != nil {
		return 0, 0, nil, nil, e
	}
	out, stderr, e := limitedOutputWithStderr(cmd, 64<<20)
	if e != nil {
		return 0, 0, nil, nil, classifyRcloneError(backend, stderr)
	}
	var rows []struct {
		Path    string `json:"Path"`
		Name    string `json:"Name"`
		Size    int64  `json:"Size"`
		ModTime string `json:"ModTime"`
		IsDir   bool   `json:"IsDir"`
	}
	if json.Unmarshal(out, &rows) != nil {
		return 0, 0, nil, nil, errors.New("无法解析远端源目录文件清单")
	}
	slices.SortFunc(rows, func(a, b struct {
		Path    string `json:"Path"`
		Name    string `json:"Name"`
		Size    int64  `json:"Size"`
		ModTime string `json:"ModTime"`
		IsDir   bool   `json:"IsDir"`
	}) int {
		return strings.Compare(a.Path, b.Path)
	})

	f, e := os.OpenFile(listPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if e != nil {
		return 0, 0, nil, nil, e
	}
	buf := bufio.NewWriter(f)

	scannedDirs = 1
	excludedDirs := make([]string, 0)
	isUnderExcluded := func(p string) bool {
		for _, ex := range excludedDirs {
			if p == ex || strings.HasPrefix(p, ex+"/") {
				return true
			}
		}
		return false
	}
	dirHasChild := map[string]bool{}
	var allDirs []string

	for _, r := range rows {
		if ctx.Err() != nil {
			_ = f.Close()
			return 0, 0, nil, nil, ctx.Err()
		}
		rel := strings.TrimPrefix(path.Clean("/"+r.Path), "/")
		if rel == "" || rel == "." || strings.HasPrefix(rel, "../") {
			continue
		}
		if isUnderExcluded(rel) {
			continue
		}
		modTime := parseRcloneTime(r.ModTime)
		fi := syntheticFileInfo{
			name:    path.Base(rel),
			size:    r.Size,
			modTime: modTime,
			isDir:   r.IsDir,
		}
		if opt.Include != nil && !opt.Include(rel, fi) {
			if r.IsDir {
				excludedDirs = append(excludedDirs, rel)
			}
			continue
		}
		parentDir := path.Dir(rel)
		if parentDir != "." {
			dirHasChild[parentDir] = true
		}
		if r.IsDir {
			scannedDirs++
			allDirs = append(allDirs, rel)
			continue
		}
		if strings.ContainsAny(rel, "\r\n") {
			_ = f.Close()
			return 0, 0, nil, nil, errors.New("远端源目录含有换行符文件名，无法生成 rclone 文件清单")
		}
		if _, wErr := fmt.Fprintln(buf, rel); wErr != nil {
			_ = f.Close()
			return 0, 0, nil, nil, wErr
		}
		fileSizes[rel] = r.Size
		if len(pending) < maxPendingEventFiles {
			pending = append(pending, PendingFile{Path: rel, Size: r.Size})
		}
		count++
		if count%500 == 0 {
			emit(Event{ScanDirs: scannedDirs, ScanFiles: int64(count)})
		}
	}
	if err := errors.Join(buf.Flush(), f.Close()); err != nil {
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
