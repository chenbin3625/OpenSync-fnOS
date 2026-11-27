//go:build linux

package service

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	defaultMaxInotifyWatchesPerJob = 65536
	procMaxUserWatchesPath         = "/proc/sys/fs/inotify/max_user_watches"
)

var readMaxUserWatchesFn = defaultReadMaxUserWatches

func defaultReadMaxUserWatches() int {
	raw, err := os.ReadFile(procMaxUserWatchesPath)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// computeInotifyWatchCap derives a per-job watch ceiling from the kernel's
// max_user_watches (up to 50% of system capacity, capped at 65536).
func computeInotifyWatchCap(sysMax int) int {
	if sysMax <= 0 {
		return defaultMaxInotifyWatchesPerJob
	}
	safeShare := sysMax / 2
	if safeShare < 1 {
		safeShare = 1
	}
	if safeShare > defaultMaxInotifyWatchesPerJob {
		return defaultMaxInotifyWatchesPerJob
	}
	return safeShare
}

func resolveInotifyWatchCap() (watchCap int, sysMax int) {
	sysMax = readMaxUserWatchesFn()
	return computeInotifyWatchCap(sysMax), sysMax
}

func watchLocalRootsPlatform(ctx context.Context, roots []watchLocalRoot, onEvents func(paths []string, fullScan bool), opts localWatchOptions) {
	reportDegraded := func(reason string) {
		if opts.onDegraded != nil && strings.TrimSpace(reason) != "" {
			opts.onDegraded(reason)
		}
	}
	notifyReady := func(covers bool) {
		if opts.onReady != nil {
			opts.onReady(covers)
		}
	}

	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		log.Printf("fs_watcher: inotify init failed (%v), falling back to polling", err)
		reportDegraded(fmt.Sprintf("inotify 初始化失败，已降级为轮询检测：%v", err))
		return
	}
	defer unix.Close(fd)

	watchCap, sysMax := resolveInotifyWatchCap()

	const mask = unix.IN_CLOSE_WRITE | unix.IN_MOVED_TO | unix.IN_MOVED_FROM |
		unix.IN_CREATE | unix.IN_DELETE | unix.IN_ATTRIB | unix.IN_DELETE_SELF | unix.IN_MOVE_SELF

	type wdInfo struct {
		rootAbs string
		dirAbs  string
	}
	wdMap := make(map[int]wdInfo)
	dirToWD := make(map[string]int)
	var lastAddErr error

	addDir := func(rootAbs, dirAbs string) bool {
		if _, exists := dirToWD[dirAbs]; exists {
			return true
		}
		if len(wdMap) >= watchCap {
			return false
		}
		wd, wErr := unix.InotifyAddWatch(fd, dirAbs, mask)
		if wErr != nil {
			lastAddErr = wErr
			return false
		}
		wdMap[wd] = wdInfo{rootAbs: rootAbs, dirAbs: dirAbs}
		dirToWD[dirAbs] = wd
		return true
	}

	limitExceeded := false
	coversChanges := true
	for _, r := range roots {
		// Network mounts can change on the server without emitting local events.
		var stat unix.Statfs_t
		if err := unix.Statfs(r.absDir, &stat); err != nil {
			coversChanges = false
		} else {
			switch uint32(stat.Type) {
			case unix.NFS_SUPER_MAGIC, unix.CIFS_SUPER_MAGIC, unix.SMB2_SUPER_MAGIC, unix.SMB_SUPER_MAGIC,
				unix.FUSE_SUPER_MAGIC, unix.V9FS_MAGIC, unix.CEPH_SUPER_MAGIC, unix.AFS_SUPER_MAGIC:
				coversChanges = false
			}
		}
		_ = filepath.WalkDir(r.absDir, func(p string, d fs.DirEntry, walkErr error) error {
			if ctx.Err() != nil {
				return fs.SkipAll
			}
			if walkErr != nil || (p == r.absDir && !d.IsDir()) {
				if walkErr != nil {
					lastAddErr = walkErr
				}
				limitExceeded = true
				return fs.SkipAll
			}
			if !d.IsDir() {
				return nil
			}
			if shouldSkipWatchDir(r.absDir, p, d.Name(), opts.exclude) {
				return filepath.SkipDir
			}
			if !addDir(r.absDir, p) {
				limitExceeded = true
				return fs.SkipAll
			}
			return nil
		})
		if limitExceeded {
			break
		}
	}
	if ctx.Err() != nil {
		return
	}
	if limitExceeded {
		reason := fmt.Sprintf("本地目录数超过 inotify 监听上限（%d，系统上限=%d），已降级为周期轮询", watchCap, sysMax)
		if lastAddErr != nil {
			reason = fmt.Sprintf("安装 inotify 目录监听失败，已降级为周期轮询：%v", lastAddErr)
		}
		log.Printf("fs_watcher: %s", reason)
		reportDegraded(reason)
		return
	}
	if !coversChanges {
		reportDegraded("源目录位于网络或虚拟文件系统，inotify 无法覆盖服务端变更，已开启周期兜底轮询")
	}
	notifyReady(coversChanges)

	buf := make([]byte, 32*1024)
	pollFds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}

	for {
		if ctx.Err() != nil {
			return
		}
		n, pollErr := unix.Poll(pollFds, 500)
		if pollErr != nil {
			if errors.Is(pollErr, unix.EINTR) {
				continue
			}
			reportDegraded(fmt.Sprintf("inotify poll 异常，已降级为周期轮询：%v", pollErr))
			onEvents(nil, true)
			return
		}
		if n <= 0 {
			continue
		}
		if pollFds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
			reportDegraded("inotify 监听句柄异常关闭，已降级为周期轮询")
			onEvents(nil, true)
			return
		}
		rn, readErr := unix.Read(fd, buf)
		if readErr != nil {
			if errors.Is(readErr, unix.EAGAIN) || errors.Is(readErr, unix.EINTR) {
				continue
			}
			reportDegraded(fmt.Sprintf("读取 inotify 事件失败，已降级为周期轮询：%v", readErr))
			onEvents(nil, true)
			return
		}
		if rn < unix.SizeofInotifyEvent {
			continue
		}
		var changed []string
		fullScan := false
		offset := 0
		for offset+unix.SizeofInotifyEvent <= rn {
			raw := (*unix.InotifyEvent)(unsafe.Pointer(&buf[offset]))
			nameLen := int(raw.Len)
			offset += unix.SizeofInotifyEvent
			name := ""
			if nameLen > 0 && offset+nameLen <= rn {
				nameBytes := buf[offset : offset+nameLen]
				name = strings.TrimRight(string(nameBytes), "\x00")
			}
			offset += nameLen
			// An overflow can hide newly created directories and invalidate coverage.
			if raw.Mask&unix.IN_Q_OVERFLOW != 0 {
				reportDegraded("inotify 事件队列溢出，已降级为周期轮询并触发全量对账")
				onEvents(changed, true)
				return
			}
			info, ok := wdMap[int(raw.Wd)]
			if !ok {
				continue
			}
			if raw.Mask&unix.IN_IGNORED != 0 {
				delete(dirToWD, info.dirAbs)
				delete(wdMap, int(raw.Wd))
				continue
			}
			if raw.Mask&(unix.IN_DELETE_SELF|unix.IN_MOVE_SELF|unix.IN_UNMOUNT) != 0 {
				reportDegraded("监听目录被移除或卸载，已降级为周期轮询")
				onEvents(changed, true)
				return
			}
			if name == "" {
				continue
			}
			if name == ".opensync-partial" || name == ".opensync-trash" || strings.HasPrefix(name, ".opensync-") {
				continue
			}
			fullPath := filepath.Join(info.dirAbs, name)
			if raw.Mask&(unix.IN_CREATE|unix.IN_MOVED_TO) != 0 && raw.Mask&unix.IN_ISDIR != 0 {
				if shouldSkipWatchDir(info.rootAbs, fullPath, name, opts.exclude) {
					continue
				}
				// Watch newly created directory subtree.
				walkErr := filepath.WalkDir(fullPath, func(p string, d fs.DirEntry, walkErr error) error {
					if ctx.Err() != nil {
						return fs.SkipAll
					}
					if walkErr != nil {
						return walkErr
					}
					if !d.IsDir() {
						return nil
					}
					if shouldSkipWatchDir(info.rootAbs, p, d.Name(), opts.exclude) {
						return filepath.SkipDir
					}
					if !addDir(info.rootAbs, p) {
						limitExceeded = true
						return fs.SkipAll
					}
					return nil
				})
				fullScan = true
				if limitExceeded || (walkErr != nil && !errors.Is(walkErr, fs.ErrNotExist)) {
					reason := fmt.Sprintf("新增子目录后超出 inotify 监听上限（%d），已降级为周期轮询", watchCap)
					if !limitExceeded && walkErr != nil {
						reason = fmt.Sprintf("扩展子目录 inotify 监听失败，已降级为周期轮询：%v", walkErr)
					}
					reportDegraded(reason)
					onEvents(changed, true)
					return
				}
				continue
			}
			if raw.Mask&(unix.IN_DELETE|unix.IN_MOVED_FROM|unix.IN_DELETE_SELF) != 0 {
				if raw.Mask&unix.IN_ISDIR != 0 && shouldSkipWatchDir(info.rootAbs, fullPath, name, opts.exclude) {
					continue
				}
				fullScan = true
				continue
			}
			rel, relErr := filepath.Rel(info.rootAbs, fullPath)
			if relErr != nil {
				fullScan = true
				continue
			}
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "../") || rel == "." {
				continue
			}
			if opts.exclude != nil && opts.exclude.matchesListed(rel, raw.Mask&unix.IN_ISDIR != 0) {
				continue
			}
			if raw.Mask&unix.IN_ISDIR != 0 {
				fullScan = true
				continue
			}
			// Ignore transient zero-event or non-regular files if already gone.
			if st, statErr := os.Lstat(fullPath); statErr == nil && st.Mode().IsRegular() {
				changed = append(changed, rel)
			} else {
				fullScan = true
			}
		}
		if fullScan || len(changed) > 0 {
			onEvents(changed, fullScan)
		}
	}
}

// Keep binary import used on all builds cleanly.
var _ = binary.LittleEndian
var _ = time.Second
