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

const prefix = "/app/opensync"

func newRouter(development bool, allowedOrigins []string) *gin.Engine {
	r := gin.New()
	_ = r.SetTrustedProxies(nil)
	r.Use(gin.Logger(), func(c *gin.Context) {
		defer func() {
			if value := recover(); value != nil {
				log.Printf("request failed: %v", value)
				msg := "操作失败，请检查引擎连接或查看服务日志"
				if public, ok := value.(model.PublicError); ok {
					msg = string(public)
				}
				c.AbortWithStatusJSON(500, gin.H{"code": 500, "msg": msg})
			}
		}()
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "same-origin")
		c.Header("Content-Security-Policy", "frame-ancestors 'self'; object-src 'none'; base-uri 'self'")
		if c.Request.ContentLength > 1<<20 {
			c.AbortWithStatusJSON(413, gin.H{"code": 413, "msg": "请求内容过大"})
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
		c.Next()
	})
	api := r.Group(prefix+"/svr", platform.GatewayRequired(development, allowedOrigins))
	api.GET("/session", func(c *gin.Context) {
		c.JSON(200, model.Success(gin.H{"uid": c.GetInt64("uid"), "development": development, "version": "0.1.0"}))
	})
	api.GET("/system/config", handler.GetSystemConfig)
	api.PUT("/system/config", func(c *gin.Context) {
		var values config.SystemSettings
		if c.ShouldBindJSON(&values) != nil {
			c.JSON(400, model.Error("配置参数无效"))
			return
		}
		if err := config.UpdateSystemSettings(values); err != nil {
			c.JSON(400, model.Error(err.Error()))
			return
		}
		service.RunTaskRetentionCleanup()
		c.JSON(200, model.Success(config.GetSystemSettings()))
	})
	api.GET("/alist", handler.GetAlist)
	api.POST("/alist", handler.AddAlist)
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
	defer mapper.CloseDB()
	service.InitJobs()
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
	server := &http.Server{Handler: newRouter(*development, config.GetConfig().Server.AllowedOrigins), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 32 << 10}
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
	shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	service.ShutdownJobs(shutdown)
	if server.Shutdown(shutdown) != nil {
		server.Close()
	}
}
