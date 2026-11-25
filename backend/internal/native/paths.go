package native

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
)

type localRoot struct {
	Path, ID, LegacyID string
	Device             uint64
}

// LocalRootInfo exposes an fnOS-authorized local directory for UI display.
type LocalRootInfo struct {
	Path string `json:"path"`
	ID   string `json:"id"`
}

// AuthorizedRoots returns the currently authorized local directories in sorted order.
func AuthorizedRoots() []LocalRootInfo {
	roots := authorizedRoots()
	out := make([]LocalRootInfo, 0, len(roots))
	for _, r := range roots {
		out = append(out, LocalRootInfo{Path: r.Path, ID: r.ID})
	}
	slices.SortFunc(out, func(a, b LocalRootInfo) int { return strings.Compare(a.Path, b.Path) })
	return out
}

type rootsCacheState struct {
	mu      sync.Mutex
	key     string
	dir     string
	fileIno uint64
	fileMod int64
	fileSz  int64
	roots   []localRoot
}

var cachedRoots rootsCacheState

func authorizedRoots() []localRoot {
	dir := os.Getenv("TRIM_PKGTMP")
	if dir != "" {
		rootsFile := filepath.Join(dir, "native-roots")
		st, err := os.Stat(rootsFile)
		if err != nil {
			return nil
		}
		var ino uint64
		if sys, ok := st.Sys().(*syscall.Stat_t); ok {
			ino = sys.Ino
		}
		mod := st.ModTime().UnixNano()
		sz := st.Size()

		cachedRoots.mu.Lock()
		if cachedRoots.dir == rootsFile && cachedRoots.fileIno == ino && cachedRoots.fileMod == mod && cachedRoots.fileSz == sz {
			// Re-verify each cached root's path still points to the same inode/fsid.
			if rootsStillValid(cachedRoots.roots) {
				out := slices.Clone(cachedRoots.roots)
				cachedRoots.mu.Unlock()
				return out
			}
		}
		cachedRoots.mu.Unlock()

		data, err := os.ReadFile(rootsFile)
		if err != nil {
			return nil
		}
		roots := parseAuthorizedRoots(string(data))
		cachedRoots.mu.Lock()
		cachedRoots.dir = rootsFile
		cachedRoots.key = ""
		cachedRoots.fileIno = ino
		cachedRoots.fileMod = mod
		cachedRoots.fileSz = sz
		cachedRoots.roots = slices.Clone(roots)
		cachedRoots.mu.Unlock()
		return roots
	}

	grants := os.Getenv("TRIM_DATA_ACCESSIBLE_PATHS")
	return parseAuthorizedRoots(grants)
}

func rootsStillValid(roots []localRoot) bool {
	for _, r := range roots {
		realPath, err := filepath.EvalSymlinks(r.Path)
		if err != nil || realPath != r.Path {
			return false
		}
		info, err := os.Stat(realPath)
		if err != nil || !info.IsDir() || rootID(realPath, info) != r.ID {
			return false
		}
		if sys, ok := info.Sys().(*syscall.Stat_t); !ok || uint64(sys.Dev) != r.Device {
			return false
		}
	}
	return true
}

func parseAuthorizedRoots(grants string) []localRoot {
	var roots []localRoot
	seen := make(map[string]struct{})
	for _, p := range filepath.SplitList(grants) {
		p = strings.TrimSpace(p)
		if !filepath.IsAbs(p) {
			continue
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		info, err := os.Stat(real)
		if err != nil || !info.IsDir() {
			continue
		}
		id := rootID(real, info)
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		roots = append(roots, localRoot{
			Path:     real,
			ID:       id,
			LegacyID: legacyRootID(real, info),
			Device:   uint64(info.Sys().(*syscall.Stat_t).Dev),
		})
	}
	return roots
}

// AuthorizedCount returns the number of currently accessible fnOS-authorized local roots.
func AuthorizedCount() int {
	return len(authorizedRoots())
}

// rootID identifies a local authorized root using its canonical path, stable
// filesystem ID (fsid), and directory inode. It avoids st.Dev because Linux
// Btrfs subvolumes and hot-plugged USB drives receive dynamic device numbers
// across reboots.
func rootID(p string, info os.FileInfo) string {
	st := info.Sys().(*syscall.Stat_t)
	fs0, fs1 := filesystemID(p)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d/%d/%d", p, fs0, fs1, st.Ino)))
	base := filepath.Base(p)
	if base == "/" {
		base = "根目录"
	}
	return fmt.Sprintf("%s~%x", base, sum[:6])
}

// legacyRootID matches local root IDs persisted by earlier 0.0.45 builds that
// hashed st.Dev instead of fsid, so upgrading keeps existing native jobs valid.
func legacyRootID(p string, info os.FileInfo) string {
	st := info.Sys().(*syscall.Stat_t)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%d/%d", p, st.Dev, st.Ino)))
	base := filepath.Base(p)
	if base == "/" {
		base = "根目录"
	}
	return fmt.Sprintf("%s~%x", base, sum[:6])
}

func matchesRootID(p string, info os.FileInfo, id string) bool {
	return rootID(p, info) == id || legacyRootID(p, info) == id
}

func virtualLocal(p string) (string, error) {
	if filepath.Clean(p) != p {
		return "", errors.New("无效的本地路径")
	}
	for _, root := range authorizedRoots() {
		rel, e := filepath.Rel(root.Path, p)
		if e == nil && (rel == "." || filepath.IsLocal(rel)) {
			return path.Join("/", Local, root.ID, rel), nil
		}
	}
	return "", errors.New("目录不在 fnOS 授权范围内")
}

// The identity embedded in a stored path detects a disappeared or replaced mount.
func resolveLocal(sub string) (string, error) {
	id, rel, _ := strings.Cut(sub, "/")
	for _, r := range authorizedRoots() {
		if r.ID != id && r.LegacyID != id {
			continue
		}
		p := r.Path
		for _, name := range strings.Split(rel, "/") {
			if name == "" {
				continue
			}
			p = filepath.Join(p, name)
			st, e := os.Lstat(p)
			if os.IsNotExist(e) {
				continue
			}
			if e == nil && uint64(st.Sys().(*syscall.Stat_t).Dev) != r.Device {
				return "", errors.New("此路径跨越其他挂载卷，请在飞牛中直接授权该卷上的目录")
			}
			if e != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
				return "", errors.New("本地路径含链接或不可访问的目录")
			}
		}
		return p, nil
	}
	return "", errors.New("授权目录或磁盘已变更，请检查挂载与目录授权")
}

type Endpoint struct {
	Path            string
	Backend         string
	Vendor          string
	LocalPath       string
	Identity        string
	BackingIdentity string
	CryptScope      string
	Sub             string
	IsCrypt         bool
}

func cryptChainFingerprint(c Config, start string) string {
	h := sha256.New()
	for ref := start; c[ref]["type"] == "crypt"; {
		p := c[ref]
		fmt.Fprintf(h, "crypt\x00")
		for _, k := range slices.Sorted(maps.Keys(p)) {
			fmt.Fprintf(h, "%s=%s\x00", k, p[k])
		}
		ref, _, _ = strings.Cut(p["remote"], ":")
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
}

// Resolve maps only trusted names to rclone arguments. It never accepts an
// inline connection string or lets a configured local remote expose the host root.
func (c Config) Resolve(v string) (Endpoint, error) {
	v = strings.TrimSuffix(v, "/")
	name, sub, _ := strings.Cut(strings.TrimPrefix(v, "/"), "/")
	if !strings.HasPrefix(v, "/") || !cleanRelative(sub) {
		return Endpoint{}, errors.New("无效的备份路径")
	}
	if name == Local || c[name]["type"] == "local" {
		p, e := resolveLocal(sub)
		id := "/local" + p
		return Endpoint{Path: p, Backend: "local", LocalPath: p, Identity: id, BackingIdentity: id, Sub: sub}, e
	}
	if c[name] == nil {
		return Endpoint{}, errors.New("备份存储不存在")
	}
	isCrypt := c[name]["type"] == "crypt"
	ref := name
	var backing string
	rest := sub
	for c[ref]["type"] == "crypt" {
		var prefix string
		ref, prefix, _ = strings.Cut(c[ref]["remote"], ":")
		backing = prefix
		rest = strings.TrimSuffix(strings.Trim(prefix+"/"+rest, "/"), "/")
	}
	options := c[ref]
	result := Endpoint{
		Path:    name + ":" + sub,
		Backend: options["type"],
		Vendor:  strings.ToLower(strings.TrimSpace(options["vendor"])),
		Sub:     sub,
		IsCrypt: isCrypt,
	}
	// Never include credentials or access keys in a path identity. Aliases of
	// the same server protect the same backup tree even with different keys.
	host := strings.ToLower(strings.Trim(options["host"], "[]"))
	port := options["port"]
	if port == "" && result.Backend == "sftp" {
		port = "22"
	}
	if port == "" && result.Backend == "smb" {
		port = "445"
	}
	location := host + ":" + port
	switch result.Backend {
	case "s3":
		location = normalizeS3Location(options)
	case "webdav":
		location = strings.TrimRight(options["url"], "/")
	case "sftp":
		location = options["user"] + "@" + location
	}
	backingScope := rest
	if isCrypt {
		backingScope = backing
	}
	result.BackingIdentity = path.Join("/", result.Backend, location, backingScope)
	result.Identity = result.BackingIdentity
	if ref == Local || c[ref]["type"] == "local" {
		// Crypt encodes the child names. Its physical scope is the backing directory.
		p, e := resolveLocal(backing)
		if e != nil {
			return Endpoint{}, e
		}
		result.LocalPath = p
		result.BackingIdentity = "/local" + p
		result.Identity = result.BackingIdentity
		result.Backend = "local"
	}
	if isCrypt {
		fp := cryptChainFingerprint(c, name)
		result.CryptScope = path.Join(result.BackingIdentity, fp)
		result.Identity = path.Join("/crypt", result.CryptScope, sub)
	}
	if (result.Backend == "s3" || result.Backend == "smb") && rest == "" {
		return Endpoint{}, errors.New("请选择存储桶或共享目录")
	}
	return result, nil
}

func normalizeS3Location(options map[string]string) string {
	loc := strings.TrimRight(strings.TrimSpace(options["endpoint"]), "/")
	if loc == "" {
		return options["provider"] + "/" + options["region"]
	}
	loc = strings.TrimPrefix(loc, "https://")
	loc = strings.TrimPrefix(loc, "http://")
	return strings.ToLower(loc)
}

func endpointPathContains(parent, candidate string) bool {
	parent = strings.ToLower(path.Clean("/" + strings.TrimPrefix(parent, "/")))
	candidate = strings.ToLower(path.Clean("/" + strings.TrimPrefix(candidate, "/")))
	if parent == candidate {
		return true
	}
	if parent == "/" {
		return strings.HasPrefix(candidate, "/")
	}
	return strings.HasPrefix(candidate, parent+"/")
}

// EndpointsConflict reports whether two backup targets write to overlapping
// locations. Sibling subdirectories within the same crypt remote (e.g.
// /vault/photos and /vault/docs) are independent and do not conflict, whereas
// a crypt target and a plain target (or a different crypt) sharing the same
// physical backing tree do conflict.
func EndpointsConflict(a, b Endpoint) bool {
	if a.CryptScope != "" && a.CryptScope == b.CryptScope {
		return endpointPathContains(a.Sub, b.Sub) || endpointPathContains(b.Sub, a.Sub)
	}
	return endpointPathContains(a.BackingIdentity, b.BackingIdentity) ||
		endpointPathContains(b.BackingIdentity, a.BackingIdentity)
}

func (c Config) List(ctx context.Context, v string) ([]map[string]string, error) {
	result := []map[string]string{}
	push := func(label, child string) { result = append(result, map[string]string{"name": label, "path": child}) }
	if v == "/" {
		push(Local, "/"+Local)
		for n := range c {
			push(n, "/"+n)
		}
		slices.SortFunc(result, func(a, b map[string]string) int {
			if a["name"] == Local && b["name"] != Local {
				return -1
			}
			if b["name"] == Local && a["name"] != Local {
				return 1
			}
			return strings.Compare(a["name"], b["name"])
		})
		return result, nil
	}

	n, sub, _ := strings.Cut(strings.Trim(v, "/"), "/")
	if n == Local || c[n]["type"] == "local" {
		if sub == "" {
			for _, r := range authorizedRoots() {
				push(r.Path, path.Join(v, r.ID))
			}
		} else {
			_, e := c.Resolve(v)
			if e != nil {
				return nil, e
			}
			root, rel, e := openDisk(sub)
			if e != nil {
				return nil, e
			}
			defer root.Close()
			entries, e := fs.ReadDir(root.FS(), rel)
			if e != nil {
				return nil, errors.New("无法读取本地授权目录")
			}
			for _, entry := range entries {
				if entry.IsDir() {
					push(entry.Name(), path.Join(v, entry.Name()))
				}
			}
		}
	} else {
		if c[n] == nil || !cleanRelative(sub) {
			return nil, errors.New("远端路径无效")
		}
		session, e := c.Session(n, false, false)
		if e != nil {
			return nil, e
		}
		defer session.Close()
		cmd, e := session.Cmd(ctx, "lsjson", "--dirs-only", "--no-mimetype", "--no-modtime", "--", n+":"+sub)
		if e != nil {
			return nil, e
		}
		out, stderr, e := limitedOutputWithStderr(cmd, 16<<20)
		if e != nil {
			return nil, classifyRcloneError(c[n]["type"], stderr)
		}
		var rows []struct {
			Name  string
			IsDir bool
		}
		if json.Unmarshal(out, &rows) != nil {
			return nil, errors.New("无法读取远端目录清单")
		}
		for _, r := range rows {
			if r.IsDir && r.Name != "" && r.Name != "." && r.Name != ".." && !strings.ContainsAny(r.Name, "/\\\n\r") {
				push(r.Name, path.Join(v, r.Name))
			}
		}
	}
	slices.SortFunc(result, func(a, b map[string]string) int { return strings.Compare(a["name"], b["name"]) })
	return result, nil
}
