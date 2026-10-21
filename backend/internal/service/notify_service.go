package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"opensync/internal/mapper"
	"opensync/internal/model"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxNotifyResponseBytes = 1 << 20 // 1MB

// Delivery outcome stored on a notify config and returned by GET /notify.
// notifySendStatusUnknown is the column default, meaning "no task notification
// has been attempted for this config yet".
const (
	notifySendStatusUnknown = 0
	notifySendStatusSuccess = 1
	notifySendStatusFailed  = 2
)

// maxNotifyErrorLength caps the stored failure reason. Provider responses can be
// long, and the list endpoint returns one of these per config.
const maxNotifyErrorLength = 300

var wecomTokenCache struct {
	sync.Mutex
	entries map[wecomCredentials]wecomTokenEntry
}

type wecomCredentials struct {
	corpID, corpSecret string
}

type wecomTokenEntry struct {
	token   string
	expires time.Time
}

// notifyErrorURLPattern finds URLs inside an error message so they can be
// masked before the text is stored. The character class stops at quotes because
// net/http formats its errors as `Post "<url>": <cause>`.
var notifyErrorURLPattern = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.\-]*://[^\s"']+`)

var cloudMetadataCIDRs []*net.IPNet

func init() {
	// LAN and loopback destinations stay allowed on purpose: self-hosted
	// webhook receivers on the NAS or the local network are a supported setup.
	// Only link-local and the cloud metadata endpoints are blocked.
	for _, cidr := range []string{
		"169.254.0.0/16",     // IPv4 link-local, incl. AWS/GCP/Azure/OpenStack metadata at .169.254
		"100.100.100.200/32", // Alibaba Cloud metadata
		"fe80::/10",          // IPv6 link-local
		"fd00:ec2::254/128",  // AWS IPv6 metadata
	} {
		_, network, _ := net.ParseCIDR(cidr)
		cloudMetadataCIDRs = append(cloudMetadataCIDRs, network)
	}
}

func isCloudMetadataIP(ip net.IP) bool {
	// net.IPNet.Contains already matches an IPv4-mapped IPv6 address
	// (::ffff:169.254.169.254) against an IPv4 network, but normalising first
	// keeps that guarantee explicit rather than incidental.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, cidr := range cloudMetadataCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	// IPv6 transition prefixes carry an IPv4 address the network translates
	// to: NAT64 (64:ff9b::a9fe:a9fe) and 6to4 (2002:a9fe:a9fe::) both reach
	// 169.254.169.254 through a gateway, so the embedded address is checked
	// against the same list.
	if embedded := embeddedIPv4(ip); embedded != nil {
		for _, cidr := range cloudMetadataCIDRs {
			if cidr.Contains(embedded) {
				return true
			}
		}
	}
	return false
}

var (
	nat64WellKnownPrefix = mustParseCIDR("64:ff9b::/96")
	nat64LocalPrefix     = mustParseCIDR("64:ff9b:1::/48")
	sixToFourPrefix      = mustParseCIDR("2002::/16")
)

func mustParseCIDR(cidr string) *net.IPNet {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	return network
}

// embeddedIPv4 returns the IPv4 address encoded in a NAT64 or 6to4 address,
// or nil for any other address.
func embeddedIPv4(ip net.IP) net.IP {
	if ip.To4() != nil {
		return nil
	}
	ip16 := ip.To16()
	if ip16 == nil {
		return nil
	}
	switch {
	case nat64WellKnownPrefix.Contains(ip16), nat64LocalPrefix.Contains(ip16):
		// RFC 6052 /96 layout: the IPv4 address is the last 32 bits. The
		// local-use /48 is also checked at the /96 position, which is what
		// translators configured with a /96 out of it use.
		return net.IPv4(ip16[12], ip16[13], ip16[14], ip16[15]).To4()
	case sixToFourPrefix.Contains(ip16):
		// RFC 3056: 2002:VVVV:VVVV::/48.
		return net.IPv4(ip16[2], ip16[3], ip16[4], ip16[5]).To4()
	}
	return nil
}

// webhookLookupTimeout bounds the early destination check. net.LookupHost had
// no deadline, so a stalled resolver held a test send (and the request
// goroutine) for as long as the system resolver cared to wait.
const webhookLookupTimeout = 5 * time.Second

// webhookLookupIPAddr is the resolver used by validateWebhookDestination;
// tests replace it.
var webhookLookupIPAddr = net.DefaultResolver.LookupIPAddr

// validateWebhookDestination rejects a URL whose host resolves to a blocked
// address before any request is built. It is an early, friendly error only:
// validatedNotifyDialContext repeats the check on every connection (including
// redirects and DNS rebinding), and that dial-time check is the real boundary.
func validateWebhookDestination(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("webhook destination has no host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if isCloudMetadataIP(ip) {
			return fmt.Errorf("webhook destination %s is a blocked address", host)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), webhookLookupTimeout)
	defer cancel()
	addrs, err := webhookLookupIPAddr(ctx, host)
	if err != nil {
		return nil // DNS failure is not a security block; the dial-time check handles it
	}
	for _, addr := range addrs {
		if isCloudMetadataIP(addr.IP) {
			return fmt.Errorf("webhook destination %s resolves to blocked address %s", host, addr.IP)
		}
	}
	return nil
}

// Resolve on each connection and dial only an address from that resolution.
// The URL hostname remains unchanged for HTTP Host and TLS certificate checks.
func validatedNotifyDialContext(
	lookup func(context.Context, string) ([]net.IPAddr, error),
	dial func(context.Context, string, string) (net.Conn, error),
) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		var ips []net.IPAddr
		if ip := net.ParseIP(host); ip != nil {
			ips = []net.IPAddr{{IP: ip}}
		} else {
			ips, err = lookup(ctx, host)
			if err != nil {
				return nil, err
			}
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("webhook destination %s has no addresses", host)
		}
		for _, ip := range ips {
			if ip.IP == nil || isCloudMetadataIP(ip.IP) {
				return nil, fmt.Errorf("webhook destination %s resolves to blocked address %s", host, ip.String())
			}
		}
		var dialErr error
		for _, ip := range ips {
			conn, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			dialErr = err
		}
		return nil, dialErr
	}
}

var forbiddenWebhookHeaders = map[string]struct{}{
	"host": {}, "cookie": {}, "set-cookie": {},
	"transfer-encoding": {}, "content-length": {},
	"connection": {}, "upgrade": {},
}

var notifyHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	// Redirects are not followed. net/http forwards custom headers (X-Token,
	// a user-supplied Authorization, …) to a redirect target on another host
	// and happily follows https→http, so a compromised or misconfigured
	// endpoint could harvest the credential or downgrade the transport. No
	// supported provider redirects a send request, so a 3xx is reported as a
	// failed delivery by the status check in sendNotifyRequestBytes.
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
	Transport: &http.Transport{
		// Webhook destinations may be self-hosted on a LAN or loopback address;
		// do not reject them after DNS resolution.
		MaxIdleConns:        50,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		DialContext: validatedNotifyDialContext(
			net.DefaultResolver.LookupIPAddr,
			(&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		),
	},
}

// GetNotifyList returns notify list with secret fields redacted so tokens
// never reach the client DOM. Raw secrets are still held in the DB and used
// internally by SendTaskNotification; only the list response is masked.
func GetNotifyList() ([]map[string]interface{}, error) {
	list, err := mapper.GetNotifyList(false)
	if err != nil {
		return nil, fmt.Errorf("get notify list: %w", err)
	}
	for _, notify := range list {
		method := util.ToInt(notify["method"])
		notify["params"] = redactNotifyParams(method, fmt.Sprintf("%v", notify["params"]))
	}
	return list, nil
}

// notifySecretKeys are param keys whose values are redacted in list responses.
// body is included because request-body templates commonly embed tokens; the
// stored value is restored for editing via resolveNotifyParams.
var notifySecretKeys = map[int][]string{
	0: {"url", "headers", "body"},   // custom webhook: URL may embed token, headers/body carry auth
	1: {"sendKey"},                  // Server酱
	2: {"url", "webhook", "secret"}, // 钉钉 (access_token in URL query, 加签 secret)
	3: {"corpsecret", "corpSecret"}, // 企业微信应用密钥
	4: {"url", "webhook", "secret"}, // 飞书 (token in URL path, 签名校验 secret)
	5: {"server", "deviceKey"},      // Bark: server 是投递目标（可能内嵌凭据），deviceKey 是设备密钥
}

// allNotifySecretKeys is the union over every method. It is used when a stored
// row carries an unknown method: redaction must fail closed and mask every
// known secret field instead of echoing the row, because a malformed method
// value must not turn the list endpoint into a credential dump.
var allNotifySecretKeys = []string{
	"url", "webhook", "headers", "body", "sendKey", "secret",
	"corpsecret", "corpSecret", "deviceKey", "server",
}

const notifyRedactionMarker = "****"

// notifyBodyRedacted replaces a request-body template in list responses. It
// contains the standard marker so resolveNotifyParams restores the stored
// template on edit.
const notifyBodyRedacted = "******"

func maskSecretValue(value string) string {
	if len(value) <= 8 {
		return notifyRedactionMarker
	}
	return notifyRedactionMarker + value[len(value)-4:]
}

// maskNotifyURL redacts credential material in a webhook URL while keeping the
// host visible so the card summary stays identifiable. DingTalk embeds the
// token in a query param (access_token); Lark embeds it in the path.
//
// Every query value is masked rather than only those whose name contains
// token/key/secret. That name list could never be complete — real webhook URLs
// use k, sig, sign, auth, pwd, u, t — and the cost of the two directions is not
// symmetric: over-masking makes a summary line less descriptive, under-masking
// publishes a live credential to the page DOM. A query string on a webhook URL
// is routing and authentication data, not something the user reads here.
func maskNotifyURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return maskSecretValue(rawURL)
	}
	if u.User != nil {
		u.User = url.User(notifyRedactionMarker)
	}
	if u.RawQuery != "" {
		u.RawQuery = maskNotifyQuery(u.RawQuery)
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) > 0 && segs[len(segs)-1] != "" {
		last := segs[len(segs)-1]
		// Lark-style webhooks put the token in the final path segment. The old
		// 16-char threshold let short tokens through; 12 still keeps short
		// human-readable paths visible while masking credential-sized segments.
		if len(last) >= 12 {
			segs[len(segs)-1] = maskSecretValue(last)
			u.Path = "/" + strings.Join(segs, "/")
		}
	}
	return u.String()
}

// maskNotifyQuery masks every value in a raw query string, preserving parameter
// names and their order so the result still reads like the original URL.
//
// It works on the raw string instead of url.Values because ParseQuery drops
// malformed pairs and Encode reorders and re-escapes the rest: a query that
// failed to parse used to be written back verbatim, leaking the very token this
// function exists to hide. Anything unparseable is replaced wholesale here.
func maskNotifyQuery(rawQuery string) string {
	if _, err := url.ParseQuery(rawQuery); err != nil {
		return notifyRedactionMarker
	}
	pairs := strings.Split(rawQuery, "&")
	for i, pair := range pairs {
		if pair == "" {
			continue
		}
		name, value, hasValue := strings.Cut(pair, "=")
		if !hasValue {
			// A bare flag such as "?debug" carries no value to mask.
			continue
		}
		if value == "" {
			continue
		}
		decoded, err := url.QueryUnescape(value)
		if err != nil {
			decoded = value
		}
		// QueryEscape would render the marker as %2A%2A%2A%2A, which makes the
		// summary line unreadable. "*" needs no escaping in a query value, so it
		// is restored after escaping the surviving suffix.
		escaped := strings.ReplaceAll(
			url.QueryEscape(maskSecretValue(decoded)), "%2A", "*",
		)
		pairs[i] = name + "=" + escaped
	}
	return strings.Join(pairs, "&")
}

// redactNotifyParams returns paramsStr with secret fields masked for display.
func redactNotifyParams(method int, paramsStr string) string {
	params, err := parseNotifyParams(paramsStr)
	if err != nil {
		// Fail closed. A hand-written, truncated or legacy row is exactly the
		// case where the raw string may still contain a token; echoing it back
		// verbatim would defeat the entire redaction boundary (the same failure
		// mode maskNotifyQuery already guards against).
		return "{}"
	}
	keys := notifySecretKeys[method]
	if keys == nil {
		// Unknown method: mask the union rather than nothing.
		keys = allNotifySecretKeys
	}
	// Redaction is decided by key, never by the value's Go type. headers is
	// stored as a JSON object, so the old `s, _ := v.(string); if s == ""
	// { continue }` guard skipped it entirely and returned Authorization in
	// cleartext — the case "headers" branch below was unreachable.
	for _, key := range keys {
		v, ok := params[key]
		if !ok || v == nil {
			continue
		}
		if key == "headers" {
			// Any non-empty headers value is replaced with the standard marker
			// so resolveNotifyParams restores the stored object on edit. An
			// already-empty value carries no secret and is left alone.
			if isEmptyNotifyValue(v) {
				continue
			}
			params[key] = notifyRedactionMarker
			continue
		}
		s, isStr := v.(string)
		if !isStr {
			// A sensitive key holding a non-string (malformed or hand-written
			// config) still must not be echoed back verbatim.
			params[key] = notifyRedactionMarker
			continue
		}
		if s == "" {
			continue
		}
		switch key {
		case "body":
			// Unlike sendKey-style masks, no suffix is kept: a body template
			// may embed a token at any position.
			params[key] = notifyBodyRedacted
		case "url", "webhook", "server":
			// server is Bark's target URL; masking it like url keeps userinfo
			// and query credentials out of the list response while the host
			// stays visible.
			params[key] = maskNotifyURL(s)
		default:
			params[key] = maskSecretValue(s)
		}
	}
	out, err := json.Marshal(params)
	if err != nil {
		return "{}"
	}
	return string(out)
}

// isEmptyNotifyValue reports whether a params value carries nothing worth
// redacting: an absent/empty string, or an empty headers object.
func isEmptyNotifyValue(v interface{}) bool {
	switch value := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(value) == ""
	case map[string]interface{}:
		return len(value) == 0
	default:
		return false
	}
}

// isMaskedSecretValue reports whether value looks like a redacted secret, so
// the caller should preserve the existing stored value instead of overwriting.
func isMaskedSecretValue(value string) bool {
	if strings.Contains(value, notifyRedactionMarker) {
		return true
	}
	decoded, err := url.QueryUnescape(value)
	return err == nil && strings.Contains(decoded, notifyRedactionMarker)
}

// rejectRedactedNotifyParams refuses params that still contain a redaction
// placeholder. A placeholder is a display value only: persisting one overwrites
// the stored credential with "****", and sending one ships the placeholder as
// if it were the secret. Both are silent failures users can neither see nor
// repair from the UI, so the write/test path must fail closed instead.
func rejectRedactedNotifyParams(params map[string]interface{}) error {
	if key := firstRedactedNotifyKey(params); key != "" {
		return publicError(fmt.Sprintf("通知参数 %s 仍是脱敏占位符，请重新填写后再保存或测试", key))
	}
	return nil
}

func firstRedactedNotifyKey(params map[string]interface{}) string {
	for _, key := range allNotifySecretKeys {
		v, ok := params[key]
		if !ok || v == nil {
			continue
		}
		switch value := v.(type) {
		case string:
			if isMaskedSecretValue(value) {
				return key
			}
		case map[string]interface{}:
			for nestedKey, item := range value {
				s, isStr := item.(string)
				if isStr && isMaskedSecretValue(s) {
					return key + "." + nestedKey
				}
			}
		}
	}
	return ""
}

// normalizeNotifyWebhookBody turns a JSON object/array body into the template
// string sendWebhook expects. A REST client naturally submits
// {"body":{"text":"{title}"}}, but sendWebhook replaces placeholders in a
// string template and json.Unmarshal's it; a raw object was stored happily and
// then every send failed with "Body 格式无效".
func normalizeNotifyWebhookBody(params map[string]interface{}) error {
	raw, ok := params["body"]
	if !ok || raw == nil {
		return nil
	}
	switch value := raw.(type) {
	case string:
		return validateWebhookBodyTemplate(value)
	case map[string]interface{}, []interface{}:
		encoded, err := json.Marshal(value)
		if err != nil {
			return publicError("通知自定义 Body 格式无效")
		}
		params["body"] = string(encoded)
		return nil
	default:
		return publicError("通知自定义 Body 格式无效")
	}
}

// validateWebhookBodyTemplate checks a custom body template at save time
// instead of at delivery time. sendWebhook substitutes the placeholders with
// JSON-escaped strings and then unmarshals the result, so the template must
// stay valid JSON after substitution.
func validateWebhookBodyTemplate(template string) error {
	if strings.TrimSpace(template) == "" {
		return nil
	}
	rendered := strings.NewReplacer("{title}", "title", "{content}", "content").Replace(template)
	var body interface{}
	if err := json.Unmarshal([]byte(rendered), &body); err != nil {
		return publicError("通知自定义 Body 格式无效")
	}
	return nil
}

// normalizeNotifyEnable converts the enable shapes the API accepts (JSON bool,
// number, numeric/boolean string) into 0/1. util.ToInt silently maps `true` to
// 0, which used to create/update a config as disabled.
func normalizeNotifyEnable(value interface{}) (int, error) {
	switch v := value.(type) {
	case bool:
		if v {
			return 1, nil
		}
		return 0, nil
	case int:
		if v == 0 || v == 1 {
			return v, nil
		}
	case int64:
		if v == 0 || v == 1 {
			return int(v), nil
		}
	case float64:
		if v == 0 || v == 1 {
			return int(v), nil
		}
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "0", "false":
			return 0, nil
		case "1", "true":
			return 1, nil
		}
	}
	return 0, publicError(msg.T(msg.LostPart))
}

// loadStoredNotify reads the stored config that resolveNotifyParams merges
// secrets from. It is a variable so tests can simulate a failing read.
var loadStoredNotify = mapper.GetNotifyByID

// Write seams, so the validation/redaction paths can be exercised without a
// database.
var (
	addNotifyRecord  = mapper.AddNotify
	editNotifyRecord = mapper.EditNotify
)

// resolveNotifyParams merges incoming params with stored secrets for fields
// that were redacted (masked) or left empty. For new configs (no id) the
// incoming params are returned unchanged. The returned map contains real
// secret values, suitable for sending a test or persisting. An explicitly
// empty headers object ({}) clears the stored headers instead of restoring
// them.
func resolveNotifyParams(notify map[string]interface{}) (map[string]interface{}, error) {
	method := util.ToInt(notify["method"])
	incoming, err := notifyParamsValue(notify["params"])
	if err != nil {
		return nil, err
	}
	notifyID := util.ToInt64(notify["id"])
	if notifyID <= 0 {
		return incoming, nil
	}
	existing, err := loadStoredNotify(notifyID)
	if err != nil {
		// A read failure must not fall through to the incoming params: they
		// still carry the redaction placeholders, so a transient DB error (or an
		// undecryptable row) would permanently overwrite the stored secrets with
		// "****" strings on save.
		return nil, fmt.Errorf("load stored notify %d: %w", notifyID, err)
	}
	if existing == nil {
		// Not found: nothing to merge. EditNotify's update then reports the
		// missing row.
		return incoming, nil
	}
	// A different method means a different kind of credential: a stored
	// DingTalk secret is not a Bark deviceKey. Switching method (which also
	// switches the target field compared below) must not inherit anything.
	if storedMethod, ok := existing["method"]; ok && storedMethod != nil && util.ToInt(storedMethod) != method {
		return incoming, nil
	}
	existingParams, _ := parseNotifyParams(fmt.Sprintf("%v", existing["params"]))
	// Stored secrets are only merged back when the delivery target is unchanged.
	// Otherwise a caller could submit {"id":<existing>,"params":{"url":"https://
	// attacker/hook"}} and have the saved Authorization header merged in, so a
	// test send (or a PUT) ships the credential to an address of their choosing.
	compareIncoming, compareExisting := incoming, existingParams
	if method == 5 {
		// Bark's empty server means the public default server. Normalising
		// both sides (for the comparison only) makes "stored empty, incoming
		// https://api.day.app" an unchanged target, and any other server a
		// change — instead of an empty stored server never counting as one.
		compareIncoming = withDefaultBarkServer(incoming)
		compareExisting = withDefaultBarkServer(existingParams)
	}
	if notifyTargetChanged(compareIncoming, compareExisting) {
		return incoming, nil
	}
	for _, key := range notifySecretKeys[method] {
		v, ok := incoming[key]
		if !ok || v == nil {
			if ev, ok2 := existingParams[key]; ok2 && ev != nil {
				incoming[key] = ev
			}
			continue
		}
		s, isStr := v.(string)
		if isStr {
			// secret (DingTalk 加签 / Lark 签名校验) is optional: an empty value
			// is the user turning signing off, not an untouched field. Only the
			// masked placeholder restores the stored one.
			if key == "secret" && s == "" {
				delete(incoming, key)
				continue
			}
			if s == "" || isMaskedSecretValue(s) {
				if ev, ok2 := existingParams[key]; ok2 && ev != nil {
					incoming[key] = ev
				}
			}
			continue
		}
		// Non-string values (e.g. the parsed headers object) are real input
		// and replace the stored value. An explicitly-empty headers object
		// means the user cleared headers, so drop the stored value.
		if key == "headers" {
			if hMap, ok := v.(map[string]interface{}); ok && len(hMap) == 0 {
				delete(incoming, key)
			}
		}
	}
	return incoming, nil
}

// notifyTargetChanged reports whether the incoming params point at a different
// delivery target than the stored ones. A masked or absent URL is not a change:
// that is the UI round-tripping the redacted value it was given.
//
// Bark keeps its target in server rather than url; it is compared the same way
// so a changed server cannot inherit the stored deviceKey. A stored row without
// any target (e.g. a Bark config that relies on the default server) is treated
// as a change when the caller supplies one: the previous value is unknown, and
// merging stored secrets into an unknown target would ship them to a
// destination of the caller's choosing. WeCom has no URL target at all, so its
// corp id is compared instead.
func notifyTargetChanged(incoming, existing map[string]interface{}) bool {
	incomingURL := paramString(incoming, "url", "webhook", "server")
	if incomingURL != "" && !isMaskedSecretValue(incomingURL) {
		existingURL := paramString(existing, "url", "webhook", "server")
		if existingURL == "" {
			return true
		}
		if incomingURL != existingURL {
			return true
		}
	}
	incomingCorp := paramString(incoming, "corpid", "corpId")
	existingCorp := paramString(existing, "corpid", "corpId")
	return incomingCorp != "" && existingCorp != "" && incomingCorp != existingCorp
}

func validateNotifyParams(method int, params map[string]interface{}) error {
	switch method {
	case 0:
		if err := validateNotifyWebhookURL(paramString(params, "url", "webhook")); err != nil {
			return err
		}
		if err := validateWebhookMethod(paramString(params, "method", "httpMethod")); err != nil {
			return err
		}
		return validateWebhookPayload(params)
	case 1:
		if paramString(params, "sendKey") == "" {
			return errors.New(msg.T(msg.NotifyParamInvalid))
		}
		return nil
	case 2, 4:
		return validateNotifyHTTPSURL(paramString(params, "url", "webhook"))
	case 3:
		if paramString(params, "corpid", "corpId") == "" ||
			paramString(params, "corpsecret", "corpSecret") == "" ||
			paramString(params, "agentid", "agentId") == "" {
			return errors.New(msg.T(msg.NotifyParamInvalid))
		}
		return nil
	case 5:
		if paramString(params, "deviceKey") == "" {
			return errors.New(msg.T(msg.NotifyParamInvalid))
		}
		// server is optional; an empty value means the public Bark service.
		if server := paramString(params, "server"); server != "" {
			return validateNotifyWebhookURL(server)
		}
		return nil
	default:
		return errors.New(msg.T(msg.NotifyMethodInvalid))
	}
}

func validateWebhookMethod(method string) error {
	if method == "" {
		return nil
	}
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodGet, http.MethodPost, http.MethodPut:
		return nil
	default:
		return errors.New(msg.T(msg.NotifyParamInvalid))
	}
}

// validateWebhookPayload checks the two custom-webhook fields sendWebhook can
// only use in one shape: headers must be an object (a bare string is silently
// dropped at send time, which looks like an authentication failure) and body
// must be a template that stays valid JSON after placeholder substitution.
func validateWebhookPayload(params map[string]interface{}) error {
	if raw, ok := params["headers"]; ok && raw != nil {
		if _, isMap := raw.(map[string]interface{}); !isMap {
			return errors.New("通知自定义 Header 必须是 JSON 对象")
		}
	}
	raw, ok := params["body"]
	if !ok || raw == nil {
		return nil
	}
	template, isStr := raw.(string)
	if !isStr {
		return publicError("通知自定义 Body 格式无效")
	}
	return validateWebhookBodyTemplate(template)
}

func validateNotifyWebhookURL(rawURL string) error {
	if err := util.ValidateHTTPURL(rawURL, msg.T(msg.NotifyURLInvalid)); err != nil {
		return err
	}
	if u, err := url.Parse(rawURL); err == nil && strings.ToLower(u.Scheme) == "http" {
		log.Printf("Warning: webhook URL uses plain HTTP, notification content will be transmitted unencrypted: %s", u.Host)
	}
	return nil
}

func validateNotifyHTTPSURL(rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New(msg.T(msg.NotifyURLInvalid))
	}
	if strings.ToLower(u.Scheme) != "https" {
		return errors.New(msg.T(msg.NotifyURLInvalid))
	}
	return nil
}

// notifyParamsValue accepts params either as an already-parsed JSON object or
// as a serialized JSON string, so callers sending either shape get consistent
// validation instead of a raw 500.
func notifyParamsValue(value interface{}) (map[string]interface{}, error) {
	switch v := value.(type) {
	case map[string]interface{}:
		return v, nil
	case string:
		return parseNotifyParams(v)
	default:
		return parseNotifyParams(fmt.Sprintf("%v", value))
	}
}

// AddNewNotify adds a new notify config and returns its new row id. Callers
// (and API clients) need the id to address the row they just created; deriving
// it from "the last entry of GET /notify" is wrong as soon as two configs are
// created concurrently, or when the list is not ordered by insertion.
func AddNewNotify(notify map[string]interface{}) (int64, error) {
	params, err := notifyParamsValue(notify["params"])
	if err != nil {
		return 0, fmt.Errorf("parse notify params: %w", err)
	}
	method := util.ToInt(notify["method"])
	notify["method"] = method
	enable, err := normalizeNotifyEnable(notify["enable"])
	if err != nil {
		return 0, err
	}
	notify["enable"] = enable
	if err := rejectRedactedNotifyParams(params); err != nil {
		return 0, err
	}
	if err := normalizeNotifyWebhookBody(params); err != nil {
		return 0, err
	}
	if err := validateNotifyParams(method, params); err != nil {
		return 0, publicError(err.Error())
	}
	out, err := json.Marshal(params)
	if err != nil {
		return 0, fmt.Errorf("marshal notify params: %w", err)
	}
	notify["params"] = string(out)
	id, err := addNotifyRecord(notify)
	if err != nil {
		return 0, fmt.Errorf("add notify: %w", err)
	}
	return id, nil
}

// EditNotify updates a notify config. Secret fields that were redacted in
// the list view (or left empty) are preserved from the stored config so the
// user can edit other fields without re-entering credentials.
func EditNotify(notify map[string]interface{}) error {
	// enable is required and normalised exactly as AddNewNotify does. An edit
	// without it used to write NULL, which the enable=1 filter treats as off,
	// so the config was silently disabled.
	enableRaw, ok := notify["enable"]
	if !ok || enableRaw == nil {
		return publicError(msg.T(msg.LostPart))
	}
	enable, err := normalizeNotifyEnable(enableRaw)
	if err != nil {
		return err
	}
	notify["enable"] = enable
	resolved, err := resolveNotifyParams(notify)
	if err != nil {
		return fmt.Errorf("resolve notify params: %w", err)
	}
	notify["method"] = util.ToInt(notify["method"])
	if err := rejectRedactedNotifyParams(resolved); err != nil {
		return err
	}
	if err := normalizeNotifyWebhookBody(resolved); err != nil {
		return err
	}
	if err := validateNotifyParams(util.ToInt(notify["method"]), resolved); err != nil {
		return publicError(err.Error())
	}
	out, err := json.Marshal(resolved)
	if err != nil {
		return fmt.Errorf("marshal notify params: %w", err)
	}
	notify["params"] = string(out)
	if err := editNotifyRecord(notify); err != nil {
		// A missing row is the caller's mistake (stale page, deleted elsewhere),
		// not an internal failure: report it as such.
		if errors.Is(err, mapper.ErrNotifyNotFound) {
			return publicError(msg.T(msg.NotifyNotFound))
		}
		return fmt.Errorf("edit notify: %w", err)
	}
	return nil
}

// UpdateNotifyStatus updates notify enable status
func UpdateNotifyStatus(notifyID int64, enable int) error {
	err := mapper.UpdateNotifyStatus(notifyID, enable)
	return publicErrorIf(err, msg.T(msg.NotifyNotFound))
}

// DeleteNotify deletes a notify config
func DeleteNotify(notifyID int64) error {
	err := mapper.DeleteNotify(notifyID)
	return publicErrorIf(err, msg.T(msg.NotifyNotFound))
}

// TestNotify sends a test notification. Secrets that were redacted in the
// list view are restored from the stored config (when an id is given) so the
// test sends with real credentials.
func TestNotify(notify map[string]interface{}) error {
	resolved, err := resolveNotifyParams(notify)
	if err != nil {
		return fmt.Errorf("resolve notify params: %w", err)
	}
	// A test send is the other path that reaches the network with the caller's
	// params. A placeholder must never be sent: it is not a credential, and a
	// changed target must not inherit the stored one.
	if err := rejectRedactedNotifyParams(resolved); err != nil {
		return err
	}
	if err := normalizeNotifyWebhookBody(resolved); err != nil {
		return err
	}
	if err := validateNotifyParams(util.ToInt(notify["method"]), resolved); err != nil {
		return publicError(err.Error())
	}
	out, err := json.Marshal(resolved)
	if err != nil {
		return fmt.Errorf("marshal notify params: %w", err)
	}
	notify["params"] = string(out)
	testMsg := msg.T(msg.NotifyTestMsg)
	_, sendErr := sendNotify(notify, "OpenSync Test", testMsg, false)
	if sendErr != nil {
		// A PublicError is an actionable message for the user (e.g. invalid URL,
		// bad config). Other errors are opaque failures; wrap them with the
		// generic "send failed" message so the user is not shown raw internals.
		var pubErr model.PublicError
		if errors.As(sendErr, &pubErr) {
			return sendErr
		}
		log.Printf("notify test failed: %v", sendErr)
		return publicError(msg.T(msg.NotifySendFail))
	}
	return nil
}

// SendTaskNotification sends notification after task completion.
//
// This is the synchronous delivery path. The task completion path does not call
// it directly — it hands the work to the background dispatcher via
// QueueTaskNotification, so a slow webhook cannot hold up the finishing task.
func SendTaskNotification(taskID int64, status int, taskNum map[string]interface{}, duration int, createTime float64) {
	notifyList, err := mapper.GetNotifyList(true)
	if err != nil {
		log.Printf("Failed to load notify configs for task %d: %v", taskID, err)
		return
	}
	if len(notifyList) == 0 {
		return
	}

	job, err := mapper.GetJobByTaskID(taskID)
	if err != nil {
		log.Printf("Failed to load job for task %d notification: %v", taskID, err)
		return
	}

	statusNames := map[int]string{
		0: "Waiting", 1: "Running", 2: "Success", 3: "Partial Success",
		4: "Stopped", 5: "Timeout", 6: "System Failed", 7: "Failed", 8: "No sync needed",
	}
	statusName := statusNames[status]
	if status < 0 || status > 8 {
		statusName = "Unknown"
	}

	needNotSync := status == taskStatusNoSync.Int()
	if status == taskStatusSuccess.Int() {
		allNum := util.ToInt(taskNum["allNum"])
		if allNum == 0 {
			needNotSync = true
		}
	}
	if needNotSync {
		statusName = statusNames[8]
	}

	remark := ""
	if r, ok := job["remark"]; ok && r != nil {
		remark = fmt.Sprintf("%v", r)
	}
	if remark != "" {
		statusName = remark + ": " + statusName
	}

	title := fmt.Sprintf("OpenSync - %s", statusName)

	successNum := util.ToInt(taskNum["successNum"])
	failNum := util.ToInt(taskNum["failNum"])
	allNum := util.ToInt(taskNum["allNum"])
	srcPath := strings.Join(parsePathList(job["srcPath"]), "、")
	dstPath := strings.Join(parsePathList(job["dstPath"]), "、")

	content := fmt.Sprintf("Source: %s | Target: %s | Total: %d | Success: %d | Fail: %d",
		srcPath, dstPath, allNum, successNum, failNum)

	if createTime > 0 && duration > 0 {
		hours, minutes, seconds := util.ConvertSeconds(duration)
		durationText := fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
		sumSize := util.ToInt64(taskNum["sumSize"])
		content += fmt.Sprintf(" | Duration: %s | Size: %s", durationText, util.ConvertBytes(sumSize))
	}

	if (status > 3 && status < 6) || status == 7 {
		content += fmt.Sprintf(" | Status: %s", statusName)
	}

	sendNotifyFanOut(notifyList, title, content, needNotSync)
}

// deliverTaskNotification is the dispatcher worker's entry point: it runs the
// same delivery as SendTaskNotification for one queued job.
func deliverTaskNotification(job notifyJob) {
	SendTaskNotification(job.taskID, job.status, job.taskNum, job.duration, job.createTime)
}

// sendNotifyFanOut delivers one notification per config concurrently.
//
// Sending serially meant the slowest destination decided how long a finished
// task stayed in its wrap-up phase: notifyHTTPClient allows 30s per request, so
// five unreachable webhooks held the task for two and a half minutes. The sends
// are independent HTTP posts to different hosts and nothing depends on their
// order, so the total wait is now the slowest single destination rather than
// their sum. Each send keeps its own recover: one bad config must not take down
// the goroutine and with it the remaining notifications.
func sendNotifyFanOut(notifyList []map[string]interface{}, title, content string, needNotSync bool) {
	var wg sync.WaitGroup
	slots := make(chan struct{}, notifySendConcurrency)
	for _, notify := range notifyList {
		wg.Add(1)
		go func(notify map[string]interface{}) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			sent, sendErr := trySendNotify(notify, title, content, needNotSync)
			recordNotifySendResult(notify, sent, sendErr)
		}(notify)
	}
	// SendTaskNotification is called on the task's completion path, which reports
	// the task done only after the notifications it promised have been attempted.
	wg.Wait()
}

// trySendNotify turns the send path into a value. The outcome has to survive as
// data, not just as a log line: a config whose token expired kept reporting
// nothing at all through the API, so the UI showed a healthy channel while every
// message was being rejected. The defer/recover is kept as a safety net for any
// unexpected panic that might still escape.
func trySendNotify(notify map[string]interface{}, title, content string, needNotSync bool) (sent bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			sent = false
			err = notifySendFailure(r)
		}
	}()
	return sendNotify(notify, title, content, needNotSync)
}

func notifySendFailure(recovered interface{}) error {
	if recoveredErr, ok := recovered.(error); ok {
		return recoveredErr
	}
	return fmt.Errorf("%v", recovered)
}

// recordNotifySendResult stores the outcome on the config row so GET /notify can
// report it. A send skipped by notSendNull is neither a success nor a failure,
// so the previously recorded outcome is left untouched.
func recordNotifySendResult(notify map[string]interface{}, sent bool, sendErr error) {
	if !sent && sendErr == nil {
		return
	}
	notifyID := util.ToInt64(notify["id"])
	if notifyID <= 0 {
		return
	}
	status := notifySendStatusSuccess
	reason := ""
	if sendErr != nil {
		status = notifySendStatusFailed
		reason = sanitizeNotifyError(sendErr.Error())
		log.Printf("%s", msg.NotifyError(reason))
	}
	if notifyBaseContext().Err() != nil {
		// Shutdown gave up on this send and is closing the database; the
		// outcome is the cancellation itself, not worth a write after CloseDB.
		return
	}
	if err := notifyDeps.RecordSendOutcome(notifyID, status, time.Now().Unix(), reason); err != nil {
		log.Printf("Failed to record delivery result for notify %d: %v", notifyID, err)
	}
}

// sanitizeNotifyError prepares a delivery failure for storage. The text is
// echoed back by GET /notify, and net/http errors embed the full request URL —
// which is exactly where ServerChan, DingTalk and Lark keep their tokens. The
// masking sits at this single boundary rather than at each of the panic sites
// that produce the text.
func sanitizeNotifyError(text string) string {
	cleaned := strings.TrimSpace(notifyErrorURLPattern.ReplaceAllStringFunc(text, maskNotifyErrorURL))
	if cleaned == "" {
		return msg.T(msg.NotifySendFail)
	}
	// Truncated by rune, so a split multi-byte character cannot produce invalid
	// UTF-8 in the JSON response.
	if runes := []rune(cleaned); len(runes) > maxNotifyErrorLength {
		cleaned = string(runes[:maxNotifyErrorLength]) + "…"
	}
	return cleaned
}

// maskNotifyErrorURL reduces a URL to scheme://host, the same shape
// notifyRequestTarget already uses for logs. Everything dropped — path, query
// and userinfo — is where the providers put their credentials, and the host is
// the only part that helps the user identify which destination failed.
func maskNotifyErrorURL(rawURL string) string {
	trimmed := strings.TrimRight(rawURL, `.,;:)]}"'`)
	suffix := rawURL[len(trimmed):]
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return notifyRedactionMarker + suffix
	}
	return u.Scheme + "://" + u.Host + suffix
}

// sendNotify sends a notification via the configured method. It reports whether
// a message was actually sent, so a config skipped by notSendNull is not
// recorded as a successful delivery.
func sendNotify(notify map[string]interface{}, title, content string, needNotSync bool) (bool, error) {
	params, err := notifyParamsValue(notify["params"])
	if err != nil {
		return false, fmt.Errorf("parse notify params: %w", err)
	}
	// Refuse to send a redaction placeholder that a pre-fix row may still
	// carry, then normalise what a REST add could have stored in another shape.
	if err := rejectRedactedNotifyParams(params); err != nil {
		return false, err
	}
	if err := normalizeNotifyWebhookBody(params); err != nil {
		return false, err
	}

	method := util.ToInt(notify["method"])
	if err := validateNotifyParams(method, params); err != nil {
		return false, publicError(err.Error())
	}

	// Check notSendNull flag
	if needNotSync {
		if v, ok := params["notSendNull"]; ok {
			if util.ToBool(v) {
				return false, nil
			}
		}
	}

	var sendErr error
	switch method {
	case 0: // Custom webhook
		sendErr = sendWebhook(notifyHTTPClient, params, title, content)
	case 1: // ServerChan
		sendErr = sendServerChan(notifyHTTPClient, params, title, content)
	case 2: // DingTalk
		sendErr = sendDingTalk(notifyHTTPClient, params, title, content)
	case 3: // WeCom (Enterprise WeChat)
		sendErr = sendWeCom(notifyHTTPClient, params, title, content)
	case 4: // Lark (Feishu)
		sendErr = sendLark(notifyHTTPClient, params, title, content)
	case 5: // Bark
		sendErr = sendBark(notifyHTTPClient, params, title, content)
	default:
		// validateNotifyParams rejects unknown methods, so this is unreachable
		// unless a new method is added without a send branch. Reporting it as a
		// failure beats recording a delivery that never happened.
		return false, publicError(msg.T(msg.NotifyMethodInvalid))
	}
	if sendErr != nil {
		return false, sendErr
	}
	return true, nil
}

func parseNotifyParams(paramsStr string) (map[string]interface{}, error) {
	paramsStr = strings.TrimSpace(paramsStr)
	if paramsStr == "" || paramsStr == "<nil>" {
		return map[string]interface{}{}, nil
	}
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(paramsStr), &params); err != nil {
		return nil, err
	}
	if params == nil {
		params = map[string]interface{}{}
	}
	return params, nil
}

func buildNotifyRequest(method, urlStr string, body io.Reader, contentType string) (*http.Request, error) {
	method = strings.TrimSpace(method)
	urlStr = strings.TrimSpace(urlStr)
	if urlStr == "" {
		return nil, fmt.Errorf("url is required")
	}
	if err := validateWebhookDestination(urlStr); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(notifyBaseContext(), method, urlStr, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req, nil
}

func sendNotifyRequest(client *http.Client, req *http.Request) error {
	_, err := sendNotifyRequestBytes(client, req)
	return err
}

// sendNotifyRequestBytes sends the request, validates the HTTP status, and
// returns the response body so callers can inspect provider-specific error
// codes. DingTalk/Lark/ServerChan/WeCom return HTTP 200 with a non-zero
// errcode/code/errno in the body on failure, which a status-only check misses.
func sendNotifyRequestBytes(client *http.Client, req *http.Request) ([]byte, error) {
	resp, err := doNotifyRequest(client, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	bodyBytes, err := readAllWithLimit(resp.Body, maxNotifyResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("read notify response: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		if summary := notifyResponseSummary(bodyBytes); summary != "" {
			log.Printf("notify request failed: status=%s response=%q", resp.Status, summary)
		}
		return nil, fmt.Errorf("notify request failed: %s", resp.Status)
	}
	return bodyBytes, nil
}

func sendJSONNotify(client *http.Client, urlStr string, body interface{}, errorFields ...string) ([]byte, error) {
	jsonData, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal notify body: %w", err)
	}
	req, err := buildNotifyRequest(http.MethodPost, urlStr, bytes.NewReader(jsonData), "application/json")
	if err != nil {
		return nil, fmt.Errorf("build notify request: %w", err)
	}
	respBody, err := sendNotifyRequestBytes(client, req)
	if err != nil {
		return nil, err
	}
	if err := notifyProviderError(respBody, errorFields...); err != nil {
		return nil, err
	}
	return respBody, nil
}

// notifyProviderError returns a non-nil error when the provider's JSON response
// body indicates failure via one of the given fields being non-zero. Returns nil
// for non-JSON bodies (e.g. custom webhooks) or when no recognized field is
// present, so callers don't false-positive on arbitrary response shapes.
func notifyProviderError(body []byte, fields ...string) error {
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		return nil
	}
	// Every named field is checked, not just the first one present: Lark is
	// passed ("code", "StatusCode") and can answer code=0 alongside a non-zero
	// StatusCode. Returning on the first field found would report that failure
	// as a success.
	for _, field := range fields {
		v, ok := m[field]
		if !ok {
			continue
		}
		if code := util.ToInt(v); code != 0 {
			if message := notifyProviderMessage(m); message != "" {
				return fmt.Errorf("notify %s=%d: %s", field, code, message)
			}
			return fmt.Errorf("notify %s=%d", field, code)
		}
	}
	return nil
}

// maxNotifyResponseExcerpt caps how much of a non-JSON provider response is
// kept for logs and error text.
const maxNotifyResponseExcerpt = 200

// notifyProviderMessageFields are the keys providers use for a human-readable
// failure reason (DingTalk/WeCom errmsg, Lark msg, ServerChan message/info).
var notifyProviderMessageFields = []string{"errmsg", "msg", "message", "info", "error"}

// notifyProviderMessage returns the provider's failure reason from a parsed
// JSON response, or "" when none is present.
//
// Only the dedicated message field is used, never the whole body: providers
// echo request data in their responses (webhook receivers in particular often
// reflect headers or the payload), and the result ends up in logs and in
// lastSendError, which GET /notify returns to the browser.
func notifyProviderMessage(m map[string]interface{}) string {
	for _, field := range notifyProviderMessageFields {
		v, ok := m[field]
		if !ok || v == nil {
			continue
		}
		s, isStr := v.(string)
		if !isStr {
			continue
		}
		if s = strings.TrimSpace(s); s != "" {
			return truncateNotifyExcerpt(sanitizeNotifyExcerpt(s))
		}
	}
	return ""
}

// notifyResponseSummary describes a provider response for logs without
// reproducing it. A JSON body is reduced to its code and message fields; any
// other body is cut to a short, URL-masked excerpt.
func notifyResponseSummary(body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err == nil {
		parts := []string{}
		for _, field := range []string{"errcode", "code", "errno", "StatusCode"} {
			if v, ok := m[field]; ok && v != nil {
				parts = append(parts, fmt.Sprintf("%s=%v", field, v))
			}
		}
		if message := notifyProviderMessage(m); message != "" {
			parts = append(parts, message)
		}
		return strings.Join(parts, " ")
	}
	return truncateNotifyExcerpt(sanitizeNotifyExcerpt(text))
}

// sanitizeNotifyExcerpt masks URLs (and the credentials in their paths and
// queries) in provider-supplied text.
func sanitizeNotifyExcerpt(text string) string {
	return notifyErrorURLPattern.ReplaceAllStringFunc(text, maskNotifyErrorURL)
}

func truncateNotifyExcerpt(text string) string {
	if len(text) <= maxNotifyResponseExcerpt {
		return text
	}
	// Cut on a rune boundary so the excerpt stays valid UTF-8.
	cut := maxNotifyResponseExcerpt
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

func doNotifyRequest(client *http.Client, req *http.Request) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		// On a redirect error client.Do can return a non-nil resp together
		// with err; close the body or the underlying connection leaks.
		if resp != nil {
			_ = resp.Body.Close()
		}
		log.Printf("notify request failed: target=%s error=%s", notifyRequestTarget(req), notifyNetworkError(err))
		return nil, publicError(msg.T(msg.NotifySendFail))
	}
	return resp, nil
}

func notifyRequestTarget(req *http.Request) string {
	if req == nil || req.URL == nil {
		return "<unknown>"
	}
	return req.URL.Scheme + "://" + req.URL.Host
}

func notifyNetworkError(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Sprintf("%s: %v", urlErr.Op, urlErr.Err)
	}
	return fmt.Sprintf("%T", err)
}

func sendWebhook(client *http.Client, params map[string]interface{}, title, content string) error {
	urlStr := paramString(params, "url", "webhook")
	method := "POST"
	if m := paramString(params, "method", "httpMethod"); m != "" {
		method = strings.ToUpper(m)
	}
	contentType := paramString(params, "contentType")
	if contentType == "" {
		contentType = "application/json"
	}
	titleName := paramString(params, "titleName")
	if titleName == "" {
		titleName = "title"
	}
	contentName := paramString(params, "contentName")
	if contentName == "" {
		contentName = "content"
	}
	needContent := true
	if v, ok := params["needContent"]; ok {
		needContent = util.ToBool(v)
	}

	body := map[string]interface{}{
		titleName: title,
	}
	if needContent {
		body[contentName] = content
	}
	if customBody, ok := params["body"]; ok && customBody != nil {
		// One pass over the template. Replacing {title} and then {content}
		// separately substituted placeholders that came from the inserted
		// title itself — a job remark containing "{content}" was expanded a
		// second time. strings.Replacer never rescans replaced text. Values are
		// still JSON-escaped so they are safe inside the template's strings.
		bodyStr := strings.NewReplacer(
			"{title}", jsonStringContent(title),
			"{content}", jsonStringContent(content),
		).Replace(fmt.Sprintf("%v", customBody))
		body = nil
		if err := json.Unmarshal([]byte(bodyStr), &body); err != nil {
			return publicError("通知自定义 Body 格式无效")
		}
	}

	var req *http.Request
	var err error
	if method == "GET" {
		req, err = buildNotifyRequest(http.MethodGet, urlStr, nil, "")
		if err != nil {
			return fmt.Errorf("build webhook request: %w", err)
		}
		q := req.URL.Query()
		q.Set(titleName, title)
		if needContent {
			q.Set(contentName, content)
		}
		req.URL.RawQuery = q.Encode()
	} else {
		if contentType == "application/x-www-form-urlencoded" {
			formBody := make([]string, 0, len(body))
			for k, v := range body {
				formBody = append(formBody, fmt.Sprintf("%s=%s", url.QueryEscape(k), url.QueryEscape(fmt.Sprintf("%v", v))))
			}
			req, err = buildNotifyRequest(method, urlStr, strings.NewReader(strings.Join(formBody, "&")), contentType)
		} else {
			jsonData, marshalErr := json.Marshal(body)
			if marshalErr != nil {
				return fmt.Errorf("marshal webhook body: %w", marshalErr)
			}
			req, err = buildNotifyRequest(method, urlStr, bytes.NewReader(jsonData), contentType)
		}
		if err != nil {
			return fmt.Errorf("build webhook request: %w", err)
		}
	}

	if headers, ok := params["headers"]; ok && headers != nil {
		if hMap, ok := headers.(map[string]interface{}); ok {
			for k, v := range hMap {
				if _, blocked := forbiddenWebhookHeaders[strings.ToLower(k)]; blocked {
					continue
				}
				req.Header.Set(k, fmt.Sprintf("%v", v))
			}
		}
	}

	return sendNotifyRequest(client, req)
}

func jsonStringContent(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	encodedStr := string(encoded)
	if len(encodedStr) < 2 {
		return encodedStr
	}
	return encodedStr[1 : len(encodedStr)-1]
}

func sendServerChan(client *http.Client, params map[string]interface{}, title, content string) error {
	sendKey := paramString(params, "sendKey")
	version := "v1"
	if v, ok := params["version"]; ok {
		version = fmt.Sprintf("%v", v)
	}

	var urlStr string
	if version == "v3" {
		urlStr = fmt.Sprintf("https://sctapi.ftqq.com/%s.send", url.PathEscape(sendKey))
	} else {
		urlStr = fmt.Sprintf("https://sc.ftqq.com/%s.send", url.PathEscape(sendKey))
	}

	body := map[string]string{
		"title": title,
		"desp":  content,
	}
	_, err := sendJSONNotify(client, urlStr, body, "code", "errno")
	return err
}

// notifyNow is the clock used for signed webhooks; tests replace it to get a
// deterministic signature.
var notifyNow = time.Now

// dingTalkSignedURL appends the 加签 timestamp and sign query params. DingTalk
// signs "timestamp\nsecret" with the secret as the HMAC key, in milliseconds.
func dingTalkSignedURL(webhook, secret string, now time.Time) (string, error) {
	u, err := url.Parse(webhook)
	if err != nil {
		return "", fmt.Errorf("parse dingtalk webhook: %w", err)
	}
	timestamp := fmt.Sprintf("%d", now.UnixMilli())
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "\n" + secret))
	query := u.Query()
	query.Set("timestamp", timestamp)
	query.Set("sign", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	u.RawQuery = query.Encode()
	return u.String(), nil
}

// larkSign computes the 签名校验 value. Unlike DingTalk, Lark uses
// "timestamp\nsecret" as the HMAC key over an empty message, in seconds.
func larkSign(secret string, timestamp int64) string {
	mac := hmac.New(sha256.New, []byte(fmt.Sprintf("%d\n%s", timestamp, secret)))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func sendDingTalk(client *http.Client, params map[string]interface{}, title, content string) error {
	webhook := paramString(params, "url", "webhook")
	if secret := paramString(params, "secret"); secret != "" {
		signed, err := dingTalkSignedURL(webhook, secret, notifyNow())
		if err != nil {
			return err
		}
		webhook = signed
	}
	body := map[string]interface{}{
		"msgtype": "text",
		"text": map[string]string{
			"content": title + "\n" + content,
		},
	}
	_, err := sendJSONNotify(client, webhook, body, "errcode")
	return err
}

// wecomTokenExpiredCodes are the send-API errcodes meaning the cached access
// token is no longer valid (40014 invalid token, 41001 token missing, 42001
// token expired). WeCom can revoke a token before expires_in — e.g. when the
// app secret is reset or another client refreshes it — so the cache cannot rely
// on the expiry time alone.
var wecomTokenExpiredCodes = map[int]struct{}{40014: {}, 41001: {}, 42001: {}}

// getWeComAccessToken returns a cached token for the credentials or fetches a
// new one. The lock is held only to read and write the cache, never across the
// gettoken request: holding it there made every WeCom send — for any corp —
// queue behind one slow or unreachable token fetch for up to the 30s client
// timeout. Two concurrent misses may both fetch; each gets a valid token and
// the later one is kept, which is harmless.
func getWeComAccessToken(client *http.Client, corpID, corpSecret string) (string, error) {
	credentials := wecomCredentials{corpID, corpSecret}
	wecomTokenCache.Lock()
	entry, ok := wecomTokenCache.entries[credentials]
	wecomTokenCache.Unlock()
	if ok && entry.token != "" && time.Now().Before(entry.expires) {
		return entry.token, nil
	}

	token, expires, err := fetchWeComAccessToken(client, corpID, corpSecret)
	if err != nil {
		return "", err
	}
	wecomTokenCache.Lock()
	if wecomTokenCache.entries == nil {
		wecomTokenCache.entries = make(map[wecomCredentials]wecomTokenEntry)
	}
	wecomTokenCache.entries[credentials] = wecomTokenEntry{token: token, expires: expires}
	wecomTokenCache.Unlock()
	return token, nil
}

// invalidateWeComAccessToken drops the cached token for the credentials, but
// only if it is still the one that was rejected, so a fresh token stored by a
// concurrent send is not thrown away.
func invalidateWeComAccessToken(corpID, corpSecret, rejected string) {
	credentials := wecomCredentials{corpID, corpSecret}
	wecomTokenCache.Lock()
	defer wecomTokenCache.Unlock()
	if entry, ok := wecomTokenCache.entries[credentials]; ok && entry.token == rejected {
		delete(wecomTokenCache.entries, credentials)
	}
}

// fetchWeComAccessToken calls the gettoken API. It takes no locks.
func fetchWeComAccessToken(client *http.Client, corpID, corpSecret string) (string, time.Time, error) {
	tokenURL := wecomTokenURL + "?" + url.Values{
		"corpid":     {corpID},
		"corpsecret": {corpSecret},
	}.Encode()
	req, err := buildNotifyRequest(http.MethodGet, tokenURL, nil, "")
	if err != nil {
		return "", time.Time{}, fmt.Errorf("build WeCom token request: %w", err)
	}
	resp, err := doNotifyRequest(client, req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()
	tokenBody, err := readAllWithLimit(resp.Body, maxNotifyResponseBytes)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read WeCom token response: %w", err)
	}
	var tokenResult struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
	}
	if err := json.Unmarshal(tokenBody, &tokenResult); err != nil {
		return "", time.Time{}, fmt.Errorf("parse WeCom token response: %w", err)
	}
	if tokenResult.ErrCode != 0 {
		// errcode/errmsg only: the raw body is not echoed into logs or the
		// stored lastSendError.
		return "", time.Time{}, fmt.Errorf("WeCom token error: errcode=%d %s",
			tokenResult.ErrCode, truncateNotifyExcerpt(sanitizeNotifyExcerpt(tokenResult.ErrMsg)))
	}
	if tokenResult.AccessToken == "" {
		return "", time.Time{}, errors.New("WeCom token error: empty access_token")
	}

	expiresIn := tokenResult.ExpiresIn
	if expiresIn <= 60 {
		expiresIn = 7200
	}
	return tokenResult.AccessToken, time.Now().Add(time.Duration(expiresIn-60) * time.Second), nil
}

// wecomSendError carries the send API's errcode so sendWeCom can tell a
// rejected token from other failures.
type wecomSendError struct {
	code int
	err  error
}

func (e *wecomSendError) Error() string { return e.err.Error() }
func (e *wecomSendError) Unwrap() error { return e.err }

// WeCom API endpoints; tests point them at a local server.
var (
	wecomTokenURL       = "https://qyapi.weixin.qq.com/cgi-bin/gettoken"
	wecomMessageSendURL = "https://qyapi.weixin.qq.com/cgi-bin/message/send"
)

func sendWeCom(client *http.Client, params map[string]interface{}, title, content string) error {
	corpID := paramString(params, "corpid", "corpId")
	corpSecret := paramString(params, "corpsecret", "corpSecret")
	agentID := paramString(params, "agentid", "agentId")
	toUser := "@all"
	if u := paramString(params, "touser", "toUser"); u != "" {
		toUser = u
	}

	msgBody := map[string]interface{}{
		"touser":  toUser,
		"msgtype": "text",
		"agentid": agentID,
		"text": map[string]string{
			"content": title + "\n" + content,
		},
	}

	// At most one retry: a token the send API rejects as expired is dropped
	// from the cache and the send is repeated once with a fresh token. Before,
	// the rejected token stayed cached until its expires_in ran out, so every
	// notification in that window (up to two hours) failed.
	for attempt := 0; ; attempt++ {
		accessToken, err := getWeComAccessToken(client, corpID, corpSecret)
		if err != nil {
			return err
		}
		err = sendWeComMessage(client, accessToken, msgBody)
		var sendErr *wecomSendError
		if attempt == 0 && errors.As(err, &sendErr) {
			if _, expired := wecomTokenExpiredCodes[sendErr.code]; expired {
				invalidateWeComAccessToken(corpID, corpSecret, accessToken)
				continue
			}
		}
		return err
	}
}

func sendWeComMessage(client *http.Client, accessToken string, msgBody map[string]interface{}) error {
	msgURL := wecomMessageSendURL + "?" + url.Values{
		"access_token": {accessToken},
	}.Encode()
	respBody, err := sendJSONNotify(client, msgURL, msgBody)
	if err != nil {
		return err
	}
	// notifyProviderError decides failure exactly as for the other providers
	// (including string-typed codes); the code is extracted separately only to
	// recognise a rejected token.
	if providerErr := notifyProviderError(respBody, "errcode"); providerErr != nil {
		var m map[string]interface{}
		_ = json.Unmarshal(respBody, &m)
		return &wecomSendError{code: util.ToInt(m["errcode"]), err: providerErr}
	}
	return nil
}

func sendLark(client *http.Client, params map[string]interface{}, title, content string) error {
	webhook := paramString(params, "url", "webhook")
	body := map[string]interface{}{
		"msg_type": "interactive",
		"card": map[string]interface{}{
			"header": map[string]interface{}{
				"title": map[string]interface{}{
					"tag":     "plain_text",
					"content": title,
				},
			},
			"elements": []map[string]interface{}{
				{
					"tag":     "markdown",
					"content": content,
				},
			},
		},
	}
	if secret := paramString(params, "secret"); secret != "" {
		timestamp := notifyNow().Unix()
		body["timestamp"] = fmt.Sprintf("%d", timestamp)
		body["sign"] = larkSign(secret, timestamp)
	}
	_, err := sendJSONNotify(client, webhook, body, "code", "StatusCode")
	return err
}

// defaultBarkServer is used when a Bark config leaves server empty.
const defaultBarkServer = "https://api.day.app"

// withDefaultBarkServer returns a shallow copy of params whose empty server is
// replaced by defaultBarkServer, for comparing delivery targets. params itself
// is never modified, so the stored value keeps following the default.
func withDefaultBarkServer(params map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(params)+1)
	for k, v := range params {
		out[k] = v
	}
	if paramString(out, "server") == "" {
		out["server"] = defaultBarkServer
	}
	return out
}

// sendBark posts to {server}/push. Bark reports success as code=200, so the
// shared non-zero check in notifyProviderError would treat every success as a
// failure; the code is checked here instead.
func sendBark(client *http.Client, params map[string]interface{}, title, content string) error {
	server := paramString(params, "server")
	if server == "" {
		server = defaultBarkServer
	}
	body := map[string]interface{}{
		"device_key": paramString(params, "deviceKey"),
		"title":      title,
		"body":       content,
	}
	for _, key := range []string{"group", "sound"} {
		if v := paramString(params, key); v != "" {
			body[key] = v
		}
	}
	jsonData, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal notify body: %w", err)
	}
	req, err := buildNotifyRequest(http.MethodPost, strings.TrimRight(server, "/")+"/push", bytes.NewReader(jsonData), "application/json")
	if err != nil {
		return fmt.Errorf("build notify request: %w", err)
	}
	respBody, err := sendNotifyRequestBytes(client, req)
	if err != nil {
		return err
	}
	var m map[string]interface{}
	if json.Unmarshal(respBody, &m) != nil {
		return nil
	}
	if v, ok := m["code"]; ok {
		if code := util.ToInt(v); code != http.StatusOK {
			if message := notifyProviderMessage(m); message != "" {
				return fmt.Errorf("notify code=%d: %s", code, message)
			}
			return fmt.Errorf("notify code=%d", code)
		}
	}
	return nil
}

func paramString(params map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if v, ok := params[key]; ok && v != nil {
			s := strings.TrimSpace(fmt.Sprintf("%v", v))
			if s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}
