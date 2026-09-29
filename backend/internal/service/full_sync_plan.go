package service

import (
	"fmt"
	"log"
	"opensync/internal/msg"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/text/unicode/norm"
)

type fullSyncSnapshot struct {
	root string
	dirs map[string]FileListResult
	mu   sync.Mutex
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
	dir        string
	name       string
	metadata   FileMetadata
	object     string
	blocksPath string
	// subtree is the destination snapshot key of a directory entry, so the
	// safety valve can weigh a recursive delete by everything it removes.
	subtree string
}

type fullSyncRelocation struct {
	source *fullSyncExtraFile
	target *fullSyncFile
	item   *CopyItem
}

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
}

func (jt *JobTask) syncFull(work scanWork, spec *excludeMatcher) {
	// Full sync needs both complete trees before it mutates the destination;
	// otherwise an old path may be deleted before it can satisfy a moved file.
	var srcSnapshot, dstSnapshot *fullSyncSnapshot
	var srcErr, dstErr error

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer jt.recoverWorkerPanic("full-sync source scan", &srcErr)
		srcSnapshot, srcErr = jt.scanFullSyncTree(work.SrcPath, work.FirstDst, spec, true)
	}()
	go func() {
		defer wg.Done()
		defer jt.recoverWorkerPanic("full-sync destination scan", &dstErr)
		dstSnapshot, dstErr = jt.scanFullSyncTree(work.DstPath, work.FirstDst, spec, false)
	}()
	wg.Wait()

	if srcErr != nil || jt.isBreak() {
		return
	}
	if dstErr != nil {
		// The destination root may simply not exist yet on a first run. Create it
		// and rescan once; anything else is a real failure.
		if !jt.ensureDstDirAfterListError(work.DstPath, dstErr) {
			return
		}
		dstSnapshot, dstErr = jt.scanFullSyncTree(work.DstPath, work.FirstDst, spec, false)
		if dstErr != nil || jt.isBreak() {
			return
		}
	}

	plan := newFullSyncPlan(srcSnapshot, dstSnapshot, jt.scanBranchSem)
	plan.build(jt.Job)
	jt.executeFullSyncPlan(plan)
}

func (jt *JobTask) scanFullSyncTree(root string, firstDst bool, spec *excludeMatcher, isSrc bool) (*fullSyncSnapshot, error) {
	root = normalizeDirPath(root)
	snapshot := &fullSyncSnapshot{
		root: root,
		dirs: make(map[string]FileListResult),
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

		items, err := jt.listDir(root+relDir, firstDst, spec, root, isSrc)
		if err != nil {
			errMu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			errMu.Unlock()
			scanFailed.Store(true)
			return
		}
		snapshot.mu.Lock()
		snapshot.dirs[relDir] = items
		snapshot.mu.Unlock()

		children := make([]string, 0)
		for name := range items {
			if strings.HasSuffix(name, "/") {
				children = append(children, relDir+name)
			}
		}
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
	plan.compareDir(job, "", "")
}

func (plan *fullSyncPlan) compareDir(job map[string]interface{}, srcRelDir, dstRelDir string) {
	srcItems := plan.src.dirs[srcRelDir]
	dstItems := plan.dst.dirs[dstRelDir]
	dstIndex := newDstNameMatchIndex(dstItems)
	srcIndex := newSrcNameMatchIndex(srcItems)
	matchedDst := make(map[string]struct{})
	srcDir := plan.src.root + srcRelDir
	dstDir := plan.dst.root + dstRelDir

	// Collect child directory pairs for concurrent comparison.
	type childPair struct{ srcRel, dstRel string }
	var children []childPair

	for _, name := range sortedFileListKeys(srcItems) {
		metadata := srcItems[name]
		if !strings.HasSuffix(name, "/") {
			// The size filter decides what gets copied, never what gets deleted.
			// Claim the matching destination entry before skipping so a file that
			// still exists at the source is not treated as an extra file and
			// deleted from the destination.
			allowed := jobAllowsFileSize(job, metadata.Size)
			if !allowed {
				if dstName, _, exists := dstIndex.find(name, srcIndex); exists {
					matchedDst[dstName] = struct{}{}
				}
				continue
			}

			if conflictName, conflictMeta, ok := dstIndex.find(name+"/", srcIndex); ok {
				matchedDst[conflictName] = struct{}{}
				plan.addBlocker(dstDir, conflictName, conflictMeta, fullSyncObjectPath(dstDir, name), dstRelDir+conflictName)
			}
			dstName, dstMetadata, exists := dstIndex.find(name, srcIndex)
			if exists {
				matchedDst[dstName] = struct{}{}
				if fileChanged(metadata, dstMetadata) {
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
			plan.addBlocker(dstDir, conflictName, conflictMeta, fullSyncObjectPath(dstDir, fileName)+"/", dstRelDir+conflictName)
		}
		dstName, _, exists := dstIndex.find(name, srcIndex)
		if exists {
			matchedDst[dstName] = struct{}{}
			children = append(children, childPair{srcRelDir + name, dstRelDir + dstName})
			continue
		}
		plan.collectSourceOnly(job, srcRelDir+name, dstRelDir+name)
	}

	for _, name := range sortedFileListKeys(dstItems) {
		if _, matched := matchedDst[name]; matched {
			continue
		}
		metadata := dstItems[name]
		deleteRoot := plan.addExtraDelete(dstDir, name, metadata, dstRelDir+name)
		if strings.HasSuffix(name, "/") {
			plan.collectDestinationFiles(dstRelDir+name, deleteRoot)
			continue
		}
		plan.addExtraFile(dstDir, name, metadata, deleteRoot)
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
	srcDir := plan.src.root + srcRelDir
	dstDir := plan.dst.root + dstRelDir
	plan.addNewDir(normalizeDirPath(dstDir), fullSyncDir{srcDir: srcDir, dstDir: dstDir})

	for _, name := range sortedFileListKeys(plan.src.dirs[srcRelDir]) {
		metadata := plan.src.dirs[srcRelDir][name]
		if strings.HasSuffix(name, "/") {
			plan.collectSourceOnly(job, srcRelDir+name, dstRelDir+name)
			continue
		}
		if jobAllowsFileSize(job, metadata.Size) {
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

func (plan *fullSyncPlan) addExtraDelete(dir, name string, metadata FileMetadata, relPath string) string {
	object := fullSyncObjectPath(dir, name)
	plan.mu.Lock()
	plan.extraDeletes[object] = fullSyncDelete{
		dir:      dir,
		name:     name,
		metadata: metadata,
		object:   object,
		subtree:  subtreeKey(name, relPath),
	}
	plan.mu.Unlock()
	return object
}

func (plan *fullSyncPlan) addBlocker(dir, name string, metadata FileMetadata, blocksPath, relPath string) {
	object := fullSyncObjectPath(dir, name)
	plan.mu.Lock()
	plan.blockers[object] = fullSyncDelete{
		dir:        dir,
		name:       name,
		metadata:   metadata,
		object:     object,
		blocksPath: blocksPath,
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

func (jt *JobTask) executeFullSyncPlan(plan *fullSyncPlan) {
	// Every destructive decision is made before the first mutation. Type
	// conflicts are removed first and can take whole directory trees with them,
	// so they pass the same collision check, safety valve and failure cap as
	// the delete phase instead of running ahead of all three.
	relocations := plan.relocations()
	writes := plan.writeIndex()
	guardReason, deletesUnsafe := plan.unsafeDeleteVolume(plan.plannedDeleteWeight(relocations, writes))
	deletes := &fullSyncDeleteRun{jt: jt}

	blocked := make(map[string]struct{})
	// Remove type conflicts first so missing destination directories can be
	// created before target-side relocations begin. A conflict left in place
	// blocks its replacement: writing over the other type would only fail.
	for _, key := range sortedDeleteKeys(plan.blockers, false) {
		item := plan.blockers[key]
		if deletesUnsafe || jt.skipCollidingDelete(writes, item) || !deletes.run(item) {
			blocked[normalizeBlockedPath(item.blocksPath)] = struct{}{}
		}
	}

	for _, key := range sortedDirKeys(plan.newDirs) {
		item := plan.newDirs[key]
		if fullSyncPathBlocked(item.dstDir, blocked) {
			continue
		}
		if jt.createFullSyncDir(item) != taskStatusSuccess {
			blocked[normalizeBlockedPath(item.dstDir)] = struct{}{}
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
	for _, relocation := range active {
		if relocation.item.status() == taskStatusSuccess {
			movedTargets[relocation.target.target] = struct{}{}
			if relocation.source.deleteRoot == relocation.source.object {
				consumedFileDeletes[relocation.source.deleteRoot] = struct{}{}
			}
			continue
		}
		protectedDeletes[relocation.source.deleteRoot] = struct{}{}
	}

	for _, file := range sortedFullSyncFiles(plan.changed) {
		if !fullSyncPathBlocked(file.target, blocked) {
			jt.queueCopyFile(file.srcDir, file.dstDir, file.name, file.metadata.Size, taskItemTypeCopy)
		}
	}
	for _, target := range sortedNewFileTargets(plan.newFiles) {
		file := plan.newFiles[target]
		if _, moved := movedTargets[target]; moved || fullSyncPathBlocked(file.target, blocked) {
			continue
		}
		jt.queueCopyFile(file.srcDir, file.dstDir, file.name, file.metadata.Size, taskItemTypeCopy)
	}

	deleteKeys := make([]string, 0, len(plan.extraDeletes))
	for _, key := range sortedDeleteKeys(plan.extraDeletes, true) {
		if _, protected := protectedDeletes[key]; protected {
			continue
		}
		if _, consumed := consumedFileDeletes[key]; consumed {
			continue
		}
		deleteKeys = append(deleteKeys, key)
	}

	// Safety valve. Deletions are derived purely from "destination entries the
	// source did not match", so any listing anomaly on the source side is
	// amplified into a mass delete. Individual anomalies are rejected upstream,
	// but the amplifier itself is what turns one of them into data loss, so an
	// implausible deletion volume stops every delete instead of executing it.
	if deletesUnsafe {
		log.Printf("Task %d skipped the full-sync delete phase: %s", jt.TaskID, guardReason)
		errMsg := msg.MirrorDeleteGuard(guardReason)
		// The record exists to show the user why nothing was deleted. It names
		// the destination root with an empty file name so it can never be
		// replayed as a delete: retry refuses delete records without a name.
		jt.CopyHook("", plan.dst.root, "", nil, "", taskStatusFailed, &errMsg, taskItemPath, taskItemTypeDelete, time.Now().Unix())
		return
	}

	for _, key := range deleteKeys {
		if jt.isBreak() || deletes.aborted {
			break
		}
		item := plan.extraDeletes[key]
		if jt.skipCollidingDelete(writes, item) {
			continue
		}
		deletes.run(item)
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
	consecutiveErrors int
	aborted           bool
}

// run deletes item and reports whether it is gone. Once the cap trips, every
// later delete is refused without contacting the server.
func (run *fullSyncDeleteRun) run(item fullSyncDelete) bool {
	if run.aborted || run.jt.isBreak() {
		return false
	}
	if run.jt.delFile(item.dir, item.name, item.metadata.Size) == taskStatusSuccess {
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
// it would run concurrently with the queued copy into that entry, removing the
// file mid-upload and re-triggering the copy on every round.
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
			if _, collides := writes.collision(item.dir, item.name); collides {
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

// entryCount totals the entries recorded across the scanned tree.
func (snapshot *fullSyncSnapshot) entryCount() int {
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	total := 0
	for _, items := range snapshot.dirs {
		total += len(items)
	}
	return total
}

// deleteWeight is how many snapshot entries deleting item removes: the entry
// itself plus, for a directory, every entry recorded beneath it.
func (snapshot *fullSyncSnapshot) deleteWeight(item fullSyncDelete) int {
	if item.subtree == "" {
		return 1
	}
	return 1 + snapshot.subtreeEntryCount(item.subtree)
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
		if !jt.waitForCopySlot(limit) {
			break
		}
		jt.startCopyItemTracked(relocations[i].item, &batch)
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
		if parent == value || parent == "." || parent == "/" {
			break
		}
		if _, ok := blocked[parent]; ok {
			return true
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
