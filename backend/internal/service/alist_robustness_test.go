package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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

	"opensync/internal/config"
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
	oldCfg := config.GetConfig()
	config.SetConfigForTest(&config.Config{Server: config.ServerConfig{PasswdStr: "test-secret"}})
	t.Cleanup(func() { config.SetConfigForTest(oldCfg) })

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

func TestCreateEmptyFileContextUsesPutWithoutSourceDownload(t *testing.T) {
	handlerErr := make(chan string, 1)
	failHandler := func(w http.ResponseWriter, format string, args ...interface{}) {
		select {
		case handlerErr <- fmt.Sprintf(format, args...):
		default:
		}
		w.WriteHeader(http.StatusBadRequest)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/fs/put" {
			failHandler(w, "request = %s %s, want PUT /api/fs/put", r.Method, r.URL.Path)
			return
		}
		if got := r.Header.Get("File-Path"); got != "%2Fdst%2F%E7%A9%BA@zod.txt" {
			failHandler(w, "File-Path = %q, want escaped destination path", got)
			return
		}
		if got := r.Header.Get("Content-Type"); got != "application/octet-stream" {
			failHandler(w, "Content-Type = %q, want application/octet-stream", got)
			return
		}
		if got := r.Header.Get("Overwrite"); got != "true" {
			failHandler(w, "Overwrite = %q, want true", got)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			failHandler(w, "read request body: %v", err)
			return
		}
		if len(body) != 0 {
			failHandler(w, "request body length = %d, want 0", len(body))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":200,"message":"success","data":null}`)
	}))
	defer server.Close()

	client := &AlistClient{URL: server.URL, client: server.Client()}
	if err := client.CreateEmptyFileContext(context.Background(), "/dst/", "空@zod.txt"); err != nil {
		t.Fatalf("CreateEmptyFileContext() error: %v", err)
	}
	select {
	case msg := <-handlerErr:
		t.Fatal(msg)
	default:
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
	if got := newAlistRoundTripper().(*protocolTripper).h12SlowMeta.(*http.Transport).ResponseHeaderTimeout; got != alistSlowMetaResponseHeaderTimeout {
		t.Fatalf("slow-meta transport ResponseHeaderTimeout = %v, want %v", got, alistSlowMetaResponseHeaderTimeout)
	}
}

func TestProtocolTripperRoutesSlowMetaOperationsToSlowMetaTransport(t *testing.T) {
	var shortHits, slowMetaHits, longHits atomic.Int32
	tripper := newProtocolTripper(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		shortHits.Add(1)
		return jsonResponse(r, `{"code":200,"message":"success","data":{"total":0,"content":[]}}`), nil
	}), nil)
	tripper.h12SlowMeta = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		slowMetaHits.Add(1)
		if r.URL.Path == "/api/fs/list" {
			return jsonResponse(r, `{"code":200,"message":"success","data":{"total":0,"content":[]}}`), nil
		}
		return jsonResponse(r, `{"code":200,"message":"success","data":{}}`), nil
	})
	tripper.h12Long = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		longHits.Add(1)
		return jsonResponse(r, `{"code":200,"message":"success","data":{"tasks":[]}}`), nil
	})

	// Explicit long operation takes precedence over slow-meta if both are present.
	bothCtx := withAlistLongOperation(withAlistSlowMetaOperation(context.Background()))
	req, _ := http.NewRequestWithContext(bothCtx, http.MethodPost, "http://alist.test/api/fs/copy", nil)
	resp, err := tripper.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip(both) error: %v", err)
	}
	closeBody(resp.Body)

	client := &AlistClient{
		URL:        "http://alist.test",
		client:     &http.Client{Transport: tripper},
		longClient: &http.Client{Transport: tripper},
	}
	if _, err := client.FileListApiContext(context.Background(), "/quark/photos", 0, 0); err != nil {
		t.Fatalf("FileListApiContext() error: %v", err)
	}
	if err := client.MkdirContext(context.Background(), "/quark/photos/2026", 0); err != nil {
		t.Fatalf("MkdirContext() error: %v", err)
	}
	if _, err := client.FileStatContext(context.Background(), "/quark/photos", "a.jpg"); err != nil {
		t.Fatalf("FileStatContext() error: %v", err)
	}

	if longHits.Load() != 1 || slowMetaHits.Load() != 2 || shortHits.Load() != 1 {
		t.Fatalf("long/slowMeta/short hits = %d/%d/%d, want 1/2/1", longHits.Load(), slowMetaHits.Load(), shortHits.Load())
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

// Only read-only requests and non-mutating API paths may be transparently
// replayed after an HTTP/3 transport failure. Replaying copy/move/remove (or a
// task admin action) could execute the server-side operation twice.
func TestIsReplayableAlistRequest(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodGet, "/api/fs/list", true},
		{http.MethodHead, "/api/fs/get", true},
		{http.MethodPost, "/api/fs/list", true},
		{http.MethodPost, "/api/fs/get", true},
		{http.MethodPost, "/api/fs/copy", false},
		{http.MethodPost, "/api/fs/move", false},
		{http.MethodPost, "/api/fs/remove", false},
		{http.MethodPost, "/api/fs/mkdir", false},
		{http.MethodPost, "/alist/api/fs/copy", false},
		{http.MethodPost, "/alist/api/fs/move", false},
		{http.MethodPost, "/alist/api/fs/remove", false},
		{http.MethodPost, "/alist/api/fs/mkdir", false},
		{http.MethodPost, "/alist/api/fs/list", true},
		{http.MethodPost, "/alist/api/fs/get", true},
		{http.MethodPut, "/alist/api/fs/put", false},
		{http.MethodPost, "/api/admin/task/copy/cancel", false},
		{http.MethodPost, "/alist/api/admin/task/copy/cancel", false},
		{http.MethodPost, "/api/admin/task/move/info", false},
	}
	for _, tc := range cases {
		req, err := http.NewRequest(tc.method, "https://nas.example"+tc.path, nil)
		if err != nil {
			t.Fatalf("NewRequest(%s %s) error: %v", tc.method, tc.path, err)
		}
		if got := isReplayableAlistRequest(req); got != tc.want {
			t.Errorf("isReplayableAlistRequest(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

// A failed HTTP/3 attempt must not blacklist the host forever: the mark expires
// so a transient QUIC blip does not disable HTTP/3 for the cached client's
// lifetime.
func TestProtocolTripperHTTP3FailureExpires(t *testing.T) {
	now := time.Unix(1700000000, 0)
	tripper := newProtocolTripper(
		roundTripFunc(func(r *http.Request) (*http.Response, error) { return jsonResponse(r, `{}`), nil }),
		roundTripFunc(func(r *http.Request) (*http.Response, error) { return jsonResponse(r, `{}`), nil }),
	)
	tripper.now = func() time.Time { return now }
	tripper.markHTTP3("nas.example")
	if !tripper.preferHTTP3("nas.example") {
		t.Fatal("HTTP/3 not preferred despite a valid Alt-Svc mark")
	}

	tripper.markHTTP3Failed("nas.example")
	if tripper.preferHTTP3("nas.example") {
		t.Fatal("HTTP/3 still preferred right after a failure")
	}
	tripper.markHTTP3("nas.example")
	if tripper.preferHTTP3("nas.example") {
		t.Fatal("HTTP/3 re-enabled before the failure mark expired")
	}

	now = now.Add(h3FailedTTL + time.Second)
	tripper.markHTTP3("nas.example")
	if !tripper.preferHTTP3("nas.example") {
		t.Fatal("HTTP/3 stayed disabled after the failure mark expired")
	}
}

func TestAlistHTTP3TransportSetsLongIdleTimeout(t *testing.T) {
	tr := newAlistHTTP3Transport()
	if tr.QUICConfig == nil {
		t.Fatal("QUICConfig = nil")
	}
	if tr.QUICConfig.MaxIdleTimeout < alistLongOpClientTimeout {
		t.Fatalf("MaxIdleTimeout = %v, want >= %v", tr.QUICConfig.MaxIdleTimeout, alistLongOpClientTimeout)
	}
}

func TestProtocolTripperBypassesHTTP3WhenProxyApplies(t *testing.T) {
	var h12Calls, h3Calls atomic.Int64
	h12 := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		h12Calls.Add(1)
		header := make(http.Header)
		header.Set("Alt-Svc", `h3=":443"`)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Request:    req,
		}, nil
	})
	h3 := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		h3Calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Request:    req,
		}, nil
	})
	tripper := newProtocolTripper(h12, h3)
	tripper.proxyForRequest = func(req *http.Request) (*url.URL, error) {
		return url.Parse("http://proxy.example:8080")
	}
	tripper.markHTTP3("example.com")

	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest(http.MethodGet, "https://example.com/api/me", nil)
		resp, err := tripper.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip() error: %v", err)
		}
		_ = resp.Body.Close()
	}
	if h3Calls.Load() != 0 {
		t.Fatalf("h3Calls = %d, want 0 when proxy applies", h3Calls.Load())
	}
	if h12Calls.Load() != 2 {
		t.Fatalf("h12Calls = %d, want 2", h12Calls.Load())
	}
}

func TestAlistCheckRedirectPolicy(t *testing.T) {
	client, longClient := newAlistHTTPClients()
	if client.CheckRedirect == nil || longClient.CheckRedirect == nil {
		t.Fatal("newAlistHTTPClients() must install CheckRedirect on both clients")
	}

	oldLookup := alistRedirectLookupIPAddr
	alistRedirectLookupIPAddr = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		switch host {
		case "public.example":
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		case "rebind-loopback.example":
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		case "rebind-metadata.example":
			return []net.IPAddr{{IP: net.ParseIP("169.254.169.254")}}, nil
		default:
			return nil, errors.New("unknown host")
		}
	}
	t.Cleanup(func() { alistRedirectLookupIPAddr = oldLookup })

	makeReq := func(method, rawURL string) *http.Request {
		req, err := http.NewRequest(method, rawURL, nil)
		if err != nil {
			t.Fatalf("NewRequest(%s %s) error: %v", method, rawURL, err)
		}
		req.Header.Set("Authorization", "secret-token")
		return req
	}

	// 1. HTTPS -> HTTP downgrade blocked.
	if err := alistCheckRedirect(makeReq(http.MethodGet, "http://alist.example/api/me"), []*http.Request{
		makeReq(http.MethodGet, "https://alist.example/api/me"),
	}); err == nil {
		t.Fatal("HTTPS -> HTTP downgrade was allowed")
	}

	// 2. Non-GET/HEAD redirects blocked.
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		if err := alistCheckRedirect(makeReq(http.MethodGet, "https://alist.example/next"), []*http.Request{
			makeReq(method, "https://alist.example/api/fs/copy"),
		}); err == nil {
			t.Fatalf("%s -> GET redirect was allowed", method)
		}
		if err := alistCheckRedirect(makeReq(method, "https://alist.example/next"), []*http.Request{
			makeReq(method, "https://alist.example/api/fs/copy"),
		}); err == nil {
			t.Fatalf("%s -> %s redirect was allowed", method, method)
		}
	}

	// 3. Cross-host redirect strips Authorization, Cookie, and Proxy-Authorization.
	crossReq := makeReq(http.MethodGet, "https://public.example/api/me")
	crossReq.Header.Set("Cookie", "sid=abc")
	crossReq.Header.Set("Proxy-Authorization", "Basic secret")
	if err := alistCheckRedirect(crossReq, []*http.Request{
		makeReq(http.MethodGet, "https://alist.example/api/me"),
	}); err != nil {
		t.Fatalf("cross-host public redirect error = %v, want allowed without Authorization", err)
	}
	for _, hdr := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if got := crossReq.Header.Get(hdr); got != "" {
			t.Fatalf("%s leaked across hosts: %q", hdr, got)
		}
	}

	// 3b. Unsupported scheme blocked.
	if err := alistCheckRedirect(makeReq(http.MethodGet, "file:///etc/passwd"), []*http.Request{
		makeReq(http.MethodGet, "https://alist.example/api/me"),
	}); err == nil {
		t.Fatal("file:// redirect was allowed")
	}

	// 4. Private / loopback / metadata SSRF redirects blocked.
	for _, target := range []string{
		"http://127.0.0.1:8080/admin",
		"http://localhost:5244/api/me",
		"http://localhost.:5244/api/me",
		"http://10.0.0.1/secret",
		"http://192.168.1.1/secret",
		"http://169.254.169.254/latest/meta-data",
		"http://[::1]:8080/",
		"https://rebind-loopback.example/api/me",
		"https://rebind-metadata.example/api/me",
	} {
		if err := alistCheckRedirect(makeReq(http.MethodGet, target), []*http.Request{
			makeReq(http.MethodGet, "http://127.0.0.1:5244/api/me"),
		}); err == nil {
			t.Fatalf("SSRF redirect to %q was allowed", target)
		}
	}

	// 5. Same-origin GET redirect allowed and keeps Authorization.
	sameReq := makeReq(http.MethodGet, "http://127.0.0.1:5244/v2/api/me")
	if err := alistCheckRedirect(sameReq, []*http.Request{
		makeReq(http.MethodGet, "http://127.0.0.1:5244/api/me"),
	}); err != nil {
		t.Fatalf("same-origin GET redirect error = %v", err)
	}
	if got := sameReq.Header.Get("Authorization"); got != "secret-token" {
		t.Fatalf("same-origin redirect stripped Authorization: %q", got)
	}
}
