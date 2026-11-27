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

type localWatchOptions struct {
	exclude    *excludeMatcher
	onReady    func(coversChanges bool)
	onDegraded func(reason string)
}

type jobWatcher struct {
	jobID               int64
	cancel              context.CancelFunc
	done                chan struct{}
	roots               []watchLocalRoot
	mu                  sync.Mutex
	wake                chan struct{}
	scanning            atomic.Bool
	persistenceError    string
	failedPaths         map[string]struct{}
	failedFullScan      bool
	watchDegraded       bool
	watchDegradedReason string
	basePoll            time.Duration
	effectivePoll       time.Duration
	consecutiveNoChange int
	remoteDeltaCursors  map[string]string
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

func shouldSkipWatchDir(rootAbs, dirAbs, baseName string, exclude *excludeMatcher) bool {
	if dirAbs != rootAbs && (baseName == ".opensync-partial" || baseName == ".opensync-trash" || strings.HasPrefix(baseName, ".opensync-")) {
		return true
	}
	if dirAbs == rootAbs || exclude == nil {
		return false
	}
	rel, err := filepath.Rel(rootAbs, dirAbs)
	if err != nil {
		return false
	}
	relSlash := strings.Trim(filepath.ToSlash(rel), "/")
	if relSlash == "" || relSlash == "." || strings.HasPrefix(relSlash, "../") {
		return false
	}
	return exclude.matchesListed(relSlash+"/", true)
}

const maxAdaptivePollCap = 3600 * time.Second

func computeAdaptivePollInterval(basePoll time.Duration, noChangeStreak int) time.Duration {
	if basePoll <= 0 {
		return 0
	}
	maxCap := maxAdaptivePollCap
	if basePoll > maxCap {
		maxCap = basePoll
	}
	if noChangeStreak <= 1 {
		return basePoll
	}
	shift := noChangeStreak - 1
	if shift > 16 {
		shift = 16
	}
	cur := basePoll
	for i := 0; i < shift; i++ {
		if cur >= maxCap/2 {
			return maxCap
		}
		cur *= 2
	}
	if cur > maxCap {
		return maxCap
	}
	return cur
}

func (w *jobWatcher) setWatchDegraded(degraded bool, reason string) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.watchDegraded = degraded
	w.watchDegradedReason = strings.TrimSpace(reason)
	w.mu.Unlock()
}

func (w *jobWatcher) watchDegradation() (bool, string) {
	if w == nil {
		return false, ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.watchDegraded, w.watchDegradedReason
}

func (w *jobWatcher) initPollBackoff(basePoll time.Duration) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.basePoll = basePoll
	w.effectivePoll = basePoll
	w.consecutiveNoChange = 0
	w.mu.Unlock()
}

func (w *jobWatcher) resetPollBackoffLocked(basePoll time.Duration) {
	if basePoll > 0 {
		w.basePoll = basePoll
	}
	w.consecutiveNoChange = 0
	if w.basePoll > 0 {
		w.effectivePoll = w.basePoll
	}
}

func (w *jobWatcher) resetPollBackoff(basePoll time.Duration) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.resetPollBackoffLocked(basePoll)
	w.mu.Unlock()
}

func (w *jobWatcher) advancePollBackoff(basePoll time.Duration) time.Duration {
	if w == nil {
		return basePoll
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if basePoll > 0 {
		w.basePoll = basePoll
	}
	w.consecutiveNoChange++
	w.effectivePoll = computeAdaptivePollInterval(w.basePoll, w.consecutiveNoChange)
	return w.effectivePoll
}

func (w *jobWatcher) pollStatus(fallback time.Duration) (time.Duration, int) {
	if w == nil {
		return fallback, 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	eff := w.effectivePoll
	if eff <= 0 {
		eff = fallback
	}
	return eff, w.consecutiveNoChange
}

func (w *jobWatcher) remoteCursor(root string) string {
	if w == nil {
		return ""
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.remoteDeltaCursors[root]
}

func (w *jobWatcher) setRemoteCursor(root, cursor string) {
	if w == nil || cursor == "" {
		return
	}
	w.mu.Lock()
	if w.remoteDeltaCursors == nil {
		w.remoteDeltaCursors = make(map[string]string)
	}
	w.remoteDeltaCursors[root] = cursor
	w.mu.Unlock()
}

func (m *fsWatcherManager) ResetPollBackoff(jobID int64) {
	if jobID <= 0 {
		return
	}
	m.mu.Lock()
	w := m.watchers[jobID]
	m.mu.Unlock()
	if w == nil {
		return
	}
	w.resetPollBackoff(0)
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// watchLocalRootsPolling is a portable fallback watcher that detects file/dir
// creations, modifications, and deletions by comparing periodic snapshots.
func watchLocalRootsPolling(ctx context.Context, roots []watchLocalRoot, exclude *excludeMatcher, onEvents func(paths []string, fullScan bool)) {
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
				if d.IsDir() && shouldSkipWatchDir(r.absDir, p, name, exclude) {
					return filepath.SkipDir
				}
				if p == r.absDir {
					return nil
				}
				rel, relErr := filepath.Rel(r.absDir, p)
				if relErr != nil {
					return nil
				}
				relSlash := filepath.ToSlash(rel)
				if exclude != nil && exclude.matchesListed(relSlash, d.IsDir()) {
					if d.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				info, infoErr := d.Info()
				if infoErr != nil {
					return nil
				}
				key := r.absDir + "\x00" + relSlash
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
			var changed []string
			fullScan := false
			for k, curSig := range curr {
				if prevSig, ok := prev[k]; !ok || prevSig != curSig {
					_, rel, _ := strings.Cut(k, "\x00")
					if curSig.isDir {
						fullScan = true
					} else {
						changed = append(changed, rel)
					}
				}
			}
			for k := range prev {
				if _, ok := curr[k]; !ok {
					fullScan = true
				}
			}
			if fullScan || len(changed) > 0 {
				onEvents(changed, fullScan)
			}
			prev = curr
		}
	}
}
