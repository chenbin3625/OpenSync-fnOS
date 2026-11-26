package service

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"opensync/internal/mapper"
	"opensync/internal/native"
)

func TestQuarkJobDefaultCompareModeAndWebDAVRegression(t *testing.T) {
	localRoot := t.TempDir()
	t.Setenv("TRIM_PKGTMP", "")
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", localRoot)
	roots := native.AuthorizedRoots()
	if len(roots) != 1 {
		t.Fatalf("expected 1 authorized root, got %+v", roots)
	}
	localSrc := "/" + native.Local + "/" + roots[0].ID

	cfg := native.Config{
		"qk": {
			"type":   "quark",
			"cookie": "__pus=1; __puus=2",
		},
		"qkCrypt": {
			"type":     "crypt",
			"remote":   "qk:vault",
			"password": base64.RawURLEncoding.EncodeToString(make([]byte, 24)),
		},
		"s3": {
			"type":              "s3",
			"provider":          "Minio",
			"endpoint":          "https://minio.local",
			"access_key_id":     "ak",
			"secret_access_key": "sk",
		},
		"openlist": {
			"type":   "webdav",
			"url":    "https://dav.example.com/dav",
			"vendor": "other",
		},
	}

	// Decision 1.A: local -> quark and s3 -> quark must NOT be locked to compareMode=1.
	if jobHasStandardWebDAVDst(cfg, map[string]interface{}{
		"srcPath": `["` + localSrc + `"]`,
		"dstPath": `["/qk/backup"]`,
	}) {
		t.Fatal("local -> quark must keep default compareMode=0 (auto Size+MD5), not force size-only")
	}
	if jobHasStandardWebDAVDst(cfg, map[string]interface{}{
		"srcPath": `["/s3/bucket/src"]`,
		"dstPath": `["/qk/backup"]`,
	}) {
		t.Fatal("s3 -> quark must keep default compareMode=0 (auto Size+MD5), not force size-only")
	}

	// local -> crypt(quark) cannot use native MD5 -> should default to size-only (1).
	if !jobHasStandardWebDAVDst(cfg, map[string]interface{}{
		"srcPath": `["` + localSrc + `"]`,
		"dstPath": `["/qkCrypt/backup"]`,
	}) {
		t.Fatal("local -> crypt(quark) must default to size-only")
	}

	// Regression guard: standard OpenList WebDAV still defaults to size-only (1).
	if !jobHasStandardWebDAVDst(cfg, map[string]interface{}{
		"srcPath": `["` + localSrc + `"]`,
		"dstPath": `["/openlist/backup"]`,
	}) {
		t.Fatal("local -> openlist WebDAV must still default to size-only")
	}
}

func TestPersistRefreshedQuarkCookie(t *testing.T) {
	defer setupNativeTestDB(t)()

	if _, err := EnsureNativeEngine(); err != nil {
		t.Fatalf("EnsureNativeEngine: %v", err)
	}
	orig := map[string]string{
		"type":         "quark",
		"cookie":       "__pus=abc; __puus=old_puus",
		"account_name": "测试号",
	}
	pan := map[string]string{"type": "pan123", "protocol": "open_api", "access_token": "old"}
	cfg := native.Config{"qk": orig, "pan": pan}
	raw, _ := json.Marshal(cfg)
	id, remark, _, _, err := loadExistingNativeRowFull()
	if err != nil || id == 0 {
		t.Fatalf("loadExistingNativeRowFull: id=%d err=%v", id, err)
	}
	secret := string(raw)
	if err := mapper.UpdateAlist(id, remark, native.EngineURL, &secret); err != nil {
		t.Fatalf("seed quark config: %v", err)
	}

	// Stale original must be ignored.
	persistRefreshedNativeTokens(map[string]native.TokenUpdate{
		"qk": {
			Original: map[string]string{"type": "quark", "cookie": "stale"},
			Fields:   map[string]string{"cookie": "__pus=abc; __puus=wrong"},
		},
	})
	_, _, _, loaded, err := loadExistingNativeRowFull()
	if err != nil || loaded["qk"]["cookie"] != "__pus=abc; __puus=old_puus" {
		t.Fatalf("stale update should be ignored, got %+v err=%v", loaded["qk"], err)
	}

	// Matching original persists rotated cookie.
	persistRefreshedNativeTokens(map[string]native.TokenUpdate{
		"qk": {
			Original: orig,
			Fields:   map[string]string{"cookie": "__pus=abc; __puus=new_puus"},
		},
		"pan": {
			Original: pan,
			Fields:   map[string]string{"access_token": "rotated", "token_expires_at": "2026-11-28T00:00:00Z"},
		},
	})
	_, _, _, loaded, err = loadExistingNativeRowFull()
	if err != nil || !strings.Contains(loaded["qk"]["cookie"], "__puus=new_puus") {
		t.Fatalf("expected rotated quark cookie persisted, got %+v err=%v", loaded["qk"], err)
	}
	if loaded["pan"]["access_token"] != "rotated" || loaded["pan"]["token_expires_at"] != "2026-11-28T00:00:00Z" {
		t.Fatal("expected both refreshed 123 cloud token fields to persist")
	}
}
