package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
)

// ImportCandidate describes one section parsed from an uploaded rclone.conf.
type ImportCandidate struct {
	Name              string            `json:"name"`
	Type              string            `json:"type"`
	Status            string            `json:"status"` // "ready" | "conflict" | "incompatible" | "invalid"
	ExistingName      string            `json:"existingName,omitempty"`
	ExistingType      string            `json:"existingType,omitempty"`
	CanReplace        bool              `json:"canReplace"`
	Summary           string            `json:"summary,omitempty"`
	Options           map[string]string `json:"options"`
	ConfiguredSecrets []string          `json:"configuredSecrets"`
	Warnings          []string          `json:"warnings,omitempty"`
	Error             string            `json:"error,omitempty"`
}

// ImportPreview summarizes all sections found in an uploaded rclone.conf.
type ImportPreview struct {
	Encrypted bool              `json:"encrypted"`
	Items     []ImportCandidate `json:"items"`
}

// autoDroppedImportFields are rclone-managed fields that are safe to drop on
// import because OpenSync manages them explicitly (e.g. host key pinning and
// shell_type=none on SFTP).
var autoDroppedImportFields = map[string]map[string]string{
	"sftp": {
		"known_hosts_file": "已忽略 known_hosts_file，导入时将自动重新获取并固定 SSH 主机公钥",
		"md5sum_command":   "已忽略 rclone 自动探测的 md5sum_command 字段",
		"sha1sum_command":  "已忽略 rclone 自动探测的 sha1sum_command 字段",
	},
}

func isEncryptedRcloneConfig(content string) bool {
	trimmed := strings.TrimPrefix(content, "\uFEFF")
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		return line == "RCLONE_ENCRYPT_V0:"
	}
	return false
}

// parseRawINI parses rclone INI sections without rejecting unsupported remote
// types upfront so the preview endpoint can report per-section status.
func parseRawINI(input string) (Config, error) {
	cfg := Config{}
	var section map[string]string
	for i, line := range strings.Split(strings.TrimPrefix(input, "\uFEFF"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			name := strings.TrimSpace(line[1 : len(line)-1])
			if name == "" {
				return nil, fmt.Errorf("第 %d 行的存储名称为空", i+1)
			}
			if _, dup := cfg.FindKey(name); dup {
				return nil, fmt.Errorf("第 %d 行的存储名称 %q 重复", i+1, name)
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
			return nil, fmt.Errorf("第 %d 行存在重复字段 %q", i+1, key)
		}
		section[key] = strings.TrimSpace(value)
	}
	if len(cfg) == 0 {
		return nil, errors.New("配置中没有命名存储")
	}
	return cfg, nil
}

// ImportFile only uses rclone's configuration reader for encrypted files. No
// backend is instantiated until the decoded sections pass our allowlist.
func ImportFile(ctx context.Context, content, password string) (Config, error) {
	if strings.TrimSpace(strings.TrimPrefix(content, "\uFEFF")) == "" {
		return nil, errors.New("rclone.conf 内容为空")
	}
	if len(content) > 512<<10 {
		return nil, errors.New("rclone.conf 超过 512 KiB")
	}
	if !isEncryptedRcloneConfig(content) {
		return ParseINI(content)
	}
	return decryptImportFile(ctx, content, password)
}

func decryptImportFile(ctx context.Context, content, password string) (Config, error) {
	if password == "" {
		return nil, errors.New("请输入 rclone.conf 的配置密码")
	}
	content = strings.TrimPrefix(content, "\uFEFF")
	f, e := os.CreateTemp(nativeTempDir(), "opensync-import-")
	if e != nil {
		return nil, e
	}
	defer f.Close()
	if e = os.Remove(f.Name()); e != nil {
		return nil, e
	}
	if _, e = f.WriteString(content); e != nil {
		return nil, e
	}
	if _, e = f.Seek(0, io.SeekStart); e != nil {
		return nil, e
	}
	cmd, e := command(ctx, "config", "dump", "--config", "/dev/fd/3", "--ask-password=false")
	if e != nil {
		return nil, e
	}
	cmd.ExtraFiles = []*os.File{f}
	cmd.Env = append(cmd.Env, "RCLONE_CONFIG_PASS="+password)
	out, e := limitedOutput(cmd, 2<<20)
	if e != nil {
		return nil, errors.New("无法解密 rclone.conf，请检查密码和文件格式")
	}
	var c Config
	if json.Unmarshal(out, &c) != nil || len(c) == 0 {
		return nil, errors.New("配置中没有有效的命名存储")
	}
	return c, nil
}

func parseImportRaw(ctx context.Context, content, password string) (Config, bool, error) {
	if strings.TrimSpace(strings.TrimPrefix(content, "\uFEFF")) == "" {
		return nil, false, errors.New("rclone.conf 内容为空")
	}
	if len(content) > 512<<10 {
		return nil, false, errors.New("rclone.conf 超过 512 KiB")
	}
	encrypted := isEncryptedRcloneConfig(content)
	if !encrypted {
		cfg, err := parseRawINI(content)
		return cfg, false, err
	}
	cfg, err := decryptImportFile(ctx, content, password)
	return cfg, true, err
}

// sanitizeImportedEntry normalizes one imported section and strips safe
// rclone-generated fields while recording user-visible warnings.
func sanitizeImportedEntry(raw map[string]string) (map[string]string, []string) {
	clean := NormalizeParams(raw)
	typ := clean["type"]
	var warnings []string
	if drops, ok := autoDroppedImportFields[typ]; ok {
		for _, k := range slices.Sorted(maps.Keys(drops)) {
			if _, present := clean[k]; present {
				delete(clean, k)
				warnings = append(warnings, drops[k])
			}
		}
	}
	if typ == "sftp" && clean["shell_type"] == "unix" {
		clean["shell_type"] = "none"
		warnings = append(warnings, "已将 SFTP shell_type 调整为 none（纯文件协议）")
	}
	if typ == "sftp" && clean["known_hosts"] == "" {
		warnings = append(warnings, "未包含内嵌 known_hosts，导入时将连接服务器自动获取并固定主机公钥")
	}
	return clean, warnings
}

func validateImportedEntryOffline(name string, p map[string]string) error {
	if err := ValidateEntry(name, p, true); err != nil {
		return err
	}
	return validateObscuredFields(name, p)
}

// InspectImport parses an uploaded rclone.conf and produces a per-section
// preview without mutating stored configuration or making network connections.
func InspectImport(ctx context.Context, content, password string, existing Config) (*ImportPreview, error) {
	rawCfg, encrypted, err := parseImportRaw(ctx, content, password)
	if err != nil {
		return nil, err
	}
	preview := &ImportPreview{
		Encrypted: encrypted,
		Items:     make([]ImportCandidate, 0, len(rawCfg)),
	}
	for _, name := range slices.Sorted(maps.Keys(rawCfg)) {
		clean, warnings := sanitizeImportedEntry(rawCfg[name])
		typ := clean["type"]
		public, secrets := RedactParams(clean)
		item := ImportCandidate{
			Name:              name,
			Type:              typ,
			Status:            "ready",
			Summary:           RemoteSummary(clean),
			Options:           public,
			ConfiguredSecrets: secrets,
			Warnings:          warnings,
		}
		if err := validateImportedEntryOffline(name, clean); err != nil {
			item.Status = "invalid"
			item.Error = err.Error()
		}
		if item.Status == "ready" && existing != nil {
			if existingKey, exists := existing.FindKey(name); exists {
				item.ExistingName = existingKey
				item.ExistingType = existing[existingKey]["type"]
				if item.ExistingType == typ {
					item.Status = "conflict"
					item.CanReplace = true
				} else {
					item.Status = "incompatible"
					item.Error = fmt.Sprintf("已存在同名存储 %q（类型 %s），无法用 %s 类型覆盖", existingKey, item.ExistingType, typ)
				}
			}
		}
		preview.Items = append(preview.Items, item)
	}
	return preview, nil
}

// ImportSelected parses rclone.conf and extracts either the chosen section names
// (when selected is non-empty) or all sections, stripping safe automatic fields
// and running offline entry checks before Prepare.
func ImportSelected(ctx context.Context, content, password string, selected []string) (Config, error) {
	rawCfg, _, err := parseImportRaw(ctx, content, password)
	if err != nil {
		return nil, err
	}
	filter := map[string]bool{}
	for _, s := range selected {
		s = strings.TrimSpace(s)
		if s != "" {
			filter[strings.ToLower(s)] = true
		}
	}
	result := Config{}
	for name, rawParams := range rawCfg {
		if len(filter) > 0 && !filter[strings.ToLower(name)] {
			continue
		}
		clean, _ := sanitizeImportedEntry(rawParams)
		if err := validateImportedEntryOffline(name, clean); err != nil {
			return nil, err
		}
		result[name] = clean
	}
	if len(result) == 0 {
		return nil, errors.New("请至少选择一个可导入的存储配置")
	}
	return result, nil
}
