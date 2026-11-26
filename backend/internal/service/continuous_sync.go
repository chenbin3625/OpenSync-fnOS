package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"opensync/internal/mapper"
	"opensync/internal/native"
	"opensync/pkg/util"
)

func syncFingerprint(job map[string]interface{}, cfg native.Config) string {
	// Only include directory/storage identity fields so tuning policies
	// (filters, trash, conflictPolicy, bwlimit, compareMode, minFileAge)
	// never invalidate the sync baseline or force full re-initialization.
	definition := make(map[string]interface{})
	for _, key := range []string{"srcPath", "dstPath", "dstMap", "alistId", "method"} {
		definition[key] = job[key]
	}
	identities := make(map[string]map[string]string)
	for _, name := range extractJobStorageNamesWithDeps(job, cfg) {
		if key, ok := cfg.FindKey(name); ok {
			identity := make(map[string]string)
			for _, field := range []string{"type", "provider", "url", "host", "port", "user", "account", "tenant", "root_folder_id", "drive_id", "team_drive", "remote", "upstreams", "endpoint", "region", "namespace", "compartment"} {
				if value := cfg[key][field]; value != "" {
					identity[field] = value
				}
			}
			identities[key] = identity
		}
	}
	definition["storageIdentity"] = identities
	raw, _ := json.Marshal(definition)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func syncIntervals(extra nativeExtraFlags) (time.Duration, time.Duration, time.Duration) {
	poll, audit, debounce := extra.SyncPollIntervalSec, extra.SyncAuditIntervalMin, extra.WatchDebounceSec
	if poll == 0 {
		poll = 60
	}
	if audit == 0 {
		audit = 1440
	}
	if debounce == 0 {
		debounce = defaultWatchDebounceSec
	}
	return time.Duration(poll) * time.Second, time.Duration(audit) * time.Minute, time.Duration(max(minWatchDebounceSec, debounce)) * time.Second
}

func (m *fsWatcherManager) startSyncJob(jc *JobClient) {
	if jc == nil {
		return
	}
	id := jc.idSnapshot()
	if id <= 0 {
		return
	}
	m.Stop(id)
	job := jc.jobSnapshot()
	extra, _, err := parseNativeExtraFlags(job["extraFlags"])
	continuous := util.ToInt(job["isCron"]) == 3
	if err != nil || !jc.enabled() || jc.isClosing() || (!continuous && !extra.WatchEnable) {
		return
	}
	paths := parsePathList(job["srcPath"])
	if util.ToInt(job["method"]) == 3 {
		paths = append(paths, parsePathList(job["dstPath"])...)
	}
	roots := resolveLocalWatchRoots(paths)
	if !continuous && len(roots) == 0 {
		return
	}
	cfg, err := loadNative(util.ToInt64(job["alistId"]))
	if err != nil {
		log.Printf("sync: load storage for job %d: %v", id, err)
		return
	}
	if err := mapper.EnsureSyncState(id, syncFingerprint(job, cfg)); err != nil {
		log.Printf("sync: initialize job %d: %v", id, err)
		return
	}
	if len(roots) == 0 {
		if err := mapper.EnqueueSyncChanges(id, nil, true); err != nil {
			log.Printf("sync: queue startup reconciliation for job %d: %v", id, err)
			return
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &jobWatcher{jobID: id, cancel: cancel, done: make(chan struct{}), roots: roots, wake: make(chan struct{}, 1)}
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		cancel()
		return
	}
	m.watchers[id] = w
	m.mu.Unlock()
	go w.runSync(ctx, jc, job, extra, continuous)
}

func (w *jobWatcher) enqueue(paths []string, full bool) {
	w.mu.Lock()
	if w.failedPaths == nil {
		w.failedPaths = make(map[string]struct{})
	}
	for _, p := range paths {
		if p != "" {
			w.failedPaths[p] = struct{}{}
		}
	}
	merged := make([]string, 0, len(w.failedPaths))
	for p := range w.failedPaths {
		merged = append(merged, p)
	}
	full = full || w.failedFullScan
	if err := mapper.EnqueueSyncChanges(w.jobID, merged, full); err != nil {
		w.persistenceError = fmt.Sprintf("保存待同步变更失败：%v", err)
		w.failedFullScan = full
		log.Printf("sync: job %d persist event: %v", w.jobID, err)
	} else {
		w.persistenceError = ""
		w.failedPaths = nil
		w.failedFullScan = false
	}
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *jobWatcher) runSync(ctx context.Context, jc *JobClient, job map[string]interface{}, extra nativeExtraFlags, continuous bool) {
	defer close(w.done)
	poll, audit, debounce := syncIntervals(extra)
	// Stable-age rules must leave queued files pending until they can be copied.
	settle := max(debounce, time.Duration(util.ToInt(job["minFileAgeMinutes"]))*time.Minute)
	method := util.ToInt(job["method"])
	partial := method == 0 || method == 1 || method == 3
	exclude, include := buildJobExcludeMatcher(job), buildJobIncludeMatcher(job)
	pollRoots := parsePathList(job["srcPath"])
	if method == 3 {
		pollRoots = append(pollRoots, parsePathList(job["dstPath"])...)
	}
	followSymlinks := util.ToInt(job["followSymlinks"]) == 1
	watchCoverage := make(map[string]*atomic.Bool, len(w.roots))
	var ready, watchers sync.WaitGroup
	for _, root := range w.roots {
		covered := new(atomic.Bool)
		watchCoverage[root.virtualPath] = covered
		ready.Add(1)
		watchers.Add(1)
		go func() {
			defer watchers.Done()
			onEvents := func(paths []string, fullScan bool) {
				if ctx.Err() != nil {
					return
				}
				var changed []string
				for _, rel := range paths {
					rel = strings.TrimPrefix(path.Clean("/"+rel), "/")
					if rel == "" || rel == "." || strings.ContainsAny(rel, "\r\n") {
						continue
					}
					if exclude != nil && exclude.matchesListed(rel, false) || include != nil && !include.matchesListed(rel) {
						continue
					}
					virtual := path.Join(root.virtualPath, rel)
					// Continuous jobs reconcile queued events against the post-transfer
					// baseline. A time-based mask could also drop a user's next edit.
					if !continuous && method == 3 && bisyncEchoSuppress.ShouldSuppress(w.jobID, virtual) {
						continue
					}
					if partial {
						changed = append(changed, virtual)
					} else {
						fullScan = true
					}
				}
				if fullScan || len(changed) > 0 {
					w.enqueue(changed, fullScan)
				}
			}
			installed := false
			watchLocalRootsPlatform(ctx, []watchLocalRoot{root}, onEvents, func(coversChanges bool) {
				installed = true
				covered.Store(coversChanges)
				ready.Done()
			})
			covered.Store(false)
			if !installed {
				ready.Done()
			}
			if !continuous && ctx.Err() == nil {
				watchLocalRootsPolling(ctx, []watchLocalRoot{root}, onEvents)
			}
		}()
	}
	defer func() { w.cancel(); watchers.Wait() }()
	ready.Wait()
	// Queue reconciliation after local watch setup so it also covers the
	// installation gap, even if a manual run finished during setup.
	if len(w.roots) > 0 && ctx.Err() == nil {
		w.enqueue(nil, true)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-w.wake:
		}
		if ctx.Err() != nil || !jc.enabled() || jc.isClosing() {
			return
		}
		w.mu.Lock()
		persistErr := w.persistenceError
		w.mu.Unlock()
		if persistErr != "" {
			w.enqueue(nil, true)
			w.mu.Lock()
			persistErr = w.persistenceError
			w.mu.Unlock()
			if persistErr != "" {
				continue
			}
		}
		state, err := mapper.GetSyncState(w.jobID)
		if err != nil {
			continue
		}
		now := time.Now()
		if !JobInTimeWindow(job, now) || jc.isBusy() || now.Unix() < state.RetryAt {
			continue
		}
		if continuous && (state.LastPoll == 0 || now.Sub(time.Unix(state.LastPoll, 0)) >= poll) {
			var roots []string
			for _, root := range pollRoots {
				covered := watchCoverage[root]
				if followSymlinks || covered == nil || !covered.Load() {
					roots = append(roots, root)
				}
			}
			if len(roots) > 0 {
				w.scanning.Store(true)
				err = w.scanSyncRoots(ctx, job, roots, partial)
				w.scanning.Store(false)
				if ctx.Err() != nil {
					return
				}
				if err != nil {
					_ = mapper.RecordSyncError(w.jobID, err.Error())
					continue
				}
				if err = mapper.MarkSyncPoll(w.jobID); err != nil {
					continue
				}
				state, err = mapper.GetSyncState(w.jobID)
				if err != nil {
					continue
				}
			}
		}
		if continuous && (state.LastAudit == 0 || now.Sub(time.Unix(state.LastAudit, 0)) >= audit) {
			if err = mapper.RequestSyncAudit(w.jobID); err != nil {
				continue
			}
			state, err = mapper.GetSyncState(w.jobID)
			if err != nil {
				continue
			}
		}
		if !state.Pending || now.Sub(time.Unix(state.UpdatedAt, 0)) < settle {
			continue
		}
		if !jc.tryMarkDoing() {
			continue
		}
		go jc.runMarkedJobWithOptions(markedJobRunOptions{syncChangedFiles: state.Paths, syncPartial: !state.FullScan && partial, syncGeneration: state.Generation})
	}
}

func (w *jobWatcher) scanSyncRoots(ctx context.Context, job map[string]interface{}, roots []string, partial bool) error {
	cfg, err := loadNative(util.ToInt64(job["alistId"]))
	if err != nil {
		return err
	}
	if err := mapper.EnsureSyncState(w.jobID, syncFingerprint(job, cfg)); err != nil {
		return err
	}
	jobWantsHash := util.ToInt(job["compareMode"]) == 2
	if !jobWantsHash && util.ToInt(job["compareMode"]) == 0 {
		for _, src := range parsePathList(job["srcPath"]) {
			srcEp, sErr := cfg.Resolve(src)
			if sErr != nil {
				continue
			}
			for _, dst := range parsePathList(job["dstPath"]) {
				dstEp, dErr := cfg.Resolve(dst)
				if dErr != nil {
					continue
				}
				if native.EffectiveCompareMode(srcEp, dstEp, 0) == 2 {
					jobWantsHash = true
					break
				}
			}
			if jobWantsHash {
				break
			}
		}
	}
	for _, root := range roots {
		ep, err := cfg.Resolve(root)
		if err != nil {
			return err
		}
		// Avoid hashing entire local disks during fallback polling.
		includeHash := jobWantsHash && ep.SupportsNativeMD5() && !ep.IsLocal
		scanTimeout := 5 * time.Minute
		if ep.IsLocal {
			scanTimeout = 15 * time.Minute
		}
		scanCtx, cancel := context.WithTimeout(ctx, scanTimeout)
		entries, exists, err := cfg.ListTree(scanCtx, root, native.TreeOptions{
			FastList: util.ToInt(job["fastList"]) == 1, FollowSymlinks: util.ToInt(job["followSymlinks"]) == 1,
			IncludeHash: includeHash, ProxyURL: globalNativeProxyURL(),
		})
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			return fmt.Errorf("检测目录 %s 失败：%w", root, err)
		}
		if !exists {
			return fmt.Errorf("检测目录 %s 不存在或不可访问；已保留待同步记录", root)
		}
		files := make([]mapper.SyncFile, 0, len(entries))
		exclude, include := buildJobExcludeMatcher(job), buildJobIncludeMatcher(job)
		for _, e := range entries {
			if exclude != nil && exclude.matchesListed(e.RelPath, e.IsDir) {
				continue
			}
			if !e.IsDir && (include != nil && !include.matchesListed(e.RelPath)) {
				continue
			}
			files = append(files, mapper.SyncFile{Path: e.RelPath, Size: e.Size, ModTime: e.ModTime, Hash: e.MD5, IsDir: e.IsDir})
		}
		if err := mapper.ObserveSyncTree(w.jobID, root, files, partial); err != nil {
			return err
		}
	}
	return nil
}

// addSyncStatus exposes durable progress separately from an individual run.
func addSyncStatus(row map[string]interface{}, s mapper.SyncState, stateErr error, conflictCount int, client *JobClient) {
	jobID := util.ToInt64(row["id"])
	continuous := util.ToInt(row["isCron"]) == 3
	extra, _, _ := parseNativeExtraFlags(row["extraFlags"])
	if !continuous && !extra.WatchEnable {
		return
	}
	status := "monitoring"
	message := s.Error
	if stateErr != nil {
		status = "error"
		message = "无法读取持续同步状态"
	}
	if s.LastSuccess > 0 && !s.Pending && message == "" {
		status = "synced"
	}
	if s.Pending {
		status = "pending"
	}
	if !JobInTimeWindow(row, time.Now()) {
		status = "waiting_window"
	}
	if message != "" {
		status = "error"
	}
	if util.ToInt(row["method"]) != 3 || jobID <= 0 {
		conflictCount = 0
	}
	m := jobFSWatchers
	m.mu.Lock()
	w := m.watchers[jobID]
	m.mu.Unlock()
	if w != nil {
		w.mu.Lock()
		persistErr := w.persistenceError
		w.mu.Unlock()
		if persistErr != "" {
			status = "error"
			message = persistErr
		}
		if w.scanning.Load() {
			status = "scanning"
		}
	} else if util.ToInt(row["enable"]) == 1 && stateErr == nil {
		status = "error"
		message = "同步监控未启动，请检查存储与数据库后重新启用任务"
	}
	if client != nil && client.isBusy() {
		status = "syncing"
		if t := client.currentTask(); t != nil {
			if t.FirstSync.Load() == 0 {
				status = "scanning"
			}
			if t.isPaused() {
				status = "paused"
			}
		}
	}
	if util.ToInt(row["enable"]) != 1 {
		status = "paused"
	}
	pending := len(s.Paths)
	if s.FullScan {
		pending = -1
	}
	poll, audit, debounce := syncIntervals(extra)
	row["syncStatus"] = map[string]interface{}{
		"state":            status,
		"pendingChanges":   pending,
		"conflictCount":    conflictCount,
		"lastSuccess":      s.LastSuccess,
		"lastPoll":         s.LastPoll,
		"nextPoll":         s.LastPoll + int64(poll/time.Second),
		"error":            message,
		"failures":         s.Failures,
		"retryAt":          s.RetryAt,
		"direction":        extra.SyncDirection,
		"pollIntervalSec":  int(poll / time.Second),
		"auditIntervalMin": int(audit / time.Minute),
		"debounceSec":      int(debounce / time.Second),
	}
}
