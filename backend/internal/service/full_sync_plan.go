package service

import (
	"crypto/sha256"
	"fmt"
	"log"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/text/unicode/norm"
)

type fullSyncSnapshot struct {
	root          string
	dirs          map[string]FileListResult
	excluded      map[string]struct{}
	protectedDirs map[string]struct{}
	// specExcluded and sizeExcluded split the excluded entries by reason. A
	// destination entry filtered out by an exclude rule must never be
	// overwritten, while one skipped only because its size is outside the
	// configured range is replaced when the source file is in range.
	specExcluded map[string]struct{}
	sizeExcluded map[string]struct{}
	mu           sync.Mutex
	// subtreeCounts memoizes subtreeEntryCount. It is built on first use, after
	// the scan has finished writing dirs.
	subtreeCounts map[string]int
}

type fullSyncFile struct {
	srcDir   string
	dstDir   string
	name     string
	metadata FileMetadata
	target   string
}

type fullSyncExtraFile struct {
	dir        string
	name       string
	metadata   FileMetadata
	object     string
	deleteRoot string
}

type fullSyncDir struct {
	srcDir string
	dstDir string
}

type fullSyncDelete struct {
	srcDir     string
	dir        string
	name       string
	metadata   FileMetadata
	object     string
	blocksPath string
	protected  bool
	isBlocker  bool
	// subtree is the destination snapshot key of a directory entry, so the
	// safety valve can weigh a recursive delete by everything it removes.
	subtree string
}

type fullSyncRelocation struct {
	source *fullSyncExtraFile
	target *fullSyncFile
	item   *CopyItem
}

// FullSyncApproval describes the deferred destructive deletions of a full-sync
// run. DeleteCount includes entries below a recursive directory delete; Paths
// is a bounded preview so a large mirror does not put an unbounded payload in
// API responses.
type FullSyncApproval struct {
	JobID       int64                  `json:"jobId,omitempty"`
	JobName     string                 `json:"jobName,omitempty"`
	TaskID      int64                  `json:"taskId,omitempty"`
	DeleteCount int                    `json:"deleteCount"`
	DeleteSize  int64                  `json:"deleteSize"`
	PathCount   int                    `json:"pathCount"`
	Paths       []FullSyncApprovalPath `json:"paths"`
	Status      string                 `json:"status,omitempty"`
	CreatedAt   int64                  `json:"createdAt,omitempty"`
	ExpiresAt   int64                  `json:"expiresAt,omitempty"`
	PlanChanged bool                   `json:"planChanged,omitempty"`
	LastError   string                 `json:"lastError,omitempty"`
	signature   string
}

type FullSyncApprovalPath struct {
	Path   string `json:"path"`
	IsPath bool   `json:"isPath"`
	Count  int    `json:"count"`
	Size   int64  `json:"size"`
	Action string `json:"action"`
}

const fullSyncApprovalPathLimit = 100

type fullSyncPlan struct {
	src *fullSyncSnapshot
	dst *fullSyncSnapshot

	// branchGate bounds the concurrent recursion in compareDir. Without it a
	// deep destination tree forks one goroutine per child directory at every
	// level, so a wide tree can spawn thousands at once. It is the task's
	// existing scan-branch semaphore, so plan building and tree scanning share
	// one budget; a nil gate (tests) runs the recursion sequentially.
	branchGate chan struct{}

	mu           sync.Mutex
	newDirs      map[string]fullSyncDir
	newFiles     map[string]*fullSyncFile
	changed      []*fullSyncFile
	extraFiles   map[string]*fullSyncExtraFile
	extraDeletes map[string]fullSyncDelete
	blockers     map[string]fullSyncDelete
	// emptySrcDirs are destination-relative keys of matched directories whose
	// source listing was empty while the destination one was not.
	emptySrcDirs []string
	// scanIncomplete is set when at least one listing failed. The plan then
	// carries only the writes proven by the listings that succeeded: deletes,
	// relocations and type-conflict removals could all be based on entries the
	// failed listing never showed, so they are left to a complete run.
	scanIncomplete bool
	srcScanFailed  bool
	dstScanFailed  bool
	// planErr is the first panic captured from a concurrent compareDir branch.
	// A partially built plan is never executed: it could not have weighed its
	// own deletions and may treat unmatched entries as extra.
	planErr error
}

// fail records the first plan-branch panic; later panics are ignored.
func (plan *fullSyncPlan) fail(err error) {
	if err == nil {
		return
	}
	plan.mu.Lock()
	if plan.planErr == nil {
		plan.planErr = err
	}
	plan.mu.Unlock()
}

// error reports whether a plan branch panicked while building the plan.
func (plan *fullSyncPlan) error() error {
	plan.mu.Lock()
	defer plan.mu.Unlock()
	return plan.planErr
}

// fullSyncPlanBranchHook runs at the start of every concurrent compareDir
// branch. Tests swap it to exercise the panic recovery; production leaves it nil.
var fullSyncPlanBranchHook func()

func (jt *JobTask) syncFull(work scanWork, spec *excludeMatcher) {
	plan := jt.buildFullSyncPlan(work, spec)
	if plan == nil {
		return
	}
	jt.executeFullSyncPlan(plan)
}

func (jt *JobTask) buildFullSyncPlan(work scanWork, spec *excludeMatcher) *fullSyncPlan {
	// Full sync needs both complete trees before it mutates the destination;
	// otherwise an old path may be deleted before it can satisfy a moved file.
	var srcSnapshot, dstSnapshot *fullSyncSnapshot
	var srcErr, dstErr error

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer jt.recoverWorkerPanic("full-sync source scan", &srcErr)
		srcSnapshot, srcErr = jt.scanFullSyncTree(work.SrcPath, work.DstPath, work.FirstDst, spec, true)
	}()
	go func() {
		defer wg.Done()
		defer jt.recoverWorkerPanic("full-sync destination scan", &dstErr)
		dstSnapshot, dstErr = jt.scanFullSyncTree(work.DstPath, work.SrcPath, work.FirstDst, spec, false)
	}()
	wg.Wait()

	if jt.isBreak() {
		return nil
	}
	if srcSnapshot == nil {
		srcSnapshot = emptyFullSyncSnapshot(work.SrcPath)
	}
	if dstSnapshot == nil {
		dstSnapshot = emptyFullSyncSnapshot(work.DstPath)
	}
	if srcErr != nil {
		log.Printf("Task %d full-sync source scan incomplete: %v; copying scanned entries only, destructive actions skipped", jt.TaskID, srcErr)
	}
	if dstErr != nil {
		// The destination root may simply not exist yet on a first run. Create it
		// and rescan once; anything else is a real failure.
		if !jt.ensureDstDirAfterListError(work.DstPath, dstErr) {
			log.Printf("Task %d full-sync destination scan incomplete: %v; copying scanned entries only, destructive actions skipped", jt.TaskID, dstErr)
		} else {
			dstSnapshot, dstErr = jt.scanFullSyncTree(work.DstPath, work.SrcPath, work.FirstDst, spec, false)
			if dstErr != nil && !jt.isBreak() {
				log.Printf("Task %d full-sync destination scan incomplete after repair: %v; destructive actions skipped", jt.TaskID, dstErr)
			}
		}
		if jt.isBreak() {
			return nil
		}
	}

	plan := newFullSyncPlan(srcSnapshot, dstSnapshot, jt.scanBranchSem)
	plan.srcScanFailed = srcErr != nil
	plan.dstScanFailed = dstErr != nil
	plan.scanIncomplete = plan.srcScanFailed || plan.dstScanFailed
	func() {
		// A panic in the inline recursion path must abort the plan, not just the
		// goroutine that happened to run it.
		defer func() {
			if r := recover(); r != nil {
				plan.fail(fmt.Errorf("full-sync plan panic: %v", r))
			}
		}()
		plan.build(jt.Job)
	}()
	if err := plan.error(); err != nil {
		// Never execute a partially built plan: the safety valve was computed
		// from it, so executing it could delete destination entries that were
		// never compared against the source.
		log.Printf("Task %d aborted full-sync plan: %v", jt.TaskID, err)
		jt.setFatalError(err.Error())
		jt.requestBreak()
		return nil
	}
	return plan
}

func combineFullSyncApprovals(plans []*fullSyncPlan) *FullSyncApproval {
	combined := &FullSyncApproval{}
	var signatures []string
	for _, plan := range plans {
		approval := plan.approvalPreview()
		if approval == nil {
			continue
		}
		combined.DeleteCount += approval.DeleteCount
		combined.DeleteSize += approval.DeleteSize
		combined.PathCount += approval.PathCount
		if approval.signature != "" {
			signatures = append(signatures, approval.signature)
		}
		if len(combined.Paths) < fullSyncApprovalPathLimit {
			remaining := fullSyncApprovalPathLimit - len(combined.Paths)
			if remaining > len(approval.Paths) {
				remaining = len(approval.Paths)
			}
			combined.Paths = append(combined.Paths, approval.Paths[:remaining]...)
		}
	}
	if combined.PathCount == 0 {
		return nil
	}
	sort.Strings(signatures)
	sum := sha256.Sum256([]byte(strings.Join(signatures, "\x00")))
	combined.signature = fmt.Sprintf("%x", sum[:])
	return combined
}

func sameFullSyncApproval(before, after *FullSyncApproval) bool {
	if before == nil || after == nil {
		return before == nil && after == nil
	}
	return before.signature != "" && before.signature == after.signature
}

func (jt *JobTask) scanFullSyncTree(root, pairedRoot string, firstDst bool, spec *excludeMatcher, isSrc bool) (*fullSyncSnapshot, error) {
	root = normalizeDirPath(root)
	snapshot := &fullSyncSnapshot{
		root:          root,
		dirs:          make(map[string]FileListResult),
		excluded:      make(map[string]struct{}),
		protectedDirs: make(map[string]struct{}),
		specExcluded:  make(map[string]struct{}),
		sizeExcluded:  make(map[string]struct{}),
	}

	var firstErr error
	var errMu sync.Mutex
	var scanFailed atomic.Bool
	var scanDir func(string)
	scanDir = func(relDir string) {
		jt.ScanTotalDirs.Add(1)
		defer jt.finishScanWork()
		if jt.isBreak() || scanFailed.Load() {
			return
		}

		items, err := jt.listDir(root+relDir, firstDst, nil, root, isSrc, pairedRoot)
		if err != nil {
			errMu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			errMu.Unlock()
			scanFailed.Store(true)
			return
		}
		children := make([]string, 0)
		var excluded []string
		var specExcluded, sizeExcluded []string
		includeM := buildJobIncludeMatcher(jt.Job)
		now := time.Now()
		for name, metadata := range items {
			isDir := strings.HasSuffix(name, "/")
			relPath := relDir + name
			if spec != nil && spec.matchesListed(relPath, isDir) {
				if isSrc {
					delete(items, name)
				} else {
					excluded = append(excluded, relPath)
					specExcluded = append(specExcluded, relPath)
				}
				continue
			}
			if !isDir && includeM != nil && !includeM.matchesListed(relPath) {
				if isSrc {
					delete(items, name)
				} else {
					excluded = append(excluded, relPath)
					specExcluded = append(specExcluded, relPath)
				}
				continue
			}
			if !isSrc && !isDir && !jobAllowsFileMeta(jt.Job, metadata, now) {
				excluded = append(excluded, relPath)
				sizeExcluded = append(sizeExcluded, relPath)
				continue
			}
			if isDir {
				children = append(children, relPath)
			}
		}
		snapshot.mu.Lock()
		snapshot.dirs[relDir] = items
		for _, entry := range excluded {
			snapshot.excluded[entry] = struct{}{}
		}
		for _, entry := range specExcluded {
			snapshot.specExcluded[entry] = struct{}{}
		}
		for _, entry := range sizeExcluded {
			snapshot.sizeExcluded[entry] = struct{}{}
		}
		if len(excluded) > 0 {
			for ancestor := relDir; ancestor != ""; {
				if _, protected := snapshot.protectedDirs[ancestor]; protected {
					break
				}
				snapshot.protectedDirs[ancestor] = struct{}{}
				ancestor = ancestor[:strings.LastIndex(strings.TrimSuffix(ancestor, "/"), "/")+1]
			}
		}
		snapshot.mu.Unlock()
		sort.Strings(children)

		var wg sync.WaitGroup
		for _, child := range children {
			child := child
			if jt.tryAcquireScanBranchSlot() {
				wg.Add(1)
				go func() {
					var childErr error
					defer wg.Done()
					defer jt.releaseScanBranchSlot()
					defer func() {
						if childErr == nil {
							return
						}
						errMu.Lock()
						if firstErr == nil {
							firstErr = childErr
						}
						errMu.Unlock()
						scanFailed.Store(true)
					}()
					defer jt.recoverWorkerPanic("full-sync child scan", &childErr)
					scanDir(child)
				}()
				continue
			}
			scanDir(child)
		}
		wg.Wait()
	}

	scanDir("")
	return snapshot, firstErr
}

func emptyFullSyncSnapshot(root string) *fullSyncSnapshot {
	return &fullSyncSnapshot{
		root:          normalizeDirPath(root),
		dirs:          make(map[string]FileListResult),
		excluded:      make(map[string]struct{}),
		protectedDirs: make(map[string]struct{}),
		specExcluded:  make(map[string]struct{}),
		sizeExcluded:  make(map[string]struct{}),
	}
}

func newFullSyncPlan(src, dst *fullSyncSnapshot, branchGate chan struct{}) *fullSyncPlan {
	return &fullSyncPlan{
		src:          src,
		dst:          dst,
		branchGate:   branchGate,
		newDirs:      make(map[string]fullSyncDir),
		newFiles:     make(map[string]*fullSyncFile),
		extraFiles:   make(map[string]*fullSyncExtraFile),
		extraDeletes: make(map[string]fullSyncDelete),
		blockers:     make(map[string]fullSyncDelete),
	}
}

func (plan *fullSyncPlan) build(job map[string]interface{}) {
	// Under conflictPolicy=2 (keep both), OpenSync renames replaced destination
	// files to *.conflict-YYYYMMDD-HHMMSS.*. Mirror mode must never treat its
	// own conflict backups as extra files to delete on subsequent runs.
	if util.ToInt(job["conflictPolicy"]) == 2 && plan.dst != nil {
		plan.dst.protectConflictBackups()
	}
	plan.compareDir(job, "", "")
}

func (snapshot *fullSyncSnapshot) protectConflictBackups() {
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	var added bool
	for relDir, items := range snapshot.dirs {
		for name := range items {
			if strings.HasSuffix(name, "/") || !isConflictBackupName(name) {
				continue
			}
			relPath := relDir + name
			if snapshot.excluded == nil {
				snapshot.excluded = make(map[string]struct{})
			}
			if snapshot.specExcluded == nil {
				snapshot.specExcluded = make(map[string]struct{})
			}
			if snapshot.protectedDirs == nil {
				snapshot.protectedDirs = make(map[string]struct{})
			}
			snapshot.excluded[relPath] = struct{}{}
			snapshot.specExcluded[relPath] = struct{}{}
			delete(snapshot.sizeExcluded, relPath)
			for ancestor := relDir; ancestor != ""; {
				if _, protected := snapshot.protectedDirs[ancestor]; protected {
					break
				}
				snapshot.protectedDirs[ancestor] = struct{}{}
				ancestor = ancestor[:strings.LastIndex(strings.TrimSuffix(ancestor, "/"), "/")+1]
			}
			added = true
		}
	}
	if added {
		snapshot.subtreeCounts = nil
	}
}

func (plan *fullSyncPlan) compareDir(job map[string]interface{}, srcRelDir, dstRelDir string) {
	srcItems, srcListed := plan.src.dirs[srcRelDir]
	dstItems, dstListed := plan.dst.dirs[dstRelDir]
	// Never compare against an unlisted directory: a failed destination listing
	// (e.g. 403) leaves dstRelDir absent from plan.dst.dirs, and treating a
	// missing map entry as an empty directory would enqueue every source file as
	// an unconditional new copy and bypass conflict/trash rules.
	if !srcListed || !dstListed {
		return
	}
	dstIndex := newDstNameMatchIndex(dstItems)
	srcIndex := newSrcNameMatchIndex(srcItems)
	matchedDst := make(map[string]struct{})
	srcDir := plan.src.root + srcRelDir
	dstDir := plan.dst.root + dstRelDir
	var protectedNames map[string]string
	if _, protected := plan.dst.protectedDirs[dstRelDir]; protected || (dstRelDir == "" && len(plan.dst.excluded) > 0) {
		for name := range dstItems {
			_, excluded := plan.dst.excluded[dstRelDir+name]
			_, ancestor := plan.dst.protectedDirs[dstRelDir+name]
			if excluded || ancestor {
				if protectedNames == nil {
					protectedNames = make(map[string]string)
				}
				key := foldedEntryName(canonicalFileName(name))
				if _, exists := protectedNames[key]; !exists || excluded {
					protectedNames[key] = name
				}
			}
		}
	}

	// A directory present on both sides whose source listing came back empty
	// while the destination still holds entries is the signature of a listing
	// anomaly (a driver returning an empty page, an unmounted share). The
	// executor weighs these against the subtree floor before deleting. The
	// root is included: an empty root is the most destructive anomaly of all
	// and a destination below the floor used to be wiped without any guard.
	if !plan.scanIncomplete && len(srcItems) == 0 && len(dstItems) > 0 {
		plan.mu.Lock()
		plan.emptySrcDirs = append(plan.emptySrcDirs, dstRelDir)
		plan.mu.Unlock()
	}

	// Collect child directory pairs for concurrent comparison.
	type childPair struct{ srcRel, dstRel string }
	var children []childPair
	now := time.Now()
	compareMode := util.ToInt(job["compareMode"])
	conflictPolicy := util.ToInt(job["conflictPolicy"])

	for _, name := range sortedFileListKeys(srcItems) {
		metadata := srcItems[name]
		isDir := strings.HasSuffix(name, "/")
		if len(protectedNames) > 0 {
			if dstName, reserved := protectedNames[foldedEntryName(canonicalFileName(name))]; reserved {
				dstRelPath := dstRelDir + dstName
				_, nameExcluded := plan.dst.specExcluded[dstRelPath]
				typeConflict := isDir != strings.HasSuffix(dstName, "/")
				// A size-only exclusion does not protect the entry from being
				// replaced by an in-range source file; it only keeps it from
				// being deleted when the source has no counterpart.
				if nameExcluded || typeConflict {
					matchedDst[dstName] = struct{}{}
					if typeConflict && !plan.scanIncomplete && (isDir || jobAllowsFileMeta(job, metadata, now)) {
						plan.addBlocker(srcDir, dstDir, dstName, dstItems[dstName], dstDir+name, dstRelPath)
					}
					continue
				}
			}
		}
		if !isDir {
			// The size/time filter decides what gets copied, never what gets deleted.
			// Claim the matching destination entry before skipping so a file that
			// still exists at the source is not treated as an extra file and
			// deleted from the destination.
			allowed := jobAllowsFileMeta(job, metadata, now)
			if !allowed {
				if dstName, _, exists := dstIndex.find(name, srcIndex); exists {
					matchedDst[dstName] = struct{}{}
				}
				// A destination directory of the same name would otherwise be a
				// type conflict removed only so this file could be written. The
				// file is filtered out and never written, so that delete would
				// destroy the directory for nothing.
				if dstName, _, exists := dstIndex.find(name+"/", srcIndex); exists {
					matchedDst[dstName] = struct{}{}
				}
				continue
			}

			if conflictName, conflictMeta, ok := dstIndex.find(name+"/", srcIndex); ok {
				matchedDst[conflictName] = struct{}{}
				if !plan.scanIncomplete {
					plan.addBlocker(srcDir, dstDir, conflictName, conflictMeta, fullSyncObjectPath(dstDir, name), dstRelDir+conflictName)
				}
			}
			dstName, dstMetadata, exists := dstIndex.find(name, srcIndex)
			if exists {
				matchedDst[dstName] = struct{}{}
				if conflictPolicy == 1 {
					// Conflict policy: skip existing destination file.
					continue
				}
				dstRelPath := dstRelDir + dstName
				_, nameExcluded := plan.dst.specExcluded[dstRelPath]
				_, sizeExcluded := plan.dst.sizeExcluded[dstRelPath]
				// The destination copy was skipped by the size/time filter, so it was
				// never compared; the source file is in range now and must
				// replace it. Leaving it in place silently blocked the update
				// forever, with no record saying why.
				if !nameExcluded && (sizeExcluded || fileChangedWithMode(metadata, dstMetadata, compareMode)) {
					plan.addChanged(newFullSyncFile(srcDir, dstDir, name, metadata))
				}
				continue
			}
			plan.addNewFile(newFullSyncFile(srcDir, dstDir, name, metadata))
			continue
		}

		fileName := strings.TrimSuffix(name, "/")
		if conflictName, conflictMeta, ok := dstIndex.find(fileName, srcIndex); ok {
			matchedDst[conflictName] = struct{}{}
			if !plan.scanIncomplete {
				plan.addBlocker(srcDir, dstDir, conflictName, conflictMeta, fullSyncObjectPath(dstDir, fileName)+"/", dstRelDir+conflictName)
			}
		}
		dstName, _, exists := dstIndex.find(name, srcIndex)
		if exists {
			matchedDst[dstName] = struct{}{}
			if _, excluded := plan.dst.excluded[dstRelDir+dstName]; !excluded {
				children = append(children, childPair{srcRelDir + name, dstRelDir + dstName})
			}
			continue
		}
		plan.collectSourceOnly(job, srcRelDir+name, dstRelDir+name)
	}

	if !plan.scanIncomplete {
		// A destination entry is "extra" only if its source counterpart was
		// absent from a complete scan. With a failed listing anywhere, it may
		// simply be in the part of the tree that could not be read.
		for _, name := range sortedFileListKeys(dstItems) {
			if _, matched := matchedDst[name]; matched {
				continue
			}
			plan.collectDestinationOnly(srcRelDir, dstRelDir, name, dstItems[name])
		}
	}

	// Recurse into child directories, forking only while the shared branch
	// budget allows it. A child that cannot get a slot is compared inline, so
	// the walk always makes progress and can never deadlock waiting on itself.
	if len(children) == 1 {
		plan.compareDir(job, children[0].srcRel, children[0].dstRel)
		return
	}
	var wg sync.WaitGroup
	for _, child := range children {
		child := child
		if !plan.acquireBranchSlot() {
			plan.compareDir(job, child.srcRel, child.dstRel)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer plan.releaseBranchSlot()
			// A panic in a plan branch must not take the whole process down (an
			// unrecovered goroutine panic is fatal) and must not let an
			// incomplete plan be executed; record it and let the caller abort.
			defer func() {
				if r := recover(); r != nil {
					plan.fail(fmt.Errorf("full-sync plan branch panic: %v", r))
				}
			}()
			if fullSyncPlanBranchHook != nil {
				fullSyncPlanBranchHook()
			}
			plan.compareDir(job, child.srcRel, child.dstRel)
		}()
	}
	wg.Wait()
}

// acquireBranchSlot takes a slot from the shared branch budget without
// blocking. Plan building is pure in-memory comparison, so waiting for a slot
// would only add latency: falling back to an inline call is strictly better.
func (plan *fullSyncPlan) acquireBranchSlot() bool {
	if plan.branchGate == nil {
		return false
	}
	select {
	case plan.branchGate <- struct{}{}:
		return true
	default:
		return false
	}
}

func (plan *fullSyncPlan) releaseBranchSlot() {
	<-plan.branchGate
}

func (plan *fullSyncPlan) collectSourceOnly(job map[string]interface{}, srcRelDir, dstRelDir string) {
	srcItems, listed := plan.src.dirs[srcRelDir]
	if !listed {
		return
	}
	srcDir := plan.src.root + srcRelDir
	dstDir := plan.dst.root + dstRelDir
	plan.addNewDir(normalizeDirPath(dstDir), fullSyncDir{srcDir: srcDir, dstDir: dstDir})
	now := time.Now()

	for _, name := range sortedFileListKeys(srcItems) {
		metadata := srcItems[name]
		if strings.HasSuffix(name, "/") {
			plan.collectSourceOnly(job, srcRelDir+name, dstRelDir+name)
			continue
		}
		if jobAllowsFileMeta(job, metadata, now) {
			plan.addNewFile(newFullSyncFile(srcDir, dstDir, name, metadata))
		}
	}
}

func (plan *fullSyncPlan) collectDestinationFiles(dstRelDir, deleteRoot string) {
	dstDir := plan.dst.root + dstRelDir
	for _, name := range sortedFileListKeys(plan.dst.dirs[dstRelDir]) {
		metadata := plan.dst.dirs[dstRelDir][name]
		if strings.HasSuffix(name, "/") {
			plan.collectDestinationFiles(dstRelDir+name, deleteRoot)
			continue
		}
		plan.addExtraFile(dstDir, name, metadata, deleteRoot)
	}
}

func (plan *fullSyncPlan) collectDestinationOnly(srcRelDir, dstRelDir, name string, metadata FileMetadata) {
	relPath := dstRelDir + name
	if _, excluded := plan.dst.excluded[relPath]; excluded {
		return
	}
	if _, protected := plan.dst.protectedDirs[relPath]; protected {
		items := plan.dst.dirs[relPath]
		for _, child := range sortedFileListKeys(items) {
			plan.collectDestinationOnly(srcRelDir+name, relPath, child, items[child])
		}
		return
	}
	srcDir := plan.src.root + srcRelDir
	dstDir := plan.dst.root + dstRelDir
	deleteRoot := plan.addExtraDelete(srcDir, dstDir, name, metadata, relPath)
	if strings.HasSuffix(name, "/") {
		plan.collectDestinationFiles(relPath, deleteRoot)
	} else {
		plan.addExtraFile(dstDir, name, metadata, deleteRoot)
	}
}

func (plan *fullSyncPlan) addNewFile(file *fullSyncFile) {
	plan.mu.Lock()
	plan.newFiles[file.target] = file
	plan.mu.Unlock()
}

func (plan *fullSyncPlan) addExtraFile(dir, name string, metadata FileMetadata, deleteRoot string) {
	object := fullSyncObjectPath(dir, name)
	plan.mu.Lock()
	plan.extraFiles[object] = &fullSyncExtraFile{
		dir:        dir,
		name:       name,
		metadata:   metadata,
		object:     object,
		deleteRoot: deleteRoot,
	}
	plan.mu.Unlock()
}

// subtreeKey keeps the snapshot key only for directory entries; a file has no
// subtree of its own.
func subtreeKey(name, relPath string) string {
	if strings.HasSuffix(name, "/") {
		return relPath
	}
	return ""
}

func (plan *fullSyncPlan) addExtraDelete(srcDir, dir, name string, metadata FileMetadata, relPath string) string {
	object := fullSyncObjectPath(dir, name)
	plan.mu.Lock()
	plan.extraDeletes[object] = fullSyncDelete{
		srcDir:   srcDir,
		dir:      dir,
		name:     name,
		metadata: metadata,
		object:   object,
		subtree:  subtreeKey(name, relPath),
	}
	plan.mu.Unlock()
	return object
}

func (plan *fullSyncPlan) addBlocker(srcDir, dir, name string, metadata FileMetadata, blocksPath, relPath string) {
	object := fullSyncObjectPath(dir, name)
	_, excluded := plan.dst.excluded[relPath]
	_, protected := plan.dst.protectedDirs[relPath]
	plan.mu.Lock()
	plan.blockers[object] = fullSyncDelete{
		srcDir:     srcDir,
		dir:        dir,
		name:       name,
		metadata:   metadata,
		object:     object,
		blocksPath: blocksPath,
		protected:  excluded || protected,
		isBlocker:  true,
		subtree:    subtreeKey(name, relPath),
	}
	plan.mu.Unlock()
}

func (plan *fullSyncPlan) addChanged(file *fullSyncFile) {
	plan.mu.Lock()
	plan.changed = append(plan.changed, file)
	plan.mu.Unlock()
}

func (plan *fullSyncPlan) addNewDir(key string, dir fullSyncDir) {
	plan.mu.Lock()
	plan.newDirs[key] = dir
	plan.mu.Unlock()
}

func newFullSyncFile(srcDir, dstDir, name string, metadata FileMetadata) *fullSyncFile {
	return &fullSyncFile{
		srcDir:   srcDir,
		dstDir:   dstDir,
		name:     name,
		metadata: metadata,
		target:   fullSyncObjectPath(dstDir, name),
	}
}

func (plan *fullSyncPlan) incompleteScanReason() string {
	switch {
	case plan.srcScanFailed && plan.dstScanFailed:
		return "full-sync source and destination scans were incomplete; only copies from the scanned entries ran and deletions, relocations and type-conflict removals were skipped"
	case plan.srcScanFailed:
		return "full-sync source scan was incomplete; only copies from the scanned entries ran and deletions, relocations and type-conflict removals were skipped"
	case plan.dstScanFailed:
		return "full-sync destination scan was incomplete; only copies from the scanned entries ran and deletions, relocations and type-conflict removals were skipped"
	default:
		return "full-sync scan was incomplete; only copies from the scanned entries ran and deletions, relocations and type-conflict removals were skipped"
	}
}

func (jt *JobTask) executeFullSyncPlan(plan *fullSyncPlan) {
	if plan.scanIncomplete {
		jt.fullSyncDeleteGuardSkipped.Store(true)
		errMsg := msg.MirrorDeleteGuard(plan.incompleteScanReason())
		// Empty file name: this record can never be replayed as a delete.
		jt.CopyHook("", plan.dst.root, "", nil, "", taskStatusFailed, &errMsg, taskItemPath, taskItemTypeDelete, time.Now().Unix())
	}
	// Every destructive decision is made before the first mutation. Type
	// conflicts are removed first and can take whole directory trees with them,
	// so they pass the same collision check, safety valve and failure cap as
	// the delete phase instead of running ahead of all three.
	repairOnly := jt.hasRetrySource()
	deferDeletes := jt.RequireFullSyncApproval && !repairOnly
	var relocations []fullSyncRelocation
	var writes fullSyncWriteIndex
	var guardReason string
	var deletesUnsafe bool
	if !repairOnly {
		relocations = plan.relocations()
		writes = plan.writeIndex()
		guardReason, deletesUnsafe = plan.unsafeDeleteVolume(plan.plannedDeleteWeight(relocations, writes))
	}
	deletes := &fullSyncDeleteRun{jt: jt, root: plan.dst.root}

	blocked := make(map[string]struct{})
	// writeBlocked tracks paths blocked by real failures (protected conflicts,
	// failed deletions, failed directory creation). Approval-deferred blockers
	// populate blocked so writes under them do not fail on this pass, without
	// poisoning writesFailed against unrelated deletes.
	writeBlocked := make(map[string]struct{})
	var deferredBlockers []fullSyncDelete
	// Remove type conflicts first so missing destination directories can be
	// created before target-side relocations begin. A conflict left in place
	// blocks its replacement: writing over the other type would only fail.
	for _, key := range sortedDeleteKeys(plan.blockers, false) {
		item := plan.blockers[key]
		if item.protected || repairOnly {
			reason := fmt.Sprintf("destination type conflict at %q requires a normal full sync; directory retry does not remove entries", item.object)
			if item.protected {
				reason = fmt.Sprintf("destination type conflict at %q contains or matches an excluded entry", item.object)
			}
			errMsg := msg.MirrorDeleteGuard(reason)
			jt.DelHook(item.dir, item.name, nil, taskStatusFailed, &errMsg, boolToTaskItemObject(item.subtree != ""), time.Now().Unix())
		}
		if item.protected || repairOnly || deletesUnsafe || jt.skipCollidingDelete(writes, item) {
			p := normalizeBlockedPath(item.blocksPath)
			blocked[p] = struct{}{}
			writeBlocked[p] = struct{}{}
			continue
		}
		if deferDeletes {
			blocked[normalizeBlockedPath(item.blocksPath)] = struct{}{}
			deferredBlockers = append(deferredBlockers, item)
			continue
		}
		if !deletes.run(item) {
			p := normalizeBlockedPath(item.blocksPath)
			blocked[p] = struct{}{}
			writeBlocked[p] = struct{}{}
		}
	}

	for _, key := range sortedDirKeys(plan.newDirs) {
		item := plan.newDirs[key]
		if fullSyncPathBlocked(item.dstDir, blocked) {
			continue
		}
		if jt.createFullSyncDir(item) != taskStatusSuccess {
			p := normalizeBlockedPath(item.dstDir)
			blocked[p] = struct{}{}
			writeBlocked[p] = struct{}{}
		}
	}

	active := make([]fullSyncRelocation, 0, len(relocations))
	protectedDeletes := make(map[string]struct{})
	for _, relocation := range relocations {
		if fullSyncPathBlocked(relocation.target.target, blocked) {
			protectedDeletes[relocation.source.deleteRoot] = struct{}{}
			continue
		}
		relocation.item = newCopyItem(
			jt,
			jt.AlistClient,
			relocation.source.dir,
			relocation.target.dstDir,
			relocation.source.name,
			relocation.source.metadata.Size,
			taskItemTypeMove,
		)
		active = append(active, relocation)
	}
	jt.runFullSyncRelocations(active)

	movedTargets := make(map[string]struct{})
	consumedFileDeletes := make(map[string]struct{})
	relocatedOut := make(map[string]struct{})
	for _, relocation := range active {
		if relocation.item.status() == taskStatusSuccess {
			movedTargets[relocation.target.target] = struct{}{}
			relocatedOut[relocation.source.object] = struct{}{}
			if relocation.source.deleteRoot == relocation.source.object {
				consumedFileDeletes[relocation.source.deleteRoot] = struct{}{}
			}
			continue
		}
		protectedDeletes[relocation.source.deleteRoot] = struct{}{}
	}

	writesFailed := &fullSyncWriteFailures{}
	for dir := range writeBlocked {
		writesFailed.protectDir(dir)
	}
	for _, relocation := range active {
		if relocation.item.status() != taskStatusSuccess {
			// A failed relocation left the target without the file it was
			// supposed to hold; deleting a same-named or same-size entry there
			// could remove the only remaining copy.
			writesFailed.record(relocation.target.dstDir, relocation.target.name, relocation.target.metadata.Size, relocation.item.status())
		}
	}
	for _, file := range sortedFullSyncFiles(plan.changed) {
		if !fullSyncPathBlocked(file.target, blocked) {
			jt.queueCopyFileWithConflict(file.srcDir, file.dstDir, plan.dst.root, file.name, file.metadata.Size, taskItemTypeCopy, writesFailed, true)
		}
	}
	for _, target := range sortedNewFileTargets(plan.newFiles) {
		file := plan.newFiles[target]
		if _, moved := movedTargets[target]; moved || fullSyncPathBlocked(file.target, blocked) {
			continue
		}
		jt.queueCopyFile(file.srcDir, file.dstDir, file.name, file.metadata.Size, taskItemTypeCopy, writesFailed)
	}
	if repairOnly {
		return
	}

	suspiciousCounts := plan.suspiciousEmptySourceDirs()
	suspicious := make(map[string]struct{}, len(suspiciousCounts))
	for dir := range suspiciousCounts {
		suspicious[dir] = struct{}{}
	}
	if !deletesUnsafe {
		for _, dir := range sortedBlockedKeys(suspicious) {
			jt.fullSyncDeleteGuardSkipped.Store(true)
			reason := fmt.Sprintf("source directory for %s listed empty while the destination holds %d entries below it; deletes under it were skipped",
				dir, suspiciousCounts[dir])
			log.Printf("Task %d %s", jt.TaskID, reason)
			errMsg := msg.MirrorDeleteGuard(reason)
			// Empty file name: like the global guard record, it can never be
			// replayed as a delete.
			jt.CopyHook("", normalizeDirPath(dir), "", nil, "", taskStatusFailed, &errMsg, taskItemPath, taskItemTypeDelete, time.Now().Unix())
		}
	}

	// Safety valve. Deletions are derived purely from "destination entries the
	// source did not match", so any listing anomaly on the source side is
	// amplified into a mass delete. Individual anomalies are rejected upstream,
	// but the amplifier itself is what turns one of them into data loss, so an
	// implausible deletion volume stops every delete instead of executing it.
	if deletesUnsafe {
		jt.fullSyncDeleteGuardSkipped.Store(true)
		log.Printf("Task %d skipped the full-sync delete phase: %s", jt.TaskID, guardReason)
		errMsg := msg.MirrorDeleteGuard(guardReason)
		// The record exists to show the user why nothing was deleted. It names
		// the destination root with an empty file name so it can never be
		// replayed as a delete: retry refuses delete records without a name.
		jt.CopyHook("", plan.dst.root, "", nil, "", taskStatusFailed, &errMsg, taskItemPath, taskItemTypeDelete, time.Now().Unix())
		return
	}

	if jt.isBreak() || deletes.aborted {
		return
	}
	items := make([]fullSyncDelete, 0, len(deferredBlockers)+len(plan.extraDeletes))
	items = append(items, deferredBlockers...)
	for _, key := range sortedDeleteKeys(plan.extraDeletes, true) {
		if _, protected := protectedDeletes[key]; protected {
			continue
		}
		if _, consumed := consumedFileDeletes[key]; consumed {
			continue
		}
		if fullSyncPathBlocked(key, suspicious) {
			continue
		}
		item := plan.extraDeletes[key]
		if !jt.skipCollidingDelete(writes, item) {
			items = append(items, item)
		}
	}
	if len(items) > 0 {
		jt.fullSyncDeleteMu.Lock()
		jt.fullSyncDeletes = append(jt.fullSyncDeletes, fullSyncDeleteBatch{
			srcRoot:         plan.src.root,
			srcRootNonEmpty: plan.src.entryCount() > 0,
			root:            plan.dst.root,
			dstSnapshot:     plan.dst,
			relocatedOut:    relocatedOut,
			items:           items,
			writesFailed:    writesFailed,
			run:             *deletes,
		})
		jt.fullSyncDeleteMu.Unlock()
	}
}

func (plan *fullSyncPlan) approvalPreview() *FullSyncApproval {
	relocations := plan.relocations()
	writes := plan.writeIndex()
	_, deletesUnsafe := plan.unsafeDeleteVolume(plan.plannedDeleteWeight(relocations, writes))
	suspiciousCounts := plan.suspiciousEmptySourceDirs()
	suspicious := make(map[string]struct{}, len(suspiciousCounts))
	for dir := range suspiciousCounts {
		suspicious[dir] = struct{}{}
	}
	consumed := make(map[string]struct{}, len(relocations))
	relocatedCounts := make(map[string]int, len(relocations))
	relocatedSizes := make(map[string]int64, len(relocations))
	for _, relocation := range relocations {
		if relocation.source.deleteRoot == relocation.source.object {
			consumed[relocation.source.deleteRoot] = struct{}{}
		}
		relocatedCounts[relocation.source.deleteRoot]++
		if relocation.source.metadata.Size > 0 {
			relocatedSizes[relocation.source.deleteRoot] += relocation.source.metadata.Size
		}
	}
	preview := &FullSyncApproval{}
	var signatureParts []string
	add := func(item fullSyncDelete, action string) {
		if item.protected {
			return
		}
		if _, collides := writes.collision(item.dir, item.name); collides {
			return
		}
		itemPath := fullSyncObjectPath(item.dir, item.name)
		if action == "删除" && (deletesUnsafe || fullSyncPathBlocked(itemPath, suspicious)) {
			return
		}
		count := plan.dst.deleteWeight(item)
		size := plan.dst.deleteSize(item)
		if action == "删除" && item.subtree != "" {
			prefix := strings.TrimSuffix(itemPath, "/") + "/"
			for root, relocated := range relocatedCounts {
				if root == itemPath || strings.HasPrefix(root, prefix) {
					count -= relocated
					size -= relocatedSizes[root]
				}
			}
			if count < 1 {
				count = 1
			}
			if size < 0 {
				size = 0
			}
		}
		preview.PathCount++
		if action == "删除" {
			preview.DeleteCount += count
			preview.DeleteSize += size
		}
		signatureParts = append(signatureParts, fmt.Sprintf("%s\x00%s\x00%d\x00%d", action, itemPath, count, size))
		if len(preview.Paths) < fullSyncApprovalPathLimit {
			preview.Paths = append(preview.Paths, FullSyncApprovalPath{
				Path:   itemPath,
				IsPath: strings.HasSuffix(item.name, "/"),
				Count:  count,
				Size:   size,
				Action: action,
			})
		}
	}
	for _, key := range sortedDeleteKeys(plan.blockers, false) {
		add(plan.blockers[key], "删除")
	}
	for _, key := range sortedDeleteKeys(plan.extraDeletes, true) {
		if _, moved := consumed[key]; moved {
			continue
		}
		add(plan.extraDeletes[key], "删除")
	}
	if preview.PathCount == 0 {
		return nil
	}
	sort.Strings(signatureParts)
	sum := sha256.Sum256([]byte(strings.Join(signatureParts, "\x00")))
	preview.signature = fmt.Sprintf("%x", sum[:])
	return preview
}

// fullSyncWriteFailures tracks writes that failed during the copy phase. A
// single failure used to keep every delete of the whole destination root
// skipped forever, so one permanently failing file (a rejected name, a
// permission problem) froze mirror cleanup for the whole job. Deletes are now
// held back only where they could remove the last destination copy of a failed
// write: the same directory, an ancestor/descendant of it, the same file name,
// or — for a rename/move, which keeps the size — the same size.
type fullSyncWriteFailures struct {
	mu    sync.Mutex
	any   bool
	dirs  map[string]struct{}
	names map[string]struct{}
	sizes map[int64]struct{}
}

func (failures *fullSyncWriteFailures) record(dir, name string, size interface{}, status taskStatus) {
	if failures == nil || status == taskStatusSuccess {
		return
	}
	failures.mu.Lock()
	if failures.dirs == nil {
		failures.dirs = make(map[string]struct{})
		failures.names = make(map[string]struct{})
		failures.sizes = make(map[int64]struct{})
	}
	failures.any = true
	failures.dirs[normalizeBlockedPath(dir)] = struct{}{}
	failures.names[foldedEntryName(name)] = struct{}{}
	if size != nil {
		if value := util.ToInt64(size); value >= 0 {
			failures.sizes[value] = struct{}{}
		}
	}
	failures.mu.Unlock()
}

// protectDir marks a destination directory whose own creation/writes did not
// succeed. Deletes inside it are unsafe.
func (failures *fullSyncWriteFailures) protectDir(dir string) {
	failures.mu.Lock()
	if failures.dirs == nil {
		failures.dirs = make(map[string]struct{})
		failures.names = make(map[string]struct{})
		failures.sizes = make(map[int64]struct{})
	}
	failures.any = true
	failures.dirs[normalizeBlockedPath(dir)] = struct{}{}
	failures.mu.Unlock()
}

func (failures *fullSyncWriteFailures) blocks(item fullSyncDelete) bool {
	if failures == nil {
		return false
	}
	failures.mu.Lock()
	defer failures.mu.Unlock()
	if !failures.any {
		return false
	}
	itemPath := normalizeBlockedPath(fullSyncObjectPath(item.dir, item.name))
	for failedDir := range failures.dirs {
		// A delete inside a directory whose write failed, a delete of that
		// directory or one of its ancestors, or a delete of a destination that
		// the failed write targeted.
		if fullSyncPathBlocked(normalizeBlockedPath(item.dir), map[string]struct{}{failedDir: {}}) ||
			fullSyncPathBlocked(itemPath, map[string]struct{}{failedDir: {}}) ||
			fullSyncPathBlocked(failedDir, map[string]struct{}{itemPath: {}}) {
			return true
		}
	}
	if _, ok := failures.names[foldedEntryName(item.name)]; ok {
		return true
	}
	if item.subtree == "" && item.metadata.Size >= 0 {
		if _, ok := failures.sizes[item.metadata.Size]; ok {
			return true
		}
	}
	return false
}

type fullSyncDeleteBatch struct {
	srcRoot         string
	srcRootNonEmpty bool
	root            string
	dstSnapshot     *fullSyncSnapshot
	relocatedOut    map[string]struct{}
	items           []fullSyncDelete
	writesFailed    *fullSyncWriteFailures
	run             fullSyncDeleteRun
}

func (jt *JobTask) runFullSyncDeletes() {
	jt.fullSyncDeleteMu.Lock()
	batches := jt.fullSyncDeletes
	jt.fullSyncDeletes = nil
	jt.fullSyncDeleteMu.Unlock()
	if jt.RequireFullSyncApproval {
		jt.stageDeferredFullSyncDeletes(batches)
		return
	}
	if len(batches) == 0 {
		if !jt.fullSyncDeleteGuardSkipped.Load() && !jt.isBreak() && jt.context().Err() == nil && util.ToInt(jt.Job["method"]) == 1 && !jt.hasRetrySource() {
			if jobID := jt.approvalJobID(); jobID > 0 {
				pendingDeleteApprovals.ClearIfPending(jobID)
			}
		}
		return
	}
	wasDrained := jt.drained.Swap(false)
	anySkippedOrAborted := false
	for _, batch := range batches {
		if jt.isBreak() || jt.context().Err() != nil {
			return
		}
		skipped := 0
		for _, item := range batch.items {
			if jt.isBreak() || jt.context().Err() != nil || batch.run.aborted {
				break
			}
			if batch.writesFailed.blocks(item) {
				skipped++
				continue
			}
			batch.run.run(item)
		}
		if batch.run.aborted {
			anySkippedOrAborted = true
		}
		if skipped > 0 {
			anySkippedOrAborted = true
			reason := fmt.Sprintf("destination writes did not all succeed; %d extra entries that may be the previous copy of a failed write were kept until a clean run", skipped)
			errMsg := msg.MirrorDeleteGuard(reason)
			jt.DelHook(batch.root, "", nil, taskStatusFailed, &errMsg, taskItemPath, time.Now().Unix())
		}
	}
	if !anySkippedOrAborted && !jt.fullSyncDeleteGuardSkipped.Load() && !jt.isBreak() && jt.context().Err() == nil && util.ToInt(jt.Job["method"]) == 1 && !jt.hasRetrySource() {
		if jobID := jt.approvalJobID(); jobID > 0 {
			pendingDeleteApprovals.ClearIfPending(jobID)
		}
	}
	if wasDrained && !jt.isBreak() && jt.context().Err() == nil {
		jt.drained.Store(true)
	}
}

// stageDeferredFullSyncDeletes turns the surviving delete candidates from a
// completed copy phase into a post-positioned pending deletion approval without
// blocking the sync task.
func (jt *JobTask) stageDeferredFullSyncDeletes(batches []fullSyncDeleteBatch) {
	if jt.isBreak() || jt.context().Err() != nil {
		return
	}
	var targets []pendingDeleteTarget
	hadGuardedSkip := jt.fullSyncDeleteGuardSkipped.Load()
	for _, batch := range batches {
		skipped := 0
		for _, item := range batch.items {
			if !item.isBlocker && batch.writesFailed.blocks(item) {
				skipped++
				continue
			}
			if !isSafeDeleteTargetName(item.name) {
				continue
			}
			isDir := strings.HasSuffix(item.name, "/")
			count := 1
			size := item.metadata.Size
			if size < 0 {
				size = 0
			}
			var digest string
			if isDir && batch.dstSnapshot != nil && item.subtree != "" {
				count, size, digest = batch.dstSnapshot.subtreeSummary(item.subtree, batch.relocatedOut)
			} else if isDir {
				size = 0
				count, size, digest = summarizeSubtreeEntries(nil)
			}
			srcDir := item.srcDir
			if srcDir == "" {
				relDir := strings.TrimPrefix(normalizeDirPath(item.dir), normalizeDirPath(batch.root))
				srcDir = normalizeDirPath(batch.srcRoot) + relDir
			}
			targets = append(targets, pendingDeleteTarget{
				SrcRoot:         normalizeDirPath(batch.srcRoot),
				DstRoot:         normalizeDirPath(batch.root),
				SrcDir:          normalizeDirPath(srcDir),
				DstDir:          normalizeDirPath(item.dir),
				Name:            item.name,
				IsDir:           isDir,
				IsBlocker:       item.isBlocker,
				Size:            size,
				Count:           count,
				Modified:        item.metadata.Modified,
				SubtreeDigest:   digest,
				SrcRootNonEmpty: batch.srcRootNonEmpty,
			})
		}
		if skipped > 0 {
			hadGuardedSkip = true
			reason := fmt.Sprintf("destination writes did not all succeed; %d extra entries that may be the previous copy of a failed write were kept until a clean run", skipped)
			errMsg := msg.MirrorDeleteGuard(reason)
			jt.DelHook(batch.root, "", nil, taskStatusFailed, &errMsg, taskItemPath, time.Now().Unix())
		}
	}
	if len(targets) > 0 {
		pendingDeleteApprovals.Stage(jt, targets)
		return
	}
	if !hadGuardedSkip && util.ToInt(jt.Job["method"]) == 1 && !jt.hasRetrySource() {
		if jobID := jt.approvalJobID(); jobID > 0 {
			pendingDeleteApprovals.ClearIfPending(jobID)
		}
	}
}

// maxConsecutiveDeleteErrors stops a run of deletes that keeps failing: the
// destination is most likely unreachable or rejecting writes, and pressing on
// only piles up failed records.
const maxConsecutiveDeleteErrors = 10

// fullSyncDeleteRun executes one plan's deletes under a shared consecutive
// failure cap, so type-conflict removals and extra deletes count together.
type fullSyncDeleteRun struct {
	jt                *JobTask
	root              string
	consecutiveErrors int
	aborted           bool
}

// run deletes item and reports whether it is gone. Once the cap trips, every
// later delete is refused without contacting the server.
func (run *fullSyncDeleteRun) run(item fullSyncDelete) bool {
	if run.aborted || run.jt.isBreak() {
		return false
	}
	if run.jt.delFileWithRoot(run.root, item.dir, item.name, item.metadata.Size) == taskStatusSuccess {
		run.consecutiveErrors = 0
		return true
	}
	run.consecutiveErrors++
	if run.consecutiveErrors >= maxConsecutiveDeleteErrors {
		log.Printf("Task %d aborting delete phase after %d consecutive failures", run.jt.TaskID, run.consecutiveErrors)
		run.aborted = true
	}
	return false
}

// fullSyncWriteIndex holds, per destination directory, the names this plan
// writes (copies, updates, relocation targets, new directories), keyed by a
// case- and Unicode-normalization-folded form of the name. Only directories
// that hold a delete candidate are tracked; writes elsewhere cannot collide.
type fullSyncWriteIndex map[string]map[string]fullSyncWrittenName

// fullSyncWrittenName is the first spelling written under a folded key, and
// whether other spellings were written too.
type fullSyncWrittenName struct {
	name     string
	multiple bool
}

// foldedEntryName is the collision key for destinations that compare names
// case-insensitively or normalize Unicode (NFD on macOS/SMB, many cloud
// drives). It is used only to avoid deleting what is being written, never for
// matching: on a case-sensitive store "Photo.jpg" and "photo.jpg" are distinct.
func foldedEntryName(name string) string {
	return strings.ToLower(norm.NFC.String(strings.TrimSuffix(name, "/")))
}

// track registers dir as holding a delete candidate.
func (index fullSyncWriteIndex) track(dir string) {
	dir = normalizeBlockedPath(dir)
	if index[dir] == nil {
		index[dir] = make(map[string]fullSyncWrittenName)
	}
}

func (index fullSyncWriteIndex) add(dir, name string) {
	byName := index[normalizeBlockedPath(dir)]
	if byName == nil {
		return
	}
	name = strings.TrimSuffix(name, "/")
	folded := foldedEntryName(name)
	written, exists := byName[folded]
	if !exists {
		byName[folded] = fullSyncWrittenName{name: name}
		return
	}
	if written.name != name {
		written.multiple = true
		byName[folded] = written
	}
}

// collision returns a differently spelled name written in dir that folds to
// the same key as name. The exact same spelling is not a collision: a type
// conflict deletes "X/" precisely so "X" can be written.
func (index fullSyncWriteIndex) collision(dir, name string) (string, bool) {
	name = strings.TrimSuffix(name, "/")
	written, exists := index[normalizeBlockedPath(dir)][foldedEntryName(name)]
	if !exists || (written.name == name && !written.multiple) {
		return "", false
	}
	// With several spellings written, at most one equals name, so another one
	// collides; the first spelling is reported for the log.
	return written.name, true
}

func (plan *fullSyncPlan) writeIndex() fullSyncWriteIndex {
	index := make(fullSyncWriteIndex)
	for _, item := range plan.extraDeletes {
		index.track(item.dir)
	}
	for _, item := range plan.blockers {
		index.track(item.dir)
	}
	if len(index) == 0 {
		return index
	}
	for _, file := range plan.changed {
		index.add(file.dstDir, file.name)
	}
	for _, file := range plan.newFiles {
		index.add(file.dstDir, file.name)
	}
	for _, dir := range plan.newDirs {
		clean := normalizeBlockedPath(dir.dstDir)
		index.add(path.Dir(clean), path.Base(clean))
	}
	return index
}

// skipCollidingDelete reports whether item must be kept because the plan is
// writing a name that the destination may treat as the same entry. Deleting
// it could remove the file written by the same run.
func (jt *JobTask) skipCollidingDelete(writes fullSyncWriteIndex, item fullSyncDelete) bool {
	written, collides := writes.collision(item.dir, item.name)
	if collides {
		log.Printf("Task %d kept %q in %q: it may be the same entry as %q being written there (case or Unicode normalization)",
			jt.TaskID, item.name, item.dir, written)
	}
	return collides
}

// plannedDeleteWeight is an upper bound on the destination entries this plan
// removes. A directory delete is recursive, so it weighs everything beneath it
// in the destination snapshot; files relocated out of a doomed tree are moved,
// not deleted, and are subtracted. Deletes the collision check will keep are
// not counted. A relocation that later fails only protects its tree, so the
// actual volume never exceeds this bound.
func (plan *fullSyncPlan) plannedDeleteWeight(relocations []fullSyncRelocation, writes fullSyncWriteIndex) int {
	relocatedOut := make(map[string]int)
	for _, relocation := range relocations {
		relocatedOut[relocation.source.deleteRoot]++
	}
	weight := 0
	add := func(items map[string]fullSyncDelete) {
		for key, item := range items {
			if _, collides := writes.collision(item.dir, item.name); collides || item.protected {
				continue
			}
			w := plan.dst.deleteWeight(item) - relocatedOut[key]
			if w > 0 {
				weight += w
			}
		}
	}
	add(plan.blockers)
	add(plan.extraDeletes)
	return weight
}

// mirrorDeleteRatioThreshold / mirrorDeleteFloor gate the delete phase. Both
// must be exceeded: a ratio alone would block routine cleanup of a small
// directory, and an absolute count alone would block a legitimate large purge.
const (
	mirrorDeleteRatioThreshold = 0.5
	mirrorDeleteFloor          = 100
)

// unsafeDeleteVolume reports whether the planned deletions are too large a share
// of the destination to be plausible. deleteCount must be the weighted count
// (plannedDeleteWeight), in the same unit as entryCount: counting a recursive
// directory delete as one entry would let a missing source subtree of any size
// slip under the floor.
func (plan *fullSyncPlan) unsafeDeleteVolume(deleteCount int) (string, bool) {
	if deleteCount < mirrorDeleteFloor {
		return "", false
	}
	total := plan.dst.entryCount()
	if total == 0 {
		return "", false
	}
	ratio := float64(deleteCount) / float64(total)
	if ratio <= mirrorDeleteRatioThreshold {
		return "", false
	}
	return fmt.Sprintf("planned deletions %d of %d destination entries (%.0f%%) exceed the %.0f%% safety threshold",
		deleteCount, total, ratio*100, mirrorDeleteRatioThreshold*100), true
}

// suspiciousEmptySourceDirs returns, keyed by normalized absolute destination
// path, the matched directories whose source listing was empty while the
// destination subtree below them holds at least mirrorDeleteFloor entries.
//
// The global valve weighs deletes against the whole destination, so one
// subdirectory whose source listing came back empty (an unmounted share, a
// driver returning an empty page) is deleted outright when it is a minority of
// a large tree. Emptying a directory that large in one go is rare enough that
// keeping it for a run, with a visible record, is the conservative choice; a
// directory removed from the source entirely is not affected.
func (plan *fullSyncPlan) suspiciousEmptySourceDirs() map[string]int {
	result := make(map[string]int)
	for _, rel := range plan.emptySrcDirs {
		count := plan.dst.subtreeEntryCount(rel)
		if rel == "" {
			// subtreeEntryCount does not credit the root itself.
			count = plan.dst.entryCount()
		}
		if count <= 0 {
			continue
		}
		// A subdirectory needs the floor to be considered anomalous; the root
		// does not. While a small emptied directory is ordinary mirror
		// cleanup, an empty root would empty the whole destination, and a
		// destination with fewer than 100 entries would otherwise slip under
		// every guard.
		if rel != "" && count < mirrorDeleteFloor {
			continue
		}
		result[normalizeBlockedPath(plan.dst.root+rel)] = count
	}
	return result
}

func sortedBlockedKeys(items map[string]struct{}) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// entryCount totals the entries recorded across the scanned tree.
func (snapshot *fullSyncSnapshot) entryCount() int {
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	total := 0
	for _, items := range snapshot.dirs {
		total += len(items)
	}
	return total - len(snapshot.excluded)
}

// deleteWeight is how many snapshot entries deleting item removes: the entry
// itself plus, for a directory, every entry recorded beneath it.
func (snapshot *fullSyncSnapshot) deleteWeight(item fullSyncDelete) int {
	if item.subtree == "" {
		return 1
	}
	return 1 + snapshot.subtreeEntryCount(item.subtree)
}

func (snapshot *fullSyncSnapshot) deleteSize(item fullSyncDelete) int64 {
	if item.subtree == "" {
		if item.metadata.Size < 0 {
			return 0
		}
		return item.metadata.Size
	}
	root := strings.TrimSuffix(item.subtree, "/")
	var total int64
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	for dir, items := range snapshot.dirs {
		if dir != item.subtree && !strings.HasPrefix(dir, item.subtree) {
			continue
		}
		for name, metadata := range items {
			if strings.HasSuffix(name, "/") || metadata.Size < 0 {
				continue
			}
			if dir == item.subtree || strings.HasPrefix(strings.TrimSuffix(dir, "/"), root) {
				total += metadata.Size
			}
		}
	}
	return total
}

// subtreeSummary computes the exact remaining entry count, total file bytes,
// and content digest of a directory subtree after subtracting excluded entries
// and files already relocated out of the directory during the sync phase.
func (snapshot *fullSyncSnapshot) subtreeSummary(subtree string, relocatedOut map[string]struct{}) (count int, size int64, digest string) {
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	var entries []subtreeDigestEntry
	for dir, items := range snapshot.dirs {
		if dir != subtree && !strings.HasPrefix(dir, subtree) {
			continue
		}
		subRelDir := strings.TrimPrefix(dir, subtree)
		for name, meta := range items {
			if _, excluded := snapshot.excluded[dir+name]; excluded {
				continue
			}
			if len(relocatedOut) > 0 {
				if _, moved := relocatedOut[fullSyncObjectPath(snapshot.root+dir, name)]; moved {
					continue
				}
			}
			entries = append(entries, subtreeDigestEntry{
				relPath:  subRelDir + name,
				isDir:    strings.HasSuffix(name, "/"),
				size:     meta.Size,
				modified: meta.Modified,
			})
		}
	}
	return summarizeSubtreeEntries(entries)
}

// subtreeEntryCount totals the entries recorded in relDir and all directories
// below it. Counts for every directory are built in one pass on first use, so
// weighing many delete roots stays linear in the snapshot size.
func (snapshot *fullSyncSnapshot) subtreeEntryCount(relDir string) int {
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	if snapshot.subtreeCounts == nil {
		counts := make(map[string]int, len(snapshot.dirs))
		for dir, items := range snapshot.dirs {
			n := len(items)
			if _, protected := snapshot.protectedDirs[dir]; protected {
				for name := range items {
					if _, excluded := snapshot.excluded[dir+name]; excluded {
						n--
					}
				}
			}
			// Credit the directory and each ancestor ("a/b/" -> "a/b/", "a/").
			for ancestor := dir; ancestor != ""; {
				counts[ancestor] += n
				trimmed := strings.TrimSuffix(ancestor, "/")
				idx := strings.LastIndex(trimmed, "/")
				if idx < 0 {
					break
				}
				ancestor = trimmed[:idx+1]
			}
		}
		snapshot.subtreeCounts = counts
	}
	return snapshot.subtreeCounts[relDir]
}

func (plan *fullSyncPlan) relocations() []fullSyncRelocation {
	targetGroups := make(map[string][]*fullSyncFile)
	for _, file := range plan.newFiles {
		key := fullSyncRelocationKey(file.name, file.metadata)
		if key != "" {
			targetGroups[key] = append(targetGroups[key], file)
		}
	}
	sourceGroups := make(map[string][]*fullSyncExtraFile)
	for _, file := range plan.extraFiles {
		key := fullSyncRelocationKey(file.name, file.metadata)
		if key != "" {
			sourceGroups[key] = append(sourceGroups[key], file)
		}
	}

	keys := make([]string, 0, len(targetGroups))
	for key := range targetGroups {
		if len(sourceGroups[key]) > 0 {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	var result []fullSyncRelocation
	for _, key := range keys {
		targets := targetGroups[key]
		sources := sourceGroups[key]
		sort.Slice(targets, func(i, j int) bool { return targets[i].target < targets[j].target })
		sort.Slice(sources, func(i, j int) bool { return sources[i].object < sources[j].object })
		count := len(targets)
		if len(sources) < count {
			count = len(sources)
		}
		for i := 0; i < count; i++ {
			result = append(result, fullSyncRelocation{source: sources[i], target: targets[i]})
		}
	}
	return result
}

// runFullSyncRelocations starts the destination-side moves and waits for them.
// The caller inspects each item's terminal status to decide which deletes are
// safe, so this has to be synchronous. Admission goes through the shared
// in-flight budget: this path runs on the scan goroutine while the submit
// executor is also starting copies, and both honouring the limit independently
// would put twice the configured concurrency on the AList server. Waiting is
// scoped to this batch so unrelated copies do not serialize it.
func (jt *JobTask) runFullSyncRelocations(relocations []fullSyncRelocation) {
	if len(relocations) == 0 {
		return
	}
	limit := jt.taskLimits().CopyConcurrency
	if limit < 1 {
		limit = 1
	}

	var batch sync.WaitGroup
	for i := range relocations {
		if jt.isBreak() {
			break
		}
		// waitForCopySlot reserves the slot atomically (same accounting as the
		// submit executor), so two starters can no longer overshoot
		// CopyConcurrency; launch the admitted item without re-checking.
		if !jt.waitForCopySlot(limit) {
			break
		}
		jt.launchAdmittedCopy(relocations[i].item, &batch)
	}
	batch.Wait()
}

func (jt *JobTask) createFullSyncDir(item fullSyncDir) taskStatus {
	return jt.mkdirAndRecord(item.srcDir, item.dstDir, taskItemTypeCopy)
}

func fullSyncRelocationKey(name string, metadata FileMetadata) string {
	// Size-only matches are deliberately excluded: moving the wrong target file
	// is more damaging than falling back to the existing copy-and-delete path.
	// The digest also has to be well-formed, so a driver that returns a constant
	// placeholder for every file cannot make unrelated files look interchangeable.
	md5 := normalizeMD5(metadata.MD5)
	if !isMD5Digest(md5) || strings.Contains(name, "/") {
		return ""
	}
	return fmt.Sprintf("%s\x00%d\x00%s", name, metadata.Size, md5)
}

func fullSyncObjectPath(dir, name string) string {
	return path.Join(strings.TrimSpace(dir), strings.TrimSuffix(name, "/"))
}

func normalizeBlockedPath(value string) string {
	return path.Clean(strings.TrimSpace(value))
}

func fullSyncPathBlocked(value string, blocked map[string]struct{}) bool {
	if len(blocked) == 0 {
		return false
	}
	value = normalizeBlockedPath(value)
	// Check exact match first (O(1)).
	if _, ok := blocked[value]; ok {
		return true
	}
	// Walk ancestors upward: O(depth) instead of O(len(blocked)).
	for {
		parent := path.Dir(value)
		if parent == value {
			break
		}
		if _, ok := blocked[parent]; ok {
			return true
		}
		if parent == "." || parent == "/" {
			break
		}
		value = parent
	}
	return false
}

func sortedFileListKeys(items FileListResult) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedDirKeys(items map[string]fullSyncDir) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		leftDepth := strings.Count(strings.Trim(keys[i], "/"), "/")
		rightDepth := strings.Count(strings.Trim(keys[j], "/"), "/")
		if leftDepth == rightDepth {
			return keys[i] < keys[j]
		}
		return leftDepth < rightDepth
	})
	return keys
}

func sortedDeleteKeys(items map[string]fullSyncDelete, deepestFirst bool) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		leftDepth := strings.Count(strings.Trim(keys[i], "/"), "/")
		rightDepth := strings.Count(strings.Trim(keys[j], "/"), "/")
		if leftDepth == rightDepth {
			return keys[i] < keys[j]
		}
		if deepestFirst {
			return leftDepth > rightDepth
		}
		return leftDepth < rightDepth
	})
	return keys
}

func sortedNewFileTargets(items map[string]*fullSyncFile) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedFullSyncFiles(items []*fullSyncFile) []*fullSyncFile {
	result := append([]*fullSyncFile(nil), items...)
	sort.Slice(result, func(i, j int) bool { return result[i].target < result[j].target })
	return result
}
