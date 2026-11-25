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
)

const (
	// Only pass --no-traverse for small file lists (e.g. targeted retries or small
	// diff sets). On larger trees, letting rclone traverse matching directories
	// uses concurrent directory listings (PROPFIND Depth: 1, S3 ListObjects, SFTP
	// Readdir) and streams transfers immediately, avoiding sequential per-file
	// source PROPFINDs and per-file destination probes.
	noTraverseFileThreshold = 32
)

type CopyOptions struct {
	Transfers, Checkers, Retries, Compare int
	SkipExisting, DryRun                  bool
	Include                               func(string, os.FileInfo) bool
}
type Event struct {
	File      string
	Size      int64
	Updated   bool
	Failed    bool
	Error     string
	Stats     *Stats
	ScanDirs  int64
	ScanFiles int64
	ScanDone  bool
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
	Speed          float64        `json:"speed"`
	LastError      string         `json:"lastError"`
	Transferring   []TransferItem `json:"transferring"`
}

func parts(v string) (string, string) {
	n, sub, _ := strings.Cut(strings.Trim(v, "/"), "/")
	return n, sub
}

func (c Config) Pair(src, dst string) (Endpoint, Endpoint, error) {
	sn, _ := parts(src)
	if sn != Local && c[sn]["type"] != "local" {
		return Endpoint{}, Endpoint{}, errors.New("备份源必须是本地授权目录")
	}
	a, e := c.Resolve(src)
	if e != nil {
		return a, Endpoint{}, e
	}
	b, e := c.Resolve(dst)
	if e != nil {
		return a, b, e
	}
	if b.LocalPath != "" && (within(a.LocalPath, b.LocalPath) || within(b.LocalPath, a.LocalPath)) {
		return a, b, errors.New("备份源与目标实际目录重叠")
	}
	return a, b, nil
}
func within(a, b string) bool {
	rel, e := filepath.Rel(a, b)
	return e == nil && (rel == "." || filepath.IsLocal(rel))
}

func destSupportsNativeMD5(dest Endpoint) bool {
	if dest.IsCrypt {
		return false
	}
	switch dest.Backend {
	case "local", "s3":
		return true
	case "webdav":
		return dest.Vendor == "owncloud" || dest.Vendor == "nextcloud"
	default:
		return false
	}
}

func destHasReliableModTime(dest Endpoint) bool {
	if dest.Backend != "webdav" {
		return true
	}
	return slices.Contains([]string{"owncloud", "nextcloud", "rclone", "fastmail", "sharepoint"}, dest.Vendor)
}

func (c Config) Copy(ctx context.Context, src, dst string, opt CopyOptions, emit func(Event)) error {
	_, dest, e := c.Pair(src, dst)
	if e != nil {
		return e
	}
	// Prefer native checksum comparison (--checksum) when the destination
	// exposes MD5 hashes (S3, local, OwnCloud/Nextcloud WebDAV), avoiding full
	// remote downloads. Fall back to `rclone check --download` only when the
	// user requested content comparison on a hashless backend or default mode
	// on standard WebDAV without reliable modtimes.
	useChecksum := !opt.SkipExisting && opt.Compare == 2 && destSupportsNativeMD5(dest)
	deep := !opt.SkipExisting && ((opt.Compare == 2 && !useChecksum) || (opt.Compare == 0 && !destHasReliableModTime(dest)))

	n, sub := parts(dst)
	s, e := c.Session(n, !opt.DryRun, useChecksum)
	if e != nil {
		return e
	}
	defer s.Close()
	_, ss := parts(src)
	sourceArg, e := s.disk(ss, false, useChecksum)
	if e != nil {
		return e
	}
	destArg := dest.Path
	if n == Local || c[n]["type"] == "local" {
		destArg, e = s.disk(sub, !opt.DryRun, useChecksum)
		if e != nil {
			return e
		}
	}
	// Use the same rooted source and OpenSync filter semantics for preview and copy.
	volume, relative, e := openDisk(ss)
	if e != nil {
		return e
	}
	defer volume.Close()
	rootStat, e := volume.Stat(".")
	if e != nil {
		return errors.New("无法读取备份源目录挂载状态")
	}
	rootDev := uint64(rootStat.Sys().(*syscall.Stat_t).Dev)
	source, e := volume.OpenRoot(relative)
	if e != nil {
		return errors.New("无法打开备份源目录")
	}
	defer source.Close()
	list := filepath.Join(s.dir, "files")
	f, e := os.OpenFile(list, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	buf := bufio.NewWriter(f)
	count := 0
	var scannedDirs int64
	e = fs.WalkDir(source.FS(), ".", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("无法完整读取备份源目录")
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		if p == "." {
			scannedDirs++
			emit(Event{ScanDirs: scannedDirs, ScanFiles: int64(count)})
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		st, e := d.Info()
		if e != nil {
			return e
		}
		// Match resolveLocal's mount boundary rule: never silently cross into a
		// different mounted filesystem beneath an authorized root.
		if sys, ok := st.Sys().(*syscall.Stat_t); ok && uint64(sys.Dev) != rootDev {
			if st.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if opt.Include != nil && !opt.Include(p, st) {
			if st.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if st.IsDir() {
			scannedDirs++
			emit(Event{ScanDirs: scannedDirs, ScanFiles: int64(count)})
			return nil
		}
		if !st.Mode().IsRegular() {
			return nil
		}
		if strings.ContainsAny(p, "\r\n") {
			return errors.New("备份源含有换行符文件名，无法生成 rclone 文件清单")
		}
		_, e = fmt.Fprintln(buf, p)
		count++
		if count%500 == 0 {
			emit(Event{ScanDirs: scannedDirs, ScanFiles: int64(count)})
		}
		return e
	})
	flushErr := buf.Flush()
	closeErr := f.Close()
	if e = errors.Join(e, flushErr, closeErr); e != nil {
		return e
	}
	emit(Event{ScanDirs: scannedDirs, ScanFiles: int64(count), ScanDone: true})
	if count == 0 {
		return nil
	}
	common := []string{"--checkers", strconv.Itoa(max(1, opt.Checkers)), "--retries", strconv.Itoa(max(1, opt.Retries+1)), "--low-level-retries", "3"}
	copyCount := count
	if deep {
		diff := filepath.Join(s.dir, "different")
		missing := filepath.Join(s.dir, "missing")
		bad := filepath.Join(s.dir, "unreadable")
		args := append([]string{"check", "--download", "--one-way", "--use-json-log", "--log-level", "INFO", "--stats", "1s", "--files-from-raw", list, "--differ", diff, "--missing-on-dst", missing, "--error", bad}, common...)
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
				log.Printf("native rclone check failed (%v): %s", runErr, detail)
			}
			return fmt.Errorf("读取目标内容失败：%w", runErr)
		}
		if st, e := os.Stat(bad); e != nil || st.Size() != 0 {
			if detail := checkErrBuf.String(); detail != "" {
				log.Printf("native rclone check unreadable (%v): %s", e, detail)
			}
			return errors.New("目标文件无法读取，内容比对未完成")
		}
		out, e := os.OpenFile(list, os.O_TRUNC|os.O_WRONLY, 0600)
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
					log.Printf("native rclone check error with empty diff: %s", detail)
				}
				return errors.New("目标内容比对失败")
			}
			return nil
		}
	}
	args := []string{"copy", "--use-json-log", "--log-level", "INFO", "--stats", "1s", "--transfers", strconv.Itoa(max(1, opt.Transfers)), "--files-from-raw", list}
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
	if opt.SkipExisting {
		args = append(args, "--ignore-existing")
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
		size int64
		msg  string
	}
	failed := map[string]failedInfo{}
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
				sz := m.Size
				if sz <= 0 {
					if st, err := source.Stat(m.Object); err == nil {
						sz = st.Size()
					}
				}
				failed[m.Object] = failedInfo{size: sz, msg: strings.TrimSpace(m.Msg)}
			}
		}
		if m.Stats != nil {
			if msg := strings.TrimSpace(m.Stats.LastError); msg != "" {
				lastErrMsg = msg
			}
			emit(Event{Stats: m.Stats})
			continue
		}
		if !opt.DryRun && m.Object != "" && strings.HasPrefix(m.Msg, "Copied (") {
			delete(failed, m.Object)
			emit(Event{File: path.Clean(m.Object), Size: m.Size, Updated: strings.Contains(m.Msg, "replaced")})
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
			log.Printf("native rclone copy failed (%v): %s", e, lastErrMsg)
		}
		for file, info := range failed {
			emit(Event{File: file, Size: info.size, Failed: true, Error: sanitizeRcloneError(info.msg)})
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
		for lines.Scan() {
			op, name, ok := strings.Cut(lines.Text(), " ")
			if !ok || (op != "+" && op != "*") {
				continue
			}
			st, err := source.Stat(name)
			if err != nil {
				return err
			}
			emit(Event{File: name, Size: st.Size(), Updated: op == "*"})
		}
		return lines.Err()
	}
	return nil
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
	if idx := strings.Index(s, "http://127.0.0.1:"); idx >= 0 {
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
