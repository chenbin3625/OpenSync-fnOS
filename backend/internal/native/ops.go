package native

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// TreeEntry represents one file or directory in a recursive native tree scan.
type TreeEntry struct {
	RelPath  string
	Name     string
	IsDir    bool
	Size     int64
	ModTime  int64
	MD5      string
	Inode    uint64
	ObjectID string
}

type TreeOptions struct {
	FastList       bool
	FollowSymlinks bool
	IncludeHash    bool
	ProxyURL       string
	Controller     *RCController
}

// DirEntry represents one immediate child of a browsed directory.
type DirEntry struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	IsDir    bool   `json:"isDir"`
	Size     int64  `json:"size"`
	ModTime  int64  `json:"modTime"`
	MD5      string `json:"md5,omitempty"`
	Inode    uint64 `json:"-"`
	ObjectID string `json:"-"`
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
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	errBuf := &tailBuffer{limit: maxStderrCapture}
	cmd.Stderr = errBuf
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	if opt.Controller != nil && cmd.Process != nil {
		detachTree := opt.Controller.Attach(cmd.Process.Pid, "", "", "")
		defer detachTree()
		stopTreeWake := context.AfterFunc(ctx, func() { opt.Controller.ResumeAllProcesses() })
		defer stopTreeWake()
	}

	dec := json.NewDecoder(bufio.NewReaderSize(stdout, 64<<10))
	var entries []TreeEntry
	var parseErr error
	killed := false
	if tok, tokErr := dec.Token(); tokErr != nil {
		parseErr = tokErr
	} else if delim, ok := tok.(json.Delim); !ok || delim != '[' {
		parseErr = errors.New("无法解析远端目录树")
	} else {
		for dec.More() {
			if ctx.Err() != nil {
				parseErr = ctx.Err()
				break
			}
			if err := opt.Controller.WaitIfPaused(ctx); err != nil {
				parseErr = err
				break
			}
			var r struct {
				Path    string            `json:"Path"`
				Name    string            `json:"Name"`
				Size    int64             `json:"Size"`
				ModTime string            `json:"ModTime"`
				IsDir   bool              `json:"IsDir"`
				ID      string            `json:"ID"`
				Hashes  map[string]string `json:"Hashes"`
			}
			if decErr := dec.Decode(&r); decErr != nil {
				parseErr = errors.New("无法解析远端目录树")
				break
			}
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
				RelPath:  rel,
				Name:     path.Base(rel),
				IsDir:    r.IsDir,
				Size:     r.Size,
				ModTime:  parseRcloneTime(r.ModTime).Unix(),
				MD5:      md5Sum,
				ObjectID: strings.TrimSpace(r.ID),
			})
		}
		if parseErr == nil {
			if tok, tokErr := dec.Token(); tokErr != nil {
				parseErr = errors.New("无法解析远端目录树")
			} else if delim, ok := tok.(json.Delim); !ok || delim != ']' {
				parseErr = errors.New("无法解析远端目录树")
			}
		}
	}
	if parseErr != nil && !errors.Is(parseErr, io.EOF) {
		if cmd.Process != nil {
			_ = resumeProcess(cmd.Process.Pid)
			_ = cmd.Process.Kill()
			killed = true
		}
		_ = stdout.Close()
	}
	waitErr := cmd.Wait()
	stderr := errBuf.String()
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	if waitErr != nil && !killed {
		if isRcloneNotFound(stderr) {
			return nil, false, nil
		}
		return nil, false, classifyRcloneError(ep.Backend, stderr)
	}
	if parseErr != nil {
		if isRcloneNotFound(stderr) {
			return nil, false, nil
		}
		if strings.TrimSpace(stderr) != "" {
			return nil, false, classifyRcloneError(ep.Backend, stderr)
		}
		return nil, false, errors.New("无法解析远端目录树")
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
		if err := opt.Controller.WaitIfPaused(ctx); err != nil {
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
			if isInternalPartialEntry(item.Name()) {
				continue
			}
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
			var ino uint64
			if sys, ok := st.Sys().(*syscall.Stat_t); ok {
				ino = sys.Ino
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
					Inode:   ino,
				})
				if err := walk(childRel); err != nil {
					return err
				}
				continue
			}
			if !st.Mode().IsRegular() {
				continue
			}
			entries = append(entries, TreeEntry{
				RelPath: childRel,
				Name:    item.Name(),
				IsDir:   false,
				Size:    st.Size(),
				ModTime: st.ModTime().Unix(),
				Inode:   ino,
			})
		}
		return nil
	}
	if err := walk("."); err != nil {
		return nil, false, err
	}
	if opt.IncludeHash {
		hashIndices := make([]int, 0, len(entries))
		for i := range entries {
			if !entries[i].IsDir {
				hashIndices = append(hashIndices, i)
			}
		}
		if len(hashIndices) > 0 {
			workers := runtime.NumCPU()
			if workers < 2 {
				workers = 2
			}
			if workers > 4 {
				workers = 4
			}
			if workers > len(hashIndices) {
				workers = len(hashIndices)
			}
			jobs := make(chan int, len(hashIndices))
			for _, idx := range hashIndices {
				jobs <- idx
			}
			close(jobs)
			var wg sync.WaitGroup
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for idx := range jobs {
						if ctx.Err() != nil {
							return
						}
						if err := opt.Controller.WaitIfPaused(ctx); err != nil {
							return
						}
						if sum, sumErr := md5FileInRoot(source, entries[idx].RelPath); sumErr == nil {
							entries[idx].MD5 = sum
						}
					}
				}()
			}
			wg.Wait()
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
		}
	}
	return entries, true, nil
}

// ListDirEntries lists immediate files and subdirectories of virtualPath.
func (c Config) ListDirEntries(ctx context.Context, virtualPath string, opts SessionOptions) ([]DirEntry, bool, error) {
	return c.ListDirEntriesWithHash(ctx, virtualPath, false, opts)
}

// ListDirEntriesWithHash lists immediate files and subdirectories of virtualPath,
// optionally computing/requesting MD5 hashes for regular files.
func (c Config) ListDirEntriesWithHash(ctx context.Context, virtualPath string, includeHash bool, opts SessionOptions) ([]DirEntry, bool, error) {
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
		defer dirFile.Close()
		out := []DirEntry{}
		for {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			entries, readErr := dirFile.ReadDir(256)
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return nil, false, errors.New("无法读取本地授权目录")
			}
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
				md5Sum := ""
				if includeHash && !st.IsDir() {
					childRel := entry.Name()
					if rel != "." && rel != "" {
						childRel = path.Join(rel, entry.Name())
					}
					if sum, sumErr := md5FileInRoot(root, childRel); sumErr == nil {
						md5Sum = sum
					}
				}
				var ino uint64
				if sys, ok := st.Sys().(*syscall.Stat_t); ok {
					ino = sys.Ino
				}
				out = append(out, DirEntry{
					Name:    entry.Name(),
					Path:    path.Join(v, entry.Name()),
					IsDir:   st.IsDir(),
					Size:    st.Size(),
					ModTime: st.ModTime().Unix(),
					MD5:     md5Sum,
					Inode:   ino,
				})
			}
			if errors.Is(readErr, io.EOF) {
				break
			}
		}
		sortDirEntries(out)
		return out, true, nil
	}

	canonical, ok := c.FindKey(n)
	if !ok || !cleanRelative(sub) {
		return nil, false, errors.New("远端路径无效")
	}
	s, err := c.SessionFor([]SessionSpec{{Name: canonical, Mode: diskReadOnly, Checksum: includeHash}}, opts)
	if err != nil {
		return nil, false, err
	}
	defer s.Close()

	lsArgs := []string{"lsjson", "--no-mimetype"}
	if includeHash {
		lsArgs = append(lsArgs, "--hash")
	}
	lsArgs = append(lsArgs, "--", canonical+":"+sub)
	cmd, err := s.Cmd(ctx, lsArgs...)
	if err != nil {
		return nil, false, err
	}
	type listEntry struct {
		Name    string            `json:"Name"`
		Size    int64             `json:"Size"`
		ModTime string            `json:"ModTime"`
		IsDir   bool              `json:"IsDir"`
		ID      string            `json:"ID"`
		Hashes  map[string]string `json:"Hashes"`
	}
	out := []DirEntry{}
	stderr, parseErr, runErr := readRcloneList(cmd, 32<<20, func(r listEntry) {
		if r.Name == "" || r.Name == "." || r.Name == ".." || strings.ContainsAny(r.Name, "/\\\n\r") {
			return
		}
		md5Sum := ""
		if r.Hashes != nil {
			md5Sum = strings.ToLower(strings.TrimSpace(r.Hashes["md5"]))
			if md5Sum == "" {
				md5Sum = strings.ToLower(strings.TrimSpace(r.Hashes["MD5"]))
			}
		}
		out = append(out, DirEntry{
			Name:     r.Name,
			Path:     path.Join(v, r.Name),
			IsDir:    r.IsDir,
			Size:     r.Size,
			ModTime:  parseRcloneTime(r.ModTime).Unix(),
			MD5:      md5Sum,
			ObjectID: strings.TrimSpace(r.ID),
		})
	})
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if isRcloneNotFound(stderr) && (parseErr != nil || runErr != nil) {
		return nil, false, nil
	}
	if runErr != nil || (parseErr != nil && stderr != "") {
		return nil, false, classifyRcloneError(c[canonical]["type"], stderr)
	}
	if parseErr != nil {
		return nil, false, parseErr
	}
	sortDirEntries(out)
	return out, true, nil
}

// StatEntry inspects a single file or directory at virtualPath. Missing paths
// return (nil, false, nil) so callers can tell absence apart from I/O errors.
func (c Config) StatEntry(ctx context.Context, virtualPath string, includeHash bool, opts SessionOptions) (*DirEntry, bool, error) {
	v := strings.TrimSuffix(virtualPath, "/")
	if v == "" || v == "/" {
		return nil, false, errors.New("路径无效")
	}
	n, sub := parts(v)
	if c.isLocalName(n) {
		if sub == "" {
			return nil, false, errors.New("路径无效")
		}
		ep, err := c.ResolveEntry(v)
		if err != nil {
			return nil, false, err
		}
		st, statErr := os.Lstat(ep.LocalPath)
		if statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				return nil, false, nil
			}
			return nil, false, statErr
		}
		if st.Mode()&os.ModeSymlink != 0 || (!st.IsDir() && !st.Mode().IsRegular()) {
			return nil, false, nil
		}
		var ino uint64
		if sys, ok := st.Sys().(*syscall.Stat_t); ok {
			ino = sys.Ino
		}
		md5Sum := ""
		if includeHash && !st.IsDir() {
			if root, rel, openErr := openDisk(sub); openErr == nil {
				if sum, sumErr := md5FileInRoot(root, rel); sumErr == nil {
					md5Sum = sum
				}
				_ = root.Close()
			}
		}
		return &DirEntry{
			Name:    st.Name(),
			Path:    v,
			IsDir:   st.IsDir(),
			Size:    st.Size(),
			ModTime: st.ModTime().Unix(),
			MD5:     md5Sum,
			Inode:   ino,
		}, true, nil
	}

	canonical, ok := c.FindKey(n)
	if !ok || !cleanRelative(sub) {
		return nil, false, errors.New("远端路径无效")
	}
	s, err := c.SessionFor([]SessionSpec{{Name: canonical, Mode: diskReadOnly, Checksum: includeHash}}, opts)
	if err != nil {
		return nil, false, err
	}
	defer s.Close()

	lsArgs := []string{"lsjson", "--stat", "--no-mimetype"}
	if includeHash {
		lsArgs = append(lsArgs, "--hash")
	}
	lsArgs = append(lsArgs, "--", canonical+":"+sub)
	cmd, err := s.Cmd(ctx, lsArgs...)
	if err != nil {
		return nil, false, err
	}
	outBytes, stderr, err := limitedOutputWithStderr(cmd, 1<<20)
	if err != nil {
		if isRcloneNotFound(stderr) {
			return nil, false, nil
		}
		return nil, false, classifyRcloneError(c[canonical]["type"], stderr)
	}
	var r struct {
		Name    string            `json:"Name"`
		Size    int64             `json:"Size"`
		ModTime string            `json:"ModTime"`
		IsDir   bool              `json:"IsDir"`
		ID      string            `json:"ID"`
		Hashes  map[string]string `json:"Hashes"`
	}
	if json.Unmarshal(outBytes, &r) != nil {
		return nil, false, errors.New("无法解析远端文件信息")
	}
	md5Sum := ""
	if r.Hashes != nil {
		md5Sum = strings.ToLower(strings.TrimSpace(r.Hashes["md5"]))
		if md5Sum == "" {
			md5Sum = strings.ToLower(strings.TrimSpace(r.Hashes["MD5"]))
		}
	}
	return &DirEntry{
		Name:     r.Name,
		Path:     v,
		IsDir:    r.IsDir,
		Size:     r.Size,
		ModTime:  parseRcloneTime(r.ModTime).Unix(),
		MD5:      md5Sum,
		ObjectID: strings.TrimSpace(r.ID),
	}, true, nil
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

func isStorageRootPath(virtualPath string, ep Endpoint) bool {
	_, sub := parts(virtualPath)
	if !ep.IsLocal {
		return strings.Trim(sub, "/") == ""
	}
	_, rel, _ := strings.Cut(sub, "/")
	return strings.Trim(rel, "/") == ""
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
	if isStorageRootPath(virtualPath, ep) {
		return errors.New("禁止删除存储根目录，请选择根目录下的具体文件或文件夹")
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
		if isDir {
			err = removeInRootSafe(root, rel, ".", rootDev, hasRootDev)
		} else {
			st, statErr := root.Lstat(rel)
			err = statErr
			if statErr == nil {
				if !st.Mode().IsRegular() || !sameDevice(st, rootDev, hasRootDev) {
					return os.ErrPermission
				}
				err = root.Remove(rel)
			}
		}
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

// DeleteFilesBatch deletes multiple relative file paths under rootVirtual using
// a single local os.Root handle or a single `rclone delete --files-from-raw`
// subprocess for remotes. If the remote batch command reports an error, it
// falls back to per-file deletion so callers receive exact per-file errors.
func (c Config) DeleteFilesBatch(ctx context.Context, rootVirtual string, relPaths []string, opts SessionOptions) (map[string]error, error) {
	if len(relPaths) == 0 {
		return nil, nil
	}
	ep, err := c.Resolve(rootVirtual)
	if err != nil {
		return nil, err
	}
	if ep.ReadOnly {
		return nil, errors.New("只读存储不支持删除")
	}

	seen := make(map[string]struct{}, len(relPaths))
	cleanRels := make([]string, 0, len(relPaths))
	for _, raw := range relPaths {
		rel := strings.TrimPrefix(path.Clean("/"+strings.TrimSpace(raw)), "/")
		if rel == "" || rel == "." || strings.HasPrefix(rel, "../") {
			continue
		}
		if _, dup := seen[rel]; dup {
			continue
		}
		seen[rel] = struct{}{}
		cleanRels = append(cleanRels, rel)
	}
	if len(cleanRels) == 0 {
		return nil, nil
	}

	errs := make(map[string]error)
	if ep.IsLocal {
		_, sub := parts(rootVirtual)
		root, baseRel, rootDev, hasRootDev, openErr := openDiskWithDev(sub)
		if openErr != nil {
			return nil, openErr
		}
		defer root.Close()
		for _, rel := range cleanRels {
			if ctx.Err() != nil {
				return errs, ctx.Err()
			}
			target := rel
			if baseRel != "" && baseRel != "." {
				target = path.Join(baseRel, rel)
			}
			if target == "" || target == "." {
				errs[rel] = errors.New("拒绝删除本地授权根目录")
				continue
			}
			if delErr := removeInRootSafe(root, target, ".", rootDev, hasRootDev); delErr != nil && !errors.Is(delErr, os.ErrNotExist) {
				errs[rel] = delErr
			}
		}
		return errs, nil
	}

	var batchSafe []string
	var individual []string
	for _, rel := range cleanRels {
		if strings.ContainsAny(rel, "\r\n") {
			individual = append(individual, rel)
		} else {
			batchSafe = append(batchSafe, rel)
		}
	}

	rootTrim := strings.TrimRight(strings.TrimSpace(rootVirtual), "/")
	if len(batchSafe) == 1 {
		individual = append(individual, batchSafe[0])
		batchSafe = nil
	}
	if len(batchSafe) > 1 {
		name, _ := parts(rootVirtual)
		s, sessErr := c.SessionFor([]SessionSpec{{Name: name, Mode: diskFullWrite}}, opts)
		if sessErr != nil {
			return nil, sessErr
		}
		listFile := filepath.Join(s.dir, "delete-list")
		f, createErr := os.OpenFile(listFile, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
		if createErr != nil {
			s.Close()
			return nil, createErr
		}
		bw := bufio.NewWriter(f)
		for _, rel := range batchSafe {
			if _, wErr := fmt.Fprintln(bw, rel); wErr != nil {
				_ = f.Close()
				s.Close()
				return nil, wErr
			}
		}
		if flushErr := errors.Join(bw.Flush(), f.Close()); flushErr != nil {
			s.Close()
			return nil, flushErr
		}

		cmd, cmdErr := s.Cmd(ctx, "delete", "--files-from-raw", listFile, "--no-traverse", "--checkers", "16", "--", ep.Path)
		if cmdErr != nil {
			s.Close()
			return nil, cmdErr
		}
		_, stderr, runErr := limitedOutputWithStderr(cmd, 64<<10)
		s.Close()
		if runErr != nil && !isRcloneNotFound(stderr) {
			if ctx.Err() != nil {
				return errs, ctx.Err()
			}
			// Batch command failed; fall back to individual deletes for exact per-file status.
			individual = append(individual, batchSafe...)
		}
	}

	if len(individual) == 1 {
		if ctx.Err() != nil {
			return errs, ctx.Err()
		}
		if delErr := c.DeletePath(ctx, rootTrim+"/"+individual[0], false, opts); delErr != nil {
			errs[individual[0]] = delErr
		}
	} else if len(individual) > 1 {
		var errMu sync.Mutex
		sem := make(chan struct{}, min(4, len(individual)))
		var wg sync.WaitGroup
		for _, rel := range individual {
			if ctx.Err() != nil {
				break
			}
			sem <- struct{}{}
			wg.Add(1)
			go func(rel string) {
				defer wg.Done()
				defer func() { <-sem }()
				if ctx.Err() != nil {
					return
				}
				if delErr := c.DeletePath(ctx, rootTrim+"/"+rel, false, opts); delErr != nil {
					errMu.Lock()
					errs[rel] = delErr
					errMu.Unlock()
				}
			}(rel)
		}
		wg.Wait()
		if ctx.Err() != nil {
			return errs, ctx.Err()
		}
	}
	return errs, nil
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
	return c.transferPath(ctx, srcVirtual, dstVirtual, isDir, true, opts)
}

// CopyFileToPath copies one file to an exact destination, keeping the source intact.
func (c Config) CopyFileToPath(ctx context.Context, srcVirtual, dstVirtual string, opts SessionOptions) error {
	return c.transferPath(ctx, srcVirtual, dstVirtual, false, false, opts)
}

func (c Config) transferPath(ctx context.Context, srcVirtual, dstVirtual string, isDir, move bool, opts SessionOptions) error {
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
	if isStorageRootPath(srcVirtual, srcEp) || isStorageRootPath(dstVirtual, dstEp) {
		return errors.New("禁止移动存储根目录，请选择根目录下的具体文件或文件夹")
	}
	if (move && srcEp.ReadOnly) || dstEp.ReadOnly {
		return errors.New("只读存储不支持移动操作")
	}
	// Fast path: local->local rename within the same authorized root.
	if move && srcEp.IsLocal && dstEp.IsLocal {
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
			if st.Mode()&os.ModeSymlink != 0 || (!isDir && !st.Mode().IsRegular()) || !sameDevice(st, rootDev, hasRootDev) {
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
	srcMode := diskFullWrite
	if !move {
		srcMode = diskReadOnly
	}
	var specs []SessionSpec
	if !srcEp.IsLocal {
		specs = append(specs, SessionSpec{Name: srcName, Mode: srcMode})
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
			srcArg, err = s.diskWithMode(srcSub, srcMode, false)
		} else {
			parentSub := path.Dir(srcSub)
			base := path.Base(srcSub)
			var parentArg string
			parentArg, err = s.diskWithMode(parentSub, srcMode, false)
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
	args := []string{"moveto"}
	if isDir {
		args[0] = "move"
	}
	if !move {
		args = []string{"copyto", "--ignore-times"}
	}
	cmd, err := s.Cmd(ctx, append(args, "--", srcArg, dstArg)...)
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
	quarkRemote := canonical
	for c[quarkRemote] != nil && c[quarkRemote]["type"] == "alias" {
		ref, _, cutOK := strings.Cut(c[quarkRemote]["remote"], ":")
		if !cutOK {
			break
		}
		if matched, found := c.FindKey(ref); found {
			quarkRemote = matched
		} else {
			break
		}
	}
	if c[quarkRemote] != nil && c[quarkRemote]["type"] == "quark" {
		orig := maps.Clone(c[quarkRemote])
		qClient, err := newQuarkClientFromParams(orig, opts, func(newCookie string) {
			dispatchTokenUpdate(quarkRemote, orig, "cookie", newCookie, opts)
		})
		if err != nil {
			return nil, err
		}
		defer qClient.Close()
		q, err := qClient.About(ctx)
		if err != nil {
			return nil, err
		}
		total, used, free := q.Total, q.Used, q.Free
		return &AboutInfo{
			Supported: true,
			Total:     &total,
			Used:      &used,
			Free:      &free,
		}, nil
	}
	if c[quarkRemote] != nil && c[quarkRemote]["type"] == "pan123" {
		orig := maps.Clone(c[quarkRemote])
		pClient, err := newPan123ClientFromParams(orig, opts, func(newToken string, expiresAt time.Time) {
			dispatchPan123TokenUpdate(quarkRemote, orig, newToken, expiresAt, opts)
		})
		if err != nil {
			return nil, err
		}
		defer pClient.Close()
		q, err := pClient.About(ctx)
		if err != nil {
			return nil, err
		}
		total, used, free := q.Total, q.Used, q.Free
		return &AboutInfo{
			Supported: true,
			Total:     &total,
			Used:      &used,
			Free:      &free,
		}, nil
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
		if bw := strings.TrimSpace(opts.BwLimit); bw != "" {
			if err := ValidateBwLimit(bw); err != nil {
				return err
			}
			args = append(args, "--bwlimit", bw)
		}
		args = append(args, "--", srcArg, targetDir)
	} else {
		targetFile := joinRcloneRemotePath(dstArg, baseName)
		args = []string{"copyto"}
		if !overwrite {
			args = append(args, "--ignore-existing")
		}
		if bw := strings.TrimSpace(opts.BwLimit); bw != "" {
			if err := ValidateBwLimit(bw); err != nil {
				return err
			}
			args = append(args, "--bwlimit", bw)
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
	Download        bool
	CryptCheck      bool
	Compare         int // 0/2: hash/default, 1: size-only
	OneWay          bool
	Checkers        int
	Retries         int
	FilterLines     []string // rclone --filter-from rules
	MinSize         int64
	MaxSize         int64
	MinAgeMinutes   int
	OlderThanDays   int
	NewerThanDays   int
	BwLimit         string
	TPSLimit        float64
	TPSLimitBurst   int
	ModifyWindow    string
	FastList        bool
	FollowSymlinks  bool
	UserAgent       string
	IOTimeout       string
	ConTimeout      string
	LowLevelRetries int
	RetriesSleep    string
	ProxyURL        string
	MaxItems        int // cap preview items per category (default 500)
}

type CheckResult struct {
	Mode              string   `json:"mode"` // "check" | "download" | "cryptcheck"
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

func appendFilterCriteriaFlags(args []string, minSize, maxSize int64, minAgeMin, olderDays, newerDays int) []string {
	if minSize > 0 {
		args = append(args, "--min-size", strconv.FormatInt(minSize, 10)+"B")
	}
	if maxSize > 0 {
		args = append(args, "--max-size", strconv.FormatInt(maxSize, 10)+"B")
	}
	effectiveMinMinutes := minAgeMin
	if olderDays > 0 {
		olderMinutes := olderDays * 24 * 60
		if olderMinutes > effectiveMinMinutes {
			effectiveMinMinutes = olderMinutes
		}
	}
	if effectiveMinMinutes > 0 {
		args = append(args, "--min-age", strconv.Itoa(effectiveMinMinutes)+"m")
	}
	if newerDays > 0 {
		args = append(args, "--max-age", strconv.Itoa(newerDays*24)+"h")
	}
	return args
}

// Check runs `rclone check` (or `rclone cryptcheck` when CryptCheck is enabled)
// between src and dst and returns structured verification counts and previews.
func (c Config) Check(ctx context.Context, src, dst string, opt CheckOptions, emit func(*Stats)) (*CheckResult, error) {
	srcEp, dstEp, err := c.Pair(src, dst)
	if err != nil {
		return nil, err
	}
	useCryptCheck := opt.CryptCheck && !opt.Download
	if opt.CryptCheck && !srcEp.IsCrypt && !dstEp.IsCrypt {
		return nil, errors.New("cryptcheck 仅支持包含 Crypt 加密存储的任务")
	}
	swappedForCrypt := false
	if useCryptCheck && !dstEp.IsCrypt && srcEp.IsCrypt {
		if opt.OneWay {
			return nil, errors.New("源端为 Crypt 且开启单向校验时请改用常规或逐字节校验")
		}
		swappedForCrypt = true
	}

	effectiveCompare := EffectiveCompareMode(srcEp, dstEp, opt.Compare)
	useChecksum := !opt.Download && !useCryptCheck && effectiveCompare != 1 && endpointSupportsNativeMD5(srcEp) && endpointSupportsNativeMD5(dstEp)

	srcName, srcSub := parts(src)
	dstName, dstSub := parts(dst)
	var specs []SessionSpec
	if !srcEp.IsLocal {
		specs = append(specs, SessionSpec{Name: srcName, Mode: diskReadOnly, Checksum: useChecksum || useCryptCheck})
	}
	if !dstEp.IsLocal {
		specs = append(specs, SessionSpec{Name: dstName, Mode: diskReadOnly, Checksum: useChecksum || useCryptCheck})
	}
	s, err := c.SessionFor(specs, SessionOptions{ProxyURL: opt.ProxyURL, PreserveCryptChecksum: useCryptCheck})
	if err != nil {
		return nil, err
	}
	defer s.Close()

	srcArg := srcEp.Path
	if srcEp.IsLocal {
		srcArg, err = s.diskWithOptions(srcSub, diskReadOnly, useChecksum || useCryptCheck, opt.FollowSymlinks)
		if err != nil {
			return nil, err
		}
	}
	dstArg := dstEp.Path
	if dstEp.IsLocal {
		dstArg, err = s.diskWithMode(dstSub, diskReadOnly, useChecksum || useCryptCheck)
		if err != nil {
			return nil, err
		}
	}

	matchFile := filepath.Join(s.dir, "match")
	differFile := filepath.Join(s.dir, "differ")
	missSrcFile := filepath.Join(s.dir, "missing-src")
	missDstFile := filepath.Join(s.dir, "missing-dst")
	errFile := filepath.Join(s.dir, "errors")

	subCmd := "check"
	modeLabel := "check"
	if opt.Download {
		modeLabel = "download"
	} else if useCryptCheck {
		subCmd = "cryptcheck"
		modeLabel = "cryptcheck"
	}

	lowLevelRetries := opt.LowLevelRetries
	if lowLevelRetries <= 0 {
		lowLevelRetries = 10
	}
	args := []string{
		subCmd,
		"--use-json-log", "--log-level", "INFO", "--stats", "1s",
		"--checkers", strconv.Itoa(max(1, opt.Checkers)),
		"--retries", strconv.Itoa(max(1, opt.Retries+1)),
		"--low-level-retries", strconv.Itoa(lowLevelRetries),
		"--match", matchFile,
		"--differ", differFile,
		"--missing-on-src", missSrcFile,
		"--missing-on-dst", missDstFile,
		"--error", errFile,
	}
	if rs := strings.TrimSpace(opt.RetriesSleep); rs != "" {
		if err := ValidateDurationSpec(rs, time.Hour, "重试间隔格式无效，如 5s、30s、1m（最大 1h）"); err != nil {
			return nil, err
		}
		args = append(args, "--retries-sleep", rs)
	}
	if opt.Download {
		args = append(args, "--download")
	} else if !useCryptCheck && effectiveCompare == 1 {
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
	args = appendFilterCriteriaFlags(args, opt.MinSize, opt.MaxSize, opt.MinAgeMinutes, opt.OlderThanDays, opt.NewerThanDays)
	args, err = appendTuningFlags(args, CopyOptions{
		BwLimit:       opt.BwLimit,
		TPSLimit:      opt.TPSLimit,
		TPSLimitBurst: opt.TPSLimitBurst,
		ModifyWindow:  opt.ModifyWindow,
		FastList:      opt.FastList,
		UserAgent:     opt.UserAgent,
		IOTimeout:     opt.IOTimeout,
		ConTimeout:    opt.ConTimeout,
	})
	if err != nil {
		return nil, err
	}
	if swappedForCrypt {
		args = append(args, "--", dstArg, srcArg)
	} else {
		args = append(args, "--", srcArg, dstArg)
	}

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
			log.Printf("native rclone %s failed (%v): %s", subCmd, runErr, detail)
			return nil, fmt.Errorf("一致性校验失败：%s", detail)
		}
		return nil, fmt.Errorf("一致性校验失败：%w", runErr)
	}

	limit := opt.MaxItems
	if limit <= 0 {
		limit = 500
	}
	res := &CheckResult{
		Mode:         modeLabel,
		Differ:       []string{},
		MissingOnSrc: []string{},
		MissingOnDst: []string{},
		Errors:       []string{},
	}
	res.MatchedCount, _, _ = readLinesCapped(matchFile, 0)
	var trunc bool
	res.DifferCount, res.Differ, trunc = readLinesCapped(differFile, limit)
	res.Truncated = res.Truncated || trunc
	if swappedForCrypt {
		res.MissingOnDstCount, res.MissingOnDst, trunc = readLinesCapped(missSrcFile, limit)
		res.Truncated = res.Truncated || trunc
		res.MissingOnSrcCount, res.MissingOnSrc, trunc = readLinesCapped(missDstFile, limit)
		res.Truncated = res.Truncated || trunc
	} else {
		res.MissingOnSrcCount, res.MissingOnSrc, trunc = readLinesCapped(missSrcFile, limit)
		res.Truncated = res.Truncated || trunc
		res.MissingOnDstCount, res.MissingOnDst, trunc = readLinesCapped(missDstFile, limit)
		res.Truncated = res.Truncated || trunc
	}
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

// SizeInfo reports recursive object count and byte total for a virtual path.
type SizeInfo struct {
	Count    int64 `json:"count"`
	Bytes    int64 `json:"bytes"`
	Sizeless int64 `json:"sizeless"`
}

// Size counts objects and total bytes under virtualPath.
func (c Config) Size(ctx context.Context, virtualPath string, opts SessionOptions) (*SizeInfo, error) {
	ep, err := c.Resolve(virtualPath)
	if err != nil {
		return nil, err
	}
	if ep.IsLocal {
		_, sub := parts(virtualPath)
		entries, exists, err := listLocalTree(ctx, sub, TreeOptions{})
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, errors.New("本地目录不存在")
		}
		info := &SizeInfo{}
		for _, e := range entries {
			if !e.IsDir {
				info.Count++
				info.Bytes += e.Size
			}
		}
		return info, nil
	}
	name, _ := parts(virtualPath)
	s, err := c.SessionFor([]SessionSpec{{Name: name, Mode: diskReadOnly}}, opts)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	cmd, err := s.Cmd(ctx, "size", "--json", "--", ep.Path)
	if err != nil {
		return nil, err
	}
	out, stderr, err := limitedOutputWithStderr(cmd, 64<<10)
	if err != nil {
		return nil, classifyRcloneError(ep.Backend, stderr)
	}
	var parsed SizeInfo
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, errors.New("无法解析目录统计信息")
	}
	return &parsed, nil
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
