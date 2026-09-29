package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"opensync/internal/mapper"
)

func TestDeleteFileContextRejectsUnsafeNamesWithoutRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":null}`))
	}))
	defer server.Close()
	client := &AlistClient{URL: server.URL, client: server.Client()}

	cases := []struct {
		dir   string
		names []string
	}{
		{"/dst", nil},
		{"/dst", []string{}},
		{"/dst", []string{""}},
		{"/dst", []string{"/"}},
		{"/dst", []string{"."}},
		{"/dst", []string{".."}},
		{"/dst", []string{"ok.txt", "../escape"}},
		{"/dst", []string{"a/b"}},
		{"/dst", []string{`a\b`}},
		{"", []string{"file.txt"}},
	}
	for _, tc := range cases {
		if err := client.DeleteFileContext(context.Background(), tc.dir, tc.names, 0); err == nil {
			t.Fatalf("DeleteFileContext(%q, %q) error = nil, want rejection", tc.dir, tc.names)
		}
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("server received %d requests, want 0 for rejected removes", got)
	}

	// A normal file and a directory in listing form ("sub/") are allowed.
	if err := client.DeleteFileContext(context.Background(), "/dst", []string{"file.txt", "sub/"}, 0); err != nil {
		t.Fatalf("DeleteFileContext(valid) error: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("server received %d requests, want 1", got)
	}
}

func TestFileGetContextKeepsNameWhitespace(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body alistPathRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotPath = body.Path
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{"name":" a.txt ","size":7,"is_dir":false}}`))
	}))
	defer server.Close()
	client := &AlistClient{URL: server.URL, client: server.Client()}

	stat, err := client.FileStatContext(context.Background(), "/dst//", " a.txt ")
	if err != nil {
		t.Fatalf("FileStatContext() error: %v", err)
	}
	if gotPath != "/dst/ a.txt " {
		t.Fatalf("path = %q, want name whitespace preserved", gotPath)
	}
	if !stat.Exists || !stat.SizeKnown || stat.Size != 7 {
		t.Fatalf("stat = %+v, want exists with size 7", stat)
	}
}

func TestFileStatContextReportsMissingAndUnknownSize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body alistPathRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.HasSuffix(body.Path, "missing") {
			_, _ = w.Write([]byte(`{"code":500,"message":"object not found","data":null}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":200,"message":"ok","data":{}}`))
	}))
	defer server.Close()
	client := &AlistClient{URL: server.URL, client: server.Client()}

	stat, err := client.FileStatContext(context.Background(), "/d", "missing")
	if err != nil || stat.Exists {
		t.Fatalf("missing stat = %+v, %v; want not exists, nil", stat, err)
	}
	stat, err = client.FileStatContext(context.Background(), "/d", "present")
	if err != nil || !stat.Exists || stat.SizeKnown {
		t.Fatalf("present stat = %+v, %v; want exists with unknown size", stat, err)
	}
}

func TestUpdateClientBadTokenKeepsWorkingCachedClient(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open() error: %v", err)
	}
	defer testDB.Close()
	if _, err := testDB.Exec(`CREATE TABLE alist_list(
		id integer primary key autoincrement, remark text, url text UNIQUE, userName text, token text)`); err != nil {
		t.Fatalf("create alist_list: %v", err)
	}
	restoreDB := mapper.SetDBForTest(testDB)
	defer restoreDB()

	// /api/me rejects the new token with AList's business 401.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":401,"message":"token is invalidated","data":null}`))
	}))
	defer server.Close()
	if _, err := testDB.Exec("INSERT INTO alist_list(id, remark, url, userName, token) VALUES (5, '', ?, 'u', 'old')", server.URL); err != nil {
		t.Fatalf("insert alist: %v", err)
	}

	oldClient := &AlistClient{AlistID: 5, URL: server.URL}
	alistClientListMu.Lock()
	previousList := alistClientList
	alistClientList = map[int64]*AlistClient{5: oldClient}
	alistClientListMu.Unlock()
	defer func() {
		alistClientListMu.Lock()
		alistClientList = previousList
		alistClientListMu.Unlock()
	}()

	err = UpdateClient(map[string]interface{}{"id": int64(5), "url": server.URL, "token": "bad-token"})
	if err == nil {
		t.Fatal("UpdateClient() error = nil, want validation failure")
	}
	alistClientListMu.RLock()
	cached := alistClientList[5]
	alistClientListMu.RUnlock()
	if cached != oldClient {
		t.Fatal("failed token validation evicted the working cached client")
	}
}

func TestCopyMoveUseLongOperationClient(t *testing.T) {
	var shortHits, longHits atomic.Int32
	short := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		shortHits.Add(1)
		return jsonResponse(r, `{"code":200,"message":"ok","data":{"tasks":[]}}`), nil
	})}
	long := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		longHits.Add(1)
		if !isAlistLongOperation(r.Context()) {
			t.Errorf("long client request for %s missing long-operation marker", r.URL.Path)
		}
		return jsonResponse(r, `{"code":200,"message":"ok","data":{"tasks":[{"id":"t1"}]}}`), nil
	})}
	client := &AlistClient{URL: "http://alist.test", client: short, longClient: long}

	if id, err := client.CopyFileContext(context.Background(), "/a", "/b", "f"); err != nil || id != "t1" {
		t.Fatalf("CopyFileContext() = %q, %v", id, err)
	}
	if _, err := client.MoveFileContext(context.Background(), "/a", "/b", "f"); err != nil {
		t.Fatalf("MoveFileContext() error: %v", err)
	}
	if _, err := client.FileStatContext(context.Background(), "/a", "f"); err != nil {
		t.Fatalf("FileStatContext() error: %v", err)
	}
	if longHits.Load() != 2 || shortHits.Load() != 1 {
		t.Fatalf("long/short hits = %d/%d, want 2/1", longHits.Load(), shortHits.Load())
	}
}

func TestProtocolTripperRoutesLongOperationsToLongTransport(t *testing.T) {
	var shortHits, longHits atomic.Int32
	tripper := newProtocolTripper(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		shortHits.Add(1)
		return jsonResponse(r, `{}`), nil
	}), nil)
	tripper.h12Long = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		longHits.Add(1)
		return jsonResponse(r, `{}`), nil
	})

	req, _ := http.NewRequestWithContext(withAlistLongOperation(context.Background()), http.MethodPost, "http://alist.test/api/fs/copy", nil)
	resp, err := tripper.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip() error: %v", err)
	}
	closeBody(resp.Body)
	req, _ = http.NewRequest(http.MethodPost, "http://alist.test/api/fs/list", nil)
	resp, err = tripper.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip() error: %v", err)
	}
	closeBody(resp.Body)
	if longHits.Load() != 1 || shortHits.Load() != 1 {
		t.Fatalf("long/short hits = %d/%d, want 1/1", longHits.Load(), shortHits.Load())
	}
	if got := newAlistRoundTripper().(*protocolTripper).h12Long.(*http.Transport).ResponseHeaderTimeout; got != alistLongOpResponseHeaderTimeout {
		t.Fatalf("long transport ResponseHeaderTimeout = %v, want %v", got, alistLongOpResponseHeaderTimeout)
	}
}
func TestParseAltSvcHTTP3(t *testing.T) {
	u443, _ := url.Parse("https://nas.example:443/api")
	uDefault, _ := url.Parse("https://nas.example/api")
	u5244, _ := url.Parse("https://nas.example:5244/api")
	cases := []struct {
		name        string
		header      string
		u           *url.URL
		wantOffered bool
		wantCleared bool
	}{
		{"same port", `h3=":443"; ma=86400`, u443, true, false},
		{"implicit default port", `h3=":443"`, uDefault, true, false},
		{"other port", `h3=":443"`, u5244, false, false},
		{"matching custom port", `h3=":5244"; ma=3600, h2=":443"`, u5244, true, false},
		{"multiple entries", `h2=":443", h3=":8443", h3=":5244"`, u5244, true, false},
		{"other host", `h3="cdn.example:5244"`, u5244, false, false},
		{"same host", `h3="nas.example:5244"`, u5244, true, false},
		{"draft only", `h3-29=":5244"`, u5244, false, false},
		{"clear", `clear`, u5244, false, true},
		{"quoted comma", `h3=":5244"; foo="a,b", h2=":1"`, u5244, true, false},
		{"empty", ``, u5244, false, false},
	}
	for _, tc := range cases {
		offered, cleared := parseAltSvcHTTP3([]string{tc.header}, tc.u)
		if offered != tc.wantOffered || cleared != tc.wantCleared {
			t.Fatalf("%s: parseAltSvcHTTP3(%q) = %v/%v, want %v/%v", tc.name, tc.header, offered, cleared, tc.wantOffered, tc.wantCleared)
		}
	}
}

func TestProtocolTripperAltSvcClearDisablesHTTP3(t *testing.T) {
	header := `h3=":443"`
	h12 := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp := jsonResponse(r, `{}`)
		resp.Header.Set("Alt-Svc", header)
		return resp, nil
	})
	tripper := newProtocolTripper(h12, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(r, `{}`), nil
	}))
	req, _ := http.NewRequest(http.MethodGet, "https://nas.example/api/me", nil)
	resp, _ := tripper.RoundTrip(req)
	closeBody(resp.Body)
	if !tripper.preferHTTP3("nas.example") {
		t.Fatal("HTTP/3 not enabled after matching Alt-Svc")
	}
	tripper.unmarkHTTP3("nas.example")
	header = "clear"
	resp, _ = tripper.RoundTrip(req)
	closeBody(resp.Body)
	if tripper.preferHTTP3("nas.example") {
		t.Fatal("HTTP/3 still preferred after Alt-Svc: clear")
	}
}

func newHTTP3PreferredTripper(h12, h3 http.RoundTripper) *protocolTripper {
	tripper := newProtocolTripper(h12, h3)
	tripper.h3Hosts["nas.example"] = struct{}{}
	return tripper
}

func TestProtocolTripperContextCancelDoesNotMarkHTTP3Failed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h3 := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		cancel()
		return nil, context.Canceled
	})
	tripper := newHTTP3PreferredTripper(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatal("h12 must not be used after cancellation")
		return nil, nil
	}), h3)

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://nas.example/api/me", nil)
	if _, err := tripper.RoundTrip(req); err == nil {
		t.Fatal("RoundTrip() error = nil, want cancellation")
	}
	if _, failed := tripper.h3Failed["nas.example"]; failed {
		t.Fatal("user cancellation blacklisted HTTP/3")
	}
	if !tripper.preferHTTP3("nas.example") {
		t.Fatal("HTTP/3 preference dropped after cancellation")
	}
}

func TestProtocolTripperReplaysMutatingRequestAfterPreSendHandshakeFailure(t *testing.T) {
	var h12Bodies []string
	h12 := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		h12Bodies = append(h12Bodies, string(body))
		return jsonResponse(r, `{}`), nil
	})
	h3 := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, &quic.HandshakeTimeoutError{}
	})
	tripper := newHTTP3PreferredTripper(h12, h3)

	req, _ := http.NewRequest(http.MethodPost, "https://nas.example/api/fs/move", strings.NewReader(`{"names":["f"]}`))
	resp, err := tripper.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip() error: %v, want fallback to HTTP/2", err)
	}
	closeBody(resp.Body)
	if len(h12Bodies) != 1 || h12Bodies[0] != `{"names":["f"]}` {
		t.Fatalf("h12 bodies = %q, want one replay with the original body", h12Bodies)
	}
	if _, failed := tripper.h3Failed["nas.example"]; !failed {
		t.Fatal("handshake failure did not mark HTTP/3 failed")
	}
}

func TestProtocolTripperDoesNotReplayMutatingRequestAfterHeadersSent(t *testing.T) {
	h12Calls := 0
	h12 := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		h12Calls++
		return jsonResponse(r, `{}`), nil
	})
	h3 := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if trace := httptraceFrom(r); trace != nil && trace.WroteHeaders != nil {
			trace.WroteHeaders()
		}
		return nil, &net.OpError{Op: "read", Err: errors.New("connection reset")}
	})
	tripper := newHTTP3PreferredTripper(h12, h3)

	req, _ := http.NewRequest(http.MethodPost, "https://nas.example/api/fs/copy", strings.NewReader(`{}`))
	if _, err := tripper.RoundTrip(req); err == nil {
		t.Fatal("RoundTrip() error = nil, want the HTTP/3 error surfaced")
	}
	if h12Calls != 0 {
		t.Fatalf("h12 calls = %d, want 0: request may already have run", h12Calls)
	}
}

func TestIsHTTP3PreSendError(t *testing.T) {
	if !isHTTP3PreSendError(&quic.IdleTimeoutError{}, false) {
		t.Fatal("idle timeout before headers must be pre-send")
	}
	if !isHTTP3PreSendError(&net.OpError{Op: "dial", Err: errors.New("no route")}, false) {
		t.Fatal("dial error must be pre-send")
	}
	if isHTTP3PreSendError(&quic.HandshakeTimeoutError{}, true) {
		t.Fatal("any error after headers were written is not pre-send")
	}
	if isHTTP3PreSendError(errors.New("stream reset"), false) {
		t.Fatal("unknown errors must not be treated as pre-send")
	}
}

type closeCountingRoundTripper struct {
	roundTripFunc
	closes atomic.Int32
}

func (c *closeCountingRoundTripper) Close() error {
	c.closes.Add(1)
	return nil
}

func TestProtocolTripperCloseDefersHTTP3CloseUntilInFlightDone(t *testing.T) {
	h3 := &closeCountingRoundTripper{roundTripFunc: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(r, `{"code":200}`), nil
	}}
	tripper := newHTTP3PreferredTripper(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(r, `{}`), nil
	}), h3)

	req, _ := http.NewRequest(http.MethodGet, "https://nas.example/api/me", nil)
	resp, err := tripper.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip() error: %v", err)
	}
	_ = tripper.Close()
	if got := h3.closes.Load(); got != 0 {
		t.Fatalf("h3 closed %d times while a response body was open", got)
	}
	closeBody(resp.Body)
	_ = resp.Body.Close() // a second Close must not release twice
	if got := h3.closes.Load(); got != 1 {
		t.Fatalf("h3 closes = %d, want 1 after the last body closed", got)
	}
	_ = tripper.Close()
	if got := h3.closes.Load(); got != 1 {
		t.Fatalf("h3 closes = %d, want 1 (idempotent)", got)
	}
	if tripper.preferHTTP3("nas.example") {
		t.Fatal("closed tripper must not start new HTTP/3 requests")
	}
}

func TestProtocolTripperCloseConcurrentWithRequests(t *testing.T) {
	h3 := &closeCountingRoundTripper{roundTripFunc: func(r *http.Request) (*http.Response, error) {
		return jsonResponse(r, `{}`), nil
	}}
	tripper := newHTTP3PreferredTripper(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(r, `{}`), nil
	}), h3)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, "https://nas.example/api/me", nil)
			if resp, err := tripper.RoundTrip(req); err == nil {
				time.Sleep(time.Millisecond)
				closeBody(resp.Body)
			}
		}()
	}
	_ = tripper.Close()
	wg.Wait()
	if got := h3.closes.Load(); got != 1 {
		t.Fatalf("h3 closes = %d, want exactly 1", got)
	}
}

func TestAlistClientCloseReleasesRealHTTP3Transport(t *testing.T) {
	client, _ := newAlistHTTPClients()
	c := &AlistClient{client: client}
	c.Close()
	tripper := client.Transport.(*protocolTripper)
	tripper.inflightMu.Lock()
	closed := tripper.h3Closed
	tripper.inflightMu.Unlock()
	if !closed {
		t.Fatal("Close() with no in-flight requests did not close the HTTP/3 transport")
	}
}

func TestCloseBodyDrainsBeforeClose(t *testing.T) {
	body := &trackingBody{Reader: strings.NewReader(strings.Repeat("x", 1000))}
	closeBody(body)
	if !body.closed || body.read != 1000 {
		t.Fatalf("closeBody read=%d closed=%v, want 1000/true", body.read, body.closed)
	}
	big := &trackingBody{Reader: strings.NewReader(strings.Repeat("x", maxDrainBytes*2))}
	closeBody(big)
	if big.read != maxDrainBytes {
		t.Fatalf("closeBody drained %d bytes, want cap %d", big.read, maxDrainBytes)
	}
	closeBody(nil)
}

func TestCopyQueueCloseWakesAllBlockedProducers(t *testing.T) {
	q := newCopyQueueWithCapacity(1)
	if !q.pushWait(context.Background(), &CopyItem{}) {
		t.Fatal("first push failed")
	}
	const producers = 5
	results := make(chan bool, producers)
	for i := 0; i < producers; i++ {
		go func() { results <- q.pushWait(context.Background(), &CopyItem{}) }()
	}
	time.Sleep(20 * time.Millisecond)
	q.closeAndDrain()
	for i := 0; i < producers; i++ {
		select {
		case ok := <-results:
			if ok {
				t.Fatal("pushWait succeeded on a closed queue")
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("producer %d still blocked after closeAndDrain", i)
		}
	}
	q.closeAndDrain() // idempotent, must not panic on double close
}

type trackingBody struct {
	io.Reader
	read   int
	closed bool
}

func (b *trackingBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}

func (b *trackingBody) Close() error {
	b.closed = true
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(r *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}
}

func httptraceFrom(r *http.Request) *httptrace.ClientTrace {
	return httptrace.ContextClientTrace(r.Context())
}
