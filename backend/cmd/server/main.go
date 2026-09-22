package main

import (
	"context"
	"embed"
	"errors"
	"flag"
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
	var listener net.Listener
	var err error
	if *development {
		listener, err = net.Listen("tcp", "127.0.0.1:"+*port)
	} else {
		destination := os.Getenv("TRIM_APPDEST")
		if destination == "" {
			log.Fatal("TRIM_APPDEST is required; use --dev only for local development")
		}
		socket := filepath.Join(destination, "app.sock")
		if info, statErr := os.Lstat(socket); statErr == nil {
			if info.Mode()&os.ModeSocket == 0 {
				log.Fatal("socket path contains a non-socket file")
			}
			if conn, dialErr := net.DialTimeout("unix", socket, time.Second); dialErr == nil {
				conn.Close()
				log.Fatal("service is already running")
			}
			if err := os.Remove(socket); err != nil {
				log.Fatal(err)
			}
		}
		listener, err = net.Listen("unix", socket)
		if err == nil {
			err = os.Chmod(socket, 0660)
		}
	}
	if err != nil {
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
	shutdown, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	service.ShutdownJobs(shutdown)
	// Drain queued notifications after the jobs stop (stopping them enqueues the
	// last ones) and within the same deadline, so a wedged webhook delays exit by
	// at most shutdownTimeout instead of indefinitely.
	service.ShutdownNotifyDispatcher(shutdown)
	// Ends the SSE handlers before Shutdown waits on them.
	cancelRequests()
	if server.Shutdown(shutdown) != nil {
		server.Close()
	}
}
