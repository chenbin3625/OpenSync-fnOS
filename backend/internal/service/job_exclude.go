package service

import (
	"path"
	"strings"
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

// parseExcludePatterns keeps only the three supported rule forms:
// filename, *.extension, and relative directory/. Comments and unsupported
// operators are intentionally ignored.
func parseExcludePatterns(exclude string) []string {
	lines := strings.Split(strings.ReplaceAll(exclude, "\r\n", "\n"), "\n")
	patterns := make([]string, 0, len(lines))
	for _, line := range lines {
		if pattern, _, ok := normalizeExcludeRule(line); ok {
			patterns = append(patterns, pattern)
		}
	}
	return patterns
}

func normalizeExclude(exclude string) string {
	return strings.Join(parseExcludePatterns(exclude), "\n")
}

func normalizeExcludeRule(input string) (string, excludeRuleKind, bool) {
	value := strings.TrimSpace(strings.TrimSuffix(input, "\r"))
	if value == "" || strings.HasPrefix(value, "!") {
		return "", 0, false
	}
	if strings.HasPrefix(value, "# ") {
		return "", 0, false
	}

	// Older defaults escaped a directory beginning with "#". The directory
	// form is unambiguous because it always ends with a slash.
	if strings.HasPrefix(value, `\#`) && strings.HasSuffix(value, "/") {
		value = strings.TrimPrefix(value, `\`)
	}

	if strings.HasSuffix(value, "/") {
		directory := strings.TrimSuffix(value, "/")
		if !validDirectoryRule(directory) {
			return "", 0, false
		}
		return directory + "/", excludeDirectory, true
	}

	if strings.HasPrefix(value, "#") {
		return "", 0, false
	}

	if strings.HasPrefix(value, "*.") {
		extension := strings.TrimPrefix(value, "*.")
		if !validExtensionRule(extension) {
			return "", 0, false
		}
		return "*." + strings.ToLower(extension), excludeFileExtension, true
	}

	if !validFileNameRule(value) {
		return "", 0, false
	}
	return value, excludeFileName, true
}

func validDirectoryRule(value string) bool {
	return value != "" &&
		!strings.HasPrefix(value, "/") &&
		!strings.HasSuffix(value, "/") &&
		!strings.Contains(value, "//") &&
		!strings.ContainsAny(value, `\*?[]{}!`) &&
		!containsControl(value)
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
		pattern, kind, ok := normalizeExcludeRule(line)
		if ok {
			matcher.rules = append(matcher.rules, excludeRule{kind: kind, value: pattern})
		}
	}
	return matcher
}

func (matcher *excludeMatcher) matches(relativePath string) bool {
	if matcher == nil {
		return false
	}

	relativePath = strings.TrimSpace(relativePath)
	if relativePath == "" {
		return false
	}
	isDirectory := strings.HasSuffix(relativePath, "/")
	cleanPath := strings.Trim(path.Clean(strings.TrimSuffix(relativePath, "/")), "/")
	if cleanPath == "" || cleanPath == "." {
		return false
	}
	baseName := path.Base(cleanPath)

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
			rulePath := strings.TrimSuffix(rule.value, "/")
			if strings.Contains(rulePath, "/") {
				if cleanPath == rulePath || strings.HasSuffix(cleanPath, "/"+rulePath) {
					return true
				}
			} else if baseName == rulePath {
				return true
			}
		}
	}
	return false
}

func simpleWildcardMatch(pattern, value string) bool {
	patternIndex, valueIndex := 0, 0
	lastStar, lastMatch := -1, 0
	for valueIndex < len(value) {
		if patternIndex < len(pattern) &&
			(pattern[patternIndex] == value[valueIndex] || pattern[patternIndex] == '*') {
			if pattern[patternIndex] == '*' {
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
	for patternIndex < len(pattern) && pattern[patternIndex] == '*' {
		patternIndex++
	}
	return patternIndex == len(pattern)
}
