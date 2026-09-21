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

func syncPathContains(parent, candidate string) bool {
	parent = path.Clean(strings.TrimSpace(parent))
	candidate = path.Clean(strings.TrimSpace(candidate))
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
		cleaned = append(cleaned, item)
	}
	return cleaned
}
