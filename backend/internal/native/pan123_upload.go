package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"opensync/internal/pan123"
)

const pan123ResumeMinBytes int64 = 16 << 20 // persist part resume state for >= 16 MiB files

// canUsePan123DirectUpload reports whether remoteParams represents a valid
// 123Pan remote suitable for OpenSync's pure-Go rapid-upload + concurrent
// multipart upload fast path.
func canUsePan123DirectUpload(p map[string]string) bool {
	if p == nil || p["type"] != "pan123" {
		return false
	}
	proto := pan123.NormalizeProtocol(p["protocol"])
	if proto == pan123.ProtocolWeb {
		return strings.TrimSpace(p["access_token"]) != "" ||
			(strings.TrimSpace(p["web_user"]) != "" && p["web_password"] != "")
	}
	return strings.TrimSpace(p["access_token"]) != "" ||
		(strings.TrimSpace(p["client_id"]) != "" && strings.TrimSpace(p["client_secret"]) != "")
}

func pan123ResumeStatePath(stateDir, remoteName, dstRel string) string {
	sum := sha256.Sum256([]byte(remoteName + "\x00" + dstRel))
	return filepath.Join(stateDir, "pan123", hex.EncodeToString(sum[:16])+".json")
}

// directUploadFilesToPan123 transfers local files directly to 123Pan using
// rapid upload (秒传), small-file single-step upload (OpenAPI), concurrent
// multipart upload, persistent part-level resume state, and live RCController
// pause/resume + bandwidth throttling.
func directUploadFilesToPan123(
	ctx context.Context,
	srcSub string,
	remoteName string,
	remoteParams map[string]string,
	dstSub string,
	files []resumeFileMeta,
	opt CopyOptions,
	emit func(Event),
) error {
	if len(files) == 0 {
		return nil
	}
	if opt.Controller != nil {
		leaveGo := opt.Controller.EnterGoTransfer()
		defer leaveGo()
	}

	orig := maps.Clone(remoteParams)
	sessOpts := SessionOptions{ProxyURL: opt.ProxyURL}
	client, err := newPan123ClientFromParams(orig, sessOpts, func(newToken string, expiresAt time.Time) {
		dispatchPan123TokenUpdate(remoteName, orig, newToken, expiresAt, sessOpts)
	})
	if err != nil {
		return err
	}
	defer client.Close()

	root, baseRel, err := openDisk(srcSub)
	if err != nil {
		return err
	}
	defer root.Close()

	stateDir := strings.TrimSpace(opt.ResumeStateDir)
	if stateDir == "" {
		stateDir = filepath.Join(nativeTempDir(), "opensync-pan123-resume")
	}

	partConcurrency := pan123.DefaultUploadConcurrency
	if uc, err := strconv.Atoi(strings.TrimSpace(remoteParams["upload_concurrency"])); err == nil && uc >= 1 && uc <= pan123.MaxUploadConcurrency {
		partConcurrency = uc
	}

	fileWorkers := opt.Transfers
	if fileWorkers <= 0 {
		fileWorkers = 2
	}
	if fileWorkers > 4 {
		fileWorkers = 4
	}
	if fileWorkers > len(files) {
		fileWorkers = len(files)
	}

	dstPrefix := strings.Trim(dstSub, "/")
	skipExisting := opt.SkipExisting || opt.ConflictPolicy == 1

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
		var inFlight int64
		var speedSum float64
		for _, item := range activeItems {
			transferring = append(transferring, item)
			inFlight += item.Bytes
			speedSum += item.Speed
		}
		activeMu.Unlock()
		emit(Event{Stats: &Stats{
			Bytes:          completedBytes.Load() + inFlight,
			TotalBytes:     max(0, totalBytes-failedBytes.Load()),
			Transfers:      completedCount.Load(),
			TotalTransfers: max(0, int64(len(files))-errorCount.Load()),
			Errors:         errorCount.Load(),
			Speed:          speedSum,
			Transferring:   transferring,
		}})
	}

	jobCh := make(chan resumeFileMeta, len(files))
	for _, fm := range files {
		jobCh <- fm
	}
	close(jobCh)

	var wg sync.WaitGroup
	for w := 0; w < fileWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for fm := range jobCh {
				if ctx.Err() != nil {
					return
				}
				if opt.Controller != nil {
					if err := opt.Controller.WaitIfPaused(ctx); err != nil {
						return
					}
				}

				dstRel := fm.Rel
				if dstPrefix != "" {
					dstRel = path.Join(dstPrefix, fm.Rel)
				}
				dstRel = strings.TrimPrefix(path.Clean("/"+dstRel), "/")
				srcRel := joinInRoot(baseRel, fm.Rel)

				var existing pan123.Entry
				exists := false
				if st, statErr := client.Stat(ctx, dstRel); statErr == nil {
					if st.IsDir {
						err := fmt.Errorf("目标路径 %q 已存在同名目录", dstRel)
						errorCount.Add(1)
						failedBytes.Add(fm.Size)
						emit(Event{File: fm.Rel, Size: fm.Size, Failed: true, Error: sanitizeRcloneError(err.Error())})
						errsMu.Lock()
						errs = append(errs, err)
						errsMu.Unlock()
						publishStats()
						continue
					}
					existing = st
					exists = true
				} else if !errors.Is(statErr, os.ErrNotExist) {
					if ctx.Err() != nil {
						return
					}
					errorCount.Add(1)
					failedBytes.Add(fm.Size)
					emit(Event{File: fm.Rel, Size: fm.Size, Failed: true, Error: sanitizeRcloneError(statErr.Error())})
					errsMu.Lock()
					errs = append(errs, statErr)
					errsMu.Unlock()
					publishStats()
					continue
				}

				if exists && !opt.ForceOverwrite {
					if skipExisting {
						completedBytes.Add(fm.Size)
						completedCount.Add(1)
						publishStats()
						continue
					}
					if existing.Size == fm.Size {
						if opt.Compare == 1 {
							completedBytes.Add(fm.Size)
							completedCount.Add(1)
							publishStats()
							continue
						}
						if existing.MD5 != "" {
							if localMD5, md5Err := md5FileInRoot(root, srcRel); md5Err == nil && strings.EqualFold(localMD5, existing.MD5) {
								completedBytes.Add(fm.Size)
								completedCount.Add(1)
								publishStats()
								continue
							}
						}
					}
				}

				if exists {
					var prepErr error
					if opt.ConflictPolicy == 2 {
						suffix := strings.TrimSpace(opt.ConflictSuffix)
						if suffix == "" {
							suffix = ".conflict-" + time.Now().Format("20060102-150405")
						}
						ext := path.Ext(dstRel)
						stem := strings.TrimSuffix(dstRel, ext)
						prepErr = client.Move(ctx, dstRel, stem+suffix+ext, true)
					} else if bd := strings.TrimSpace(opt.BackupDir); bd != "" {
						backupRel := path.Join(bd, fm.Rel)
						if dstPrefix != "" {
							backupRel = path.Join(dstPrefix, backupRel)
						}
						backupRel = strings.TrimPrefix(path.Clean("/"+backupRel), "/")
						prepErr = client.Move(ctx, dstRel, backupRel, true)
					}
					if prepErr != nil {
						if ctx.Err() != nil {
							return
						}
						errorCount.Add(1)
						failedBytes.Add(fm.Size)
						emit(Event{File: fm.Rel, Size: fm.Size, Failed: true, Error: sanitizeRcloneError(prepErr.Error())})
						errsMu.Lock()
						errs = append(errs, prepErr)
						errsMu.Unlock()
						publishStats()
						continue
					}
				}

				upErr := uploadOneFileToPan123(ctx, client, root, srcRel, remoteName, dstRel, stateDir, partConcurrency, fm.Size, opt, func(doneBytes int64, speed float64, rapid bool) {
					pct := 0.0
					if fm.Size > 0 {
						pct = float64(doneBytes) * 100.0 / float64(fm.Size)
						if pct > 100 {
							pct = 100
						}
					} else if rapid {
						pct = 100
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
				})
				activeMu.Lock()
				delete(activeItems, fm.Rel)
				activeMu.Unlock()

				if upErr != nil {
					if ctx.Err() != nil {
						return
					}
					errorCount.Add(1)
					failedBytes.Add(fm.Size)
					emit(Event{File: fm.Rel, Size: fm.Size, Failed: true, Error: sanitizeRcloneError(upErr.Error())})
					errsMu.Lock()
					errs = append(errs, upErr)
					errsMu.Unlock()
					publishStats()
					continue
				}

				completedBytes.Add(fm.Size)
				completedCount.Add(1)
				emit(Event{File: fm.Rel, Size: fm.Size, Updated: exists})
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

func uploadOneFileToPan123(
	ctx context.Context,
	client *pan123.Client,
	root *os.Root,
	srcRel, remoteName, dstRel, stateDir string,
	partConcurrency int,
	expectedSize int64,
	opt CopyOptions,
	onProgress func(doneBytes int64, speed float64, rapid bool),
) error {
	f, err := root.Open(srcRel)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	size := st.Size()
	if size < 0 {
		size = expectedSize
	}

	resumePath := ""
	if opt.ResumeEnable || size >= pan123ResumeMinBytes {
		resumePath = pan123ResumeStatePath(stateDir, remoteName, dstRel)
	}

	var waitFn func(context.Context) error
	var throttleFn func(context.Context, int) error
	if opt.Controller != nil {
		waitFn = opt.Controller.WaitIfPaused
		throttleFn = opt.Controller.ThrottleUploadBytes
	}

	res, err := client.UploadFile(ctx, dstRel, pan123.UploadFileOptions{
		File:            f,
		Size:            size,
		ModTime:         st.ModTime(),
		Concurrency:     partConcurrency,
		ResumeStatePath: resumePath,
		Overwrite:       true,
		WaitIfPaused:    waitFn,
		ThrottleBytes:   throttleFn,
		OnProgress:      onProgress,
	})
	if err != nil {
		return err
	}
	if res != nil && res.MD5 != "" {
		if k, ok := md5CacheKeyFromFileInfo(st); ok {
			defaultMD5Cache.set(k, res.MD5)
		}
	}
	return nil
}
