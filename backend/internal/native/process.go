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
	"time"
)

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
		return errors.New("认证失败，请检查账号、密码、OAuth Token 或访问密钥是否正确")
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
	InsecureSkipVerify    bool
	PreserveCryptChecksum bool
}

type Session struct {
	dir                string
	closers            []func()
	configFile         *os.File
	proxyURL           string
	insecureSkipVerify bool
}

func (s *Session) Close() {
	for _, close := range s.closers {
		close()
	}
	s.configFile.Close()
	os.RemoveAll(s.dir)
}

func (s *Session) Cmd(ctx context.Context, args ...string) (*exec.Cmd, error) {
	// Reset file offset so platforms where /dev/fd/3 shares the open file
	// description (e.g. macOS local dev) always read the config from byte 0.
	if _, err := s.configFile.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	fullArgs := make([]string, 0, len(args)+4)
	fullArgs = append(fullArgs, "--config", "/dev/fd/3", "--ask-password=false")
	if s.insecureSkipVerify {
		fullArgs = append(fullArgs, "--no-check-certificate")
	}
	fullArgs = append(fullArgs, args...)
	cmd, err := command(ctx, fullArgs...)
	if err == nil {
		cmd.ExtraFiles = []*os.File{s.configFile}
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
	f, err := os.CreateTemp(dir, "config-")
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if err = os.Remove(f.Name()); err != nil {
		f.Close()
		os.RemoveAll(dir)
		return nil, err
	}
	s := &Session{
		dir:                dir,
		configFile:         f,
		proxyURL:           strings.TrimSpace(opts.ProxyURL),
		insecureSkipVerify: opts.InsecureSkipVerify,
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
		if name == "" || name == Local || c[name] == nil || c[name]["type"] == "local" {
			return
		}
		if prev, exists := modeByRemote[name]; !exists {
			modeByRemote[name] = mode
			checksumByRemote[name] = checksum
			queue = append(queue, name)
		} else {
			if mode > prev {
				modeByRemote[name] = mode
			}
			if checksum {
				checksumByRemote[name] = true
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
			if cutOK && ref != Local && c[ref] != nil && c[ref]["type"] != "local" {
				enqueue(ref, curMode, curChecksum && (opts.PreserveCryptChecksum || p["type"] != "crypt"))
			}
		case "union":
			if ups, err := ParseUnionUpstreams(p["upstreams"]); err == nil {
				for _, up := range ups {
					ref, _, cutOK := strings.Cut(up.Target, ":")
					if cutOK && ref != Local && c[ref] != nil && c[ref]["type"] != "local" {
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
					if cutOK && ref != Local && c[ref] != nil && c[ref]["type"] != "local" {
						enqueue(ref, curMode, curChecksum)
					}
				}
			}
		}
	}

	var conf strings.Builder
	knownHostsIdx := 0
	for _, n := range queue {
		p := maps.Clone(c[n])
		if p == nil || p["type"] == "local" {
			continue
		}
		curMode := modeByRemote[n]
		curChecksum := checksumByRemote[n]

		if p["insecure_skip_verify"] == "true" {
			s.insecureSkipVerify = true
		}
		if p["proxy_url"] != "" {
			s.proxyURL = p["proxy_url"]
		}
		allowHashCheck := p["allow_hashcheck"] == "true"
		for metaKey := range openSyncMetaFields {
			delete(p, metaKey)
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
	if _, e = s.configFile.WriteString(conf.String()); e != nil {
		return nil, e
	}
	ok = true
	return s, nil
}
