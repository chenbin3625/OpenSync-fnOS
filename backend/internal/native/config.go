// Package native runs the bundled rclone with storage definitions owned by OpenSync.
package native

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"opensync/internal/pan123"
	"opensync/internal/quark"
)

const EngineURL = "rclone://native"
const Local = "本地"

// RedactedSecretPlaceholder is written when exporting a non-secret system backup.
const RedactedSecretPlaceholder = "***REDACTED***"

type Config map[string]map[string]string

var namePattern = regexp.MustCompile("^[\\p{L}\\p{N}_][\\p{L}\\p{N}_. -]{0,63}$")
var regexpKey = regexp.MustCompile("^[a-z0-9][a-zA-Z0-9_]*$")

var (
	bwRatePartPattern      = regexp.MustCompile(`^(?i:off|0|[0-9]+(?:\.[0-9]+)?(?:b|[kmgtp](?:i|ib)?)?)$`)
	bwScheduleTokenPattern = regexp.MustCompile(`^(?:(?i:mon|tue|wed|thu|fri|sat|sun)-)?(?:[01]?[0-9]|2[0-3]):[0-5][0-9],(.+)$`)
)

// supportedRemoteOrder defines the canonical ordering of remote backend types
// exposed to clients.
var supportedRemoteOrder = []string{
	"s3",
	"webdav",
	"quark",
	"pan123",
	"sftp",
	"smb",
	"ftp",
	"http",
	"b2",
	"onedrive",
	"drive",
	"dropbox",
	"box",
	"pcloud",
	"mega",
	"azureblob",
	"azurefiles",
	"googlecloudstorage",
	"swift",
	"oracleobjectstorage",
	"qingstor",
	"storj",
	"yandex",
	"koofr",
	"seafile",
	"pikpak",
	"protondrive",
	"iclouddrive",
	"jottacloud",
	"filen",
	"hidrive",
	"huaweidrive",
	"mailru",
	"fichier",
	"gofile",
	"pixeldrain",
	"crypt",
	"chunker",
	"compress",
	"hasher",
	"alias",
	"archive",
	"union",
	"combine",
}

// openSyncMetaFields are OpenSync-owned per-remote options stripped before
// emitting standard rclone INI sections or exporting rclone.conf.
var openSyncMetaFields = map[string]bool{
	"insecure_skip_verify": true,
	"proxy_url":            true,
	"allow_hashcheck":      true,
	"max_concurrent_jobs":  true,
	"managed_by_job":       true,
	"managed_job_label":    true,
}

var fields = map[string]string{
	"local":               "description",
	"s3":                  "provider env_auth access_key_id secret_access_key session_token region endpoint location_constraint acl storage_class force_path_style no_check_bucket v2_auth list_version chunk_size upload_cutoff upload_concurrency disable_http2 server_side_encryption sse_kms_key_id directory_markers leave_parts_on_error upload_checksum_algorithm no_head no_head_object insecure_skip_verify proxy_url description",
	"webdav":              "url vendor user pass bearer_token pacer_min_sleep nextcloud_chunk_size headers encoding insecure_skip_verify proxy_url description",
	"quark":               "cookie root_folder_id account_name upload_concurrency part_size tps_limit user_agent insecure_skip_verify proxy_url description",
	"pan123":              "protocol client_id client_secret access_token token_expires_at web_user web_password platform use_direct_link direct_link_key direct_link_ttl root_folder_id account_name upload_concurrency tps_limit user_agent insecure_skip_verify proxy_url description",
	"sftp":                "host user port pass key_pem key_file_pass shell_type disable_hashcheck allow_hashcheck known_hosts idle_timeout concurrency chunk_size use_insecure_cipher set_modtime description",
	"smb":                 "host user port pass domain spn hide_special_share case_insensitive idle_timeout description",
	"ftp":                 "host user port pass tls explicit_tls disable_tls13 concurrency idle_timeout no_check_certificate shut_timeout writing_mdtm force_list_hidden insecure_skip_verify description",
	"http":                "url headers no_head no_slash insecure_skip_verify proxy_url description",
	"b2":                  "account key endpoint hard_delete chunk_size upload_cutoff copy_cutoff download_url memory_pool_flush_time insecure_skip_verify proxy_url description",
	"onedrive":            "token drive_id drive_type root_folder_id client_id client_secret region tenant auth_url token_url chunk_size expose_onenote_files server_side_across_configs insecure_skip_verify proxy_url description",
	"drive":               "token client_id client_secret auth_url token_url scope root_folder_id team_drive service_account_credentials shared_with_me trashed_only chunk_size upload_cutoff ack_abuse server_side_across_configs insecure_skip_verify proxy_url description",
	"dropbox":             "token client_id client_secret auth_url token_url chunk_size shared_files shared_folders impersonate insecure_skip_verify proxy_url description",
	"box":                 "token client_id client_secret auth_url token_url root_folder_id box_sub_type owned_by insecure_skip_verify proxy_url description",
	"pcloud":              "token client_id client_secret auth_url token_url hostname username password root_folder_id insecure_skip_verify proxy_url description",
	"mega":                "user pass debug hard_delete use_https insecure_skip_verify proxy_url description",
	"azureblob":           "account key sas_url endpoint tenant client_id client_secret access_tier archive_tier chunk_size upload_cutoff no_check_container insecure_skip_verify proxy_url description",
	"azurefiles":          "account env_auth key sas_url connection_string tenant client_id client_secret username password endpoint share_name chunk_size upload_concurrency max_stream_size insecure_skip_verify proxy_url description",
	"googlecloudstorage":  "project_number service_account_credentials token client_id client_secret auth_url token_url location storage_class bucket_policy_only object_acl bucket_acl anonymous endpoint insecure_skip_verify proxy_url description",
	"swift":               "user key auth domain tenant tenant_id tenant_domain region storage_url auth_token application_credential_id application_credential_name application_credential_secret auth_version chunk_size no_chunk insecure_skip_verify proxy_url description",
	"oracleobjectstorage": "provider namespace compartment region endpoint storage_tier upload_cutoff chunk_size upload_concurrency leave_parts_on_error attempt_resume_upload no_check_bucket insecure_skip_verify proxy_url description",
	"qingstor":            "env_auth access_key_id secret_access_key endpoint zone connection_retries upload_cutoff chunk_size upload_concurrency insecure_skip_verify proxy_url description",
	"storj":               "provider access_grant satellite_address api_key passphrase insecure_skip_verify proxy_url description",
	"yandex":              "token client_id client_secret auth_url token_url hard_delete spoof_ua upload_wait app_folder insecure_skip_verify proxy_url description",
	"koofr":               "provider endpoint mountid setmtime user password insecure_skip_verify proxy_url description",
	"seafile":             "url user pass 2fa library library_key create_library auth_token insecure_skip_verify proxy_url description",
	"pikpak":              "user pass device_id user_agent root_folder_id use_trash trashed_only no_media_link upload_cutoff chunk_size upload_concurrency insecure_skip_verify proxy_url description",
	"protondrive":         "username password mailbox_password otp_secret_key client_uid client_access_token client_refresh_token client_salted_key_pass replace_existing_draft enable_caching insecure_skip_verify proxy_url description",
	"iclouddrive":         "service apple_id password trust_token cookies client_id insecure_skip_verify proxy_url description",
	"jottacloud":          "token tokenURL configVersion device mountpoint client_id client_secret auth_url token_url md5_memory_limit trashed_only hard_delete upload_resume_limit no_versions insecure_skip_verify proxy_url description",
	"filen":               "email password api_key upload_concurrency master_keys private_key public_key auth_version base_folder_uuid insecure_skip_verify proxy_url description",
	"hidrive":             "token client_id client_secret auth_url token_url scope_access scope_role root_prefix endpoint chunk_size upload_cutoff upload_concurrency insecure_skip_verify proxy_url description",
	"huaweidrive":         "token client_id client_secret auth_url token_url root_folder_id chunk_size list_chunk upload_cutoff insecure_skip_verify proxy_url description",
	"mailru":              "token user pass speedup_enable check_hash user_agent insecure_skip_verify proxy_url description",
	"fichier":             "api_key shared_folder file_password folder_password cdn insecure_skip_verify proxy_url description",
	"gofile":              "access_token root_folder_id account_id list_chunk insecure_skip_verify proxy_url description",
	"pixeldrain":          "api_key root_folder_id api_url insecure_skip_verify proxy_url description",
	"crypt":               "remote password password2 filename_encryption directory_name_encryption filename_encoding suffix no_data_encryption description managed_by_job managed_job_label",
	"chunker":             "remote chunk_size name_format start_from meta_format hash_type fail_hard transactions description",
	"compress":            "remote mode level ram_cache_limit description",
	"hasher":              "remote hashes max_age auto_size description",
	"alias":               "remote description",
	"archive":             "remote description",
	"union":               "upstreams action_policy create_policy search_policy cache_time min_free_space description",
	"combine":             "upstreams description",
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
	"token",
	"client_secret",
	"key",
	"service_account_credentials",
	"sas_url",
	"auth_token",
	"application_credential_secret",
	"access_grant",
	"api_key",
	"passphrase",
	"library_key",
	"connection_string",
	"mailbox_password",
	"otp_secret_key",
	"client_access_token",
	"client_refresh_token",
	"client_salted_key_pass",
	"trust_token",
	"cookies",
	"cookie",
	"master_keys",
	"private_key",
	"file_password",
	"folder_password",
	"access_token",
	"web_password",
	"direct_link_key",
}

// ObscuredFields lists parameter keys stored using rclone's password obfuscation
// across all backends.
var ObscuredFields = []string{
	"pass",
	"password",
	"password2",
	"key_file_pass",
	"library_key",
	"mailbox_password",
	"otp_secret_key",
	"file_password",
	"folder_password",
}

func IsSecretField(key string) bool {
	return slices.Contains(SecretFields, key)
}

func IsObscuredField(key string) bool {
	return slices.Contains(ObscuredFields, key)
}

func IsObscuredFieldForType(typ, key string) bool {
	return IsObscuredField(key) || (typ == "filen" && key == "api_key")
}

func IsOpenSyncMetaField(key string) bool {
	return openSyncMetaFields[key]
}

func IsSupportedType(typ string) bool {
	_, ok := fields[typ]
	return ok
}

func IsRemoteFormType(typ string) bool {
	return typ != "" && typ != "local" && IsSupportedType(typ)
}

func SupportedRemoteTypes() []string {
	return slices.Clone(supportedRemoteOrder)
}

func IsWrapperType(typ string) bool {
	switch typ {
	case "crypt", "chunker", "compress", "hasher", "alias", "archive":
		return true
	default:
		return false
	}
}

func IsCompositeType(typ string) bool {
	return IsWrapperType(typ) || typ == "union" || typ == "combine"
}

func ValidName(s string) bool {
	return validName(s)
}

func validName(s string) bool {
	return namePattern.MatchString(s) && strings.TrimSpace(s) == s && !strings.HasPrefix(strings.ToLower(s), "_opensync_") && !strings.EqualFold(s, Local) && !strings.EqualFold(s, "RCLONE_CONFIG")
}

// ValidateBwLimit validates an rclone --bwlimit value (empty, single rate, up:down, or timetable).
func ValidateBwLimit(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if len(trimmed) > 1024 || strings.HasPrefix(trimmed, "-") || strings.IndexFunc(trimmed, unicode.IsControl) >= 0 {
		return errors.New("限速格式无效，支持如 10M、500K、10M:2M 或分时段规则 08:00,5M 23:00,off")
	}
	tokens := strings.Fields(trimmed)
	if len(tokens) > 128 {
		return errors.New("限速格式无效，支持如 10M、500K、10M:2M 或分时段规则 08:00,5M 23:00,off")
	}
	validRate := func(spec string) bool {
		if spec == "" {
			return false
		}
		if up, down, hasColon := strings.Cut(spec, ":"); hasColon {
			if strings.Contains(down, ":") {
				return false
			}
			return bwRatePartPattern.MatchString(up) && bwRatePartPattern.MatchString(down)
		}
		return bwRatePartPattern.MatchString(spec)
	}
	if len(tokens) == 1 && !strings.Contains(tokens[0], ",") {
		if !validRate(tokens[0]) {
			return errors.New("限速格式无效，支持如 10M、500K、10M:2M 或分时段规则 08:00,5M 23:00,off")
		}
		return nil
	}
	for _, tok := range tokens {
		m := bwScheduleTokenPattern.FindStringSubmatch(tok)
		if m == nil || !validRate(m[1]) {
			return errors.New("限速格式无效，支持如 10M、500K、10M:2M 或分时段规则 08:00,5M 23:00,off")
		}
	}
	return nil
}

// ValidateProxyURL validates an optional outbound HTTP/HTTPS/SOCKS5 proxy URL.
func ValidateProxyURL(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if len(trimmed) > 512 || strings.IndexFunc(trimmed, unicode.IsSpace) >= 0 || strings.IndexFunc(trimmed, unicode.IsControl) >= 0 {
		return errors.New("代理地址无效，仅支持 http://、https://、socks5:// 或 socks5h:// URL")
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Hostname() == "" {
		return errors.New("代理地址无效，仅支持 http://、https://、socks5:// 或 socks5h:// URL")
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return errors.New("代理地址无效，仅支持 http://、https://、socks5:// 或 socks5h:// URL")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return errors.New("代理地址端口无效（应为 1–65535）")
		}
	}
	return nil
}

// ValidateModifyWindow validates an optional rclone --modify-window Go duration.
func ValidateModifyWindow(value string) error {
	return ValidateDurationSpec(value, 24*time.Hour, "时间精度容差格式无效，请使用如 1s、2s、100ms、1m（最大 24h）")
}

// ValidateDurationSpec validates an optional positive Go/rclone duration up to maxDur.
func ValidateDurationSpec(value string, maxDur time.Duration, errMsg string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if len(trimmed) > 32 || strings.HasPrefix(trimmed, "-") || strings.IndexFunc(trimmed, unicode.IsControl) >= 0 {
		return errors.New(errMsg)
	}
	d, err := time.ParseDuration(trimmed)
	if err != nil || d <= 0 || (maxDur > 0 && d > maxDur) {
		return errors.New(errMsg)
	}
	return nil
}

// ValidateRcloneSize validates an optional rclone SizeSuffix value (e.g. 16M, 1G, 500K).
func ValidateRcloneSize(value string, allowZero bool, errMsg string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if len(trimmed) > 32 || strings.HasPrefix(trimmed, "-") || strings.IndexFunc(trimmed, unicode.IsControl) >= 0 || strings.ContainsAny(trimmed, " \t") {
		return errors.New(errMsg)
	}
	if strings.EqualFold(trimmed, "off") || trimmed == "0" {
		if allowZero {
			return nil
		}
		return errors.New(errMsg)
	}
	if !bwRatePartPattern.MatchString(trimmed) {
		return errors.New(errMsg)
	}
	return nil
}

// ValidateCutoffMode validates rclone --cutoff-mode values.
func ValidateCutoffMode(value string) error {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	switch trimmed {
	case "", "hard", "soft", "cautious":
		return nil
	default:
		return errors.New("超限停止模式仅支持 hard、soft 或 cautious")
	}
}

// ValidateOrderBy validates rclone --order-by specifications.
func ValidateOrderBy(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if len(trimmed) > 128 || strings.IndexFunc(trimmed, unicode.IsControl) >= 0 || strings.ContainsAny(trimmed, " \t") {
		return errors.New("传输排序规则格式无效，如 size,asc、modtime,desc、name,asc")
	}
	for _, clause := range strings.Split(trimmed, ":") {
		if clause == "" {
			return errors.New("传输排序规则格式无效")
		}
		field, arg, hasArg := strings.Cut(clause, ",")
		switch strings.ToLower(field) {
		case "size", "modtime", "name":
			if hasArg {
				switch strings.ToLower(arg) {
				case "asc", "ascending", "desc", "descending":
				default:
					return errors.New("排序方向仅支持 asc 或 desc")
				}
			}
		case "mixed":
			if hasArg {
				n, err := strconv.Atoi(arg)
				if err != nil || n < 0 || n > 100 {
					return errors.New("mixed 排序比例需为 0–100 的整数")
				}
			}
		default:
			return errors.New("排序字段仅支持 size、modtime、name 或 mixed")
		}
	}
	return nil
}

// ValidateUserAgent validates an optional HTTP User-Agent string.
func ValidateUserAgent(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	if len(trimmed) > 256 || strings.IndexFunc(trimmed, unicode.IsControl) >= 0 {
		return errors.New("User-Agent 长度不能超过 256 且不能包含控制字符")
	}
	return nil
}

// UnionUpstream represents one entry in an rclone union backend's upstreams field.
type UnionUpstream struct {
	Target string // e.g. "remote:sub" or "/abs/local/path"
	Suffix string // "", ":ro", or ":nc"
}

// ParseUnionUpstreams parses rclone's whitespace-separated (optionally quoted)
// upstreams specification.
func ParseUnionUpstreams(raw string) ([]UnionUpstream, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("缺少上游存储列表 upstreams")
	}
	var tokens []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		switch {
		case ch == '"':
			inQuote = !inQuote
		case !inQuote && (ch == ' ' || ch == '\t'):
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(ch)
		}
	}
	if inQuote {
		return nil, errors.New("upstreams 引号未闭合")
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	if len(tokens) == 0 {
		return nil, errors.New("缺少上游存储列表 upstreams")
	}
	out := make([]UnionUpstream, 0, len(tokens))
	for _, tok := range tokens {
		suffix := ""
		target := tok
		if strings.HasSuffix(target, ":ro") {
			suffix = ":ro"
			target = strings.TrimSuffix(target, ":ro")
		} else if strings.HasSuffix(target, ":nc") {
			suffix = ":nc"
			target = strings.TrimSuffix(target, ":nc")
		}
		target = normalizeCryptRemote(target)
		if target == "" {
			return nil, errors.New("upstreams 包含空的上游条目")
		}
		out = append(out, UnionUpstream{Target: target, Suffix: suffix})
	}
	return out, nil
}

// FormatUnionUpstreams formats union upstreams back into rclone's upstreams string.
func FormatUnionUpstreams(items []UnionUpstream) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		token := item.Target + item.Suffix
		if strings.ContainsAny(token, " \t") {
			token = `"` + token + `"`
		}
		parts = append(parts, token)
	}
	return strings.Join(parts, " ")
}

// CombineUpstream represents one directory=target entry in an rclone combine backend.
type CombineUpstream struct {
	Name   string // directory name at the combine root
	Target string // e.g. "remote:sub" or "/abs/local/path"
}

func splitQuotedTokens(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var tokens []string
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(raw); i++ {
		ch := raw[i]
		switch {
		case ch == '"':
			inQuote = !inQuote
		case !inQuote && (ch == ' ' || ch == '\t'):
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteByte(ch)
		}
	}
	if inQuote {
		return nil, errors.New("upstreams 引号未闭合")
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens, nil
}

// ParseCombineUpstreams parses rclone combine's "dir=remote:path" upstreams list.
func ParseCombineUpstreams(raw string) ([]CombineUpstream, error) {
	tokens, err := splitQuotedTokens(raw)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, errors.New("缺少组合目录列表 upstreams（格式为 目录名=存储名:路径）")
	}
	seenNames := map[string]bool{}
	out := make([]CombineUpstream, 0, len(tokens))
	for _, tok := range tokens {
		dirName, target, ok := strings.Cut(tok, "=")
		dirName = strings.TrimSpace(dirName)
		target = normalizeCryptRemote(target)
		if !ok || dirName == "" || dirName == "." || dirName == ".." || strings.ContainsAny(dirName, `/\:`+"\r\n\x00") {
			return nil, fmt.Errorf("combine 上游条目 %q 格式无效，应为 目录名=存储名:路径", tok)
		}
		if seenNames[strings.ToLower(dirName)] {
			return nil, fmt.Errorf("combine 存在重复目录名 %q", dirName)
		}
		seenNames[strings.ToLower(dirName)] = true
		if target == "" {
			return nil, fmt.Errorf("combine 目录 %q 缺少目标路径", dirName)
		}
		out = append(out, CombineUpstream{Name: dirName, Target: target})
	}
	return out, nil
}

// FormatCombineUpstreams formats combine upstreams back into rclone's upstreams string.
func FormatCombineUpstreams(items []CombineUpstream) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		token := item.Name + "=" + item.Target
		if strings.ContainsAny(token, " \t") {
			token = `"` + token + `"`
		}
		parts = append(parts, token)
	}
	return strings.Join(parts, " ")
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

// StorageConcurrencyLimits extracts configured per-remote max_concurrent_jobs (>0)
// limits across all remotes in c.
func (c Config) StorageConcurrencyLimits() map[string]int {
	limits := make(map[string]int)
	for name, p := range c {
		raw := strings.TrimSpace(p["max_concurrent_jobs"])
		if raw == "" {
			continue
		}
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limits[name] = n
		}
	}
	return limits
}

// ExpandRemoteDependencies returns name together with all transitive backing
// storage names (including Local if wrapped over a local root).
func (c Config) ExpandRemoteDependencies(name string) []string {
	if name == "" {
		return nil
	}
	if name == Local {
		return []string{Local}
	}
	canonical, ok := c.FindKey(name)
	if !ok {
		return []string{name}
	}
	seen := map[string]bool{canonical: true}
	queue := []string{canonical}
	for i := 0; i < len(queue); i++ {
		cur := queue[i]
		if cur == Local {
			continue
		}
		for _, dep := range c.remoteDependencies(cur) {
			if dep == "" || seen[dep] {
				continue
			}
			seen[dep] = true
			queue = append(queue, dep)
		}
	}
	return queue
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
	typ := canonicalRemoteType(strings.TrimSpace(raw["type"]))
	if typ != "" {
		out["type"] = typ
	}
	for k, v := range raw {
		key := strings.TrimSpace(k)
		if key == "" || key == "type" {
			continue
		}
		if IsObscuredFieldForType(typ, key) {
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
		if key == "cookie" {
			v = strings.ReplaceAll(v, "\r\n", "")
			v = strings.ReplaceAll(v, "\r", "")
			v = strings.ReplaceAll(v, "\n", "")
			v = strings.TrimSpace(v)
			if len(v) >= 7 && strings.EqualFold(v[:7], "cookie:") {
				v = strings.TrimSpace(v[7:])
			}
			if v != "" {
				out[key] = v
			}
			continue
		}
		if key == "service_account_credentials" || key == "token" {
			v = strings.TrimSpace(v)
			if v == "" {
				continue
			}
			// Compact multiline JSON so INI line-based formatting stays valid.
			if strings.ContainsAny(v, "\r\n") {
				var obj any
				if json.Unmarshal([]byte(v), &obj) == nil {
					if compact, err := json.Marshal(obj); err == nil {
						v = string(compact)
					}
				}
			}
			out[key] = v
			continue
		}
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if typ == "s3" && key == "endpoint" {
			v = strings.TrimRight(v, "/")
		}
		if IsWrapperType(typ) && key == "remote" {
			v = normalizeCryptRemote(v)
		}
		if v != "" {
			out[key] = v
		}
	}
	return out
}

func canonicalRemoteType(typ string) string {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "gcs", "google cloud storage":
		return "googlecloudstorage"
	case "oos":
		return "oracleobjectstorage"
	default:
		return strings.TrimSpace(typ)
	}
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

var rcloneObscureKey = []byte{
	0x9c, 0x93, 0x5b, 0x48, 0x73, 0x0a, 0x55, 0x4d,
	0x6b, 0xfd, 0x7c, 0x63, 0xc8, 0x86, 0xa9, 0x2b,
	0xd3, 0x90, 0x19, 0x8e, 0xb8, 0x12, 0x8a, 0xfb,
	0xf4, 0xde, 0x16, 0x2b, 0x8b, 0x95, 0xf6, 0x38,
}

func revealObscured(obscured string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(obscured))
	if err != nil {
		return "", errors.New("混淆密码格式无效")
	}
	if len(raw) < aes.BlockSize {
		return "", errors.New("混淆密码长度不足")
	}
	iv := raw[:aes.BlockSize]
	ciphertext := raw[aes.BlockSize:]
	block, err := aes.NewCipher(rcloneObscureKey)
	if err != nil {
		return "", err
	}
	plain := make([]byte, len(ciphertext))
	cipher.NewCTR(block, iv).XORKeyStream(plain, ciphertext)
	return string(plain), nil
}

// VerifyObscured compares candidatePlain against a stored rclone-obscured secret in constant time.
func VerifyObscured(obscured, candidatePlain string) (bool, error) {
	revealed, err := revealObscured(obscured)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare([]byte(revealed), []byte(candidatePlain)) == 1, nil
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
	case "webdav", "http":
		urlStr := strings.TrimSpace(p["url"])
		user := strings.TrimSpace(p["user"])
		if user != "" && urlStr != "" {
			return urlStr + " · " + user
		}
		return urlStr
	case "quark":
		if acct := strings.TrimSpace(p["account_name"]); acct != "" {
			return "夸克网盘 · " + acct
		}
		if rf := strings.TrimSpace(p["root_folder_id"]); rf != "" && rf != "0" {
			return "夸克网盘 · 目录 " + rf
		}
		return "夸克网盘"
	case "pan123":
		modeLabel := "开放平台"
		if pan123.NormalizeProtocol(p["protocol"]) == pan123.ProtocolWeb {
			modeLabel = "个人盘 Web"
		}
		if acct := strings.TrimSpace(p["account_name"]); acct != "" {
			return "123云盘 (" + modeLabel + ") · " + acct
		}
		if u := strings.TrimSpace(p["web_user"]); u != "" && pan123.NormalizeProtocol(p["protocol"]) == pan123.ProtocolWeb {
			return "123云盘 (" + modeLabel + ") · " + u
		}
		if rf := strings.TrimSpace(p["root_folder_id"]); rf != "" && rf != "0" {
			return "123云盘 (" + modeLabel + ") · 目录 " + rf
		}
		return "123云盘 (" + modeLabel + ")"
	case "sftp", "smb", "ftp":
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
	case "b2":
		if acct := strings.TrimSpace(p["account"]); acct != "" {
			return "B2 · " + acct
		}
		return "Backblaze B2"
	case "onedrive":
		var parts []string
		if v := strings.TrimSpace(p["drive_type"]); v != "" {
			parts = append(parts, v)
		}
		if v := strings.TrimSpace(p["region"]); v != "" {
			parts = append(parts, v)
		}
		if len(parts) > 0 {
			return "OneDrive · " + strings.Join(parts, " · ")
		}
		return "OneDrive"
	case "drive":
		if td := strings.TrimSpace(p["team_drive"]); td != "" {
			return "Google Drive · Team Drive"
		}
		if root := strings.TrimSpace(p["root_folder_id"]); root != "" {
			return "Google Drive · " + root
		}
		return "Google Drive"
	case "dropbox":
		return "Dropbox"
	case "box":
		if v := strings.TrimSpace(p["box_sub_type"]); v != "" {
			return "Box · " + v
		}
		return "Box"
	case "pcloud":
		if h := strings.TrimSpace(p["hostname"]); h != "" {
			return "pCloud · " + h
		}
		if u := strings.TrimSpace(p["username"]); u != "" {
			return "pCloud · " + u
		}
		return "pCloud"
	case "mega":
		if u := strings.TrimSpace(p["user"]); u != "" {
			return "MEGA · " + u
		}
		return "MEGA"
	case "azureblob":
		if acct := strings.TrimSpace(p["account"]); acct != "" {
			return "Azure Blob · " + acct
		}
		if ep := strings.TrimSpace(p["endpoint"]); ep != "" {
			return "Azure Blob · " + ep
		}
		return "Azure Blob"
	case "googlecloudstorage":
		var parts []string
		if v := strings.TrimSpace(p["project_number"]); v != "" {
			parts = append(parts, v)
		}
		if v := strings.TrimSpace(p["location"]); v != "" {
			parts = append(parts, v)
		}
		if len(parts) > 0 {
			return "GCS · " + strings.Join(parts, " · ")
		}
		return "Google Cloud Storage"
	case "swift":
		if v := strings.TrimSpace(p["auth"]); v != "" {
			return "Swift · " + v
		}
		if v := strings.TrimSpace(p["storage_url"]); v != "" {
			return "Swift · " + v
		}
		return "OpenStack Swift"
	case "crypt", "chunker", "compress", "hasher", "alias", "archive":
		if r := strings.TrimSpace(p["remote"]); r != "" {
			return "底层 " + r
		}
		return ""
	case "union":
		if u := strings.TrimSpace(p["upstreams"]); u != "" {
			return "合并 " + u
		}
		return ""
	case "combine":
		if u := strings.TrimSpace(p["upstreams"]); u != "" {
			return "组合 " + u
		}
		return ""
	case "yandex":
		if f := strings.TrimSpace(p["app_folder"]); f != "" && f != "false" {
			return "Yandex Disk · AppFolder"
		}
		return "Yandex Disk"
	case "koofr":
		if u := strings.TrimSpace(p["user"]); u != "" {
			return "Koofr · " + u
		}
		return "Koofr"
	case "seafile":
		if u := strings.TrimSpace(p["url"]); u != "" {
			return u
		}
		return "Seafile"
	case "storj":
		if s := strings.TrimSpace(p["satellite_address"]); s != "" {
			return "Storj · " + s
		}
		return "Storj"
	case "qingstor":
		if z := strings.TrimSpace(p["zone"]); z != "" {
			return "QingStor · " + z
		}
		return "QingStor"
	case "oracleobjectstorage":
		var parts []string
		if v := strings.TrimSpace(p["namespace"]); v != "" {
			parts = append(parts, v)
		}
		if v := strings.TrimSpace(p["region"]); v != "" {
			parts = append(parts, v)
		}
		if len(parts) > 0 {
			return "Oracle · " + strings.Join(parts, " · ")
		}
		return "Oracle Object Storage"
	case "azurefiles":
		var parts []string
		if v := strings.TrimSpace(p["account"]); v != "" {
			parts = append(parts, v)
		}
		if v := strings.TrimSpace(p["share_name"]); v != "" {
			parts = append(parts, v)
		}
		if len(parts) > 0 {
			return "Azure Files · " + strings.Join(parts, "/")
		}
		return "Azure Files"
	case "pikpak":
		if u := strings.TrimSpace(p["user"]); u != "" {
			return "PikPak · " + u
		}
		return "PikPak"
	case "protondrive":
		if u := strings.TrimSpace(p["username"]); u != "" {
			return "Proton Drive · " + u
		}
		return "Proton Drive"
	case "iclouddrive":
		if u := strings.TrimSpace(p["apple_id"]); u != "" {
			return "iCloud · " + u
		}
		return "iCloud Drive"
	case "jottacloud":
		return "Jottacloud"
	case "filen":
		if u := strings.TrimSpace(p["email"]); u != "" {
			return "Filen · " + u
		}
		return "Filen"
	case "hidrive":
		return "HiDrive"
	case "huaweidrive":
		return "华为云盘"
	case "mailru":
		if u := strings.TrimSpace(p["user"]); u != "" {
			return "Mail.ru · " + u
		}
		return "Mail.ru Cloud"
	case "fichier":
		return "1Fichier"
	case "gofile":
		return "Gofile"
	case "pixeldrain":
		return "Pixeldrain"
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

// RedactConfigForBackup returns a copy of stored native engine JSON where all
// secret parameter values are replaced by RedactedSecretPlaceholder while
// preserving non-secret remote definitions. An empty pure-local "{}" config
// stays "{}" unchanged so pure-local backups round-trip without secrets.
func RedactConfigForBackup(stored string) (string, bool, error) {
	var c Config
	if err := json.Unmarshal([]byte(stored), &c); err != nil || c == nil {
		return "", false, errors.New("原生引擎配置不可读")
	}
	if len(c) == 0 {
		return "{}", false, nil
	}
	out := make(Config, len(c))
	redactedAny := false
	for name, params := range c {
		cp := make(map[string]string, len(params))
		for k, v := range params {
			if v != "" && IsSecretField(k) {
				cp[k] = RedactedSecretPlaceholder
				redactedAny = true
			} else {
				cp[k] = v
			}
		}
		out[name] = cp
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "", false, err
	}
	return string(raw), redactedAny, nil
}

// ConfigContainsRedactedSecrets reports whether a raw native config JSON
// contains any redacted secret placeholders from a non-secret backup export.
func ConfigContainsRedactedSecrets(stored string) bool {
	var c Config
	if json.Unmarshal([]byte(stored), &c) != nil {
		return false
	}
	for _, params := range c {
		for k, v := range params {
			if IsSecretField(k) && v == RedactedSecretPlaceholder {
				return true
			}
		}
	}
	return false
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

// Rename returns a new Config with oldName renamed to newName and updates every
// wrapper/union reference pointing to oldName.
func (c Config) Rename(oldName, newName string) (Config, error) {
	actualOld, ok := c.FindKey(oldName)
	if !ok {
		return nil, fmt.Errorf("存储 %q 不存在", oldName)
	}
	newName = strings.TrimSpace(newName)
	if !validName(newName) {
		return nil, fmt.Errorf("存储名称 %q 无效（1–64 个字符，不能使用保留名或首尾空格）", newName)
	}
	if strings.EqualFold(actualOld, newName) && actualOld == newName {
		return nil, errors.New("新名称与原名称相同")
	}
	if existing, exists := c.FindKey(newName); exists && !strings.EqualFold(existing, actualOld) {
		return nil, fmt.Errorf("存储 %q 已存在，请使用其他名称", existing)
	}

	next := make(Config, len(c))
	for name, params := range c {
		cp := maps.Clone(params)
		key := name
		if name == actualOld {
			key = newName
		}
		if IsWrapperType(cp["type"]) {
			if ref, sub, cutOK := strings.Cut(cp["remote"], ":"); cutOK && strings.EqualFold(ref, actualOld) {
				cp["remote"] = newName + ":" + sub
			}
		}
		if cp["type"] == "union" && cp["upstreams"] != "" {
			if ups, err := ParseUnionUpstreams(cp["upstreams"]); err == nil {
				changed := false
				for i := range ups {
					if ref, sub, cutOK := strings.Cut(ups[i].Target, ":"); cutOK && strings.EqualFold(ref, actualOld) {
						ups[i].Target = newName + ":" + sub
						changed = true
					}
				}
				if changed {
					cp["upstreams"] = FormatUnionUpstreams(ups)
				}
			}
		}
		if cp["type"] == "combine" && cp["upstreams"] != "" {
			if ups, err := ParseCombineUpstreams(cp["upstreams"]); err == nil {
				changed := false
				for i := range ups {
					if ref, sub, cutOK := strings.Cut(ups[i].Target, ":"); cutOK && strings.EqualFold(ref, actualOld) {
						ups[i].Target = newName + ":" + sub
						changed = true
					}
				}
				if changed {
					cp["upstreams"] = FormatCombineUpstreams(ups)
				}
			}
		}
		next[key] = cp
	}
	if err := next.Validate(); err != nil {
		return nil, err
	}
	return next, nil
}

// UsesBuiltInDriver reports whether name or any transitive composite dependency
// uses an OpenSync built-in driver (such as quark) that external official rclone
// does not natively support.
func (c Config) UsesBuiltInDriver(name string) bool {
	visited := make(map[string]bool)
	var walk func(cur string) bool
	walk = func(cur string) bool {
		canonical, ok := c.FindKey(cur)
		if !ok || visited[canonical] {
			return false
		}
		visited[canonical] = true
		p := c[canonical]
		if p["type"] == "quark" || p["type"] == "pan123" {
			return true
		}
		for _, dep := range c.remoteDependencies(canonical) {
			if strings.EqualFold(dep, Local) {
				continue
			}
			if walk(dep) {
				return true
			}
		}
		return false
	}
	return walk(name)
}

// ExportINI formats selected (or all) remotes into standard rclone.conf INI.
// When encryptPassword is non-empty, rclone encrypts the output using that password.
func (c Config) ExportINI(ctx context.Context, names []string, encryptPassword string) (string, error) {
	if len(c) == 0 {
		return "", errors.New("当前没有可导出的远端存储配置")
	}
	selected := map[string]bool{}
	var skippedBuiltIn []string
	if len(names) == 0 {
		for k := range c {
			if c.UsesBuiltInDriver(k) {
				skippedBuiltIn = append(skippedBuiltIn, k)
				continue
			}
			selected[k] = true
		}
		slices.Sort(skippedBuiltIn)
		if len(selected) == 0 && len(skippedBuiltIn) > 0 {
			return "", errors.New("当前仅包含 OpenSync 内置网盘驱动（如夸克网盘、123云盘），暂不支持导出到外部官方 rclone")
		}
	} else {
		var queue []string
		for _, raw := range names {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			key, ok := c.FindKey(raw)
			if !ok {
				return "", fmt.Errorf("存储 %q 不存在", raw)
			}
			if c.UsesBuiltInDriver(key) {
				return "", fmt.Errorf("存储 %q 使用 OpenSync 内置网盘驱动，外部官方 rclone 暂不支持直接导出", key)
			}
			if !selected[key] {
				selected[key] = true
				queue = append(queue, key)
			}
		}
		// Automatically pull transitive wrapper/union remote dependencies so the
		// exported rclone.conf is self-contained.
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, dep := range c.remoteDependencies(cur) {
				if dep != Local && c[dep] != nil && !selected[dep] {
					if c.UsesBuiltInDriver(dep) {
						return "", fmt.Errorf("存储依赖 %q 使用 OpenSync 内置网盘驱动，外部官方 rclone 暂不支持直接导出", dep)
					}
					selected[dep] = true
					queue = append(queue, dep)
				}
			}
		}
	}
	if len(selected) == 0 {
		return "", errors.New("请至少选择一个要导出的存储配置")
	}
	var b strings.Builder
	if len(skippedBuiltIn) > 0 {
		fmt.Fprintf(&b, "# OpenSync 提示：已自动跳过内置驱动存储（外部官方 rclone 暂不支持）：%s\n\n", strings.Join(skippedBuiltIn, ", "))
	}
	for _, name := range slices.Sorted(maps.Keys(selected)) {
		p := maps.Clone(c[name])
		if IsWrapperType(p["type"]) {
			target, err := c.exportLocalTarget(p["remote"])
			if err != nil {
				return "", fmt.Errorf("无法导出存储 %q：%w", name, err)
			}
			p["remote"] = target
		}
		if p["type"] == "union" {
			ups, err := ParseUnionUpstreams(p["upstreams"])
			if err != nil {
				return "", err
			}
			for i := range ups {
				ups[i].Target, err = c.exportLocalTarget(ups[i].Target)
				if err != nil {
					return "", fmt.Errorf("无法导出存储 %q：%w", name, err)
				}
			}
			p["upstreams"] = FormatUnionUpstreams(ups)
		}
		if p["type"] == "combine" {
			ups, err := ParseCombineUpstreams(p["upstreams"])
			if err != nil {
				return "", err
			}
			for i := range ups {
				ups[i].Target, err = c.exportLocalTarget(ups[i].Target)
				if err != nil {
					return "", fmt.Errorf("无法导出存储 %q：%w", name, err)
				}
			}
			p["upstreams"] = FormatCombineUpstreams(ups)
		}
		fmt.Fprintf(&b, "[%s]\n", name)
		fmt.Fprintf(&b, "type = %s\n", p["type"])
		for _, k := range slices.Sorted(maps.Keys(p)) {
			if k == "type" || IsOpenSyncMetaField(k) {
				continue
			}
			v := p[k]
			if v == "" {
				continue
			}
			// INI format is single-line; known_hosts is multiline and cannot be
			// represented cleanly in standard rclone.conf without known_hosts_file.
			if k == "known_hosts" {
				continue
			}
			fmt.Fprintf(&b, "%s = %s\n", k, v)
		}
		b.WriteByte('\n')
	}
	plainINI := b.String()
	if encryptPassword == "" {
		return plainINI, nil
	}
	if len(encryptPassword) > 1024 || strings.IndexFunc(encryptPassword, unicode.IsControl) >= 0 {
		return "", errors.New("导出加密密码格式无效")
	}

	dir, err := os.MkdirTemp(nativeTempDir(), "opensync-export-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	confPath := filepath.Join(dir, "rclone.conf")
	if err := os.WriteFile(confPath, []byte(plainINI), 0600); err != nil {
		return "", err
	}
	// Pass the encryption password exclusively via environment variable so it
	// never appears on process argv or disk.
	cmd, err := command(ctx, "config", "encryption", "set", "--config", confPath, "--password-command", "printenv OPENSYNC_EXPORT_PASS")
	if err != nil {
		return "", err
	}
	cmd.Env = append(cmd.Env, "OPENSYNC_EXPORT_PASS="+encryptPassword)
	if _, _, err := limitedOutputWithStderr(cmd, 64<<10); err != nil {
		return "", errors.New("加密导出 rclone.conf 失败")
	}
	encrypted, err := os.ReadFile(confPath)
	if err != nil {
		return "", err
	}
	return string(encrypted), nil
}

func (c Config) exportLocalTarget(target string) (string, error) {
	ref, sub, ok := strings.Cut(target, ":")
	if !ok || !c.isLocalName(ref) {
		return target, nil
	}
	if !cleanRelative(sub) {
		return "", errors.New("本地授权目录路径无效")
	}
	abs, err := resolveLocalEntry(sub, true)
	if err != nil {
		return "", err
	}
	return abs, nil
}

// AffectedRemotes returns the canonical set of remote names that are either in
// changed or transitively depend on any remote in changed (wrappers, union, combine).
func (c Config) AffectedRemotes(changed []string) map[string]bool {
	revDeps := make(map[string][]string, len(c))
	for name := range c {
		for _, dep := range c.remoteDependencies(name) {
			if strings.EqualFold(dep, Local) {
				continue
			}
			if canonical, ok := c.FindKey(dep); ok {
				revDeps[canonical] = append(revDeps[canonical], name)
			}
		}
	}
	affected := make(map[string]bool, len(changed))
	var queue []string
	for _, raw := range changed {
		if canonical, ok := c.FindKey(raw); ok && !affected[canonical] {
			affected[canonical] = true
			queue = append(queue, canonical)
		}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, parent := range revDeps[cur] {
			if !affected[parent] {
				affected[parent] = true
				queue = append(queue, parent)
			}
		}
	}
	return affected
}

// RemoteHasCrypt reports whether name or any transitive composite dependency
// (wrapper/union/combine) uses the crypt backend.
func (c Config) RemoteHasCrypt(name string) bool {
	visited := make(map[string]bool)
	var walk func(cur string) bool
	walk = func(cur string) bool {
		canonical, ok := c.FindKey(cur)
		if !ok || visited[canonical] {
			return false
		}
		visited[canonical] = true
		p := c[canonical]
		if p["type"] == "crypt" {
			return true
		}
		for _, dep := range c.remoteDependencies(canonical) {
			if strings.EqualFold(dep, Local) {
				continue
			}
			if walk(dep) {
				return true
			}
		}
		return false
	}
	return walk(name)
}

func (c Config) remoteDependencies(name string) []string {
	p := c[name]
	if p == nil {
		return nil
	}
	switch p["type"] {
	case "crypt", "chunker", "compress", "hasher", "alias", "archive":
		ref, _, ok := strings.Cut(p["remote"], ":")
		if ok && ref != "" {
			return []string{ref}
		}
	case "union":
		ups, err := ParseUnionUpstreams(p["upstreams"])
		if err != nil {
			return nil
		}
		var deps []string
		for _, up := range ups {
			if ref, _, ok := strings.Cut(up.Target, ":"); ok && ref != "" {
				deps = append(deps, ref)
			}
		}
		return deps
	case "combine":
		ups, err := ParseCombineUpstreams(p["upstreams"])
		if err != nil {
			return nil
		}
		var deps []string
		for _, up := range ups {
			if ref, _, ok := strings.Cut(up.Target, ":"); ok && ref != "" {
				deps = append(deps, ref)
			}
		}
		return deps
	}
	return nil
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
	case (typ == "drive" || typ == "googlecloudstorage") && field == "service_account_file":
		return fmt.Errorf("存储 %q 出于安全限制不支持本地凭据文件路径 service_account_file，请改用内嵌 JSON 字段 service_account_credentials", name)
	case typ == "azureblob" && field == "service_principal_file":
		return fmt.Errorf("存储 %q 出于安全限制不支持本地文件路径 service_principal_file，请改用内嵌 tenant/client_id/client_secret 或 sas_url", name)
	case typ == "oracleobjectstorage" && (field == "config_file" || field == "sse_customer_key_file"):
		return fmt.Errorf("存储 %q 出于安全限制不支持本地文件路径 %s", name, field)
	case (typ == "azurefiles" || typ == "azureblob") && field == "client_certificate_path":
		return fmt.Errorf("存储 %q 出于安全限制不支持本地证书路径 %s", name, field)
	case strings.HasSuffix(field, "_command") || field == "ssh" || field == "credential_process":
		return fmt.Errorf("存储 %q 的字段 %q 受安全限制不支持", name, field)
	default:
		return fmt.Errorf("存储 %q 含不支持的字段 %q", name, field)
	}
}

func validateObscuredFields(name string, p map[string]string) error {
	typ := p["type"]
	for k, v := range p {
		if v == "" || !IsObscuredFieldForType(typ, k) {
			continue
		}
		decoded, err := base64.RawURLEncoding.DecodeString(v)
		if err != nil || len(decoded) < 16 {
			return fmt.Errorf("%s 的 %s 需要 rclone 配置中的混淆值", name, k)
		}
	}
	return nil
}

func validateOAuthTokenJSON(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(raw), &obj); err != nil || len(obj) == 0 {
		return false
	}
	for _, k := range []string{"access_token", "refresh_token"} {
		if s, ok := obj[k].(string); ok && strings.TrimSpace(s) != "" {
			return true
		}
	}
	return false
}

func validateInlineJSONObject(raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	var obj map[string]any
	return json.Unmarshal([]byte(raw), &obj) == nil && len(obj) > 0
}

func validateBoolField(name, key, val string) error {
	if val != "" && val != "true" && val != "false" {
		return fmt.Errorf("%s 的 %s 必须为 true 或 false", name, key)
	}
	return nil
}

func validatePortField(name, val string) error {
	if val == "" {
		return nil
	}
	n, err := strconv.Atoi(val)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%s 的端口无效（应为 1–65535）", name)
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
		if k != "type" && k != "max_concurrent_jobs" && !strings.Contains(" "+allowed+" ", " "+k+" ") {
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
	if mc := p["max_concurrent_jobs"]; mc != "" {
		n, err := strconv.Atoi(mc)
		if err != nil || n < 0 || n > 50 {
			return fmt.Errorf("%s 的单存储最大并发任务数必须为 0–50 的整数", name)
		}
	}
	if err := validateBoolField(name, "insecure_skip_verify", p["insecure_skip_verify"]); err != nil {
		return err
	}
	if err := validateBoolField(name, "allow_hashcheck", p["allow_hashcheck"]); err != nil {
		return err
	}
	if p["proxy_url"] != "" {
		if err := ValidateProxyURL(p["proxy_url"]); err != nil {
			return fmt.Errorf("%s：%s", name, err.Error())
		}
	}

	switch typ {
	case "s3":
		if err := validateBoolField(name, "env_auth", p["env_auth"]); err != nil {
			return err
		}
		hasAccessKey := p["access_key_id"] != ""
		hasSecretKey := p["secret_access_key"] != ""
		if hasAccessKey != hasSecretKey {
			return fmt.Errorf("%s 的 S3 Access Key ID 和 Secret Access Key 必须同时填写，或同时留空使用运行时/匿名访问", name)
		}
		if p["endpoint"] != "" && !validS3Endpoint(p["endpoint"]) {
			return fmt.Errorf("%s 的 S3 端点无效（请填写如 oss-cn-hangzhou.aliyuncs.com 或 https://...）", name)
		}
		for _, bk := range []string{"force_path_style", "no_check_bucket", "v2_auth", "disable_http2", "directory_markers", "leave_parts_on_error", "no_head", "no_head_object"} {
			if err := validateBoolField(name, bk, p[bk]); err != nil {
				return err
			}
		}
	case "webdav":
		if !validURL(p["url"]) {
			return fmt.Errorf("%s 的 WebDAV 地址无效（请填写有效的 HTTP/HTTPS 地址）", name)
		}
	case "quark":
		if !quark.ValidateCookieFormat(p["cookie"]) {
			return fmt.Errorf("%s 需要填写有效的夸克网盘 Cookie（或使用二维码扫码登录）", name)
		}
		if rf := p["root_folder_id"]; rf != "" {
			if strings.ContainsAny(rf, "/\\ \t") || rf == "." || rf == ".." || len(rf) > 128 {
				return fmt.Errorf("%s 的根目录 ID（root_folder_id）格式无效", name)
			}
		}
		if uc := p["upload_concurrency"]; uc != "" {
			n, err := strconv.Atoi(uc)
			if err != nil || n < 1 || n > 8 {
				return fmt.Errorf("%s 的分片上传并发数（upload_concurrency）需为 1–8 的整数", name)
			}
		}
		if tps := p["tps_limit"]; tps != "" {
			v, err := strconv.ParseFloat(tps, 64)
			if err != nil || v <= 0 || v > 20 {
				return fmt.Errorf("%s 的接口限流速率（tps_limit）需为大于 0 且不超过 20 的数值", name)
			}
		}
		if ps := p["part_size"]; ps != "" {
			if err := ValidateRcloneSize(ps, false, fmt.Sprintf("%s 的分片大小（part_size）格式无效，如 10M、20M", name)); err != nil {
				return err
			}
		}
		if ua := p["user_agent"]; ua != "" {
			if err := ValidateUserAgent(ua); err != nil {
				return fmt.Errorf("%s：%s", name, err.Error())
			}
		}
	case "pan123":
		proto := pan123.NormalizeProtocol(p["protocol"])
		if proto != pan123.ProtocolOpenAPI && proto != pan123.ProtocolWeb {
			return fmt.Errorf("%s 的协议模式（protocol）无效，仅支持 open_api 或 web", name)
		}
		hasToken := strings.TrimSpace(p["access_token"]) != ""
		if proto == pan123.ProtocolOpenAPI {
			hasClientPair := strings.TrimSpace(p["client_id"]) != "" && strings.TrimSpace(p["client_secret"]) != ""
			if !hasToken && !hasClientPair {
				return fmt.Errorf("%s 需要填写 123云盘开放平台的 Client ID 与 Client Secret，或直接提供 Access Token", name)
			}
		} else {
			hasUserPass := strings.TrimSpace(p["web_user"]) != "" && p["web_password"] != ""
			if !hasToken && !hasUserPass {
				return fmt.Errorf("%s 需要填写 123云盘个人账号与密码，或直接粘贴 Web Access Token", name)
			}
		}
		if _, err := pan123.ParseRootFolderID(p["root_folder_id"]); err != nil {
			return fmt.Errorf("%s 的%s", name, err.Error())
		}
		if uc := p["upload_concurrency"]; uc != "" {
			n, err := strconv.Atoi(uc)
			if err != nil || n < 1 || n > pan123.MaxUploadConcurrency {
				return fmt.Errorf("%s 的分片上传并发数（upload_concurrency）需为 1–8 的整数", name)
			}
		}
		if tps := p["tps_limit"]; tps != "" {
			v, err := strconv.ParseFloat(tps, 64)
			if err != nil || v <= 0 || v > 20 {
				return fmt.Errorf("%s 的接口限流速率（tps_limit）需为大于 0 且不超过 20 的数值", name)
			}
		}
		if err := validateBoolField(name, "use_direct_link", p["use_direct_link"]); err != nil {
			return err
		}
		if ttl := p["direct_link_ttl"]; ttl != "" {
			n, err := strconv.Atoi(ttl)
			if err != nil || n < 1 || n > 10080 {
				return fmt.Errorf("%s 的直链签名有效期（direct_link_ttl）需为 1–10080 分钟的整数", name)
			}
		}
		if ua := p["user_agent"]; ua != "" {
			if err := ValidateUserAgent(ua); err != nil {
				return fmt.Errorf("%s：%s", name, err.Error())
			}
		}
	case "http":
		if !validURL(p["url"]) {
			return fmt.Errorf("%s 的 HTTP 地址无效（请填写有效的 HTTP/HTTPS 地址）", name)
		}
		for _, bk := range []string{"no_head", "no_slash"} {
			if err := validateBoolField(name, bk, p[bk]); err != nil {
				return err
			}
		}
	case "sftp", "smb", "ftp":
		if p["host"] == "" || strings.ContainsAny(p["host"], "/\\@ \t") || p["user"] == "" {
			return fmt.Errorf("%s 需要填写有效的主机地址和用户名", name)
		}
		if err := validatePortField(name, p["port"]); err != nil {
			return err
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
			if p["shell_type"] != "" && p["shell_type"] != "none" && !(p["shell_type"] == "unix" && p["allow_hashcheck"] == "true") {
				return fmt.Errorf("%s 仅支持 SFTP 文件协议", name)
			}
		}
		if typ == "ftp" {
			for _, bk := range []string{"tls", "explicit_tls", "disable_tls13", "no_check_certificate", "writing_mdtm", "force_list_hidden"} {
				if err := validateBoolField(name, bk, p[bk]); err != nil {
					return err
				}
			}
		}
	case "b2":
		if p["account"] == "" || p["key"] == "" {
			return fmt.Errorf("%s 需要填写 B2 Account ID（account）和 Application Key（key）", name)
		}
		if p["endpoint"] != "" && !validS3Endpoint(p["endpoint"]) {
			return fmt.Errorf("%s 的 B2 端点无效", name)
		}
	case "onedrive":
		if !validateOAuthTokenJSON(p["token"]) {
			return fmt.Errorf("%s 需要填写有效的 OAuth token（JSON 中需包含 access_token 或 refresh_token）", name)
		}
	case "drive":
		hasToken := p["token"] != ""
		hasSA := p["service_account_credentials"] != ""
		if !hasToken && !hasSA {
			return fmt.Errorf("%s 需要填写有效的 OAuth token 或内嵌 service_account_credentials JSON", name)
		}
		if hasToken && !validateOAuthTokenJSON(p["token"]) {
			return fmt.Errorf("%s 的 OAuth token 格式无效", name)
		}
		if hasSA && !validateInlineJSONObject(p["service_account_credentials"]) {
			return fmt.Errorf("%s 的 service_account_credentials 必须为有效 JSON", name)
		}
	case "dropbox", "box", "yandex", "jottacloud", "hidrive", "huaweidrive":
		if !validateOAuthTokenJSON(p["token"]) {
			return fmt.Errorf("%s 需要填写有效的 OAuth token（JSON 中需包含 access_token 或 refresh_token）", name)
		}
	case "pcloud":
		hasToken := p["token"] != ""
		hasUserPass := p["username"] != "" && p["password"] != ""
		if !hasToken && !hasUserPass {
			return fmt.Errorf("%s 需要填写有效的 OAuth token 或用户名+密码", name)
		}
		if hasToken && !validateOAuthTokenJSON(p["token"]) {
			return fmt.Errorf("%s 的 OAuth token 格式无效", name)
		}
	case "mega":
		if p["user"] == "" || p["pass"] == "" {
			return fmt.Errorf("%s 需要填写 MEGA 账号（user）和密码（pass）", name)
		}
	case "azureblob":
		hasSAS := p["sas_url"] != ""
		hasKey := p["account"] != "" && p["key"] != ""
		hasSP := p["account"] != "" && p["tenant"] != "" && p["client_id"] != "" && p["client_secret"] != ""
		if !hasSAS && !hasKey && !hasSP {
			return fmt.Errorf("%s 需要填写 SAS URL，或 Account + Key，或 Account + Service Principal 凭据", name)
		}
		if hasSAS && !validURLWithQuery(p["sas_url"]) {
			return fmt.Errorf("%s 的 sas_url 不是有效的 HTTP/HTTPS 地址", name)
		}
	case "azurefiles":
		hasConn := p["connection_string"] != ""
		hasSAS := p["sas_url"] != ""
		hasKey := p["account"] != "" && p["key"] != ""
		hasSP := p["account"] != "" && p["tenant"] != "" && p["client_id"] != "" && p["client_secret"] != ""
		if !hasConn && !hasSAS && !hasKey && !hasSP {
			return fmt.Errorf("%s 需要填写连接字符串、SAS URL、Account + Key 或 Service Principal 凭据", name)
		}
		if hasSAS && !validURLWithQuery(p["sas_url"]) {
			return fmt.Errorf("%s 的 sas_url 不是有效的 HTTP/HTTPS 地址", name)
		}
	case "googlecloudstorage":
		hasToken := p["token"] != ""
		hasSA := p["service_account_credentials"] != ""
		anon := p["anonymous"] == "true"
		if !hasToken && !hasSA && !anon {
			return fmt.Errorf("%s 需要填写 OAuth token、service_account_credentials JSON 或开启 anonymous", name)
		}
		if hasToken && !validateOAuthTokenJSON(p["token"]) {
			return fmt.Errorf("%s 的 OAuth token 格式无效", name)
		}
		if hasSA && !validateInlineJSONObject(p["service_account_credentials"]) {
			return fmt.Errorf("%s 的 service_account_credentials 必须为有效 JSON", name)
		}
	case "swift":
		hasAuth := p["auth"] != "" && (p["key"] != "" || p["application_credential_secret"] != "")
		hasTokenAuth := p["storage_url"] != "" && p["auth_token"] != ""
		if !hasAuth && !hasTokenAuth {
			return fmt.Errorf("%s 需要填写 auth 认证地址与密钥，或 storage_url + auth_token", name)
		}
		if p["auth"] != "" && !validURL(p["auth"]) {
			return fmt.Errorf("%s 的 auth 地址无效", name)
		}
		if p["storage_url"] != "" && !validURL(p["storage_url"]) {
			return fmt.Errorf("%s 的 storage_url 地址无效", name)
		}
	case "oracleobjectstorage":
		if strings.TrimSpace(p["namespace"]) == "" || strings.TrimSpace(p["region"]) == "" {
			return fmt.Errorf("%s 需要填写 Oracle Namespace 和 Region", name)
		}
		switch strings.TrimSpace(p["provider"]) {
		case "", "env_auth", "user_principal_auth", "instance_principal_auth", "resource_principal_auth", "workload_identity_auth", "no_auth":
		default:
			return fmt.Errorf("%s 的 Oracle provider 不受支持", name)
		}
	case "qingstor":
		if err := validateBoolField(name, "env_auth", p["env_auth"]); err != nil {
			return err
		}
		hasAccessKey := p["access_key_id"] != ""
		hasSecretKey := p["secret_access_key"] != ""
		if hasAccessKey != hasSecretKey {
			return fmt.Errorf("%s 的 QingStor Access Key ID 和 Secret Access Key 必须同时填写，或同时留空使用运行时/匿名访问", name)
		}
	case "storj":
		hasGrant := p["access_grant"] != ""
		hasKeys := p["satellite_address"] != "" && p["api_key"] != "" && p["passphrase"] != ""
		if !hasGrant && !hasKeys {
			return fmt.Errorf("%s 需要填写 Storj Access Grant，或 Satellite + API Key + Passphrase", name)
		}
	case "koofr":
		if !validURL(p["endpoint"]) || p["user"] == "" || p["password"] == "" {
			return fmt.Errorf("%s 需要填写有效的 Koofr endpoint、用户名和密码", name)
		}
	case "seafile":
		if !validURL(p["url"]) || p["user"] == "" || (p["pass"] == "" && p["auth_token"] == "") {
			return fmt.Errorf("%s 需要填写有效的 Seafile 服务地址、用户名和密码（或 auth_token）", name)
		}
	case "pikpak":
		if p["user"] == "" || p["pass"] == "" {
			return fmt.Errorf("%s 需要填写 PikPak 账号（user）和密码（pass）", name)
		}
	case "protondrive":
		if p["username"] == "" || p["password"] == "" {
			return fmt.Errorf("%s 需要填写 Proton 用户名和密码", name)
		}
	case "iclouddrive":
		if p["apple_id"] == "" || p["password"] == "" {
			return fmt.Errorf("%s 需要填写 Apple ID 和密码", name)
		}
	case "filen":
		if p["email"] == "" || p["password"] == "" || p["api_key"] == "" {
			return fmt.Errorf("%s 需要填写 Filen 邮箱、密码和 API Key", name)
		}
	case "mailru":
		hasToken := p["token"] != ""
		hasUserPass := p["user"] != "" && p["pass"] != ""
		if !hasToken && !hasUserPass {
			return fmt.Errorf("%s 需要填写有效的 OAuth token 或账号+密码", name)
		}
		if hasToken && !validateOAuthTokenJSON(p["token"]) {
			return fmt.Errorf("%s 的 OAuth token 格式无效", name)
		}
	case "fichier":
		if p["api_key"] == "" && p["shared_folder"] == "" {
			return fmt.Errorf("%s 需要填写 1Fichier API Key 或共享目录链接", name)
		}
	case "gofile":
		if p["access_token"] == "" && p["root_folder_id"] == "" {
			return fmt.Errorf("%s 需要填写 Gofile Access Token 或 Root Folder ID", name)
		}
	case "pixeldrain":
		if p["api_key"] == "" && p["root_folder_id"] == "" {
			return fmt.Errorf("%s 需要填写 Pixeldrain API Key 或 Root Folder ID", name)
		}
	case "crypt":
		if p["password"] == "" {
			return fmt.Errorf("%s 缺少加密密码", name)
		}
		if strings.TrimSpace(p["remote"]) == "" {
			return fmt.Errorf("%s 缺少底层路径", name)
		}
		if err := validateBoolField(name, "managed_by_job", p["managed_by_job"]); err != nil {
			return err
		}
		if len([]rune(p["managed_job_label"])) > 128 {
			return fmt.Errorf("%s 的关联任务标签长度不能超过 128 个字符", name)
		}
	case "chunker", "compress", "hasher", "alias", "archive":
		if strings.TrimSpace(p["remote"]) == "" {
			return fmt.Errorf("%s 缺少底层路径 remote", name)
		}
	case "union":
		if _, err := ParseUnionUpstreams(p["upstreams"]); err != nil {
			return fmt.Errorf("%s：%s", name, err.Error())
		}
	case "combine":
		if _, err := ParseCombineUpstreams(p["upstreams"]); err != nil {
			return fmt.Errorf("%s：%s", name, err.Error())
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
		if IsWrapperType(p["type"]) {
			ref, sub, ok := strings.Cut(p["remote"], ":")
			if !ok || !cleanRelative(sub) || (ref != Local && c[ref] == nil) {
				return fmt.Errorf("%s 的底层路径必须引用已配置的存储或本地授权目录", name)
			}
			if ref == Local && sub == "" {
				return fmt.Errorf("%s 需要指定一个本地授权目录", name)
			}
		}
		if p["type"] == "union" {
			ups, err := ParseUnionUpstreams(p["upstreams"])
			if err != nil {
				return fmt.Errorf("%s：%s", name, err.Error())
			}
			for _, up := range ups {
				ref, sub, ok := strings.Cut(up.Target, ":")
				if !ok || !cleanRelative(sub) || (ref != Local && c[ref] == nil) {
					return fmt.Errorf("%s 的上游路径 %q 必须引用已配置的存储或本地授权目录", name, up.Target)
				}
				if ref == Local && sub == "" {
					return fmt.Errorf("%s 的本地上游需要指定授权目录", name)
				}
			}
		}
		if p["type"] == "combine" {
			ups, err := ParseCombineUpstreams(p["upstreams"])
			if err != nil {
				return fmt.Errorf("%s：%s", name, err.Error())
			}
			for _, up := range ups {
				ref, sub, ok := strings.Cut(up.Target, ":")
				if !ok || !cleanRelative(sub) || (ref != Local && c[ref] == nil) {
					return fmt.Errorf("%s 的组合目录 %q（%s）必须引用已配置的存储或本地授权目录", name, up.Name, up.Target)
				}
				if ref == Local && sub == "" {
					return fmt.Errorf("%s 的本地组合目录 %q 需要指定授权目录", name, up.Name)
				}
			}
		}
	}
	// DFS cycle detection across all composite (wrapper + union) remotes.
	const (
		unvisited = 0
		visiting  = 1
		visited   = 2
	)
	state := make(map[string]int, len(c))
	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case visiting:
			return fmt.Errorf("%s 存在循环引用", name)
		case visited:
			return nil
		}
		state[name] = visiting
		for _, dep := range c.remoteDependencies(name) {
			if dep == Local || c[dep] == nil {
				continue
			}
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[name] = visited
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(c)) {
		if err := visit(name); err != nil {
			return err
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

func validURLWithQuery(s string) bool {
	u, e := url.Parse(s)
	if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
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

func rewriteCompositeTarget(target string, existing, additions Config) (string, error) {
	target = normalizeCryptRemote(target)
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
			return "", e
		}
		return Local + ":" + strings.TrimPrefix(v, "/"+Local+"/"), nil
	}
	if ref, sub, ok := strings.Cut(target, ":"); ok && ref != Local {
		if actual, found := additions.FindKey(ref); found {
			return actual + ":" + sub, nil
		}
		if existing != nil {
			if actual, found := existing.FindKey(ref); found {
				return actual + ":" + sub, nil
			}
		}
	}
	return target, nil
}

// PrepareWithSkipObscure normalizes incoming entries, validates their basic
// shape before doing any network/rclone work, captures missing SFTP host keys,
// obscures newly provided plaintext secrets, and translates local composite paths
// while consulting both additions and existing stored remotes.
func PrepareWithSkipObscure(ctx context.Context, existing, additions Config, plain bool, skipObscure map[string]map[string]bool) error {
	for name, raw := range additions {
		p := NormalizeParams(raw)
		if p["type"] == "sftp" && p["shell_type"] == "unix" && p["allow_hashcheck"] != "true" {
			// rclone defaults SFTP remotes to shell_type=unix; normalize to none
			// unless the user explicitly opts into allow_hashcheck.
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
			typ := p["type"]
			for _, key := range slices.Sorted(maps.Keys(p)) {
				if p[key] == "" || !IsObscuredFieldForType(typ, key) || (skipObscure != nil && skipObscure[name][key]) {
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
		if IsWrapperType(p["type"]) {
			rewritten, err := rewriteCompositeTarget(p["remote"], existing, additions)
			if err != nil {
				return err
			}
			p["remote"] = rewritten
		}
		if p["type"] == "union" {
			ups, err := ParseUnionUpstreams(p["upstreams"])
			if err != nil {
				return err
			}
			for i := range ups {
				rewritten, err := rewriteCompositeTarget(ups[i].Target, existing, additions)
				if err != nil {
					return err
				}
				ups[i].Target = rewritten
			}
			p["upstreams"] = FormatUnionUpstreams(ups)
		}
		if p["type"] == "combine" {
			ups, err := ParseCombineUpstreams(p["upstreams"])
			if err != nil {
				return err
			}
			for i := range ups {
				rewritten, err := rewriteCompositeTarget(ups[i].Target, existing, additions)
				if err != nil {
					return err
				}
				ups[i].Target = rewritten
			}
			p["upstreams"] = FormatCombineUpstreams(ups)
		}
	}
	return nil
}
