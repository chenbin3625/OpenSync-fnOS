package main

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// shortSocketDir keeps the socket path under the unix-socket length limit,
// which t.TempDir() on macOS can exceed.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "osync")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// A second instance must stop at the socket probe, before it can touch the
// database the running instance owns.
func TestOpenListenerRefusesWhenServiceAlreadyRunning(t *testing.T) {
	dir := shortSocketDir(t)
	first, publish, err := openListener(false, "", dir)
	if err != nil {
		t.Fatalf("first openListener() error: %v", err)
	}
	defer first.Close()
	defer publish.discard()
	if err := publish.commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	go func() {
		for {
			conn, err := first.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	second, _, err := openListener(false, "", dir)
	if !errors.Is(err, errServiceAlreadyRunning) {
		if second != nil {
			second.Close()
		}
		t.Fatalf("second openListener() error = %v, want errServiceAlreadyRunning", err)
	}
}

func TestOpenListenerReplacesStaleSocket(t *testing.T) {
	dir := shortSocketDir(t)
	socket := filepath.Join(dir, "app.sock")
	stale, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	// Leave the file behind the way a crashed process does.
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()

	listener, publish, err := openListener(false, "", dir)
	if err != nil {
		t.Fatalf("openListener() error: %v", err)
	}
	defer listener.Close()
	defer publish.discard()
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("stale socket still present before commit: %v", err)
	}
	if err := publish.commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	info, err := os.Stat(socket)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if info.Mode().Perm() != 0660 {
		t.Fatalf("socket mode = %v, want 0660", info.Mode().Perm())
	}
}

func TestOpenListenerRejectsNonSocketFile(t *testing.T) {
	dir := shortSocketDir(t)
	if err := os.WriteFile(filepath.Join(dir, "app.sock"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if listener, _, err := openListener(false, "", dir); err == nil {
		listener.Close()
		t.Fatal("openListener() accepted a regular file at the socket path")
	}
}

func TestOpenListenerDevModeUsesLoopbackTCP(t *testing.T) {
	listener, _, err := openListener(true, "0", "")
	if err != nil {
		t.Fatalf("openListener(dev) error: %v", err)
	}
	defer listener.Close()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !addr.IP.IsLoopback() {
		t.Fatalf("dev listener addr = %v, want loopback TCP", listener.Addr())
	}
}

func TestOpenListenerRequiresDestinationOutsideDevMode(t *testing.T) {
	if listener, _, err := openListener(false, "", ""); err == nil {
		listener.Close()
		t.Fatal("openListener() without TRIM_APPDEST succeeded")
	}
}

// The start script reports success as soon as app.sock appears, so the socket
// must stay private until initialisation finished, and a failed start must not
// leave anything that later looks like a running service.
func TestOpenListenerPublishesSocketOnlyOnCommit(t *testing.T) {
	dir := shortSocketDir(t)
	socket := filepath.Join(dir, "app.sock")
	listener, publish, err := openListener(false, "", dir)
	if err != nil {
		t.Fatalf("openListener() error: %v", err)
	}
	defer listener.Close()
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("app.sock exists before commit: %v", err)
	}
	if err := publish.commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		t.Fatalf("dial published socket: %v", err)
	}
	conn.Close()
	listener.Close()
	publish.discard()
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("app.sock left after discard: %v", err)
	}
}

func TestOpenListenerDiscardBeforeCommitLeavesNoSocket(t *testing.T) {
	dir := shortSocketDir(t)
	listener, publish, err := openListener(false, "", dir)
	if err != nil {
		t.Fatalf("openListener() error: %v", err)
	}
	listener.Close()
	publish.discard()
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		// The lock marker is intentionally persistent; the kernel releases the
		// flock when the process exits, and unlinking it would race a waiting
		// start.
		if entry.Name() == startupLockName {
			continue
		}
		t.Fatalf("file left after discard: %v", entry.Name())
	}
}

// A start that is still initialising owns only its private socket; a second
// start must see it as running rather than begin a concurrent migration.
func TestOpenListenerRefusesWhileAnotherStartIsInitialising(t *testing.T) {
	dir := shortSocketDir(t)
	pending := filepath.Join(dir, pendingSocketPrefix+"1")
	other, err := net.Listen("unix", pending)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer other.Close()
	go func() {
		for {
			conn, err := other.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()
	if listener, _, err := openListener(false, "", dir); !errors.Is(err, errServiceAlreadyRunning) {
		if listener != nil {
			listener.Close()
		}
		t.Fatalf("openListener() error = %v, want errServiceAlreadyRunning", err)
	}
}

// log.Fatal skips deferred cleanup, so a start that died during
// initialisation leaves its private socket behind; the next start removes it.
func TestOpenListenerRemovesDeadPendingSocket(t *testing.T) {
	dir := shortSocketDir(t)
	pending := filepath.Join(dir, pendingSocketPrefix+"1")
	dead, err := net.Listen("unix", pending)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	dead.(*net.UnixListener).SetUnlinkOnClose(false)
	dead.Close()
	listener, publish, err := openListener(false, "", dir)
	if err != nil {
		t.Fatalf("openListener() error: %v", err)
	}
	defer listener.Close()
	defer publish.discard()
	if _, err := os.Lstat(pending); !os.IsNotExist(err) {
		t.Fatalf("dead pending socket not removed: %v", err)
	}
}

// New requests must be refused before jobs stop (a POST /job in between used to
// start a job just before the database closed), and notifications drain last.
func TestGracefulShutdownOrder(t *testing.T) {
	var mu sync.Mutex
	var order []string
	record := func(step string) {
		mu.Lock()
		order = append(order, step)
		mu.Unlock()
	}
	gracefulShutdown(shutdownSteps{
		cancelRequests: func() { record("cancelRequests") },
		stopServer:     func(context.Context) error { record("stopServer"); return nil },
		closeServer:    func() error { record("closeServer"); return nil },
		stopJobs:       func(context.Context) { record("stopJobs") },
		stopNotify:     func(context.Context) { record("stopNotify") },
	}, time.Second, 100*time.Millisecond)

	want := []string{"cancelRequests", "stopServer", "stopJobs", "stopNotify"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("shutdown order = %v, want %v", order, want)
	}
}

func TestGracefulShutdownForceClosesServerOnTimeout(t *testing.T) {
	closed := false
	gracefulShutdown(shutdownSteps{
		cancelRequests: func() {},
		stopServer:     func(context.Context) error { return context.DeadlineExceeded },
		closeServer:    func() error { closed = true; return nil },
		stopJobs:       func(context.Context) {},
		stopNotify:     func(context.Context) {},
	}, time.Second, 100*time.Millisecond)
	if !closed {
		t.Fatal("server was not force-closed after Shutdown failed")
	}
}

// Slow jobs use up their share of the deadline, not the notifier's reserve.
func TestGracefulShutdownReservesBudgetForNotifications(t *testing.T) {
	const total = 300 * time.Millisecond
	const reserve = 150 * time.Millisecond
	var notifyBudget time.Duration
	gracefulShutdown(shutdownSteps{
		cancelRequests: func() {},
		stopServer:     func(context.Context) error { return nil },
		closeServer:    func() error { return nil },
		stopJobs: func(ctx context.Context) {
			<-ctx.Done() // a job that never stops in time
		},
		stopNotify: func(ctx context.Context) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Error("notify context has no deadline")
				return
			}
			notifyBudget = time.Until(deadline)
		},
	}, total, reserve)

	if notifyBudget < reserve-50*time.Millisecond {
		t.Fatalf("notify budget = %s, want about %s reserved after jobs used their share", notifyBudget, reserve)
	}
}

func TestStaticAssetsSendCacheHeadersAndGzip(t *testing.T) {
	r := newRouter(false, nil)

	// index.html is not fingerprinted and must be revalidated so an update is
	// picked up on the next load.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/app/opensync/", nil))
	if w.Code != 200 {
		t.Fatalf("index status = %d", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("index Cache-Control = %q, want no-cache", got)
	}
	if w.Body.Len() == 0 {
		t.Fatal("empty index.html")
	}

	// Fingerprinted assets are immutable.
	files, err := fs.Sub(webFiles, "web")
	if err != nil {
		t.Fatalf("fs.Sub: %v", err)
	}
	matches, err := fs.Glob(files, "assets/*.js")
	if err != nil || len(matches) == 0 {
		t.Fatalf("no built assets found: %v", err)
	}
	asset := matches[0]
	plain := httptest.NewRecorder()
	r.ServeHTTP(plain, httptest.NewRequest("GET", "/app/opensync/"+asset, nil))
	if plain.Code != 200 {
		t.Fatalf("asset status = %d", plain.Code)
	}
	if got := plain.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("asset Cache-Control = %q", got)
	}

	compressed := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/app/opensync/"+asset, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	r.ServeHTTP(compressed, req)
	if got := compressed.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	reader, err := gzip.NewReader(compressed.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read gzip body: %v", err)
	}
	if string(decoded) != plain.Body.String() {
		t.Fatal("gzip body does not match the plain asset")
	}
}

func TestStaticRoutesRejectNeighborPrefix(t *testing.T) {
	r := newRouter(false, nil)
	for _, path := range []string{"/app/opensync-other", "/app/opensync/svr", "/app/opensync/svr/missing"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatalf("path %s status=%d", path, w.Code)
		}
	}
}

func TestNativeRoutesDoNotExposeLegacyLogin(t *testing.T) {
	r := newRouter(false, nil)
	for _, path := range []string{"/svr/noAuth/login", "/app/opensync/svr/noAuth/init"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatalf("legacy route %s status=%d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/app/opensync/svr/session", nil))
	if w.Code != 401 {
		t.Fatalf("missing gateway identity status=%d", w.Code)
	}
	req := httptest.NewRequest("GET", "/app/opensync/svr/session", nil)
	req.Header.Set("X-Trim-Userid", "1000")
	req.Header.Set("X-Trim-Isadmin", "true")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("gateway session status=%d", w.Code)
	}
}

// The lock marker serializes starts before either one probes or binds: probing
// alone was racy, and two starts could migrate the database concurrently.
func TestStartupLockExcludesSecondOwner(t *testing.T) {
	dir := shortSocketDir(t)
	first, err := acquireStartupLock(dir)
	if err != nil {
		t.Fatalf("acquireStartupLock() error: %v", err)
	}
	if _, err := acquireStartupLock(dir); !errors.Is(err, errServiceAlreadyRunning) {
		t.Fatalf("second acquireStartupLock() error = %v, want errServiceAlreadyRunning", err)
	}
	first.Close()
	third, err := acquireStartupLock(dir)
	if err != nil {
		t.Fatalf("acquireStartupLock() after release error: %v", err)
	}
	third.Close()
}

// A start that has taken the lock must win even before it publishes a socket or
// a pending name, which is exactly the window the probe-based check missed.
func TestOpenListenerRefusesWhileLockIsHeld(t *testing.T) {
	dir := shortSocketDir(t)
	lock, err := acquireStartupLock(dir)
	if err != nil {
		t.Fatalf("acquireStartupLock() error: %v", err)
	}
	defer lock.Close()

	listener, _, err := openListener(false, "", dir)
	if !errors.Is(err, errServiceAlreadyRunning) {
		if listener != nil {
			listener.Close()
		}
		t.Fatalf("openListener() error = %v, want errServiceAlreadyRunning", err)
	}
}

// A chunked request has no Content-Length for the middleware pre-check; the
// MaxBytesReader path must answer 413, not a 200 "missing parameters" envelope.
func TestBodyLimitMiddlewareAnswers413ForChunkedBody(t *testing.T) {
	router := newRouter(true, nil)
	body := `{"name":"` + strings.Repeat("x", (1<<20)+1024) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/app/opensync/svr/native/storage", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = -1
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d body=%s, want 413", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"code":413`) {
		t.Fatalf("body = %s, want the 413 envelope", w.Body.String())
	}
}

func TestSystemImportAllowsUpTo12MiBAndRejectsLarger(t *testing.T) {
	router := newRouter(true, nil)

	// 2 MiB payload to /system/import must pass body-limit middleware (not 413).
	// Use version=999 so ImportSystemBackup rejects validation before touching DB.
	twoMiB := `{"conflictMode":"skip","payload":{"version":999,"padding":"` + strings.Repeat("a", 2<<20) + `"}}`
	req := httptest.NewRequest(http.MethodPost, "/app/opensync/svr/system/import", strings.NewReader(twoMiB))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("2 MiB import was rejected with 413: %s", w.Body.String())
	}

	// Same 2 MiB payload to normal endpoint must still be rejected with 413.
	reqNormal := httptest.NewRequest(http.MethodPost, "/app/opensync/svr/alist", strings.NewReader(twoMiB))
	reqNormal.Header.Set("Content-Type", "application/json")
	wNormal := httptest.NewRecorder()
	router.ServeHTTP(wNormal, reqNormal)
	if wNormal.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("2 MiB /alist status = %d, want 413", wNormal.Code)
	}

	// >12 MiB payload to /system/import must be rejected with 413.
	tooBig := `{"conflictMode":"skip","payload":{"version":1,"padding":"` + strings.Repeat("b", (12<<20)+1024) + `"}}`
	reqBig := httptest.NewRequest(http.MethodPost, "/app/opensync/svr/system/import", strings.NewReader(tooBig))
	reqBig.Header.Set("Content-Type", "application/json")
	wBig := httptest.NewRecorder()
	router.ServeHTTP(wBig, reqBig)
	if wBig.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf(">12 MiB import status = %d, want 413", wBig.Code)
	}
}

// stopServer hitting its budget must force-close the server and leave the rest
// of the non-notify phase budget for stopJobs, and stopRetention must run
// before stopJobs.
func TestGracefulShutdownSeparatesServerAndJobsBudgets(t *testing.T) {
	const total = 450 * time.Millisecond
	const reserve = 150 * time.Millisecond
	var order []string
	var jobsBudget time.Duration
	gracefulShutdown(shutdownSteps{
		cancelRequests: func() { order = append(order, "cancelRequests") },
		stopServer: func(ctx context.Context) error {
			order = append(order, "stopServer")
			<-ctx.Done()
			return ctx.Err()
		},
		closeServer:   func() error { order = append(order, "closeServer"); return nil },
		stopRetention: func() { order = append(order, "stopRetention") },
		stopJobs: func(ctx context.Context) {
			order = append(order, "stopJobs")
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Error("jobs context has no deadline")
				return
			}
			jobsBudget = time.Until(deadline)
		},
		stopNotify:      func(context.Context) { order = append(order, "stopNotify") },
		stopNotifyTests: func(context.Context) { order = append(order, "stopNotifyTests") },
	}, total, reserve)

	wantOrder := []string{"cancelRequests", "stopServer", "closeServer", "stopRetention", "stopJobs", "stopNotify", "stopNotifyTests"}
	if !reflect.DeepEqual(order, wantOrder) {
		t.Fatalf("order = %v, want %v", order, wantOrder)
	}
	// phaseBudget = 300ms, serverBudget = 100ms, so stopJobs gets ~200ms.
	if jobsBudget < 120*time.Millisecond {
		t.Fatalf("jobs budget after server timeout = %s, want >= 120ms", jobsBudget)
	}
}

// discard must unlink its socket while still holding the startup lock, so a
// second instance waiting on the lock never races with the old discard.
func TestSocketPublisherDiscardUnlinksBeforeReleasingLock(t *testing.T) {
	dir := shortSocketDir(t)
	socket := filepath.Join(dir, "app.sock")
	listener, publish, err := openListener(false, "", dir)
	if err != nil {
		t.Fatalf("openListener() error: %v", err)
	}
	if err := publish.commit(); err != nil {
		listener.Close()
		publish.discard()
		t.Fatalf("commit() error: %v", err)
	}
	// While publish holds its lock, a second acquireStartupLock must fail.
	if _, err := acquireStartupLock(dir); !errors.Is(err, errServiceAlreadyRunning) {
		listener.Close()
		publish.discard()
		t.Fatalf("acquireStartupLock while publisher active = %v, want errServiceAlreadyRunning", err)
	}
	listener.Close()
	publish.discard()
	if _, err := os.Lstat(socket); !os.IsNotExist(err) {
		t.Fatalf("socket still exists after discard: %v", err)
	}
	// Idempotent second discard must not touch a newly opened instance's socket.
	l2, p2, err := openListener(false, "", dir)
	if err != nil {
		t.Fatalf("second openListener() error: %v", err)
	}
	defer l2.Close()
	defer p2.discard()
	if err := p2.commit(); err != nil {
		t.Fatalf("second commit() error: %v", err)
	}
	publish.discard()
	if _, err := os.Lstat(socket); err != nil {
		t.Fatalf("second instance socket removed by stale discard: %v", err)
	}
}
