package service

import (
	"strings"
	"testing"
)

func TestParseExcludePatternsSupportsNewlinesAndComments(t *testing.T) {
	input := "# macOS\n.DS_Store\n\n._*\r\n# Windows\nThumbs.db\n"

	got := parseExcludePatterns(input)
	want := []string{".DS_Store", "._*", "Thumbs.db"}

	if len(got) != len(want) {
		t.Fatalf("parseExcludePatterns() length = %d, want %d (%#v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseExcludePatterns()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParseExcludePatternsTreatsColonAsLiteralPattern(t *testing.T) {
	got := parseExcludePatterns("foo:bar.txt")
	want := []string{"foo:bar.txt"}

	if len(got) != len(want) {
		t.Fatalf("parseExcludePatterns() length = %d, want %d (%#v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseExcludePatterns()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNormalizeExcludeStoresNewlineSeparatedRules(t *testing.T) {
	got := normalizeExclude("# comment\n*.tmp\n.git/\nnode_modules/\n!important.txt")
	want := "*.tmp\n.git/\nnode_modules/"

	if got != want {
		t.Fatalf("normalizeExclude() = %q, want %q", got, want)
	}
}

func TestParseExcludePatternsAcceptsOnlySupportedRuleKinds(t *testing.T) {
	input := "*.PST\nreport+1.txt\nphotos/raw/\nreport[1].txt\n!important.txt\nfoo/*.txt\n\\#recycle/\n# #recycle/"
	got := parseExcludePatterns(input)
	want := []string{"*.pst", "report+1.txt", "photos/raw/", "#recycle/"}

	if len(got) != len(want) {
		t.Fatalf("parseExcludePatterns() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parseExcludePatterns()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExcludeMatcherSeparatesNamesExtensionsAndDirectories(t *testing.T) {
	matcher := newExcludeMatcher("report.txt\n*.pst\nphotos/raw/\ncache/")

	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{"exact file name", "nested/report.txt", true},
		{"file name is case sensitive", "nested/REPORT.TXT", false},
		{"extension is case insensitive", "mail/archive.PST", true},
		{"different extension", "mail/archive.zip", false},
		{"nested directory path", "photos/raw/", true},
		{"directory name matches at any depth", "backup/cache/", true},
		{"directory rule does not match a file", "backup/cache.txt", false},
		{"negation is not supported", "important.txt", false},
	}

	for _, test := range tests {
		isDir := strings.HasSuffix(test.path, "/")
		if got := matcher.matches(test.path, isDir); got != test.expected {
			t.Errorf("%s: matches(%q) = %v, want %v", test.name, test.path, got, test.expected)
		}
	}
}

func TestExcludeMatchPathOmitsLeadingSlashForRootFiles(t *testing.T) {
	if got := excludeMatchPath("/src", "/src", "file.tmp"); got != "file.tmp" {
		t.Fatalf("excludeMatchPath(root file) = %q, want file.tmp", got)
	}
	if got := excludeMatchPath("/src", "/src/dir", "file.tmp"); got != "dir/file.tmp" {
		t.Fatalf("excludeMatchPath(nested file) = %q, want dir/file.tmp", got)
	}
}

// A rule the user typed but we cannot honour must not be dropped in silence:
// the job would keep syncing files the user believes are excluded.
func TestInvalidExcludeRulesNamesUnsupportedLines(t *testing.T) {
	input := "*.tmp\n!important.txt\nfoo/*.txt\nreport[1].txt\n**/cache\n# comment\n\ncache/"
	got := invalidExcludeRules(input)
	want := []string{"!important.txt", "foo/*.txt", "report[1].txt", "**/cache"}

	if len(got) != len(want) {
		t.Fatalf("invalidExcludeRules() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("invalidExcludeRules()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// Blank lines and comments carry no rule, so they stay silently ignored.
func TestInvalidExcludeRulesIgnoresBlankLinesAndComments(t *testing.T) {
	if got := invalidExcludeRules("# macOS\n\n.DS_Store\n# Windows\r\nThumbs.db\n"); len(got) > 0 {
		t.Fatalf("invalidExcludeRules() = %#v, want none", got)
	}
}

// Every rule the UI ships as a default must survive save validation, or the
// default form would be unsavable.
func TestInvalidExcludeRulesAcceptsShippedDefaults(t *testing.T) {
	defaults := []string{
		".DS_Store", "._*", ".Spotlight-V100/", ".Trashes/", ".fseventsd/",
		".DocumentRevisions-V100/", ".TemporaryItems/", "Thumbs.db", "Desktop.ini",
		"$RECYCLE.BIN/", "System Volume Information/", "lost+found/", "@eaDir/",
		"#recycle/", `\#recycle/`, "@Recycle/", ".Recycle/", ".recycle/", ".Trash/",
		".Trash-1000/", "*.tmp", "*.temp", "*.part", "*.crdownload", "*.download",
		".~lock.*#", "~$*", ".#*", "*.swp", "*.swo", "*.swn",
	}
	for _, rule := range defaults {
		if got := invalidExcludeRules(rule); len(got) > 0 {
			t.Errorf("shipped default %q rejected as %#v", rule, got)
		}
	}
}

func TestCleanJobInputRejectsUnsupportedExcludeRule(t *testing.T) {
	err := CleanJobInput(map[string]interface{}{"exclude": "*.tmp\n!keep.txt"})
	if err == nil {
		t.Fatal("CleanJobInput() accepted an unsupported exclude rule")
	}
}
