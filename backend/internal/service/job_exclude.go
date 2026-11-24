package service

import (
	"fmt"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"path"
	"strings"
	"time"
	"unicode"
)

type excludeRuleKind uint8

const (
	excludeFileName excludeRuleKind = iota
	excludeFileExtension
	excludeDirectory
)

type excludeRule struct {
	kind  excludeRuleKind
	value string
}

type excludeMatcher struct {
	rules []excludeRule
}

// excludeRuleOutcome distinguishes the two reasons a line yields no rule.
// Both used to be reported as "not ok", which meant a rule the user typed but
// we cannot honour (negation, **, character classes) was dropped as quietly as
// a blank line — the job then silently synced files the user believed were
// excluded. Only excludeRuleIgnored is silent now; excludeRuleRejected is
// surfaced as a validation error by validateExcludeRules.
type excludeRuleOutcome uint8

const (
	excludeRuleAccepted excludeRuleOutcome = iota
	excludeRuleIgnored
	excludeRuleRejected
)

// parseExcludePatterns keeps only the three supported rule forms:
// filename, *.extension, and relative directory/. Blank lines and comments are
// dropped silently; unsupported rules are dropped here too, but callers that
// accept user input run validateExcludeRules first so they never get this far.
func parseExcludePatterns(exclude string) []string {
	patterns := make([]string, 0, len(excludeRuleLines(exclude)))
	for _, line := range excludeRuleLines(exclude) {
		if pattern, _, outcome := normalizeExcludeRule(line); outcome == excludeRuleAccepted {
			patterns = append(patterns, pattern)
		}
	}
	return patterns
}

func excludeRuleLines(exclude string) []string {
	return strings.Split(strings.ReplaceAll(exclude, "\r\n", "\n"), "\n")
}

// invalidExcludeRules returns the input lines that express a rule we cannot
// honour, in the order the user wrote them, so the error message can name them.
func invalidExcludeRules(exclude string) []string {
	var invalid []string
	for _, line := range excludeRuleLines(exclude) {
		if _, _, outcome := normalizeExcludeRule(line); outcome == excludeRuleRejected {
			invalid = append(invalid, strings.TrimSpace(strings.TrimSuffix(line, "\r")))
		}
	}
	return invalid
}

func normalizeExclude(exclude string) string {
	return strings.Join(parseExcludePatterns(exclude), "\n")
}

func normalizeExcludeRule(input string) (string, excludeRuleKind, excludeRuleOutcome) {
	value := strings.TrimSpace(strings.TrimSuffix(input, "\r"))
	if value == "" {
		return "", 0, excludeRuleIgnored
	}
	// A "# " comment is documentation, not a rule the user expects to match.
	if strings.HasPrefix(value, "# ") {
		return "", 0, excludeRuleIgnored
	}
	// Negation reverses the meaning of a rule. Dropping it silently would
	// exclude exactly what the user asked to keep.
	if strings.HasPrefix(value, "!") {
		return "", 0, excludeRuleRejected
	}

	// Older defaults escaped a directory beginning with "#". The directory
	// form is unambiguous because it always ends with a slash.
	if strings.HasPrefix(value, `\#`) && strings.HasSuffix(value, "/") {
		value = strings.TrimPrefix(value, `\`)
	}

	if strings.HasSuffix(value, "/") {
		directory := strings.TrimSuffix(value, "/")
		if !validDirectoryRule(directory) {
			return "", 0, excludeRuleRejected
		}
		return directory + "/", excludeDirectory, excludeRuleAccepted
	}

	if strings.HasPrefix(value, "#") {
		return "", 0, excludeRuleIgnored
	}

	if strings.HasPrefix(value, "*.") {
		extension := strings.TrimPrefix(value, "*.")
		if !validExtensionRule(extension) {
			return "", 0, excludeRuleRejected
		}
		return "*." + strings.ToLower(extension), excludeFileExtension, excludeRuleAccepted
	}

	if !validFileNameRule(value) {
		return "", 0, excludeRuleRejected
	}
	return value, excludeFileName, excludeRuleAccepted
}

func validDirectoryRule(value string) bool {
	if value == "" ||
		strings.HasPrefix(value, "/") ||
		strings.HasSuffix(value, "/") ||
		strings.Contains(value, "//") ||
		strings.ContainsAny(value, `\?[]{}!`) ||
		containsControl(value) {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	if !strings.Contains(value, "*") {
		return true
	}
	// A single-level directory name may use the same simple * wildcard as file
	// names (e.g. the long-shipped ".Trash-*/" default). Wildcards in nested
	// paths, "**", and a rule made only of stars (which would exclude every
	// directory) stay unsupported.
	return !strings.Contains(value, "/") &&
		!strings.Contains(value, "**") &&
		strings.Trim(value, "*") != ""
}

func validExtensionRule(value string) bool {
	return value != "" &&
		!strings.HasPrefix(value, ".") &&
		!strings.HasSuffix(value, ".") &&
		!strings.ContainsAny(value, `/\*?[]{}!`) &&
		!containsControl(value)
}

func validFileNameRule(value string) bool {
	return value != "" &&
		value != "." &&
		value != ".." &&
		!strings.Contains(value, "/") &&
		!strings.ContainsAny(value, `\?[]{}!`) &&
		value != "*" &&
		!strings.Contains(value, "**") &&
		!containsControl(value)
}

func containsControl(value string) bool {
	for _, char := range value {
		if unicode.IsControl(char) {
			return true
		}
	}
	return false
}

func newExcludeMatcher(exclude string) *excludeMatcher {
	lines := parseExcludePatterns(exclude)
	matcher := &excludeMatcher{rules: make([]excludeRule, 0, len(lines))}
	for _, line := range lines {
		pattern, kind, outcome := normalizeExcludeRule(line)
		if outcome == excludeRuleAccepted {
			matcher.rules = append(matcher.rules, excludeRule{kind: kind, value: pattern})
		}
	}
	return matcher
}

func (matcher *excludeMatcher) matches(relativePath string, isDir bool) bool {
	if matcher == nil {
		return false
	}

	relativePath = strings.TrimSpace(relativePath)
	if relativePath == "" || containsControl(relativePath) || strings.ContainsRune(relativePath, '\\') {
		return false
	}
	isDirectory := isDir || strings.HasSuffix(relativePath, "/")
	cleanPath := strings.Trim(path.Clean(strings.TrimSuffix(relativePath, "/")), "/")
	if cleanPath == "" || cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, "../") {
		return false
	}
	segments := strings.Split(cleanPath, "/")
	baseName := segments[len(segments)-1]

	for _, rule := range matcher.rules {
		switch rule.kind {
		case excludeFileName:
			if !isDirectory && simpleWildcardMatch(rule.value, baseName) {
				return true
			}
		case excludeFileExtension:
			if !isDirectory &&
				strings.HasSuffix(strings.ToLower(baseName), strings.TrimPrefix(rule.value, "*")) {
				return true
			}
		case excludeDirectory:
			// "build/" names a directory; a plain file called build is synced.
			// Both sides are filtered alike, so this never exposes an excluded
			// directory to deletion.
			rulePath := strings.TrimSuffix(rule.value, "/")
			if strings.Contains(rulePath, "/") {
				if (isDirectory && (cleanPath == rulePath || strings.HasSuffix(cleanPath, "/"+rulePath))) ||
					strings.HasPrefix(cleanPath, rulePath+"/") ||
					strings.Contains(cleanPath, "/"+rulePath+"/") {
					return true
				}
				continue
			}
			if isDirectory && simpleWildcardMatch(rulePath, baseName) {
				return true
			}
			for _, ancestor := range segments[:len(segments)-1] {
				if simpleWildcardMatch(rulePath, ancestor) {
					return true
				}
			}
		}
	}
	return false
}

// matchesListed checks a listed entry against the rules under both its raw
// relative path and its canonical spelling. Source and destination entries are
// paired by canonicalFileName, so a destination that stores an escaped or
// fullwidth form of a name ("a：b", "a%3Ab") must be excluded by the same rule
// that excludes the source spelling. Otherwise the excluded destination entry
// matches nothing at the source and mirror mode deletes it as extra.
func (matcher *excludeMatcher) matchesListed(relativePath string, isDir bool) bool {
	if matcher.matches(relativePath, isDir) {
		return true
	}
	canonical := canonicalRelativePath(relativePath)
	return canonical != relativePath && matcher.matches(canonical, isDir)
}

func canonicalRelativePath(relativePath string) string {
	suffix := ""
	trimmed := relativePath
	if strings.HasSuffix(trimmed, "/") {
		suffix = "/"
		trimmed = strings.TrimSuffix(trimmed, "/")
	}
	segments := strings.Split(trimmed, "/")
	for i, segment := range segments {
		segments[i] = canonicalFileName(segment)
	}
	return strings.Join(segments, "/") + suffix
}

func simpleWildcardMatch(pattern, value string) bool {
	pRunes := []rune(pattern)
	vRunes := []rune(value)
	patternIndex, valueIndex := 0, 0
	lastStar, lastMatch := -1, 0
	for valueIndex < len(vRunes) {
		if patternIndex < len(pRunes) &&
			(pRunes[patternIndex] == vRunes[valueIndex] || pRunes[patternIndex] == '*') {
			if pRunes[patternIndex] == '*' {
				lastStar = patternIndex
				lastMatch = valueIndex
				patternIndex++
			} else {
				patternIndex++
				valueIndex++
			}
			continue
		}
		if lastStar == -1 {
			return false
		}
		patternIndex = lastStar + 1
		lastMatch++
		valueIndex = lastMatch
	}
	for patternIndex < len(pRunes) && pRunes[patternIndex] == '*' {
		patternIndex++
	}
	return patternIndex == len(pRunes)
}

// --- Include (Whitelist) Rules, Time Filters & Filter Tester ---

func normalizeIncludeRule(input string) (string, excludeRuleKind, excludeRuleOutcome) {
	pattern, kind, outcome := normalizeExcludeRule(input)
	if outcome != excludeRuleAccepted {
		return pattern, kind, outcome
	}
	if kind == excludeDirectory {
		// Include whitelist applies to file names and *.ext patterns only.
		return "", 0, excludeRuleRejected
	}
	return pattern, kind, excludeRuleAccepted
}

func parseIncludePatterns(include string) []string {
	lines := excludeRuleLines(include)
	patterns := make([]string, 0, len(lines))
	for _, line := range lines {
		if pattern, _, outcome := normalizeIncludeRule(line); outcome == excludeRuleAccepted {
			patterns = append(patterns, pattern)
		}
	}
	return patterns
}

func invalidIncludeRules(include string) []string {
	var invalid []string
	for _, line := range excludeRuleLines(include) {
		if _, _, outcome := normalizeIncludeRule(line); outcome == excludeRuleRejected {
			invalid = append(invalid, strings.TrimSpace(strings.TrimSuffix(line, "\r")))
		}
	}
	return invalid
}

func normalizeInclude(include string) string {
	return strings.Join(parseIncludePatterns(include), "\n")
}

type includeMatcher struct {
	rules []excludeRule
}

func newIncludeMatcher(include string) *includeMatcher {
	lines := parseIncludePatterns(include)
	if len(lines) == 0 {
		return nil
	}
	matcher := &includeMatcher{rules: make([]excludeRule, 0, len(lines))}
	for _, line := range lines {
		pattern, kind, outcome := normalizeIncludeRule(line)
		if outcome == excludeRuleAccepted {
			matcher.rules = append(matcher.rules, excludeRule{kind: kind, value: pattern})
		}
	}
	if len(matcher.rules) == 0 {
		return nil
	}
	return matcher
}

func (m *includeMatcher) matchRule(relativePath string) (string, bool) {
	if m == nil || len(m.rules) == 0 {
		return "", true
	}
	cleanPath := strings.Trim(path.Clean(strings.TrimSuffix(strings.TrimSpace(relativePath), "/")), "/")
	if cleanPath == "" || cleanPath == "." || cleanPath == ".." {
		return "", false
	}
	baseName := path.Base(cleanPath)
	for _, rule := range m.rules {
		switch rule.kind {
		case excludeFileName:
			if simpleWildcardMatch(rule.value, baseName) {
				return rule.value, true
			}
		case excludeFileExtension:
			if strings.HasSuffix(strings.ToLower(baseName), strings.TrimPrefix(rule.value, "*")) {
				return rule.value, true
			}
		}
	}
	return "", false
}

func (m *includeMatcher) matchesListed(relativePath string) bool {
	if m == nil || len(m.rules) == 0 {
		return true
	}
	if _, ok := m.matchRule(relativePath); ok {
		return true
	}
	canonical := canonicalRelativePath(relativePath)
	if canonical != relativePath {
		_, ok := m.matchRule(canonical)
		return ok
	}
	return false
}

func (matcher *excludeMatcher) matchedRule(relativePath string, isDir bool) (string, bool) {
	if matcher == nil {
		return "", false
	}
	check := func(p string) (string, bool) {
		p = strings.TrimSpace(p)
		if p == "" || containsControl(p) || strings.ContainsRune(p, '\\') {
			return "", false
		}
		isDirectory := isDir || strings.HasSuffix(p, "/")
		cleanPath := strings.Trim(path.Clean(strings.TrimSuffix(p, "/")), "/")
		if cleanPath == "" || cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, "../") {
			return "", false
		}
		segments := strings.Split(cleanPath, "/")
		baseName := segments[len(segments)-1]
		for _, rule := range matcher.rules {
			switch rule.kind {
			case excludeFileName:
				if !isDirectory && simpleWildcardMatch(rule.value, baseName) {
					return rule.value, true
				}
			case excludeFileExtension:
				if !isDirectory && strings.HasSuffix(strings.ToLower(baseName), strings.TrimPrefix(rule.value, "*")) {
					return rule.value, true
				}
			case excludeDirectory:
				rulePath := strings.TrimSuffix(rule.value, "/")
				if strings.Contains(rulePath, "/") {
					if (isDirectory && (cleanPath == rulePath || strings.HasSuffix(cleanPath, "/"+rulePath))) ||
						strings.HasPrefix(cleanPath, rulePath+"/") ||
						strings.Contains(cleanPath, "/"+rulePath+"/") {
						return rule.value, true
					}
					continue
				}
				if isDirectory && simpleWildcardMatch(rulePath, baseName) {
					return rule.value, true
				}
				for _, ancestor := range segments[:len(segments)-1] {
					if simpleWildcardMatch(rulePath, ancestor) {
						return rule.value, true
					}
				}
			}
		}
		return "", false
	}
	if r, ok := check(relativePath); ok {
		return r, true
	}
	canonical := canonicalRelativePath(relativePath)
	if canonical != relativePath {
		return check(canonical)
	}
	return "", false
}

// buildJobExcludeMatcher constructs a job's effective excludeMatcher,
// automatically adding the configured trashDir/ when soft-delete is enabled.
func buildJobExcludeMatcher(job map[string]interface{}) *excludeMatcher {
	excludeStr := util.StringValue(job["exclude"])
	if trashEnabled(job) {
		trashRule := jobTrashDir(job) + "/"
		if excludeStr == "" {
			excludeStr = trashRule
		} else {
			excludeStr = excludeStr + "\n" + trashRule
		}
	}
	if strings.TrimSpace(excludeStr) == "" {
		return nil
	}
	return newExcludeMatcher(excludeStr)
}

// buildJobIncludeMatcher constructs the whitelist matcher when filterMode == 1.
func buildJobIncludeMatcher(job map[string]interface{}) *includeMatcher {
	if job == nil || util.ToInt(job["filterMode"]) != 1 {
		return nil
	}
	inc := util.StringValue(job["include"])
	if strings.TrimSpace(inc) == "" {
		return nil
	}
	return newIncludeMatcher(inc)
}

// jobHasTimeFilters reports whether any time-based file filter is configured.
func jobHasTimeFilters(job map[string]interface{}) bool {
	if job == nil {
		return false
	}
	return util.ToInt(job["minFileAgeMinutes"]) > 0 ||
		util.ToInt(job["olderThanDays"]) > 0 ||
		util.ToInt(job["newerThanDays"]) > 0
}

// jobAllowsFileMeta checks file size and modification-time filters.
func jobAllowsFileMeta(job map[string]interface{}, meta FileMetadata, now time.Time) bool {
	if !jobAllowsFileSize(job, meta.Size) {
		return false
	}
	if !jobHasTimeFilters(job) {
		return true
	}
	mod := normalizeStatModified(meta.Modified)
	if mod <= 0 {
		// Without modification time, fail closed when a time filter is active.
		return false
	}
	age := now.Sub(time.Unix(mod, 0))
	if age < 0 {
		age = 0
	}
	if minAgeMin := util.ToInt(job["minFileAgeMinutes"]); minAgeMin > 0 {
		if age < time.Duration(minAgeMin)*time.Minute {
			return false
		}
	}
	if olderDays := util.ToInt(job["olderThanDays"]); olderDays > 0 {
		if age < time.Duration(olderDays)*24*time.Hour {
			return false
		}
	}
	if newerDays := util.ToInt(job["newerThanDays"]); newerDays > 0 {
		if age > time.Duration(newerDays)*24*time.Hour {
			return false
		}
	}
	return true
}

// TestJobFilter evaluates a sample file/folder path against filter rules and
// returns whether it would sync along with the matched rule and explanation.
func TestJobFilter(req map[string]interface{}) (map[string]interface{}, error) {
	samplePath := strings.TrimSpace(util.StringValue(req["samplePath"]))
	if samplePath == "" {
		return nil, publicError(msg.T(msg.LostPart))
	}
	sampleIsDir := util.ToBool(req["sampleIsDir"]) || strings.HasSuffix(samplePath, "/")
	sampleSize := util.ToInt64(req["sampleSize"])
	if sampleSize < 0 {
		sampleSize = 0
	}
	sampleAgeMinutes := util.ToInt(req["sampleAgeMinutes"])
	if sampleAgeMinutes < 0 {
		sampleAgeMinutes = 0
	}

	excludeStr := util.StringValue(req["exclude"])
	if invalid := invalidExcludeRules(excludeStr); len(invalid) > 0 {
		return nil, publicError(msg.ExcludeRulesUnsupported(invalid))
	}
	includeStr := util.StringValue(req["include"])
	if invalid := invalidIncludeRules(includeStr); len(invalid) > 0 {
		return nil, publicError(msg.IncludeRulesUnsupported(invalid))
	}

	excludeM := buildJobExcludeMatcher(req)
	if rule, excluded := excludeM.matchedRule(samplePath, sampleIsDir); excluded {
		return map[string]interface{}{
			"willSync":    false,
			"reason":      fmt.Sprintf("命中排除规则：%s", rule),
			"matchedRule": rule,
		}, nil
	}

	if sampleIsDir {
		return map[string]interface{}{
			"willSync":    true,
			"reason":      "目录未被排除，将进入扫描",
			"matchedRule": "",
		}, nil
	}

	if includeM := buildJobIncludeMatcher(req); includeM != nil {
		rule, ok := includeM.matchRule(samplePath)
		if !ok {
			return map[string]interface{}{
				"willSync":    false,
				"reason":      "未命中白名单包含规则",
				"matchedRule": "",
			}, nil
		}
		// Continue to size/time checks after matching whitelist
		_ = rule
	}

	if !jobAllowsFileSize(req, sampleSize) {
		minSize := util.ToInt64(req["minFileSize"])
		maxSize := util.ToInt64(req["maxFileSize"])
		if minSize > 0 && sampleSize < minSize {
			return map[string]interface{}{
				"willSync":    false,
				"reason":      fmt.Sprintf("文件大小 (%s) 小于最小限制 (%s)", util.ConvertBytes(sampleSize), util.ConvertBytes(minSize)),
				"matchedRule": "minFileSize",
			}, nil
		}
		return map[string]interface{}{
			"willSync":    false,
			"reason":      fmt.Sprintf("文件大小 (%s) 大于最大限制 (%s)", util.ConvertBytes(sampleSize), util.ConvertBytes(maxSize)),
			"matchedRule": "maxFileSize",
		}, nil
	}

	now := time.Now()
	meta := FileMetadata{
		Size:     sampleSize,
		Modified: now.Add(-time.Duration(sampleAgeMinutes) * time.Minute).Unix(),
	}
	if !jobAllowsFileMeta(req, meta, now) {
		if minAge := util.ToInt(req["minFileAgeMinutes"]); minAge > 0 && sampleAgeMinutes < minAge {
			return map[string]interface{}{
				"willSync":    false,
				"reason":      fmt.Sprintf("文件距上次修改不足 %d 分钟（最小稳定时间）", minAge),
				"matchedRule": "minFileAgeMinutes",
			}, nil
		}
		if olderDays := util.ToInt(req["olderThanDays"]); olderDays > 0 && sampleAgeMinutes < olderDays*24*60 {
			return map[string]interface{}{
				"willSync":    false,
				"reason":      fmt.Sprintf("文件修改时间晚于 %d 天前（仅同步早于 %d 天的文件）", olderDays, olderDays),
				"matchedRule": "olderThanDays",
			}, nil
		}
		if newerDays := util.ToInt(req["newerThanDays"]); newerDays > 0 && sampleAgeMinutes > newerDays*24*60 {
			return map[string]interface{}{
				"willSync":    false,
				"reason":      fmt.Sprintf("文件修改时间早于最近 %d 天（仅同步最近 %d 天内的文件）", newerDays, newerDays),
				"matchedRule": "newerThanDays",
			}, nil
		}
	}

	matched := ""
	if includeM := buildJobIncludeMatcher(req); includeM != nil {
		if r, ok := includeM.matchRule(samplePath); ok {
			matched = r
		}
	}
	return map[string]interface{}{
		"willSync":    true,
		"reason":      "通过全部过滤规则，将被同步",
		"matchedRule": matched,
	}, nil
}
