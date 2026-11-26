package quark

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	qrClientID = "532"
	qrVersion  = "1.2"

	qrStatusSuccess = 2000000
	qrStatusWaiting = 50004001
	qrStatusExpired = 50004002
)

// QRAuthState represents the current state of a Quark QR code login poll.
type QRAuthState string

const (
	QRAuthWaiting   QRAuthState = "waiting"
	QRAuthConfirmed QRAuthState = "confirmed"
	QRAuthExpired   QRAuthState = "expired"
)

// QRAuthSession tracks an active Quark CAS QR code login flow.
type QRAuthSession struct {
	client      *Client
	requestID   string
	token       string
	qrCodeURL   string
	qrCodeImage string
}

// QRCodeURL returns the scannable Quark login URL embedded in the QR code.
func (s *QRAuthSession) QRCodeURL() string { return s.qrCodeURL }

// QRCodeImage returns a base64 SVG data URL of the QR code ready for <img>.
func (s *QRAuthSession) QRCodeImage() string { return s.qrCodeImage }

// Token returns the CAS QR login token.
func (s *QRAuthSession) Token() string { return s.token }

// QRPollResult captures the outcome of a single poll against uop.quark.cn.
type QRPollResult struct {
	State    QRAuthState
	Cookie   string
	Nickname string
}

type qrTokenResp struct {
	Status  int    `json:"status"`
	Message string `json:"message"`
	Data    struct {
		Members struct {
			Token         string `json:"token"`
			ServiceTicket string `json:"service_ticket"`
		} `json:"members"`
	} `json:"data"`
}

func newUUIDv4() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// StartQRAuth initiates a Quark CAS QR login session via uop.quark.cn and
// generates a scannable QR code URL + SVG data URL.
func StartQRAuth(ctx context.Context, opts Options) (*QRAuthSession, error) {
	client, err := NewClient(opts)
	if err != nil {
		return nil, err
	}
	requestID := newUUIDv4()
	q := url.Values{
		"client_id":  {qrClientID},
		"v":          {qrVersion},
		"request_id": {requestID},
	}
	endpoint := client.qrAuthURL + "/cas/ajax/getTokenForQrcodeLogin?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	req.Header.Set("User-Agent", client.userAgent)
	req.Header.Set("Referer", client.profile.Referer)
	req.Header.Set("Accept", "application/json, text/plain, */*")

	resp, err := client.httpClient.Do(req)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("连接夸克扫码服务失败: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	_ = resp.Body.Close()
	if readErr != nil {
		_ = client.Close()
		return nil, fmt.Errorf("读取夸克扫码响应失败: %w", readErr)
	}
	client.MergeCookies(resp.Cookies())

	var parsed qrTokenResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("解析夸克扫码令牌响应失败: %w", err)
	}
	token := strings.TrimSpace(parsed.Data.Members.Token)
	if parsed.Status != qrStatusSuccess || token == "" {
		_ = client.Close()
		msg := strings.TrimSpace(parsed.Message)
		if msg == "" {
			msg = fmt.Sprintf("状态码 %d", parsed.Status)
		}
		return nil, fmt.Errorf("获取夸克登录二维码失败: %s", msg)
	}

	qrParams := url.Values{
		"token":        {token},
		"client_id":    {qrClientID},
		"ssb":          {"weblogin"},
		"uc_param_str": {""},
		"uc_biz_str":   {"S:custom|OPT:SAREA@0|OPT:IMMERSIVE@1|OPT:BACK_BTN_STYLE@0"},
	}
	qrURL := "https://su.quark.cn/4_eMHBJ?" + qrParams.Encode()
	qrImg, err := EncodeQRCodeSVGDataURL(qrURL)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("生成登录二维码图像失败: %w", err)
	}

	return &QRAuthSession{
		client:      client,
		requestID:   requestID,
		token:       token,
		qrCodeURL:   qrURL,
		qrCodeImage: qrImg,
	}, nil
}

// Close releases network resources held by the QR auth session.
func (s *QRAuthSession) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}

// Poll checks whether the user has scanned and confirmed the QR code. When
// confirmed, it automatically exchanges the service ticket for full Quark
// session cookies (including __pus / __puus) and validates account access.
func (s *QRAuthSession) Poll(ctx context.Context) (*QRPollResult, error) {
	q := url.Values{
		"client_id":  {qrClientID},
		"v":          {qrVersion},
		"token":      {s.token},
		"request_id": {s.requestID},
	}
	endpoint := s.client.qrAuthURL + "/cas/ajax/getServiceTicketByQrcodeToken?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", s.client.userAgent)
	req.Header.Set("Referer", s.client.profile.Referer)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if ck := s.client.Cookie(); ck != "" {
		req.Header.Set("Cookie", ck)
	}

	resp, err := s.client.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	_ = resp.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	s.client.MergeCookies(resp.Cookies())

	var parsed qrTokenResp
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("解析夸克扫码状态响应失败: %w", err)
	}
	switch parsed.Status {
	case qrStatusWaiting:
		return &QRPollResult{State: QRAuthWaiting}, nil
	case qrStatusExpired:
		return &QRPollResult{State: QRAuthExpired}, nil
	case qrStatusSuccess:
		st := strings.TrimSpace(parsed.Data.Members.ServiceTicket)
		if st == "" {
			return nil, errors.New("夸克扫码确认成功但缺少 service_ticket")
		}
		cookie, nickname, err := s.exchangeServiceTicket(ctx, st)
		if err != nil {
			return nil, err
		}
		return &QRPollResult{
			State:    QRAuthConfirmed,
			Cookie:   cookie,
			Nickname: nickname,
		}, nil
	default:
		msg := strings.TrimSpace(parsed.Message)
		if msg == "" {
			msg = fmt.Sprintf("未知状态码 %d", parsed.Status)
		}
		return nil, fmt.Errorf("夸克扫码登录失败: %s", msg)
	}
}

func (s *QRAuthSession) exchangeServiceTicket(ctx context.Context, serviceTicket string) (cookie string, nickname string, err error) {
	// Step 1: Exchange service_ticket at pan.quark.cn/account/info to obtain __pus / account cookies & nickname.
	infoURL := s.client.accountURL + "/account/info?" + url.Values{
		"st": {serviceTicket},
		"lw": {"scan"},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, infoURL, nil)
	if err != nil {
		return "", "", err
	}
	s.client.applyCommonHeaders(req)
	resp, err := s.client.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("换取夸克登录 Cookie 失败: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	_ = resp.Body.Close()
	s.client.MergeCookies(resp.Cookies())

	var accResp accountInfoResp
	if json.Unmarshal(body, &accResp) == nil {
		nickname = strings.TrimSpace(accResp.Data.Nickname)
	}

	// Step 2: Touch drive-pc API (/file/sort) so drive-pc issues/refreshes __puus
	// and confirms the harvested cookie can actually access the cloud drive.
	verifyCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	q := url.Values{
		"pdir_fid":     {DefaultRootFolderID},
		"_page":        {"1"},
		"_size":        {"1"},
		"_fetch_total": {"1"},
	}
	var sortCheck sortResp
	if err := s.client.doJSON(verifyCtx, http.MethodGet, "/file/sort", q, nil, &sortCheck); err != nil {
		// Also try /member as secondary trigger for __puus before failing.
		if _, aboutErr := s.client.About(verifyCtx); aboutErr != nil {
			return "", "", fmt.Errorf("扫码成功但未能获取有效网盘会话 Cookie: %w", err)
		}
	}

	cookie = s.client.Cookie()
	if !ValidateCookieFormat(cookie) {
		return "", "", errors.New("扫码完成但未获取到有效的夸克登录 Cookie，请重试")
	}
	return cookie, nickname, nil
}
