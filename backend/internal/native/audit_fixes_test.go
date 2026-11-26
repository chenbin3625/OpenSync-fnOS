package native

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestWrapperOverLocalAndCombineEndpointConflicts(t *testing.T) {
	tmp := t.TempDir()
	rootDir := filepath.Join(tmp, "nas")
	for _, sub := range []string{"a", "b", "enc"} {
		if err := os.MkdirAll(filepath.Join(rootDir, sub), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", rootDir)
	t.Setenv("TRIM_PKGTMP", "")

	roots := AuthorizedRoots()
	if len(roots) != 1 {
		t.Fatalf("expected 1 authorized root, got %d", len(roots))
	}
	localRef := Local + ":" + roots[0].ID

	c := Config{
		"localAlias": {
			"type":   "alias",
			"remote": localRef,
		},
		"localCrypt": {
			"type":     "crypt",
			"remote":   localRef + "/enc",
			"password": "AAAAAAAAAAAAAAAAAAAAAA",
		},
		"s3": {
			"type":              "s3",
			"provider":          "Alibaba",
			"endpoint":          "oss-cn-hangzhou.aliyuncs.com",
			"access_key_id":     "ak",
			"secret_access_key": "sk",
		},
		"chunk": {
			"type":   "chunker",
			"remote": "s3:bucket/base",
		},
		"deepCrypt": {
			"type":     "crypt",
			"remote":   "chunk:enc",
			"password": "AAAAAAAAAAAAAAAAAAAAAA",
		},
		"combo": {
			"type":      "combine",
			"upstreams": "photos=s3:bucket/photos docs=s3:bucket/docs",
		},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("validate config: %v", err)
	}

	// Disjoint subdirs under alias->local must NOT conflict (bug #2).
	epA, err := c.Resolve("/localAlias/a")
	if err != nil {
		t.Fatalf("resolve /localAlias/a: %v", err)
	}
	epB, err := c.Resolve("/localAlias/b")
	if err != nil {
		t.Fatalf("resolve /localAlias/b: %v", err)
	}
	if EndpointsConflict(epA, epB) {
		t.Fatalf("disjoint subdirs under alias->local must not conflict: %+v vs %+v", epA, epB)
	}
	if _, _, err := c.Pair("/localAlias/a", "/localAlias/b"); err != nil {
		t.Fatalf("sync between disjoint alias->local subdirs must succeed: %v", err)
	}
	// Alias subdir MUST still conflict with direct local path to the same subdir.
	epDirectA, err := c.Resolve("/" + Local + "/" + roots[0].ID + "/a")
	if err != nil {
		t.Fatal(err)
	}
	if !EndpointsConflict(epA, epDirectA) {
		t.Fatalf("alias->local/a must conflict with direct /本地/.../a")
	}

	// Crypt over local must still isolate sibling plaintext dirs while conflicting with raw backing dir.
	epCrypt1, err := c.Resolve("/localCrypt/p1")
	if err != nil {
		t.Fatal(err)
	}
	epCrypt2, err := c.Resolve("/localCrypt/p2")
	if err != nil {
		t.Fatal(err)
	}
	epRawEnc, err := c.Resolve("/" + Local + "/" + roots[0].ID + "/enc/raw")
	if err != nil {
		t.Fatal(err)
	}
	if EndpointsConflict(epCrypt1, epCrypt2) {
		t.Fatal("sibling dirs in same local crypt must not conflict")
	}
	if !EndpointsConflict(epCrypt1, epRawEnc) {
		t.Fatal("local crypt must conflict with raw path inside its backing dir")
	}

	// Multi-layer wrapper (crypt -> chunker -> s3:bucket/base) must retain full backing prefix.
	epDeep, err := c.Resolve("/deepCrypt/sub")
	if err != nil {
		t.Fatal(err)
	}
	epOutsideDeep, err := c.Resolve("/s3/bucket/other")
	if err != nil {
		t.Fatal(err)
	}
	epInsideDeep, err := c.Resolve("/s3/bucket/base/enc/file")
	if err != nil {
		t.Fatal(err)
	}
	if EndpointsConflict(epDeep, epOutsideDeep) {
		t.Fatalf("deepCrypt backing %q must not conflict with /s3/bucket/other", epDeep.BackingIdentity)
	}
	if !EndpointsConflict(epDeep, epInsideDeep) {
		t.Fatalf("deepCrypt backing %q must conflict with /s3/bucket/base/enc/file", epDeep.BackingIdentity)
	}

	// Combine remote scoped to one mount dir must not conflict with sibling mount dir.
	epComboPhotos, err := c.Resolve("/combo/photos/2026")
	if err != nil {
		t.Fatal(err)
	}
	epComboDocs, err := c.Resolve("/combo/docs/2026")
	if err != nil {
		t.Fatal(err)
	}
	epDirectPhotos, err := c.Resolve("/s3/bucket/photos/2026")
	if err != nil {
		t.Fatal(err)
	}
	if EndpointsConflict(epComboPhotos, epComboDocs) {
		t.Fatalf("disjoint combine mount dirs must not conflict: %+v vs %+v", epComboPhotos.UnionBackingIDs, epComboDocs.UnionBackingIDs)
	}
	if !EndpointsConflict(epComboPhotos, epDirectPhotos) {
		t.Fatalf("combine mount dir must conflict with direct upstream path")
	}
}

func TestCaseInsensitiveRemoteResolutionAndHelpers(t *testing.T) {
	c := Config{
		"MyOSS": {
			"type":              "s3",
			"provider":          "Alibaba",
			"endpoint":          "oss-cn-hangzhou.aliyuncs.com",
			"access_key_id":     "ak",
			"secret_access_key": "sk",
		},
		"Vault": {
			"type":     "crypt",
			"remote":   "MyOSS:bucket/enc",
			"password": "AAAAAAAAAAAAAAAAAAAAAA",
		},
		"AliasVault": {
			"type":   "alias",
			"remote": "Vault:sub",
		},
	}

	// Case-variant remote lookup in Resolve must return canonical RemoteName and Path (bug #4).
	ep, err := c.Resolve("/myoss/bucket/dir")
	if err != nil {
		t.Fatalf("case-insensitive Resolve failed: %v", err)
	}
	if ep.RemoteName != "MyOSS" || ep.Path != "MyOSS:bucket/dir" {
		t.Fatalf("expected canonical MyOSS, got RemoteName=%q Path=%q", ep.RemoteName, ep.Path)
	}

	// SessionFor must resolve case-variant names.
	s, err := c.SessionFor([]SessionSpec{{Name: "myoss", Mode: diskReadOnly}}, SessionOptions{})
	if err != nil {
		t.Fatalf("SessionFor case-insensitive failed: %v", err)
	}
	s.Close()

	// RemoteHasCrypt must traverse wrapper chains (bug #13).
	if !c.RemoteHasCrypt("AliasVault") || !c.RemoteHasCrypt("aliasvault") {
		t.Fatal("expected RemoteHasCrypt to detect crypt beneath alias")
	}
	if c.RemoteHasCrypt("MyOSS") {
		t.Fatal("expected RemoteHasCrypt to be false for plain S3")
	}

	// AffectedRemotes must include transitive dependents (bug #5).
	aff := c.AffectedRemotes([]string{"myoss"})
	for _, want := range []string{"MyOSS", "Vault", "AliasVault"} {
		if !aff[want] {
			t.Fatalf("expected %q in AffectedRemotes, got %+v", want, aff)
		}
	}
}

func TestListDirEntriesWithHashLocal(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "hello.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", tmp)
	t.Setenv("TRIM_PKGTMP", "")

	roots := AuthorizedRoots()
	c := Config{}
	entries, exists, err := c.ListDirEntriesWithHash(context.Background(), "/"+Local+"/"+roots[0].ID, true, SessionOptions{})
	if err != nil || !exists || len(entries) != 1 {
		t.Fatalf("ListDirEntriesWithHash failed: entries=%+v exists=%v err=%v", entries, exists, err)
	}
	// MD5("hello") = 5d41402abc4b2a76b9719d911017c592
	if entries[0].MD5 != "5d41402abc4b2a76b9719d911017c592" {
		t.Fatalf("unexpected local MD5: %q", entries[0].MD5)
	}
}

func TestBisyncCompareMode2IgnoresModtime(t *testing.T) {
	// Verify rclone bisync flags built for Compare=2:
	// - with MD5-capable endpoints -> --compare size,checksum (no modtime)
	// - with hashless endpoint -> --compare size,checksum --download-hash
	tmp := t.TempDir()
	fakeRclone := filepath.Join(tmp, "rclone")
	argsLog := filepath.Join(tmp, "args.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"" + argsLog + "\"\nexit 0\n"
	if err := os.WriteFile(fakeRclone, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENSYNC_RCLONE_BIN", fakeRclone)

	dirA := filepath.Join(tmp, "a")
	dirB := filepath.Join(tmp, "b")
	_ = os.MkdirAll(dirA, 0755)
	_ = os.MkdirAll(dirB, 0755)
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", dirA+":"+dirB)
	t.Setenv("TRIM_PKGTMP", "")
	roots := AuthorizedRoots()

	c := Config{
		"dav": {"type": "webdav", "url": "http://127.0.0.1:5244/dav", "vendor": "other"},
	}

	// Local <-> Local (both support native MD5): expect --compare size,checksum without --download-hash.
	work1 := filepath.Join(tmp, "w1")
	_, err := c.Bisync(context.Background(), "/"+Local+"/"+roots[0].ID, "/"+Local+"/"+roots[1].ID, BisyncOptions{
		WorkDir: work1,
		Compare: 2,
	}, nil)
	if err != nil {
		t.Fatalf("bisync local-local: %v", err)
	}
	rawArgs, _ := os.ReadFile(argsLog)
	args := splitNonEmptyLines(string(rawArgs))
	if !containsPair(args, "--compare", "size,checksum") {
		t.Fatalf("expected --compare size,checksum, got %v", args)
	}
	if slices.Contains(args, "--download-hash") {
		t.Fatalf("did not expect --download-hash when both endpoints support MD5: %v", args)
	}

	// Local <-> Standard WebDAV (hashless): expect --compare size,checksum --download-hash.
	work2 := filepath.Join(tmp, "w2")
	_, err = c.Bisync(context.Background(), "/"+Local+"/"+roots[0].ID, "/dav/sub", BisyncOptions{
		WorkDir: work2,
		Compare: 2,
	}, nil)
	if err != nil {
		t.Fatalf("bisync local-webdav: %v", err)
	}
	rawArgs, _ = os.ReadFile(argsLog)
	args = splitNonEmptyLines(string(rawArgs))
	if !containsPair(args, "--compare", "size,checksum") || !slices.Contains(args, "--download-hash") {
		t.Fatalf("expected --compare size,checksum --download-hash for hashless bisync, got %v", args)
	}
}

func TestEffectiveCompareModeForOpenListAndWrappers(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", srcDir)
	t.Setenv("TRIM_PKGTMP", "")
	roots := AuthorizedRoots()
	localSrc := "/" + Local + "/" + roots[0].ID

	c := Config{
		"openlist": {
			"type":   "webdav",
			"url":    "http://127.0.0.1:5244/dav",
			"vendor": "other",
		},
		"openlistNoVendor": {
			"type": "webdav",
			"url":  "http://127.0.0.1:5244/dav",
		},
		"nextcloud": {
			"type":   "webdav",
			"url":    "https://nc.example.com/remote.php/dav/files/alice",
			"vendor": "nextcloud",
		},
		"oss": {
			"type":              "s3",
			"provider":          "Alibaba",
			"endpoint":          "oss-cn-hangzhou.aliyuncs.com",
			"access_key_id":     "ak",
			"secret_access_key": "sk",
		},
		"cryptOpenlist": {
			"type":     "crypt",
			"remote":   "openlist:enc",
			"password": "AAAAAAAAAAAAAAAAAAAAAA",
		},
		"aliasCryptOpenlist": {
			"type":   "alias",
			"remote": "cryptOpenlist:photos",
		},
		"unionOpenlist": {
			"type":      "union",
			"upstreams": "oss:bucket/a openlist:b",
		},
		"combo": {
			"type":      "combine",
			"upstreams": "dav=openlist: oss=oss:bucket",
		},
	}

	srcEp, err := c.Resolve(localSrc)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		dst      string
		inMode   int
		wantMode int
	}{
		{"/openlist/quark", 0, 1},
		{"/openlist/quark", 1, 1},
		{"/openlist/quark", 2, 2},
		{"/openlistNoVendor/quark", 0, 1},
		{"/cryptOpenlist/sub", 0, 1},
		{"/aliasCryptOpenlist/2026", 0, 1},
		{"/unionOpenlist/sub", 0, 1},
		{"/combo/dav/photos", 0, 1},
		{"/combo/oss/photos", 0, 0},
		{"/nextcloud/photos", 0, 0},
		{"/oss/bucket/photos", 0, 0},
	}
	for _, tc := range cases {
		dstEp, err := c.Resolve(tc.dst)
		if err != nil {
			t.Fatalf("resolve %s: %v", tc.dst, err)
		}
		if got := EffectiveCompareMode(srcEp, dstEp, tc.inMode); got != tc.wantMode {
			t.Fatalf("EffectiveCompareMode(%s -> %s, %d) = %d, want %d", localSrc, tc.dst, tc.inMode, got, tc.wantMode)
		}
	}

	// Verify Copy with Compare=0 to OpenList invokes rclone copy --size-only and NEVER invokes check --download.
	fakeRclone := filepath.Join(tmp, "rclone")
	argsLog := filepath.Join(tmp, "copy-args.txt")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"" + argsLog + "\"\nprintf '---\\n' >> \"" + argsLog + "\"\nexit 0\n"
	if err := os.WriteFile(fakeRclone, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENSYNC_RCLONE_BIN", fakeRclone)

	if err := c.Copy(context.Background(), localSrc, "/openlist/quark", CopyOptions{
		Transfers: 2,
		Checkers:  2,
		Compare:   0,
	}, nil); err != nil {
		t.Fatalf("Copy to openlist failed: %v", err)
	}
	rawArgs, _ := os.ReadFile(argsLog)
	args := splitNonEmptyLines(string(rawArgs))
	if slices.Contains(args, "--download") || slices.Contains(args, "check") {
		t.Fatalf("Copy to OpenList under Compare=0 must not invoke check --download, got %v", args)
	}
	if !slices.Contains(args, "copy") || !slices.Contains(args, "--size-only") {
		t.Fatalf("Copy to OpenList under Compare=0 must pass --size-only, got %v", args)
	}
}

func splitNonEmptyLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '\n' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	return out
}

func containsPair(args []string, flag, val string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == val {
			return true
		}
	}
	return false
}
