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
	if relativePath == "" {
		return false
	}
	isDirectory := isDir || strings.HasSuffix(relativePath, "/")
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
