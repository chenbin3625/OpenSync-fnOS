package native

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
)

// TreeEntry represents one file or directory in a recursive native tree scan.
type TreeEntry struct {
	RelPath string
	Name    string
	IsDir   bool
	Size    int64
	ModTime int64
	MD5     string
}

type TreeOptions struct {
	FastList       bool
	FollowSymlinks bool
	IncludeHash    bool
	ProxyURL       string
}

// DirEntry represents one immediate child of a browsed directory.
type DirEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"isDir"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"modTime"`
	MD5     string `json:"md5,omitempty"`
}

// AboutInfo reports space quota / disk usage for a native storage target.
type AboutInfo struct {
	Supported bool   `json:"supported"`
	Total     *int64 `json:"total,omitempty"`
	Used      *int64 `json:"used,omitempty"`
	Free      *int64 `json:"free,omitempty"`
	Trashed   *int64 `json:"trashed,omitempty"`
	Other     *int64 `json:"other,omitempty"`
	Message   string `json:"message,omitempty"`
}

// ListTree recursively lists all files and directories under virtualPath.
// Missing target paths return (nil, false, nil) so callers can distinguish
// non-existent destination trees from operational errors.
func (c Config) ListTree(ctx context.Context, virtualPath string, opt TreeOptions) ([]TreeEntry, bool, error) {
	ep, err := c.Resolve(virtualPath)
	if err != nil {
		return nil, false, err
	}
	if ep.IsLocal {
		_, sub := parts(virtualPath)
		return listLocalTree(ctx, sub, opt)
	}
	name, _ := parts(virtualPath)
	s, err := c.SessionFor([]SessionSpec{{Name: name, Mode: diskReadOnly, Checksum: opt.IncludeHash}}, SessionOptions{ProxyURL: opt.ProxyURL})
	if err != nil {
		return nil, false, err
	}
	defer s.Close()

	args := []string{"lsjson", "-R", "--no-mimetype"}
	if opt.FastList {
		args = append(args, "--fast-list")
	}
	if opt.IncludeHash {
		args = append(args, "--hash")
	}
	args = append(args, "--", ep.Path)
	cmd, err := s.Cmd(ctx, args...)
	if err != nil {
		return nil, false, err
	}
	out, stderr, err := limitedOutputWithStderr(cmd, 64<<20)
	if err != nil {
		if isRcloneNotFound(stderr) {
			return nil, false, nil
		}
		return nil, false, classifyRcloneError(ep.Backend, stderr)
	}
	var rows []struct {
		Path    string            `json:"Path"`
		Name    string            `json:"Name"`
		Size    int64             `json:"Size"`
		ModTime string            `json:"ModTime"`
		IsDir   bool              `json:"IsDir"`
		Hashes  map[string]string `json:"Hashes"`
	}
	if json.Unmarshal(out, &rows) != nil {
		return nil, false, errors.New("无法解析远端目录树")
	}
	entries := make([]TreeEntry, 0, len(rows))
	for _, r := range rows {
		rel := strings.TrimPrefix(path.Clean("/"+r.Path), "/")
		if rel == "" || rel == "." || strings.HasPrefix(rel, "../") {
			continue
		}
		md5Sum := ""
		if r.Hashes != nil {
			md5Sum = strings.ToLower(strings.TrimSpace(r.Hashes["md5"]))
			if md5Sum == "" {
				md5Sum = strings.ToLower(strings.TrimSpace(r.Hashes["MD5"]))
			}
		}
		entries = append(entries, TreeEntry{
			RelPath: rel,
			Name:    path.Base(rel),
			IsDir:   r.IsDir,
			Size:    r.Size,
			ModTime: parseRcloneTime(r.ModTime).Unix(),
			MD5:     md5Sum,
		})
	}
	return entries, true, nil
}

func listLocalTree(ctx context.Context, sub string, opt TreeOptions) ([]TreeEntry, bool, error) {
	volume, relative, rootDev, hasRootDev, err := openDiskWithDev(sub)
	if err != nil {
		return nil, false, err
	}
	defer volume.Close()

	if relative != "." {
		st, statErr := volume.Lstat(relative)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				return nil, false, nil
			}
			return nil, false, statErr
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || !sameDevice(st, rootDev, hasRootDev) {
			return nil, false, errors.New("本地路径不可访问或不是目录")
		}
	}
	source, err := volume.OpenRoot(relative)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer source.Close()

	var entries []TreeEntry
	type devIno struct{ dev, ino uint64 }
	visitedDirs := map[devIno]bool{}
	if st, statErr := source.Stat("."); statErr == nil {
		if sys, ok := st.Sys().(*syscall.Stat_t); ok {
			visitedDirs[devIno{dev: uint64(sys.Dev), ino: sys.Ino}] = true
		}
	}

	var walk func(relDir string) error
	walk = func(relDir string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dirFile, openErr := source.Open(relDir)
		if openErr != nil {
			return openErr
		}
		items, readErr := dirFile.ReadDir(-1)
		_ = dirFile.Close()
		if readErr != nil {
			return readErr
		}
		for _, item := range items {
			childRel := item.Name()
			if relDir != "." {
				childRel = relDir + "/" + item.Name()
			}
			var st os.FileInfo
			var infoErr error
			if item.Type()&os.ModeSymlink != 0 {
				if !opt.FollowSymlinks {
					continue
				}
				st, infoErr = source.Stat(childRel)
				if infoErr != nil {
					continue
				}
			} else {
				st, infoErr = item.Info()
				if infoErr != nil {
					return infoErr
				}
			}
			if !sameDevice(st, rootDev, hasRootDev) {
				continue
			}
			if st.IsDir() {
				if opt.FollowSymlinks {
					if sys, ok := st.Sys().(*syscall.Stat_t); ok {
						k := devIno{dev: uint64(sys.Dev), ino: sys.Ino}
						if visitedDirs[k] {
							continue
						}
						visitedDirs[k] = true
					}
				}
				entries = append(entries, TreeEntry{
					RelPath: childRel,
					Name:    item.Name(),
					IsDir:   true,
					ModTime: st.ModTime().Unix(),
				})
				if err := walk(childRel); err != nil {
					return err
				}
				continue
			}
			if !st.Mode().IsRegular() {
				continue
			}
			md5Sum := ""
			if opt.IncludeHash {
				if sum, sumErr := md5FileInRoot(source, childRel); sumErr == nil {
					md5Sum = sum
				}
			}
			entries = append(entries, TreeEntry{
				RelPath: childRel,
				Name:    item.Name(),
				IsDir:   false,
				Size:    st.Size(),
				ModTime: st.ModTime().Unix(),
				MD5:     md5Sum,
			})
		}
		return nil
	}
	if err := walk("."); err != nil {
		return nil, false, err
	}
	return entries, true, nil
}

// ListDirEntries lists immediate files and subdirectories of virtualPath.
func (c Config) ListDirEntries(ctx context.Context, virtualPath string, opts SessionOptions) ([]DirEntry, bool, error) {
	v := strings.TrimSuffix(strings.TrimSpace(virtualPath), "/")
	if v == "" || v == "/" {
		dirs, err := c.ListWithOptions(ctx, "/", opts)
		if err != nil {
			return nil, false, err
		}
		out := make([]DirEntry, 0, len(dirs))
		for _, d := range dirs {
			out = append(out, DirEntry{Name: d["name"], Path: d["path"], IsDir: true})
		}
		return out, true, nil
	}
	n, sub := parts(v)
	if c.isLocalName(n) {
		if sub == "" {
			roots := authorizedRoots()
			out := make([]DirEntry, 0, len(roots))
			for _, r := range roots {
				out = append(out, DirEntry{Name: r.Path, Path: path.Join(v, r.ID), IsDir: true})
			}
			slices.SortFunc(out, func(a, b DirEntry) int { return strings.Compare(a.Name, b.Name) })
			return out, true, nil
		}
		if _, err := c.Resolve(v); err != nil {
			return nil, false, err
		}
		root, rel, rootDev, hasRootDev, err := openDiskWithDev(sub)
		if err != nil {
			return nil, false, err
		}
		defer root.Close()
		dirFile, err := root.Open(rel)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, false, nil
			}
			return nil, false, errors.New("无法读取本地授权目录")
		}
		entries, readErr := dirFile.ReadDir(-1)
		_ = dirFile.Close()
		if readErr != nil {
			return nil, false, errors.New("无法读取本地授权目录")
		}
		out := make([]DirEntry, 0, len(entries))
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			st, infoErr := entry.Info()
			if infoErr != nil || st.Mode()&os.ModeSymlink != 0 || !sameDevice(st, rootDev, hasRootDev) {
				continue
			}
			if !st.IsDir() && !st.Mode().IsRegular() {
				continue
			}
			out = append(out, DirEntry{
				Name:    entry.Name(),
				Path:    path.Join(v, entry.Name()),
				IsDir:   st.IsDir(),
				Size:    st.Size(),
				ModTime: st.ModTime().Unix(),
			})
		}
		sortDirEntries(out)
		return out, true, nil
	}

	if c[n] == nil || !cleanRelative(sub) {
		return nil, false, errors.New("远端路径无效")
	}
	s, err := c.SessionFor([]SessionSpec{{Name: n, Mode: diskReadOnly}}, opts)
	if err != nil {
		return nil, false, err
	}
	defer s.Close()

	cmd, err := s.Cmd(ctx, "lsjson", "--no-mimetype", "--", n+":"+sub)
	if err != nil {
		return nil, false, err
	}
	outBytes, stderr, err := limitedOutputWithStderr(cmd, 32<<20)
	if err != nil {
		if isRcloneNotFound(stderr) {
			return nil, false, nil
		}
		return nil, false, classifyRcloneError(c[n]["type"], stderr)
	}
	var rows []struct {
		Name    string            `json:"Name"`
		Size    int64             `json:"Size"`
		ModTime string            `json:"ModTime"`
		IsDir   bool              `json:"IsDir"`
		Hashes  map[string]string `json:"Hashes"`
	}
	if json.Unmarshal(outBytes, &rows) != nil {
		return nil, false, errors.New("无法解析远端目录列表")
	}
	out := make([]DirEntry, 0, len(rows))
	for _, r := range rows {
		if r.Name == "" || r.Name == "." || r.Name == ".." || strings.ContainsAny(r.Name, "/\\\n\r") {
			continue
		}
		md5Sum := ""
		if r.Hashes != nil {
			md5Sum = strings.ToLower(strings.TrimSpace(r.Hashes["md5"]))
		}
		out = append(out, DirEntry{
			Name:    r.Name,
			Path:    path.Join(v, r.Name),
			IsDir:   r.IsDir,
			Size:    r.Size,
			ModTime: parseRcloneTime(r.ModTime).Unix(),
			MD5:     md5Sum,
		})
	}
	sortDirEntries(out)
	return out, true, nil
}

func sortDirEntries(out []DirEntry) {
	slices.SortFunc(out, func(a, b DirEntry) int {
		if a.IsDir != b.IsDir {
			if a.IsDir {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
}

// DeletePath removes a single file or directory subtree at virtualPath.
func (c Config) DeletePath(ctx context.Context, virtualPath string, isDir bool, opts SessionOptions) error {
	var (
		ep  Endpoint
		err error
	)
	if isDir {
		ep, err = c.Resolve(virtualPath)
	} else {
		ep, err = c.ResolveEntry(virtualPath)
	}
	if err != nil {
		return err
	}
	if ep.ReadOnly {
		return errors.New("只读存储不支持删除")
	}
	if ep.IsLocal {
		_, sub := parts(virtualPath)
		id, rel, _ := strings.Cut(sub, "/")
		if rel == "" || rel == "." {
			return errors.New("拒绝删除本地授权根目录")
		}
		root, _, rootDev, hasRootDev, err := openDiskWithDev(id)
		if err != nil {
			return err
		}
		defer root.Close()
		err = removeInRootSafe(root, rel, ".", rootDev, hasRootDev)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	name, _ := parts(virtualPath)
	s, err := c.SessionFor([]SessionSpec{{Name: name, Mode: diskFullWrite}}, opts)
	if err != nil {
		return err
	}
	defer s.Close()
	subCmd := "deletefile"
	if isDir {
		subCmd = "purge"
	}
	cmd, err := s.Cmd(ctx, subCmd, "--", ep.Path)
	if err != nil {
		return err
	}
	_, stderr, err := limitedOutputWithStderr(cmd, 64<<10)
	if err != nil {
		if isRcloneNotFound(stderr) {
			return nil
		}
		return classifyRcloneError(ep.Backend, stderr)
	}
	return nil
}

// RemoveEmptyDir removes a directory only if it is empty.
func (c Config) RemoveEmptyDir(ctx context.Context, virtualPath string, opts SessionOptions) error {
	ep, err := c.Resolve(virtualPath)
	if err != nil {
		return err
	}
	if ep.ReadOnly {
		return errors.New("只读存储不支持删除目录")
	}
	if ep.IsLocal {
		_, sub := parts(virtualPath)
		id, rel, _ := strings.Cut(sub, "/")
		if rel == "" || rel == "." {
			return errors.New("拒绝删除本地授权根目录")
		}
		root, _, rootDev, hasRootDev, err := openDiskWithDev(id)
		if err != nil {
			return err
		}
		defer root.Close()
		st, statErr := root.Lstat(rel)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				return nil
			}
			return statErr
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || !sameDevice(st, rootDev, hasRootDev) {
			return os.ErrPermission
		}
		err = root.Remove(rel)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	name, _ := parts(virtualPath)
	s, err := c.SessionFor([]SessionSpec{{Name: name, Mode: diskFullWrite}}, opts)
	if err != nil {
		return err
	}
	defer s.Close()
	cmd, err := s.Cmd(ctx, "rmdir", "--", ep.Path)
	if err != nil {
		return err
	}
	_, stderr, err := limitedOutputWithStderr(cmd, 64<<10)
	if err != nil {
		if isRcloneNotFound(stderr) {
			return nil
		}
		return classifyRcloneError(ep.Backend, stderr)
	}
	return nil
}

// Mkdir creates virtualPath (including any missing parents).
func (c Config) Mkdir(ctx context.Context, virtualPath string, opts SessionOptions) error {
	ep, err := c.Resolve(virtualPath)
	if err != nil {
		return err
	}
	if ep.ReadOnly {
		return errors.New("只读存储不支持创建目录")
	}
	if ep.IsLocal {
		_, sub := parts(virtualPath)
		root, rel, err := openDisk(sub)
		if err != nil {
			return err
		}
		defer root.Close()
		return mkdirAllPerm(root, rel, backupDirPerm)
	}
	name, _ := parts(virtualPath)
	s, err := c.SessionFor([]SessionSpec{{Name: name, Mode: diskAppendWrite}}, opts)
	if err != nil {
		return err
	}
	defer s.Close()
	cmd, err := s.Cmd(ctx, "mkdir", "--", ep.Path)
	if err != nil {
		return err
	}
	_, stderr, err := limitedOutputWithStderr(cmd, 64<<10)
	if err != nil {
		return classifyRcloneError(ep.Backend, stderr)
	}
	return nil
}

// MovePath moves or renames a file or directory from srcVirtual to dstVirtual.
func (c Config) MovePath(ctx context.Context, srcVirtual, dstVirtual string, isDir bool, opts SessionOptions) error {
	var (
		srcEp, dstEp Endpoint
		err          error
	)
	if isDir {
		srcEp, err = c.Resolve(srcVirtual)
	} else {
		srcEp, err = c.ResolveEntry(srcVirtual)
	}
	if err != nil {
		return err
	}
	if isDir {
		dstEp, err = c.Resolve(dstVirtual)
	} else {
		dstEp, err = c.ResolveEntry(dstVirtual)
	}
	if err != nil {
		return err
	}
	if srcEp.ReadOnly || dstEp.ReadOnly {
		return errors.New("只读存储不支持移动操作")
	}
	// Fast path: local->local rename within the same authorized root.
	if srcEp.IsLocal && dstEp.IsLocal {
		_, srcSub := parts(srcVirtual)
		_, dstSub := parts(dstVirtual)
		srcID, srcRel, _ := strings.Cut(srcSub, "/")
		dstID, dstRel, _ := strings.Cut(dstSub, "/")
		if srcID == dstID && srcRel != "" && dstRel != "" {
			root, _, rootDev, hasRootDev, err := openDiskWithDev(srcID)
			if err != nil {
				return err
			}
			defer root.Close()
			st, err := root.Lstat(srcRel)
			if err != nil {
				return err
			}
			if st.Mode()&os.ModeSymlink != 0 || !sameDevice(st, rootDev, hasRootDev) {
				return os.ErrPermission
			}
			if err := mkdirAllPerm(root, path.Dir(dstRel), backupDirPerm); err != nil {
				return err
			}
			return root.Rename(srcRel, dstRel)
		}
	}

	srcName, srcSub := parts(srcVirtual)
	dstName, dstSub := parts(dstVirtual)
	var specs []SessionSpec
	if !srcEp.IsLocal {
		specs = append(specs, SessionSpec{Name: srcName, Mode: diskFullWrite})
	}
	if !dstEp.IsLocal {
		specs = append(specs, SessionSpec{Name: dstName, Mode: diskFullWrite})
	}
	s, err := c.SessionFor(specs, opts)
	if err != nil {
		return err
	}
	defer s.Close()

	srcArg := srcEp.Path
	if srcEp.IsLocal {
		if isDir {
			srcArg, err = s.diskWithMode(srcSub, diskFullWrite, false)
		} else {
			parentSub := path.Dir(srcSub)
			base := path.Base(srcSub)
			var parentArg string
			parentArg, err = s.diskWithMode(parentSub, diskFullWrite, false)
			if err == nil {
				srcArg = joinRcloneRemotePath(parentArg, base)
			}
		}
		if err != nil {
			return err
		}
	}
	dstArg := dstEp.Path
	if dstEp.IsLocal {
		if isDir {
			dstArg, err = s.diskWithMode(dstSub, diskFullWrite, false)
		} else {
			parentSub := path.Dir(dstSub)
			base := path.Base(dstSub)
			var parentArg string
			parentArg, err = s.diskWithMode(parentSub, diskFullWrite, false)
			if err == nil {
				dstArg = joinRcloneRemotePath(parentArg, base)
			}
		}
		if err != nil {
			return err
		}
	}
	subCmd := "moveto"
	if isDir {
		subCmd = "move"
	}
	cmd, err := s.Cmd(ctx, subCmd, "--", srcArg, dstArg)
	if err != nil {
		return err
	}
	_, stderr, err := limitedOutputWithStderr(cmd, 64<<10)
	if err != nil {
		return classifyRcloneError(dstEp.Backend, stderr)
	}
	return nil
}

// About returns disk or remote storage space usage information.
func (c Config) About(ctx context.Context, targetName string, opts SessionOptions) (*AboutInfo, error) {
	targetName = strings.TrimSpace(targetName)
	if targetName == "" {
		return nil, errors.New("请指定要查询空间的存储名称")
	}
	if targetName == Local || (c[targetName] != nil && c[targetName]["type"] == "local") {
		roots := authorizedRoots()
		if len(roots) == 0 {
			return &AboutInfo{Supported: false, Message: "当前未检测到本地授权目录"}, nil
		}
		// Deduplicate filesystems by fsid so multiple folders on the same disk aren't double-counted.
		type fsKey struct{ a, b int32 }
		seen := map[fsKey]bool{}
		var total, used, free int64
		for _, r := range roots {
			f0, f1 := filesystemID(r.Path)
			k := fsKey{f0, f1}
			if seen[k] {
				continue
			}
			seen[k] = true
			var st syscall.Statfs_t
			if err := syscall.Statfs(r.Path, &st); err != nil {
				continue
			}
			bsize := int64(st.Bsize)
			if bsize <= 0 {
				continue
			}
			fsTotal := int64(st.Blocks) * bsize
			fsFree := int64(st.Bavail) * bsize
			fsUsed := fsTotal - int64(st.Bfree)*bsize
			if fsUsed < 0 {
				fsUsed = 0
			}
			total += fsTotal
			used += fsUsed
			free += fsFree
		}
		return &AboutInfo{
			Supported: true,
			Total:     &total,
			Used:      &used,
			Free:      &free,
		}, nil
	}

	canonical, ok := c.FindKey(targetName)
	if !ok {
		return nil, fmt.Errorf("存储 %q 不存在", targetName)
	}
	s, err := c.SessionFor([]SessionSpec{{Name: canonical, Mode: diskReadOnly}}, opts)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	cmd, err := s.Cmd(ctx, "about", "--json", "--", canonical+":")
	if err != nil {
		return nil, err
	}
	out, stderr, err := limitedOutputWithStderr(cmd, 64<<10)
	if err != nil {
		low := strings.ToLower(stderr)
		if strings.Contains(low, "doesn't support about") || strings.Contains(low, "not supported") || strings.Contains(low, "not implemented") {
			return &AboutInfo{Supported: false, Message: "该存储后端不支持空间容量查询"}, nil
		}
		return nil, classifyRcloneError(c[canonical]["type"], stderr)
	}
	var parsed struct {
		Total   *int64 `json:"total"`
		Used    *int64 `json:"used"`
		Free    *int64 `json:"free"`
		Trashed *int64 `json:"trashed"`
		Other   *int64 `json:"other"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, errors.New("无法解析远端空间用量信息")
	}
	return &AboutInfo{
		Supported: true,
		Total:     parsed.Total,
		Used:      parsed.Used,
		Free:      parsed.Free,
		Trashed:   parsed.Trashed,
		Other:     parsed.Other,
	}, nil
}

// Restore copies a single file or directory from a remote (including decrypted
// crypt/chunker/compress/union) back into an fnOS-authorized local directory.
// dstLocalDir accepts either a virtual path ("/本地/...") or an absolute local
// path inside an authorized fnOS directory.
func (c Config) Restore(ctx context.Context, srcVirtual, dstLocalDir string, isDir, overwrite bool, opts SessionOptions) error {
	var (
		srcEp Endpoint
		err   error
	)
	if isDir {
		srcEp, err = c.Resolve(srcVirtual)
	} else {
		srcEp, err = c.ResolveEntry(srcVirtual)
	}
	if err != nil {
		return err
	}
	dstLocalDirVirtual := strings.TrimSpace(dstLocalDir)
	if !strings.HasPrefix(dstLocalDirVirtual, "/"+Local+"/") && dstLocalDirVirtual != "/"+Local {
		if converted, ok := localAbsToVirtual(dstLocalDirVirtual); ok {
			dstLocalDirVirtual = converted
		}
	}
	dstEp, err := c.Resolve(dstLocalDirVirtual)
	if err != nil {
		return err
	}
	if !dstEp.IsLocal {
		return errors.New("恢复目标必须是飞牛本地授权目录")
	}
	if EndpointsConflict(srcEp, dstEp) {
		return errors.New("恢复源与目标本地目录重叠")
	}

	srcName, srcSub := parts(srcVirtual)
	_, dstSub := parts(dstLocalDirVirtual)
	var specs []SessionSpec
	if !srcEp.IsLocal {
		specs = append(specs, SessionSpec{Name: srcName, Mode: diskReadOnly})
	}
	s, err := c.SessionFor(specs, opts)
	if err != nil {
		return err
	}
	defer s.Close()

	srcArg := srcEp.Path
	if srcEp.IsLocal {
		if isDir {
			srcArg, err = s.diskWithMode(srcSub, diskReadOnly, false)
		} else {
			parentSub := path.Dir(srcSub)
			base := path.Base(srcSub)
			var parentArg string
			parentArg, err = s.diskWithMode(parentSub, diskReadOnly, false)
			if err == nil {
				srcArg = joinRcloneRemotePath(parentArg, base)
			}
		}
		if err != nil {
			return err
		}
	}
	dstArg, err := s.diskWithMode(dstSub, diskAppendWrite, false)
	if err != nil {
		return err
	}

	baseName := path.Base(strings.TrimSuffix(srcVirtual, "/"))
	if baseName == "" || baseName == "." || baseName == "/" {
		return errors.New("无法确定要恢复的文件或目录名称")
	}

	var args []string
	if isDir {
		targetDir := joinRcloneRemotePath(dstArg, baseName)
		args = []string{"copy", "--transfers", "4", "--checkers", "4"}
		if !overwrite {
			args = append(args, "--ignore-existing")
		}
		args = append(args, "--", srcArg, targetDir)
	} else {
		targetFile := joinRcloneRemotePath(dstArg, baseName)
		args = []string{"copyto"}
		if !overwrite {
			args = append(args, "--ignore-existing")
		}
		args = append(args, "--", srcArg, targetFile)
	}

	cmd, err := s.Cmd(ctx, args...)
	if err != nil {
		return err
	}
	_, stderr, err := limitedOutputWithStderr(cmd, 64<<10)
	if err != nil {
		return classifyRcloneError(srcEp.Backend, stderr)
	}
	return nil
}

// CheckOptions configures an rclone consistency verification check.
type CheckOptions struct {
	Download      bool
	Compare       int // 0/2: hash/default, 1: size-only
	OneWay        bool
	Checkers      int
	Retries       int
	FilterLines   []string // rclone --filter-from rules
	BwLimit       string
	TPSLimit      float64
	TPSLimitBurst int
	ModifyWindow  string
	FastList      bool
	ProxyURL      string
	MaxItems      int // cap preview items per category (default 500)
}

type CheckResult struct {
	MatchedCount      int      `json:"matchedCount"`
	DifferCount       int      `json:"differCount"`
	MissingOnSrcCount int      `json:"missingOnSrcCount"`
	MissingOnDstCount int      `json:"missingOnDstCount"`
	ErrorCount        int      `json:"errorCount"`
	Differ            []string `json:"differ"`
	MissingOnSrc      []string `json:"missingOnSrc"`
	MissingOnDst      []string `json:"missingOnDst"`
	Errors            []string `json:"errors"`
	Truncated         bool     `json:"truncated"`
}

// Check runs `rclone check` between src and dst and returns structured verification counts and previews.
func (c Config) Check(ctx context.Context, src, dst string, opt CheckOptions, emit func(*Stats)) (*CheckResult, error) {
	srcEp, dstEp, err := c.Pair(src, dst)
	if err != nil {
		return nil, err
	}
	useChecksum := !opt.Download && opt.Compare != 1 && endpointSupportsNativeMD5(srcEp) && endpointSupportsNativeMD5(dstEp)

	srcName, srcSub := parts(src)
	dstName, dstSub := parts(dst)
	var specs []SessionSpec
	if !srcEp.IsLocal {
		specs = append(specs, SessionSpec{Name: srcName, Mode: diskReadOnly, Checksum: useChecksum})
	}
	if !dstEp.IsLocal {
		specs = append(specs, SessionSpec{Name: dstName, Mode: diskReadOnly, Checksum: useChecksum})
	}
	s, err := c.SessionFor(specs, SessionOptions{ProxyURL: opt.ProxyURL})
	if err != nil {
		return nil, err
	}
	defer s.Close()

	srcArg := srcEp.Path
	if srcEp.IsLocal {
		srcArg, err = s.diskWithMode(srcSub, diskReadOnly, useChecksum)
		if err != nil {
			return nil, err
		}
	}
	dstArg := dstEp.Path
	if dstEp.IsLocal {
		dstArg, err = s.diskWithMode(dstSub, diskReadOnly, useChecksum)
		if err != nil {
			return nil, err
		}
	}

	matchFile := filepath.Join(s.dir, "match")
	differFile := filepath.Join(s.dir, "differ")
	missSrcFile := filepath.Join(s.dir, "missing-src")
	missDstFile := filepath.Join(s.dir, "missing-dst")
	errFile := filepath.Join(s.dir, "errors")

	args := []string{
		"check",
		"--use-json-log", "--log-level", "INFO", "--stats", "1s",
		"--checkers", strconv.Itoa(max(1, opt.Checkers)),
		"--retries", strconv.Itoa(max(1, opt.Retries+1)),
		"--match", matchFile,
		"--differ", differFile,
		"--missing-on-src", missSrcFile,
		"--missing-on-dst", missDstFile,
		"--error", errFile,
	}
	if opt.Download {
		args = append(args, "--download")
	} else if opt.Compare == 1 {
		args = append(args, "--size-only")
	}
	if opt.OneWay {
		args = append(args, "--one-way")
	}
	if len(opt.FilterLines) > 0 {
		filterFile := filepath.Join(s.dir, "filters")
		if err := os.WriteFile(filterFile, []byte(strings.Join(opt.FilterLines, "\n")+"\n"), 0600); err != nil {
			return nil, err
		}
		args = append(args, "--filter-from", filterFile)
	}
	args, err = appendTuningFlags(args, CopyOptions{
		BwLimit:       opt.BwLimit,
		TPSLimit:      opt.TPSLimit,
		TPSLimitBurst: opt.TPSLimitBurst,
		ModifyWindow:  opt.ModifyWindow,
		FastList:      opt.FastList,
	})
	if err != nil {
		return nil, err
	}
	args = append(args, "--", srcArg, dstArg)

	cmd, err := s.Cmd(ctx, args...)
	if err != nil {
		return nil, err
	}
	logs, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	errBuf := &tailBuffer{limit: maxStderrCapture}
	scan := bufio.NewScanner(logs)
	scan.Buffer(make([]byte, 4096), 4<<20)
	for scan.Scan() {
		raw := scan.Bytes()
		var m struct {
			Msg, Level string
			Stats      *Stats
		}
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		if m.Stats != nil && emit != nil {
			emit(m.Stats)
			continue
		}
		if m.Level == "error" || m.Level == "critical" {
			if msg := strings.TrimSpace(m.Msg); msg != "" {
				_, _ = errBuf.Write([]byte(msg + "\n"))
			}
		}
	}
	if scan.Err() != nil {
		_ = cmd.Process.Kill()
	}
	runErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if scan.Err() != nil {
		return nil, scan.Err()
	}
	// Exit code 1 means differences were found (normal check outcome).
	if runErr != nil && (cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 1) {
		detail := sanitizeRcloneError(errBuf.String())
		if detail != "" {
			log.Printf("native rclone check failed (%v): %s", runErr, detail)
			return nil, fmt.Errorf("一致性校验失败：%s", detail)
		}
		return nil, fmt.Errorf("一致性校验失败：%w", runErr)
	}

	limit := opt.MaxItems
	if limit <= 0 {
		limit = 500
	}
	res := &CheckResult{
		Differ:       []string{},
		MissingOnSrc: []string{},
		MissingOnDst: []string{},
		Errors:       []string{},
	}
	res.MatchedCount, _, _ = readLinesCapped(matchFile, 0)
	var trunc bool
	res.DifferCount, res.Differ, trunc = readLinesCapped(differFile, limit)
	res.Truncated = res.Truncated || trunc
	res.MissingOnSrcCount, res.MissingOnSrc, trunc = readLinesCapped(missSrcFile, limit)
	res.Truncated = res.Truncated || trunc
	res.MissingOnDstCount, res.MissingOnDst, trunc = readLinesCapped(missDstFile, limit)
	res.Truncated = res.Truncated || trunc
	res.ErrorCount, res.Errors, trunc = readLinesCapped(errFile, limit)
	res.Truncated = res.Truncated || trunc
	return res, nil
}

func readLinesCapped(filePath string, limit int) (total int, items []string, truncated bool) {
	items = []string{}
	f, err := os.Open(filePath)
	if err != nil {
		return 0, items, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 4096), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		total++
		if limit > 0 {
			if len(items) < limit {
				items = append(items, line)
			} else {
				truncated = true
			}
		}
	}
	return total, items, truncated
}

// BisyncOptions configures a bidirectional rclone bisync run.
type BisyncOptions struct {
	WorkDir            string
	ForceResync        bool
	DryRun             bool
	Transfers          int
	Checkers           int
	Retries            int
	Compare            int // 0: size,modtime; 1: size; 2: size,modtime,checksum
	ConflictPolicy     int // 0: newer (delete loser), 1: path1 (delete loser), 2: none (keep both with suffix)
	FilterLines        []string
	BwLimit            string
	TPSLimit           float64
	TPSLimitBurst      int
	ModifyWindow       string
	FastList           bool
	MultiThreadStreams int
	CreateEmptySrcDirs bool
	FollowSymlinks     bool
	ProxyURL           string
}

type BisyncResult struct {
	Resynced bool
}

// Bisync executes rclone bisync between path1 and path2 using a dedicated state workdir.
func (c Config) Bisync(ctx context.Context, path1, path2 string, opt BisyncOptions, emit func(Event)) (*BisyncResult, error) {
	if emit == nil {
		emit = func(Event) {}
	}
	ep1, ep2, err := c.Pair(path1, path2)
	if err != nil {
		return nil, err
	}
	if ep1.ReadOnly || ep2.ReadOnly {
		return nil, errors.New("双向同步的两端都必须支持读写，不能使用只读 HTTP 存储")
	}
	if strings.TrimSpace(opt.WorkDir) == "" {
		return nil, errors.New("缺少双向同步状态目录")
	}
	if err := os.MkdirAll(opt.WorkDir, 0700); err != nil {
		return nil, err
	}

	useChecksum := opt.Compare == 2 && endpointSupportsNativeMD5(ep1) && endpointSupportsNativeMD5(ep2)
	mode := diskFullWrite
	if opt.DryRun {
		mode = diskReadOnly
	}
	name1, sub1 := parts(path1)
	name2, sub2 := parts(path2)
	var specs []SessionSpec
	if !ep1.IsLocal {
		specs = append(specs, SessionSpec{Name: name1, Mode: mode, Checksum: useChecksum})
	}
	if !ep2.IsLocal {
		specs = append(specs, SessionSpec{Name: name2, Mode: mode, Checksum: useChecksum})
	}
	s, err := c.SessionFor(specs, SessionOptions{ProxyURL: opt.ProxyURL})
	if err != nil {
		return nil, err
	}
	defer s.Close()

	arg1 := ep1.Path
	if ep1.IsLocal {
		arg1, err = s.diskWithOptions(sub1, mode, useChecksum, opt.FollowSymlinks)
		if err != nil {
			return nil, err
		}
	}
	arg2 := ep2.Path
	if ep2.IsLocal {
		arg2, err = s.diskWithOptions(sub2, mode, useChecksum, opt.FollowSymlinks)
		if err != nil {
			return nil, err
		}
	}

	needResync := opt.ForceResync || !bisyncHasListingState(opt.WorkDir)

	args := []string{
		"bisync",
		"--workdir", opt.WorkDir,
		"--use-json-log", "--log-level", "INFO", "--stats", "1s",
		"--transfers", strconv.Itoa(max(1, opt.Transfers)),
		"--checkers", strconv.Itoa(max(1, opt.Checkers)),
		"--retries", strconv.Itoa(max(1, opt.Retries+1)),
		"--recover", "--resilient",
	}
	switch opt.Compare {
	case 1:
		args = append(args, "--compare", "size")
	case 2:
		if useChecksum {
			args = append(args, "--compare", "size,modtime,checksum")
		} else {
			args = append(args, "--compare", "size,modtime")
		}
	default:
		args = append(args, "--compare", "size,modtime")
	}

	switch opt.ConflictPolicy {
	case 1:
		args = append(args, "--conflict-resolve", "path1", "--conflict-loser", "delete")
	case 2:
		args = append(args, "--conflict-resolve", "none", "--conflict-loser", "num", "--conflict-suffix", "conflict")
	default:
		args = append(args, "--conflict-resolve", "newer", "--conflict-loser", "delete")
	}

	if needResync {
		args = append(args, "--resync", "--resync-mode", "newer")
	}
	if opt.DryRun {
		args = append(args, "--dry-run")
	}
	if opt.CreateEmptySrcDirs {
		args = append(args, "--create-empty-src-dirs")
	}
	if len(opt.FilterLines) > 0 {
		filterFile := filepath.Join(s.dir, "filters")
		if err := os.WriteFile(filterFile, []byte(strings.Join(opt.FilterLines, "\n")+"\n"), 0600); err != nil {
			return nil, err
		}
		args = append(args, "--filter-from", filterFile)
	}
	args, err = appendTuningFlags(args, CopyOptions{
		BwLimit:            opt.BwLimit,
		TPSLimit:           opt.TPSLimit,
		TPSLimitBurst:      opt.TPSLimitBurst,
		ModifyWindow:       opt.ModifyWindow,
		FastList:           opt.FastList,
		MultiThreadStreams: opt.MultiThreadStreams,
	})
	if err != nil {
		return nil, err
	}
	args = append(args, "--", arg1, arg2)

	cmd, err := s.Cmd(ctx, args...)
	if err != nil {
		return nil, err
	}
	logs, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	scan := bufio.NewScanner(logs)
	scan.Buffer(make([]byte, 4096), 4<<20)
	var lastErrMsg string
	failed := map[string]string{}
	for scan.Scan() {
		var m struct {
			Msg, Object, Level string
			Size               int64
			Stats              *Stats
		}
		if json.Unmarshal(scan.Bytes(), &m) != nil {
			continue
		}
		if m.Level == "error" || m.Level == "critical" {
			if msg := strings.TrimSpace(m.Msg); msg != "" {
				lastErrMsg = msg
			}
			if m.Object != "" && cleanRelative(m.Object) {
				failed[path.Clean(m.Object)] = strings.TrimSpace(m.Msg)
			}
		}
		if m.Stats != nil {
			if msg := strings.TrimSpace(m.Stats.LastError); msg != "" {
				lastErrMsg = msg
			}
			emit(Event{Stats: m.Stats})
			continue
		}
		if m.Object != "" && cleanRelative(m.Object) {
			obj := path.Clean(m.Object)
			switch {
			case strings.HasPrefix(m.Msg, "Copied (") || (opt.DryRun && strings.Contains(m.Msg, "Skipped copy")):
				delete(failed, obj)
				emit(Event{
					File:    obj,
					Size:    m.Size,
					Updated: strings.Contains(m.Msg, "replaced"),
					Reason:  "双向同步复制",
				})
			case strings.HasPrefix(m.Msg, "Deleted") || (opt.DryRun && strings.Contains(m.Msg, "Skipped delete")):
				delete(failed, obj)
				emit(Event{
					File:   obj,
					Size:   m.Size,
					Reason: "delete",
				})
			case strings.HasPrefix(m.Msg, "Moved") || (opt.DryRun && strings.Contains(m.Msg, "Skipped move")):
				delete(failed, obj)
				emit(Event{
					File:   obj,
					Size:   m.Size,
					Reason: "move",
				})
			}
		}
	}
	if scan.Err() != nil {
		_ = cmd.Process.Kill()
	}
	runErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if scan.Err() != nil {
		return nil, scan.Err()
	}
	if runErr != nil {
		if lastErrMsg != "" {
			log.Printf("native rclone bisync failed (%v): %s", runErr, sanitizeRcloneError(lastErrMsg))
		}
		for file, msg := range failed {
			emit(Event{File: file, Failed: true, Error: sanitizeRcloneError(msg)})
		}
		if detail := sanitizeRcloneError(lastErrMsg); detail != "" {
			return nil, fmt.Errorf("双向同步失败：%s", detail)
		}
		return nil, fmt.Errorf("双向同步失败（%v）", runErr)
	}
	return &BisyncResult{Resynced: needResync}, nil
}

func bisyncHasListingState(workDir string) bool {
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".lst") {
			return true
		}
	}
	return false
}

func localAbsToVirtual(absPath string) (string, bool) {
	if !filepath.IsAbs(absPath) || strings.Contains(absPath, "\x00") {
		return "", false
	}
	for _, seg := range strings.Split(absPath, "/") {
		if seg == ".." {
			return "", false
		}
	}
	clean := filepath.Clean(absPath)
	roots := authorizedRoots()
	var best localRoot
	matched := false
	for _, r := range roots {
		if clean == r.Path || strings.HasPrefix(clean, r.Path+"/") {
			if !matched || len(r.Path) > len(best.Path) {
				best = r
				matched = true
			}
		}
	}
	if !matched {
		return "", false
	}
	rel := strings.TrimPrefix(clean, best.Path)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		return "/" + Local + "/" + best.ID, true
	}
	return "/" + Local + "/" + best.ID + "/" + filepath.ToSlash(rel), true
}

func isRcloneNotFound(stderr string) bool {
	low := strings.ToLower(stderr)
	return strings.Contains(low, "directory not found") ||
		strings.Contains(low, "object not found") ||
		strings.Contains(low, "no such file or directory") ||
		strings.Contains(low, "404 not found")
}
