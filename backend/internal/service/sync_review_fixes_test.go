package service

import (
	"testing"
)

// Exclusion must survive the canonical spelling a destination driver reports:
// source and destination entries are paired by canonicalFileName, so a rule
// that only matched the raw name left the escaped destination entry unmatched
// and mirror mode deleted it as extra.
func TestExcludeMatchesCanonicalSpellingOfListedName(t *testing.T) {
	matcher := newExcludeMatcher("a:b/\nnotes:1.txt")
	cases := []struct {
		path  string
		isDir bool
	}{
		{"a:b/", true},
		{"a\uFF1Ab/", true},   // fullwidth colon
		{"a%3Ab/", true},      // URL-escaped
		{"x/a\uFF1Ab/", true}, // nested
		{"notes\uFF1A1.txt", false},
		{"dir/notes%3A1.txt", false},
	}
	for _, tc := range cases {
		if !matcher.matchesListed(tc.path, tc.isDir) {
			t.Errorf("matchesListed(%q) = false, want excluded", tc.path)
		}
	}
	if matcher.matchesListed("a-b/", true) {
		t.Fatal("unrelated directory excluded")
	}
}

// A macOS/SMB source reports NFD names while the destination stores NFC; the
// two spellings are the same file and must pair instead of being re-copied and
// (in mirror mode) deleted.
func TestCanonicalFileNameFoldsUnicodeNormalization(t *testing.T) {
	nfc := "caf\u00e9.jpg"
	nfd := "cafe\u0301.jpg"
	if canonicalFileName(nfc) != canonicalFileName(nfd) {
		t.Fatalf("canonicalFileName(NFC) = %q, (NFD) = %q, want equal", canonicalFileName(nfc), canonicalFileName(nfd))
	}

	src := &fullSyncSnapshot{root: "/src/", dirs: map[string]FileListResult{"": {nfd: {Size: 5}}}}
	dst := &fullSyncSnapshot{root: "/dst/", dirs: map[string]FileListResult{"": {nfc: {Size: 5}}}}
	plan := newFullSyncPlan(src, dst, nil)
	plan.build(map[string]interface{}{"method": 1})
	if len(plan.newFiles) != 0 || len(plan.extraDeletes) != 0 {
		t.Fatalf("newFiles=%d extraDeletes=%d, want the NFD/NFC pair matched", len(plan.newFiles), len(plan.extraDeletes))
	}
}
