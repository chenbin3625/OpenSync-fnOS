package service

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

const (
	alistHTTP3HandshakeTimeout = 2 * time.Second
	alistResponseHeaderTimeout = 30 * time.Second
	// alistLongOpResponseHeaderTimeout covers /api/fs/copy and /api/fs/move.
	// Same-storage copy/move and synchronous drivers finish the whole transfer
	// before answering. With the regular 30s header timeout the client gave up
	// on operations that were still running (or had already succeeded) and the
	// retry loop resubmitted a non-idempotent request.
	alistLongOpResponseHeaderTimeout = 5 * time.Minute
	alistClientTimeout               = 300 * time.Second
	alistLongOpClientTimeout         = alistLongOpResponseHeaderTimeout + 30*time.Second
)

var errRequestBodyNotReplayable = errors.New("alist: request body cannot be retried")

type alistLongOpKey struct{}

// withAlistLongOperation marks ctx so protocolTripper routes the request
// through the HTTP/1.1-2 transport that has the long response-header timeout.
// ResponseHeaderTimeout is per-transport, so a context marker is the cheapest
// way to pick a transport per request while sharing Alt-Svc/HTTP/3 state.
func withAlistLongOperation(ctx context.Context) context.Context {
	return context.WithValue(ctx, alistLongOpKey{}, true)
}

func isAlistLongOperation(ctx context.Context) bool {
	v, _ := ctx.Value(alistLongOpKey{}).(bool)
	return v
}

// newAlistHTTPClients builds the outbound AList clients. HTTPS uses HTTP/2
// (ForceAttemptHTTP2 stays on even with a custom dialer / TLS config; Go
// otherwise silently falls back to HTTP/1.1 and the 6-connection cap). HTTP/3
// is opportunistic: only after the origin advertises Alt-Svc, so a LAN AList
// without QUIC does not pay a handshake timeout on every job.
//
// Both clients share one protocolTripper so Alt-Svc/HTTP/3 state and Close are
// common. The long client only differs in its overall timeout; the tripper
// sends its requests over the transport with the long header timeout.
func newAlistHTTPClients() (client *http.Client, longClient *http.Client) {
	tripper := newAlistRoundTripper()
	return &http.Client{Timeout: alistClientTimeout, Transport: tripper},
		&http.Client{Timeout: alistLongOpClientTimeout, Transport: tripper}
}

func newAlistRoundTripper() http.RoundTripper {
	t := newProtocolTripper(newAlistHTTPTransport(alistResponseHeaderTimeout), newAlistHTTP3Transport())
	t.h12Long = newAlistHTTPTransport(alistLongOpResponseHeaderTimeout)
	return t
}

func newAlistHTTPTransport(responseHeaderTimeout time.Duration) *http.Transport {
	dialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   64,
		MaxConnsPerHost:       64,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: responseHeaderTimeout,
		TLSHandshakeTimeout:   10 * time.Second,
		WriteBufferSize:       32 << 10,
		ReadBufferSize:        32 << 10,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"h2", "http/1.1"},
		},
	}
}

func newAlistHTTP3Transport() *http3.Transport {
	return &http3.Transport{
		QUICConfig: &quic.Config{HandshakeIdleTimeout: alistHTTP3HandshakeTimeout},
	}
}

func newProtocolTripper(h12 http.RoundTripper, h3 http.RoundTripper) *protocolTripper {
	return &protocolTripper{
		h12:      h12,
		h3:       h3,
		h3Hosts:  make(map[string]struct{}),
		h3Failed: make(map[string]struct{}),
	}
}

// protocolTripper sends HTTPS over HTTP/2 (or HTTP/1.1) until Alt-Svc names
// HTTP/3, then sticks to QUIC for that host. A failed HTTP/3 attempt is
// remembered so UDP-blocked NAS paths do not retry a 2s handshake forever.
type protocolTripper struct {
	h12 http.RoundTripper
	// h12Long is used for requests marked withAlistLongOperation. nil falls
	// back to h12 (tests, custom trippers).
	h12Long  http.RoundTripper
	h3       http.RoundTripper
	mu       sync.Mutex
	h3Hosts  map[string]struct{}
	h3Failed map[string]struct{}
	// inflight counts requests whose response body has not been closed yet.
	// Close defers http3.Transport.Close until it reaches zero, because that
	// call tears down active QUIC connections and would break a request that
	// another task is still streaming on a replaced/evicted client.
	inflightMu sync.Mutex
	inflight   int
	closeWant  bool
	h3Closed   bool
}

func (t *protocolTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	t.acquire()
	resp, err := t.roundTrip(req)
	if err != nil || resp == nil {
		t.release()
		return resp, err
	}
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	resp.Body = &releaseOnCloseBody{ReadCloser: resp.Body, release: t.release}
	return resp, nil
}

func (t *protocolTripper) roundTrip(req *http.Request) (*http.Response, error) {
	h12 := t.h12
	if t.h12Long != nil && isAlistLongOperation(req.Context()) {
		h12 = t.h12Long
	}
	if req.URL.Scheme == "https" && t.preferHTTP3(req.URL.Host) {
		var wroteHeaders atomic.Bool
		traced := req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{
			WroteHeaders: func() { wroteHeaders.Store(true) },
		}))
		resp, err := t.h3.RoundTrip(traced)
		if err == nil {
			return resp, nil
		}
		if req.Context().Err() != nil {
			// The caller cancelled (user stop, task timeout). That says nothing
			// about QUIC reachability, so HTTP/3 must not be blacklisted.
			return nil, err
		}
		t.markHTTP3Failed(req.URL.Host)
		if !isReplayableAlistRequest(req) && !isHTTP3PreSendError(err, wroteHeaders.Load()) {
			// Do not transparently resend a mutating request over a new
			// protocol: the server may have already executed it. h3Failed is
			// recorded, so the caller's bounded retry (or the next request)
			// goes over HTTP/1.1 or HTTP/2 instead.
			return nil, err
		}
		retry, retryErr := cloneHTTPRequest(req)
		if retryErr != nil {
			return nil, err
		}
		req = retry
	}

	resp, err := h12.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if req.URL.Scheme == "https" {
		offered, cleared := parseAltSvcHTTP3(resp.Header.Values("Alt-Svc"), req.URL)
		if offered {
			t.markHTTP3(req.URL.Host)
		} else if cleared {
			t.unmarkHTTP3(req.URL.Host)
		}
	}
	return resp, nil
}

// isHTTP3PreSendError reports whether an HTTP/3 failure provably happened
// before the request reached the server, so even a non-idempotent copy/move
// can be replayed once over HTTP/2 or HTTP/1.1. Both conditions are required:
// the request headers were never written to a QUIC stream (without headers
// the server cannot act on the request), and the error is a connection
// establishment failure rather than something that happened mid-exchange.
func isHTTP3PreSendError(err error, wroteHeaders bool) bool {
	if wroteHeaders || err == nil {
		return false
	}
	var handshakeTimeout *quic.HandshakeTimeoutError
	var idleTimeout *quic.IdleTimeoutError
	var versionErr *quic.VersionNegotiationError
	var transportErr *quic.TransportError
	var opErr *net.OpError
	var dnsErr *net.DNSError
	var addrErr *net.AddrError
	switch {
	case errors.As(err, &handshakeTimeout),
		errors.As(err, &idleTimeout),
		errors.As(err, &versionErr),
		errors.As(err, &transportErr),
		errors.As(err, &opErr),
		errors.As(err, &dnsErr),
		errors.As(err, &addrErr),
		errors.Is(err, http3.ErrTransportClosed),
		errors.Is(err, http3.ErrNoCachedConn):
		return true
	}
	return false
}

// releaseOnCloseBody releases the tripper's in-flight slot exactly once when
// the caller is done with the response body.
type releaseOnCloseBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (b *releaseOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.release)
	return err
}

func (t *protocolTripper) acquire() {
	t.inflightMu.Lock()
	t.inflight++
	t.inflightMu.Unlock()
}

func (t *protocolTripper) release() {
	t.inflightMu.Lock()
	t.inflight--
	closeNow := t.inflight == 0 && t.closeWant && !t.h3Closed
	if closeNow {
		t.h3Closed = true
	}
	t.inflightMu.Unlock()
	if closeNow {
		t.closeHTTP3()
	}
}

// closing reports whether Close was requested. Callers check it after
// acquire(): from then on the HTTP/3 transport cannot be closed under them.
func (t *protocolTripper) closing() bool {
	t.inflightMu.Lock()
	defer t.inflightMu.Unlock()
	return t.closeWant
}

// mutatingAlistPaths are AList endpoints whose transparent replay after an
// HTTP/3 transport failure could duplicate a server-side operation. The
// request may have been received before the connection failed. Everything else
// (including the POST-based read APIs such as /api/fs/list) is replayed, since
// re-running a read is harmless and keeps scans resilient to QUIC blips.
var mutatingAlistPaths = map[string]bool{
	"/api/fs/copy":   true,
	"/api/fs/move":   true,
	"/api/fs/remove": true,
}

func isReplayableAlistRequest(req *http.Request) bool {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	if strings.HasPrefix(req.URL.Path, "/api/admin/task/") {
		return false
	}
	return !mutatingAlistPaths[req.URL.Path]
}

func (t *protocolTripper) preferHTTP3(host string) bool {
	if t.h3 == nil || host == "" || t.closing() {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.h3Hosts[host]
	return ok
}

func (t *protocolTripper) markHTTP3(host string) {
	if t.h3 == nil || host == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, failed := t.h3Failed[host]; failed {
		return
	}
	if t.h3Hosts == nil {
		t.h3Hosts = make(map[string]struct{})
	}
	t.h3Hosts[host] = struct{}{}
}

// unmarkHTTP3 handles Alt-Svc: clear. Unlike markHTTP3Failed it does not
// blacklist the host, so a later advertisement can enable HTTP/3 again.
func (t *protocolTripper) unmarkHTTP3(host string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.h3Hosts, host)
}

const maxH3FailedEntries = 1000

func (t *protocolTripper) markHTTP3Failed(host string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.h3Hosts, host)
	if t.h3Failed == nil {
		t.h3Failed = make(map[string]struct{})
	}
	if len(t.h3Failed) >= maxH3FailedEntries {
		t.h3Failed = make(map[string]struct{})
	}
	t.h3Failed[host] = struct{}{}
}

func (t *protocolTripper) CloseIdleConnections() {
	for _, rt := range []http.RoundTripper{t.h12, t.h12Long, t.h3} {
		if closer, ok := rt.(interface{ CloseIdleConnections() }); ok {
			closer.CloseIdleConnections()
		}
	}
}

// Close releases idle connections now and closes the HTTP/3 transport (its UDP
// socket and read goroutine) once no request is in flight. Closing it
// immediately would kill active QUIC streams of other tasks still holding a
// replaced or evicted client; waiting for the last response body to close
// keeps those requests intact without leaking the socket forever.
func (t *protocolTripper) Close() error {
	t.CloseIdleConnections()
	t.inflightMu.Lock()
	t.closeWant = true
	closeNow := t.inflight == 0 && !t.h3Closed
	if closeNow {
		t.h3Closed = true
	}
	t.inflightMu.Unlock()
	if closeNow {
		t.closeHTTP3()
	}
	return nil
}

func (t *protocolTripper) closeHTTP3() {
	if closer, ok := t.h3.(io.Closer); ok {
		_ = closer.Close()
	}
}

func cloneHTTPRequest(req *http.Request) (*http.Request, error) {
	clone := req.Clone(req.Context())
	if req.Body == nil || req.Body == http.NoBody {
		return clone, nil
	}
	if req.GetBody == nil {
		return nil, errRequestBodyNotReplayable
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, err
	}
	clone.Body = body
	return clone, nil
}

// parseAltSvcHTTP3 parses Alt-Svc (RFC 7838) header values. offered is true
// when an "h3" alternative points at the same host and port the request used:
// http3.Transport dials the request URL's host:port over UDP, so an
// alternative on another port (h3=":8443") or another host would make every
// request pay a failing QUIC handshake. cleared is true for "Alt-Svc: clear".
// Parameters such as ma= / persist= are accepted and ignored.
func parseAltSvcHTTP3(values []string, reqURL *url.URL) (offered bool, cleared bool) {
	if reqURL == nil {
		return false, false
	}
	reqHost := strings.ToLower(reqURL.Hostname())
	reqPort := reqURL.Port()
	if reqPort == "" {
		reqPort = "443"
	}
	for _, value := range values {
		for _, entry := range splitAltSvcEntries(value) {
			if strings.EqualFold(entry, "clear") {
				cleared = true
				continue
			}
			alt := strings.TrimSpace(strings.SplitN(entry, ";", 2)[0])
			eq := strings.IndexByte(alt, '=')
			if eq <= 0 {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(alt[:eq]), "h3") {
				continue
			}
			authority := strings.TrimSpace(alt[eq+1:])
			if unquoted, err := strconv.Unquote(authority); err == nil {
				authority = unquoted
			} else {
				authority = strings.Trim(authority, `"`)
			}
			host, port, err := net.SplitHostPort(authority)
			if err != nil {
				// No port at all: treat the whole value as a host.
				host, port = authority, ""
			}
			if host != "" && !strings.EqualFold(host, reqHost) {
				continue
			}
			if port == "" || port == reqPort {
				offered = true
			}
		}
	}
	if cleared {
		// "clear" invalidates every alternative for the origin (RFC 7838 §3).
		return false, true
	}
	return offered, false
}

// splitAltSvcEntries splits an Alt-Svc value on commas that are outside
// quoted strings.
func splitAltSvcEntries(value string) []string {
	var entries []string
	inQuote := false
	escaped := false
	start := 0
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\' && inQuote:
			escaped = true
		case c == '"':
			inQuote = !inQuote
		case c == ',' && !inQuote:
			if entry := strings.TrimSpace(value[start:i]); entry != "" {
				entries = append(entries, entry)
			}
			start = i + 1
		}
	}
	if entry := strings.TrimSpace(value[start:]); entry != "" {
		entries = append(entries, entry)
	}
	return entries
}
