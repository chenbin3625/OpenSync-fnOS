package native

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

var (
	tokenUpdateHookMu sync.RWMutex
	tokenUpdateHook   func(updates map[string]TokenUpdate)
)

// TokenUpdate carries the saved configuration a refreshed token came from so
// persistence can reject stale sessions after edits or a newer refresh.
type TokenUpdate struct {
	Original map[string]string
	Fields   map[string]string
}

// RegisterTokenUpdateHook registers a process-wide callback invoked whenever
// rclone refreshes an OAuth/session token in a writable session config file.
func RegisterTokenUpdateHook(fn func(updates map[string]TokenUpdate)) {
	tokenUpdateHookMu.Lock()
	tokenUpdateHook = fn
	tokenUpdateHookMu.Unlock()
}

func tokenRefreshableFields(typ string) []string {
	switch typ {
	case "onedrive", "drive", "dropbox", "box", "pcloud", "yandex", "jottacloud", "hidrive", "huaweidrive", "mailru", "googlecloudstorage":
		return []string{"token"}
	case "protondrive":
		return []string{"client_access_token", "client_refresh_token"}
	case "quark":
		return []string{"cookie"}
	default:
		return nil
	}
}

const maxStderrCapture = 8 << 10

func nativeTempDir() string {
	if dir := strings.TrimSpace(os.Getenv("TRIM_PKGTMP")); dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return dir
		}
	}
	return os.TempDir()
}

var passthroughEnvKeys = []string{
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
	"http_proxy", "https_proxy", "no_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR", "TZ",
	// Runtime credential/configuration sources used by rclone backends. These
	// are intentionally allowlisted because command() otherwise starts with a
	// clean environment and HOME=/nonexistent.
	"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
	"AWS_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION", "AWS_SHARED_CREDENTIALS_FILE", "AWS_CONFIG_FILE",
	"AWS_ROLE_ARN", "AWS_ROLE_SESSION_NAME", "AWS_WEB_IDENTITY_TOKEN_FILE",
	"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI",
	"AWS_CONTAINER_AUTHORIZATION_TOKEN", "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE",
	"AWS_EC2_METADATA_DISABLED", "AWS_EC2_METADATA_SERVICE_ENDPOINT",
	"RCLONE_S3_ACCESS_KEY_ID", "RCLONE_S3_SECRET_ACCESS_KEY", "RCLONE_S3_SESSION_TOKEN",
	"RCLONE_S3_ENV_AUTH", "RCLONE_S3_SHARED_CREDENTIALS_FILE", "RCLONE_S3_PROFILE",
	"QINGSTOR_ACCESS_KEY_ID", "QINGSTOR_SECRET_ACCESS_KEY", "QINGSTOR_ZONE",
	"RCLONE_QINGSTOR_ENV_AUTH", "RCLONE_QINGSTOR_ACCESS_KEY_ID", "RCLONE_QINGSTOR_SECRET_ACCESS_KEY",
	"RCLONE_QINGSTOR_ZONE",
	"RCLONE_OOS_NAMESPACE", "RCLONE_OOS_COMPARTMENT", "RCLONE_OOS_REGION",
	"RCLONE_OOS_ENDPOINT", "RCLONE_OOS_CONFIG_FILE", "RCLONE_OOS_CONFIG_PROFILE",
	"OCI_CONFIG_FILE", "OCI_CLI_PROFILE", "OCI_RESOURCE_PRINCIPAL_VERSION", "OCI_RESOURCE_PRINCIPAL_RPST",
	"OCI_RESOURCE_PRINCIPAL_REGION", "OCI_RESOURCE_PRINCIPAL_PRIVATE_PEM", "OCI_RESOURCE_PRINCIPAL_PRIVATE_PEM_PASSPHRASE",
	"TF_VAR_tenancy_ocid", "TF_VAR_user_ocid", "TF_VAR_fingerprint", "TF_VAR_region", "TF_VAR_private_key_path",
}

// Production packages place both executables together. A development override
// is owned by the service environment, never accepted from the HTTP API.
func command(ctx context.Context, args ...string) (*exec.Cmd, error) {
	bin := os.Getenv("OPENSYNC_RCLONE_BIN")
	if bin == "" {
		self, e := os.Executable()
		if e != nil {
			return nil, e
		}
		bin = filepath.Join(filepath.Dir(self), "rclone")
	}
	if !filepath.IsAbs(bin) {
		return nil, errors.New("rclone 可执行文件需要绝对路径")
	}
	if st, e := os.Stat(bin); e != nil || !st.Mode().IsRegular() {
		return nil, errors.New("未找到内置 rclone")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	env := []string{
		"HOME=/nonexistent",
		"LANG=C.UTF-8",
		"PATH=/usr/bin:/bin",
		"TMPDIR=" + nativeTempDir(),
	}
	for _, key := range passthroughEnvKeys {
		if val := os.Getenv(key); val != "" {
			env = append(env, key+"="+val)
		}
	}
	cmd.Env = env
	setParentDeathSignal(cmd)
	cmd.WaitDelay = 3 * time.Second
	return cmd, nil
}

func Available(ctx context.Context) error {
	cmd, e := command(ctx, "version")
	if e != nil {
		return e
	}
	return cmd.Run()
}

// Version returns the bundled rclone version string (e.g. "v1.76.0").
func Version(ctx context.Context) (string, error) {
	cmd, e := command(ctx, "version")
	if e != nil {
		return "", e
	}
	out, _, err := limitedOutputWithStderr(cmd, 64<<10)
	if err != nil {
		return "", err
	}
	firstLine, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	fields := strings.Fields(firstLine)
	if len(fields) >= 2 && strings.EqualFold(fields[0], "rclone") {
		return fields[1], nil
	}
	return strings.TrimSpace(firstLine), nil
}

type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("rclone 输出超过上限")
	}
	return b.Buffer.Write(p)
}

// tailBuffer keeps up to limit bytes without failing writes once full.
type tailBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if rem := b.limit - b.buf.Len(); rem > 0 {
		if len(p) > rem {
			p = p[:rem]
		}
		b.buf.Write(p)
	}
	return n, nil
}

func (b *tailBuffer) String() string {
	return strings.TrimSpace(b.buf.String())
}

func limitedOutput(cmd *exec.Cmd, max int) ([]byte, error) {
	out, _, err := limitedOutputWithStderr(cmd, max)
	return out, err
}

func limitedOutputWithStderr(cmd *exec.Cmd, max int) ([]byte, string, error) {
	b := &cappedBuffer{limit: max}
	errBuf := &tailBuffer{limit: maxStderrCapture}
	// Wrap Write to avoid bytes.Buffer.ReadFrom bypassing the size check.
	cmd.Stdout = struct{ io.Writer }{b}
	cmd.Stderr = errBuf
	e := cmd.Run()
	detail := errBuf.String()
	if e != nil && detail != "" {
		log.Printf("native rclone command failed (%v): %s", e, sanitizeRcloneError(detail))
	}
	return b.Bytes(), detail, e
}

// classifyRcloneError turns rclone stderr diagnostics into safe, actionable
// messages without leaking internal paths or secrets.
func classifyRcloneError(backendType, stderr string) error {
	low := strings.ToLower(stderr)
	switch {
	case strings.Contains(low, "knownhosts: key mismatch") || strings.Contains(low, "key is unknown"):
		return errors.New("SSH 主机公钥校验失败，服务器密钥可能已变更，请核对后更新 known_hosts")
	case strings.Contains(low, "unable to authenticate") || strings.Contains(low, "signaturedoesnotmatch") ||
		strings.Contains(low, "invalidaccesskeyid") || strings.Contains(low, "401") ||
		strings.Contains(low, "unauthorized") || strings.Contains(low, "logon_failure") ||
		strings.Contains(low, "bad password") || strings.Contains(low, "authentication failed") ||
		strings.Contains(low, "invalid_grant") || strings.Contains(low, "token expired"):
		if backendType == "quark" {
			return errors.New("夸克网盘登录状态已失效（Cookie 过期），请在存储管理中重新扫码或更新 Cookie")
		}
		return errors.New("认证失败，请检查账号、密码、OAuth Token 或访问密钥是否正确")
	case strings.Contains(low, "429") || strings.Contains(low, "too many requests"):
		if backendType == "quark" {
			return errors.New("夸克网盘接口触发限流（429），请降低并发数或稍后重试")
		}
		return errors.New("远端存储接口触发限流（429），请降低并发或稍后重试")
	case strings.Contains(low, "accessdenied") || strings.Contains(low, "403") || strings.Contains(low, "forbidden") || strings.Contains(low, "permission denied"):
		if backendType == "s3" {
			return errors.New("无列桶权限（403）；如为单桶密钥，可先勾选“不检查存储桶”保存并在任务中输入 /存储名/存储桶/目录")
		}
		return errors.New("无目录访问权限（403），请检查账号权限")
	case strings.Contains(low, "no such host") || strings.Contains(low, "lookup "):
		return errors.New("域名解析失败，请检查地址或主机名是否正确")
	case strings.Contains(low, "connection refused"):
		return errors.New("连接被拒绝，请检查地址、端口及远端服务是否已启动")
	case strings.Contains(low, "i/o timeout") || strings.Contains(low, "deadline exceeded") || strings.Contains(low, "timed out"):
		return errors.New("连接超时，请检查网络连通性、防火墙或端点地址")
	case strings.Contains(low, "x509:") || strings.Contains(low, "certificate") || strings.Contains(low, "tls:"):
		return errors.New("TLS/SSL 证书验证失败，请检查协议（http/https）、服务端证书或开启跳过证书校验")
	case strings.Contains(low, "not supported") && strings.Contains(low, "about"):
		return errors.New("该存储后端不支持查询空间用量")
	default:
		return errors.New("目录读取失败，请检查路径、连接和列目录权限")
	}
}

type SessionSpec struct {
	Name     string
	Mode     diskAccessMode
	Checksum bool
}

type SessionOptions struct {
	ProxyURL              string
	BwLimit               string
	InsecureSkipVerify    bool
	PreserveCryptChecksum bool
	DisableTokenPersist   bool
	OnTokenUpdate         func(updates map[string]TokenUpdate)
}

type Session struct {
	dir                 string
	closers             []func()
	configFile          *os.File
	configPath          string
	initialRemotes      Config
	inProcessMu         sync.Mutex
	inProcessUpdates    map[string]map[string]string
	onTokenUpdate       func(updates map[string]TokenUpdate)
	disableTokenPersist bool
	proxyURL            string
	insecureSkipVerify  bool
}

func (s *Session) recordInProcessTokenUpdate(remoteName, field, value string) {
	value = strings.TrimSpace(value)
	if remoteName == "" || field == "" || value == "" {
		return
	}
	s.inProcessMu.Lock()
	if s.inProcessUpdates == nil {
		s.inProcessUpdates = map[string]map[string]string{}
	}
	if s.inProcessUpdates[remoteName] == nil {
		s.inProcessUpdates[remoteName] = map[string]string{}
	}
	s.inProcessUpdates[remoteName][field] = value
	s.inProcessMu.Unlock()
}

func dispatchTokenUpdate(remoteName string, original map[string]string, field, value string, opts SessionOptions) {
	value = strings.TrimSpace(value)
	if remoteName == "" || original == nil || field == "" || value == "" || value == strings.TrimSpace(original[field]) {
		return
	}
	updates := map[string]TokenUpdate{
		remoteName: {
			Original: maps.Clone(original),
			Fields:   map[string]string{field: value},
		},
	}
	if opts.OnTokenUpdate != nil {
		opts.OnTokenUpdate(updates)
		return
	}
	if opts.DisableTokenPersist {
		return
	}
	tokenUpdateHookMu.RLock()
	hook := tokenUpdateHook
	tokenUpdateHookMu.RUnlock()
	if hook != nil {
		hook(updates)
	}
}

func (s *Session) Close() {
	for _, close := range s.closers {
		close()
	}
	if s.configFile != nil {
		_ = s.configFile.Close()
		s.configFile = nil
	}
	s.flushRefreshedTokens()
	os.RemoveAll(s.dir)
}

func (s *Session) flushRefreshedTokens() {
	if len(s.initialRemotes) == 0 {
		return
	}
	updates := map[string]TokenUpdate{}
	if s.configPath != "" {
		if data, err := os.ReadFile(s.configPath); err == nil && len(data) > 0 {
			if parsed, err := parseRawINI(string(data)); err == nil {
				for remoteName, original := range s.initialRemotes {
					sec, ok := parsed[remoteName]
					if !ok {
						continue
					}
					refFields := map[string]string{}
					for _, field := range tokenRefreshableFields(original["type"]) {
						cur := strings.TrimSpace(sec[field])
						if cur == "" || cur == strings.TrimSpace(original[field]) {
							continue
						}
						if field == "token" && !validateOAuthTokenJSON(cur) {
							continue
						}
						refFields[field] = cur
					}
					if len(refFields) > 0 {
						updates[remoteName] = TokenUpdate{Original: maps.Clone(original), Fields: refFields}
					}
				}
			}
		}
	}

	s.inProcessMu.Lock()
	inProc := s.inProcessUpdates
	s.inProcessUpdates = nil
	s.inProcessMu.Unlock()
	for remoteName, fieldMap := range inProc {
		original, ok := s.initialRemotes[remoteName]
		if !ok {
			continue
		}
		allowed := tokenRefreshableFields(original["type"])
		refFields := map[string]string{}
		if prev, exists := updates[remoteName]; exists {
			refFields = prev.Fields
		}
		for _, field := range allowed {
			cur := strings.TrimSpace(fieldMap[field])
			if cur == "" || cur == strings.TrimSpace(original[field]) {
				continue
			}
			refFields[field] = cur
		}
		if len(refFields) > 0 {
			updates[remoteName] = TokenUpdate{Original: maps.Clone(original), Fields: refFields}
		}
	}

	if len(updates) == 0 {
		return
	}
	if s.onTokenUpdate != nil {
		s.onTokenUpdate(updates)
		return
	}
	if s.disableTokenPersist {
		return
	}
	tokenUpdateHookMu.RLock()
	hook := tokenUpdateHook
	tokenUpdateHookMu.RUnlock()
	if hook != nil {
		hook(updates)
	}
}

func (s *Session) Cmd(ctx context.Context, args ...string) (*exec.Cmd, error) {
	fullArgs := make([]string, 0, len(args)+4)
	if s.configPath != "" {
		if s.configFile != nil {
			_ = s.configFile.Sync()
		}
		fullArgs = append(fullArgs, "--config", s.configPath, "--ask-password=false")
	} else {
		// Reset file offset so platforms where /dev/fd/3 shares the open file
		// description (e.g. macOS local dev) always read the config from byte 0.
		if _, err := s.configFile.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		fullArgs = append(fullArgs, "--config", "/dev/fd/3", "--ask-password=false")
	}
	if s.insecureSkipVerify {
		fullArgs = append(fullArgs, "--no-check-certificate")
	}
	fullArgs = append(fullArgs, args...)
	cmd, err := command(ctx, fullArgs...)
	if err == nil {
		if s.configPath == "" {
			cmd.ExtraFiles = []*os.File{s.configFile}
		}
		if s.proxyURL != "" {
			cmd.Env = applyProxyEnv(cmd.Env, s.proxyURL)
		}
	}
	return cmd, err
}

// applyProxyEnv sets HTTP_PROXY/HTTPS_PROXY and guarantees loopback addresses
// bypass the proxy so local WebDAV endpoints always connect directly.
func applyProxyEnv(env []string, proxyURL string) []string {
	existingNoProxy := ""
	out := make([]string, 0, len(env)+6)
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			out = append(out, kv)
			continue
		}
		switch strings.ToUpper(k) {
		case "HTTP_PROXY", "HTTPS_PROXY":
			continue
		case "NO_PROXY":
			if v != "" {
				existingNoProxy = v
			}
			continue
		default:
			out = append(out, kv)
		}
	}
	noProxy := "127.0.0.1,localhost,::1"
	if existingNoProxy != "" {
		noProxy = existingNoProxy + "," + noProxy
	}
	out = append(out,
		"HTTP_PROXY="+proxyURL,
		"HTTPS_PROXY="+proxyURL,
		"http_proxy="+proxyURL,
		"https_proxy="+proxyURL,
		"NO_PROXY="+noProxy,
		"no_proxy="+noProxy,
	)
	return out
}

func (c Config) Session(selected string, write, checksum bool) (*Session, error) {
	mode := diskReadOnly
	if write {
		mode = diskAppendWrite
	}
	return c.SessionFor([]SessionSpec{{Name: selected, Mode: mode, Checksum: checksum}}, SessionOptions{})
}

// SessionFor creates an isolated rclone session containing all requested remotes
// and their transitive wrapper/union dependencies, mounting loopback os.Root
// WebDAV bridges for any local backing directories.
func (c Config) SessionFor(specs []SessionSpec, opts SessionOptions) (*Session, error) {
	dir, e := os.MkdirTemp(nativeTempDir(), "opensync-rclone-")
	if e != nil {
		return nil, e
	}
	_ = os.Chmod(dir, 0700)
	s := &Session{
		dir:                 dir,
		proxyURL:            strings.TrimSpace(opts.ProxyURL),
		insecureSkipVerify:  opts.InsecureSkipVerify,
		disableTokenPersist: opts.DisableTokenPersist,
		onTokenUpdate:       opts.OnTokenUpdate,
	}
	ok := false
	defer func() {
		if !ok {
			s.Close()
		}
	}()

	// Merge mode requirements per remote name (strictest access mode wins).
	modeByRemote := map[string]diskAccessMode{}
	checksumByRemote := map[string]bool{}
	var queue []string
	enqueue := func(name string, mode diskAccessMode, checksum bool) {
		if name == "" || strings.EqualFold(name, Local) {
			return
		}
		canonical, ok := c.FindKey(name)
		if !ok || c[canonical]["type"] == "local" {
			return
		}
		if prev, exists := modeByRemote[canonical]; !exists {
			modeByRemote[canonical] = mode
			checksumByRemote[canonical] = checksum
			queue = append(queue, canonical)
		} else {
			if mode > prev {
				modeByRemote[canonical] = mode
			}
			if checksum {
				checksumByRemote[canonical] = true
			}
		}
	}

	// First pass: collect transitive graph so every remote gets its maximum
	// required diskAccessMode before writing sections.
	for _, spec := range specs {
		enqueue(spec.Name, spec.Mode, spec.Checksum)
	}
	for i := 0; i < len(queue); i++ {
		n := queue[i]
		p := c[n]
		if p == nil {
			continue
		}
		curMode := modeByRemote[n]
		curChecksum := checksumByRemote[n]
		switch p["type"] {
		case "crypt", "chunker", "compress", "hasher", "alias", "archive":
			ref, _, cutOK := strings.Cut(p["remote"], ":")
			if cutOK && !c.isLocalName(ref) {
				enqueue(ref, curMode, curChecksum && (opts.PreserveCryptChecksum || p["type"] != "crypt"))
			}
		case "union":
			if ups, err := ParseUnionUpstreams(p["upstreams"]); err == nil {
				for _, up := range ups {
					ref, _, cutOK := strings.Cut(up.Target, ":")
					if cutOK && !c.isLocalName(ref) {
						upMode := curMode
						if up.Suffix == ":ro" {
							upMode = diskReadOnly
						}
						enqueue(ref, upMode, curChecksum)
					}
				}
			}
		case "combine":
			if ups, err := ParseCombineUpstreams(p["upstreams"]); err == nil {
				for _, up := range ups {
					ref, _, cutOK := strings.Cut(up.Target, ":")
					if cutOK && !c.isLocalName(ref) {
						enqueue(ref, curMode, curChecksum)
					}
				}
			}
		}
	}

	// Keep a private 0600 config file inside the 0700 temp dir when any remote
	// uses OAuth/session tokens that rclone refreshes by rewriting its config.
	// Built-in in-process drivers (like quark) record refreshes in memory and
	// never require writing secrets to disk.
	initialRemotes := Config{}
	needsWritableConfig := false
	for _, n := range queue {
		p := c[n]
		if p == nil {
			continue
		}
		if tracked := tokenRefreshableFields(p["type"]); len(tracked) > 0 {
			for _, field := range tracked {
				if val := strings.TrimSpace(p[field]); val != "" {
					initialRemotes[n] = maps.Clone(p)
					if p["type"] != "quark" {
						needsWritableConfig = true
					}
					break
				}
			}
		}
	}
	if len(initialRemotes) > 0 {
		s.initialRemotes = initialRemotes
	}
	if needsWritableConfig {
		configPath := filepath.Join(dir, "rclone.conf")
		f, err := os.OpenFile(configPath, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0600)
		if err != nil {
			return nil, err
		}
		s.configFile = f
		s.configPath = configPath
	} else {
		f, err := os.CreateTemp(dir, "config-")
		if err != nil {
			return nil, err
		}
		if err = os.Remove(f.Name()); err != nil {
			f.Close()
			return nil, err
		}
		s.configFile = f
	}

	var conf strings.Builder
	knownHostsIdx := 0
	var proxyOwner, proxyVal string
	var insecureOwners, leafNetRemotes []string
	for _, n := range queue {
		p := maps.Clone(c[n])
		if p == nil || p["type"] == "local" {
			continue
		}
		curMode := modeByRemote[n]
		curChecksum := checksumByRemote[n]

		if p["type"] == "quark" {
			remoteName := n
			qClient, err := newQuarkClientFromParams(p, opts, func(newCookie string) {
				s.recordInProcessTokenUpdate(remoteName, "cookie", newCookie)
			})
			if err != nil {
				return nil, err
			}
			// Always expose Quark's zero-cost native MD5 via <oc:checksums>
			// because /file/sort returns md5 in the listing response itself.
			davURL, err := s.mountBridgeDAV(&quarkBridgeFS{client: qClient}, curMode, true)
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(&conf, "[%s]\ntype = webdav\nvendor = owncloud\nnextcloud_chunk_size = 0\nurl = %s\n\n", n, davURL)
			continue
		}
		isComposite := IsWrapperType(p["type"]) || p["type"] == "union" || p["type"] == "combine"
		if !isComposite {
			leafNetRemotes = append(leafNetRemotes, n)
		}

		if p["insecure_skip_verify"] == "true" {
			s.insecureSkipVerify = true
			if !isComposite {
				insecureOwners = append(insecureOwners, n)
			}
		}
		if pu := strings.TrimSpace(p["proxy_url"]); pu != "" {
			if proxyVal != "" && proxyVal != pu {
				return nil, fmt.Errorf("同一次同步涉及的存储（%s 与 %s）配置了不同的专属代理，rclone 单进程暂不支持同时使用多个代理", proxyOwner, n)
			}
			proxyVal = pu
			proxyOwner = n
			s.proxyURL = pu
		}
		allowHashCheck := p["allow_hashcheck"] == "true"
		for metaKey := range openSyncMetaFields {
			delete(p, metaKey)
		}
		if p["type"] == "oracleobjectstorage" {
			// File paths from HTTP configuration remain forbidden. Only a path
			// configured by the application environment can provide OCI keys.
			file := strings.TrimSpace(os.Getenv("RCLONE_OOS_CONFIG_FILE"))
			if file == "" {
				file = strings.TrimSpace(os.Getenv("OCI_CONFIG_FILE"))
			}
			provider := p["provider"]
			if provider == "" {
				provider = strings.TrimSpace(os.Getenv("RCLONE_OOS_PROVIDER"))
				if provider != "" {
					p["provider"] = provider
				}
			}
			if provider == "user_principal_auth" || ((provider == "" || provider == "env_auth") && file != "") {
				if !filepath.IsAbs(file) {
					return nil, errors.New("OCI 用户主体认证需要在应用环境中设置 RCLONE_OOS_CONFIG_FILE 的绝对路径，私钥也需使用绝对路径")
				}
				st, err := os.Stat(file)
				if err != nil || !st.Mode().IsRegular() {
					return nil, errors.New("无法读取应用环境指定的 OCI 配置文件，请检查路径与权限")
				}
				p["config_file"] = file
				if profile := strings.TrimSpace(os.Getenv("RCLONE_OOS_CONFIG_PROFILE")); profile != "" {
					p["config_profile"] = profile
				}
				if provider == "" || provider == "env_auth" {
					// rclone's env_auth uses OCI's default provider, which ignores
					// the rclone config_file option. Select the file provider here.
					p["provider"] = "user_principal_auth"
				}
			}
		}

		if IsWrapperType(p["type"]) {
			ref, sub, _ := strings.Cut(p["remote"], ":")
			if ref == Local || (c[ref] != nil && c[ref]["type"] == "local") {
				pinned, e := s.diskWithMode(sub, curMode, curChecksum && (opts.PreserveCryptChecksum || p["type"] != "crypt"))
				if e != nil {
					return nil, e
				}
				p["remote"] = pinned
			}
		}
		if p["type"] == "union" {
			ups, err := ParseUnionUpstreams(p["upstreams"])
			if err != nil {
				return nil, err
			}
			for i := range ups {
				ref, sub, _ := strings.Cut(ups[i].Target, ":")
				if ref == Local || (c[ref] != nil && c[ref]["type"] == "local") {
					upMode := curMode
					if ups[i].Suffix == ":ro" {
						upMode = diskReadOnly
					}
					pinned, e := s.diskWithMode(sub, upMode, curChecksum)
					if e != nil {
						return nil, e
					}
					ups[i].Target = pinned
				}
			}
			p["upstreams"] = FormatUnionUpstreams(ups)
		}
		if p["type"] == "combine" {
			ups, err := ParseCombineUpstreams(p["upstreams"])
			if err != nil {
				return nil, err
			}
			for i := range ups {
				ref, sub, _ := strings.Cut(ups[i].Target, ":")
				if ref == Local || (c[ref] != nil && c[ref]["type"] == "local") {
					pinned, e := s.diskWithMode(sub, curMode, curChecksum)
					if e != nil {
						return nil, e
					}
					ups[i].Target = pinned
				}
			}
			p["upstreams"] = FormatCombineUpstreams(ups)
		}
		if p["type"] == "sftp" {
			if allowHashCheck {
				p["shell_type"] = "unix"
				delete(p, "disable_hashcheck")
			} else {
				p["shell_type"] = "none"
				p["disable_hashcheck"] = "true"
			}
			if line := p["known_hosts"]; line != "" {
				knownHostsIdx++
				file := filepath.Join(dir, fmt.Sprintf("known_hosts_%d", knownHostsIdx))
				if e = os.WriteFile(file, []byte(line+"\n"), 0600); e != nil {
					return nil, e
				}
				delete(p, "known_hosts")
				p["known_hosts_file"] = file
			}
		}

		fmt.Fprintf(&conf, "[%s]\n", n)
		for _, k := range slices.Sorted(maps.Keys(p)) {
			fmt.Fprintf(&conf, "%s = %s\n", k, p[k])
		}
		conf.WriteByte('\n')
	}
	if proxyOwner != "" && len(leafNetRemotes) > 1 {
		for _, leaf := range leafNetRemotes {
			if leaf != proxyOwner && strings.TrimSpace(c[leaf]["proxy_url"]) == "" {
				log.Printf("native session warning: remote %q sets proxy_url which applies to the whole rclone process (including %q)", proxyOwner, leaf)
			}
		}
	}
	if len(insecureOwners) > 0 && len(leafNetRemotes) > len(insecureOwners) {
		log.Printf("native session warning: insecure_skip_verify from %v applies process-wide across remotes %v", insecureOwners, leafNetRemotes)
	}
	if _, e = s.configFile.WriteString(conf.String()); e != nil {
		return nil, e
	}
	ok = true
	return s, nil
}
