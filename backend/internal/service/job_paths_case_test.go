package service

import "testing"

// SMB, cloud drives and macOS/Windows volumes fold case, so /Photos and
// /photos are one directory there. A case-sensitive comparison let such a pair
// through as "not overlapping" and mirror mode then computed deletes for a
// directory it was also copying into.
func TestSyncPathsOverlapIgnoresCase(t *testing.T) {
	tests := []struct {
		name     string
		src      []string
		dst      []string
		expected bool
	}{
		{"same path differing case", []string{"/Photos"}, []string{"/photos"}, true},
		{"destination inside source, mixed case", []string{"/Media"}, []string{"/media/backup"}, true},
		{"source inside destination, mixed case", []string{"/MEDIA/raw"}, []string{"/media"}, true},
		{"genuinely different paths", []string{"/photos"}, []string{"/videos"}, false},
		{"sibling with shared prefix", []string{"/media"}, []string{"/mediaback"}, false},
	}
	for _, test := range tests {
		if got := syncPathsOverlap(test.src, test.dst); got != test.expected {
			t.Errorf("%s: syncPathsOverlap(%v, %v) = %v, want %v",
				test.name, test.src, test.dst, got, test.expected)
		}
	}
}

func TestSrcSelectionsNestedIgnoresCase(t *testing.T) {
	if !srcSelectionsNested([]string{"/Media", "/media/photos"}) {
		t.Error("srcSelectionsNested() accepted a nested pair that differs only in case")
	}
	if srcSelectionsNested([]string{"/photos", "/videos"}) {
		t.Error("srcSelectionsNested() rejected two unrelated paths")
	}
}

func TestValidateJobInputRejectsCaseOnlyPathOverlap(t *testing.T) {
	err := ValidateJobInput(map[string]interface{}{
		"srcPath": `["/Photos"]`,
		"dstPath": `["/photos"]`,
		"alistId": 1,
		"method":  0,
		"isCron":  2,
	})
	if err == nil {
		t.Fatal("ValidateJobInput() accepted source and destination differing only in case")
	}
}
