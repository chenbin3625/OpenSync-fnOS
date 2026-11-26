package service

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"opensync/internal/mapper"
	"opensync/internal/native"
)

// BisyncNode describes the live observed state of one file or directory on one side.
type BisyncNode struct {
	RelPath  string
	IsDir    bool
	Size     int64
	ModTime  int64
	Hash     string
	Inode    uint64
	ObjectID string
}

// BisyncEndpointCaps captures backend capabilities relevant to 3-way decisions.
type BisyncEndpointCaps struct {
	Side              string // "A" or "B"
	Label             string // Human-readable side name, e.g. "本地" / "云端"
	IsLocal           bool
	CaseInsensitive   bool
	UnreliableModTime bool
	SupportsMD5       bool
	Endpoint          native.Endpoint
}

// BisyncPlanInput is the pure input to BuildBisyncPlan.
type BisyncPlanInput struct {
	JobID            int64
	Now              time.Time
	CompareMode      int   // 0: size+mtime, 1: size, 2: size+hash
	ModifyWindowSec  int64 // default 2s when <= 0
	ConflictPolicy   int   // 0: newer wins, 1: path1/A wins, 2: keep both
	MaxDeletePercent int   // 0 => default 50; 1..100 => percentage guard
	ForceDelete      bool  // bypass delete volume guard
	CapsA            BisyncEndpointCaps
	CapsB            BisyncEndpointCaps
	Base             map[string]mapper.BisyncEntry
	SnapA            map[string]BisyncNode
	SnapB            map[string]BisyncNode
	Excluded         map[string]bool
	OnlyPaths        []string // optional subset for fast single-path delta
}

type BisyncMove struct {
	From  string
	To    string
	NodeA BisyncNode
	NodeB BisyncNode
	Base  mapper.BisyncEntry
}

type BisyncTransfer struct {
	RelPath string
	Size    int64
	Updated bool
	Reason  string
	NodeSrc BisyncNode
	NodeDst *BisyncNode
}

type BisyncDelete struct {
	RelPath string
	IsDir   bool
	Size    int64
	ModTime int64
	Reason  string
}

type BisyncPathRef struct {
	RelPath string
	IsDir   bool
}

type BisyncConflictAction struct {
	RelPath          string
	ConflictType     string // content | case | type | invalid_name
	WinnerSide       string // "A" | "B" | ""
	ConflictCopyPath string
	RenameSide       string // side ("A" or "B") where RelPath is renamed to ConflictCopyPath
	CopyWinnerA2B    bool
	CopyWinnerB2A    bool
	CopyConflictA2B  bool
	CopyConflictB2A  bool
	NodeA            *BisyncNode
	NodeB            *BisyncNode
	Detail           string
}

// BisyncPlan is the deterministic execution plan produced by BuildBisyncPlan.
type BisyncPlan struct {
	Adopt              []mapper.BisyncEntry
	MarkIgnored        []mapper.BisyncEntry
	MarkTombstone      []BisyncPathRef
	MkdirA             []string
	MkdirB             []string
	MoveA              []BisyncMove
	MoveB              []BisyncMove
	CopyA2B            []BisyncTransfer
	CopyB2A            []BisyncTransfer
	DeleteA            []BisyncDelete
	DeleteB            []BisyncDelete
	Conflicts          []BisyncConflictAction
	DeleteGuardA       bool
	DeleteGuardB       bool
	DeleteGuardReasonA string
	DeleteGuardReasonB string
}

func (p *BisyncPlan) HasWork() bool {
	if p == nil {
		return false
	}
	return len(p.Adopt) > 0 ||
		len(p.MarkIgnored) > 0 ||
		len(p.MarkTombstone) > 0 ||
		len(p.MkdirA) > 0 ||
		len(p.MkdirB) > 0 ||
		len(p.MoveA) > 0 ||
		len(p.MoveB) > 0 ||
		len(p.CopyA2B) > 0 ||
		len(p.CopyB2A) > 0 ||
		len(p.DeleteA) > 0 ||
		len(p.DeleteB) > 0 ||
		len(p.Conflicts) > 0
}

// BuildBisyncPlan runs a pure 3-way merge (Base vs A vs B) and emits a
// conflict-aware, rename-aware, per-entry execution plan.
func BuildBisyncPlan(in BisyncPlanInput) *BisyncPlan {
	plan := &BisyncPlan{}
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	nowUnix := now.Unix()
	modWin := in.ModifyWindowSec
	if modWin <= 0 {
		modWin = 2
	}
	if in.Base == nil {
		in.Base = map[string]mapper.BisyncEntry{}
	}
	if in.SnapA == nil {
		in.SnapA = map[string]BisyncNode{}
	}
	if in.SnapB == nil {
		in.SnapB = map[string]BisyncNode{}
	}
	if in.Excluded == nil {
		in.Excluded = map[string]bool{}
	}

	// Collect candidate paths.
	pathSet := make(map[string]struct{})
	if len(in.OnlyPaths) > 0 {
		for _, p := range in.OnlyPaths {
			rel := cleanBisyncRel(p)
			if rel != "" {
				pathSet[rel] = struct{}{}
			}
		}
	} else {
		for p := range in.Base {
			if rel := cleanBisyncRel(p); rel != "" {
				pathSet[rel] = struct{}{}
			}
		}
		for p := range in.SnapA {
			if rel := cleanBisyncRel(p); rel != "" {
				pathSet[rel] = struct{}{}
			}
		}
		for p := range in.SnapB {
			if rel := cleanBisyncRel(p); rel != "" {
				pathSet[rel] = struct{}{}
			}
		}
	}
	allPaths := make([]string, 0, len(pathSet))
	for p := range pathSet {
		allPaths = append(allPaths, p)
	}
	sort.Strings(allPaths)

	// Intermediate buckets before rename pairing and case-conflict checks.
	copyA2B := make(map[string]BisyncTransfer)
	copyB2A := make(map[string]BisyncTransfer)
	delA := make(map[string]BisyncDelete)
	delB := make(map[string]BisyncDelete)
	mkdirA := make(map[string]struct{})
	mkdirB := make(map[string]struct{})

	// A move must not bypass case-conflict handling for its destination.
	normA := make(map[string][]string)
	normB := make(map[string][]string)
	if in.CapsA.CaseInsensitive || in.CapsB.CaseInsensitive {
		for p := range in.SnapA {
			k := mapper.NormalizeBisyncKey(p)
			normA[k] = append(normA[k], p)
		}
		for p := range in.SnapB {
			k := mapper.NormalizeBisyncKey(p)
			normB[k] = append(normB[k], p)
		}
	}

	// Both sides share the same active baseline for delete-volume protection.
	syncedCount := 0
	for _, b := range in.Base {
		if b.State == mapper.BisyncStateSynced && !b.IsDir {
			syncedCount++
		}
	}

	// Track paths whose ancestors had a type conflict so we don't emit conflicting child ops.
	typeConflictPrefixes := make([]string, 0)
	underTypeConflict := func(rel string) bool {
		for _, pref := range typeConflictPrefixes {
			if rel == pref || strings.HasPrefix(rel, pref+"/") {
				return true
			}
		}
		return false
	}

	// Helper to ensure parent directories exist on target side.
	ensureParents := func(target map[string]struct{}, existing map[string]BisyncNode, rel string) {
		dir := path.Dir(rel)
		for dir != "." && dir != "/" && dir != "" {
			if _, ok := existing[dir]; !ok {
				target[dir] = struct{}{}
			}
			dir = path.Dir(dir)
		}
	}

	for _, rel := range allPaths {
		base, hasBase := in.Base[rel]
		// Treat expired/tombstone or previously ignored rows as no active baseline.
		activeBase := hasBase && base.State == mapper.BisyncStateSynced

		// Newly excluded paths transition gracefully to ignored without deleting either side.
		if in.Excluded[rel] {
			if activeBase {
				ignored := base
				ignored.State = mapper.BisyncStateIgnored
				plan.MarkIgnored = append(plan.MarkIgnored, ignored)
			}
			continue
		}

		// The directory side is authoritative for descendants of a type conflict.
		// Drop the old shared baseline before any writes, including when a write fails.
		if underTypeConflict(rel) && activeBase {
			plan.MarkTombstone = append(plan.MarkTombstone, BisyncPathRef{RelPath: rel, IsDir: base.IsDir})
			activeBase = false
		}

		nodeA, hasA := in.SnapA[rel]
		nodeB, hasB := in.SnapB[rel]

		// --- Case 1: Neither side currently has the path ---
		if !hasA && !hasB {
			if activeBase {
				plan.MarkTombstone = append(plan.MarkTombstone, BisyncPathRef{RelPath: rel, IsDir: base.IsDir})
			}
			continue
		}

		// --- Case 2: Type conflict (one side is a directory, the other is a file) ---
		if hasA && hasB && nodeA.IsDir != nodeB.IsDir {
			typeConflictPrefixes = append(typeConflictPrefixes, rel)
			if activeBase {
				plan.MarkTombstone = append(plan.MarkTombstone, BisyncPathRef{RelPath: rel, IsDir: base.IsDir})
			}
			na, nb := nodeA, nodeB
			if nodeA.IsDir {
				// Keep directory (A); rename file on B to __TypeConflict and create dir on B.
				copyName := bisyncConflictCopyPath(rel, in.CapsB.Label, now, "TypeConflict")
				plan.Conflicts = append(plan.Conflicts, BisyncConflictAction{
					RelPath:          rel,
					ConflictType:     mapper.BisyncConflictType,
					WinnerSide:       "A",
					ConflictCopyPath: copyName,
					RenameSide:       "B",
					CopyConflictB2A:  true,
					NodeA:            &na,
					NodeB:            &nb,
					Detail:           fmt.Sprintf("同路径类型冲突：%s为目录，%s为文件；已保留目录并将文件重命名为冲突副本", in.CapsA.Label, in.CapsB.Label),
				})
			} else {
				// Keep directory (B); rename file on A to __TypeConflict and create dir on A.
				copyName := bisyncConflictCopyPath(rel, in.CapsA.Label, now, "TypeConflict")
				plan.Conflicts = append(plan.Conflicts, BisyncConflictAction{
					RelPath:          rel,
					ConflictType:     mapper.BisyncConflictType,
					WinnerSide:       "B",
					ConflictCopyPath: copyName,
					RenameSide:       "A",
					CopyConflictA2B:  true,
					NodeA:            &na,
					NodeB:            &nb,
					Detail:           fmt.Sprintf("同路径类型冲突：%s为文件，%s为目录；已保留目录并将文件重命名为冲突副本", in.CapsA.Label, in.CapsB.Label),
				})
			}
			continue
		}

		// --- Case 3: Both sides are directories ---
		if hasA && hasB && nodeA.IsDir && nodeB.IsDir {
			if !activeBase || !base.IsDir {
				plan.Adopt = append(plan.Adopt, makeBisyncEntry(in.JobID, rel, true, nodeA, nodeB, nowUnix))
			}
			continue
		}

		// --- Case 4: Directory on only one side ---
		if (hasA && nodeA.IsDir) || (hasB && nodeB.IsDir) {
			if hasA && !hasB {
				if activeBase && base.IsDir {
					// B deleted the directory; schedule bottom-up delete on A.
					delA[rel] = BisyncDelete{RelPath: rel, IsDir: true, ModTime: nodeA.ModTime, Reason: "远端已删除目录"}
				} else {
					if reason := in.CapsB.Endpoint.InvalidFilenameReason(rel); reason != "" {
						na := nodeA
						plan.Conflicts = append(plan.Conflicts, BisyncConflictAction{
							RelPath:      rel,
							ConflictType: mapper.BisyncConflictInvalidName,
							NodeA:        &na,
							Detail:       reason,
						})
						continue
					}
					mkdirB[rel] = struct{}{}
				}
			} else if hasB && !hasA {
				if activeBase && base.IsDir {
					delB[rel] = BisyncDelete{RelPath: rel, IsDir: true, ModTime: nodeB.ModTime, Reason: "本地已删除目录"}
				} else {
					if reason := in.CapsA.Endpoint.InvalidFilenameReason(rel); reason != "" {
						nb := nodeB
						plan.Conflicts = append(plan.Conflicts, BisyncConflictAction{
							RelPath:      rel,
							ConflictType: mapper.BisyncConflictInvalidName,
							NodeB:        &nb,
							Detail:       reason,
						})
						continue
					}
					mkdirA[rel] = struct{}{}
				}
			}
			continue
		}

		// --- Case 5: Regular files ---
		if !activeBase {
			// No active baseline (initial sync, newly created, or re-included after being ignored).
			switch {
			case hasA && !hasB:
				if reason := in.CapsB.Endpoint.InvalidFilenameReason(rel); reason != "" {
					na := nodeA
					plan.Conflicts = append(plan.Conflicts, BisyncConflictAction{
						RelPath:      rel,
						ConflictType: mapper.BisyncConflictInvalidName,
						NodeA:        &na,
						Detail:       reason,
					})
					continue
				}
				ensureParents(mkdirB, in.SnapB, rel)
				copyA2B[rel] = BisyncTransfer{RelPath: rel, Size: nodeA.Size, Updated: false, Reason: "新增上传", NodeSrc: nodeA}
			case !hasA && hasB:
				if reason := in.CapsA.Endpoint.InvalidFilenameReason(rel); reason != "" {
					nb := nodeB
					plan.Conflicts = append(plan.Conflicts, BisyncConflictAction{
						RelPath:      rel,
						ConflictType: mapper.BisyncConflictInvalidName,
						NodeB:        &nb,
						Detail:       reason,
					})
					continue
				}
				ensureParents(mkdirA, in.SnapA, rel)
				copyB2A[rel] = BisyncTransfer{RelPath: rel, Size: nodeB.Size, Updated: false, Reason: "新增下载", NodeSrc: nodeB}
			case hasA && hasB:
				if bisyncNodesEquivalent(nodeA, nodeB, in.CompareMode, modWin, in.CapsA, in.CapsB) {
					// Zero-cost adoption!
					plan.Adopt = append(plan.Adopt, makeBisyncEntry(in.JobID, rel, false, nodeA, nodeB, nowUnix))
				} else {
					plan.Conflicts = append(plan.Conflicts, buildContentConflictAction(rel, nodeA, nodeB, in.ConflictPolicy, in.CapsA, in.CapsB, now, "双端均存在同名但内容不同的文件"))
				}
			}
			continue
		}

		// Active synced baseline exists!
		switch {
		case hasA && hasB:
			changedA := bisyncSideChanged(nodeA, base, "A", in.CompareMode, modWin, in.CapsA)
			changedB := bisyncSideChanged(nodeB, base, "B", in.CompareMode, modWin, in.CapsB)
			switch {
			case !changedA && !changedB:
				// Both unchanged; if inode/objectID was newly learned, silently adopt.
				if (nodeA.Inode != 0 && base.AInode == 0) || (nodeB.ObjectID != "" && base.BObjectID == "") {
					plan.Adopt = append(plan.Adopt, makeBisyncEntry(in.JobID, rel, false, nodeA, nodeB, nowUnix))
				}
			case changedA && !changedB:
				nb := nodeB
				copyA2B[rel] = BisyncTransfer{RelPath: rel, Size: nodeA.Size, Updated: true, Reason: "本地修改上传", NodeSrc: nodeA, NodeDst: &nb}
			case !changedA && changedB:
				na := nodeA
				copyB2A[rel] = BisyncTransfer{RelPath: rel, Size: nodeB.Size, Updated: true, Reason: "远端修改下载", NodeSrc: nodeB, NodeDst: &na}
			case changedA && changedB:
				if bisyncNodesEquivalent(nodeA, nodeB, in.CompareMode, modWin, in.CapsA, in.CapsB) {
					plan.Adopt = append(plan.Adopt, makeBisyncEntry(in.JobID, rel, false, nodeA, nodeB, nowUnix))
				} else {
					plan.Conflicts = append(plan.Conflicts, buildContentConflictAction(rel, nodeA, nodeB, in.ConflictPolicy, in.CapsA, in.CapsB, now, "两端均修改了同一文件"))
				}
			}
		case hasA && !hasB:
			// B deleted the file since last sync.
			if bisyncSideChanged(nodeA, base, "A", in.CompareMode, modWin, in.CapsA) {
				// Edit-wins-over-delete: A modified the file after B deleted it -> re-upload A!
				ensureParents(mkdirB, in.SnapB, rel)
				copyA2B[rel] = BisyncTransfer{RelPath: rel, Size: nodeA.Size, Updated: false, Reason: "修改优先于删除（重新上传）", NodeSrc: nodeA}
			} else {
				delA[rel] = BisyncDelete{RelPath: rel, IsDir: false, Size: nodeA.Size, ModTime: nodeA.ModTime, Reason: "远端已删除"}
			}
		case !hasA && hasB:
			// A deleted the file since last sync.
			if bisyncSideChanged(nodeB, base, "B", in.CompareMode, modWin, in.CapsB) {
				// Edit-wins-over-delete: B modified the file after A deleted it -> re-download B!
				ensureParents(mkdirA, in.SnapA, rel)
				copyB2A[rel] = BisyncTransfer{RelPath: rel, Size: nodeB.Size, Updated: false, Reason: "修改优先于删除（重新下载）", NodeSrc: nodeB}
			} else {
				delB[rel] = BisyncDelete{RelPath: rel, IsDir: false, Size: nodeB.Size, ModTime: nodeB.ModTime, Reason: "本地已删除"}
			}
		}
	}

	// --- Rename / Move Detection (only in full-tree mode where deletions & additions are both visible) ---
	if len(in.OnlyPaths) == 0 {
		// 1) Local (A) renamed: delB[old] + copyA2B[new] -> MoveB(old -> new)
		delBByInode := make(map[uint64][]string)
		delBByHash := make(map[string][]string)
		for oldRel, d := range delB {
			if d.IsDir {
				continue
			}
			base, ok := in.Base[oldRel]
			if !ok {
				continue
			}
			if in.CapsA.IsLocal && base.AInode != 0 {
				delBByInode[base.AInode] = append(delBByInode[base.AInode], oldRel)
			}
			if base.AHash != "" {
				key := fmt.Sprintf("%d:%s", base.ASize, strings.ToLower(base.AHash))
				delBByHash[key] = append(delBByHash[key], oldRel)
			}
		}
		for _, newRel := range sortedTransferKeys(copyA2B) {
			tr := copyA2B[newRel]
			if tr.Updated || (in.CapsB.CaseInsensitive && !bisyncMoveHasDistinctDestination(newRel, normA, normB)) {
				continue
			}
			var matchedOld string
			if in.CapsA.IsLocal && tr.NodeSrc.Inode != 0 {
				if cands := delBByInode[tr.NodeSrc.Inode]; len(cands) == 1 {
					oldRel := cands[0]
					if base := in.Base[oldRel]; !bisyncSideChanged(tr.NodeSrc, base, "A", in.CompareMode, modWin, in.CapsA) &&
						(tr.NodeSrc.Hash == "" || base.AHash == "" || strings.EqualFold(tr.NodeSrc.Hash, base.AHash)) {
						matchedOld = oldRel
					}
				}
			}
			if matchedOld == "" && tr.NodeSrc.Hash != "" {
				key := fmt.Sprintf("%d:%s", tr.NodeSrc.Size, strings.ToLower(tr.NodeSrc.Hash))
				if cands := delBByHash[key]; len(cands) == 1 {
					matchedOld = cands[0]
				}
			}
			if matchedOld == "" {
				continue
			}
			if _, stillDel := delB[matchedOld]; !stillDel {
				continue
			}
			nodeB, hasOldB := in.SnapB[matchedOld]
			if !hasOldB {
				continue
			}
			plan.MoveB = append(plan.MoveB, BisyncMove{
				From:  matchedOld,
				To:    newRel,
				NodeA: tr.NodeSrc,
				NodeB: nodeB,
				Base:  in.Base[matchedOld],
			})
			delete(delB, matchedOld)
			delete(copyA2B, newRel)
		}

		// 2) Remote (B) renamed: delA[old] + copyB2A[new] -> MoveA(old -> new)
		delAByObjID := make(map[string][]string)
		delAByHash := make(map[string][]string)
		for oldRel, d := range delA {
			if d.IsDir {
				continue
			}
			base, ok := in.Base[oldRel]
			if !ok {
				continue
			}
			if base.BObjectID != "" {
				delAByObjID[base.BObjectID] = append(delAByObjID[base.BObjectID], oldRel)
			}
			if base.BHash != "" {
				key := fmt.Sprintf("%d:%s", base.BSize, strings.ToLower(base.BHash))
				delAByHash[key] = append(delAByHash[key], oldRel)
			}
		}
		for _, newRel := range sortedTransferKeys(copyB2A) {
			tr := copyB2A[newRel]
			if tr.Updated || (in.CapsA.CaseInsensitive && !bisyncMoveHasDistinctDestination(newRel, normB, normA)) {
				continue
			}
			var matchedOld string
			if tr.NodeSrc.ObjectID != "" {
				if cands := delAByObjID[tr.NodeSrc.ObjectID]; len(cands) == 1 {
					oldRel := cands[0]
					if base := in.Base[oldRel]; !bisyncSideChanged(tr.NodeSrc, base, "B", in.CompareMode, modWin, in.CapsB) &&
						(tr.NodeSrc.Hash == "" || base.BHash == "" || strings.EqualFold(tr.NodeSrc.Hash, base.BHash)) {
						matchedOld = oldRel
					}
				}
			}
			if matchedOld == "" && tr.NodeSrc.Hash != "" {
				key := fmt.Sprintf("%d:%s", tr.NodeSrc.Size, strings.ToLower(tr.NodeSrc.Hash))
				if cands := delAByHash[key]; len(cands) == 1 {
					matchedOld = cands[0]
				}
			}
			if matchedOld == "" {
				continue
			}
			if _, stillDel := delA[matchedOld]; !stillDel {
				continue
			}
			nodeA, hasOldA := in.SnapA[matchedOld]
			if !hasOldA {
				continue
			}
			plan.MoveA = append(plan.MoveA, BisyncMove{
				From:  matchedOld,
				To:    newRel,
				NodeA: nodeA,
				NodeB: tr.NodeSrc,
				Base:  in.Base[matchedOld],
			})
			delete(delA, matchedOld)
			delete(copyB2A, newRel)
		}
	}

	// --- Case-Insensitive Conflict Detection (CaseConflict) ---
	if in.CapsB.CaseInsensitive {
		occupiedB := make(map[string]string)
		for _, p := range allPaths {
			if _, exists := in.SnapB[p]; exists {
				occupiedB[mapper.NormalizeBisyncKey(p)] = p
			}
		}
		for _, rel := range sortedTransferKeys(copyA2B) {
			tr := copyA2B[rel]
			norm := mapper.NormalizeBisyncKey(rel)
			if existing, clash := occupiedB[norm]; clash && existing != rel {
				if d, removing := delB[existing]; removing {
					delete(delB, existing)
					plan.MarkTombstone = append(plan.MarkTombstone, BisyncPathRef{RelPath: existing, IsDir: d.IsDir})
				}
				na := tr.NodeSrc
				copyName := bisyncConflictCopyPath(rel, in.CapsA.Label, now, "CaseConflict")
				plan.Conflicts = append(plan.Conflicts, BisyncConflictAction{
					RelPath:          rel,
					ConflictType:     mapper.BisyncConflictCase,
					WinnerSide:       "B",
					ConflictCopyPath: copyName,
					RenameSide:       "A",
					CopyConflictA2B:  true,
					NodeA:            &na,
					Detail:           fmt.Sprintf("大小写冲突：%s不区分大小写，已存在 %q，已将 %q 另存为冲突副本", in.CapsB.Label, existing, rel),
				})
				delete(copyA2B, rel)
				continue
			}
			occupiedB[norm] = rel
		}
	}
	if in.CapsA.CaseInsensitive {
		occupiedA := make(map[string]string)
		for _, p := range allPaths {
			if _, exists := in.SnapA[p]; exists {
				occupiedA[mapper.NormalizeBisyncKey(p)] = p
			}
		}
		for _, rel := range sortedTransferKeys(copyB2A) {
			tr := copyB2A[rel]
			norm := mapper.NormalizeBisyncKey(rel)
			if existing, clash := occupiedA[norm]; clash && existing != rel {
				if d, removing := delA[existing]; removing {
					delete(delA, existing)
					plan.MarkTombstone = append(plan.MarkTombstone, BisyncPathRef{RelPath: existing, IsDir: d.IsDir})
				}
				nb := tr.NodeSrc
				copyName := bisyncConflictCopyPath(rel, in.CapsB.Label, now, "CaseConflict")
				plan.Conflicts = append(plan.Conflicts, BisyncConflictAction{
					RelPath:          rel,
					ConflictType:     mapper.BisyncConflictCase,
					WinnerSide:       "A",
					ConflictCopyPath: copyName,
					RenameSide:       "B",
					CopyConflictB2A:  true,
					NodeB:            &nb,
					Detail:           fmt.Sprintf("大小写冲突：%s不区分大小写，已存在 %q，已将 %q 另存为冲突副本", in.CapsA.Label, existing, rel),
				})
				delete(copyB2A, rel)
				continue
			}
			occupiedA[norm] = rel
		}
	}

	// Finalize sorted slices.
	plan.MkdirA = sortedDirPathsTopDown(mkdirA)
	plan.MkdirB = sortedDirPathsTopDown(mkdirB)
	for _, k := range sortedTransferKeys(copyA2B) {
		plan.CopyA2B = append(plan.CopyA2B, copyA2B[k])
	}
	for _, k := range sortedTransferKeys(copyB2A) {
		plan.CopyB2A = append(plan.CopyB2A, copyB2A[k])
	}
	plan.DeleteA = sortedBisyncDeletes(delA)
	plan.DeleteB = sortedBisyncDeletes(delB)
	sort.Slice(plan.MoveA, func(i, j int) bool { return plan.MoveA[i].From < plan.MoveA[j].From })
	sort.Slice(plan.MoveB, func(i, j int) bool { return plan.MoveB[i].From < plan.MoveB[j].From })

	// Delete-volume safety guard (per side).
	maxDelPct := in.MaxDeletePercent
	if maxDelPct <= 0 || maxDelPct > 100 {
		maxDelPct = 50
	}
	if !in.ForceDelete && maxDelPct < 100 {
		fileDelA := countBisyncFileDeletes(plan.DeleteA)
		if fileDelA >= 5 && syncedCount > 0 && fileDelA*100 >= syncedCount*maxDelPct {
			plan.DeleteGuardA = true
			plan.DeleteGuardReasonA = fmt.Sprintf("计划删除%s端 %d 个文件（占基线 %d 个文件的 %d%%，超过保护阈值 %d%%），已跳过本次删除",
				in.CapsA.Label, fileDelA, syncedCount, fileDelA*100/syncedCount, maxDelPct)
		}
		fileDelB := countBisyncFileDeletes(plan.DeleteB)
		if fileDelB >= 5 && syncedCount > 0 && fileDelB*100 >= syncedCount*maxDelPct {
			plan.DeleteGuardB = true
			plan.DeleteGuardReasonB = fmt.Sprintf("计划删除%s端 %d 个文件（占基线 %d 个文件的 %d%%，超过保护阈值 %d%%），已跳过本次删除",
				in.CapsB.Label, fileDelB, syncedCount, fileDelB*100/syncedCount, maxDelPct)
		}
	}

	return plan
}

func buildContentConflictAction(rel string, nodeA, nodeB BisyncNode, policy int, capsA, capsB BisyncEndpointCaps, now time.Time, detail string) BisyncConflictAction {
	na, nb := nodeA, nodeB
	winner := "A"
	if policy != 1 && nodeB.ModTime > nodeA.ModTime {
		winner = "B"
	}
	switch policy {
	case 0: // newer wins (loser overwritten -> moved to trash if enabled)
		return BisyncConflictAction{
			RelPath:       rel,
			ConflictType:  mapper.BisyncConflictContent,
			WinnerSide:    winner,
			CopyWinnerA2B: winner == "A",
			CopyWinnerB2A: winner == "B",
			NodeA:         &na,
			NodeB:         &nb,
			Detail:        detail + "（已按较新版本覆盖）",
		}
	case 1: // path1 / A wins
		return BisyncConflictAction{
			RelPath:       rel,
			ConflictType:  mapper.BisyncConflictContent,
			WinnerSide:    "A",
			CopyWinnerA2B: true,
			NodeA:         &na,
			NodeB:         &nb,
			Detail:        detail + fmt.Sprintf("（已以%s为准覆盖）", capsA.Label),
		}
	default: // policy == 2: keep both (default recommended)
		loserSide := "B"
		loserLabel := capsB.Label
		if winner == "B" {
			loserSide = "A"
			loserLabel = capsA.Label
		}
		copyPath := bisyncConflictCopyPath(rel, loserLabel, now, "Conflict")
		return BisyncConflictAction{
			RelPath:          rel,
			ConflictType:     mapper.BisyncConflictContent,
			WinnerSide:       winner,
			ConflictCopyPath: copyPath,
			RenameSide:       loserSide,
			CopyWinnerA2B:    winner == "A",
			CopyWinnerB2A:    winner == "B",
			CopyConflictA2B:  loserSide == "A",
			CopyConflictB2A:  loserSide == "B",
			NodeA:            &na,
			NodeB:            &nb,
			Detail:           fmt.Sprintf("%s；已保留双方副本（%s版本已另存为冲突副本）", detail, loserLabel),
		}
	}
}

func bisyncConflictCopyPath(relPath, sideLabel string, now time.Time, reason string) string {
	dir := path.Dir(relPath)
	base := path.Base(relPath)
	ext := path.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	if sideLabel == "" {
		sideLabel = "副本"
	}
	sideLabel = strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_").Replace(sideLabel)
	ts := now.In(schedulerLocation()).Format("20060102-150405")
	suffix := fmt.Sprintf("__%s__%s__%s", sideLabel, ts, reason)
	var name string
	if ext == "" || stem == "" {
		name = base + suffix
	} else {
		name = stem + suffix + ext
	}
	if dir == "." || dir == "/" || dir == "" {
		return name
	}
	return path.Join(dir, name)
}

func bisyncMoveHasDistinctDestination(rel string, sourceNames, targetNames map[string][]string) bool {
	key := mapper.NormalizeBisyncKey(rel)
	if len(sourceNames[key]) != 1 || len(targetNames[key]) != 0 {
		return false
	}
	for parent := path.Dir(rel); parent != "."; parent = path.Dir(parent) {
		key = mapper.NormalizeBisyncKey(parent)
		if len(sourceNames[key]) > 1 {
			return false
		}
		for _, existing := range targetNames[key] {
			if existing != parent {
				return false
			}
		}
	}
	return true
}

func bisyncSideChanged(cur BisyncNode, base mapper.BisyncEntry, side string, compareMode int, modWin int64, caps BisyncEndpointCaps) bool {
	if cur.IsDir != base.IsDir {
		return true
	}
	if cur.IsDir {
		return false
	}
	var baseSize, baseMod int64
	var baseHash string
	if side == "A" {
		baseSize, baseMod, baseHash = base.ASize, base.AModTime, base.AHash
	} else {
		baseSize, baseMod, baseHash = base.BSize, base.BModTime, base.BHash
	}
	if cur.Size != baseSize {
		return true
	}
	if compareMode == 2 && cur.Hash != "" && baseHash != "" {
		return !strings.EqualFold(cur.Hash, baseHash)
	}
	if compareMode == 1 || caps.UnreliableModTime {
		return false
	}
	diff := cur.ModTime - baseMod
	if diff < 0 {
		diff = -diff
	}
	return diff > modWin
}

func bisyncNodesEquivalent(a, b BisyncNode, compareMode int, modWin int64, capsA, capsB BisyncEndpointCaps) bool {
	if a.IsDir != b.IsDir {
		return false
	}
	if a.IsDir {
		return true
	}
	if a.Size != b.Size {
		return false
	}
	if compareMode == 1 {
		return true
	}
	if a.Hash != "" && b.Hash != "" {
		return strings.EqualFold(a.Hash, b.Hash)
	}
	if capsA.UnreliableModTime || capsB.UnreliableModTime {
		return true
	}
	diff := a.ModTime - b.ModTime
	if diff < 0 {
		diff = -diff
	}
	return diff <= modWin
}

func makeBisyncEntry(jobID int64, relPath string, isDir bool, a, b BisyncNode, nowUnix int64) mapper.BisyncEntry {
	return mapper.BisyncEntry{
		JobID:     jobID,
		RelPath:   relPath,
		NormKey:   mapper.NormalizeBisyncKey(relPath),
		IsDir:     isDir,
		State:     mapper.BisyncStateSynced,
		ASize:     a.Size,
		AModTime:  a.ModTime,
		AHash:     a.Hash,
		AInode:    a.Inode,
		BSize:     b.Size,
		BModTime:  b.ModTime,
		BHash:     b.Hash,
		BObjectID: b.ObjectID,
		SyncedAt:  nowUnix,
	}
}

func cleanBisyncRel(p string) string {
	// Never TrimSpace: trailing spaces are legal POSIX characters on Linux/NAS
	// and must be preserved so InvalidFilenameReason can catch TailCharacterConflict.
	if p == "" || strings.ContainsAny(p, "\r\n") {
		return ""
	}
	rel := strings.Trim(path.Clean("/"+p), "/")
	if rel == "" || rel == "." || strings.HasPrefix(rel, "../") {
		return ""
	}
	return rel
}

func sortedDirPathsTopDown(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		di := strings.Count(out[i], "/")
		dj := strings.Count(out[j], "/")
		if di != dj {
			return di < dj
		}
		return out[i] < out[j]
	})
	return out
}

func sortedTransferKeys(m map[string]BisyncTransfer) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedBisyncDeletes(m map[string]BisyncDelete) []BisyncDelete {
	out := make([]BisyncDelete, 0, len(m))
	for _, d := range m {
		out = append(out, d)
	}
	// Files first, then directories deepest-first (bottom-up).
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return !out[i].IsDir
		}
		if out[i].IsDir {
			di := strings.Count(out[i].RelPath, "/")
			dj := strings.Count(out[j].RelPath, "/")
			if di != dj {
				return di > dj
			}
		}
		return out[i].RelPath < out[j].RelPath
	})
	return out
}

func countBisyncFileDeletes(deletes []BisyncDelete) int {
	n := 0
	for _, d := range deletes {
		if !d.IsDir {
			n++
		}
	}
	return n
}
