package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// PartialDirName stores deterministic byte-level resumable download staging
	// files inside the local destination root. It is ignored by local scans.
	PartialDirName        = ".opensync-partial"
	defaultResumeMinBytes = 50 << 20 // 50 MiB
	stalePartialMaxAge    = 7 * 24 * time.Hour
)

func isInternalPartialEntry(name string) bool {
	if name == PartialDirName {
		return true
	}
	return strings.HasPrefix(name, ".opensync-") && strings.HasSuffix(name, ".partial")
}

type localPartialMeta struct {
	SrcPath   string `json:"srcPath"`
	RelPath   string `json:"relPath"`
	Size      int64  `json:"size"`
	ModTime   int64  `json:"modTime"`
	MD5       string `json:"md5,omitempty"`
	UpdatedAt int64  `json:"updatedAt"`
}

type resumeFileMeta struct {
	Rel     string
	Size    int64
	ModTime int64
	MD5     string
}

func effectiveResumeThreshold(opt CopyOptions) int64 {
	if opt.ResumeMinBytes > 0 {
		return opt.ResumeMinBytes
	}
	if opt.ResumeMinSizeMB > 0 {
		return int64(opt.ResumeMinSizeMB) << 20
	}
	return defaultResumeMinBytes
}

func partialKeyHash(scope, rel string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + rel))
	return hex.EncodeToString(sum[:16])
}

func joinInRoot(base, rel string) string {
	rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
	if base == "" || base == "." {
		if rel == "" {
			return "."
		}
		return rel
	}
	if rel == "" {
		return path.Clean(base)
	}
	return path.Join(base, rel)
}

func readPartialMeta(root *os.Root, metaRel string) (*localPartialMeta, error) {
	f, err := root.Open(metaRel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 32<<10))
	if err != nil {
		return nil, err
	}
	var m localPartialMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func writePartialMeta(root *os.Root, metaRel string, meta localPartialMeta) error {
	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	tmp := metaRel + "." + randomName() + ".tmp"
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, wErr := f.Write(data)
	cErr := f.Close()
	if wErr != nil || cErr != nil {
		_ = root.Remove(tmp)
		if wErr != nil {
			return wErr
		}
		return cErr
	}
	if err := root.Rename(tmp, metaRel); err != nil {
		_ = root.Remove(tmp)
		return err
	}
	return nil
}

func pruneStalePartials(root *os.Root, base string, maxAge time.Duration) {
	partialDir := joinInRoot(base, PartialDirName)
	dir, err := root.Open(partialDir)
	if err != nil {
		return
	}
	entries, readErr := dir.ReadDir(-1)
	_ = dir.Close()
	if readErr != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		st, err := entry.Info()
		if err != nil {
			continue
		}
		if st.ModTime().Before(cutoff) {
			_ = root.Remove(path.Join(partialDir, entry.Name()))
		}
	}
}

func localDstMatchesSource(root *os.Root, targetRel string, src resumeFileMeta, compare int) (exists bool, same bool) {
	st, err := root.Lstat(targetRel)
	if err != nil || st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return false, false
	}
	if st.Size() != src.Size {
		return true, false
	}
	switch compare {
	case 1: // size-only
		return true, true
	case 2: // checksum
		if src.MD5 != "" {
			if dstMD5, err := md5FileInRoot(root, targetRel); err == nil && strings.EqualFold(dstMD5, src.MD5) {
				return true, true
			}
			return true, false
		}
		return true, false
	default: // size + modtime (1s tolerance)
		if src.ModTime > 0 {
			diff := st.ModTime().Unix() - src.ModTime
			if diff < 0 {
				diff = -diff
			}
			return true, diff <= 1
		}
		return true, true
	}
}

func handleLocalConflictBeforeReplace(root *os.Root, base, rel, targetRel string, opt CopyOptions) error {
	if _, err := root.Lstat(targetRel); err != nil {
		return nil
	}
	if opt.ConflictPolicy == 2 {
		suffix := strings.TrimSpace(opt.ConflictSuffix)
		if suffix == "" {
			suffix = ".conflict-" + time.Now().Format("20060102-150405")
		}
		ext := path.Ext(targetRel)
		stem := strings.TrimSuffix(targetRel, ext)
		renamed := stem + suffix + ext
		return root.Rename(targetRel, renamed)
	}
	if bd := strings.TrimSpace(opt.BackupDir); bd != "" {
		backupTarget := joinInRoot(base, path.Join(bd, rel))
		if err := mkdirAllPerm(root, path.Dir(backupTarget), backupDirPerm); err != nil {
			return err
		}
		return root.Rename(targetRel, backupTarget)
	}
	return nil
}

// resumeDownloadFilesToLocal transfers large files from a remote source to a
// local os.Root destination using `rclone cat --offset <N>`, persisting partial
// downloads in .opensync-partial/ so interruptions resume at exact byte offset.
func resumeDownloadFilesToLocal(
	ctx context.Context,
	s *Session,
	sourceArg string,
	dstSub string,
	files []resumeFileMeta,
	opt CopyOptions,
	emit func(Event),
) error {
	if len(files) == 0 {
		return nil
	}
	root, baseRel, err := openDisk(dstSub)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := mkdirAllPerm(root, baseRel, backupDirPerm); err != nil {
		return err
	}
	partialDir := joinInRoot(baseRel, PartialDirName)
	if err := mkdirAllPerm(root, partialDir, 0700); err != nil {
		return err
	}
	pruneStalePartials(root, baseRel, stalePartialMaxAge)

	var totalBytes int64
	for _, f := range files {
		totalBytes += f.Size
	}
	var (
		completedBytes atomic.Int64
		completedCount atomic.Int64
		failedBytes    atomic.Int64
		errorCount     atomic.Int64
		activeMu       sync.Mutex
		activeItems    = map[string]TransferItem{}
		errsMu         sync.Mutex
		errs           []error
	)

	publishStats := func() {
		activeMu.Lock()
		transferring := make([]TransferItem, 0, len(activeItems))
		var inFlightBytes int64
		var totalSpeed float64
		for _, item := range activeItems {
			transferring = append(transferring, item)
			inFlightBytes += item.Bytes
			totalSpeed += item.Speed
		}
		activeMu.Unlock()
		emit(Event{Stats: &Stats{
			Bytes:          completedBytes.Load() + inFlightBytes,
			TotalBytes:     max(0, totalBytes-failedBytes.Load()),
			Transfers:      completedCount.Load(),
			TotalTransfers: max(0, int64(len(files))-errorCount.Load()),
			Errors:         errorCount.Load(),
			Speed:          totalSpeed,
			Transferring:   transferring,
		}})
	}

	skipExisting := opt.SkipExisting || opt.ConflictPolicy == 1
	workers := min(max(1, opt.Transfers), len(files))
	ch := make(chan resumeFileMeta, len(files))
	for _, f := range files {
		ch <- f
	}
	close(ch)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for fm := range ch {
				if ctx.Err() != nil {
					return
				}
				if err := opt.Controller.WaitIfPaused(ctx); err != nil {
					return
				}
				existed, same := localDstMatchesSource(root, joinInRoot(baseRel, fm.Rel), fm, opt.Compare)
				if existed && (skipExisting || (!opt.ForceOverwrite && same)) {
					completedBytes.Add(fm.Size)
					completedCount.Add(1)
					publishStats()
					continue
				}
				if dlErr := resumeDownloadOneFile(ctx, s, root, baseRel, partialDir, sourceArg, fm, existed, opt, func(doneBytes int64, speed float64) {
					pct := 0.0
					if fm.Size > 0 {
						pct = float64(doneBytes) * 100.0 / float64(fm.Size)
						if pct > 100 {
							pct = 100
						}
					}
					activeMu.Lock()
					activeItems[fm.Rel] = TransferItem{
						Name:       fm.Rel,
						Size:       fm.Size,
						Bytes:      doneBytes,
						Percentage: pct,
						Speed:      speed,
					}
					activeMu.Unlock()
					publishStats()
				}); dlErr != nil {
					activeMu.Lock()
					delete(activeItems, fm.Rel)
					activeMu.Unlock()
					if ctx.Err() != nil {
						return
					}
					errorCount.Add(1)
					failedBytes.Add(fm.Size)
					errText := sanitizeRcloneError(dlErr.Error())
					emit(Event{File: fm.Rel, Size: fm.Size, Failed: true, Error: errText})
					errsMu.Lock()
					errs = append(errs, dlErr)
					errsMu.Unlock()
					publishStats()
					continue
				}
				activeMu.Lock()
				delete(activeItems, fm.Rel)
				activeMu.Unlock()
				completedBytes.Add(fm.Size)
				completedCount.Add(1)
				emit(Event{File: fm.Rel, Size: fm.Size, Updated: existed})
				publishStats()
			}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func resumeDownloadOneFile(
	ctx context.Context,
	s *Session,
	root *os.Root,
	baseRel, partialDir, sourceArg string,
	fm resumeFileMeta,
	existed bool,
	opt CopyOptions,
	onProgress func(doneBytes int64, speed float64),
) error {
	key := partialKeyHash(sourceArg, fm.Rel)
	partRel := path.Join(partialDir, key+".part")
	metaRel := path.Join(partialDir, key+".meta.json")
	targetRel := joinInRoot(baseRel, fm.Rel)

	var offset int64
	if meta, err := readPartialMeta(root, metaRel); err == nil {
		metaMatch := meta.SrcPath == sourceArg &&
			meta.RelPath == fm.Rel &&
			meta.Size == fm.Size &&
			(fm.ModTime == 0 || meta.ModTime == fm.ModTime) &&
			(fm.MD5 == "" || meta.MD5 == "" || strings.EqualFold(meta.MD5, fm.MD5))
		if metaMatch {
			if st, statErr := root.Stat(partRel); statErr == nil && st.Mode().IsRegular() {
				if st.Size() >= 0 && st.Size() <= fm.Size {
					offset = st.Size()
				}
			}
		}
	}

	if offset == 0 {
		_ = root.Remove(partRel)
	}
	if err := writePartialMeta(root, metaRel, localPartialMeta{
		SrcPath:   sourceArg,
		RelPath:   fm.Rel,
		Size:      fm.Size,
		ModTime:   fm.ModTime,
		MD5:       fm.MD5,
		UpdatedAt: time.Now().Unix(),
	}); err != nil {
		return err
	}

	maxAttempts := max(1, opt.Retries+1)
	if opt.LowLevelRetries > maxAttempts {
		maxAttempts = min(opt.LowLevelRetries, 10)
	}
	var lastErr error
	for attempt := 0; attempt < maxAttempts && offset < fm.Size; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := opt.Controller.WaitIfPaused(ctx); err != nil {
			return err
		}
		if attempt > 0 {
			sleepDur := time.Duration(min(attempt*2, 15)) * time.Second
			if rs := strings.TrimSpace(opt.RetriesSleep); rs != "" {
				if parsed, err := time.ParseDuration(rs); err == nil && parsed > 0 {
					sleepDur = parsed
				}
			}
			timer := time.NewTimer(sleepDur)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}

		nextOffset, attemptErr := streamRcloneCatChunk(ctx, s, root, partRel, joinRcloneRemotePath(sourceArg, fm.Rel), offset, fm.Size, opt, onProgress)
		offset = nextOffset
		if attemptErr == nil && offset >= fm.Size {
			break
		}
		lastErr = attemptErr
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	if offset != fm.Size {
		if lastErr != nil {
			return lastErr
		}
		return fmt.Errorf("断点下载未完成（已接收 %d / %d 字节）", offset, fm.Size)
	}

	// Optional MD5 verification when compareMode == 2 and source exposes MD5.
	if opt.Compare == 2 && fm.MD5 != "" {
		if gotMD5, err := md5FileInRoot(root, partRel); err == nil && !strings.EqualFold(gotMD5, fm.MD5) {
			_ = root.Remove(partRel)
			_ = root.Remove(metaRel)
			return fmt.Errorf("断点下载校验和不匹配（期望 %s，实际 %s），已清理损坏分片", fm.MD5, gotMD5)
		}
	}

	if err := mkdirAllPerm(root, path.Dir(targetRel), backupDirPerm); err != nil {
		return err
	}
	if existed {
		if err := handleLocalConflictBeforeReplace(root, baseRel, fm.Rel, targetRel, opt); err != nil {
			return err
		}
	}
	if f, err := root.Open(partRel); err == nil {
		_ = f.Chmod(backupFilePerm)
		_ = f.Close()
	}
	if !opt.NoUpdateModtime && fm.ModTime > 0 {
		t := time.Unix(fm.ModTime, 0)
		_ = root.Chtimes(partRel, t, t)
	}
	if err := root.Rename(partRel, targetRel); err != nil {
		return err
	}
	_ = root.Remove(metaRel)
	return nil
}

func streamRcloneCatChunk(
	ctx context.Context,
	s *Session,
	root *os.Root,
	partRel, srcFileArg string,
	startOffset, totalSize int64,
	opt CopyOptions,
	onProgress func(doneBytes int64, speed float64),
) (int64, error) {
	flags := os.O_CREATE | os.O_WRONLY
	if startOffset == 0 {
		flags |= os.O_TRUNC
	}
	outFile, err := root.OpenFile(partRel, flags, backupFilePerm)
	if err != nil {
		return startOffset, err
	}
	defer outFile.Close()
	if startOffset > 0 {
		if _, err := outFile.Seek(startOffset, io.SeekStart); err != nil {
			return startOffset, err
		}
	}

	args := []string{"cat", "--offset", strconv.FormatInt(startOffset, 10)}
	if bw := strings.TrimSpace(opt.BwLimit); bw != "" && opt.Controller == nil {
		args = append(args, "--bwlimit", bw)
	}
	if opt.IOTimeout != "" {
		args = append(args, "--timeout", opt.IOTimeout)
	}
	if opt.ConTimeout != "" {
		args = append(args, "--contimeout", opt.ConTimeout)
	}
	if opt.UserAgent != "" {
		args = append(args, "--user-agent", opt.UserAgent)
	}
	args = append(args, "--", srcFileArg)

	cmd, err := s.Cmd(ctx, args...)
	if err != nil {
		return startOffset, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return startOffset, err
	}
	errBuf := &tailBuffer{limit: maxStderrCapture}
	cmd.Stderr = errBuf
	if err := cmd.Start(); err != nil {
		return startOffset, err
	}
	if opt.Controller != nil {
		leaveGo := opt.Controller.EnterGoTransfer()
		defer leaveGo()
	}
	if opt.Controller != nil && cmd.Process != nil {
		detachCat := opt.Controller.Attach(cmd.Process.Pid, "", "", "")
		defer detachCat()
		stopCatWake := context.AfterFunc(ctx, func() { opt.Controller.ResumeAllProcesses() })
		defer stopCatWake()
	}

	buf := make([]byte, 64<<10)
	written := startOffset
	windowStart := time.Now()
	var windowBytes int64
	lastReport := time.Now()
	onProgress(written, 0)

	var ioErr error
	for {
		if err := opt.Controller.WaitIfPaused(ctx); err != nil {
			ioErr = err
			break
		}
		n, readErr := stdout.Read(buf)
		if n > 0 {
			if written+int64(n) > totalSize && totalSize > 0 {
				n = int(totalSize - written)
			}
			if n > 0 {
				if _, wErr := outFile.Write(buf[:n]); wErr != nil {
					ioErr = wErr
					break
				}
				written += int64(n)
				windowBytes += int64(n)
				if err := opt.Controller.ThrottleDownloadBytes(ctx, n); err != nil {
					ioErr = err
					break
				}
				now := time.Now()
				if now.Sub(lastReport) >= 400*time.Millisecond {
					elapsed := now.Sub(windowStart).Seconds()
					speed := 0.0
					if elapsed > 0 {
						speed = float64(windowBytes) / elapsed
					}
					onProgress(written, speed)
					lastReport = now
					if elapsed >= 2 {
						windowStart = now
						windowBytes = 0
					}
				}
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				ioErr = readErr
			}
			break
		}
	}
	if ioErr != nil && cmd.Process != nil {
		_ = resumeProcess(cmd.Process.Pid)
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	_ = outFile.Sync()
	if ctx.Err() != nil {
		return written, ctx.Err()
	}
	if ioErr != nil {
		return written, ioErr
	}
	if waitErr != nil {
		detail := strings.TrimSpace(errBuf.String())
		if detail != "" {
			return written, fmt.Errorf("断点下载中断：%s", sanitizeRcloneError(detail))
		}
		return written, fmt.Errorf("断点下载中断：%w", waitErr)
	}
	return written, nil
}
