package native

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

var errCopyURLBlocked = errors.New("禁止拉取回环、私网或保留地址")

var blockedCopyURLRanges = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
}

func isBlockedCopyURLIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return true
	}
	for _, prefix := range blockedCopyURLRanges {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func validateCopyURLTarget(rawURL string) error {
	if rawURL == "" || len(rawURL) > 2048 {
		return errors.New("请输入有效的 HTTP/HTTPS 下载链接")
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return errors.New("仅支持不含账号密码的 HTTP/HTTPS 下载链接")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.Contains(host, "%") {
		return errCopyURLBlocked
	}
	if ip := net.ParseIP(host); ip != nil && isBlockedCopyURLIP(ip) {
		return errCopyURLBlocked
	}
	return nil
}

// copyURLTransport resolves each request once, rejects any private answer, and
// sends the request to that exact public IP. Host and TLS SNI retain the original
// hostname. Pinning the URL also keeps configured proxies from resolving a
// different address after our check. Redirects go through this transport too.
type copyURLTransport struct {
	proxyURL    string
	lookupIP    func(context.Context, string) ([]net.IPAddr, error)
	dialContext func(context.Context, string, string) (net.Conn, error)
}

func (t *copyURLTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := validateCopyURLTarget(req.URL.String()); err != nil {
		return nil, err
	}
	host := req.URL.Hostname()
	ips := []net.IPAddr{}
	if ip := net.ParseIP(host); ip != nil {
		ips = append(ips, net.IPAddr{IP: ip})
	} else {
		lookup := t.lookupIP
		if lookup == nil {
			lookup = net.DefaultResolver.LookupIPAddr
		}
		var err error
		ips, err = lookup(req.Context(), host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("无法解析下载链接主机名")
		}
	}
	for _, ip := range ips {
		if ip.Zone != "" || isBlockedCopyURLIP(ip.IP) {
			return nil, errCopyURLBlocked
		}
	}
	port := req.URL.Port()
	if port == "" {
		port = "80"
		if req.URL.Scheme == "https" {
			port = "443"
		}
	}
	proxy, err := http.ProxyFromEnvironment(req)
	if t.proxyURL != "" {
		proxy, err = url.Parse(t.proxyURL)
	}
	if err != nil {
		return nil, errors.New("下载代理配置无效")
	}
	dial := t.dialContext
	if dial == nil {
		dial = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	if proxy != nil && (proxy.Scheme == "http" || proxy.Scheme == "https") && req.URL.Scheme == "http" {
		// A plain HTTP proxy request can re-resolve Host or replace the origin's
		// Host header. Tunnel to the pinned IP so both remain under our control.
		proxyDial := dial
		proxyURL := proxy
		dial = func(ctx context.Context, network, target string) (net.Conn, error) {
			return dialCopyURLProxy(ctx, network, target, proxyURL, proxyDial)
		}
		proxy = nil
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyURL(proxy),
		DialContext:           dial,
		TLSClientConfig:       &tls.Config{ServerName: host},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DisableKeepAlives:     true,
		ForceAttemptHTTP2:     true,
	}
	defer transport.CloseIdleConnections()
	pinned := req.Clone(req.Context())
	pinned.URL.Host = net.JoinHostPort(ips[0].IP.String(), port)
	pinned.Host = req.URL.Host
	resp, err := transport.RoundTrip(pinned)
	if resp != nil {
		resp.Request = req
	}
	return resp, err
}

func dialCopyURLProxy(ctx context.Context, network, target string, proxy *url.URL, dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	port := proxy.Port()
	if port == "" {
		port = "80"
		if proxy.Scheme == "https" {
			port = "443"
		}
	}
	conn, err := dial(ctx, network, net.JoinHostPort(proxy.Hostname(), port))
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	rawConn := conn
	stop := context.AfterFunc(ctx, func() { rawConn.Close() })
	defer stop()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if proxy.Scheme == "https" {
		secure := tls.Client(conn, &tls.Config{ServerName: proxy.Hostname()})
		if err := secure.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		conn = secure
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: target}, Host: target, Header: make(http.Header)}
	if proxy.User != nil {
		password, _ := proxy.User.Password()
		credentials := base64.StdEncoding.EncodeToString([]byte(proxy.User.Username() + ":" + password))
		req.Header.Set("Proxy-Authorization", "Basic "+credentials)
	}
	if err := req.Write(conn); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("下载代理拒绝建立到源站的连接")
	}
	_ = conn.SetDeadline(time.Time{})
	ok = true
	return &copyURLTunnelConn{Conn: conn, reader: reader}, nil
}

type copyURLTunnelConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *copyURLTunnelConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

// copyURLSource streams a checked HTTP response to rclone over an unguessable
// loopback URL. rclone never resolves or follows an untrusted source URL itself.
type copyURLSource struct {
	URL    string
	server *http.Server
	cancel context.CancelFunc
	mu     sync.Mutex
	err    error
}

func newCopyURLSource(ctx context.Context, rawURL string, transport http.RoundTripper) (*copyURLSource, error) {
	if err := validateCopyURLTarget(rawURL); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	source := &copyURLSource{cancel: cancel}
	tokenPath := "/" + rand.Text()
	source.URL = "http://" + listener.Addr().String() + tokenPath
	client := &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("下载链接重定向次数过多")
			}
			return validateCopyURLTarget(req.URL.String())
		},
	}
	source.server = &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != tokenPath {
				http.NotFound(w, r)
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			req, err := http.NewRequestWithContext(r.Context(), r.Method, rawURL, nil)
			if err != nil {
				http.Error(w, "下载链接无效", http.StatusBadRequest)
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				source.mu.Lock()
				if errors.Is(err, errCopyURLBlocked) {
					source.err = errCopyURLBlocked
				} else {
					source.err = errors.New("下载链接访问失败，请检查链接、网络与代理")
				}
				source.mu.Unlock()
				http.Error(w, "下载链接访问失败", http.StatusBadGateway)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				http.Error(w, "下载链接返回错误", resp.StatusCode)
				return
			}
			for _, key := range []string{"Content-Type", "Content-Length", "Last-Modified"} {
				if value := resp.Header.Get(key); value != "" {
					w.Header().Set(key, value)
				}
			}
			name := ""
			if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil {
				name = params["filename"]
			}
			if name == "" {
				name = path.Base(resp.Request.URL.Path)
			}
			name = path.Base(strings.ReplaceAll(name, `\`, "/"))
			if name == "." || name == ".." || name == "/" || name == "" || strings.ContainsAny(name, "\r\n\x00") {
				name = "download"
			}
			w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
			w.WriteHeader(resp.StatusCode)
			if r.Method != http.MethodHead {
				if _, err := io.Copy(w, resp.Body); err != nil {
					// Abort the HTTP stream so rclone cannot accept a truncated body
					// as a completed download when Content-Length is unknown.
					panic(http.ErrAbortHandler)
				}
			}
		}),
	}
	go source.server.Serve(listener)
	return source, nil
}

func (s *copyURLSource) Close() {
	s.cancel()
	_ = s.server.Close()
}

func (s *copyURLSource) Error() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
