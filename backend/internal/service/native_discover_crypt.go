package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"opensync/internal/native"
	"strconv"
	"strings"
	"time"
)

const openListProbeTimeout = 1500 * time.Millisecond

type NativeOpenListDiscoverInput struct {
	Ports     []int    `json:"ports,omitempty"`
	Endpoints []string `json:"endpoints,omitempty"`
}

type NativeOpenListDiscoverCandidate struct {
	Endpoint     string `json:"endpoint"`
	BaseURL      string `json:"baseUrl"`
	Reachable    bool   `json:"reachable"`
	StatusCode   int    `json:"statusCode,omitempty"`
	AuthRequired bool   `json:"authRequired"`
	LatencyMs    int64  `json:"latencyMs"`
	ServerType   string `json:"serverType,omitempty"`
	Error        string `json:"error,omitempty"`
}

type NativeOpenListSuggestedConfig struct {
	Name               string `json:"name"`
	Type               string `json:"type"`
	Vendor             string `json:"vendor"`
	URL                string `json:"url"`
	CompareMode        int    `json:"compareMode"`
	CompareModeLabel   string `json:"compareModeLabel"`
	RecommendationNote string `json:"recommendationNote"`
}

type NativeOpenListDiscoverResult struct {
	Found           bool                              `json:"found"`
	Message         string                            `json:"message"`
	SuggestedConfig *NativeOpenListSuggestedConfig    `json:"suggestedConfig,omitempty"`
	Candidates      []NativeOpenListDiscoverCandidate `json:"candidates"`
	CheckedAt       int64                             `json:"checkedAt"`
}

type NativeCryptRecoveryInput struct {
	Name            string `json:"name"`
	VerifyPassword  string `json:"verifyPassword,omitempty"`
	VerifyPassword2 string `json:"verifyPassword2,omitempty"`
}

type NativeCryptRecoveryBundle struct {
	Version                 int      `json:"version"`
	GeneratedAt             int64    `json:"generatedAt"`
	RemoteName              string   `json:"remoteName"`
	UnderlyingRemote        string   `json:"underlyingRemote"`
	UnderlyingSubPath       string   `json:"underlyingSubPath,omitempty"`
	Remote                  string   `json:"remote"`
	FilenameEncryption      string   `json:"filenameEncryption"`
	FilenameEncoding        string   `json:"filenameEncoding"`
	DirectoryNameEncryption string   `json:"directoryNameEncryption"`
	NoDataEncryption        bool     `json:"noDataEncryption"`
	Suffix                  string   `json:"suffix,omitempty"`
	HasPassword             bool     `json:"hasPassword"`
	HasPassword2            bool     `json:"hasPassword2"`
	ManagedByJob            bool     `json:"managedByJob"`
	ManagedJobLabel         string   `json:"managedJobLabel,omitempty"`
	RcloneSnippetTemplate   string   `json:"rcloneSnippetTemplate"`
	RcloneCommand           string   `json:"rcloneCommand"`
	RecoverySteps           []string `json:"recoverySteps"`
	Warning                 string   `json:"warning"`
}

type NativeCryptRecoveryResult struct {
	Name              string                    `json:"name"`
	PasswordChecked   bool                      `json:"passwordChecked"`
	PasswordVerified  *bool                     `json:"passwordVerified,omitempty"`
	Password2Checked  bool                      `json:"password2Checked"`
	Password2Verified *bool                     `json:"password2Verified,omitempty"`
	VerifyMessage     string                    `json:"verifyMessage,omitempty"`
	Bundle            NativeCryptRecoveryBundle `json:"bundle"`
	RecoveryText      string                    `json:"recoveryText"`
}

func isAllowedLoopbackHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	h = strings.TrimPrefix(h, "[")
	h = strings.TrimSuffix(h, "]")
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func normalizeLoopbackProbeEndpoint(raw string) (endpoint string, baseURL string, err error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", errors.New("探测地址不能为空")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", "", errors.New("本机探测地址格式无效")
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme != "http" && scheme != "https" {
		return "", "", errors.New("仅支持 http:// 或 https:// 本机回环地址探测")
	}
	if u.User != nil {
		return "", "", errors.New("探测地址不允许包含账号密码信息")
	}
	host := u.Hostname()
	if !isAllowedLoopbackHost(host) {
		return "", "", errors.New("出于安全限制，仅允许探测本机回环地址（127.0.0.1、localhost 或 [::1]）")
	}
	portStr := u.Port()
	if portStr == "" {
		if scheme == "https" {
			portStr = "443"
		} else {
			portStr = "5244"
		}
	}
	port, convErr := strconv.Atoi(portStr)
	if convErr != nil || port < 1 || port > 65535 {
		return "", "", errors.New("探测端口无效（需为 1–65535）")
	}
	formattedHost := net.JoinHostPort(host, strconv.Itoa(port))
	baseURL = scheme + "://" + formattedHost
	cleanedPath := strings.TrimRight(u.Path, "/")
	if cleanedPath == "" {
		cleanedPath = "/dav"
	}
	endpoint = baseURL + cleanedPath
	return endpoint, baseURL, nil
}

type openListProbeTarget struct {
	endpoint string
	baseURL  string
}

func buildOpenListCandidates(in NativeOpenListDiscoverInput) ([]openListProbeTarget, error) {
	if len(in.Endpoints) > 8 || len(in.Ports) > 8 {
		return nil, errors.New("单次最多探测 8 个本机端口或地址")
	}
	seen := map[string]bool{}
	var out []openListProbeTarget
	add := func(raw string) error {
		ep, base, err := normalizeLoopbackProbeEndpoint(raw)
		if err != nil {
			return err
		}
		if !seen[ep] {
			seen[ep] = true
			out = append(out, openListProbeTarget{endpoint: ep, baseURL: base})
		}
		return nil
	}

	if len(in.Endpoints) == 0 && len(in.Ports) == 0 {
		for _, def := range []string{
			"http://127.0.0.1:5244/dav",
			"http://localhost:5244/dav",
			"http://[::1]:5244/dav",
			"http://127.0.0.1:5245/dav",
		} {
			if err := add(def); err != nil {
				return nil, err
			}
		}
		return out, nil
	}

	for _, port := range in.Ports {
		if port < 1 || port > 65535 {
			return nil, errors.New("探测端口无效（需为 1–65535）")
		}
		if err := add(fmt.Sprintf("http://127.0.0.1:%d/dav", port)); err != nil {
			return nil, err
		}
	}
	for _, ep := range in.Endpoints {
		if err := add(ep); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func newLoopbackDiscoveryClient() *http.Client {
	dialer := &net.Dialer{Timeout: openListProbeTimeout}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			if !isAllowedLoopbackHost(host) {
				return nil, errors.New("拒绝连接非本机回环地址")
			}
			if ip := net.ParseIP(host); ip != nil {
				if !ip.IsLoopback() {
					return nil, errors.New("拒绝连接非回环 IP")
				}
				return dialer.DialContext(ctx, network, address)
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil || len(ips) == 0 {
				return nil, errors.New("本机地址解析失败")
			}
			for _, ipAddr := range ips {
				if !ipAddr.IP.IsLoopback() {
					return nil, errors.New("检测到非本机回环解析结果，已阻止探测")
				}
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
	}
	return &http.Client{
		Timeout:   openListProbeTimeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func probeSingleOpenList(ctx context.Context, client *http.Client, endpoint, baseURL string) NativeOpenListDiscoverCandidate {
	cand := NativeOpenListDiscoverCandidate{
		Endpoint: endpoint,
		BaseURL:  baseURL,
	}
	start := time.Now()
	reqCtx, cancel := context.WithTimeout(ctx, openListProbeTimeout)
	defer cancel()

	target := strings.TrimRight(endpoint, "/") + "/"
	req, err := http.NewRequestWithContext(reqCtx, "PROPFIND", target, nil)
	if err != nil {
		cand.LatencyMs = time.Since(start).Milliseconds()
		cand.Error = "无法构造探测请求"
		return cand
	}
	req.Header.Set("Depth", "0")

	resp, err := client.Do(req)
	if err != nil {
		cand.LatencyMs = time.Since(start).Milliseconds()
		cand.Error = "连接失败或端口未启动服务"
		return cand
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()

	cand.LatencyMs = time.Since(start).Milliseconds()
	cand.StatusCode = resp.StatusCode
	serverHeader := strings.TrimSpace(resp.Header.Get("Server"))

	switch resp.StatusCode {
	case http.StatusOK, http.StatusMultiStatus:
		cand.Reachable = true
		cand.AuthRequired = false
		cand.ServerType = inferOpenListServerLabel(serverHeader)
	case http.StatusUnauthorized, http.StatusForbidden:
		cand.Reachable = true
		cand.AuthRequired = true
		cand.ServerType = inferOpenListServerLabel(serverHeader)
	default:
		// Fallback check on /api/public/settings for OpenList / AList web instances
		// whose WebDAV path differs or returned 404/405.
		if probeOpenListPublicAPI(reqCtx, client, baseURL) {
			cand.Reachable = true
			cand.AuthRequired = true
			cand.ServerType = "OpenList / AList"
			return cand
		}
		cand.Error = fmt.Sprintf("端口有响应但未检测到 WebDAV 端点（HTTP %d）", resp.StatusCode)
	}
	return cand
}

func inferOpenListServerLabel(serverHeader string) string {
	lower := strings.ToLower(serverHeader)
	switch {
	case strings.Contains(lower, "openlist"):
		return "OpenList"
	case strings.Contains(lower, "alist"):
		return "AList"
	default:
		return "OpenList / AList / WebDAV"
	}
}

func probeOpenListPublicAPI(ctx context.Context, client *http.Client, baseURL string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/public/settings", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16384))
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var payload struct {
		Code int `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	return payload.Code == 200 && payload.Data != nil
}

// DiscoverOpenList probes local loopback WebDAV endpoints for OpenList / AList
// instances without permitting arbitrary non-loopback SSRF targets.
func DiscoverOpenList(ctx context.Context, in NativeOpenListDiscoverInput) (*NativeOpenListDiscoverResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	candidatesSpec, err := buildOpenListCandidates(in)
	if err != nil {
		return nil, publicError(err.Error())
	}

	client := newLoopbackDiscoveryClient()
	candidates := make([]NativeOpenListDiscoverCandidate, 0, len(candidatesSpec))
	var firstMatch *NativeOpenListDiscoverCandidate
	for _, spec := range candidatesSpec {
		res := probeSingleOpenList(ctx, client, spec.endpoint, spec.baseURL)
		candidates = append(candidates, res)
		if res.Reachable && firstMatch == nil {
			matched := res
			firstMatch = &matched
		}
	}

	out := &NativeOpenListDiscoverResult{
		Found:      firstMatch != nil,
		Candidates: candidates,
		CheckedAt:  time.Now().Unix(),
	}
	if firstMatch == nil {
		out.Message = "未在本机常用端口（5244 / 5245）检测到 OpenList 或 AList WebDAV 服务，你仍可手动填写 NAS IP 或域名。"
		return out, nil
	}

	authDesc := "可直接填充并使用（如有需要可补充账号密码）"
	if firstMatch.AuthRequired {
		authDesc = "WebDAV 已启用鉴权，填充后请填写 OpenList / AList 用户名与密码"
	}
	out.Message = fmt.Sprintf("检测到本机 WebDAV 服务：%s（响应 %dms），%s。", firstMatch.Endpoint, firstMatch.LatencyMs, authDesc)
	out.SuggestedConfig = &NativeOpenListSuggestedConfig{
		Name:               "openlist",
		Type:               "webdav",
		Vendor:             "other",
		URL:                firstMatch.Endpoint,
		CompareMode:        1,
		CompareModeLabel:   "仅按文件大小比对",
		RecommendationNote: "建议保持 WebDAV 类型为「标准 WebDAV / OpenList / AList（other）」；创建以它为目标的同步任务时建议配合「仅按文件大小」比对，避免网盘文件触发逐文件下载校验。",
	}
	return out, nil
}

// BuildCryptRecoveryBundle constructs a non-secret recovery package and optional
// password verification result for an existing crypt remote.
func BuildCryptRecoveryBundle(_ context.Context, in NativeCryptRecoveryInput) (*NativeCryptRecoveryResult, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, publicError("请指定要导出密钥恢复包的加密存储名称")
	}
	_, c, err := loadExistingNativeRow()
	if err != nil {
		return nil, err
	}
	p, ok := c[name]
	if !ok {
		return nil, publicError(fmt.Sprintf("未找到远程存储「%s」", name))
	}
	if p["type"] != "crypt" {
		return nil, publicError(fmt.Sprintf("「%s」不是加密存储（crypt），无法生成密钥恢复包", name))
	}

	fnEnc := strings.TrimSpace(p["filename_encryption"])
	if fnEnc == "" {
		fnEnc = "standard"
	}
	fnEncoding := strings.TrimSpace(p["filename_encoding"])
	if fnEncoding == "" {
		fnEncoding = "base32"
	}
	dirEnc := strings.TrimSpace(p["directory_name_encryption"])
	if dirEnc == "" {
		dirEnc = "true"
	}
	noDataEnc := p["no_data_encryption"] == "true"
	suffix := strings.TrimSpace(p["suffix"])
	managedByJob := p["managed_by_job"] == "true"
	managedJobLabel := strings.TrimSpace(p["managed_job_label"])

	underlyingRemote, underlyingSub, _ := strings.Cut(strings.TrimSpace(p["remote"]), ":")
	hasPassword := strings.TrimSpace(p["password"]) != ""
	hasPassword2 := strings.TrimSpace(p["password2"]) != ""

	res := &NativeCryptRecoveryResult{
		Name: name,
	}
	var verifyParts []string
	if in.VerifyPassword != "" {
		res.PasswordChecked = true
		matched := false
		if hasPassword {
			okVerify, verifyErr := native.VerifyObscured(p["password"], in.VerifyPassword)
			if verifyErr == nil && okVerify {
				matched = true
			}
		}
		res.PasswordVerified = &matched
		if matched {
			verifyParts = append(verifyParts, "主加密密码验证通过")
		} else {
			verifyParts = append(verifyParts, "主加密密码与当前配置不一致")
		}
	}
	if in.VerifyPassword2 != "" {
		res.Password2Checked = true
		matched2 := false
		if hasPassword2 {
			okVerify2, verifyErr := native.VerifyObscured(p["password2"], in.VerifyPassword2)
			if verifyErr == nil && okVerify2 {
				matched2 = true
			}
		}
		res.Password2Verified = &matched2
		if matched2 {
			verifyParts = append(verifyParts, "加密盐（password2）验证通过")
		} else if !hasPassword2 {
			verifyParts = append(verifyParts, "当前存储未设置加密盐（password2）")
		} else {
			verifyParts = append(verifyParts, "加密盐（password2）与当前配置不一致")
		}
	}
	if len(verifyParts) > 0 {
		res.VerifyMessage = strings.Join(verifyParts, "；")
	}

	var snippet strings.Builder
	fmt.Fprintf(&snippet, "[%s]\n", name)
	fmt.Fprintf(&snippet, "type = crypt\n")
	fmt.Fprintf(&snippet, "remote = %s\n", strings.TrimSpace(p["remote"]))
	fmt.Fprintf(&snippet, "filename_encryption = %s\n", fnEnc)
	fmt.Fprintf(&snippet, "filename_encoding = %s\n", fnEncoding)
	fmt.Fprintf(&snippet, "directory_name_encryption = %s\n", dirEnc)
	if noDataEnc {
		snippet.WriteString("no_data_encryption = true\n")
	}
	if suffix != "" {
		fmt.Fprintf(&snippet, "suffix = %s\n", suffix)
	}
	snippet.WriteString("# password = <请填入你的主加密密码，或通过 `rclone obscure -` 生成的混淆值>\n")
	if hasPassword2 {
		snippet.WriteString("# password2 = <请填入你的加密盐（password2），或通过 `rclone obscure -` 生成的混淆值>\n")
	}

	rcloneCmd := fmt.Sprintf("rclone copy %s: ./restored-%s", name, name)
	steps := []string{
		fmt.Sprintf("在 OpenSync 恢复：进入「存储管理」找到「%s」并点击「浏览与恢复」，或在新建同步任务时将源路径选为「/%s」、目标路径选为本地目录即可解密还原。", name, name),
		"异机或离线恢复：在任意安装 rclone 的设备上先配置同名底层存储（" + underlyingRemote + "），再按本恢复包中的 rclone.conf 段落重建 crypt 配置并填入主密码（与加密盐）。",
		fmt.Sprintf("命令行还原示例：%s（将先解密文件名与文件内容后下载到本地目录）。", rcloneCmd),
	}

	bundle := NativeCryptRecoveryBundle{
		Version:                 1,
		GeneratedAt:             time.Now().Unix(),
		RemoteName:              name,
		UnderlyingRemote:        underlyingRemote,
		UnderlyingSubPath:       underlyingSub,
		Remote:                  strings.TrimSpace(p["remote"]),
		FilenameEncryption:      fnEnc,
		FilenameEncoding:        fnEncoding,
		DirectoryNameEncryption: dirEnc,
		NoDataEncryption:        noDataEnc,
		Suffix:                  suffix,
		HasPassword:             hasPassword,
		HasPassword2:            hasPassword2,
		ManagedByJob:            managedByJob,
		ManagedJobLabel:         managedJobLabel,
		RcloneSnippetTemplate:   snippet.String(),
		RcloneCommand:           rcloneCmd,
		RecoverySteps:           steps,
		Warning:                 "安全提醒：主加密密码与加密盐是解密云端文件的唯一凭证，一旦丢失任何人都无法还原文件。若在导出的备份中包含明文密码，请务必存放在密码管理器或离线安全介质中，切勿直接保存在同一云盘目录下。",
	}

	var txt strings.Builder
	fmt.Fprintf(&txt, "=== OpenSync 加密存储密钥恢复包：%s ===\n", name)
	fmt.Fprintf(&txt, "生成时间：%s\n", time.Unix(bundle.GeneratedAt, 0).Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&txt, "底层路径：%s\n", bundle.Remote)
	fmt.Fprintf(&txt, "文件名加密方式：%s\n", bundle.FilenameEncryption)
	fmt.Fprintf(&txt, "文件名编码：%s\n", bundle.FilenameEncoding)
	fmt.Fprintf(&txt, "目录名加密：%s\n", bundle.DirectoryNameEncryption)
	if bundle.NoDataEncryption {
		txt.WriteString("文件内容加密：关闭（仅加密文件名 no_data_encryption=true）\n")
	} else {
		txt.WriteString("文件内容加密：开启（完整数据加密）\n")
	}
	if bundle.Suffix != "" {
		fmt.Fprintf(&txt, "密文后缀：%s\n", bundle.Suffix)
	}
	fmt.Fprintf(&txt, "是否设置加密盐（password2）：%v\n", bundle.HasPassword2)
	if bundle.ManagedByJob {
		label := bundle.ManagedJobLabel
		if label == "" {
			label = "同步任务向导内联创建"
		}
		fmt.Fprintf(&txt, "所属任务加密通道：%s\n", label)
	}
	txt.WriteString("\n--- rclone.conf 配置模板 ---\n")
	txt.WriteString(bundle.RcloneSnippetTemplate)
	txt.WriteString("\n--- 解密恢复步骤 ---\n")
	for i, step := range steps {
		fmt.Fprintf(&txt, "%d. %s\n", i+1, step)
	}
	txt.WriteString("\n" + bundle.Warning + "\n")

	res.Bundle = bundle
	res.RecoveryText = txt.String()
	return res, nil
}
