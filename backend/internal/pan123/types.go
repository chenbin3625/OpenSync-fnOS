package pan123

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	ProtocolOpenAPI = "open_api"
	ProtocolWeb     = "web"

	DefaultOpenAPIBaseURL = "https://open-api.123pan.com"
	DefaultWebLoginURL    = "https://login.123pan.com"
	DefaultWebAPIURL      = "https://api.123278.com/b/api"
	DefaultWebOrigin      = "https://yun.123pan.cn"
	DefaultWebPlatform    = "web"
	DefaultWebAppVersion  = "3"
	DefaultUserAgent      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

	DefaultRootFolderID      = "0"
	DefaultUploadConcurrency = 3
	MaxUploadConcurrency     = 8
	DefaultDirectLinkTTL     = 30 // minutes
	DefaultCacheTTL          = 30 * time.Second
	DefaultDownloadURLTTL    = 5 * time.Minute
	TokenSafetyMargin        = 10 * time.Minute

	// Files up to 32 MiB can use OpenAPI v2 single-step upload when rapid upload
	// misses, saving preupload + per-slice + complete polling round trips.
	openAPISingleUploadMaxBytes int64 = 32 << 20
)

var (
	ErrUnauthorized        = errors.New("123云盘认证已失效，请检查凭证或重新获取访问令牌")
	ErrRateLimited         = errors.New("123云盘接口触发限流，请降低并发或稍后重试")
	ErrAuthChallenge       = errors.New("123云盘要求先在官方网页完成安全验证（短信验证码或微信登录），完成后请更新 Token 或重试")
	ErrInvalidName         = errors.New("文件名包含非法字符")
	ErrInconsistentListing = errors.New("123云盘目录列表不一致，已中止以避免同步误判")
)

// Options configures a dual-protocol 123Pan client (OpenAPI or personal Web).
type Options struct {
	Protocol string // "open_api" (default) or "web"

	// OpenAPI credentials
	ClientID       string
	ClientSecret   string
	AccessToken    string
	TokenExpiresAt time.Time
	UseDirectLink  bool
	DirectLinkKey  string
	DirectLinkTTL  int // minutes

	// Personal Web credentials
	Username string
	Password string
	Platform string

	// Common options
	RootFolderID       string
	UserAgent          string
	ProxyURL           string
	InsecureSkipVerify bool
	TPSLimit           float64
	UploadConcurrency  int
	CacheTTL           time.Duration
	DownloadURLTTL     time.Duration

	// OnTokenUpdate is invoked whenever an access token is acquired or refreshed
	// so OpenSync can persist it across restarts.
	OnTokenUpdate func(accessToken string, expiresAt time.Time)

	// Optional endpoint overrides (primarily for unit tests).
	OpenAPIBaseURL string
	WebLoginURL    string
	WebAPIURL      string
	WebOrigin      string
}

// NormalizeProtocol canonicalizes protocol names to ProtocolOpenAPI or ProtocolWeb.
func NormalizeProtocol(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", ProtocolOpenAPI, "openapi", "open":
		return ProtocolOpenAPI
	case ProtocolWeb, "personal":
		return ProtocolWeb
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

// ParseRootFolderID parses a configured root_folder_id ("0" when empty).
func ParseRootFolderID(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id < 0 {
		return 0, fmt.Errorf("根目录 ID（root_folder_id）需为非负整数")
	}
	return id, nil
}

// Entry represents a normalized file or folder on 123Pan across both protocols.
type Entry struct {
	FID       int64
	ParentFID int64
	Name      string
	IsDir     bool
	Size      int64
	ModTime   time.Time
	MD5       string
	S3KeyFlag string
	Type      int
}

// Quota reports cloud drive capacity.
type Quota struct {
	Total int64
	Used  int64
	Free  int64
}

// AccountInfo reports basic account identity.
type AccountInfo struct {
	UID      int64
	Nickname string
}

// UploadResult summarizes a completed upload.
type UploadResult struct {
	FID   int64
	Name  string
	Size  int64
	MD5   string
	Rapid bool
}

// UploadFileOptions configures a local-file direct upload.
type UploadFileOptions struct {
	File            *os.File
	Size            int64
	ModTime         time.Time
	Concurrency     int
	ResumeStatePath string
	Overwrite       bool
	WaitIfPaused    func(context.Context) error
	ThrottleBytes   func(context.Context, int) error
	OnProgress      func(doneBytes int64, speed float64, rapid bool)
}

type cachedDownload struct {
	URL      string
	Headers  http.Header
	ExpireAt time.Time
}

type dlFlight struct {
	done    chan struct{}
	url     string
	headers http.Header
	err     error
}
