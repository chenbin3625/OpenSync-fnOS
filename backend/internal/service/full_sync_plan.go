package service

import (
	"fmt"
	"log"
	"opensync/pkg/util"
	"path"
	"sort"
	"strings"
	"sync"
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
	// deepCheckSameSize is set for native pairs whose compareMode cannot be
	// decided from listing metadata alone (e.g. explicit compareMode=2 on a
	// hashless backend). Same-size files without both MD5 hashes are routed to
	// rclone check --download.
	deepCheckSameSize bool
	// compareMode overrides util.ToInt(job["compareMode"]) when hasCompareMode
	// is set (e.g. effective size-only compare for OpenList / standard WebDAV).
	compareMode    int
	hasCompareMode bool
	// planErr is the first panic captured from a concurrent compareDir branch.
	// A partially built plan is never executed: it could not have weighed its
	// own deletions and may treat unmatched entries as extra.
	planErr          error
	trackRenames     bool
	forceSourcePaths map[string]bool
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
	if extra, _, err := parseNativeExtraFlags(job["extraFlags"]); err == nil {
		plan.trackRenames = extra.TrackRenames
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
	if plan.hasCompareMode {
		compareMode = plan.compareMode
	}
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
				changed := fileChangedWithMode(metadata, dstMetadata, compareMode) || plan.forceSourcePaths[path.Join(srcDir, name)]
				if !changed && plan.deepCheckSameSize && metadata.Size == dstMetadata.Size && !(isMD5Digest(metadata.MD5) && isMD5Digest(dstMetadata.MD5)) {
					changed = true
				}
				if !nameExcluded && (sizeExcluded || changed) {
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

const emptySourceSubdirMinEntries = 100

// suspiciousEmptySourceDirs returns, keyed by normalized absolute destination
// path, the matched directories whose source listing was empty while the
// destination contains entries. An empty root is always checked, and an empty
// subdirectory is checked when its destination holds at least 100 entries.
// An empty listing can be caused by an unmounted share or a storage driver.
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
		if rel != "" && count < emptySourceSubdirMinEntries {
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

// subtreeEntryCount totals the entries recorded in relDir and all directories
// below it. Counts for every directory are built in one pass on first use.
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
	matchedTargets := make(map[string]struct{})
	matchedSources := make(map[string]struct{})
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
			matchedTargets[targets[i].target] = struct{}{}
			matchedSources[sources[i].object] = struct{}{}
		}
	}

	// Pass 2: When --track-renames is enabled on Full Sync, also match remaining
	// renamed files (different filename) with identical non-zero size and valid MD5.
	if plan.trackRenames {
		renameTargets := make(map[string][]*fullSyncFile)
		for _, file := range plan.newFiles {
			if _, used := matchedTargets[file.target]; used {
				continue
			}
			key := fullSyncRenameKey(file.metadata)
			if key != "" {
				renameTargets[key] = append(renameTargets[key], file)
			}
		}
		renameSources := make(map[string][]*fullSyncExtraFile)
		for _, file := range plan.extraFiles {
			if _, used := matchedSources[file.object]; used {
				continue
			}
			key := fullSyncRenameKey(file.metadata)
			if key != "" {
				renameSources[key] = append(renameSources[key], file)
			}
		}
		renameKeys := make([]string, 0, len(renameTargets))
		for key := range renameTargets {
			if len(renameSources[key]) > 0 {
				renameKeys = append(renameKeys, key)
			}
		}
		sort.Strings(renameKeys)
		for _, key := range renameKeys {
			targets := renameTargets[key]
			sources := renameSources[key]
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
	}
	return result
}

func fullSyncRenameKey(metadata FileMetadata) string {
	// Require non-zero size + well-formed MD5 so empty placeholder files are
	// never cross-renamed arbitrarily.
	if metadata.Size <= 0 {
		return ""
	}
	md5 := normalizeMD5(metadata.MD5)
	if !isMD5Digest(md5) {
		return ""
	}
	return fmt.Sprintf("%d\x00%s", metadata.Size, md5)
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
