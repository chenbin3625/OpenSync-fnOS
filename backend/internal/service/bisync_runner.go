package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"opensync/internal/mapper"
	"opensync/internal/msg"
	"opensync/internal/native"
	"opensync/pkg/util"
)

const (
	bisyncEchoSuppressTTL   = 6 * time.Second
	bisyncTombstoneKeepDays = 30
)

// bisyncEchoSuppressor suppresses local inotify echo events caused by our own
// B->A downloads, moves, mkdirs, or deletes during bidirectional sync.
type bisyncEchoSuppressor struct {
	mu    sync.Mutex
	items map[int64]map[string]int64 // jobID -> virtualPath -> expiresAtUnixNano
}

var bisyncEchoSuppress = &bisyncEchoSuppressor{
	items: make(map[int64]map[string]int64),
}

func (s *bisyncEchoSuppressor) Mark(jobID int64, virtualPath string) {
	if jobID <= 0 || virtualPath == "" {
		return
	}
	clean := path.Clean(virtualPath)
	exp := time.Now().Add(bisyncEchoSuppressTTL).UnixNano()
	s.mu.Lock()
	m := s.items[jobID]
	if m == nil {
		m = make(map[string]int64)
		s.items[jobID] = m
	}
	m[clean] = exp
	s.mu.Unlock()
}

func (s *bisyncEchoSuppressor) ShouldSuppress(jobID int64, virtualPath string) bool {
	if jobID <= 0 || virtualPath == "" {
		return false
	}
	clean := path.Clean(virtualPath)
	now := time.Now().UnixNano()
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.items[jobID]
	if m == nil {
		return false
	}
	exp, ok := m[clean]
	if !ok {
		return false
	}
	if now > exp {
		delete(m, clean)
		return false
	}
	return true
}

func (s *bisyncEchoSuppressor) ClearJob(jobID int64) {
	s.mu.Lock()
	delete(s.items, jobID)
	s.mu.Unlock()
}

func bisyncSideLabel(ep native.Endpoint, fallback string) string {
	if ep.IsLocal {
		return "本地"
	}
	if name := strings.TrimSpace(ep.RemoteName); name != "" {
		return name
	}
	return fallback
}

func buildBisyncEndpointCaps(ep native.Endpoint, side string) BisyncEndpointCaps {
	fallback := "左端"
	if side == "B" {
		fallback = "右端"
	}
	return BisyncEndpointCaps{
		Side:              side,
		Label:             bisyncSideLabel(ep, fallback),
		IsLocal:           ep.IsLocal,
		CaseInsensitive:   ep.IsCaseInsensitive(),
		UnreliableModTime: ep.UnreliableModTime,
		SupportsMD5:       ep.SupportsNativeMD5(),
		Endpoint:          ep,
	}
}

func bisyncStorageSig(ep native.Endpoint) string {
	raw := strings.Join([]string{
		ep.Backend,
		ep.Vendor,
		ep.Identity,
		ep.BackingIdentity,
		ep.CryptScope,
	}, "|")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:12])
}

func parseModifyWindowSec(raw string) int64 {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 2
	}
	if d, err := time.ParseDuration(trimmed); err == nil && d >= 0 {
		sec := int64(d / time.Second)
		if sec <= 0 {
			return 1
		}
		return sec
	}
	return 2
}

func bisyncVirtualPath(root, rel string) string {
	rootClean := strings.TrimRight(normalizeDirPath(root), "/")
	relClean := cleanBisyncRel(rel)
	if relClean == "" {
		return rootClean
	}
	return rootClean + "/" + relClean
}

func isBisyncInternalPath(rel, trashDir string) bool {
	if rel == "" {
		return true
	}
	if trashDir != "" && (rel == trashDir || strings.HasPrefix(rel, trashDir+"/")) {
		return true
	}
	if rel == native.PartialDirName || strings.HasPrefix(rel, native.PartialDirName+"/") {
		return true
	}
	base := path.Base(rel)
	return strings.HasPrefix(base, ".opensync-") && strings.HasSuffix(base, ".partial")
}

func bisyncNodeMatchesFilters(
	rel string,
	isDir bool,
	size int64,
	modTime int64,
	job map[string]interface{},
	spec *excludeMatcher,
	includeM *includeMatcher,
	now time.Time,
) bool {
	if spec != nil && spec.matchesListed(rel, isDir) {
		return false
	}
	if isDir {
		return true
	}
	if includeM != nil && !includeM.matchesListed(rel) {
		return false
	}
	minSize := util.ToInt64(job["minFileSize"])
	maxSize := util.ToInt64(job["maxFileSize"])
	if minSize > 0 && size < minSize {
		return false
	}
	if maxSize > 0 && size > maxSize {
		return false
	}
	minAgeMin := util.ToInt(job["minFileAgeMinutes"])
	if minAgeMin > 0 && modTime > 0 && now.Sub(time.Unix(modTime, 0)) < time.Duration(minAgeMin)*time.Minute {
		return false
	}
	olderDays := util.ToInt(job["olderThanDays"])
	if olderDays > 0 && modTime > 0 && now.Sub(time.Unix(modTime, 0)) < time.Duration(olderDays)*24*time.Hour {
		return false
	}
	newerDays := util.ToInt(job["newerThanDays"])
	if newerDays > 0 && modTime > 0 && now.Sub(time.Unix(modTime, 0)) > time.Duration(newerDays)*24*time.Hour {
		return false
	}
	return true
}

// scanBisyncTree scans one root into a normalized map of BisyncNodes and a set of excluded paths.
func scanBisyncTree(
	ctx context.Context,
	cfg native.Config,
	rootPath string,
	job map[string]interface{},
	spec *excludeMatcher,
	includeM *includeMatcher,
	wantHash bool,
	now time.Time,
	ctrl *native.RCController,
) (map[string]BisyncNode, map[string]bool, int, error) {
	extra, _, _ := parseNativeExtraFlags(job["extraFlags"])
	trashDir := jobTrashDir(job)
	entries, exists, err := cfg.ListTree(ctx, strings.TrimRight(normalizeDirPath(rootPath), "/"), native.TreeOptions{
		FastList:       util.ToInt(job["fastList"]) == 1,
		FollowSymlinks: util.ToInt(job["followSymlinks"]) == 1,
		IncludeHash:    wantHash || extra.TrackRenames,
		ProxyURL:       globalNativeProxyURL(),
		Controller:     ctrl,
	})
	if err != nil {
		return nil, nil, 0, err
	}
	if !exists {
		return nil, nil, 0, fmt.Errorf("同步目录 %s 不存在或不可访问", rootPath)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].RelPath < entries[j].RelPath
	})
	nodes := make(map[string]BisyncNode, len(entries))
	excluded := make(map[string]bool)
	excludedDirs := make([]string, 0)
	isUnderExcluded := func(rel string) bool {
		for _, pref := range excludedDirs {
			if strings.HasPrefix(rel, pref) {
				return true
			}
		}
		return false
	}
	rawFileCount := 0
	for _, e := range entries {
		rel := cleanBisyncRel(e.RelPath)
		if rel == "" || isBisyncInternalPath(rel, trashDir) {
			continue
		}
		if !e.IsDir {
			rawFileCount++
		}
		if isUnderExcluded(rel) {
			excluded[rel] = true
			continue
		}
		if !bisyncNodeMatchesFilters(rel, e.IsDir, e.Size, e.ModTime, job, spec, includeM, now) {
			excluded[rel] = true
			if e.IsDir {
				excludedDirs = append(excludedDirs, rel+"/")
			}
			continue
		}
		// Synthesize intermediate directories for flat object stores (S3/OSS/B2).
		dir := path.Dir(rel)
		for dir != "." && dir != "/" && dir != "" {
			if _, ok := nodes[dir]; !ok {
				nodes[dir] = BisyncNode{RelPath: dir, IsDir: true}
			}
			dir = path.Dir(dir)
		}
		nodes[rel] = BisyncNode{
			RelPath:  rel,
			IsDir:    e.IsDir,
			Size:     e.Size,
			ModTime:  e.ModTime,
			Hash:     strings.ToLower(strings.TrimSpace(e.MD5)),
			Inode:    e.Inode,
			ObjectID: e.ObjectID,
		}
	}
	return nodes, excluded, rawFileCount, nil
}

// checkBisyncRootHealth validates endpoint identity and protects against
// unmounted / swapped disks or unexpected empty-root drops.
func checkBisyncRootHealth(
	jobID int64,
	cfg native.Config,
	rootA, rootB string,
	epA, epB native.Endpoint,
	countA, countB int,
	isPartial bool,
) (metaA, metaB *mapper.BisyncRootMeta, resetOccurred bool, err error) {
	sigA := bisyncStorageSig(epA)
	sigB := bisyncStorageSig(epB)
	metaA, err = mapper.GetBisyncRootMeta(jobID, "A")
	if err != nil {
		return nil, nil, false, err
	}
	metaB, err = mapper.GetBisyncRootMeta(jobID, "B")
	if err != nil {
		return nil, nil, false, err
	}

	// If root path or underlying storage identity changed, cleanly reset the
	// 3-way baseline so the engine smoothly adopts matching files without
	// forcing manual intervention.
	if (metaA != nil && (metaA.RootPath != rootA || metaA.StorageSig != sigA)) ||
		(metaB != nil && (metaB.RootPath != rootB || metaB.StorageSig != sigB)) {
		if err := mapper.ClearBisyncJobState(jobID); err != nil {
			return nil, nil, false, err
		}
		return nil, nil, true, nil
	}

	// Local filesystem fsid drop-disk / swapped-mount check.
	if epA.IsLocal && metaA != nil && (metaA.FsidHi != 0 || metaA.FsidLo != 0) {
		hi, lo, ok := cfg.LocalFilesystemID(rootA)
		if !ok || hi != metaA.FsidHi || lo != metaA.FsidLo {
			return nil, nil, false, fmt.Errorf("检测到目录 %s 所在磁盘或挂载点发生变化，已中止同步以防误删", rootA)
		}
	}
	if epB.IsLocal && metaB != nil && (metaB.FsidHi != 0 || metaB.FsidLo != 0) {
		hi, lo, ok := cfg.LocalFilesystemID(rootB)
		if !ok || hi != metaB.FsidHi || lo != metaB.FsidLo {
			return nil, nil, false, fmt.Errorf("检测到目录 %s 所在磁盘或挂载点发生变化，已中止同步以防误删", rootB)
		}
	}

	// Empty-root drop protection (full scans only).
	if !isPartial {
		if metaA != nil && metaA.LastKnownCount >= 10 && countA == 0 && countB >= 10 {
			return nil, nil, false, fmt.Errorf("检测到目录 %s 异常变空（上次已知 %d 个文件），已暂停删除传播以保护数据", rootA, metaA.LastKnownCount)
		}
		if metaB != nil && metaB.LastKnownCount >= 10 && countB == 0 && countA >= 10 {
			return nil, nil, false, fmt.Errorf("检测到目录 %s 异常变空（上次已知 %d 个文件），已暂停删除传播以保护数据", rootB, metaB.LastKnownCount)
		}
	}

	return metaA, metaB, false, nil
}

func saveBisyncRootMeta(jobID int64, cfg native.Config, rootA, rootB string, epA, epB native.Endpoint, countA, countB int, prevA, prevB *mapper.BisyncRootMeta) {
	now := time.Now().Unix()
	build := func(side, root string, ep native.Endpoint, count int, prev *mapper.BisyncRootMeta) mapper.BisyncRootMeta {
		m := mapper.BisyncRootMeta{
			JobID:          jobID,
			Side:           side,
			RootPath:       root,
			StorageSig:     bisyncStorageSig(ep),
			LastKnownCount: count,
			InitializedAt:  now,
		}
		if prev != nil {
			if prev.InitializedAt > 0 {
				m.InitializedAt = prev.InitializedAt
			}
			m.RemoteCursor = prev.RemoteCursor
			if count < 0 {
				m.LastKnownCount = prev.LastKnownCount
			}
		}
		if ep.IsLocal {
			if hi, lo, ok := cfg.LocalFilesystemID(root); ok {
				m.FsidHi, m.FsidLo = hi, lo
			}
		}
		return m
	}
	_ = mapper.UpsertBisyncRootMeta(build("A", rootA, epA, countA, prevA))
	_ = mapper.UpsertBisyncRootMeta(build("B", rootB, epB, countB, prevB))
}

func dirEntryToBisyncNode(rel string, d *native.DirEntry) BisyncNode {
	return BisyncNode{
		RelPath:  rel,
		IsDir:    d.IsDir,
		Size:     d.Size,
		ModTime:  d.ModTime,
		Hash:     strings.ToLower(strings.TrimSpace(d.MD5)),
		Inode:    d.Inode,
		ObjectID: d.ObjectID,
	}
}

func matchesBisyncSnapshot(current BisyncNode, expected BisyncNode) bool {
	if current.IsDir != expected.IsDir {
		return false
	}
	if current.IsDir {
		return true
	}
	return current.Size == expected.Size && current.ModTime == expected.ModTime &&
		(expected.Hash == "" || strings.EqualFold(current.Hash, expected.Hash)) &&
		(expected.Inode == 0 || current.Inode == expected.Inode) &&
		(expected.ObjectID == "" || current.ObjectID == expected.ObjectID)
}

// statAndCommitBisyncEntry performs read-after-write metadata verification on
// both sides and atomically commits the per-file SQLite baseline row.
func statAndCommitBisyncEntry(
	ctx context.Context,
	cfg native.Config,
	jobID int64,
	rootA, rootB, rel string,
	expectedA, expectedB *BisyncNode,
	wantHashA, wantHashB bool,
	sessOpts native.SessionOptions,
) error {
	stA, okA, errA := cfg.StatEntry(ctx, bisyncVirtualPath(rootA, rel), wantHashA || (expectedA != nil && expectedA.Hash != ""), sessOpts)
	stB, okB, errB := cfg.StatEntry(ctx, bisyncVirtualPath(rootB, rel), wantHashB || (expectedB != nil && expectedB.Hash != ""), sessOpts)
	entry, err := verifiedBisyncEntry(jobID, rel, stA, stB, expectedA, expectedB, okA, okB, errA, errB)
	if err != nil {
		return err
	}
	return mapper.UpsertBisyncEntry(entry)
}

func verifiedBisyncEntry(jobID int64, rel string, stA, stB *native.DirEntry, expectedA, expectedB *BisyncNode, okA, okB bool, errA, errB error) (mapper.BisyncEntry, error) {
	if err := errors.Join(errA, errB); err != nil {
		return mapper.BisyncEntry{}, fmt.Errorf("核对传输结果 %q: %w", rel, err)
	}
	if !okA || !okB || stA == nil || stB == nil {
		return mapper.BisyncEntry{}, fmt.Errorf("核对传输结果 %q: 一侧文件缺失", rel)
	}
	nodeA, nodeB := dirEntryToBisyncNode(rel, stA), dirEntryToBisyncNode(rel, stB)
	if nodeA.IsDir || nodeB.IsDir || nodeA.Size != nodeB.Size ||
		(nodeA.Hash != "" && nodeB.Hash != "" && !strings.EqualFold(nodeA.Hash, nodeB.Hash)) ||
		(expectedA != nil && !matchesBisyncSnapshot(nodeA, *expectedA)) ||
		(expectedB != nil && !matchesBisyncSnapshot(nodeB, *expectedB)) {
		return mapper.BisyncEntry{}, fmt.Errorf("核对传输结果 %q: 文件已变更或两侧内容不一致", rel)
	}
	return makeBisyncEntry(jobID, rel, false, nodeA, nodeB, time.Now().Unix()), nil
}

func listBisyncVerificationDir(ctx context.Context, cfg native.Config, root, dir string, rels []string, wantHash bool, opts native.SessionOptions) map[string]*native.DirEntry {
	entries, exists, err := cfg.ListDirEntriesWithHash(ctx, bisyncVirtualPath(root, dir), wantHash, opts)
	if err != nil || !exists {
		return nil
	}
	matches := make(map[string]*native.DirEntry, len(rels))
	for _, rel := range rels {
		matches[path.Base(rel)] = nil
	}
	seen := make(map[string]bool, len(rels))
	for i := range entries {
		name := entries[i].Name
		if _, wanted := matches[name]; wanted {
			if seen[name] {
				matches[name] = nil // Let exact stat resolve ambiguous names.
			} else {
				entry := entries[i]
				matches[name] = &entry
				seen[name] = true
			}
		}
	}
	return matches
}

// runNativeBisync executes bidirectional sync (method=3) using the SQLite
// 3-Way Merge state machine (Base vs Local/A vs Remote/B).
func (jt *JobTask) runNativeBisync() error {
	ctx := jt.context()
	cfg := jt.nativeConfig
	job := jt.Job
	jobID := util.ToInt64(job["id"])

	srcPaths := parsePathList(job["srcPath"])
	dstPaths := parsePathList(job["dstPath"])
	if len(srcPaths) != 1 || len(dstPaths) != 1 {
		return errors.New("双向同步仅支持单个源目录与单个目标目录")
	}
	rootA := normalizeDirPath(srcPaths[0])
	rootB := normalizeDirPath(dstPaths[0])

	epA, epB, err := cfg.Pair(strings.TrimRight(rootA, "/"), strings.TrimRight(rootB, "/"))
	if err != nil {
		return err
	}
	if epA.ReadOnly || epB.ReadOnly {
		return errors.New("双向同步的两端都必须支持读写，不能使用只读存储（如 HTTP / Archive）")
	}

	capsA := buildBisyncEndpointCaps(epA, "A")
	capsB := buildBisyncEndpointCaps(epB, "B")
	effectiveCompare := nativePairEffectiveCompareMode(cfg, rootA, rootB, util.ToInt(job["compareMode"]))
	wantHash := effectiveCompare == 2
	wantHashA := wantHash && capsA.SupportsMD5
	wantHashB := wantHash && capsB.SupportsMD5
	extra, _, _ := parseNativeExtraFlags(job["extraFlags"])
	spec := buildJobExcludeMatcher(job)
	includeM := buildJobIncludeMatcher(job)
	sessOpts := native.SessionOptions{ProxyURL: globalNativeProxyURL()}
	now := time.Now()

	// Check if we can use the fast single-path delta path (local inotify delta).
	metaA, metaB, resetOccurred, healthErr := checkBisyncRootHealth(jobID, cfg, rootA, rootB, epA, epB, -1, -1, true)
	if healthErr != nil {
		return healthErr
	}

	var (
		baseMap   map[string]mapper.BisyncEntry
		snapA     map[string]BisyncNode
		snapB     map[string]BisyncNode
		excluded  map[string]bool
		onlyPaths []string
		countA    = -1
		countB    = -1
		usedFast  = false
	)

	if jt.SyncPartial && len(jt.SyncChangedFiles) > 0 && !capsA.CaseInsensitive && !capsB.CaseInsensitive &&
		!resetOccurred && metaA != nil && metaB != nil && metaA.InitializedAt > 0 && metaB.InitializedAt > 0 {
		deltaRels := extractBisyncDeltaRels(jt.SyncChangedFiles, rootA, rootB, jobTrashDir(job))
		if len(deltaRels) > 0 && len(deltaRels) <= 64 {
			if fBase, fSnapA, fSnapB, fExcl, ok := probeBisyncDeltaPaths(ctx, cfg, jobID, rootA, rootB, deltaRels, job, spec, includeM, wantHashA, wantHashB, now, sessOpts); ok {
				baseMap, snapA, snapB, excluded, onlyPaths = fBase, fSnapA, fSnapB, fExcl, deltaRels
				usedFast = true
			}
		}
	}

	if !usedFast {
		var errA, errB error
		var exclA, exclB map[string]bool
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			snapA, exclA, countA, errA = scanBisyncTree(ctx, cfg, rootA, job, spec, includeM, wantHashA, now, jt.controller())
		}()
		go func() {
			defer wg.Done()
			snapB, exclB, countB, errB = scanBisyncTree(ctx, cfg, rootB, job, spec, includeM, wantHashB, now, jt.controller())
		}()
		wg.Wait()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errA != nil {
			return fmt.Errorf("扫描目录 %s 失败: %w", rootA, errA)
		}
		if errB != nil {
			return fmt.Errorf("扫描目录 %s 失败: %w", rootB, errB)
		}

		// Re-run health guard with full counts to catch unexpected empty-root drops.
		metaA, metaB, _, healthErr = checkBisyncRootHealth(jobID, cfg, rootA, rootB, epA, epB, countA, countB, false)
		if healthErr != nil {
			return healthErr
		}

		baseMap, err = mapper.ListBisyncEntries(jobID)
		if err != nil {
			return err
		}
		excluded = make(map[string]bool, len(exclA)+len(exclB))
		for k, v := range exclA {
			if v {
				excluded[k] = true
			}
		}
		for k, v := range exclB {
			if v {
				excluded[k] = true
			}
		}
		// Mark any existing synced baseline entries that now match exclusion rules.
		for rel, b := range baseMap {
			if b.State != mapper.BisyncStateSynced {
				continue
			}
			if !bisyncNodeMatchesFilters(rel, b.IsDir, b.ASize, b.AModTime, job, spec, includeM, now) {
				excluded[rel] = true
			}
		}
	}

	plan := BuildBisyncPlan(BisyncPlanInput{
		JobID:            jobID,
		Now:              now,
		CompareMode:      effectiveCompare,
		ModifyWindowSec:  parseModifyWindowSec(util.StringValue(job["modifyWindow"])),
		ConflictPolicy:   util.ToInt(job["conflictPolicy"]),
		MaxDeletePercent: extra.BisyncMaxDelete,
		ForceDelete:      extra.BisyncForce,
		CapsA:            capsA,
		CapsB:            capsB,
		Base:             baseMap,
		SnapA:            snapA,
		SnapB:            snapB,
		Excluded:         excluded,
		OnlyPaths:        onlyPaths,
	})

	// Estimate planned copy files & bytes for realtime progress bar.
	var estFiles, estBytes int64
	for _, tr := range plan.CopyA2B {
		estFiles++
		estBytes += max(0, tr.Size)
	}
	for _, tr := range plan.CopyB2A {
		estFiles++
		estBytes += max(0, tr.Size)
	}
	jt.initNativePlanned(estFiles, estBytes)
	jt.ScanFinish.Store(true)
	jt.notifyProgressChange()

	var runErrs []error

	// Phase 1: Batch baseline bookkeeping (zero-cost adopt, newly ignored, already-both-deleted tombstones).
	if len(plan.Adopt) > 0 {
		if err := mapper.UpsertBisyncEntriesBatch(plan.Adopt); err != nil {
			runErrs = append(runErrs, err)
		}
	}
	if len(plan.MarkIgnored) > 0 {
		if err := mapper.UpsertBisyncEntriesBatch(plan.MarkIgnored); err != nil {
			runErrs = append(runErrs, err)
		}
	}
	if len(plan.MarkTombstone) > 0 {
		tombEntries := make([]mapper.BisyncEntry, 0, len(plan.MarkTombstone))
		nowUnix := time.Now().Unix()
		for _, ref := range plan.MarkTombstone {
			tombEntries = append(tombEntries, mapper.BisyncEntry{
				JobID:       jobID,
				RelPath:     ref.RelPath,
				NormKey:     mapper.NormalizeBisyncKey(ref.RelPath),
				IsDir:       ref.IsDir,
				State:       mapper.BisyncStateTombstone,
				TombstoneAt: nowUnix,
			})
		}
		if err := mapper.UpsertBisyncEntriesBatch(tombEntries); err != nil {
			return err
		}
	}

	// Phase 2: Handle conflicts (invalid_name, content, case, type).
	extraCopyA2B := make([]BisyncTransfer, 0)
	extraCopyB2A := make([]BisyncTransfer, 0)
	for _, cf := range plan.Conflicts {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := jt.controller().WaitIfPaused(ctx); err != nil {
			return err
		}
		var aSize, aMod, bSize, bMod int64
		if cf.NodeA != nil {
			aSize, aMod = cf.NodeA.Size, cf.NodeA.ModTime
		}
		if cf.NodeB != nil {
			bSize, bMod = cf.NodeB.Size, cf.NodeB.ModTime
		}

		if cf.ConflictType == mapper.BisyncConflictInvalidName {
			_, _ = mapper.UpsertBisyncConflict(mapper.BisyncConflict{
				JobID:        jobID,
				RelPath:      cf.RelPath,
				ConflictType: cf.ConflictType,
				ASize:        aSize,
				AModTime:     aMod,
				BSize:        bSize,
				BModTime:     bMod,
				Detail:       cf.Detail,
				CreatedAt:    time.Now().Unix(),
			})
			detail := cf.Detail
			jt.CopyHook(bisyncVirtualPath(rootA, path.Dir(cf.RelPath)), bisyncVirtualPath(rootB, path.Dir(cf.RelPath)), path.Base(cf.RelPath), max(aSize, bSize), "", taskStatusFailed, &detail, taskItemFile, taskItemTypeCopy, time.Now().Unix())
			continue
		}

		// Rename the losing version before writing anything at the original path.
		renameOK := true
		if cf.RenameSide != "" && cf.ConflictCopyPath != "" {
			renameRoot := rootA
			isLocal := capsA.IsLocal
			if cf.RenameSide == "B" {
				renameRoot = rootB
				isLocal = capsB.IsLocal
			}
			fromVirt := bisyncVirtualPath(renameRoot, cf.RelPath)
			toVirt := bisyncVirtualPath(renameRoot, cf.ConflictCopyPath)
			if isLocal {
				bisyncEchoSuppress.Mark(jobID, fromVirt)
				bisyncEchoSuppress.Mark(jobID, toVirt)
			}
			if err := cfg.MovePath(ctx, fromVirt, toVirt, false, sessOpts); err != nil {
				renameOK = false
				s := fmt.Sprintf("创建冲突副本失败：%v", err)
				jt.CopyHook(path.Dir(fromVirt), path.Dir(toVirt), path.Base(cf.RelPath), max(aSize, bSize), "", taskStatusFailed, &s, taskItemFile, taskItemTypeMove, time.Now().Unix())
				_ = mapper.RecordBisyncEntryError(jobID, cf.RelPath, false, s, time.Now().Unix())
				runErrs = append(runErrs, err)
			}
		}
		if !renameOK {
			if cf.ConflictType == mapper.BisyncConflictType {
				return runErrs[len(runErrs)-1]
			}
			continue
		}

		// Rebuild the directory after moving the file out of its path.
		if cf.ConflictType == mapper.BisyncConflictType {
			dirSideRoot := rootB
			isLocal := capsB.IsLocal
			if cf.RenameSide == "A" {
				dirSideRoot = rootA
				isLocal = capsA.IsLocal
			}
			dirVirt := bisyncVirtualPath(dirSideRoot, cf.RelPath)
			if isLocal {
				bisyncEchoSuppress.Mark(jobID, dirVirt)
			}
			if err := cfg.Mkdir(ctx, dirVirt, sessOpts); err != nil {
				return fmt.Errorf("创建冲突目录 %s: %w", dirVirt, err)
			}
			if err := mapper.UpsertBisyncEntry(makeBisyncEntry(jobID, cf.RelPath, true, BisyncNode{RelPath: cf.RelPath, IsDir: true}, BisyncNode{RelPath: cf.RelPath, IsDir: true}, time.Now().Unix())); err != nil {
				return err
			}
		}

		// Queue the winner and conflict copy for the directional transfers.
		if cf.CopyWinnerA2B && cf.NodeA != nil {
			extraCopyA2B = append(extraCopyA2B, BisyncTransfer{
				RelPath: cf.RelPath, Size: cf.NodeA.Size, Updated: true, Reason: "冲突胜出上传", NodeSrc: *cf.NodeA,
			})
		}
		if cf.CopyWinnerB2A && cf.NodeB != nil {
			extraCopyB2A = append(extraCopyB2A, BisyncTransfer{
				RelPath: cf.RelPath, Size: cf.NodeB.Size, Updated: true, Reason: "冲突胜出下载", NodeSrc: *cf.NodeB,
			})
		}
		if cf.CopyConflictA2B && cf.ConflictCopyPath != "" && cf.NodeA != nil {
			nCopy := *cf.NodeA
			nCopy.RelPath = cf.ConflictCopyPath
			extraCopyA2B = append(extraCopyA2B, BisyncTransfer{
				RelPath: cf.ConflictCopyPath, Size: nCopy.Size, Updated: false, Reason: "冲突副本上传", NodeSrc: nCopy,
			})
		}
		if cf.CopyConflictB2A && cf.ConflictCopyPath != "" && cf.NodeB != nil {
			nCopy := *cf.NodeB
			nCopy.RelPath = cf.ConflictCopyPath
			extraCopyB2A = append(extraCopyB2A, BisyncTransfer{
				RelPath: cf.ConflictCopyPath, Size: nCopy.Size, Updated: false, Reason: "冲突副本下载", NodeSrc: nCopy,
			})
		}

		// Record unresolved conflict in SQLite Conflict Center whenever a conflict copy was generated.
		if cf.ConflictCopyPath != "" {
			_, _ = mapper.UpsertBisyncConflict(mapper.BisyncConflict{
				JobID:            jobID,
				RelPath:          cf.RelPath,
				ConflictType:     cf.ConflictType,
				ConflictCopyPath: cf.ConflictCopyPath,
				WinnerSide:       cf.WinnerSide,
				ASize:            aSize,
				AModTime:         aMod,
				BSize:            bSize,
				BModTime:         bMod,
				Detail:           cf.Detail,
				CreatedAt:        time.Now().Unix(),
			})
		}
	}

	allCopyA2B := append(append([]BisyncTransfer(nil), plan.CopyA2B...), extraCopyA2B...)
	allCopyB2A := append(append([]BisyncTransfer(nil), plan.CopyB2A...), extraCopyB2A...)

	if len(extraCopyA2B) > 0 || len(extraCopyB2A) > 0 {
		var addFiles, addBytes int64
		for _, tr := range extraCopyA2B {
			addFiles++
			addBytes += max(0, tr.Size)
		}
		for _, tr := range extraCopyB2A {
			addFiles++
			addBytes += max(0, tr.Size)
		}
		jt.adjustNativePlanned(addFiles, addBytes)
	}

	// Phase 3: Create new directories (top-down) on both sides.
	for _, rel := range plan.MkdirA {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := jt.controller().WaitIfPaused(ctx); err != nil {
			return err
		}
		target := bisyncVirtualPath(rootA, rel)
		if capsA.IsLocal {
			bisyncEchoSuppress.Mark(jobID, target)
		}
		if err := cfg.Mkdir(ctx, target, sessOpts); err != nil {
			s := err.Error()
			jt.CopyHook(bisyncVirtualPath(rootB, path.Dir(rel)), bisyncVirtualPath(rootA, path.Dir(rel)), path.Base(rel), nil, "", taskStatusFailed, &s, taskItemPath, taskItemTypeCopy, time.Now().Unix())
			_ = mapper.RecordBisyncEntryError(jobID, rel, true, s, time.Now().Unix())
			runErrs = append(runErrs, err)
			continue
		}
		_ = mapper.UpsertBisyncEntry(makeBisyncEntry(jobID, rel, true, BisyncNode{RelPath: rel, IsDir: true}, BisyncNode{RelPath: rel, IsDir: true}, time.Now().Unix()))
	}
	for _, rel := range plan.MkdirB {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := jt.controller().WaitIfPaused(ctx); err != nil {
			return err
		}
		target := bisyncVirtualPath(rootB, rel)
		if capsB.IsLocal {
			bisyncEchoSuppress.Mark(jobID, target)
		}
		if err := cfg.Mkdir(ctx, target, sessOpts); err != nil {
			s := err.Error()
			jt.CopyHook(bisyncVirtualPath(rootA, path.Dir(rel)), bisyncVirtualPath(rootB, path.Dir(rel)), path.Base(rel), nil, "", taskStatusFailed, &s, taskItemPath, taskItemTypeCopy, time.Now().Unix())
			_ = mapper.RecordBisyncEntryError(jobID, rel, true, s, time.Now().Unix())
			runErrs = append(runErrs, err)
			continue
		}
		_ = mapper.UpsertBisyncEntry(makeBisyncEntry(jobID, rel, true, BisyncNode{RelPath: rel, IsDir: true}, BisyncNode{RelPath: rel, IsDir: true}, time.Now().Unix()))
	}

	// Phase 4: Same-side zero-traffic renames/moves.
	executeBisyncMoves := func(moves []BisyncMove, sideRoot string, isLocal bool) {
		for _, mv := range moves {
			if ctx.Err() != nil {
				return
			}
			if err := jt.controller().WaitIfPaused(ctx); err != nil {
				return
			}
			fromVirt := bisyncVirtualPath(sideRoot, mv.From)
			toVirt := bisyncVirtualPath(sideRoot, mv.To)
			expected := mv.NodeA
			if sideRoot == rootB {
				expected = mv.NodeB
			}
			current, exists, fromErr := cfg.StatEntry(ctx, fromVirt, expected.Hash != "", sessOpts)
			_, targetExists, toErr := cfg.StatEntry(ctx, toVirt, false, sessOpts)
			if err := errors.Join(fromErr, toErr); err != nil {
				runErrs = append(runErrs, fmt.Errorf("移动前复核 %s → %s: %w", fromVirt, toVirt, err))
				continue
			}
			if !exists || current == nil || !matchesBisyncSnapshot(dirEntryToBisyncNode(mv.From, current), expected) || targetExists {
				runErrs = append(runErrs, fmt.Errorf("移动前复核 %s → %s 失败：源文件已变更或目标已存在", fromVirt, toVirt))
				continue
			}
			if isLocal {
				bisyncEchoSuppress.Mark(jobID, fromVirt)
				bisyncEchoSuppress.Mark(jobID, toVirt)
			}
			// Ensure parent dir exists on target side.
			if parent := path.Dir(mv.To); parent != "." && parent != "/" && parent != "" {
				parentVirt := bisyncVirtualPath(sideRoot, parent)
				if isLocal {
					bisyncEchoSuppress.Mark(jobID, parentVirt)
				}
				_ = cfg.Mkdir(ctx, parentVirt, sessOpts)
			}
			if err := cfg.MovePath(ctx, fromVirt, toVirt, false, sessOpts); err != nil {
				s := err.Error()
				jt.CopyHook(path.Dir(fromVirt), path.Dir(toVirt), path.Base(mv.From)+" → "+path.Base(mv.To), mv.NodeA.Size, "", taskStatusFailed, &s, taskItemFile, taskItemTypeMove, time.Now().Unix())
				_ = mapper.RecordBisyncEntryError(jobID, mv.To, false, s, time.Now().Unix())
				runErrs = append(runErrs, err)
				continue
			}
			jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
			jt.CopyHook(path.Dir(fromVirt), path.Dir(toVirt), path.Base(mv.From)+" → "+path.Base(mv.To), max(mv.NodeA.Size, mv.NodeB.Size), "", taskStatusSuccess, nil, taskItemFile, taskItemTypeMove, time.Now().Unix())
			_ = mapper.MarkBisyncTombstone(jobID, mv.From, false, time.Now().Unix())
			var expectedA, expectedB *BisyncNode
			if sideRoot == rootB {
				expectedA = &mv.NodeA
			} else {
				expectedB = &mv.NodeB
			}
			if err := statAndCommitBisyncEntry(ctx, cfg, jobID, rootA, rootB, mv.To, expectedA, expectedB, wantHashA, wantHashB, sessOpts); err != nil {
				runErrs = append(runErrs, err)
			}
		}
	}
	executeBisyncMoves(plan.MoveA, rootA, capsA.IsLocal)
	executeBisyncMoves(plan.MoveB, rootB, capsB.IsLocal)

	// Phase 5: Directional transfers (A -> B uploads, then B -> A downloads) with per-file atomic baseline commit.
	runDirectionalCopy := func(transfers []BisyncTransfer, fromRoot, toRoot string, toIsLocal bool) {
		if len(transfers) == 0 || ctx.Err() != nil {
			return
		}
		onlyFiles := make([]native.PendingFile, 0, len(transfers))
		byRel := make(map[string]BisyncTransfer, len(transfers))
		for _, tr := range transfers {
			onlyFiles = append(onlyFiles, native.PendingFile{Path: tr.RelPath, Size: tr.Size})
			byRel[tr.RelPath] = tr
			if toIsLocal {
				bisyncEchoSuppress.Mark(jobID, bisyncVirtualPath(toRoot, tr.RelPath))
			}
		}

		jt.populateNativeWaiting(fromRoot, toRoot, onlyFiles)
		// Allow overwrite backup into trash regardless of conflictPolicy.
		backupDir := ""
		if trashEnabled(job) {
			backupDir = bisyncTrashBackupSubdir(jt, job)
		}
		copyOpts := buildNativeCopyOptions(job, false, backupDir)
		copyOpts.Compare = effectiveCompare
		copyOpts.ConflictPolicy = 0 // Planner already resolved conflicts; transfer should overwrite target cleanly.
		copyOpts.SkipExisting = false
		copyOpts.OnlyFiles = onlyFiles
		copyOpts.ForceOverwrite = true
		copyOpts.Controller = jt.controller()

		var (
			mu            sync.Mutex
			completed     = make(map[string]struct{}, len(onlyFiles))
			succeededRels = make([]string, 0, len(onlyFiles))
			failedRels    = make(map[string]string)
			emittedFiles  int64
			emittedBytes  int64
		)

		copyErr := cfg.Copy(ctx, strings.TrimRight(fromRoot, "/"), strings.TrimRight(toRoot, "/"), copyOpts, func(v native.Event) {
			if v.Stats != nil {
				if v.Stats.Bytes > 0 || v.Stats.Transfers > 0 || len(v.Stats.Transferring) > 0 {
					jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
				}
				statsCopy := *v.Stats
				mu.Lock()
				statsCopy.Transferring = filterCompletedTransferring(v.Stats.Transferring, completed)
				mu.Unlock()
				jt.nativeStats.Store(&nativeProgressSnapshot{src: fromRoot, dst: toRoot, stats: &statsCopy})
				jt.notifyProgressChange()
				return
			}
			if v.File == "" {
				return
			}
			jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
			rel := cleanBisyncRel(v.File)
			if rel == "" {
				return
			}
			if toIsLocal {
				bisyncEchoSuppress.Mark(jobID, bisyncVirtualPath(toRoot, rel))
			}
			srcDir := bisyncVirtualPath(fromRoot, path.Dir(rel))
			dstDir := bisyncVirtualPath(toRoot, path.Dir(rel))
			base := path.Base(rel)
			jt.Waiting.removeMatching(srcDir, dstDir, base)

			status := taskStatusSuccess
			var itemErr *string
			if v.Failed {
				text := strings.TrimSpace(v.Error)
				if text == "" {
					text = "双向同步未完成此文件传输"
				}
				itemErr = &text
				status = taskStatusFailed
			}

			mu.Lock()
			if _, seen := completed[rel]; !seen {
				completed[rel] = struct{}{}
				plannedSz := max(0, v.Size)
				if tr, ok := byRel[rel]; ok && tr.Size > 0 {
					plannedSz = tr.Size
				}
				emittedFiles++
				emittedBytes += plannedSz
				jt.recordNativePlannedFileDone(plannedSz, !v.Failed)
				if v.Failed {
					failedRels[rel] = *itemErr
				} else {
					succeededRels = append(succeededRels, rel)
				}
			}
			mu.Unlock()

			// Record true directional srcDir -> dstDir in job_task_item!
			jt.appendFinish(NewCopyJobTaskItem(jt.TaskID, srcDir, dstDir, base, v.Size, "", status, itemErr, taskItemFile, taskItemTypeCopy, time.Now().Unix()))
		})

		jt.Waiting.reset()
		jt.nativeStats.Store(nil)
		jt.notifyProgressChange()

		// Handle files that rclone skipped as already identical (no Copied event emitted) when copyErr == nil.
		if copyErr == nil && ctx.Err() == nil {
			mu.Lock()
			for _, pf := range onlyFiles {
				rel := cleanBisyncRel(pf.Path)
				if _, seen := completed[rel]; !seen {
					succeededRels = append(succeededRels, rel)
				}
			}
			mu.Unlock()
		}

		// Verify successful transfers against fresh metadata before updating the baseline.
		commitCtx := ctx
		if commitCtx.Err() != nil {
			// Even on cancellation, persist metadata for files that already completed transferring!
			var cancel context.CancelFunc
			commitCtx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
		}
		byDir := make(map[string][]string)
		for _, rel := range succeededRels {
			dir := path.Dir(rel)
			byDir[dir] = append(byDir[dir], rel)
		}
		batch := make([]mapper.BisyncEntry, 0, 256)
		flush := func() {
			if err := mapper.UpsertBisyncEntriesBatch(batch); err != nil {
				// Preserve independently valid entries if one row blocked the batch.
				for _, entry := range batch {
					if err := mapper.UpsertBisyncEntry(entry); err != nil {
						runErrs = append(runErrs, err)
					}
				}
			}
			batch = batch[:0]
		}
		for dir, rels := range byDir {
			hashA, hashB := wantHashA, wantHashB
			for _, rel := range rels {
				if tr, ok := byRel[rel]; ok && tr.NodeSrc.Hash != "" {
					hashA = hashA || fromRoot == rootA
					hashB = hashB || fromRoot == rootB
				}
			}
			var entriesA, entriesB map[string]*native.DirEntry
			if len(rels) > 1 && !capsA.IsLocal {
				entriesA = listBisyncVerificationDir(commitCtx, cfg, rootA, dir, rels, hashA, sessOpts)
			}
			if len(rels) > 1 && !capsB.IsLocal {
				entriesB = listBisyncVerificationDir(commitCtx, cfg, rootB, dir, rels, hashB, sessOpts)
			}
			for _, rel := range rels {
				var expectedA, expectedB *BisyncNode
				if tr, ok := byRel[rel]; ok {
					if fromRoot == rootA {
						expectedA = &tr.NodeSrc
					} else {
						expectedB = &tr.NodeSrc
					}
				}
				if toIsLocal {
					bisyncEchoSuppress.Mark(jobID, bisyncVirtualPath(toRoot, rel))
				}
				getEntry := func(root string, entries map[string]*native.DirEntry, wantHash bool, expected *BisyncNode) (*native.DirEntry, bool, error) {
					if entry := entries[path.Base(rel)]; entry != nil && (expected == nil || expected.Hash == "" || entry.MD5 != "") {
						return entry, true, nil
					}
					return cfg.StatEntry(commitCtx, bisyncVirtualPath(root, rel), wantHash || (expected != nil && expected.Hash != ""), sessOpts)
				}
				stA, okA, errA := getEntry(rootA, entriesA, wantHashA, expectedA)
				stB, okB, errB := getEntry(rootB, entriesB, wantHashB, expectedB)
				entry, err := verifiedBisyncEntry(jobID, rel, stA, stB, expectedA, expectedB, okA, okB, errA, errB)
				if err != nil {
					runErrs = append(runErrs, err)
					continue
				}
				batch = append(batch, entry)
				if len(batch) == cap(batch) {
					flush()
				}
			}
		}
		flush()
		for rel, msgText := range failedRels {
			_ = mapper.RecordBisyncEntryError(jobID, rel, false, msgText, time.Now().Unix())
		}
		if copyErr != nil {
			runErrs = append(runErrs, copyErr)
		}
	}

	runDirectionalCopy(allCopyA2B, rootA, rootB, capsB.IsLocal)
	runDirectionalCopy(allCopyB2A, rootB, rootA, capsA.IsLocal)

	if ctx.Err() != nil {
		return ctx.Err()
	}

	// Phase 6: Safe deletions (both sides support soft-delete trash and delete-volume safety guard).
	executeSideDeletes := func(deletes []BisyncDelete, deleteRoot, oppositeRoot string, snapshot map[string]BisyncNode, isLocal bool, guarded bool, guardReason string) {
		if len(deletes) == 0 {
			return
		}
		if guarded {
			if guardReason != "" {
				log.Printf("bisync job %d: %s", jobID, guardReason)
			}
			errMsg := guardReason
			if errMsg == "" {
				errMsg = "删除数量超过安全保护阈值，已跳过本次删除"
			}
			jt.CopyHook("", deleteRoot, "", nil, "", taskStatusFailed, &errMsg, taskItemPath, taskItemTypeDelete, time.Now().Unix())
			return
		}
		for _, d := range deletes {
			if ctx.Err() != nil {
				return
			}
			if err := jt.controller().WaitIfPaused(ctx); err != nil {
				return
			}
			dir := bisyncVirtualPath(deleteRoot, path.Dir(d.RelPath))
			name := path.Base(d.RelPath)
			targetVirt := bisyncVirtualPath(deleteRoot, d.RelPath)
			expected := snapshot[d.RelPath]
			_, oppositeExists, oppositeErr := cfg.StatEntry(ctx, bisyncVirtualPath(oppositeRoot, d.RelPath), false, sessOpts)
			current, exists, statErr := cfg.StatEntry(ctx, targetVirt, expected.Hash != "", sessOpts)
			if err := errors.Join(oppositeErr, statErr); err != nil {
				runErrs = append(runErrs, fmt.Errorf("删除前复核 %s: %w", targetVirt, err))
				continue
			}
			if oppositeExists || (exists && (current == nil || !matchesBisyncSnapshot(dirEntryToBisyncNode(d.RelPath, current), expected))) {
				runErrs = append(runErrs, fmt.Errorf("删除前复核 %s 失败：同步路径已变更", targetVirt))
				continue
			}
			if !exists {
				if err := mapper.MarkBisyncTombstone(jobID, d.RelPath, d.IsDir, time.Now().Unix()); err != nil {
					runErrs = append(runErrs, err)
				}
				continue
			}
			if isLocal {
				bisyncEchoSuppress.Mark(jobID, targetVirt)
			}
			// For directories, only remove if empty so we never accidentally wipe newly created untracked files.
			if d.IsDir {
				children, exists, listErr := cfg.ListDirEntries(ctx, targetVirt, sessOpts)
				if listErr != nil {
					runErrs = append(runErrs, listErr)
					continue
				}
				if !exists {
					_ = mapper.MarkBisyncTombstone(jobID, d.RelPath, true, time.Now().Unix())
					continue
				}
				if len(children) > 0 {
					continue
				}
			}
			var delErr error
			if d.IsDir && !trashEnabled(job) {
				delErr = cfg.RemoveEmptyDir(ctx, targetVirt, sessOpts)
			} else {
				delErr = nativeDeleteOrTrash(ctx, cfg, jt, job, deleteRoot, dir, name, d.IsDir)
			}
			if delErr != nil {
				s := delErr.Error()
				jt.DelHook(dir, name, d.Size, taskStatusFailed, &s, boolToTaskItemObject(d.IsDir), time.Now().Unix())
				_ = mapper.RecordBisyncEntryError(jobID, d.RelPath, d.IsDir, s, time.Now().Unix())
				runErrs = append(runErrs, delErr)
				continue
			}
			jt.FirstSync.CompareAndSwap(0, time.Now().Unix())
			jt.DelHook(dir, name, d.Size, taskStatusSuccess, nil, boolToTaskItemObject(d.IsDir), time.Now().Unix())
			_ = mapper.MarkBisyncTombstone(jobID, d.RelPath, d.IsDir, time.Now().Unix())
		}
	}

	// Leave old copies available if the replacements could not be written or verified.
	if len(runErrs) == 0 {
		executeSideDeletes(plan.DeleteA, rootA, rootB, snapA, capsA.IsLocal, plan.DeleteGuardA, plan.DeleteGuardReasonA)
		executeSideDeletes(plan.DeleteB, rootB, rootA, snapB, capsB.IsLocal, plan.DeleteGuardB, plan.DeleteGuardReasonB)
	}

	// Phase 7: Update root sentinel metadata, expire old tombstones & trash buckets.
	saveBisyncRootMeta(jobID, cfg, rootA, rootB, epA, epB, countA, countB, metaA, metaB)
	_ = mapper.PurgeExpiredBisyncTombstones(jobID, time.Now().AddDate(0, 0, -bisyncTombstoneKeepDays).Unix())
	if len(runErrs) == 0 && ctx.Err() == nil {
		cleanupNativeExpiredTrash(ctx, cfg, jt.TaskID, job)
	}
	if len(runErrs) > 0 {
		return runErrs[0]
	}
	return nil
}

// bisyncTrashBackupSubdir returns a versioned backup directory under trashDir
// without disabling trash when conflictPolicy == 2.
func bisyncTrashBackupSubdir(jt *JobTask, job map[string]interface{}) string {
	if !trashEnabled(job) {
		return ""
	}
	trashDir := jobTrashDir(job)
	now := time.Now().In(schedulerLocation())
	dateBucket := now.Format("2006-01-02")
	var versionBucket string
	if jt != nil {
		versionBucket = jt.nextTrashVersionBucket(now, dateBucket, "", "overwrite")
	} else {
		versionBucket = fmt.Sprintf("%s-%s", now.Format("150405"), randomHexToken(4))
	}
	return path.Join(trashDir, dateBucket, versionBucket)
}

func extractBisyncDeltaRels(changedPaths []string, rootA, rootB, trashDir string) []string {
	normA := normalizeDirPath(rootA)
	normB := normalizeDirPath(rootB)
	seen := make(map[string]struct{})
	out := make([]string, 0, len(changedPaths))
	for _, raw := range changedPaths {
		p := path.Clean("/" + raw)
		var rel string
		if strings.HasPrefix(p, normA) {
			rel = cleanBisyncRel(strings.TrimPrefix(p, normA))
		} else if strings.HasPrefix(p, normB) {
			rel = cleanBisyncRel(strings.TrimPrefix(p, normB))
		}
		if rel == "" || isBisyncInternalPath(rel, trashDir) {
			continue
		}
		if _, dup := seen[rel]; !dup {
			seen[rel] = struct{}{}
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

// probeBisyncDeltaPaths probes only the specific changed paths on A & B for
// fast sub-second incremental bidirectional sync. If any path is a directory
// or cannot be cleanly probed, it returns ok=false to fall back to full scan.
func probeBisyncDeltaPaths(
	ctx context.Context,
	cfg native.Config,
	jobID int64,
	rootA, rootB string,
	rels []string,
	job map[string]interface{},
	spec *excludeMatcher,
	includeM *includeMatcher,
	wantHashA, wantHashB bool,
	now time.Time,
	sessOpts native.SessionOptions,
) (map[string]mapper.BisyncEntry, map[string]BisyncNode, map[string]BisyncNode, map[string]bool, bool) {
	baseMap := make(map[string]mapper.BisyncEntry, len(rels))
	snapA := make(map[string]BisyncNode, len(rels))
	snapB := make(map[string]BisyncNode, len(rels))
	excluded := make(map[string]bool)

	for _, rel := range rels {
		b, err := mapper.GetBisyncEntry(jobID, rel)
		if err != nil {
			return nil, nil, nil, nil, false
		}
		if b != nil {
			if b.IsDir {
				return nil, nil, nil, nil, false
			}
			baseMap[rel] = *b
		}
		stA, okA, errA := cfg.StatEntry(ctx, bisyncVirtualPath(rootA, rel), wantHashA, sessOpts)
		if errA != nil {
			return nil, nil, nil, nil, false
		}
		if okA && stA != nil {
			if stA.IsDir {
				return nil, nil, nil, nil, false
			}
			if !bisyncNodeMatchesFilters(rel, false, stA.Size, stA.ModTime, job, spec, includeM, now) {
				excluded[rel] = true
			} else {
				snapA[rel] = dirEntryToBisyncNode(rel, stA)
			}
		}
		stB, okB, errB := cfg.StatEntry(ctx, bisyncVirtualPath(rootB, rel), wantHashB, sessOpts)
		if errB != nil {
			return nil, nil, nil, nil, false
		}
		if okB && stB != nil {
			if stB.IsDir {
				return nil, nil, nil, nil, false
			}
			if !bisyncNodeMatchesFilters(rel, false, stB.Size, stB.ModTime, job, spec, includeM, now) {
				excluded[rel] = true
			} else {
				snapB[rel] = dirEntryToBisyncNode(rel, stB)
			}
		}
	}
	return baseMap, snapA, snapB, excluded, true
}

// dryRunNativeBisync previews bidirectional sync changes using the exact same
// SQLite 3-Way Merge planner without mutating either side or the database.
func dryRunNativeBisync(ctx context.Context, sess *dryRunSession, cfg native.Config, job map[string]interface{}) error {
	jobID := util.ToInt64(job["id"])
	srcPaths := parsePathList(job["srcPath"])
	dstPaths := parsePathList(job["dstPath"])
	if len(srcPaths) != 1 || len(dstPaths) != 1 {
		return errors.New("双向同步仅支持单个源目录与单个目标目录")
	}
	rootA := normalizeDirPath(srcPaths[0])
	rootB := normalizeDirPath(dstPaths[0])
	epA, epB, err := cfg.Pair(strings.TrimRight(rootA, "/"), strings.TrimRight(rootB, "/"))
	if err != nil {
		return err
	}
	capsA := buildBisyncEndpointCaps(epA, "A")
	capsB := buildBisyncEndpointCaps(epB, "B")
	effectiveCompare := nativePairEffectiveCompareMode(cfg, rootA, rootB, util.ToInt(job["compareMode"]))
	wantHash := effectiveCompare == 2
	extra, _, _ := parseNativeExtraFlags(job["extraFlags"])
	spec := buildJobExcludeMatcher(job)
	includeM := buildJobIncludeMatcher(job)
	now := time.Now()

	snapA, exclA, _, err := scanBisyncTree(ctx, cfg, rootA, job, spec, includeM, wantHash && capsA.SupportsMD5, now, nil)
	if err != nil {
		return err
	}
	snapB, exclB, _, err := scanBisyncTree(ctx, cfg, rootB, job, spec, includeM, wantHash && capsB.SupportsMD5, now, nil)
	if err != nil {
		return err
	}
	baseMap, err := mapper.ListBisyncEntries(jobID)
	if err != nil {
		return err
	}
	excluded := make(map[string]bool, len(exclA)+len(exclB))
	for k, v := range exclA {
		if v {
			excluded[k] = true
		}
	}
	for k, v := range exclB {
		if v {
			excluded[k] = true
		}
	}

	plan := BuildBisyncPlan(BisyncPlanInput{
		JobID:            jobID,
		Now:              now,
		CompareMode:      effectiveCompare,
		ModifyWindowSec:  parseModifyWindowSec(util.StringValue(job["modifyWindow"])),
		ConflictPolicy:   util.ToInt(job["conflictPolicy"]),
		MaxDeletePercent: extra.BisyncMaxDelete,
		ForceDelete:      extra.BisyncForce,
		CapsA:            capsA,
		CapsB:            capsB,
		Base:             baseMap,
		SnapA:            snapA,
		SnapB:            snapB,
		Excluded:         excluded,
	})

	sess.scannedDirs.Add(int64(len(snapA) + len(snapB)))

	for _, rel := range plan.MkdirA {
		sess.addItem(DryRunItem{Action: "mkdir", SrcPath: bisyncVirtualPath(rootB, path.Dir(rel)), DstPath: bisyncVirtualPath(rootA, path.Dir(rel)), FileName: path.Base(rel), IsPath: true, Reason: "新建本地目录"})
	}
	for _, rel := range plan.MkdirB {
		sess.addItem(DryRunItem{Action: "mkdir", SrcPath: bisyncVirtualPath(rootA, path.Dir(rel)), DstPath: bisyncVirtualPath(rootB, path.Dir(rel)), FileName: path.Base(rel), IsPath: true, Reason: "新建远端目录"})
	}
	for _, mv := range plan.MoveA {
		sess.addItem(DryRunItem{Action: "move", SrcPath: bisyncVirtualPath(rootA, path.Dir(mv.From)), DstPath: bisyncVirtualPath(rootA, path.Dir(mv.To)), FileName: path.Base(mv.From) + " → " + path.Base(mv.To), FileSize: mv.NodeA.Size, Reason: "远端重命名同步至本地"})
	}
	for _, mv := range plan.MoveB {
		sess.addItem(DryRunItem{Action: "move", SrcPath: bisyncVirtualPath(rootB, path.Dir(mv.From)), DstPath: bisyncVirtualPath(rootB, path.Dir(mv.To)), FileName: path.Base(mv.From) + " → " + path.Base(mv.To), FileSize: mv.NodeB.Size, Reason: "本地重命名同步至远端"})
	}
	for _, tr := range plan.CopyA2B {
		act := "copy"
		if tr.Updated {
			act = "update"
		}
		sess.addItem(DryRunItem{Action: act, SrcPath: bisyncVirtualPath(rootA, path.Dir(tr.RelPath)), DstPath: bisyncVirtualPath(rootB, path.Dir(tr.RelPath)), FileName: path.Base(tr.RelPath), FileSize: tr.Size, Reason: "↑ " + tr.Reason})
	}
	for _, tr := range plan.CopyB2A {
		act := "copy"
		if tr.Updated {
			act = "update"
		}
		sess.addItem(DryRunItem{Action: act, SrcPath: bisyncVirtualPath(rootB, path.Dir(tr.RelPath)), DstPath: bisyncVirtualPath(rootA, path.Dir(tr.RelPath)), FileName: path.Base(tr.RelPath), FileSize: tr.Size, Reason: "↓ " + tr.Reason})
	}
	for _, cf := range plan.Conflicts {
		var sz int64
		if cf.NodeA != nil {
			sz = cf.NodeA.Size
		} else if cf.NodeB != nil {
			sz = cf.NodeB.Size
		}
		act := "update"
		if cf.ConflictType == mapper.BisyncConflictInvalidName {
			act = "skip"
		}
		sess.addItem(DryRunItem{Action: act, SrcPath: bisyncVirtualPath(rootA, path.Dir(cf.RelPath)), DstPath: bisyncVirtualPath(rootB, path.Dir(cf.RelPath)), FileName: path.Base(cf.RelPath), FileSize: sz, Reason: cf.Detail})
	}
	addDeletes := func(root string, deletes []BisyncDelete, guarded bool, guardReason string) {
		for _, d := range deletes {
			action, reason := "delete", d.Reason
			if guarded {
				action, reason = "skip", guardReason
			}
			sess.addItem(DryRunItem{Action: action, DstPath: bisyncVirtualPath(root, path.Dir(d.RelPath)), FileName: path.Base(d.RelPath), FileSize: d.Size, IsPath: d.IsDir, Reason: reason})
		}
	}
	addDeletes(rootA, plan.DeleteA, plan.DeleteGuardA, plan.DeleteGuardReasonA)
	addDeletes(rootB, plan.DeleteB, plan.DeleteGuardB, plan.DeleteGuardReasonB)
	return nil
}

// --- Conflict Center Service APIs ---

func ListJobBisyncConflicts(jobID int64, includeResolved bool) ([]mapper.BisyncConflict, error) {
	if _, err := mapper.GetJobByID(jobID); err != nil {
		return nil, publicErrorIf(err, msg.T(msg.JobNotFound))
	}
	return mapper.ListBisyncConflicts(jobID, includeResolved)
}

// ResolveJobBisyncConflict resolves a recorded bidirectional conflict with one
// of: "keep_both" (dismiss), "keep_local" (keep left/A), or "keep_remote" (keep right/B).
func ResolveJobBisyncConflict(ctx context.Context, jobID, conflictID int64, action string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "keep_both", "dismiss", "keep_local", "keep_remote":
	default:
		return publicError("冲突处理方式仅支持 keep_local、keep_remote 或 keep_both")
	}
	client, err := GetJobClientByID(jobID)
	if err != nil {
		return err
	}
	if !client.lockOp() {
		client.opMu.Unlock()
		return publicError(msg.T(msg.JobNotFound))
	}
	defer client.opMu.Unlock()
	if client.isBusy() || !client.tryMarkDoing() {
		return publicError("任务正在执行，请在执行结束后处理冲突")
	}
	defer client.markDone()
	conflict, err := mapper.GetBisyncConflictByID(conflictID)
	if err != nil {
		return err
	}
	if conflict == nil || conflict.JobID != jobID {
		return publicError("未找到对应的冲突记录")
	}
	if conflict.Resolved {
		return nil
	}
	if action == "keep_both" || action == "dismiss" {
		return mapper.ResolveBisyncConflict(conflictID)
	}
	if conflict.ConflictType != mapper.BisyncConflictContent || conflict.ConflictCopyPath == "" {
		return publicError("只有带副本的内容冲突可以选择保留一侧版本")
	}

	job, err := mapper.GetJobByID(jobID)
	if err != nil {
		return publicErrorIf(err, msg.T(msg.JobNotFound))
	}
	cfg, err := loadNative(util.ToInt64(job["alistId"]))
	if err != nil {
		return err
	}
	srcPaths := parsePathList(job["srcPath"])
	dstPaths := parsePathList(job["dstPath"])
	if len(srcPaths) != 1 || len(dstPaths) != 1 {
		return publicError("任务路径配置无效")
	}
	rootA := normalizeDirPath(srcPaths[0])
	rootB := normalizeDirPath(dstPaths[0])
	epA, epB, err := cfg.Pair(strings.TrimRight(rootA, "/"), strings.TrimRight(rootB, "/"))
	if err != nil {
		return publicError(err.Error())
	}
	sessOpts := native.SessionOptions{ProxyURL: globalNativeProxyURL()}

	// Determine whether the user wants the version currently at RelPath (winner)
	// or the version stored at ConflictCopyPath (loser).
	desiredSide := "A"
	if action == "keep_remote" {
		desiredSide = "B"
	}
	keepMain := conflict.WinnerSide == "" || conflict.WinnerSide == desiredSide

	deleteCopyOnSide := func(root string, isLocal bool, rel string) error {
		v := bisyncVirtualPath(root, rel)
		entry, exists, err := cfg.StatEntry(ctx, v, false, sessOpts)
		if err != nil || !exists {
			return err
		}
		if entry == nil || entry.IsDir {
			return fmt.Errorf("冲突副本 %s 的类型已变更", v)
		}
		if isLocal {
			bisyncEchoSuppress.Mark(jobID, v)
		}
		return nativeDeleteOrTrash(ctx, cfg, nil, job, root, path.Dir(v), path.Base(v), false)
	}

	finish := func() error {
		if err := deleteCopyOnSide(rootA, epA.IsLocal, conflict.ConflictCopyPath); err != nil {
			return err
		}
		if err := deleteCopyOnSide(rootB, epB.IsLocal, conflict.ConflictCopyPath); err != nil {
			return err
		}
		if err := mapper.MarkBisyncTombstone(jobID, conflict.ConflictCopyPath, false, time.Now().Unix()); err != nil {
			return err
		}
		return mapper.ResolveBisyncConflict(conflictID)
	}

	if conflict.ResolutionWritten {
		return finish()
	}
	if !keepMain {
		type side struct {
			root, label, copyPath, mainPath string
			endpoint                        native.Endpoint
			copyEntry                       *native.DirEntry
			mainExists                      bool
		}
		sides := []side{{root: rootA, label: "左", endpoint: epA}, {root: rootB, label: "右", endpoint: epB}}
		for i := range sides {
			s := &sides[i]
			s.copyPath = bisyncVirtualPath(s.root, conflict.ConflictCopyPath)
			s.mainPath = bisyncVirtualPath(s.root, conflict.RelPath)
			entry, exists, err := cfg.StatEntry(ctx, s.copyPath, s.endpoint.SupportsNativeMD5(), sessOpts)
			if err != nil {
				return err
			}
			if !exists || entry == nil || entry.IsDir {
				return publicError(fmt.Sprintf("冲突副本 %s 缺失或类型已变更", s.copyPath))
			}
			s.copyEntry = entry
			mainEntry, mainExists, err := cfg.StatEntry(ctx, s.mainPath, false, sessOpts)
			if err != nil {
				return err
			}
			if mainExists && (mainEntry == nil || mainEntry.IsDir) {
				return publicError(fmt.Sprintf("原路径 %s 的类型已变更", s.mainPath))
			}
			s.mainExists = mainExists
		}
		left, right := sides[0].copyEntry, sides[1].copyEntry
		if left.Size != right.Size || (left.MD5 != "" && right.MD5 != "" && !strings.EqualFold(left.MD5, right.MD5)) {
			return publicError("两侧冲突副本内容不一致")
		}
		chosen := sides[0]
		if desiredSide == "B" {
			chosen = sides[1]
		}
		for _, s := range sides {
			if s.endpoint.IsLocal {
				bisyncEchoSuppress.Mark(jobID, s.mainPath)
			}
			if s.mainExists && trashEnabled(job) {
				if err := nativeDeleteOrTrash(ctx, cfg, nil, job, s.root, path.Dir(s.mainPath), path.Base(s.mainPath), false); err != nil {
					return err
				}
			}
			if err := cfg.CopyFileToPath(ctx, chosen.copyPath, s.mainPath, sessOpts); err != nil {
				return publicError(fmt.Sprintf("替换%s侧文件失败：%v", s.label, err))
			}
			verifyHash := chosen.copyEntry.MD5 != "" && s.endpoint.SupportsNativeMD5()
			main, exists, err := cfg.StatEntry(ctx, s.mainPath, verifyHash, sessOpts)
			if err != nil {
				return err
			}
			if !exists || main == nil || main.IsDir || main.Size != chosen.copyEntry.Size ||
				(verifyHash && !strings.EqualFold(main.MD5, chosen.copyEntry.MD5)) {
				return publicError(fmt.Sprintf("核对%s侧文件失败：替换后的内容与冲突副本不一致", s.label))
			}
		}
	}
	if err := statAndCommitBisyncEntry(ctx, cfg, jobID, rootA, rootB, conflict.RelPath, nil, nil, epA.SupportsNativeMD5(), epB.SupportsNativeMD5(), sessOpts); err != nil {
		return err
	}
	if err := mapper.MarkBisyncConflictResolutionWritten(conflictID); err != nil {
		return err
	}
	return finish()
}
