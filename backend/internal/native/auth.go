package native

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"opensync/internal/quark"
)

const (
	authSessionActiveTTL    = 10 * time.Minute
	authSessionCompletedTTL = 30 * time.Minute
	authSessionMaxTracked   = 32
	authOAuthStartupTimeout = 10 * time.Second
	authOAuthWaitTimeout    = 5 * time.Minute
	authStepTimeout         = 45 * time.Second
	authSectionName         = "session"
)

var (
	localAuthURLPattern = regexp.MustCompile(`http://127\.0\.0\.1:(\d+)/auth\?state=([^\s"']+)`)

	directOAuthTypes = map[string]bool{
		"onedrive":           true,
		"drive":              true,
		"dropbox":            true,
		"box":                true,
		"pcloud":             true,
		"yandex":             true,
		"hidrive":            true,
		"huaweidrive":        true,
		"googlecloudstorage": true,
	}

	interactiveAuthTypes = map[string]bool{
		"onedrive":           true,
		"drive":              true,
		"dropbox":            true,
		"box":                true,
		"pcloud":             true,
		"yandex":             true,
		"jottacloud":         true,
		"hidrive":            true,
		"huaweidrive":        true,
		"googlecloudstorage": true,
		"iclouddrive":        true,
		"seafile":            true,
		"quark":              true,
	}

	quarkQRPollInterval = 2 * time.Second
)

// IsInteractiveAuthType reports whether typ supports GUI-guided authorization
// via OpenSync's rclone non-interactive config session manager.
func IsInteractiveAuthType(typ string) bool {
	return interactiveAuthTypes[canonicalRemoteType(typ)]
}

// AuthQuestionExample represents one selectable option returned by rclone's
// non-interactive config state machine.
type AuthQuestionExample struct {
	Value string `json:"value"`
	Help  string `json:"help"`
}

// AuthQuestion represents an interactive step returned by rclone (e.g., drive
// selection, Team Drive choice, Jottacloud login token, Apple/Seafile 2FA).
type AuthQuestion struct {
	State      string                `json:"state"`
	Name       string                `json:"name"`
	Help       string                `json:"help"`
	Default    string                `json:"default,omitempty"`
	Examples   []AuthQuestionExample `json:"examples,omitempty"`
	Required   bool                  `json:"required,omitempty"`
	IsPassword bool                  `json:"isPassword,omitempty"`
	Exclusive  bool                  `json:"exclusive,omitempty"`
	Type       string                `json:"type,omitempty"`
	Error      string                `json:"error,omitempty"`
}

// AuthStepSnapshot is the redacted public status of a guided auth session
// returned to the frontend. Secrets are never included in snapshots.
type AuthStepSnapshot struct {
	SessionID         string            `json:"sessionId"`
	Type              string            `json:"type"`
	Status            string            `json:"status"` // "oauth" | "qrcode" | "question" | "completed" | "failed" | "cancelled"
	AuthURL           string            `json:"authUrl,omitempty"`
	RedirectURI       string            `json:"redirectUri,omitempty"`
	QRCodeURL         string            `json:"qrCodeUrl,omitempty"`
	QRCodeImage       string            `json:"qrCodeImage,omitempty"`
	QRStatus          string            `json:"qrStatus,omitempty"` // "waiting" | "expired"
	Question          *AuthQuestion     `json:"question,omitempty"`
	PublicOptions     map[string]string `json:"publicOptions,omitempty"`
	ConfiguredSecrets []string          `json:"configuredSecrets,omitempty"`
	Error             string            `json:"error,omitempty"`
	ExpiresAt         int64             `json:"expiresAt"`
}

// CompletedAuthData holds the server-side parameters extracted from a finished
// guided auth session. ObscuredKeys marks password fields that are already
// rclone-obscured so PrepareWithSkipObscure does not double-obscure them.
type CompletedAuthData struct {
	Type         string
	Params       map[string]string
	ObscuredKeys map[string]bool
}

type rcloneConfigReply struct {
	State  string              `json:"State"`
	Option *rcloneConfigOption `json:"Option"`
	Error  string              `json:"Error"`
	Result string              `json:"Result"`
}

type rcloneConfigOption struct {
	Name       string `json:"Name"`
	Help       string `json:"Help"`
	Default    any    `json:"Default"`
	DefaultStr string `json:"DefaultStr"`
	Examples   []struct {
		Value string `json:"Value"`
		Help  string `json:"Help"`
	} `json:"Examples"`
	Required   bool   `json:"Required"`
	IsPassword bool   `json:"IsPassword"`
	Exclusive  bool   `json:"Exclusive"`
	Type       string `json:"Type"`
}

type authSession struct {
	mu sync.Mutex

	id                 string
	typ                string
	tempDir            string
	confPath           string
	proxyURL           string
	insecureSkipVerify bool

	status            string
	authURL           string
	redirectURI       string
	qrCodeURL         string
	qrCodeImage       string
	qrStatus          string
	oauthState        string
	oauthLocalBase    string
	oauthCancel       context.CancelFunc
	oauthDone         chan struct{}
	question          *AuthQuestion
	finalParams       map[string]string
	obscuredKeys      map[string]bool
	publicOptions     map[string]string
	configuredSecrets []string
	errMsg            string
	expiresAt         time.Time
	closed            bool
}

type authSessionManager struct {
	mu       sync.Mutex
	sessions map[string]*authSession
}

var globalAuthManager = &authSessionManager{
	sessions: map[string]*authSession{},
}

func loopbackClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			DisableKeepAlives:     true,
			ResponseHeaderTimeout: timeout,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func randomAuthSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (s *authSession) snapshotLocked() *AuthStepSnapshot {
	snap := &AuthStepSnapshot{
		SessionID:   s.id,
		Type:        s.typ,
		Status:      s.status,
		AuthURL:     s.authURL,
		RedirectURI: s.redirectURI,
		QRCodeURL:   s.qrCodeURL,
		QRCodeImage: s.qrCodeImage,
		QRStatus:    s.qrStatus,
		Error:       s.errMsg,
		ExpiresAt:   s.expiresAt.Unix(),
	}
	if s.question != nil {
		q := *s.question
		if len(s.question.Examples) > 0 {
			q.Examples = append([]AuthQuestionExample(nil), s.question.Examples...)
		}
		snap.Question = &q
	}
	if len(s.publicOptions) > 0 {
		snap.PublicOptions = maps.Clone(s.publicOptions)
	}
	if len(s.configuredSecrets) > 0 {
		snap.ConfiguredSecrets = append([]string(nil), s.configuredSecrets...)
	}
	return snap
}

func (s *authSession) snapshot() *AuthStepSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

func (s *authSession) cleanupTempDirLocked() {
	if s.tempDir != "" {
		_ = os.RemoveAll(s.tempDir)
		s.tempDir = ""
		s.confPath = ""
	}
}

func (s *authSession) cancelOAuthAndWait() {
	s.mu.Lock()
	cancel := s.oauthCancel
	done := s.oauthDone
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(4 * time.Second):
		}
	}
}

func (s *authSession) closeWithStatus(status string) {
	s.mu.Lock()
	cancel := s.oauthCancel
	done := s.oauthDone
	s.closed = true
	if status != "" && s.status != "completed" {
		s.status = status
	}
	s.oauthCancel = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-time.After(4 * time.Second):
		}
	}

	s.mu.Lock()
	s.cleanupTempDirLocked()
	s.mu.Unlock()
}

func (m *authSessionManager) reapExpiredLocked(now time.Time) []*authSession {
	var expired []*authSession
	for id, s := range m.sessions {
		s.mu.Lock()
		isExpired := !s.expiresAt.IsZero() && now.After(s.expiresAt)
		s.mu.Unlock()
		if isExpired {
			delete(m.sessions, id)
			expired = append(expired, s)
		}
	}
	return expired
}

func (m *authSessionManager) cancelActiveOAuthSessions(excludeID string) {
	m.mu.Lock()
	var active []*authSession
	for id, s := range m.sessions {
		if id == excludeID {
			continue
		}
		s.mu.Lock()
		isOAuth := s.status == "oauth" && s.oauthCancel != nil
		s.mu.Unlock()
		if isOAuth {
			active = append(active, s)
			delete(m.sessions, id)
		}
	}
	m.mu.Unlock()
	for _, s := range active {
		s.closeWithStatus("cancelled")
	}
}

func (m *authSessionManager) register(s *authSession) {
	now := time.Now()
	m.mu.Lock()
	expired := m.reapExpiredLocked(now)
	var evicted []*authSession
	for len(m.sessions) >= authSessionMaxTracked {
		var oldestID string
		var oldestExp time.Time
		for id, cur := range m.sessions {
			cur.mu.Lock()
			exp := cur.expiresAt
			cur.mu.Unlock()
			if oldestID == "" || exp.Before(oldestExp) {
				oldestID = id
				oldestExp = exp
			}
		}
		if oldestID == "" {
			break
		}
		evicted = append(evicted, m.sessions[oldestID])
		delete(m.sessions, oldestID)
	}
	m.sessions[s.id] = s
	m.mu.Unlock()

	for _, old := range append(expired, evicted...) {
		old.closeWithStatus("cancelled")
	}
}

func (m *authSessionManager) get(id string) *authSession {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	now := time.Now()
	m.mu.Lock()
	expired := m.reapExpiredLocked(now)
	s := m.sessions[id]
	m.mu.Unlock()
	for _, old := range expired {
		old.closeWithStatus("cancelled")
	}
	return s
}

func (m *authSessionManager) remove(id string) *authSession {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	m.mu.Lock()
	s := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	return s
}

// ShutdownAuthSessions terminates all active guided auth subprocesses and
// removes temporary directories. Call during server graceful shutdown.
func ShutdownAuthSessions() {
	globalAuthManager.mu.Lock()
	all := make([]*authSession, 0, len(globalAuthManager.sessions))
	for id, s := range globalAuthManager.sessions {
		all = append(all, s)
		delete(globalAuthManager.sessions, id)
	}
	globalAuthManager.mu.Unlock()
	for _, s := range all {
		s.closeWithStatus("cancelled")
	}
}

// LookupCompletedAuthSession returns a copy of the stored parameters from a
// completed auth session matching expectedType without consuming it.
func LookupCompletedAuthSession(sessionID, expectedType string) (*CompletedAuthData, error) {
	s := globalAuthManager.get(sessionID)
	if s == nil {
		return nil, errors.New("授权会话不存在或已过期，请重新完成授权")
	}
	expectedType = canonicalRemoteType(expectedType)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != "completed" {
		return nil, errors.New("当前授权尚未完成，请先完成授权步骤")
	}
	if expectedType != "" && s.typ != expectedType {
		return nil, errors.New("授权会话与当前所选存储类型不匹配，请重新授权")
	}
	return &CompletedAuthData{
		Type:         s.typ,
		Params:       maps.Clone(s.finalParams),
		ObscuredKeys: maps.Clone(s.obscuredKeys),
	}, nil
}

// ConsumeAuthSession deletes a completed auth session after the remote has
// been successfully persisted.
func ConsumeAuthSession(sessionID string) {
	if s := globalAuthManager.remove(sessionID); s != nil {
		s.closeWithStatus("")
	}
}

// CancelAuthSession stops and removes an auth session.
func CancelAuthSession(sessionID string) {
	if s := globalAuthManager.remove(sessionID); s != nil {
		s.closeWithStatus("cancelled")
	}
}

// GetAuthSessionStatus returns the current public status snapshot of sessionID.
func GetAuthSessionStatus(sessionID string) (*AuthStepSnapshot, error) {
	s := globalAuthManager.get(sessionID)
	if s == nil {
		return nil, errors.New("授权会话不存在或已过期，请重新发起授权")
	}
	return s.snapshot(), nil
}

// authCommand builds an rclone subprocess with PATH cleared so rclone never
// attempts to spawn a desktop browser (xdg-open) on the NAS host.
func (s *authSession) authCommand(ctx context.Context, args ...string) (*exec.Cmd, error) {
	fullArgs := make([]string, 0, len(args)+4)
	fullArgs = append(fullArgs, "--config", s.confPath, "--ask-password=false")
	if s.insecureSkipVerify {
		fullArgs = append(fullArgs, "--no-check-certificate")
	}
	fullArgs = append(fullArgs, args...)
	cmd, err := command(ctx, fullArgs...)
	if err != nil {
		return nil, err
	}
	// Clear PATH so rclone's local browser opener fails fast and prints the
	// loopback auth URL immediately instead of invoking xdg-open on the host.
	env := make([]string, 0, len(cmd.Env))
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "PATH=") {
			env = append(env, "PATH=")
			continue
		}
		env = append(env, kv)
	}
	if s.proxyURL != "" {
		env = applyProxyEnv(env, s.proxyURL)
	}
	cmd.Env = env
	return cmd, nil
}

// StartAuthSession launches a GUI-guided rclone non-interactive authorization
// session for typ using initialParams (with optional pre-obscured keys in
// skipObscure, e.g. preserved secrets when editing an existing remote).
func StartAuthSession(ctx context.Context, typ string, initialParams map[string]string, skipObscure map[string]bool, fallbackProxyURL string) (*AuthStepSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	typ = canonicalRemoteType(typ)
	if !IsInteractiveAuthType(typ) {
		return nil, fmt.Errorf("存储类型 %q 不需要引导授权，请直接填写表单参数", typ)
	}

	raw := maps.Clone(initialParams)
	if raw == nil {
		raw = map[string]string{}
	}
	raw["type"] = typ
	clean := NormalizeParams(raw)

	// Extract OpenSync-only transport options before emitting the temp INI.
	proxyURL := strings.TrimSpace(clean["proxy_url"])
	if proxyURL == "" {
		proxyURL = strings.TrimSpace(fallbackProxyURL)
	}
	if proxyURL != "" {
		if err := ValidateProxyURL(proxyURL); err != nil {
			return nil, err
		}
	}
	insecureSkip := clean["insecure_skip_verify"] == "true"

	if typ == "quark" {
		return startQuarkQRAuthSession(ctx, proxyURL, insecureSkip, strings.TrimSpace(clean["user_agent"]))
	}

	if err := Available(ctx); err != nil {
		return nil, err
	}
	for metaKey := range openSyncMetaFields {
		delete(clean, metaKey)
	}

	// Strip previously stored tokens so rclone starts a fresh authorization flow.
	delete(clean, "token")
	delete(clean, "trust_token")
	delete(clean, "cookies")
	if typ == "seafile" {
		delete(clean, "auth_token")
		clean["2fa"] = "true"
	}

	// Validate required pre-auth fields for non-OAuth interactive backends.
	switch typ {
	case "iclouddrive":
		if strings.TrimSpace(clean["apple_id"]) == "" || clean["password"] == "" {
			return nil, errors.New("请先填写 Apple ID 和密码后再发起登录验证")
		}
	case "seafile":
		if !validURL(clean["url"]) || strings.TrimSpace(clean["user"]) == "" || clean["pass"] == "" {
			return nil, errors.New("请先填写有效的 Seafile 服务地址、账号和密码后再发起两步验证登录")
		}
	}

	// Keep only allowed fields for typ and obscure newly supplied plaintext passwords.
	allowed := fields[typ]
	filtered := map[string]string{"type": typ}
	for _, k := range slices.Sorted(maps.Keys(clean)) {
		if k == "type" {
			continue
		}
		if !strings.Contains(" "+allowed+" ", " "+k+" ") {
			continue
		}
		v := clean[k]
		if v == "" {
			continue
		}
		if len(v) > 65536 || strings.IndexFunc(v, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("参数 %q 格式无效", k)
		}
		if IsObscuredFieldForType(typ, k) && !skipObscure[k] {
			obscured, err := obscureSecret(ctx, v)
			if err != nil {
				return nil, err
			}
			v = obscured
		}
		filtered[k] = v
	}

	sessionID, err := randomAuthSessionID()
	if err != nil {
		return nil, err
	}
	tempDir, err := os.MkdirTemp(nativeTempDir(), "opensync-auth-")
	if err != nil {
		return nil, err
	}
	confPath := filepath.Join(tempDir, "rclone.conf")
	var ini strings.Builder
	fmt.Fprintf(&ini, "[%s]\n", authSectionName)
	fmt.Fprintf(&ini, "type = %s\n", typ)
	for _, k := range slices.Sorted(maps.Keys(filtered)) {
		if k == "type" {
			continue
		}
		fmt.Fprintf(&ini, "%s = %s\n", k, filtered[k])
	}
	if err := os.WriteFile(confPath, []byte(ini.String()), 0600); err != nil {
		_ = os.RemoveAll(tempDir)
		return nil, err
	}

	sess := &authSession{
		id:                 sessionID,
		typ:                typ,
		tempDir:            tempDir,
		confPath:           confPath,
		proxyURL:           proxyURL,
		insecureSkipVerify: insecureSkip,
		status:             "starting",
		expiresAt:          time.Now().Add(authSessionActiveTTL),
	}
	globalAuthManager.register(sess)

	// Direct OAuth backends initialize scopes/endpoints in-memory during
	// state="" before opening the loopback OAuth server, so entering OAuth in
	// a single rclone process preserves custom scope/region/scope_access values.
	if directOAuthTypes[typ] {
		snap, err := sess.startOAuthSubprocess("")
		if err != nil {
			globalAuthManager.remove(sess.id)
			sess.closeWithStatus("failed")
			return nil, err
		}
		return snap, nil
	}

	stepCtx, cancel := context.WithTimeout(ctx, authStepTimeout)
	defer cancel()
	reply, stderr, runErr := sess.runConfigStep(stepCtx, "", "")
	if runErr != nil {
		msg := classifyAuthError(stderr, runErr)
		globalAuthManager.remove(sess.id)
		sess.closeWithStatus("failed")
		return nil, errors.New(msg)
	}

	snap, err := sess.processConfigReply(ctx, reply)
	if err != nil {
		globalAuthManager.remove(sess.id)
		sess.closeWithStatus("failed")
		return nil, err
	}
	return snap, nil
}

func startQuarkQRAuthSession(ctx context.Context, proxyURL string, insecureSkip bool, userAgent string) (*AuthStepSnapshot, error) {
	qOpts := quark.Options{
		Provider:           quark.ProviderQuark,
		ProxyURL:           proxyURL,
		InsecureSkipVerify: insecureSkip,
		UserAgent:          userAgent,
	}
	applyQuarkOptionsHook(&qOpts)

	startCtx, cancelStart := context.WithTimeout(ctx, 15*time.Second)
	defer cancelStart()
	qrSess, err := quark.StartQRAuth(startCtx, qOpts)
	if err != nil {
		return nil, err
	}

	sessionID, err := randomAuthSessionID()
	if err != nil {
		_ = qrSess.Close()
		return nil, err
	}

	pollCtx, pollCancel := context.WithTimeout(context.Background(), authOAuthWaitTimeout)
	doneCh := make(chan struct{})

	sess := &authSession{
		id:                 sessionID,
		typ:                "quark",
		proxyURL:           proxyURL,
		insecureSkipVerify: insecureSkip,
		status:             "qrcode",
		qrCodeURL:          qrSess.QRCodeURL(),
		qrCodeImage:        qrSess.QRCodeImage(),
		qrStatus:           string(quark.QRAuthWaiting),
		oauthCancel:        pollCancel,
		oauthDone:          doneCh,
		expiresAt:          time.Now().Add(authOAuthWaitTimeout),
	}
	globalAuthManager.register(sess)

	go func() {
		defer close(doneCh)
		defer pollCancel()
		defer qrSess.Close()

		interval := quarkQRPollInterval
		if interval <= 0 {
			interval = 2 * time.Second
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		consecutiveErrs := 0
		for {
			select {
			case <-pollCtx.Done():
				sess.mu.Lock()
				if !sess.closed && sess.status == "qrcode" {
					sess.status = "failed"
					sess.qrStatus = string(quark.QRAuthExpired)
					sess.errMsg = "登录二维码已过期，请刷新二维码重新扫码"
				}
				sess.oauthCancel = nil
				sess.mu.Unlock()
				return
			case <-ticker.C:
				reqCtx, reqCancel := context.WithTimeout(pollCtx, 15*time.Second)
				res, pollErr := qrSess.Poll(reqCtx)
				reqCancel()
				if pollErr != nil {
					if pollCtx.Err() != nil {
						continue
					}
					consecutiveErrs++
					if consecutiveErrs >= 5 {
						sess.mu.Lock()
						if !sess.closed && sess.status == "qrcode" {
							sess.status = "failed"
							sess.errMsg = pollErr.Error()
						}
						sess.oauthCancel = nil
						sess.mu.Unlock()
						return
					}
					continue
				}
				consecutiveErrs = 0
				switch res.State {
				case quark.QRAuthWaiting:
					continue
				case quark.QRAuthExpired:
					sess.mu.Lock()
					if !sess.closed && sess.status == "qrcode" {
						sess.status = "failed"
						sess.qrStatus = string(quark.QRAuthExpired)
						sess.errMsg = "登录二维码已过期，请刷新二维码重新扫码"
					}
					sess.oauthCancel = nil
					sess.mu.Unlock()
					return
				case quark.QRAuthConfirmed:
					finalParams := map[string]string{
						"type":   "quark",
						"cookie": res.Cookie,
					}
					if strings.TrimSpace(res.Nickname) != "" {
						finalParams["account_name"] = strings.TrimSpace(res.Nickname)
					}
					finalParams = NormalizeParams(finalParams)
					pub, secrets := RedactParams(finalParams)
					sess.mu.Lock()
					if !sess.closed {
						sess.finalParams = finalParams
						sess.obscuredKeys = map[string]bool{}
						sess.publicOptions = pub
						sess.configuredSecrets = secrets
						sess.status = "completed"
						sess.qrStatus = ""
						sess.errMsg = ""
						sess.expiresAt = time.Now().Add(authSessionCompletedTTL)
					}
					sess.oauthCancel = nil
					sess.mu.Unlock()
					return
				}
			}
		}
	}()

	return sess.snapshot(), nil
}

func obscureSecret(ctx context.Context, plaintext string) (string, error) {
	if len(plaintext) > 65536 || strings.IndexFunc(plaintext, unicode.IsControl) >= 0 {
		return "", errors.New("密码不能包含控制字符或超过长度限制")
	}
	cmd, err := command(ctx, "obscure", "-")
	if err != nil {
		return "", err
	}
	cmd.Stdin = strings.NewReader(plaintext)
	out, _, err := limitedOutputWithStderr(cmd, 64<<10)
	if err != nil {
		return "", errors.New("处理存储密码失败")
	}
	return strings.TrimSpace(string(out)), nil
}

func (s *authSession) runConfigStep(ctx context.Context, state, answer string) (*rcloneConfigReply, string, error) {
	args := []string{"config", "update", authSectionName, "--non-interactive"}
	if state != "" {
		args = append(args, "--continue", "--state", state, "--result", answer)
	}
	cmd, err := s.authCommand(ctx, args...)
	if err != nil {
		return nil, "", err
	}
	out, stderr, err := limitedOutputWithStderr(cmd, 512<<10)
	if err != nil {
		return nil, stderr, err
	}
	reply, parseErr := parseConfigReply(out)
	if parseErr != nil {
		return nil, stderr, parseErr
	}
	return reply, stderr, nil
}

func parseConfigReply(out []byte) (*rcloneConfigReply, error) {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return &rcloneConfigReply{}, nil
	}
	var reply rcloneConfigReply
	if err := json.Unmarshal([]byte(trimmed), &reply); err != nil {
		return nil, fmt.Errorf("解析 rclone 配置步骤响应失败: %w", err)
	}
	return &reply, nil
}

func convertConfigQuestion(reply *rcloneConfigReply) *AuthQuestion {
	if reply == nil || reply.State == "" {
		return nil
	}
	q := &AuthQuestion{
		State: reply.State,
		Error: strings.TrimSpace(reply.Error),
	}
	if reply.Option != nil {
		opt := reply.Option
		q.Name = opt.Name
		q.Help = strings.TrimSpace(opt.Help)
		q.Default = opt.DefaultStr
		if q.Default == "" && opt.Default != nil {
			q.Default = fmt.Sprintf("%v", opt.Default)
		}
		q.Required = opt.Required
		q.IsPassword = opt.IsPassword
		q.Exclusive = opt.Exclusive
		q.Type = opt.Type
		if len(opt.Examples) > 0 {
			q.Examples = make([]AuthQuestionExample, 0, len(opt.Examples))
			for _, ex := range opt.Examples {
				q.Examples = append(q.Examples, AuthQuestionExample{
					Value: ex.Value,
					Help:  strings.TrimSpace(ex.Help),
				})
			}
		}
	}
	return q
}

// processConfigReply advances through any auto-handled prompts (shared client
// warning, refresh token confirmation, OAuth local mode selection) and puts the
// session into "oauth", "question", or "completed".
func (s *authSession) processConfigReply(ctx context.Context, reply *rcloneConfigReply) (*AuthStepSnapshot, error) {
	cur := reply
	for hop := 0; hop < 6; hop++ {
		if cur == nil || cur.State == "" {
			if err := s.finalizeFromConfigFile(); err != nil {
				return nil, err
			}
			return s.snapshot(), nil
		}

		// Auto-enter GUI OAuth flow whenever rclone asks config_is_local.
		if strings.HasPrefix(cur.State, "*oauth-islocal") || (cur.Option != nil && cur.Option.Name == "config_is_local") {
			return s.startOAuthSubprocess(cur.State)
		}

		// Auto-accept non-blocking confirmations so the user goes straight to
		// the real provider authorization page without extra English prompts.
		if cur.Option != nil && (cur.Option.Name == "config_shared_client_id" || cur.Option.Name == "config_refresh_token") {
			stepCtx, cancel := context.WithTimeout(ctx, authStepTimeout)
			next, stderr, err := s.runConfigStep(stepCtx, cur.State, "true")
			cancel()
			if err != nil {
				return nil, errors.New(classifyAuthError(stderr, err))
			}
			cur = next
			continue
		}

		q := convertConfigQuestion(cur)
		s.mu.Lock()
		s.status = "question"
		s.question = q
		s.authURL = ""
		s.redirectURI = ""
		s.oauthState = ""
		s.oauthLocalBase = ""
		s.errMsg = ""
		s.expiresAt = time.Now().Add(authSessionActiveTTL)
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return snap, nil
	}
	return nil, errors.New("授权流程步骤过多，未能进入有效配置状态")
}

func (s *authSession) startOAuthSubprocess(state string) (*AuthStepSnapshot, error) {
	// Release port 53682 if an earlier OAuth session is still holding it.
	globalAuthManager.cancelActiveOAuthSessions(s.id)

	oauthCtx, cancel := context.WithTimeout(context.Background(), authOAuthWaitTimeout)
	var args []string
	if state == "" {
		args = []string{
			"config", "update", authSectionName,
			"config_shared_client_id=true",
			"config_refresh_token=true",
			"config_is_local=true",
			"--non-interactive",
		}
	} else {
		args = []string{
			"config", "update", authSectionName,
			"--non-interactive",
			"--continue",
			"--state", state,
			"--result", "true",
		}
	}
	cmd, err := s.authCommand(oauthCtx, args...)
	if err != nil {
		cancel()
		return nil, err
	}

	stdoutBuf := &cappedBuffer{limit: 512 << 10}
	stderrBuf := &tailBuffer{limit: maxStderrCapture}
	cmd.Stdout = struct{ io.Writer }{stdoutBuf}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("启动授权服务失败: %w", err)
	}

	type foundLocal struct {
		localURL string
		port     string
		state    string
	}
	foundCh := make(chan foundLocal, 1)
	doneCh := make(chan struct{})

	s.mu.Lock()
	s.oauthCancel = cancel
	s.oauthDone = doneCh
	s.status = "oauth"
	s.question = nil
	s.errMsg = ""
	s.expiresAt = time.Now().Add(authOAuthWaitTimeout + time.Minute)
	s.mu.Unlock()

	// Drain stderr continuously so rclone never blocks on pipe writes, while
	// extracting the local loopback auth URL as soon as it is printed.
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		scanner := bufio.NewScanner(stderrPipe)
		scanner.Buffer(make([]byte, 4096), 64*1024)
		sent := false
		for scanner.Scan() {
			line := scanner.Text()
			_, _ = stderrBuf.Write([]byte(line + "\n"))
			if !sent {
				if m := localAuthURLPattern.FindStringSubmatch(line); m != nil {
					sent = true
					foundCh <- foundLocal{
						localURL: m[0],
						port:     m[1],
						state:    m[2],
					}
				}
			}
		}
	}()

	// Wait for process exit in the background and transition session state
	// automatically when the OAuth callback finishes (whether via direct
	// localhost browser redirect or via SubmitAuthCallback forwarding).
	go func() {
		defer close(doneCh)
		defer cancel()
		waitErr := cmd.Wait()
		<-stderrDone

		s.mu.Lock()
		wasClosed := s.closed
		s.oauthCancel = nil
		s.mu.Unlock()
		if wasClosed {
			return
		}

		stderrText := stderrBuf.String()
		if waitErr != nil {
			msg := classifyAuthError(stderrText, waitErr)
			log.Printf("native rclone auth session %s failed (%v): %s", s.id, waitErr, sanitizeRcloneError(stderrText))
			s.mu.Lock()
			if !s.closed && s.status == "oauth" {
				s.status = "failed"
				s.errMsg = msg
				s.cleanupTempDirLocked()
			}
			s.mu.Unlock()
			return
		}

		reply, parseErr := parseConfigReply(stdoutBuf.Bytes())
		if parseErr != nil {
			s.mu.Lock()
			if !s.closed {
				s.status = "failed"
				s.errMsg = "解析授权结果失败，请重试"
				s.cleanupTempDirLocked()
			}
			s.mu.Unlock()
			return
		}

		if reply.State == "" {
			if err := s.finalizeFromConfigFile(); err != nil {
				s.mu.Lock()
				if !s.closed {
					s.status = "failed"
					s.errMsg = err.Error()
					s.cleanupTempDirLocked()
				}
				s.mu.Unlock()
			}
			return
		}

		// Transition to post-OAuth question (e.g. OneDrive drive selection or
		// Google Drive Team Drive prompt).
		q := convertConfigQuestion(reply)
		s.mu.Lock()
		if !s.closed {
			s.status = "question"
			s.question = q
			s.authURL = ""
			s.redirectURI = ""
			s.oauthState = ""
			s.oauthLocalBase = ""
			s.errMsg = ""
			s.expiresAt = time.Now().Add(authSessionActiveTTL)
		}
		s.mu.Unlock()
	}()

	// Wait until rclone prints its loopback auth URL or exits early.
	select {
	case found := <-foundCh:
		extURL, redirectURI, err := resolveExternalAuthURL(found.localURL)
		if err != nil {
			s.closeWithStatus("failed")
			return nil, err
		}
		s.mu.Lock()
		s.authURL = extURL
		s.redirectURI = redirectURI
		s.oauthState = found.state
		s.oauthLocalBase = "http://127.0.0.1:" + found.port
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return snap, nil

	case <-doneCh:
		s.mu.Lock()
		errMsg := s.errMsg
		s.mu.Unlock()
		if errMsg == "" {
			errMsg = classifyAuthError(stderrBuf.String(), errors.New("授权进程提前退出"))
		}
		return nil, errors.New(errMsg)

	case <-time.After(authOAuthStartupTimeout):
		s.closeWithStatus("failed")
		return nil, errors.New("启动网盘授权流程超时，请稍后重试")
	}
}

func resolveExternalAuthURL(localAuthURL string) (authURL, redirectURI string, err error) {
	client := loopbackClient(5 * time.Second)
	resp, err := client.Get(localAuthURL)
	if err != nil {
		return "", "", errors.New("无法读取本地授权跳转地址，请重试")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	loc := strings.TrimSpace(resp.Header.Get("Location"))
	if loc == "" {
		return "", "", errors.New("未能获取到网盘 OAuth 授权页面地址")
	}
	parsed, err := url.Parse(loc)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return "", "", errors.New("网盘返回的授权页面地址无效")
	}
	redirectURI = strings.TrimSpace(parsed.Query().Get("redirect_uri"))
	if redirectURI == "" {
		redirectURI = "http://localhost:53682/"
	}
	return loc, redirectURI, nil
}

// ParseAuthCallbackQuery extracts OAuth callback query parameters from a user-
// pasted callback URL or query string without ever trusting or dialing its host.
func ParseAuthCallbackQuery(rawInput, expectedState string) (url.Values, error) {
	trimmed := strings.TrimSpace(rawInput)
	trimmed = strings.Trim(trimmed, `"'<>`)
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return nil, errors.New("请粘贴授权完成后浏览器地址栏中的回调网址")
	}
	if len(trimmed) > 8192 || strings.IndexFunc(trimmed, unicode.IsControl) >= 0 {
		return nil, errors.New("回调网址格式无效或长度超限")
	}

	var rawQuery string
	switch {
	case strings.Contains(trimmed, "://"):
		u, err := url.Parse(trimmed)
		if err != nil {
			return nil, errors.New("回调网址格式无效，请完整复制浏览器地址栏中的 http://localhost:53682/?code=... 地址")
		}
		rawQuery = u.RawQuery
	case strings.Contains(trimmed, "?"):
		_, rawQuery, _ = strings.Cut(trimmed, "?")
		if idx := strings.IndexByte(rawQuery, '#'); idx >= 0 {
			rawQuery = rawQuery[:idx]
		}
	case strings.Contains(trimmed, "="):
		rawQuery = trimmed
		if idx := strings.IndexByte(rawQuery, '#'); idx >= 0 {
			rawQuery = rawQuery[:idx]
		}
	default:
		return nil, errors.New("请粘贴授权完成后浏览器地址栏中的完整回调网址（包含 ?code=... 参数）")
	}

	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, errors.New("回调网址参数解析失败，请重新完整复制地址栏链接")
	}
	if errCode := strings.TrimSpace(query.Get("error")); errCode != "" {
		desc := strings.TrimSpace(query.Get("error_description"))
		if desc != "" {
			return nil, fmt.Errorf("网盘拒绝了授权（%s: %s）", errCode, desc)
		}
		return nil, fmt.Errorf("网盘拒绝了授权（%s）", errCode)
	}
	if strings.TrimSpace(query.Get("code")) == "" {
		return nil, errors.New("回调网址中缺少授权码（code 参数），请确认在网盘页面点击同意授权后复制跳转地址")
	}
	gotState := strings.TrimSpace(query.Get("state"))
	if expectedState != "" {
		if gotState != "" && gotState != expectedState {
			return nil, errors.New("回调网址中的 state 与当前授权会话不匹配，请确认复制的是本次授权跳转后的最新地址")
		}
		if gotState == "" {
			query.Set("state", expectedState)
		}
	}
	return query, nil
}

// SubmitAuthCallback forwards the OAuth callback query parameters from the
// user's browser to rclone's local loopback OAuth listener and waits for the
// token exchange to complete.
func SubmitAuthCallback(ctx context.Context, sessionID, callbackInput string) (*AuthStepSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s := globalAuthManager.get(sessionID)
	if s == nil {
		return nil, errors.New("授权会话不存在或已过期，请重新发起授权")
	}

	s.mu.Lock()
	status := s.status
	expectedState := s.oauthState
	localBase := s.oauthLocalBase
	doneCh := s.oauthDone
	if status != "oauth" {
		snap := s.snapshotLocked()
		s.mu.Unlock()
		if status == "completed" || status == "question" {
			return snap, nil
		}
		return nil, errors.New("当前授权会话已结束，请重新发起授权")
	}
	s.mu.Unlock()

	query, err := ParseAuthCallbackQuery(callbackInput, expectedState)
	if err != nil {
		return nil, err
	}
	if localBase == "" {
		return nil, errors.New("本地授权监听尚未就绪，请重新发起授权")
	}

	// Strictly forward only to the loopback listener started by rclone itself.
	targetURL := localBase + "/?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}
	client := loopbackClient(15 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		// Check if rclone already completed via direct localhost redirect.
		if snap := s.snapshot(); snap.Status == "completed" || snap.Status == "question" {
			return snap, nil
		}
		return nil, errors.New("无法将授权码提交至本地授权服务，请重新发起授权")
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()

	if doneCh != nil {
		waitTimer := time.NewTimer(authStepTimeout)
		defer waitTimer.Stop()
		select {
		case <-doneCh:
		case <-waitTimer.C:
			return nil, errors.New("等待网盘换取授权令牌超时，请检查网络连接或代理设置后重试")
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	snap := s.snapshot()
	if snap.Status == "failed" {
		globalAuthManager.remove(s.id)
		return nil, errors.New(snap.Error)
	}
	return snap, nil
}

// ContinueAuthSession submits an answer to an interactive rclone config
// question (e.g., OneDrive drive selection, Google Drive Team Drive, Jottacloud
// login token, Apple iCloud 2FA, Seafile 2FA) and returns the next step.
func ContinueAuthSession(ctx context.Context, sessionID, state, answer string) (*AuthStepSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s := globalAuthManager.get(sessionID)
	if s == nil {
		return nil, errors.New("授权会话不存在或已过期，请重新发起授权")
	}

	s.mu.Lock()
	if s.status != "question" || s.question == nil {
		snap := s.snapshotLocked()
		s.mu.Unlock()
		if snap.Status == "completed" {
			return snap, nil
		}
		return nil, errors.New("当前授权步骤状态已变更，请刷新后重试")
	}
	expectedState := s.question.State
	s.mu.Unlock()

	if state == "" {
		state = expectedState
	}
	if state != expectedState {
		return nil, errors.New("配置步骤已变更，请按当前提示重新提交")
	}
	if len(answer) > 65536 || strings.IndexFunc(answer, unicode.IsControl) >= 0 {
		return nil, errors.New("输入内容包含非法控制字符或超过长度限制")
	}

	stepCtx, cancel := context.WithTimeout(ctx, authStepTimeout)
	defer cancel()
	reply, stderr, err := s.runConfigStep(stepCtx, state, answer)
	if err != nil {
		return nil, errors.New(classifyAuthError(stderr, err))
	}

	snap, err := s.processConfigReply(ctx, reply)
	if err != nil {
		return nil, err
	}
	return snap, nil
}

func (s *authSession) finalizeFromConfigFile() error {
	s.mu.Lock()
	confPath := s.confPath
	typ := s.typ
	s.mu.Unlock()

	if confPath == "" {
		return errors.New("授权临时配置文件不存在")
	}
	rawBytes, err := os.ReadFile(confPath)
	if err != nil {
		return errors.New("读取授权结果配置失败")
	}
	parsed, err := ParseINI(string(rawBytes))
	if err != nil {
		return fmt.Errorf("解析授权结果配置失败: %w", err)
	}
	section := parsed[authSectionName]
	if len(section) == 0 {
		return errors.New("未生成有效的授权配置")
	}

	allowed := fields[typ]
	finalParams := map[string]string{"type": typ}
	obscuredKeys := map[string]bool{}
	for k, v := range section {
		if k == "type" || v == "" {
			continue
		}
		if !strings.Contains(" "+allowed+" ", " "+k+" ") {
			continue
		}
		if IsObscuredFieldForType(typ, k) {
			finalParams[k] = v
			obscuredKeys[k] = true
			continue
		}
		finalParams[k] = v
	}
	finalParams = NormalizeParams(finalParams)
	// Restore exact obscured values in case NormalizeParams touched anything.
	for k := range obscuredKeys {
		finalParams[k] = section[k]
	}

	pub, secrets := RedactParams(finalParams)

	s.mu.Lock()
	s.finalParams = finalParams
	s.obscuredKeys = obscuredKeys
	s.publicOptions = pub
	s.configuredSecrets = secrets
	s.status = "completed"
	s.question = nil
	s.authURL = ""
	s.redirectURI = ""
	s.oauthState = ""
	s.oauthLocalBase = ""
	s.errMsg = ""
	s.expiresAt = time.Now().Add(authSessionCompletedTTL)
	s.cleanupTempDirLocked()
	s.mu.Unlock()
	return nil
}

func classifyAuthError(stderr string, err error) string {
	low := strings.ToLower(stderr)
	if err != nil {
		low += " " + strings.ToLower(err.Error())
	}
	switch {
	case strings.Contains(low, "address already in use") || strings.Contains(low, "53682"):
		return "本地授权回调端口 53682 已被其他进程占用，请释放该端口后重试"
	case strings.Contains(low, "access_denied") || strings.Contains(low, "cancelled") || strings.Contains(low, "canceled"):
		return "授权已取消"
	case strings.Contains(low, "invalid_grant") || strings.Contains(low, "code doesn't exist") || strings.Contains(low, "code has expired"):
		return "授权码无效或已过期，请重新点击授权按钮并在网盘完成登录后立即复制回调地址"
	case strings.Contains(low, "incorrect username or password") || strings.Contains(low, "bad password") || strings.Contains(low, "invalid credentials") || strings.Contains(low, "authentication failed") || strings.Contains(low, "401") || strings.Contains(low, "unauthorized"):
		return "账号、密码或验证码验证失败，请核对后重试"
	case strings.Contains(low, "no such host") || strings.Contains(low, "lookup ") || strings.Contains(low, "connection refused") || strings.Contains(low, "i/o timeout") || strings.Contains(low, "deadline exceeded") || strings.Contains(low, "timed out") || strings.Contains(low, "tls:") || strings.Contains(low, "x509:"):
		return "连接网盘授权服务器失败，请检查网络连接或在「高级与网络选项」中配置代理后重试"
	case strings.Contains(low, "invalid_client"):
		return "OAuth Client ID 或 Client Secret 无效，请核对或留空使用内置应用"
	default:
		// Extract the first "Error: ..." line from rclone stderr if present.
		for _, line := range strings.Split(stderr, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "Error:") {
				cleaned := strings.TrimSpace(strings.TrimPrefix(line, "Error:"))
				cleaned = sanitizeRcloneError(cleaned)
				if cleaned != "" && len(cleaned) <= 240 {
					return "授权失败：" + cleaned
				}
			}
		}
		return "网盘授权失败，请检查网络、代理设置或凭据后重试"
	}
}
