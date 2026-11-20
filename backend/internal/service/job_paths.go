package service

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

func parsePathList(value interface{}) []string {
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		raw := strings.TrimSpace(v)
		if raw == "" {
			return nil
		}
		if strings.HasPrefix(raw, "[") {
			var paths []string
			if err := json.Unmarshal([]byte(raw), &paths); err == nil {
				return cleanPathList(paths)
			}
		}
		return cleanPathList([]string{raw})
	case []string:
		return cleanPathList(v)
	case []interface{}:
		paths := make([]string, 0, len(v))
		for _, item := range v {
			paths = append(paths, fmt.Sprintf("%v", item))
		}
		return cleanPathList(paths)
	default:
		return cleanPathList([]string{fmt.Sprintf("%v", v)})
	}
}

func encodePathList(paths []string) string {
	cleaned := cleanPathList(paths)
	data, err := json.Marshal(cleaned)
	if err != nil {
		return "[]"
	}
	return string(data)
}

func normalizePathListForStorage(value interface{}) string {
	return encodePathList(parsePathList(value))
}

func pathListHasParentSegment(paths []string) bool {
	for _, value := range paths {
		for _, segment := range strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '\\' }) {
			if segment == ".." {
				return true
			}
		}
	}
	return false
}

func syncPathsOverlap(srcPaths, dstPaths []string) bool {
	for _, src := range srcPaths {
		for _, dst := range dstPaths {
			if syncPathContains(src, dst) || syncPathContains(dst, src) {
				return true
			}
		}
	}
	return false
}

// srcSelectionsNested reports whether one selected source path contains
// another. Such a pair is rejected because both selections resolve into
// overlapping destination subtrees: the parent's scan walks into the same
// directory the child is mapped onto, so two top-level scan works mutate one
// destination tree concurrently. In mirror mode each of them independently
// computes "extra files to delete" for that directory, which can delete files
// the sibling work is still copying.
func srcSelectionsNested(srcPaths []string) bool {
	for i, parent := range srcPaths {
		for j, candidate := range srcPaths {
			if i == j {
				continue
			}
			if syncPathContains(parent, candidate) {
				return true
			}
		}
	}
	return false
}

// resolvedDstPaths returns the destination root of every top-level (src, dst)
// scan work, computed exactly as JobTask.sync builds them: both sides
// normalized to directory form, then mapped through dstPathForSrcSelection.
// Validation has to look at these rather than the configured dstPath entries,
// because with several sources each one lands in its own derived subdirectory.
func resolvedDstPaths(srcPaths, dstPaths []string) []string {
	resolved := make([]string, 0, len(srcPaths)*len(dstPaths))
	for _, srcItem := range srcPaths {
		srcItem = normalizeDirPath(srcItem)
		for _, dstItem := range dstPaths {
			resolved = append(resolved, dstPathForSrcSelection(normalizeDirPath(dstItem), srcItem, srcPaths))
		}
	}
	return resolved
}

// resolvedDstPathsNested reports whether one resolved destination root contains
// another. Two scan works whose destinations nest mutate one tree
// concurrently, and in mirror mode the outer one deletes the inner one's files
// as "extra" on every run. This covers nested dstPath entries (["/d","/d/x"])
// and sources whose derived subdirectories collide (["/x/a","/y/a","/z/x"]
// maps to D/x/a and D/x).
func resolvedDstPathsNested(srcPaths, dstPaths []string) bool {
	resolved := resolvedDstPaths(srcPaths, dstPaths)
	for i := range resolved {
		for j := i + 1; j < len(resolved); j++ {
			if syncPathContains(resolved[i], resolved[j]) || syncPathContains(resolved[j], resolved[i]) {
				return true
			}
		}
	}
	return false
}

// syncPathContains compares case-insensitively. The overlap and nesting checks
// exist to prevent two works from writing the same destination subtree, and the
// storage behind an engine decides whether /Photos and /photos are one
// directory: SMB, most cloud drives and a macOS/Windows volume fold case, so a
// case-sensitive comparison let "/Photos" -> "/photos" pass as non-overlapping
// and then mirror a directory onto itself. Treating them as the same path can
// at worst reject a genuinely distinct pair on a case-sensitive backend, which
// is the safe direction for a check whose failure mode is deleting files.
func syncPathContains(parent, candidate string) bool {
	parent = strings.ToLower(path.Clean(strings.TrimSpace(parent)))
	candidate = strings.ToLower(path.Clean(strings.TrimSpace(candidate)))
	if parent == candidate {
		return true
	}
	if parent == "/" {
		return true
	}
	return strings.HasPrefix(candidate, parent+"/")
}

// cleanPathList drops blank entries and duplicates. Duplicates matter because a
// path repeated in the selection is compared against itself when the shared
// parent is derived, which yields a suffix carrying a leading slash.
func cleanPathList(paths []string) []string {
	cleaned := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, item := range paths {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key := path.Clean(item)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		cleaned = append(cleaned, key)
	}
	return cleaned
}

// jobDstMapVersion tags the persisted destination mapping. Entries are reused
// only for the same destination-list base; any other change recomputes them.
const jobDstMapVersion = 1

// jobDstMap stores the resolved destination root of every selected (source,
// destination) pair, so the mapping stays stable when sources are added or
// removed. Without it, adding one source changes the common parent and silently
// re-maps every other source: the next run copies them to new directories and,
// in mirror mode, treats the old directories as extra and deletes them.
type jobDstMap struct {
	Version int               `json:"version"`
	Base    []string          `json:"base"`
	Map     map[string]string `json:"map"`
}

func jobDstMapKey(srcRoot, dstRoot string) string {
	return normalizeDirPath(srcRoot) + "\x00" + normalizeDirPath(dstRoot)
}

func jobDstMapRaw(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

func parseStoredJobDstMap(value interface{}) *jobDstMap {
	raw := jobDstMapRaw(value)
	if raw == "" {
		return nil
	}
	var stored jobDstMap
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return nil
	}
	if stored.Version != jobDstMapVersion || stored.Map == nil {
		return nil
	}
	return &stored
}

func jobDstBaseEquals(stored []string, current []string) bool {
	if len(stored) != len(current) {
		return false
	}
	for i := range stored {
		if normalizeDirPath(stored[i]) != normalizeDirPath(current[i]) {
			return false
		}
	}
	return true
}

// ensureJobDstMap computes and stores destination roots for all selected pairs.
// Existing entries survive edits as long as the destination list is unchanged;
// only new pairs are computed. The stored map records the destination list it
// was built for, so changing the destinations deliberately re-maps everything.
func ensureJobDstMap(job map[string]interface{}) {
	srcPaths := parsePathList(job["srcPath"])
	dstPaths := parsePathList(job["dstPath"])
	if len(srcPaths) == 0 || len(dstPaths) == 0 {
		job["dstMap"] = nil
		return
	}
	base := make([]string, len(dstPaths))
	for i, dstItem := range dstPaths {
		base[i] = normalizeDirPath(dstItem)
	}
	reuse := map[string]string(nil)
	if stored := parseStoredJobDstMap(job["dstMap"]); stored != nil && jobDstBaseEquals(stored.Base, base) {
		reuse = stored.Map
	}
	next := make(map[string]string, len(srcPaths)*len(dstPaths))
	for _, srcItem := range srcPaths {
		srcDir := normalizeDirPath(srcItem)
		for _, dstItem := range dstPaths {
			dstDir := normalizeDirPath(dstItem)
			key := jobDstMapKey(srcDir, dstDir)
			if value, ok := reuse[key]; ok && syncPathContains(dstDir, value) {
				next[key] = normalizeDirPath(value)
				continue
			}
			next[key] = dstPathForSrcSelection(dstDir, srcDir, srcPaths)
		}
	}
	encoded, err := json.Marshal(jobDstMap{Version: jobDstMapVersion, Base: base, Map: next})
	if err != nil {
		job["dstMap"] = nil
		return
	}
	job["dstMap"] = string(encoded)
}

// jobDstForSrc resolves the destination root of one (source, destination) pair.
// It uses the persisted mapping when it covers the pair and falls back to the
// selection-based algorithm for jobs saved before the map existed. A pair whose
// destination is no longer configured resolves to "", so callers cannot act on
// a path outside the current job definition.
func jobDstForSrc(job map[string]interface{}, srcRoot, dstRoot string) string {
	configured := false
	for _, item := range parsePathList(job["dstPath"]) {
		if normalizeDirPath(item) == normalizeDirPath(dstRoot) {
			configured = true
			break
		}
	}
	if !configured {
		return ""
	}
	if stored := parseStoredJobDstMap(job["dstMap"]); stored != nil {
		// The stored value is only trusted while it stays inside the
		// destination it was derived from; a damaged or hand-edited row must
		// not send writes to an unrelated path.
		if value, ok := stored.Map[jobDstMapKey(srcRoot, dstRoot)]; ok && value != "" && syncPathContains(dstRoot, value) {
			return normalizeDirPath(value)
		}
	}
	return dstPathForSrcSelection(dstRoot, srcRoot, parsePathList(job["srcPath"]))
}

// resolvedDstPathsForJob returns the destination root of every top-level
// (source, destination) pair of a job, using its persisted mapping.
func resolvedDstPathsForJob(job map[string]interface{}) []string {
	srcPaths := parsePathList(job["srcPath"])
	dstPaths := parsePathList(job["dstPath"])
	resolved := make([]string, 0, len(srcPaths)*len(dstPaths))
	for _, srcItem := range srcPaths {
		for _, dstItem := range dstPaths {
			resolved = append(resolved, jobDstForSrc(job, srcItem, dstItem))
		}
	}
	return resolved
}

// resolvedDstPathsNestedForJob reports whether one resolved destination root of
// the job contains another.
func resolvedDstPathsNestedForJob(job map[string]interface{}) bool {
	resolved := resolvedDstPathsForJob(job)
	for i := range resolved {
		for j := i + 1; j < len(resolved); j++ {
			if syncPathContains(resolved[i], resolved[j]) || syncPathContains(resolved[j], resolved[i]) {
				return true
			}
		}
	}
	return false
}
