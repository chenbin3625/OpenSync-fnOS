package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"opensync/internal/native"
)

func requirePublicError(t *testing.T, fn func() error) {
	t.Helper()
	if err := fn(); err == nil {
		t.Fatalf("expected error")
	}
}

func TestParsePathListPreservesColonInJSONPaths(t *testing.T) {
	got := parsePathList(`["/movies/director:cut","/backup"]`)
	want := []string{"/movies/director:cut", "/backup"}

	if len(got) != len(want) {
		t.Fatalf("parsePathList() length = %d, want %d (%#v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parsePathList()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParsePathListTreatsColonAsPartOfSinglePath(t *testing.T) {
	got := parsePathList("/alpha:/beta")
	want := []string{"/alpha:/beta"}

	if len(got) != len(want) {
		t.Fatalf("parsePathList() length = %d, want %d (%#v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parsePathList()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCleanJobInputNormalizesDstPathToJSON(t *testing.T) {
	job := map[string]interface{}{
		"dstPath": []interface{}{"/movies/director:cut", " /backup "},
	}

	if err := CleanJobInput(job); err != nil {
		t.Fatalf("CleanJobInput() error: %v", err)
	}
	got := parsePathList(job["dstPath"])
	want := []string{"/movies/director:cut", "/backup"}

	if len(got) != len(want) {
		t.Fatalf("normalized dstPath length = %d, want %d (%#v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalized dstPath[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParsePathListPreservesColonInPaths(t *testing.T) {
	got := parsePathList(`["/movies/director:cut","/backup"]`)
	want := []string{"/movies/director:cut", "/backup"}

	if len(got) != len(want) {
		t.Fatalf("parsePathList() length = %d, want %d (%#v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parsePathList()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParsePathListTreatsPlainStringAsSinglePath(t *testing.T) {
	got := parsePathList("/movies/director:cut")
	want := []string{"/movies/director:cut"}

	if len(got) != len(want) {
		t.Fatalf("parsePathList() length = %d, want %d (%#v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parsePathList()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestCleanJobInputNormalizesSrcPathToJSON(t *testing.T) {
	job := map[string]interface{}{
		"srcPath": []interface{}{"/photos", " /videos "},
	}

	if err := CleanJobInput(job); err != nil {
		t.Fatalf("CleanJobInput() error: %v", err)
	}
	got := parsePathList(job["srcPath"])
	want := []string{"/photos", "/videos"}

	if len(got) != len(want) {
		t.Fatalf("normalized srcPath length = %d, want %d (%#v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalized srcPath[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestValidateJobInputRejectsMissingRequiredFields(t *testing.T) {
	job := map[string]interface{}{
		"isCron": 2,
	}

	requirePublicError(t, func() error {
		return ValidateJobInput(job)
	})
}

func TestValidateJobInputAcceptsManualJobWithRequiredFields(t *testing.T) {
	job := map[string]interface{}{
		"srcPath": []string{"/src"},
		"dstPath": []string{"/dst"},
		"alistId": int64(1),
		"isCron":  2,
		"method":  0,
	}

	if err := ValidateJobInput(job); err != nil {
		t.Fatalf("ValidateJobInput() error: %v", err)
	}
}

func TestValidateJobInputRejectsOverlappingSourceAndDestinationPaths(t *testing.T) {
	for name, paths := range map[string]struct {
		src string
		dst string
	}{
		"same path": {
			src: "/data",
			dst: "/data/",
		},
		"destination is inside source": {
			src: "/data",
			dst: "/data/archive",
		},
		"source is inside destination": {
			src: "/data/archive",
			dst: "/data",
		},
	} {
		t.Run(name, func(t *testing.T) {
			job := map[string]interface{}{
				"srcPath": []string{paths.src},
				"dstPath": []string{paths.dst},
				"alistId": int64(1),
				"isCron":  2,
				"method":  1,
			}
			requirePublicError(t, func() error {
				return ValidateJobInput(job)
			})
		})
	}
}

func TestDstPathForSrcSelectionPreservesSourceDirWhenMultipleSelected(t *testing.T) {
	got := dstPathForSrcSelection("/backup/", "/media/photos", []string{"/media/photos", "/archive/videos"})
	want := "/backup/photos/"

	if got != want {
		t.Fatalf("dstPathForSrcSelection() = %q, want %q", got, want)
	}
}

func TestDstPathForSrcSelectionKeepsSingleSourceAtTargetRoot(t *testing.T) {
	got := dstPathForSrcSelection("/backup/", "/media/photos", []string{"/media/photos"})
	want := "/backup/"

	if got != want {
		t.Fatalf("dstPathForSrcSelection() = %q, want %q", got, want)
	}
}

// Selecting sibling directories is how a partially checked tree branch arrives:
// the user ticked every child of one folder, so that folder is already
// accounted for by the selection itself and must not reappear on the
// destination. This matches the single-source layout, where `srcPaths` is not
// involved at all.
func TestDstPathForSrcSelectionDropsParentOfSiblingSelection(t *testing.T) {
	srcPaths := []string{"/drive/2", "/drive/学习"}
	tests := []struct {
		srcPath string
		want    string
	}{
		{srcPath: "/drive/2", want: "/backup/2/"},
		{srcPath: "/drive/学习", want: "/backup/学习/"},
	}

	for _, tt := range tests {
		got := dstPathForSrcSelection("/backup/", tt.srcPath, srcPaths)
		if got != tt.want {
			t.Errorf("dstPathForSrcSelection(%q) = %q, want %q", tt.srcPath, got, tt.want)
		}
	}
}

// Only the shared parent is subtracted. Intermediate directories below it still
// belong to the selection and have to survive, otherwise two branches with the
// same leaf name would collide at the destination root.
func TestDstPathForSrcSelectionPreservesNestedPathsBelowCommonParent(t *testing.T) {
	srcPaths := []string{"/drive/work/2", "/drive/study/notes"}
	tests := []struct {
		srcPath string
		want    string
	}{
		{srcPath: "/drive/work/2", want: "/backup/work/2/"},
		{srcPath: "/drive/study/notes", want: "/backup/study/notes/"},
	}

	for _, tt := range tests {
		got := dstPathForSrcSelection("/backup/", tt.srcPath, srcPaths)
		if got != tt.want {
			t.Errorf("dstPathForSrcSelection(%q) = %q, want %q", tt.srcPath, got, tt.want)
		}
	}
}

// A mixed selection is the case that regressed in v1.13.0: siblings plus nested
// paths share the parent of the siblings, and that parent must be stripped
// rather than pushed to the destination.
func TestDstPathForSrcSelectionStripsSharedParentOfMixedSelection(t *testing.T) {
	srcPaths := []string{"/nas/docker", "/nas/photos", "/nas/temp", "/nas/common/sec", "/nas/common/sync"}
	tests := []struct {
		srcPath string
		want    string
	}{
		{srcPath: "/nas/docker", want: "/google/docker/"},
		{srcPath: "/nas/photos", want: "/google/photos/"},
		{srcPath: "/nas/temp", want: "/google/temp/"},
		{srcPath: "/nas/common/sec", want: "/google/common/sec/"},
		{srcPath: "/nas/common/sync", want: "/google/common/sync/"},
	}

	for _, tt := range tests {
		got := dstPathForSrcSelection("/google/", tt.srcPath, srcPaths)
		if got != tt.want {
			t.Errorf("dstPathForSrcSelection(%q) = %q, want %q", tt.srcPath, got, tt.want)
		}
	}
}

// Identically named leaves in different branches must stay separated. Keeping
// the intermediate directories is what prevents them from colliding at the
// destination root.
func TestDstPathForSrcSelectionKeepsIdenticalLeavesApart(t *testing.T) {
	srcPaths := []string{"/drive/work/2", "/drive/study/2"}
	tests := []struct {
		srcPath string
		want    string
	}{
		{srcPath: "/drive/work/2", want: "/backup/work/2/"},
		{srcPath: "/drive/study/2", want: "/backup/study/2/"},
	}

	seen := make(map[string]string, len(tests))
	for _, tt := range tests {
		got := dstPathForSrcSelection("/backup/", tt.srcPath, srcPaths)
		if got != tt.want {
			t.Errorf("dstPathForSrcSelection(%q) = %q, want %q", tt.srcPath, got, tt.want)
		}
		if other, dup := seen[got]; dup {
			t.Errorf("srcPaths %q and %q both resolve to %q", other, tt.srcPath, got)
		}
		seen[got] = tt.srcPath
	}
}

// Sources with no shared parent fall back to the historical base-name layout.
func TestDstPathForSrcSelectionUsesBaseNameWithoutSharedParent(t *testing.T) {
	srcPaths := []string{"/media/photos", "/archive/videos"}
	tests := []struct {
		srcPath string
		want    string
	}{
		{srcPath: "/media/photos", want: "/backup/photos/"},
		{srcPath: "/archive/videos", want: "/backup/videos/"},
	}

	for _, tt := range tests {
		got := dstPathForSrcSelection("/backup/", tt.srcPath, srcPaths)
		if got != tt.want {
			t.Errorf("dstPathForSrcSelection(%q) = %q, want %q", tt.srcPath, got, tt.want)
		}
	}
}

func TestCommonSrcSelectionParentReturnsDeepestSharedDirectory(t *testing.T) {
	tests := []struct {
		name     string
		srcPaths []string
		want     string
	}{
		{name: "siblings", srcPaths: []string{"/drive/2", "/drive/学习"}, want: "/drive"},
		{name: "nested", srcPaths: []string{"/drive/work/2", "/drive/study/notes"}, want: "/drive"},
		{name: "mixed", srcPaths: []string{"/nas/temp", "/nas/common/sec"}, want: "/nas"},
		{name: "no shared parent", srcPaths: []string{"/media/photos", "/archive/videos"}, want: "/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := commonSrcSelectionParent(tt.srcPaths); got != tt.want {
				t.Errorf("commonSrcSelectionParent(%#v) = %q, want %q", tt.srcPaths, got, tt.want)
			}
		})
	}
}

// Sources that share a base name must not collapse into one destination: in
// mirror mode each run would delete the other selection's files.
func TestDstPathForSrcSelectionDisambiguatesCollidingBaseNames(t *testing.T) {
	srcPaths := []string{"/a/docs", "/b/docs"}
	tests := []struct {
		srcPath string
		want    string
	}{
		{srcPath: "/a/docs", want: "/google/a/docs/"},
		{srcPath: "/b/docs", want: "/google/b/docs/"},
	}

	for _, tt := range tests {
		if got := dstPathForSrcSelection("/google", tt.srcPath, srcPaths); got != tt.want {
			t.Errorf("dstPathForSrcSelection(%q) = %q, want %q", tt.srcPath, got, tt.want)
		}
	}
}

// Selecting a directory together with one of its own children must not emit a
// doubled separator for the parent entry.
func TestDstPathForSrcSelectionHandlesParentSelectedWithChild(t *testing.T) {
	srcPaths := []string{"/nas", "/nas/photos"}
	tests := []struct {
		srcPath string
		want    string
	}{
		{srcPath: "/nas", want: "/google/"},
		{srcPath: "/nas/photos", want: "/google/photos/"},
	}

	for _, tt := range tests {
		if got := dstPathForSrcSelection("/google", tt.srcPath, srcPaths); got != tt.want {
			t.Errorf("dstPathForSrcSelection(%q) = %q, want %q", tt.srcPath, got, tt.want)
		}
	}
}

// Distinct base names keep the historical layout.
func TestDstPathForSrcSelectionKeepsBaseNameWhenUnique(t *testing.T) {
	srcPaths := []string{"/media/photos", "/archive/videos"}
	if got := dstPathForSrcSelection("/backup/", "/media/photos", srcPaths); got != "/backup/photos/" {
		t.Errorf("dstPathForSrcSelection = %q, want /backup/photos/", got)
	}
}

func TestStripNativeLocalRootID(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "my~folder")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", root)
	t.Setenv("TRIM_PKGTMP", "")
	roots := native.AuthorizedRoots()
	if len(roots) != 1 {
		t.Fatalf("expected 1 authorized root, got %+v", roots)
	}
	if got, ok := stripNativeLocalRootID(roots[0].ID); !ok || got != "my~folder" {
		t.Fatalf("stripNativeLocalRootID(%q) = (%q, %v), want (%q, true)", roots[0].ID, got, ok, "my~folder")
	}

	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"docker~b3fbe6ed1ca9", "docker", true},
		{"X~35c37bec9b16", "X", true},
		{"根目录~0123456789ab", "根目录", true},
		{"~~0123456789ab", "~", true},
		{"~0123456789ab", "~0123456789ab", false},
		{".~0123456789ab", ".~0123456789ab", false},
		{"..~0123456789ab", "..~0123456789ab", false},
		{"docker~B3FBE6ED1CA9", "docker~B3FBE6ED1CA9", false},
		{"docker~0123456789a", "docker~0123456789a", false},
		{"docker~0123456789abc", "docker~0123456789abc", false},
		{"docker~0123456789ag", "docker~0123456789ag", false},
		{"docker", "docker", false},
	}
	for _, tc := range cases {
		got, ok := stripNativeLocalRootID(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("stripNativeLocalRootID(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestDstPathForSrcSelectionStripsNativeLocalRootFingerprint(t *testing.T) {
	srcPaths := []string{
		"/本地/docker~b3fbe6ed1ca9",
		"/本地/X~35c37bec9b16",
		"/本地/photos~5f78544a37a3",
		"/本地/sec~972efbdc2b1f",
		"/本地/sync~bd3b4223a876",
	}
	tests := []struct {
		srcPath string
		want    string
	}{
		{"/本地/docker~b3fbe6ed1ca9", "/openlist/quark/docker/"},
		{"/本地/X~35c37bec9b16", "/openlist/quark/X/"},
		{"/本地/photos~5f78544a37a3", "/openlist/quark/photos/"},
		{"/本地/sec~972efbdc2b1f", "/openlist/quark/sec/"},
		{"/本地/sync~bd3b4223a876", "/openlist/quark/sync/"},
	}
	for _, tt := range tests {
		if got := dstPathForSrcSelection("/openlist/quark", tt.srcPath, srcPaths); got != tt.want {
			t.Errorf("dstPathForSrcSelection(%q) = %q, want %q", tt.srcPath, got, tt.want)
		}
	}

	// Subdirectories across different local roots strip only the root segment;
	// deeper subdirectories literally ending in ~<12hex> stay untouched.
	nested := []string{
		"/本地/docker~b3fbe6ed1ca9/foo~0123456789ab",
		"/本地/photos~5f78544a37a3/2025",
	}
	if got := dstPathForSrcSelection("/openlist/quark", nested[0], nested); got != "/openlist/quark/docker/foo~0123456789ab/" {
		t.Errorf("nested[0] = %q, want /openlist/quark/docker/foo~0123456789ab/", got)
	}
	if got := dstPathForSrcSelection("/openlist/quark", nested[1], nested); got != "/openlist/quark/photos/2025/" {
		t.Errorf("nested[1] = %q, want /openlist/quark/photos/2025/", got)
	}

	// Multiple subdirs under the same local root drop the shared root cleanly.
	sameRoot := []string{"/本地/docker~b3fbe6ed1ca9/nginx", "/本地/docker~b3fbe6ed1ca9/redis"}
	if got := commonSrcSelectionParent(sameRoot); got != "/本地/docker" {
		t.Errorf("commonSrcSelectionParent(sameRoot) = %q, want /本地/docker", got)
	}
	if got := dstPathForSrcSelection("/openlist/quark", sameRoot[0], sameRoot); got != "/openlist/quark/nginx/" {
		t.Errorf("sameRoot[0] = %q, want /openlist/quark/nginx/", got)
	}

	// Local-to-local destination preserves the target rootID while cleaning source suffixes.
	if got := dstPathForSrcSelection("/本地/backup~112233445566", srcPaths[0], srcPaths); got != "/本地/backup~112233445566/docker/" {
		t.Errorf("local-to-local dst = %q, want /本地/backup~112233445566/docker/", got)
	}
}

func TestDstPathForSrcSelectionKeepsFingerprintWhenNativeRootsCollide(t *testing.T) {
	// Two distinct volumes sharing a folder name (exact or case-insensitive) keep
	// their rootIDs, while non-colliding siblings in the same task still clean up.
	colliding := []string{
		"/本地/Photos~111111111111",
		"/本地/photos~222222222222",
		"/本地/sec~972efbdc2b1f",
	}
	want := map[string]string{
		"/本地/Photos~111111111111": "/openlist/quark/Photos~111111111111/",
		"/本地/photos~222222222222": "/openlist/quark/photos~222222222222/",
		"/本地/sec~972efbdc2b1f":    "/openlist/quark/sec/",
	}
	for _, s := range colliding {
		if got := dstPathForSrcSelection("/openlist/quark", s, colliding); got != want[s] {
			t.Errorf("dstPathForSrcSelection(%q) = %q, want %q", s, got, want[s])
		}
	}
	if resolvedDstPathsNested(colliding, []string{"/openlist/quark"}) {
		t.Fatal("disambiguated colliding native roots unexpectedly reported as nested")
	}

	// Root vs subdirectory on two different volumes with the same basename must
	// not collapse and nest (/docker/ vs /docker/sub/).
	rootVsSub := []string{
		"/本地/docker~111111111111",
		"/本地/docker~222222222222/sub",
	}
	if got := dstPathForSrcSelection("/openlist/quark", rootVsSub[0], rootVsSub); got != "/openlist/quark/docker~111111111111/" {
		t.Errorf("rootVsSub[0] = %q", got)
	}
	if got := dstPathForSrcSelection("/openlist/quark", rootVsSub[1], rootVsSub); got != "/openlist/quark/docker~222222222222/sub/" {
		t.Errorf("rootVsSub[1] = %q", got)
	}
	if resolvedDstPathsNested(rootVsSub, []string{"/openlist/quark"}) {
		t.Fatal("rootVsSub unexpectedly nested")
	}

	// Cross-alias local roots (e.g. /本地 and imported local remote /disk).
	crossAlias := []string{
		"/本地/docker~111111111111",
		"/disk/photos~222222222222",
	}
	if got := dstPathForSrcSelection("/openlist/quark", crossAlias[0], crossAlias); got != "/openlist/quark/docker/" {
		t.Errorf("crossAlias[0] = %q, want /openlist/quark/docker/", got)
	}
	if got := dstPathForSrcSelection("/openlist/quark", crossAlias[1], crossAlias); got != "/openlist/quark/photos/" {
		t.Errorf("crossAlias[1] = %q, want /openlist/quark/photos/", got)
	}

	// Cross-alias case-insensitive leaf collision disambiguates instead of colliding.
	crossCase := []string{
		"/本地/docker~111111111111",
		"/disk/other~222222222222/Docker",
	}
	if got := dstPathForSrcSelection("/openlist/quark", crossCase[0], crossCase); got != "/openlist/quark/本地/docker/" {
		t.Errorf("crossCase[0] = %q, want /openlist/quark/本地/docker/", got)
	}
	if got := dstPathForSrcSelection("/openlist/quark", crossCase[1], crossCase); got != "/openlist/quark/disk/other/Docker/" {
		t.Errorf("crossCase[1] = %q, want /openlist/quark/disk/other/Docker/", got)
	}
	if resolvedDstPathsNested(crossCase, []string{"/openlist/quark"}) {
		t.Fatal("crossCase unexpectedly nested")
	}
}

func TestNativeJobPersistedDstMapPreservesLegacyHashedFoldersUntilDstChanges(t *testing.T) {
	legacyMap, _ := json.Marshal(jobDstMap{
		Version: jobDstMapVersion,
		Base:    []string{"/openlist/quark/"},
		Map: map[string]string{
			jobDstMapKey("/本地/docker~b3fbe6ed1ca9", "/openlist/quark"): "/openlist/quark/docker~b3fbe6ed1ca9/",
			jobDstMapKey("/本地/photos~5f78544a37a3", "/openlist/quark"): "/openlist/quark/photos~5f78544a37a3/",
		},
	})
	job := map[string]interface{}{
		"srcPath": []string{"/本地/docker~b3fbe6ed1ca9", "/本地/photos~5f78544a37a3", "/本地/sec~972efbdc2b1f"},
		"dstPath": []string{"/openlist/quark"},
		"dstMap":  string(legacyMap),
	}
	if err := CleanJobInput(job); err != nil {
		t.Fatal(err)
	}
	// Existing sources stay at their persisted destinations; newly added source uses the clean name.
	if got := jobDstForSrc(job, "/本地/docker~b3fbe6ed1ca9", "/openlist/quark"); got != "/openlist/quark/docker~b3fbe6ed1ca9/" {
		t.Fatalf("legacy docker dst = %q, want preserved hashed dst", got)
	}
	if got := jobDstForSrc(job, "/本地/sec~972efbdc2b1f", "/openlist/quark"); got != "/openlist/quark/sec/" {
		t.Fatalf("new sec dst = %q, want /openlist/quark/sec/", got)
	}

	// Changing destination recomputes all entries with clean folder names.
	job["dstPath"] = []string{"/openlist/quark2"}
	if err := CleanJobInput(job); err != nil {
		t.Fatal(err)
	}
	if got := jobDstForSrc(job, "/本地/docker~b3fbe6ed1ca9", "/openlist/quark2"); got != "/openlist/quark2/docker/" {
		t.Fatalf("recomputed docker dst = %q, want /openlist/quark2/docker/", got)
	}
}

// A repeated selection entry is deduped, so it cannot be treated as two
// sources and produce a doubled separator.
func TestParsePathListDropsDuplicates(t *testing.T) {
	got := parsePathList([]string{"/nas/a", "/nas/a", " /nas/a "})
	if len(got) != 1 || got[0] != "/nas/a" {
		t.Fatalf("parsePathList = %#v, want one /nas/a", got)
	}
}


func TestCleanAndValidateJobInputRejectsTraversalAndUnsafeChars(t *testing.T) {
	for _, badPath := range []string{
		"/src/../other",
		"../other",
		"/src/..",
		`/src\sub`,
		"/src/\x00file",
		"/src/line\nbreak",
		".",
		"relative/path",
	} {
		job := map[string]interface{}{
			"srcPath": []string{badPath},
			"dstPath": []string{"/dst"},
			"alistId": int64(1),
			"isCron":  2,
			"method":  0,
		}
		if err := CleanJobInput(job); err != nil {
			continue
		}
		if err := ValidateJobInput(job); err == nil {
			t.Fatalf("ValidateJobInput() accepted unsafe path %q (after CleanJobInput: %v)", badPath, job["srcPath"])
		}
	}

	okJob := map[string]interface{}{
		"srcPath": []string{"/Photos/backup..2024"},
		"dstPath": []string{"/dst"},
		"alistId": int64(1),
		"isCron":  2,
		"method":  0,
	}
	if err := CleanJobInput(okJob); err != nil {
		t.Fatalf("CleanJobInput(valid dots) error: %v", err)
	}
	if err := ValidateJobInput(okJob); err != nil {
		t.Fatalf("ValidateJobInput(valid dots) error: %v", err)
	}
}

func TestSyncPathContainsEdgeCases(t *testing.T) {
	cases := []struct {
		parent    string
		candidate string
		want      bool
	}{
		{"", "", false},
		{"/", "", false},
		{"/", ".", false},
		{"/", "relative", false},
		{"/a/../b", "/b", false},
		{"/d/", "/d", true},
		{"/d", "/dx", false},
		{"/", "/data", true},
	}
	for _, tc := range cases {
		if got := syncPathContains(tc.parent, tc.candidate); got != tc.want {
			t.Errorf("syncPathContains(%q, %q) = %v, want %v", tc.parent, tc.candidate, got, tc.want)
		}
	}
}

func TestValidateJobInputStrictlyValidatesEnableMethodCronAndExclude(t *testing.T) {
	base := func() map[string]interface{} {
		return map[string]interface{}{
			"srcPath": []string{"/src"},
			"dstPath": []string{"/dst"},
			"alistId": int64(1),
			"isCron":  2,
			"method":  0,
		}
	}

	for name, mutate := range map[string]func(map[string]interface{}){
		"invalid enable string": func(j map[string]interface{}) { j["enable"] = "abc" },
		"invalid enable int":    func(j map[string]interface{}) { j["enable"] = 2 },
		"invalid enable float":  func(j map[string]interface{}) { j["enable"] = 1.5 },
		"invalid method string": func(j map[string]interface{}) { j["method"] = "abc" },
		"invalid method range":  func(j map[string]interface{}) { j["method"] = 4 },
		"invalid method float":  func(j map[string]interface{}) { j["method"] = 1.9 },
		"out of range cron sec": func(j map[string]interface{}) { j["isCron"] = 1; j["second"] = "99"; j["minute"] = "0" },
		"unsupported exclude":   func(j map[string]interface{}) { j["exclude"] = "!keep.txt" },
		"invalid size range":    func(j map[string]interface{}) { j["minFileSize"] = 100; j["maxFileSize"] = 50 },
	} {
		t.Run(name, func(t *testing.T) {
			job := base()
			mutate(job)
			if err := ValidateJobInput(job); err == nil {
				t.Fatalf("ValidateJobInput() accepted %s", name)
			}
		})
	}
}
