package service

import "testing"

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
		if got := matcher.matches(test.path); got != test.expected {
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
