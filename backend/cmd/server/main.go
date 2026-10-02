package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"github.com/gin-gonic/gin"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"opensync/internal/config"
	"opensync/internal/handler"
	"opensync/internal/mapper"
	"opensync/internal/model"
	"opensync/internal/msg"
	"opensync/internal/platform"
	"opensync/internal/service"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

//go:embed all:web
var webFiles embed.FS

// appVersion is injected at build time via
// -ldflags "-X main.appVersion=<version>" (see scripts/lib/package.mjs, which
// takes it from fnos/manifest). Local `go build` leaves the fallback.
var appVersion = "dev"

const prefix = "/app/opensync"
const maxRequestBodyBytes = 1 << 20
const shutdownTimeout = 30 * time.Second

func newRouter(development bool, allowedOrigins []string) *gin.Engine {
	return newRouterWithVersionChecker(development, allowedOrigins, newVersionChecker(latestReleaseAPI, http.DefaultClient))
}

func newRouterWithVersionChecker(development bool, allowedOrigins []string, checker *versionChecker) *gin.Engine {
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(gin.Logger(), func(c *gin.Context) {
		defer func() {
			if value := recover(); value != nil {
				log.Printf("unexpected panic (safety net): %v", value)
				if c.Writer.Written() {
					c.Abort()
					return
				}
				c.AbortWithStatusJSON(500, gin.H{"code": 500, "msg": msg.T(msg.InternalError)})
			}
		}()
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "same-origin")
		c.Header("Content-Security-Policy", "frame-ancestors 'self'; object-src 'none'; base-uri 'self'")
		if c.Request.ContentLength > maxRequestBodyBytes {
			c.AbortWithStatusJSON(413, gin.H{"code": 413, "msg": msg.T(msg.RequestTooLarge)})
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBodyBytes)
		c.Next()
	})
	api := r.Group(prefix+"/svr", platform.GatewayRequired(development, allowedOrigins))
	api.GET("/session", func(c *gin.Context) {
		c.JSON(200, model.Success(gin.H{"uid": c.GetInt64("uid"), "development": development, "version": appVersion}))
	})
	api.GET("/version/latest", func(c *gin.Context) {
		// refresh=1 is what the version entry's "检查更新" button sends: the user
		// asked explicitly, so answer from GitHub instead of the 30 minute cache.
		result, err := checker.checkWithRefresh(c.Request.Context(), appVersion, c.Query("refresh") == "1")
		if err != nil {
			log.Printf("latest release check failed: %v", err)
			c.JSON(http.StatusBadGateway, model.Error("暂时无法检查最新版本"))
			return
		}
		c.JSON(http.StatusOK, model.Success(result))
	})
	api.GET("/system/config", handler.GetSystemConfig)
	api.PUT("/system/config", handler.UpdateSystemConfig)
	api.GET("/alist", handler.GetAlist)
	api.POST("/alist", handler.AddAlist)
	api.POST("/alist/test", handler.TestAlist)
	api.PUT("/alist", handler.UpdateAlist)
	api.DELETE("/alist", handler.DeleteAlist)
	api.GET("/job", handler.GetJob)
	api.GET("/job/stream", handler.StreamJobCurrent)
	api.POST("/job", handler.AddJob)
	api.PUT("/job", handler.UpdateJob)
	api.DELETE("/job", handler.DeleteJob)
	api.GET("/notify", handler.GetNotify)
	api.POST("/notify", handler.AddNotify)
	api.POST("/notify/test", handler.TestNotify)
	api.PUT("/notify", handler.UpdateNotify)
	api.DELETE("/notify", handler.DeleteNotify)
	files, _ := fs.Sub(webFiles, "web")
	r.NoRoute(func(c *gin.Context) {
		name := strings.TrimPrefix(c.Request.URL.Path, prefix+"/")
		if c.Request.URL.Path == prefix {
			name = "index.html"
		}
		if c.Request.Method != "GET" || (c.Request.URL.Path != prefix && !strings.HasPrefix(c.Request.URL.Path, prefix+"/")) || name == "svr" || strings.HasPrefix(name, "svr/") || strings.Contains(name, "..") {
			c.Status(404)
			return
		}
		if !strings.HasPrefix(name, "assets/") && path.Ext(name) == "" {
			name = "index.html"
		}
		data, err := fs.ReadFile(files, name)
		if err != nil {
			c.Status(404)
			return
		}
		contentType := mime.TypeByExtension(path.Ext(name))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		c.Data(200, contentType, data)
	})
	return r
}

func main() {
	development := flag.Bool("dev", false, "loopback-only local development mode")
	port := flag.String("port", "8040", "development port")
	flag.Parse()
	if *development && os.Getenv("TRIM_APPNAME") != "" {
		log.Fatal("development mode is not allowed inside a fnOS application")
	}
	if !*development && os.Getenv("TRIM_APPDEST") == "" {
		log.Fatal("TRIM_APPDEST is required; use --dev only for local development")
	}
	if err := os.MkdirAll(config.DataDir(), 0700); err != nil {
		log.Fatal(err)
	}
	config.GetConfig()
	platform.LogOriginPolicy(config.GetConfig().Server.AllowedOrigins)
	// Bind before touching the database. The "already running" probe used to
	// come after InitSQL/InitJobs, so a second instance first rewrote the
	// running instance's in-flight tasks as aborted and only then noticed it
	// should not be running. The socket is bound under a private name and only
	// published as app.sock once initialisation succeeded: the fnOS start
	// script treats the socket's appearance as "started", so publishing it
	// before a migration that may still fail would report a dead service as up.
	listener, publish, err := openListener(*development, *port, os.Getenv("TRIM_APPDEST"))
	if err != nil {
		log.Fatal(err)
	}
	defer publish.discard()
	mapper.InitSQL()
	// ShutdownDB, not CloseDB: a straggler (debounced persist flush, cron tick)
	// reaching GetDB afterwards must fail rather than reopen the database and
	// recreate the files shutdown just released.
	defer mapper.ShutdownDB()
	service.InitJobs()
	// Completion notifications are delivered by background workers so a slow or
	// unreachable webhook cannot hold up a finishing task.
	service.StartNotifyDispatcher()
	stopRetention := service.StartTaskRetentionScheduler()
	defer stopRetention()
	if err := publish.commit(); err != nil {
		log.Fatal(err)
	}
	// baseCtx is cancelled at shutdown so long-lived handlers (the SSE progress
	// stream) return instead of keeping http.Server.Shutdown blocked for its
	// full timeout: an SSE handler only exits when its request context ends.
	baseCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	server := &http.Server{
		Handler:           newRouter(*development, config.GetConfig().Server.AllowedOrigins),
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { log.Printf("OpenSync fnOS listening at %s", listener.Addr()); done <- server.Serve(listener) }()
	select {
	case err = <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Printf("server failed: %v", err)
		}
	case <-ctx.Done():
	}
	gracefulShutdown(shutdownSteps{
		cancelRequests: cancelRequests,
		stopServer:     server.Shutdown,
		closeServer:    server.Close,
		stopJobs:       service.ShutdownJobs,
		stopNotify:     service.ShutdownNotifyDispatcher,
	}, shutdownTimeout, notifyShutdownReserve)
}

// notifyShutdownReserve is the part of shutdownTimeout kept back for draining
// notifications. Jobs stopping slowly would otherwise use the whole shared
// deadline and the final "task stopped" messages would never be sent.
const notifyShutdownReserve = 5 * time.Second

type shutdownSteps struct {
	cancelRequests func()
	stopServer     func(context.Context) error
	closeServer    func() error
	stopJobs       func(context.Context)
	stopNotify     func(context.Context)
}

// gracefulShutdown stops the process in dependency order within total:
//
//  1. Stop accepting requests. Jobs used to be stopped first while the server
//     still served, so a POST /job in that window could create and start a new
//     job just before the database closed under it.
//  2. Stop the jobs (and the retention cleanup), which enqueues their final
//     notifications.
//  3. Drain notifications, with at least reserve of its own, so a wedged
//     webhook delays exit by a bounded amount instead of indefinitely.
func gracefulShutdown(steps shutdownSteps, total, reserve time.Duration) {
	start := time.Now()
	deadline := start.Add(total)
	phaseDeadline := deadline.Add(-reserve)
	if reserve >= total {
		phaseDeadline = start
	}
	phaseCtx, cancelPhase := context.WithDeadline(context.Background(), phaseDeadline)
	defer cancelPhase()

	// Ends the SSE handlers before Shutdown waits on them.
	steps.cancelRequests()
	if steps.stopServer(phaseCtx) != nil {
		steps.closeServer()
	}
	steps.stopJobs(phaseCtx)

	notifyDeadline := deadline
	if min := time.Now().Add(reserve); notifyDeadline.Before(min) {
		notifyDeadline = min
	}
	notifyCtx, cancelNotify := context.WithDeadline(context.Background(), notifyDeadline)
	defer cancelNotify()
	steps.stopNotify(notifyCtx)
}

// openListener binds the HTTP listener: loopback TCP in --dev mode, otherwise
// the fnOS unix socket. It refuses to start when another instance answers on
// the socket, and replaces a stale socket file left by a crash.
//
// Outside dev mode the socket is bound under a temporary name next to app.sock;
// the returned socketPublisher renames it into place (commit) once the service
// is ready, or removes it (discard) if startup fails first. Accepting starts as
// soon as it is bound, so the rename does not race with clients.
func openListener(development bool, port, destination string) (net.Listener, *socketPublisher, error) {
	if development {
		listener, err := net.Listen("tcp", "127.0.0.1:"+port)
		return listener, &socketPublisher{}, err
	}
	if destination == "" {
		return nil, nil, errors.New("TRIM_APPDEST is required; use --dev only for local development")
	}
	socket := filepath.Join(destination, "app.sock")
	if info, statErr := os.Lstat(socket); statErr == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, nil, errors.New("socket path contains a non-socket file")
		}
		if conn, dialErr := net.DialTimeout("unix", socket, time.Second); dialErr == nil {
			conn.Close()
			return nil, nil, errServiceAlreadyRunning
		}
		if err := os.Remove(socket); err != nil {
			return nil, nil, err
		}
	}
	if err := probePendingSockets(destination); err != nil {
		return nil, nil, err
	}
	pending := filepath.Join(destination, fmt.Sprintf("%s%d", pendingSocketPrefix, os.Getpid()))
	_ = os.Remove(pending)
	listener, err := net.Listen("unix", pending)
	if err != nil {
		return nil, nil, err
	}
	// The listener must not unlink the path it was bound to on Close: after
	// commit that path no longer exists, and before commit discard removes it.
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := os.Chmod(pending, 0660); err != nil {
		listener.Close()
		_ = os.Remove(pending)
		return nil, nil, err
	}
	info, err := os.Lstat(pending)
	if err != nil {
		listener.Close()
		_ = os.Remove(pending)
		return nil, nil, err
	}
	return listener, &socketPublisher{pending: pending, final: socket, info: info}, nil
}

// pendingSocketPrefix names the private sockets bound before app.sock is
// published; the process ID follows it.
const pendingSocketPrefix = ".app.sock."

// probePendingSockets handles the private sockets other starts left behind.
// One that still accepts connections belongs to an instance that is
// initialising right now, which must not be raced; one that refuses is what a
// start that died before publishing leaves, and is removed.
func probePendingSockets(destination string) error {
	entries, err := os.ReadDir(destination)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), pendingSocketPrefix) || entry.Type()&os.ModeSocket == 0 {
			continue
		}
		path := filepath.Join(destination, entry.Name())
		if conn, dialErr := net.DialTimeout("unix", path, time.Second); dialErr == nil {
			conn.Close()
			return errServiceAlreadyRunning
		}
		_ = os.Remove(path)
	}
	return nil
}

// socketPublisher moves a bound socket from its private name to app.sock.
type socketPublisher struct {
	pending, final string
	// info identifies the bound socket file, so discard never removes an
	// app.sock that a later instance published after this one stopped serving.
	info      os.FileInfo
	committed bool
}

func (p *socketPublisher) commit() error {
	if p == nil || p.pending == "" || p.committed {
		return nil
	}
	if err := os.Rename(p.pending, p.final); err != nil {
		return fmt.Errorf("publish socket: %w", err)
	}
	p.committed = true
	return nil
}

// discard removes whichever socket file this process owns. log.Fatal skips
// deferred calls, so startup failures after binding rely on the pending name
// being private: the next start ignores it and the start script never saw it.
func (p *socketPublisher) discard() {
	if p == nil || p.pending == "" {
		return
	}
	name := p.pending
	if p.committed {
		name = p.final
	}
	if current, err := os.Lstat(name); err == nil && os.SameFile(current, p.info) {
		_ = os.Remove(name)
	}
}

var errServiceAlreadyRunning = errors.New("service is already running")
