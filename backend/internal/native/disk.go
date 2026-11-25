package native

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/net/webdav"
)

const (
	backupDirPerm  os.FileMode = 0755
	backupFilePerm os.FileMode = 0644
)

// diskAccessMode controls which WebDAV mutations are allowed on the loopback
// disk endpoint for a given operation.
type diskAccessMode int

const (
	diskReadOnly    diskAccessMode = iota // PROPFIND, OPTIONS, GET, HEAD
	diskAppendWrite                       // + MKCOL, PUT
	diskFullWrite                         // + DELETE, MOVE (bounded inside os.Root)
)

// A task-scoped loopback DAV endpoint confines every file operation (including
// symlink resolution during a transfer) to the open fnOS-authorized root.
type diskFS struct {
	root           *os.Root
	base           string
	checksum       bool
	followSymlinks bool
	openRead       func(string) (*os.File, error)
}

func (d diskFS) openReadOnly(rel string) (*os.File, error) {
	if d.openRead != nil {
		return d.openRead(rel)
	}
	return d.root.Open(rel)
}

func (d diskFS) name(p string) string {
	return path.Join(".", d.base, strings.TrimPrefix(path.Clean("/"+p), "/"))
}
func (d diskFS) Mkdir(_ context.Context, p string, m os.FileMode) error {
	return mkdirAllPerm(d.root, d.name(p), backupDirPerm)
}
func (d diskFS) OpenFile(_ context.Context, p string, flag int, m os.FileMode) (webdav.File, error) {
	return d.root.OpenFile(d.name(p), flag, m)
}
func (d diskFS) RemoveAll(context.Context, string) error               { return os.ErrPermission }
func (d diskFS) Rename(context.Context, string, string) error          { return os.ErrPermission }
func (d diskFS) Stat(_ context.Context, p string) (os.FileInfo, error) { return d.root.Stat(d.name(p)) }

func md5FileInRoot(root *os.Root, rel string) (string, error) {
	return md5FileInDisk(diskFS{root: root}, rel)
}

func md5FileInDisk(d diskFS, rel string) (string, error) {
	in, err := d.openReadOnly(rel)
	if err != nil {
		return "", err
	}
	defer in.Close()
	h := md5.New()
	if _, err := io.Copy(h, in); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sameDevice(info os.FileInfo, rootDev uint64, hasRootDev bool) bool {
	if !hasRootDev {
		return true
	}
	sys, ok := info.Sys().(*syscall.Stat_t)
	return !ok || uint64(sys.Dev) == rootDev
}

// removeInRootSafe removes a file or directory subtree inside root without ever
// crossing symlinks, mount points, or removing root/base itself.
func removeInRootSafe(root *os.Root, rel, base string, rootDev uint64, hasRootDev bool) error {
	rel = path.Clean(rel)
	base = path.Clean(base)
	if rel == "." || rel == "" || rel == "/" || rel == base {
		return os.ErrPermission
	}
	st, err := root.Lstat(rel)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 || !sameDevice(st, rootDev, hasRootDev) {
		return os.ErrPermission
	}
	if !st.IsDir() {
		return root.Remove(rel)
	}
	f, err := root.Open(rel)
	if err != nil {
		return err
	}
	entries, readErr := f.ReadDir(-1)
	_ = f.Close()
	if readErr != nil {
		return readErr
	}
	for _, entry := range entries {
		childRel := path.Join(rel, entry.Name())
		if entry.Type()&os.ModeSymlink != 0 {
			// Refuse deleting symlinks across boundaries; remove only plain entries.
			if err := root.Remove(childRel); err != nil {
				return err
			}
			continue
		}
		if err := removeInRootSafe(root, childRel, base, rootDev, hasRootDev); err != nil {
			return err
		}
	}
	return root.Remove(rel)
}

func writeDiskReadError(w http.ResponseWriter, err error) {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	if errors.Is(err, os.ErrPermission) {
		http.Error(w, "Permission denied", http.StatusForbidden)
		return
	}
	http.Error(w, "Internal Server Error", http.StatusInternalServerError)
}

func writeDiskWriteError(w http.ResponseWriter, err error) {
	if errors.Is(err, os.ErrPermission) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusConflict)
}

func writePropfindResponse(buf *bytes.Buffer, d diskFS, hrefPath string, st os.FileInfo, rel string, wantChecksum, ignoreChecksumPermErr bool) error {
	escaped := (&url.URL{Path: hrefPath}).EscapedPath()
	buf.WriteString("<D:response><D:href>")
	_ = xml.EscapeText(buf, []byte(escaped))
	buf.WriteString("</D:href><D:propstat><D:prop>")
	if st.IsDir() {
		buf.WriteString("<D:resourcetype><D:collection/></D:resourcetype>")
	} else {
		buf.WriteString("<D:resourcetype/>")
		buf.WriteString("<D:getcontentlength>")
		buf.WriteString(strconv.FormatInt(st.Size(), 10))
		buf.WriteString("</D:getcontentlength>")
	}
	buf.WriteString("<D:getlastmodified>")
	buf.WriteString(st.ModTime().UTC().Format(http.TimeFormat))
	buf.WriteString("</D:getlastmodified>")
	if wantChecksum && !st.IsDir() && st.Mode().IsRegular() {
		sum, err := md5FileInDisk(d, rel)
		if err != nil {
			if !(ignoreChecksumPermErr && errors.Is(err, os.ErrPermission)) {
				return err
			}
		} else {
			buf.WriteString(`<oc:checksums><oc:checksum>MD5:`)
			buf.WriteString(sum)
			buf.WriteString(`</oc:checksum></oc:checksums>`)
		}
	}
	buf.WriteString("</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>")
	return nil
}

// serveGetHead serves GET and HEAD directly against os.Root instead of
// delegating to golang.org/x/net/webdav, whose handleGetHeadPost maps every
// OpenFile error (including EACCES / ACL denial) to HTTP 404 Not Found.
func serveGetHead(w http.ResponseWriter, r *http.Request, d diskFS, prefix string, rootDev uint64, hasRootDev bool) {
	subPath := strings.TrimPrefix(r.URL.Path, prefix)
	rel := d.name(subPath)
	if rel == "." {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var (
		st  os.FileInfo
		err error
	)
	if d.followSymlinks {
		st, err = d.root.Stat(rel)
	} else {
		st, err = d.root.Lstat(rel)
	}
	if err != nil {
		writeDiskReadError(w, err)
		return
	}
	if (!d.followSymlinks && st.Mode()&os.ModeSymlink != 0) || !sameDevice(st, rootDev, hasRootDev) {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	if st.IsDir() {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !st.Mode().IsRegular() {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	f, err := d.openReadOnly(rel)
	if err != nil {
		writeDiskReadError(w, err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		writeDiskReadError(w, err)
		return
	}
	if fi.IsDir() {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !fi.Mode().IsRegular() || !sameDevice(fi, rootDev, hasRootDev) {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%x%x"`, fi.ModTime().UnixNano(), fi.Size()))
	http.ServeContent(w, r, path.Base(rel), fi.ModTime(), f)
}

// servePropfind handles WebDAV PROPFIND directly against os.Root without
// golang.org/x/net/webdav's per-entry triple OpenFile + 512-byte MIME sniff
// (or double DeadProps MD5 hashing). This avoids tens of thousands of random
// disk reads when rclone lists large source trees on spinning HDDs.
func servePropfind(w http.ResponseWriter, r *http.Request, d diskFS, prefix string, rootDev uint64, hasRootDev bool) {
	depth := 1
	switch strings.TrimSpace(r.Header.Get("Depth")) {
	case "", "1":
		depth = 1
	case "0":
		depth = 0
	default:
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	wantChecksum := d.checksum && (len(bytes.TrimSpace(body)) == 0 || bytes.Contains(bytes.ToLower(body), []byte("checksum")))

	subPath := strings.TrimPrefix(r.URL.Path, prefix)
	reqClean := path.Clean("/" + subPath)
	rel := d.name(subPath)

	var (
		st  os.FileInfo
		err error
	)
	if rel == "." {
		st, err = d.root.Stat(".")
	} else if d.followSymlinks {
		// os.Root.Stat strictly confines symlink resolution inside the authorized root.
		st, err = d.root.Stat(rel)
	} else {
		st, err = d.root.Lstat(rel)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if errors.Is(err, os.ErrPermission) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if rel != "." && ((!d.followSymlinks && st.Mode()&os.ModeSymlink != 0) || !sameDevice(st, rootDev, hasRootDev)) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if !st.IsDir() && !st.Mode().IsRegular() {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	var buf bytes.Buffer
	if d.checksum {
		buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?><D:multistatus xmlns:D="DAV:" xmlns:oc="http://owncloud.org/ns">`)
	} else {
		buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?><D:multistatus xmlns:D="DAV:">`)
	}

	baseHref := prefix
	if reqClean != "/" {
		baseHref += reqClean
	}
	targetHref := baseHref
	if st.IsDir() {
		targetHref += "/"
	}
	if err := writePropfindResponse(&buf, d, targetHref, st, rel, wantChecksum, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if errors.Is(err, os.ErrPermission) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if depth == 1 && st.IsDir() {
		dirFile, err := d.root.Open(rel)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if errors.Is(err, os.ErrPermission) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		entries, readErr := dirFile.ReadDir(-1)
		_ = dirFile.Close()
		if readErr != nil {
			if errors.Is(readErr, os.ErrPermission) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		for _, entry := range entries {
			name := entry.Name()
			childRel := path.Join(rel, name)
			var (
				childSt os.FileInfo
				infoErr error
			)
			if entry.Type()&os.ModeSymlink != 0 {
				if !d.followSymlinks {
					continue
				}
				childSt, infoErr = d.root.Stat(childRel)
				if infoErr != nil {
					continue
				}
			} else {
				childSt, infoErr = entry.Info()
				if infoErr != nil {
					continue
				}
				if childSt.Mode()&os.ModeSymlink != 0 {
					continue
				}
			}
			if !sameDevice(childSt, rootDev, hasRootDev) {
				continue
			}
			if !childSt.IsDir() && !childSt.Mode().IsRegular() {
				continue
			}
			childHref := baseHref + "/" + name
			if childSt.IsDir() {
				childHref += "/"
			}
			if err := writePropfindResponse(&buf, d, childHref, childSt, childRel, wantChecksum, true); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
	}

	buf.WriteString("</D:multistatus>")
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(http.StatusMultiStatus)
	_, _ = w.Write(buf.Bytes())
}

// mkdirAllPerm creates dir and any missing parents inside root and explicitly
// applies perm to newly created directories so the process umask (077 under
// fnOS) never locks NAS users out of their own backup folders.
func mkdirAllPerm(root *os.Root, dir string, perm os.FileMode) error {
	clean := path.Clean(dir)
	if clean == "." || clean == "" || clean == "/" {
		return nil
	}
	var cur string
	for _, seg := range strings.Split(clean, "/") {
		if seg == "" || seg == "." {
			continue
		}
		if cur == "" {
			cur = seg
		} else {
			cur = cur + "/" + seg
		}
		if st, err := root.Stat(cur); err == nil {
			if !st.IsDir() {
				return errors.New("目标路径与已有文件冲突")
			}
			continue
		}
		if err := root.Mkdir(cur, perm); err != nil {
			if st, statErr := root.Stat(cur); statErr == nil && st.IsDir() {
				continue
			}
			return err
		}
		// Apply mode explicitly on the opened fd to bypass process umask 077
		// without following a symlink race.
		if f, err := root.Open(cur); err == nil {
			_ = f.Chmod(perm)
			_ = f.Close()
		}
	}
	return nil
}

func openDisk(sub string) (*os.Root, string, error) {
	id, rel, _ := strings.Cut(sub, "/")
	p, e := resolveLocal(id)
	if e != nil {
		return nil, "", e
	}
	root, e := os.OpenRoot(p)
	if e != nil {
		return nil, "", e
	}
	st, e := root.Stat(".")
	if e != nil || !matchesRootID(p, st, id) {
		root.Close()
		return nil, "", errors.New("授权目录的挂载身份已变更")
	}
	if rel == "" {
		rel = "."
	}
	return root, rel, nil
}

func openDiskWithDev(sub string) (*os.Root, string, uint64, bool, error) {
	root, rel, err := openDisk(sub)
	if err != nil {
		return nil, "", 0, false, err
	}
	var rootDev uint64
	var hasRootDev bool
	if rootSt, statErr := root.Stat("."); statErr == nil {
		if sys, ok := rootSt.Sys().(*syscall.Stat_t); ok {
			rootDev = uint64(sys.Dev)
			hasRootDev = true
		}
	}
	return root, rel, rootDev, hasRootDev, nil
}

func randomName() string { var b [16]byte; rand.Read(b[:]); return hex.EncodeToString(b[:]) }

func (s *Session) disk(sub string, write, checksum bool) (string, error) {
	mode := diskReadOnly
	if write {
		mode = diskAppendWrite
	}
	return s.diskWithMode(sub, mode, checksum)
}

func (s *Session) diskWithMode(sub string, mode diskAccessMode, checksum bool) (string, error) {
	return s.diskWithOptions(sub, mode, checksum, false)
}

func (s *Session) diskWithOptions(sub string, mode diskAccessMode, checksum, followSymlinks bool) (string, error) {
	root, rel, rootDev, hasRootDev, e := openDiskWithDev(sub)
	if e != nil {
		return "", e
	}
	// Serve the authorized volume from a private root handle. The unguessable
	// prefix is only in the private config, never logged or passed on argv.
	ln, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		root.Close()
		return "", e
	}
	fs := diskFS{root: root, base: rel, checksum: checksum, followSymlinks: followSymlinks}
	id, _, _ := strings.Cut(sub, "/")
	prefix := "/" + randomName()
	dav := &webdav.Handler{Prefix: prefix, FileSystem: fs, LockSystem: webdav.NewMemLS()}

	// Re-verify fnOS authorization at most once per second instead of on every
	// single DAV request, preserving revocation enforcement without repeating
	// EvalSymlinks + Statfs + SHA256 across all roots tens of thousands of times.
	var lastAuthCheck atomic.Int64
	var authCheckMu sync.Mutex
	lastAuthCheck.Store(time.Now().UnixNano())
	checkAuthorized := func() bool {
		now := time.Now().UnixNano()
		if prev := lastAuthCheck.Load(); prev > 0 && now-prev < int64(time.Second) {
			return true
		}
		authCheckMu.Lock()
		defer authCheckMu.Unlock()
		if prev := lastAuthCheck.Load(); prev > 0 && now-prev < int64(time.Second) {
			return true
		}
		if _, err := resolveLocal(id); err != nil {
			lastAuthCheck.Store(0)
			return false
		}
		lastAuthCheck.Store(now)
		return true
	}

	allowWrite := mode >= diskAppendWrite
	allowDeleteMove := mode >= diskFullWrite

	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, prefix+"/") && r.URL.Path != prefix {
			http.NotFound(w, r)
			return
		}
		if !checkAuthorized() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.Method {
		case "PROPFIND":
			servePropfind(w, r, fs, prefix, rootDev, hasRootDev)
		case "OPTIONS":
			dav.ServeHTTP(w, r)
		case "GET", "HEAD":
			serveGetHead(w, r, fs, prefix, rootDev, hasRootDev)
		case "MKCOL":
			if !allowWrite {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if err := mkdirAllPerm(root, fs.name(strings.TrimPrefix(r.URL.Path, prefix)), backupDirPerm); err != nil {
				writeDiskWriteError(w, err)
				return
			}
			w.WriteHeader(http.StatusCreated)
		case "PUT":
			if !allowWrite {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			target := fs.name(strings.TrimPrefix(r.URL.Path, prefix))
			if target == "." {
				w.WriteHeader(http.StatusConflict)
				return
			}
			parent := path.Dir(target)
			// rclone copy --no-traverse does not MKCOL the WebDAV root when
			// uploading top-level files into a newly specified or derived local
			// subdirectory; ensure the parent directory exists before writing.
			if err := mkdirAllPerm(root, parent, backupDirPerm); err != nil {
				writeDiskWriteError(w, err)
				return
			}
			tmp := path.Join(parent, ".opensync-"+randomName()+".partial")
			file, e := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, backupFilePerm)
			if e != nil {
				writeDiskWriteError(w, e)
				return
			}
			defer root.Remove(tmp)
			count, e := io.Copy(file, r.Body)
			if e == nil {
				// Bypass process umask 077 so backups written into shared NAS
				// folders remain readable to the NAS owner / SMB users.
				_ = file.Chmod(backupFilePerm)
			}
			if closeErr := file.Close(); e == nil {
				e = closeErr
			}
			if e != nil || r.Context().Err() != nil || r.ContentLength >= 0 && count != r.ContentLength {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if sec, e := strconv.ParseInt(r.Header.Get("X-OC-Mtime"), 10, 64); e == nil {
				t := time.Unix(sec, 0)
				if root.Chtimes(tmp, t, t) != nil {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.Header().Set("X-OC-Mtime", "accepted")
			}
			if e = root.Rename(tmp, target); e != nil {
				writeDiskWriteError(w, e)
				return
			}
			w.WriteHeader(http.StatusCreated)
		case "DELETE":
			if !allowDeleteMove {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			target := fs.name(strings.TrimPrefix(r.URL.Path, prefix))
			err := removeInRootSafe(root, target, fs.base, rootDev, hasRootDev)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if errors.Is(err, os.ErrPermission) {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				w.WriteHeader(http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "MOVE":
			if !allowDeleteMove {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			destHeader := strings.TrimSpace(r.Header.Get("Destination"))
			if destHeader == "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			destURL, err := url.Parse(destHeader)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			destPath := destURL.Path
			if !strings.HasPrefix(destPath, prefix+"/") && destPath != prefix {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			srcRel := fs.name(strings.TrimPrefix(r.URL.Path, prefix))
			dstRel := fs.name(strings.TrimPrefix(destPath, prefix))
			baseClean := path.Clean(fs.base)
			if srcRel == "." || dstRel == "." || srcRel == baseClean || dstRel == baseClean {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			srcSt, err := root.Lstat(srcRel)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if errors.Is(err, os.ErrPermission) {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if srcSt.Mode()&os.ModeSymlink != 0 || !sameDevice(srcSt, rootDev, hasRootDev) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			existed := false
			if dstSt, statErr := root.Lstat(dstRel); statErr == nil {
				existed = true
				if strings.EqualFold(strings.TrimSpace(r.Header.Get("Overwrite")), "F") {
					w.WriteHeader(http.StatusPreconditionFailed)
					return
				}
				if dstSt.Mode()&os.ModeSymlink != 0 || !sameDevice(dstSt, rootDev, hasRootDev) {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if dstSt.IsDir() {
					if err := removeInRootSafe(root, dstRel, fs.base, rootDev, hasRootDev); err != nil {
						writeDiskWriteError(w, err)
						return
					}
				}
			}
			if err := mkdirAllPerm(root, path.Dir(dstRel), backupDirPerm); err != nil {
				writeDiskWriteError(w, err)
				return
			}
			if err := root.Rename(srcRel, dstRel); err != nil {
				writeDiskWriteError(w, err)
				return
			}
			if existed {
				w.WriteHeader(http.StatusNoContent)
			} else {
				w.WriteHeader(http.StatusCreated)
			}
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ln) }()
	s.closers = append(s.closers, func() { srv.Close(); <-done; root.Close() })
	name := fmt.Sprintf("_opensync_disk%d", len(s.closers))
	vendor := "rclone"
	extra := ""
	if checksum {
		vendor = "owncloud"
		// Disable OwnCloud/Nextcloud chunked uploads so files > 10 MiB use
		// standard single-PUT requests against the loopback endpoint.
		extra = "nextcloud_chunk_size = 0\n"
	}
	_, e = fmt.Fprintf(s.configFile, "\n[%s]\ntype = webdav\nvendor = %s\n%surl = http://%s%s/\n", name, vendor, extra, ln.Addr(), prefix)
	return name + ":", e
}
