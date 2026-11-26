package service

import (
	"opensync/internal/msg"
	"opensync/pkg/util"
	"path"
	"strings"
)

// FileMetadata contains lightweight comparison data from directory listings.
type FileMetadata struct {
	Size     int64
	MD5      string
	Modified int64
}

func normalizeMD5(md5 string) string {
	return strings.ToLower(strings.TrimSpace(md5))
}

// FileListResult maps filename -> comparison metadata.
// Directories use a key ending with "/" and a zero FileMetadata value.
type FileListResult = map[string]FileMetadata

// isSafeListedName rejects entry names that are not plain file names.
func isSafeListedName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, "/\\")
}

// isSafeDeleteTargetName reports whether fileName (optionally carrying the
// directory suffix) names exactly one entry inside its directory.
func isSafeDeleteTargetName(fileName string) bool {
	return isSafeListedName(strings.TrimSuffix(fileName, "/"))
}

// taskItemErrMsg reads a stored errMsg column. Rows loaded from the database
// carry a string; in-memory item maps (JobTaskItem.ToMap) carry a *string.
func taskItemErrMsg(value interface{}) string {
	switch v := value.(type) {
	case *string:
		if v == nil {
			return ""
		}
		return *v
	case nil:
		return ""
	default:
		return util.StringValue(v)
	}
}

// unsafeDeleteTargetMessage explains a refused delete.
func unsafeDeleteTargetMessage(dir, fileName string) string {
	return msg.UnsafeDeleteTarget(fileName, dir)
}

func (jt *JobTask) hasRetrySource() bool {
	return jt.RetrySourceTaskID > 0
}

func (jt *JobTask) markScanFinished() {
	jt.ScanFinish.Store(true)
	jt.notifyProgressChange()
}

func dstPathForSrcSelection(dstPath, srcPath string, srcPaths []string) string {
	dstPath = normalizeDirPath(dstPath)
	if len(srcPaths) <= 1 {
		return dstPath
	}

	suffix := srcSelectionSuffix(srcPath, srcPaths)
	if suffix == "." || suffix == "/" || suffix == "" {
		return dstPath
	}
	return normalizeDirPath(dstPath + suffix)
}

// stripNativeLocalRootID strips the "~<12-lowercase-hex>" fingerprint suffix
// appended by native.rootID / legacyRootID to an authorized local root segment.
// Returns (base, true) only when segment matches "<base>~<12 lowercase hex>"
// with a safe non-empty base name (not "." or "..").
func stripNativeLocalRootID(segment string) (string, bool) {
	idx := strings.LastIndexByte(segment, '~')
	if idx <= 0 || len(segment)-idx-1 != 12 {
		return segment, false
	}
	base := segment[:idx]
	if base == "." || base == ".." {
		return segment, false
	}
	suffix := segment[idx+1:]
	for i := 0; i < len(suffix); i++ {
		c := suffix[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return segment, false
		}
	}
	return base, true
}

// splitLocalRootSegment extracts the top-level storage segment, second-level
// directory segment (where native local root IDs live), and remaining tail of
// a cleaned absolute POSIX path. Returns ok=false for paths shallower than two
// segments.
func splitLocalRootSegment(cleanPath string) (storage, rootSeg, rest string, ok bool) {
	if !strings.HasPrefix(cleanPath, "/") {
		return "", "", "", false
	}
	trimmed := strings.TrimPrefix(cleanPath, "/")
	storage, afterStorage, hasRoot := strings.Cut(trimmed, "/")
	if !hasRoot || storage == "" || afterStorage == "" {
		return "", "", "", false
	}
	rootSeg, rest, _ = strings.Cut(afterStorage, "/")
	if rootSeg == "" {
		return "", "", "", false
	}
	return storage, rootSeg, rest, true
}

// sanitizeSrcPathsForDstDerivation strips the "~<12-hex>" fingerprint suffix
// from native local authorized root segments (/<storage>/<base>~<12hex>[/...])
// when deriving destination subdirectories for multi-source jobs.
//
// If two distinct root segments in the same selection collapse to the same
// base name (compared case-insensitively, matching syncPathContains), both
// keep their fingerprint suffixes so same-named folders from distinct volumes
// never collide or nest at the destination.
func sanitizeSrcPathsForDstDerivation(srcPaths []string) ([]string, func(string) string) {
	cleanedInputs := make([]string, len(srcPaths))
	rawByBase := make(map[string]map[string]struct{}, len(srcPaths))
	for i, raw := range srcPaths {
		clean := path.Clean(strings.TrimSpace(raw))
		cleanedInputs[i] = clean
		_, rootSeg, _, ok := splitLocalRootSegment(clean)
		if !ok {
			continue
		}
		base, _ := stripNativeLocalRootID(rootSeg)
		baseKey := strings.ToLower(base)
		rawKey := strings.ToLower(rootSeg)
		set := rawByBase[baseKey]
		if set == nil {
			set = make(map[string]struct{}, 1)
			rawByBase[baseKey] = set
		}
		set[rawKey] = struct{}{}
	}

	sanitizeOne := func(clean string) string {
		storage, rootSeg, rest, ok := splitLocalRootSegment(clean)
		if !ok {
			return clean
		}
		base, stripped := stripNativeLocalRootID(rootSeg)
		if !stripped {
			return clean
		}
		set := rawByBase[strings.ToLower(base)]
		if len(set) > 1 {
			return clean
		}
		if len(set) == 1 {
			if _, same := set[strings.ToLower(rootSeg)]; !same {
				return clean
			}
		}
		if rest == "" {
			return "/" + storage + "/" + base
		}
		return "/" + storage + "/" + base + "/" + rest
	}

	sanitized := make([]string, len(cleanedInputs))
	for i, clean := range cleanedInputs {
		sanitized[i] = sanitizeOne(clean)
	}
	return sanitized, sanitizeOne
}

func srcSelectionSuffix(srcPath string, srcPaths []string) string {
	sanitizedPaths, sanitizeOne := sanitizeSrcPathsForDstDerivation(srcPaths)
	cleanSrc := sanitizeOne(path.Clean(strings.TrimSpace(srcPath)))
	commonParent := commonSrcSelectionParentRaw(sanitizedPaths)
	if commonParent == "" || commonParent == "." || commonParent == "/" {
		if srcSelectionBaseNameIsAmbiguous(cleanSrc, sanitizedPaths) {
			return strings.TrimPrefix(cleanSrc, "/")
		}
		return path.Base(cleanSrc)
	}
	if cleanSrc == commonParent {
		return ""
	}
	return strings.TrimPrefix(cleanSrc, normalizeDirPath(commonParent))
}

func srcSelectionBaseNameIsAmbiguous(cleanSrc string, srcPaths []string) bool {
	base := path.Base(cleanSrc)
	for _, other := range srcPaths {
		cleanOther := path.Clean(strings.TrimSpace(other))
		if cleanOther == cleanSrc {
			continue
		}
		if strings.EqualFold(path.Base(cleanOther), base) {
			return true
		}
	}
	return false
}

func commonSrcSelectionParent(srcPaths []string) string {
	sanitized, _ := sanitizeSrcPathsForDstDerivation(srcPaths)
	return commonSrcSelectionParentRaw(sanitized)
}

func commonSrcSelectionParentRaw(srcPaths []string) string {
	if len(srcPaths) < 2 {
		return ""
	}
	common := path.Clean(strings.TrimSpace(srcPaths[0]))
	for _, srcPath := range srcPaths[1:] {
		selected := path.Clean(strings.TrimSpace(srcPath))
		for common != "." && common != "/" && selected != common && !strings.HasPrefix(selected, normalizeDirPath(common)) {
			common = path.Dir(common)
		}
	}
	return common
}

func normalizeDirPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return p
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

func jobAllowsFileSize(job map[string]interface{}, size int64) bool {
	minSize := util.ToInt64(job["minFileSize"])
	maxSize := util.ToInt64(job["maxFileSize"])
	if size < 0 {
		return minSize == 0 && maxSize == 0
	}
	if minSize > 0 && size < minSize {
		return false
	}
	if maxSize > 0 && size > maxSize {
		return false
	}
	return true
}

func fileChanged(srcVal, dstVal interface{}) bool {
	return fileChangedWithMode(srcVal, dstVal, 0)
}

// fileChangedWithMode compares source and destination metadata according to compareMode:
//   - 0 (default): size -> MD5 -> mtime + 2s tolerance
//   - 1 (size only): size difference only
//   - 2 (size + MD5): size -> MD5 (ignore mtime)
func fileChangedWithMode(srcVal, dstVal interface{}, compareMode int) bool {
	src := toFileMetadata(srcVal)
	dst := toFileMetadata(dstVal)
	if src.Size != dst.Size {
		return true
	}
	if compareMode == 1 {
		return false
	}
	if isMD5Digest(src.MD5) && isMD5Digest(dst.MD5) {
		return src.MD5 != dst.MD5
	}
	if compareMode == 2 {
		return false
	}
	if src.Modified > 0 && dst.Modified > 0 {
		return src.Modified > dst.Modified+fileModifiedTolerance
	}
	return false
}

// fileModifiedTolerance is the largest mtime difference (in seconds) that is
// treated as clock granularity rather than a change.
const fileModifiedTolerance = 2

// isMD5Digest reports whether value looks like a real MD5 hex digest.
func isMD5Digest(value string) bool {
	if len(value) != 32 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func fileSize(val interface{}) int64 {
	return toFileMetadata(val).Size
}

// normalizeStatModified turns driver-specific mtime units into Unix seconds.
func normalizeStatModified(modified int64) int64 {
	if modified > 1e12 {
		return modified / 1000
	}
	return modified
}

func toFileMetadata(val interface{}) FileMetadata {
	switch v := val.(type) {
	case FileMetadata:
		v.MD5 = normalizeMD5(v.MD5)
		return v
	case *FileMetadata:
		if v == nil {
			return FileMetadata{}
		}
		metadata := *v
		metadata.MD5 = normalizeMD5(metadata.MD5)
		return metadata
	case map[string]interface{}:
		return FileMetadata{
			Size:     util.ToInt64(v["size"]),
			MD5:      normalizeMD5(util.StringValue(v["md5"])),
			Modified: util.ToInt64(v["modified"]),
		}
	default:
		return FileMetadata{Size: util.ToInt64(val)}
	}
}
