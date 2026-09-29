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
