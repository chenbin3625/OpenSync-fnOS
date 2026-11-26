package service

import (
	"context"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"opensync/internal/native"
	"opensync/pkg/util"
)

const (
	defaultWatchDebounceSec = 30
	minWatchDebounceSec     = 2
	maxWatchChangedPaths    = 200
	watchPollingInterval    = 5 * time.Second
)

type watchLocalRoot struct {
	virtualPath string
	absDir      string
}

type jobWatcher struct {
	jobID   int64
	cancel  context.CancelFunc
	done    chan struct{}
	roots   []watchLocalRoot
	pending bool
	mu      sync.Mutex
	timer   *time.Timer
	changed map[string]struct{}
	overflow bool
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

// SyncJob starts, restarts, or stops the filesystem watcher for jc based on its
// current configuration and enabled state.
func (m *fsWatcherManager) SyncJob(jc *JobClient) {
	if jc == nil {
		return
	}
	jobID := jc.idSnapshot()
	if jobID <= 0 {
		return
	}
	job := jc.jobSnapshot()
	if job == nil || !jc.enabled() || jc.isClosing() {
		m.Stop(jobID)
		return
	}
	extra, _, err := parseNativeExtraFlags(job["extraFlags"])
	if err != nil || !extra.WatchEnable {
		m.Stop(jobID)
		return
	}
	srcPaths := parsePathList(job["srcPath"])
	roots := resolveLocalWatchRoots(srcPaths)
	if len(roots) == 0 {
		m.Stop(jobID)
		return
	}
	debounceSec := extra.WatchDebounceSec
	if debounceSec <= 0 {
		debounceSec = defaultWatchDebounceSec
	}
	if debounceSec < minWatchDebounceSec {
		debounceSec = minWatchDebounceSec
	}

	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	old := m.watchers[jobID]
	delete(m.watchers, jobID)
	ctx, cancel := context.WithCancel(context.Background())
	w := &jobWatcher{
		jobID:   jobID,
		cancel:  cancel,
		done:    make(chan struct{}),
		roots:   roots,
		changed: make(map[string]struct{}),
	}
	m.watchers[jobID] = w
	m.mu.Unlock()

	if old != nil {
		old.stop()
	}

	excludeM := buildJobExcludeMatcher(job)
	includeM := buildJobIncludeMatcher(job)
	multiRoot := len(srcPaths) > 1
	method := util.ToInt(job["method"])

	go func() {
		defer close(w.done)
		debounceDur := time.Duration(debounceSec) * time.Second
		onEvent := func(relPath string, overflow bool) {
			if ctx.Err() != nil {
				return
			}
			if relPath != "" {
				relPath = strings.TrimPrefix(path.Clean("/"+relPath), "/")
				if relPath == "" || relPath == "." || strings.HasPrefix(relPath, "../") {
					return
				}
				if excludeM != nil && excludeM.matchesListed(relPath, false) {
					return
				}
				if includeM != nil && !includeM.matchesListed(relPath) {
					return
				}
			}
			w.mu.Lock()
			if overflow || multiRoot || method != 0 || relPath == "" {
				w.overflow = true
			} else if len(w.changed) >= maxWatchChangedPaths {
				w.overflow = true
			} else {
				w.changed[relPath] = struct{}{}
			}
			w.pending = true
			if w.timer != nil {
				w.timer.Reset(debounceDur)
			} else {
				w.timer = time.AfterFunc(debounceDur, func() {
					w.flushTrigger(ctx, jc, debounceDur)
				})
			}
			w.mu.Unlock()
		}
		watchLocalRootsPlatform(ctx, roots, onEvent)
	}()
	log.Printf("fs_watcher: started watching %d local root(s) for job %d (debounce=%ds)", len(roots), jobID, debounceSec)
}

func (w *jobWatcher) flushTrigger(ctx context.Context, jc *JobClient, debounceDur time.Duration) {
	if ctx.Err() != nil {
		return
	}
	w.mu.Lock()
	if !w.pending {
		w.timer = nil
		w.mu.Unlock()
		return
	}
	overflow := w.overflow
	var paths []string
	if !overflow && len(w.changed) > 0 {
		paths = make([]string, 0, len(w.changed))
		for p := range w.changed {
			paths = append(paths, p)
		}
		sort.Strings(paths)
	}
	w.changed = make(map[string]struct{})
	w.overflow = false
	w.pending = false
	w.timer = nil
	w.mu.Unlock()

	if !jc.enabled() || jc.isClosing() {
		return
	}
	if !JobInTimeWindow(jc.jobSnapshot(), time.Now()) {
		return
	}
	if !jc.DoWatchSync(paths) {
		// Job is busy right now: requeue a follow-up trigger after debounceDur so
		// edits made during an active run are not lost.
		w.mu.Lock()
		w.pending = true
		if overflow || len(paths) == 0 {
			w.overflow = true
		} else {
			for _, p := range paths {
				if len(w.changed) >= maxWatchChangedPaths {
					w.overflow = true
					break
				}
				w.changed[p] = struct{}{}
			}
		}
		if w.timer == nil && ctx.Err() == nil {
			w.timer = time.AfterFunc(debounceDur, func() {
				w.flushTrigger(ctx, jc, debounceDur)
			})
		}
		w.mu.Unlock()
	}
}

func (w *jobWatcher) stop() {
	w.mu.Lock()
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.mu.Unlock()
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
