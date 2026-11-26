package native

import (
	"bytes"
	"context"
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
	"time"

	"opensync/internal/pan123"
	"opensync/internal/quark"
)

// BridgeEntry is a backend-agnostic filesystem entry exposed through OpenSync's
// in-process loopback OwnCloud WebDAV bridge (BridgeFS).
type BridgeEntry struct {
	Name    string
	IsDir   bool
	Size    int64
	ModTime time.Time
	MD5     string
}

// BridgeFS defines the minimal filesystem contract needed to mount any pure-Go
// cloud drive driver (starting with Quark/UC) as a loopback OwnCloud WebDAV
// endpoint consumed by OpenSync's bundled rclone.
type BridgeFS interface {
	Stat(ctx context.Context, relPath string) (BridgeEntry, error)
	ListDir(ctx context.Context, relPath string) ([]BridgeEntry, error)
	OpenRead(ctx context.Context, relPath string, rangeHeader, ifRange string) (io.ReadCloser, http.Header, int, error)
	Put(ctx context.Context, relPath string, body io.Reader, size int64, modTime *time.Time) error
	Mkdir(ctx context.Context, relPath string) error
	Move(ctx context.Context, srcRel, dstRel string, overwrite bool) error
	Delete(ctx context.Context, relPath string) error
	Close() error
}

var (
	quarkOptionsHookMu sync.RWMutex
	quarkOptionsHook   func(*quark.Options)
)

// setQuarkOptionsHookForTest lets unit tests redirect Quark/UC API calls to an
// httptest.Server without exposing endpoint override fields in user config.
func setQuarkOptionsHookForTest(fn func(*quark.Options)) func() {
	quarkOptionsHookMu.Lock()
	prev := quarkOptionsHook
	quarkOptionsHook = fn
	quarkOptionsHookMu.Unlock()
	return func() {
		quarkOptionsHookMu.Lock()
		quarkOptionsHook = prev
		quarkOptionsHookMu.Unlock()
	}
}

func applyQuarkOptionsHook(opts *quark.Options) {
	quarkOptionsHookMu.RLock()
	fn := quarkOptionsHook
	quarkOptionsHookMu.RUnlock()
	if fn != nil {
		fn(opts)
	}
}

func newQuarkClientFromParams(p map[string]string, sessOpts SessionOptions, onCookieUpdate func(string)) (*quark.Client, error) {
	proxyURL := strings.TrimSpace(p["proxy_url"])
	if proxyURL == "" {
		proxyURL = strings.TrimSpace(sessOpts.ProxyURL)
	}
	insecure := p["insecure_skip_verify"] == "true" || sessOpts.InsecureSkipVerify

	var tps float64
	if raw := strings.TrimSpace(p["tps_limit"]); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 {
			tps = v
		}
	}
	var conc int
	if raw := strings.TrimSpace(p["upload_concurrency"]); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 1 && v <= 8 {
			conc = v
		}
	}
	var partSize int64
	if raw := strings.TrimSpace(p["part_size"]); raw != "" {
		if v, ok := parseRcloneSizeBytes(raw); ok && v > 0 {
			partSize = v
		}
	}

	opts := quark.Options{
		Provider:           quark.ProviderQuark,
		Cookie:             p["cookie"],
		RootFolderID:       strings.TrimSpace(p["root_folder_id"]),
		UserAgent:          strings.TrimSpace(p["user_agent"]),
		ProxyURL:           proxyURL,
		InsecureSkipVerify: insecure,
		TPSLimit:           tps,
		UploadConcurrency:  conc,
		PartSize:           partSize,
		OnCookieUpdate:     onCookieUpdate,
	}
	applyQuarkOptionsHook(&opts)
	return quark.NewClient(opts)
}

func parseRcloneSizeBytes(raw string) (int64, bool) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" || s == "off" || s == "0" {
		return 0, false
	}
	mult := float64(1)
	switch {
	case strings.HasSuffix(s, "gib"):
		mult = 1 << 30
		s = strings.TrimSuffix(s, "gib")
	case strings.HasSuffix(s, "gi") || strings.HasSuffix(s, "g"):
		mult = 1 << 30
		s = strings.TrimRight(s, "gi")
	case strings.HasSuffix(s, "mib"):
		mult = 1 << 20
		s = strings.TrimSuffix(s, "mib")
	case strings.HasSuffix(s, "mi") || strings.HasSuffix(s, "m"):
		mult = 1 << 20
		s = strings.TrimRight(s, "mi")
	case strings.HasSuffix(s, "kib"):
		mult = 1 << 10
		s = strings.TrimSuffix(s, "kib")
	case strings.HasSuffix(s, "ki") || strings.HasSuffix(s, "k"):
		mult = 1 << 10
		s = strings.TrimRight(s, "ki")
	case strings.HasSuffix(s, "b"):
		s = strings.TrimSuffix(s, "b")
	}
	val, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || val <= 0 {
		return 0, false
	}
	return int64(val * mult), true
}

// quarkBridgeFS adapts *quark.Client to the generic BridgeFS interface.
type quarkBridgeFS struct {
	client *quark.Client
}

func (q *quarkBridgeFS) Stat(ctx context.Context, relPath string) (BridgeEntry, error) {
	e, err := q.client.Stat(ctx, relPath)
	if err != nil {
		return BridgeEntry{}, err
	}
	return BridgeEntry{
		Name:    e.Name,
		IsDir:   e.IsDir,
		Size:    e.Size,
		ModTime: e.ModTime,
		MD5:     e.MD5,
	}, nil
}

func (q *quarkBridgeFS) ListDir(ctx context.Context, relPath string) ([]BridgeEntry, error) {
	items, err := q.client.ListDir(ctx, relPath)
	if err != nil {
		return nil, err
	}
	out := make([]BridgeEntry, 0, len(items))
	for _, e := range items {
		out = append(out, BridgeEntry{
			Name:    e.Name,
			IsDir:   e.IsDir,
			Size:    e.Size,
			ModTime: e.ModTime,
			MD5:     e.MD5,
		})
	}
	return out, nil
}

func (q *quarkBridgeFS) OpenRead(ctx context.Context, relPath, rangeHeader, ifRange string) (io.ReadCloser, http.Header, int, error) {
	return q.client.DownloadStream(ctx, relPath, rangeHeader, ifRange)
}

func (q *quarkBridgeFS) Put(ctx context.Context, relPath string, body io.Reader, size int64, modTime *time.Time) error {
	_, err := q.client.UploadStream(ctx, relPath, body, size, modTime)
	return err
}

func (q *quarkBridgeFS) Mkdir(ctx context.Context, relPath string) error {
	_, err := q.client.MkdirAll(ctx, relPath)
	return err
}

func (q *quarkBridgeFS) Move(ctx context.Context, srcRel, dstRel string, overwrite bool) error {
	return q.client.Move(ctx, srcRel, dstRel, overwrite)
}

func (q *quarkBridgeFS) Delete(ctx context.Context, relPath string) error {
	return q.client.Delete(ctx, relPath)
}

func (q *quarkBridgeFS) Close() error {
	return q.client.Close()
}

var (
	pan123OptionsHookMu sync.RWMutex
	pan123OptionsHook   func(*pan123.Options)
)

// setPan123OptionsHookForTest lets unit tests redirect 123Pan API calls to an
// httptest.Server without exposing endpoint override fields in user config.
func setPan123OptionsHookForTest(fn func(*pan123.Options)) func() {
	pan123OptionsHookMu.Lock()
	prev := pan123OptionsHook
	pan123OptionsHook = fn
	pan123OptionsHookMu.Unlock()
	return func() {
		pan123OptionsHookMu.Lock()
		pan123OptionsHook = prev
		pan123OptionsHookMu.Unlock()
	}
}

func applyPan123OptionsHook(opts *pan123.Options) {
	pan123OptionsHookMu.RLock()
	fn := pan123OptionsHook
	pan123OptionsHookMu.RUnlock()
	if fn != nil {
		fn(opts)
	}
}

func newPan123ClientFromParams(p map[string]string, sessOpts SessionOptions, onTokenUpdate func(string, time.Time)) (*pan123.Client, error) {
	proxyURL := strings.TrimSpace(p["proxy_url"])
	if proxyURL == "" {
		proxyURL = strings.TrimSpace(sessOpts.ProxyURL)
	}
	insecure := p["insecure_skip_verify"] == "true" || sessOpts.InsecureSkipVerify

	var tps float64
	if raw := strings.TrimSpace(p["tps_limit"]); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 {
			tps = v
		}
	}
	var conc int
	if raw := strings.TrimSpace(p["upload_concurrency"]); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 1 && v <= pan123.MaxUploadConcurrency {
			conc = v
		}
	}
	var ttlMin int
	if raw := strings.TrimSpace(p["direct_link_ttl"]); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			ttlMin = v
		}
	}
	var expAt time.Time
	if raw := strings.TrimSpace(p["token_expires_at"]); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			expAt = parsed
		}
	}

	opts := pan123.Options{
		Protocol:           pan123.NormalizeProtocol(p["protocol"]),
		ClientID:           strings.TrimSpace(p["client_id"]),
		ClientSecret:       strings.TrimSpace(p["client_secret"]),
		AccessToken:        strings.TrimSpace(p["access_token"]),
		TokenExpiresAt:     expAt,
		UseDirectLink:      p["use_direct_link"] == "true",
		DirectLinkKey:      strings.TrimSpace(p["direct_link_key"]),
		DirectLinkTTL:      ttlMin,
		Username:           strings.TrimSpace(p["web_user"]),
		Password:           p["web_password"],
		Platform:           strings.TrimSpace(p["platform"]),
		RootFolderID:       strings.TrimSpace(p["root_folder_id"]),
		UserAgent:          strings.TrimSpace(p["user_agent"]),
		ProxyURL:           proxyURL,
		InsecureSkipVerify: insecure,
		TPSLimit:           tps,
		UploadConcurrency:  conc,
		OnTokenUpdate:      onTokenUpdate,
	}
	applyPan123OptionsHook(&opts)
	return pan123.NewClient(opts)
}

// pan123BridgeFS adapts *pan123.Client to the generic BridgeFS interface.
type pan123BridgeFS struct {
	client *pan123.Client
}

func (p *pan123BridgeFS) Stat(ctx context.Context, relPath string) (BridgeEntry, error) {
	e, err := p.client.Stat(ctx, relPath)
	if err != nil {
		return BridgeEntry{}, err
	}
	return BridgeEntry{
		Name:    e.Name,
		IsDir:   e.IsDir,
		Size:    e.Size,
		ModTime: e.ModTime,
		MD5:     e.MD5,
	}, nil
}

func (p *pan123BridgeFS) ListDir(ctx context.Context, relPath string) ([]BridgeEntry, error) {
	items, err := p.client.ListDir(ctx, relPath)
	if err != nil {
		return nil, err
	}
	out := make([]BridgeEntry, 0, len(items))
	for _, e := range items {
		out = append(out, BridgeEntry{
			Name:    e.Name,
			IsDir:   e.IsDir,
			Size:    e.Size,
			ModTime: e.ModTime,
			MD5:     e.MD5,
		})
	}
	return out, nil
}

func (p *pan123BridgeFS) OpenRead(ctx context.Context, relPath, rangeHeader, ifRange string) (io.ReadCloser, http.Header, int, error) {
	return p.client.DownloadStream(ctx, relPath, rangeHeader, ifRange)
}

func (p *pan123BridgeFS) Put(ctx context.Context, relPath string, body io.Reader, size int64, modTime *time.Time) error {
	_, err := p.client.UploadStream(ctx, relPath, body, size, modTime)
	return err
}

func (p *pan123BridgeFS) Mkdir(ctx context.Context, relPath string) error {
	_, err := p.client.MkdirAll(ctx, relPath)
	return err
}

func (p *pan123BridgeFS) Move(ctx context.Context, srcRel, dstRel string, overwrite bool) error {
	return p.client.Move(ctx, srcRel, dstRel, overwrite)
}

func (p *pan123BridgeFS) Delete(ctx context.Context, relPath string) error {
	return p.client.Delete(ctx, relPath)
}

func (p *pan123BridgeFS) Close() error {
	return p.client.Close()
}

func writeBridgeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, os.ErrNotExist):
		http.Error(w, "Not Found", http.StatusNotFound)
	case errors.Is(err, os.ErrExist):
		http.Error(w, "Precondition Failed", http.StatusPreconditionFailed)
	case errors.Is(err, os.ErrPermission):
		http.Error(w, "Forbidden", http.StatusForbidden)
	case errors.Is(err, quark.ErrUnauthorized), errors.Is(err, pan123.ErrUnauthorized), errors.Is(err, pan123.ErrAuthChallenge):
		http.Error(w, "Unauthorized: "+err.Error(), http.StatusUnauthorized)
	case errors.Is(err, quark.ErrRateLimited), errors.Is(err, pan123.ErrRateLimited):
		http.Error(w, "Too Many Requests", http.StatusTooManyRequests)
	case errors.Is(err, quark.ErrInvalidName), errors.Is(err, pan123.ErrInvalidName):
		http.Error(w, "Bad Request", http.StatusBadRequest)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		http.Error(w, "Request Timeout", http.StatusGatewayTimeout)
	default:
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
}

func writeBridgePropfindEntry(buf *bytes.Buffer, hrefPath string, entry BridgeEntry, wantChecksum bool) {
	escaped := (&url.URL{Path: hrefPath}).EscapedPath()
	buf.WriteString("<D:response><D:href>")
	_ = xml.EscapeText(buf, []byte(escaped))
	buf.WriteString("</D:href><D:propstat><D:prop>")
	if entry.IsDir {
		buf.WriteString("<D:resourcetype><D:collection/></D:resourcetype>")
	} else {
		buf.WriteString("<D:resourcetype/>")
		buf.WriteString("<D:getcontentlength>")
		buf.WriteString(strconv.FormatInt(entry.Size, 10))
		buf.WriteString("</D:getcontentlength>")
	}
	if !entry.ModTime.IsZero() {
		buf.WriteString("<D:getlastmodified>")
		buf.WriteString(entry.ModTime.UTC().Format(http.TimeFormat))
		buf.WriteString("</D:getlastmodified>")
	}
	if wantChecksum && !entry.IsDir && entry.MD5 != "" {
		buf.WriteString(`<oc:checksums><oc:checksum>MD5:`)
		buf.WriteString(strings.ToLower(entry.MD5))
		buf.WriteString(`</oc:checksum></oc:checksums>`)
	}
	buf.WriteString("</D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>")
}

func bridgeRelPath(urlPath, prefix string) (cleanReq string, rel string, ok bool) {
	if urlPath != prefix && !strings.HasPrefix(urlPath, prefix+"/") {
		return "", "", false
	}
	sub := strings.TrimPrefix(urlPath, prefix)
	cleanReq = path.Clean("/" + sub)
	rel = strings.Trim(cleanReq, "/")
	if rel == "." {
		rel = ""
	}
	return cleanReq, rel, true
}

func serveBridgeDAV(fs BridgeFS, prefix string, mode diskAccessMode, enableChecksum bool) http.Handler {
	allowWrite := mode >= diskAppendWrite
	allowDeleteMove := mode >= diskFullWrite

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cleanReq, relPath, ok := bridgeRelPath(r.URL.Path, prefix)
		if !ok {
			http.NotFound(w, r)
			return
		}

		switch r.Method {
		case http.MethodOptions:
			w.Header().Set("DAV", "1, 2")
			w.Header().Set("MS-Author-Via", "DAV")
			w.Header().Set("Allow", "OPTIONS, PROPFIND, GET, HEAD, MKCOL, PUT, DELETE, MOVE")
			w.WriteHeader(http.StatusOK)

		case "PROPFIND":
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
			wantChecksum := enableChecksum && (len(bytes.TrimSpace(body)) == 0 || bytes.Contains(bytes.ToLower(body), []byte("checksum")))

			st, err := fs.Stat(r.Context(), relPath)
			if err != nil {
				writeBridgeError(w, err)
				return
			}

			var buf bytes.Buffer
			if enableChecksum {
				buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?><D:multistatus xmlns:D="DAV:" xmlns:oc="http://owncloud.org/ns">`)
			} else {
				buf.WriteString(`<?xml version="1.0" encoding="UTF-8"?><D:multistatus xmlns:D="DAV:">`)
			}

			baseHref := prefix
			if cleanReq != "/" {
				baseHref += cleanReq
			}
			targetHref := baseHref
			if st.IsDir {
				targetHref += "/"
			}
			writeBridgePropfindEntry(&buf, targetHref, st, wantChecksum)

			if depth == 1 && st.IsDir {
				entries, err := fs.ListDir(r.Context(), relPath)
				if err != nil {
					writeBridgeError(w, err)
					return
				}
				for _, entry := range entries {
					if entry.Name == "" || entry.Name == "." || entry.Name == ".." || strings.ContainsAny(entry.Name, "/\\\r\n") {
						continue
					}
					childHref := baseHref + "/" + entry.Name
					if entry.IsDir {
						childHref += "/"
					}
					writeBridgePropfindEntry(&buf, childHref, entry, wantChecksum)
				}
			}

			buf.WriteString("</D:multistatus>")
			w.Header().Set("Content-Type", "text/xml; charset=utf-8")
			w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write(buf.Bytes())

		case http.MethodHead:
			st, err := fs.Stat(r.Context(), relPath)
			if err != nil {
				writeBridgeError(w, err)
				return
			}
			if st.IsDir {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", strconv.FormatInt(st.Size, 10))
			w.Header().Set("Accept-Ranges", "bytes")
			if !st.ModTime.IsZero() {
				w.Header().Set("Last-Modified", st.ModTime.UTC().Format(http.TimeFormat))
			}
			if st.MD5 != "" {
				w.Header().Set("ETag", `"`+st.MD5+`"`)
			}
			w.WriteHeader(http.StatusOK)

		case http.MethodGet:
			rc, headers, status, err := fs.OpenRead(r.Context(), relPath, r.Header.Get("Range"), r.Header.Get("If-Range"))
			if err != nil {
				writeBridgeError(w, err)
				return
			}
			defer rc.Close()
			for k, vals := range headers {
				for _, v := range vals {
					w.Header().Add(k, v)
				}
			}
			if status == 0 {
				status = http.StatusOK
			}
			w.WriteHeader(status)
			_, _ = io.Copy(w, rc)

		case "MKCOL":
			if !allowWrite {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if relPath == "" {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if err := fs.Mkdir(r.Context(), relPath); err != nil {
				writeBridgeError(w, err)
				return
			}
			w.WriteHeader(http.StatusCreated)

		case http.MethodPut:
			if !allowWrite {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if relPath == "" {
				w.WriteHeader(http.StatusConflict)
				return
			}
			var mt *time.Time
			if raw := strings.TrimSpace(r.Header.Get("X-OC-Mtime")); raw != "" {
				if sec, err := strconv.ParseInt(raw, 10, 64); err == nil && sec > 0 {
					t := time.Unix(sec, 0).UTC()
					mt = &t
				}
			}
			if err := fs.Put(r.Context(), relPath, r.Body, r.ContentLength, mt); err != nil {
				writeBridgeError(w, err)
				return
			}
			w.WriteHeader(http.StatusCreated)

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
			_, dstRel, ok := bridgeRelPath(destURL.Path, prefix)
			if !ok || relPath == "" || dstRel == "" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			overwrite := !strings.EqualFold(strings.TrimSpace(r.Header.Get("Overwrite")), "F")
			if err := fs.Move(r.Context(), relPath, dstRel, overwrite); err != nil {
				writeBridgeError(w, err)
				return
			}
			w.WriteHeader(http.StatusCreated)

		case http.MethodDelete:
			if !allowDeleteMove {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if relPath == "" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if err := fs.Delete(r.Context(), relPath); err != nil {
				writeBridgeError(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

// mountBridgeDAV starts an ephemeral 127.0.0.1 OwnCloud-compatible WebDAV server
// backed by fs, registers shutdown cleanup on s, and returns its base URL.
func (s *Session) mountBridgeDAV(fs BridgeFS, mode diskAccessMode, enableChecksum bool) (string, error) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		_ = fs.Close()
		return "", err
	}
	prefix := "/" + randomName()
	srv := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler:           serveBridgeDAV(fs, prefix, mode, enableChecksum),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ln)
	}()
	s.closers = append(s.closers, func() {
		_ = srv.Close()
		<-done
		_ = fs.Close()
	})
	return fmt.Sprintf("http://%s%s/", ln.Addr(), prefix), nil
}
