//go:build linux

package service

import (
	"context"
	"encoding/binary"
	"errors"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const maxInotifyWatchesPerJob = 4096

func watchLocalRootsPlatform(ctx context.Context, roots []watchLocalRoot, onEvent func(relPath string, overflow bool)) {
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		log.Printf("fs_watcher: inotify init failed (%v), falling back to polling", err)
		watchLocalRootsPolling(ctx, roots, onEvent)
		return
	}
	defer unix.Close(fd)

	const mask = unix.IN_CLOSE_WRITE | unix.IN_MOVED_TO | unix.IN_MOVED_FROM |
		unix.IN_CREATE | unix.IN_DELETE | unix.IN_ATTRIB | unix.IN_DELETE_SELF | unix.IN_MOVE_SELF

	type wdInfo struct {
		rootAbs string
		dirAbs  string
	}
	wdMap := make(map[int]wdInfo)
	dirToWD := make(map[string]int)

	addDir := func(rootAbs, dirAbs string) bool {
		if _, exists := dirToWD[dirAbs]; exists {
			return true
		}
		if len(wdMap) >= maxInotifyWatchesPerJob {
			return false
		}
		wd, wErr := unix.InotifyAddWatch(fd, dirAbs, mask)
		if wErr != nil {
			return false
		}
		wdMap[wd] = wdInfo{rootAbs: rootAbs, dirAbs: dirAbs}
		dirToWD[dirAbs] = wd
		return true
	}

	limitExceeded := false
	for _, r := range roots {
		_ = filepath.WalkDir(r.absDir, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				limitExceeded = true
				return fs.SkipAll
			}
			if !d.IsDir() {
				return nil
			}
			name := d.Name()
			if p != r.absDir && (name == ".opensync-partial" || name == ".opensync-trash" || strings.HasPrefix(name, ".opensync-")) {
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
	if limitExceeded {
		log.Printf("fs_watcher: incomplete inotify coverage (cap %d), falling back to polling", maxInotifyWatchesPerJob)
		watchLocalRootsPolling(ctx, roots, onEvent)
		return
	}

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
			onEvent("", true)
			watchLocalRootsPolling(ctx, roots, onEvent)
			return
		}
		if n <= 0 {
			continue
		}
		rn, readErr := unix.Read(fd, buf)
		if readErr != nil {
			if errors.Is(readErr, unix.EAGAIN) || errors.Is(readErr, unix.EINTR) {
				continue
			}
			onEvent("", true)
			watchLocalRootsPolling(ctx, roots, onEvent)
			return
		}
		if rn < unix.SizeofInotifyEvent {
			continue
		}
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
			// Consume queue overflow cleanly.
			if raw.Mask&unix.IN_Q_OVERFLOW != 0 {
				onEvent("", true)
				continue
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
				onEvent("", true)
				watchLocalRootsPolling(ctx, roots, onEvent)
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
				// Watch newly created directory subtree.
				_ = filepath.WalkDir(fullPath, func(p string, d fs.DirEntry, walkErr error) error {
					if walkErr != nil || !d.IsDir() {
						return nil
					}
					if !addDir(info.rootAbs, p) {
						limitExceeded = true
						onEvent("", true)
						return fs.SkipAll
					}
					return nil
				})
				onEvent("", true)
				if limitExceeded {
					watchLocalRootsPolling(ctx, roots, onEvent)
					return
				}
				continue
			}
			if raw.Mask&(unix.IN_DELETE|unix.IN_MOVED_FROM|unix.IN_DELETE_SELF) != 0 {
				onEvent("", true)
				continue
			}
			rel, relErr := filepath.Rel(info.rootAbs, fullPath)
			if relErr != nil {
				onEvent("", true)
				continue
			}
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "../") || rel == "." {
				continue
			}
			if raw.Mask&unix.IN_ISDIR != 0 {
				onEvent("", true)
				continue
			}
			// Ignore transient zero-event or non-regular files if already gone.
			if st, statErr := os.Lstat(fullPath); statErr == nil && st.Mode().IsRegular() {
				onEvent(rel, false)
			} else {
				onEvent("", true)
			}
		}
	}
}

// Keep binary import used on all builds cleanly.
var _ = binary.LittleEndian
var _ = time.Second
