// Package native runs the bundled rclone with storage definitions owned by OpenSync.
package native

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

const EngineURL = "rclone://native"
const Local = "本地"

type Config map[string]map[string]string

var namePattern = regexp.MustCompile("^[\\p{L}\\p{N}_][\\p{L}\\p{N}_. -]{0,63}$")
var regexpKey = regexp.MustCompile("^[a-z][a-z0-9_]*$")

var fields = map[string]string{
	"local":  "description",
	"s3":     "provider env_auth access_key_id secret_access_key session_token region endpoint location_constraint acl storage_class force_path_style no_check_bucket v2_auth list_version chunk_size upload_cutoff upload_concurrency disable_http2 server_side_encryption sse_kms_key_id directory_markers leave_parts_on_error description",
	"webdav": "url vendor user pass bearer_token pacer_min_sleep nextcloud_chunk_size description",
	"sftp":   "host user port pass key_pem key_file_pass shell_type disable_hashcheck known_hosts description",
	"smb":    "host user port pass domain hide_special_share case_insensitive idle_timeout description",
	"crypt":  "remote password password2 filename_encryption directory_name_encryption filename_encoding suffix no_data_encryption description",
}

// SecretFields lists parameter keys whose values must never be echoed back to
// clients after storage.
var SecretFields = []string{
	"secret_access_key",
	"session_token",
	"pass",
	"bearer_token",
	"key_pem",
	"key_file_pass",
	"password",
	"password2",
}

// ObscuredFields lists parameter keys stored using rclone's password obfuscation.
var ObscuredFields = []string{
	"pass",
	"password",
	"password2",
	"key_file_pass",
}

func IsSecretField(key string) bool {
	return slices.Contains(SecretFields, key)
}

func IsObscuredField(key string) bool {
	return slices.Contains(ObscuredFields, key)
}

func IsSupportedType(typ string) bool {
	_, ok := fields[typ]
	return ok
}

func IsRemoteFormType(typ string) bool {
	return typ == "s3" || typ == "webdav" || typ == "sftp" || typ == "smb" || typ == "crypt"
}

func ValidName(s string) bool {
	return validName(s)
}

func validName(s string) bool {
	return namePattern.MatchString(s) && strings.TrimSpace(s) == s && !strings.HasPrefix(strings.ToLower(s), "_opensync_") && !strings.EqualFold(s, Local) && !strings.EqualFold(s, "RCLONE_CONFIG")
}

// FindKey performs a case-insensitive lookup matching Validate's uniqueness rule.
func (c Config) FindKey(name string) (string, bool) {
	if _, ok := c[name]; ok {
		return name, true
	}
	for k := range c {
		if strings.EqualFold(k, name) {
			return k, true
		}
	}
	return "", false
}

// normalizeKnownHosts cleans multiline known_hosts text by stripping blank
// lines and '#' comment lines while keeping every host-key entry separated by '\n'.
func normalizeKnownHosts(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func validateKnownHosts(raw string) error {
	cleaned := normalizeKnownHosts(raw)
	if cleaned == "" {
		return errors.New("SSH 主机公钥为空或仅包含注释行")
	}
	for _, line := range strings.Split(cleaned, "\n") {
		if strings.IndexFunc(line, unicode.IsControl) >= 0 {
			return errors.New("SSH 主机公钥包含非法控制字符")
		}
		parts := strings.Fields(line)
		offset := 0
		if len(parts) > 0 && strings.HasPrefix(parts[0], "@") {
			offset = 1
		}
		if len(parts) < offset+3 {
			return errors.New("SSH 主机公钥格式无效，请提供标准的 known_hosts 记录（主机 算法 公钥）")
		}
		keyType := parts[offset+1]
		if !strings.HasPrefix(keyType, "ssh-") &&
			!strings.HasPrefix(keyType, "ecdsa-") &&
			!strings.HasPrefix(keyType, "sk-") {
			return fmt.Errorf("SSH 主机公钥包含未知密钥类型 %q", keyType)
		}
		if _, err := base64.StdEncoding.DecodeString(parts[offset+2]); err != nil {
			return errors.New("SSH 主机公钥的 Base64 内容无效")
		}
	}
	return nil
}

// NormalizeParams trims non-secret values, normalizes inline PEM newlines,
// multiline known_hosts, trailing path slashes, and drops empty keys so blank
// inputs are never persisted or emitted into temporary rclone configs.
func NormalizeParams(raw map[string]string) map[string]string {
	out := make(map[string]string, len(raw))
	typ := strings.TrimSpace(raw["type"])
	if typ != "" {
		out["type"] = typ
	}
	for k, v := range raw {
		key := strings.TrimSpace(k)
		if key == "" || key == "type" {
			continue
		}
		if IsObscuredField(key) {
			if v != "" {
				out[key] = v
			}
			continue
		}
		if key == "key_pem" {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			v = strings.ReplaceAll(v, "\r\n", "\n")
			v = strings.ReplaceAll(v, "\r", "\n")
			v = strings.ReplaceAll(v, "\n", `\n`)
			out[key] = v
			continue
		}
		if key == "known_hosts" {
			v = normalizeKnownHosts(v)
			if v != "" {
				out[key] = v
			}
			continue
		}
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if typ == "s3" && key == "endpoint" {
			v = strings.TrimRight(v, "/")
		}
		if typ == "crypt" && key == "remote" {
			v = normalizeCryptRemote(v)
		}
		if v != "" {
			out[key] = v
		}
	}
	return out
}

func normalizeCryptRemote(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "/") {
		if raw == "/" {
			return raw
		}
		return strings.TrimRight(raw, "/")
	}
	if ref, sub, ok := strings.Cut(raw, ":"); ok {
		ref = strings.TrimSpace(ref)
		sub = strings.TrimSpace(sub)
		if sub != "/" {
			sub = strings.TrimRight(sub, "/")
		}
		return ref + ":" + sub
	}
	return raw
}

// RedactParams separates public display parameters from configured secret keys.
func RedactParams(p map[string]string) (public map[string]string, secrets []string) {
	public = map[string]string{}
	for _, k := range slices.Sorted(maps.Keys(p)) {
		v := p[k]
		if v == "" || k == "type" {
			continue
		}
		if IsSecretField(k) {
			secrets = append(secrets, k)
			continue
		}
		public[k] = v
	}
	if secrets == nil {
		secrets = []string{}
	}
	return public, secrets
}

// RemoteSummary produces a concise, non-sensitive summary string for UI cards.
func RemoteSummary(p map[string]string) string {
	if p == nil {
		return ""
	}
	switch p["type"] {
	case "local":
		return "本地授权目录"
	case "s3":
		var parts []string
		if v := strings.TrimSpace(p["provider"]); v != "" {
			parts = append(parts, v)
		}
		if v := strings.TrimSpace(p["endpoint"]); v != "" {
			parts = append(parts, v)
		}
		if v := strings.TrimSpace(p["region"]); v != "" {
			parts = append(parts, v)
		}
		return strings.Join(parts, " · ")
	case "webdav":
		urlStr := strings.TrimSpace(p["url"])
		user := strings.TrimSpace(p["user"])
		if user != "" && urlStr != "" {
			return urlStr + " · " + user
		}
		return urlStr
	case "sftp", "smb":
		host := strings.TrimSpace(p["host"])
		port := strings.TrimSpace(p["port"])
		user := strings.TrimSpace(p["user"])
		domain := strings.TrimSpace(p["domain"])
		addr := host
		if port != "" {
			addr = host + ":" + port
		}
		if user != "" {
			if p["type"] == "smb" && domain != "" {
				return domain + `\` + user + "@" + addr
			}
			return user + "@" + addr
		}
		return addr
	case "crypt":
		if r := strings.TrimSpace(p["remote"]); r != "" {
			return "底层 " + r
		}
		return ""
	default:
		return p["type"]
	}
}

func ParseINI(input string) (Config, error) {
	cfg := Config{}
	var section map[string]string
	for i, line := range strings.Split(strings.TrimPrefix(input, "\uFEFF"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			name := strings.TrimSpace(line[1 : len(line)-1])
			if !validName(name) {
				return nil, fmt.Errorf("第 %d 行的存储名称无效", i+1)
			}
			if _, dup := cfg.FindKey(name); dup {
				return nil, fmt.Errorf("第 %d 行的存储名称重复", i+1)
			}
			section = map[string]string{}
			cfg[name] = section
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || section == nil || key == "" {
			return nil, fmt.Errorf("第 %d 行不是有效的 rclone.conf 配置", i+1)
		}
		if _, dup := section[key]; dup {
			return nil, fmt.Errorf("第 %d 行存在重复字段", i+1)
		}
		section[key] = strings.TrimSpace(value)
	}
	if len(cfg) == 0 {
		return nil, errors.New("配置中没有命名存储")
	}
	return cfg, nil
}

func Decode(stored string) (Config, error) {
	var c Config
	if err := json.Unmarshal([]byte(stored), &c); err != nil || c == nil {
		return nil, errors.New("原生引擎配置不可读")
	}
	return c, c.Validate()
}

// Merge validates the whole dependency graph before any configuration is committed.
func (c Config) Merge(in Config) (Config, error) {
	merged := Config{}
	for name, params := range c {
		merged[name] = maps.Clone(params)
	}
	for name, params := range in {
		if existing, ok := merged.FindKey(name); ok {
			return nil, fmt.Errorf("存储 %q 已存在，请使用其他名称", existing)
		}
		merged[name] = maps.Clone(params)
	}
	return merged, merged.Validate()
}

func cleanRelative(s string) bool {
	return !strings.ContainsAny(s, "\\\r\n\x00") && !strings.HasPrefix(s, "/") && (s == "" || path.Clean(s) == s && s != "." && s != ".." && !strings.HasPrefix(s, "../"))
}

func disallowedFieldError(name, typ, field string) error {
	switch {
	case typ == "sftp" && field == "known_hosts_file":
		return fmt.Errorf("存储 %q 出于安全限制不支持文件路径 known_hosts_file，请改用内嵌公钥 known_hosts", name)
	case typ == "sftp" && field == "key_file":
		return fmt.Errorf("存储 %q 出于安全限制不支持本地私钥路径 key_file，请改用内嵌私钥 key_pem", name)
	case strings.HasSuffix(field, "_command") || field == "ssh" || field == "credential_process":
		return fmt.Errorf("存储 %q 的字段 %q 受安全限制不支持", name, field)
	default:
		return fmt.Errorf("存储 %q 含不支持的字段 %q", name, field)
	}
}

func validateObscuredFields(name string, p map[string]string) error {
	for _, k := range ObscuredFields {
		if p[k] != "" {
			decoded, err := base64.RawURLEncoding.DecodeString(p[k])
			if err != nil || len(decoded) < 16 {
				return fmt.Errorf("%s 的 %s 需要 rclone 配置中的混淆值", name, k)
			}
		}
	}
	return nil
}

// ValidateEntry validates one storage definition before network/rclone
// preparation or cross-remote reference resolution. When plainSecrets is true,
// password fields are accepted as raw plaintext (to be obscured by Prepare) and
// SFTP known_hosts may be empty (to be captured by Prepare).
func ValidateEntry(name string, p map[string]string, plainSecrets bool) error {
	if !validName(name) {
		return fmt.Errorf("存储名称 %q 无效（1–64 个字符，不能使用保留名或首尾空格）", name)
	}
	typ := p["type"]
	allowed, ok := fields[typ]
	if !ok {
		return fmt.Errorf("存储 %q 的后端类型不受支持", name)
	}
	for k, v := range p {
		if k != "type" && !strings.Contains(" "+allowed+" ", " "+k+" ") {
			return disallowedFieldError(name, typ, k)
		}
		if !regexpKey.MatchString(k) || len(v) > 65536 {
			return fmt.Errorf("存储 %q 的字段 %q 格式无效", name, k)
		}
		if k == "known_hosts" {
			// Multiline known_hosts is validated separately below.
			continue
		}
		if strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return fmt.Errorf("存储 %q 的字段 %q 格式无效", name, k)
		}
	}
	switch typ {
	case "s3":
		if p["access_key_id"] == "" || p["secret_access_key"] == "" || (p["env_auth"] != "" && p["env_auth"] != "false") {
			return fmt.Errorf("%s 需要显式填写 S3 Access Key ID 和 Secret Access Key", name)
		}
		if p["endpoint"] != "" && !validS3Endpoint(p["endpoint"]) {
			return fmt.Errorf("%s 的 S3 端点无效（请填写如 oss-cn-hangzhou.aliyuncs.com 或 https://...）", name)
		}
		if v := p["force_path_style"]; v != "" && v != "true" && v != "false" {
			return fmt.Errorf("%s 的 force_path_style 必须为 true 或 false", name)
		}
		if v := p["no_check_bucket"]; v != "" && v != "true" && v != "false" {
			return fmt.Errorf("%s 的 no_check_bucket 必须为 true 或 false", name)
		}
	case "webdav":
		if !validURL(p["url"]) {
			return fmt.Errorf("%s 的 WebDAV 地址无效（请填写有效的 HTTP/HTTPS 地址）", name)
		}
	case "sftp", "smb":
		if p["host"] == "" || strings.ContainsAny(p["host"], "/\\@ \t") || p["user"] == "" {
			return fmt.Errorf("%s 需要填写有效的主机地址和用户名", name)
		}
		if v := p["port"]; v != "" {
			n, e := strconv.Atoi(v)
			if e != nil || n < 1 || n > 65535 {
				return fmt.Errorf("%s 的端口无效（应为 1–65535）", name)
			}
		}
		if typ == "sftp" {
			if p["known_hosts"] != "" {
				if err := validateKnownHosts(p["known_hosts"]); err != nil {
					return fmt.Errorf("%s：%s", name, err.Error())
				}
			} else if !plainSecrets {
				return fmt.Errorf("%s 缺少已固定的 SSH 主机公钥", name)
			}
			if p["pass"] == "" && p["key_pem"] == "" {
				return fmt.Errorf("%s 需要填写密码或内嵌 SSH 私钥", name)
			}
			if p["shell_type"] != "" && p["shell_type"] != "none" {
				return fmt.Errorf("%s 仅支持 SFTP 文件协议", name)
			}
		}
	case "crypt":
		if p["password"] == "" {
			return fmt.Errorf("%s 缺少加密密码", name)
		}
		if strings.TrimSpace(p["remote"]) == "" {
			return fmt.Errorf("%s 缺少底层路径", name)
		}
	}
	if !plainSecrets {
		return validateObscuredFields(name, p)
	}
	return nil
}

func (c Config) Validate() error {
	if len(c) > 128 {
		return errors.New("最多支持 128 个命名存储")
	}
	names := map[string]bool{}
	for name, p := range c {
		lower := strings.ToLower(name)
		if !validName(name) || names[lower] {
			return errors.New("存储名称无效或重复")
		}
		names[lower] = true
		if err := ValidateEntry(name, p, false); err != nil {
			return err
		}
		if p["type"] == "crypt" {
			ref, sub, ok := strings.Cut(p["remote"], ":")
			if !ok || !cleanRelative(sub) || (ref != Local && c[ref] == nil) {
				return fmt.Errorf("%s 的底层路径必须引用已配置的存储或本地授权目录", name)
			}
			if ref == Local && sub == "" {
				return errors.New("Crypt 需要指定一个本地授权目录")
			}
		}
	}
	for name := range c {
		seen := map[string]bool{}
		for ref := name; c[ref]["type"] == "crypt"; {
			if seen[ref] {
				return fmt.Errorf("%s 存在 Crypt 循环引用", name)
			}
			seen[ref] = true
			ref, _, _ = strings.Cut(c[ref]["remote"], ":")
		}
	}
	return nil
}

func validURL(s string) bool {
	u, e := url.Parse(s)
	if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	if strings.HasSuffix(u.Host, ":") {
		return false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
	}
	return true
}

// validS3Endpoint accepts either an explicit HTTP(S) URL or a bare host[:port]
// matching rclone's native S3 endpoint format (e.g. oss-cn-hangzhou.aliyuncs.com).
func validS3Endpoint(s string) bool {
	if s == "" || strings.IndexFunc(s, unicode.IsSpace) >= 0 || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return false
	}
	if strings.Contains(s, "://") {
		return validURL(s)
	}
	if strings.ContainsAny(s, "/?#@\\") {
		return false
	}
	return validURL("https://" + s)
}

func Prepare(ctx context.Context, existing, additions Config, plain bool) error {
	return PrepareWithSkipObscure(ctx, existing, additions, plain, nil)
}

// PrepareWithSkipObscure normalizes incoming entries, validates their basic
// shape before doing any network/rclone work, captures missing SFTP host keys,
// obscures newly provided plaintext secrets, and translates local crypt paths
// while consulting both additions and existing stored remotes.
func PrepareWithSkipObscure(ctx context.Context, existing, additions Config, plain bool, skipObscure map[string]map[string]bool) error {
	for name, raw := range additions {
		p := NormalizeParams(raw)
		if p["type"] == "sftp" && p["shell_type"] == "unix" {
			// rclone defaults SFTP remotes to shell_type=unix; normalize to none
			// because OpenSync strictly uses pure SFTP file operations.
			p["shell_type"] = "none"
		}
		additions[name] = p

		// Validate structure & required fields before dialing SFTP or spawning rclone.
		if err := ValidateEntry(name, p, true); err != nil {
			return err
		}
		if !plain {
			if err := validateObscuredFields(name, p); err != nil {
				return err
			}
		}

		if p["type"] == "sftp" && p["known_hosts"] == "" {
			line, err := firstHostKey(ctx, p["host"], p["port"])
			if err != nil {
				return err
			}
			p["known_hosts"] = line
		}
		if plain {
			for _, key := range ObscuredFields {
				if p[key] == "" || (skipObscure != nil && skipObscure[name][key]) {
					continue
				}
				if len(p[key]) > 65536 || strings.IndexFunc(p[key], unicode.IsControl) >= 0 {
					return errors.New("密码不能包含控制字符或超过长度限制")
				}
				cmd, e := command(ctx, "obscure", "-")
				if e != nil {
					return e
				}
				cmd.Stdin = strings.NewReader(p[key])
				out, e := cmd.Output()
				if e != nil {
					return errors.New("处理存储密码失败")
				}
				p[key] = strings.TrimSpace(string(out))
			}
		}
		if p["type"] == "crypt" {
			target := normalizeCryptRemote(p["remote"])
			if alias, sub, ok := strings.Cut(target, ":"); ok && strings.HasPrefix(sub, "/") {
				isLocalAlias := false
				if actual, found := additions.FindKey(alias); found && additions[actual]["type"] == "local" {
					isLocalAlias = true
				} else if existing != nil {
					if actual, found := existing.FindKey(alias); found && existing[actual]["type"] == "local" {
						isLocalAlias = true
					}
				}
				if isLocalAlias {
					target = sub
				}
			}
			if strings.HasPrefix(target, "/") {
				v, e := virtualLocal(target)
				if e != nil {
					return e
				}
				p["remote"] = Local + ":" + strings.TrimPrefix(v, "/"+Local+"/")
			} else if ref, sub, ok := strings.Cut(target, ":"); ok && ref != Local {
				if actual, found := additions.FindKey(ref); found {
					p["remote"] = actual + ":" + sub
				} else if existing != nil {
					if actual, found := existing.FindKey(ref); found {
						p["remote"] = actual + ":" + sub
					} else {
						p["remote"] = target
					}
				} else {
					p["remote"] = target
				}
			} else {
				p["remote"] = target
			}
		}
	}
	return nil
}
