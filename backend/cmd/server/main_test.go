package main

import (
	"context"
	"errors"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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
	if len(entries) != 0 {
		t.Fatalf("files left after discard: %v", entries)
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
