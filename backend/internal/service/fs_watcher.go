package service

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"opensync/internal/native"
)

const (
	defaultWatchDebounceSec = 30
	minWatchDebounceSec     = 2
	watchPollingInterval    = 5 * time.Second
)

type watchLocalRoot struct {
	virtualPath string
	absDir      string
}

type jobWatcher struct {
	jobID            int64
	cancel           context.CancelFunc
	done             chan struct{}
	roots            []watchLocalRoot
	mu               sync.Mutex
	wake             chan struct{}
	scanning         atomic.Bool
	persistenceError string
	failedPaths      map[string]struct{}
	failedFullScan   bool
}

type fsWatcherManager struct {
	mu       sync.Mutex
	watchers map[int64]*jobWatcher
	stopped  bool
}

var jobFSWatchers = &fsWatcherManager{
	watchers: make(map[int64]*jobWatcher),
}

// resolveLocalWatchRoots converts local virtual source paths ("/本地/<rootID>/...")
// into real filesystem directories strictly confined inside fnOS authorized roots.
func resolveLocalWatchRoots(srcPaths []string) []watchLocalRoot {
	if native.AuthorizedCount() == 0 {
		return nil
	}
	emptyCfg := native.Config{}
	var out []watchLocalRoot
	seen := make(map[string]struct{})
	for _, vp := range srcPaths {
		trimmed := strings.TrimPrefix(strings.TrimSpace(vp), "/")
		seg, _, _ := strings.Cut(trimmed, "/")
		if !strings.EqualFold(seg, native.Local) {
			continue
		}
		ep, err := emptyCfg.Resolve(vp)
		if err != nil || !ep.IsLocal || ep.LocalPath == "" {
			continue
		}
		absPath := filepath.Clean(ep.LocalPath)
		st, err := os.Stat(absPath)
		if err != nil || !st.IsDir() {
			continue
		}
		if _, dup := seen[absPath]; dup {
			continue
		}
		seen[absPath] = struct{}{}
		out = append(out, watchLocalRoot{
			virtualPath: vp,
			absDir:      absPath,
		})
	}
	return out
}

func (m *fsWatcherManager) IsWatching(jobID int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.watchers[jobID]
	return ok
}

func (m *fsWatcherManager) Stop(jobID int64) {
	m.mu.Lock()
	w := m.watchers[jobID]
	delete(m.watchers, jobID)
	m.mu.Unlock()
	if w != nil {
		w.stop()
	}
}

func (m *fsWatcherManager) StopAll() {
	m.mu.Lock()
	m.stopped = true
	list := make([]*jobWatcher, 0, len(m.watchers))
	for id, w := range m.watchers {
		list = append(list, w)
		delete(m.watchers, id)
	}
	m.mu.Unlock()
	for _, w := range list {
		w.stop()
	}
}

func (m *fsWatcherManager) ResetForTest() {
	m.StopAll()
	m.mu.Lock()
	m.stopped = false
	m.mu.Unlock()
}

// SyncJob applies the durable watcher and continuous-sync lifecycle.
func (m *fsWatcherManager) SyncJob(jc *JobClient) {
	m.startSyncJob(jc)
}

func (w *jobWatcher) stop() {
	if w.cancel != nil {
		w.cancel()
	}
	if w.done != nil {
		select {
		case <-w.done:
		case <-time.After(2 * time.Second):
		}
	}
}

// watchLocalRootsPolling is a portable fallback watcher that detects file/dir
// creations, modifications, and deletions by comparing periodic snapshots.
func watchLocalRootsPolling(ctx context.Context, roots []watchLocalRoot, onEvent func(relPath string, overflow bool)) {
	type fileSig struct {
		size  int64
		mtime int64
		isDir bool
	}
	snapshot := func() map[string]fileSig {
		snap := make(map[string]fileSig)
		for _, r := range roots {
			_ = filepath.WalkDir(r.absDir, func(p string, d fs.DirEntry, err error) error {
				if err != nil || ctx.Err() != nil {
					return nil
				}
				name := d.Name()
				if p != r.absDir && (name == ".opensync-partial" || name == ".opensync-trash" || strings.HasPrefix(name, ".opensync-")) {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if p == r.absDir {
					return nil
				}
				rel, relErr := filepath.Rel(r.absDir, p)
				if relErr != nil {
					return nil
				}
				info, infoErr := d.Info()
				if infoErr != nil {
					return nil
				}
				key := r.absDir + "\x00" + filepath.ToSlash(rel)
				snap[key] = fileSig{
					size:  info.Size(),
					mtime: info.ModTime().UnixNano(),
					isDir: info.IsDir(),
				}
				return nil
			})
		}
		return snap
	}

	prev := snapshot()
	ticker := time.NewTicker(watchPollingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			curr := snapshot()
			if ctx.Err() != nil {
				return
			}
			for k, curSig := range curr {
				if prevSig, ok := prev[k]; !ok || prevSig != curSig {
					_, rel, _ := strings.Cut(k, "\x00")
					if curSig.isDir {
						onEvent("", true)
					} else {
						onEvent(rel, false)
					}
				}
			}
			for k := range prev {
				if _, ok := curr[k]; !ok {
					onEvent("", true)
				}
			}
			prev = curr
		}
	}
}
