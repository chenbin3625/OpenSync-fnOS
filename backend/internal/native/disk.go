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

// A task-scoped loopback DAV endpoint confines every file operation (including
// symlink resolution during a transfer) to the open fnOS-authorized root.
type diskFS struct {
	root     *os.Root
	base     string
	checksum bool
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
	in, err := root.Open(rel)
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

func writePropfindResponse(buf *bytes.Buffer, root *os.Root, hrefPath string, st os.FileInfo, rel string, wantChecksum bool) error {
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
		sum, err := md5FileInRoot(root, rel)
		if err != nil {
			return err
		}
		buf.WriteString(`<oc:checksums><oc:checksum>MD5:`)
		buf.WriteString(sum)
		buf.WriteString(`</oc:checksum></oc:checksums>`)
	}
	buf.WriteString("</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>")
	return nil
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
	} else {
		st, err = d.root.Lstat(rel)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if rel != "." && (st.Mode()&os.ModeSymlink != 0 || !sameDevice(st, rootDev, hasRootDev)) {
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
	if err := writePropfindResponse(&buf, d.root, targetHref, st, rel, wantChecksum); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			w.WriteHeader(http.StatusNotFound)
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
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		entries, readErr := dirFile.ReadDir(-1)
		_ = dirFile.Close()
		if readErr != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			childSt, infoErr := entry.Info()
			if infoErr != nil {
				continue
			}
			if childSt.Mode()&os.ModeSymlink != 0 || !sameDevice(childSt, rootDev, hasRootDev) {
				continue
			}
			if !childSt.IsDir() && !childSt.Mode().IsRegular() {
				continue
			}
			name := entry.Name()
			childRel := path.Join(rel, name)
			childHref := baseHref + "/" + name
			if childSt.IsDir() {
				childHref += "/"
			}
			if err := writePropfindResponse(&buf, d.root, childHref, childSt, childRel, wantChecksum); err != nil {
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

func randomName() string { var b [16]byte; rand.Read(b[:]); return hex.EncodeToString(b[:]) }

func (s *Session) disk(sub string, write, checksum bool) (string, error) {
	root, rel, e := openDisk(sub)
	if e != nil {
		return "", e
	}
	var rootDev uint64
	var hasRootDev bool
	if rootSt, statErr := root.Stat("."); statErr == nil {
		if sys, ok := rootSt.Sys().(*syscall.Stat_t); ok {
			rootDev = uint64(sys.Dev)
			hasRootDev = true
		}
	}
	// Serve the authorized volume from a private root handle. The unguessable
	// prefix is only in the private config, never logged or passed on argv.
	ln, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		root.Close()
		return "", e
	}
	fs := diskFS{root: root, base: rel, checksum: checksum}
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
		case "OPTIONS", "GET", "HEAD":
			dav.ServeHTTP(w, r)
		case "MKCOL":
			if !write {
				w.WriteHeader(403)
				return
			}
			if err := mkdirAllPerm(root, fs.name(strings.TrimPrefix(r.URL.Path, prefix)), backupDirPerm); err != nil {
				w.WriteHeader(409)
				return
			}
			w.WriteHeader(201)
		case "PUT":
			if !write {
				w.WriteHeader(403)
				return
			}
			target := fs.name(strings.TrimPrefix(r.URL.Path, prefix))
			if target == "." {
				w.WriteHeader(409)
				return
			}
			parent := path.Dir(target)
			// rclone copy --no-traverse does not MKCOL the WebDAV root when
			// uploading top-level files into a newly specified or derived local
			// subdirectory; ensure the parent directory exists before writing.
			if err := mkdirAllPerm(root, parent, backupDirPerm); err != nil {
				w.WriteHeader(409)
				return
			}
			tmp := path.Join(parent, ".opensync-"+randomName()+".partial")
			file, e := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, backupFilePerm)
			if e != nil {
				w.WriteHeader(409)
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
				w.WriteHeader(500)
				return
			}
			if sec, e := strconv.ParseInt(r.Header.Get("X-OC-Mtime"), 10, 64); e == nil {
				t := time.Unix(sec, 0)
				if root.Chtimes(tmp, t, t) != nil {
					w.WriteHeader(500)
					return
				}
				w.Header().Set("X-OC-Mtime", "accepted")
			}
			if e = root.Rename(tmp, target); e != nil {
				w.WriteHeader(409)
				return
			}
			w.WriteHeader(201)
		default:
			w.WriteHeader(405)
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
