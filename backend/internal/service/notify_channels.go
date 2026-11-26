package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTelegramAPIBase = "https://api.telegram.org"
	defaultNtfyServer      = "https://ntfy.sh"
	pushPlusSendURL        = "https://www.pushplus.plus/send"
)

// validNotifyEvents lists every supported notification event identifier.
var validNotifyEvents = map[string]struct{}{
	"success":      {},
	"no_sync":      {},
	"partial_fail": {},
	"fail":         {},
	"stopped":      {},
	"timeout":      {},
	"engine_alert": {},
	"check_alert":  {},
	"digest":       {},
}

// errorNotifyEvents are the events matched by eventMode="errors_only".
var errorNotifyEvents = map[string]struct{}{
	"partial_fail": {},
	"fail":         {},
	"timeout":      {},
	"engine_alert": {},
	"check_alert":  {},
}

func validateNotifyEventFilter(params map[string]interface{}) error {
	mode := paramString(params, "eventMode")
	if mode == "" {
		return nil
	}
	switch mode {
	case "all", "errors_only":
		return nil
	case "custom":
		raw, ok := params["events"]
		if !ok || raw == nil {
			return errors.New(msg.T(msg.NotifyParamInvalid))
		}
		events := parseStringSlice(raw)
		if len(events) == 0 {
			return errors.New(msg.T(msg.NotifyParamInvalid))
		}
		for _, ev := range events {
			if _, valid := validNotifyEvents[ev]; !valid {
				return errors.New(msg.T(msg.NotifyParamInvalid))
			}
		}
		params["events"] = events
		return nil
	default:
		return errors.New(msg.T(msg.NotifyParamInvalid))
	}
}

func parseStringSlice(raw interface{}) []string {
	switch v := raw.(type) {
	case []string:
		out := make([]string, 0, len(v))
		seen := make(map[string]struct{}, len(v))
		for _, item := range v {
			s := strings.TrimSpace(item)
			if s == "" {
				continue
			}
			if _, dup := seen[s]; dup {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
		return out
	case []interface{}:
		out := make([]string, 0, len(v))
		seen := make(map[string]struct{}, len(v))
		for _, item := range v {
			s := strings.TrimSpace(fmt.Sprintf("%v", item))
			if s == "" || s == "<nil>" {
				continue
			}
			if _, dup := seen[s]; dup {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
		return out
	case string:
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		seen := make(map[string]struct{}, len(parts))
		for _, item := range parts {
			s := strings.TrimSpace(item)
			if s == "" {
				continue
			}
			if _, dup := seen[s]; dup {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
		return out
	default:
		return nil
	}
}

// shouldSendNotifyForEvent evaluates eventMode / events / notSendNull for a channel.
func shouldSendNotifyForEvent(params map[string]interface{}, event string, needNotSync bool) bool {
	mode := paramString(params, "eventMode")
	if event == "" {
		// Test sends or legacy direct callers without an event name.
		if needNotSync && util.ToBool(params["notSendNull"]) {
			return false
		}
		return true
	}
	switch mode {
	case "errors_only":
		_, ok := errorNotifyEvents[event]
		return ok
	case "custom":
		events := parseStringSlice(params["events"])
		for _, ev := range events {
			if ev == event {
				return true
			}
		}
		return false
	default:
		// "all" or legacy unset
		if needNotSync && util.ToBool(params["notSendNull"]) {
			return false
		}
		return true
	}
}

func renderNotifyTemplate(template string, vars map[string]string) string {
	if strings.TrimSpace(template) == "" || len(vars) == 0 {
		return template
	}
	pairs := make([]string, 0, len(vars)*2)
	for k, v := range vars {
		pairs = append(pairs, "{"+k+"}", v)
	}
	return strings.NewReplacer(pairs...).Replace(template)
}

func applyNotifyTemplates(params map[string]interface{}, defaultTitle, defaultContent string, vars map[string]string) (string, string) {
	title := defaultTitle
	content := defaultContent
	if len(vars) == 0 {
		return title, content
	}
	if tpl := paramString(params, "titleTemplate"); tpl != "" {
		if rendered := strings.TrimSpace(renderNotifyTemplate(tpl, vars)); rendered != "" {
			title = rendered
		}
	}
	if tpl := paramString(params, "contentTemplate"); tpl != "" {
		if rendered := strings.TrimSpace(renderNotifyTemplate(tpl, vars)); rendered != "" {
			content = rendered
		}
	}
	return title, content
}

func notifyEventForTask(status int, needNotSync bool) string {
	if needNotSync {
		return "no_sync"
	}
	switch status {
	case taskStatusSuccess.Int():
		return "success"
	case taskStatusPartialFail.Int():
		return "partial_fail"
	case taskStatusStopped.Int():
		return "stopped"
	case taskStatusTimeout.Int():
		return "timeout"
	case taskStatusNoSync.Int():
		return "no_sync"
	default:
		return "fail"
	}
}

func localizedTaskStatusLabel(status int, needNotSync bool) string {
	if needNotSync {
		return msg.T(msg.NotifyStatusNoSync)
	}
	switch status {
	case taskStatusSuccess.Int():
		return msg.T(msg.NotifyStatusSuccess)
	case taskStatusPartialFail.Int():
		return msg.T(msg.NotifyStatusPartialFail)
	case taskStatusStopped.Int():
		return msg.T(msg.NotifyStatusStopped)
	case taskStatusTimeout.Int():
		return msg.T(msg.NotifyStatusTimeout)
	case taskStatusNoSync.Int():
		return msg.T(msg.NotifyStatusNoSync)
	case taskStatusSystemFailed.Int(), taskStatusFailed.Int():
		return msg.T(msg.NotifyStatusFail)
	default:
		return msg.T(msg.NotifyStatusUnknown)
	}
}

func localizedMethodLabel(method int) string {
	switch method {
	case 0:
		return msg.T(msg.NotifyMethodIncremental)
	case 1:
		return msg.T(msg.NotifyMethodFull)
	case 2:
		return msg.T(msg.NotifyMethodMove)
	case 3:
		return msg.T(msg.NotifyMethodBisync)
	default:
		return msg.T(msg.NotifyMethodIncremental)
	}
}

func parseNotifyIDList(raw interface{}) ([]int64, error) {
	if raw == nil {
		return nil, nil
	}
	var ids []int64
	switch v := raw.(type) {
	case []int64:
		ids = v
	case []int:
		for _, item := range v {
			ids = append(ids, int64(item))
		}
	case []interface{}:
		for _, item := range v {
			n, ok := parseStrictPositiveInt64(item)
			if !ok {
				return nil, publicError(msg.T(msg.NotifyIdsInvalid))
			}
			ids = append(ids, n)
		}
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" || trimmed == "null" || trimmed == "[]" {
			return nil, nil
		}
		if strings.HasPrefix(trimmed, "[") {
			var decoded []interface{}
			if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
				return nil, publicError(msg.T(msg.NotifyIdsInvalid))
			}
			return parseNotifyIDList(decoded)
		}
		for _, part := range strings.Split(trimmed, ",") {
			p := strings.TrimSpace(part)
			if p == "" {
				continue
			}
			n, err := strconv.ParseInt(p, 10, 64)
			if err != nil || n <= 0 {
				return nil, publicError(msg.T(msg.NotifyIdsInvalid))
			}
			ids = append(ids, n)
		}
	default:
		return nil, publicError(msg.T(msg.NotifyIdsInvalid))
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, publicError(msg.T(msg.NotifyIdsInvalid))
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out, nil
}

func parseStrictPositiveInt64(raw interface{}) (int64, bool) {
	switch v := raw.(type) {
	case int:
		return int64(v), v > 0
	case int64:
		return v, v > 0
	case float64:
		if v != float64(int64(v)) || v <= 0 {
			return 0, false
		}
		return int64(v), true
	case json.Number:
		n, err := v.Int64()
		return n, err == nil && n > 0
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n, err == nil && n > 0
	default:
		return 0, false
	}
}

func filterNotifyListForJob(notifyList []map[string]interface{}, job map[string]interface{}) []map[string]interface{} {
	if job == nil || len(notifyList) == 0 {
		return notifyList
	}
	mode := util.ToInt(job["notifyMode"])
	if mode == 2 {
		return nil
	}
	if mode != 1 {
		return notifyList
	}
	ids, err := parseNotifyIDList(job["notifyIds"])
	if err != nil || len(ids) == 0 {
		return nil
	}
	allowed := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	filtered := make([]map[string]interface{}, 0, len(ids))
	for _, item := range notifyList {
		if _, ok := allowed[util.ToInt64(item["id"])]; ok {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func withDefaultParamURL(params map[string]interface{}, key, fallback string) map[string]interface{} {
	out := make(map[string]interface{}, len(params)+1)
	for k, v := range params {
		out[k] = v
	}
	if paramString(out, key) == "" {
		out[key] = fallback
	}
	return out
}

// --- Channel 6: Telegram Bot ---

func sendTelegram(client *http.Client, params map[string]interface{}, title, content string) error {
	botToken := paramString(params, "botToken")
	chatID := paramString(params, "chatId")
	apiBase := paramString(params, "apiBase")
	if apiBase == "" {
		apiBase = defaultTelegramAPIBase
	}
	apiBase = strings.TrimRight(apiBase, "/")
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", apiBase, url.PathEscape(botToken))

	text := title + "\n\n" + content
	body := map[string]interface{}{
		"chat_id": chatID,
		"text":    text,
	}
	if parseMode := paramString(params, "parseMode"); parseMode != "" {
		body["parse_mode"] = parseMode
	}
	jsonData, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal telegram body: %w", err)
	}
	req, err := buildNotifyRequest(http.MethodPost, endpoint, bytes.NewReader(jsonData), "application/json")
	if err != nil {
		return fmt.Errorf("build telegram request: %w", err)
	}
	respBody, err := sendNotifyRequestBytes(client, req)
	if err != nil {
		return err
	}
	var res struct {
		OK          bool   `json:"ok"`
		ErrorCode   int    `json:"error_code"`
		Description string `json:"description"`
	}
	if json.Unmarshal(respBody, &res) == nil && !res.OK {
		desc := truncateNotifyExcerpt(sanitizeNotifyExcerpt(res.Description))
		if desc != "" {
			return fmt.Errorf("telegram error %d: %s", res.ErrorCode, desc)
		}
		return fmt.Errorf("telegram error %d", res.ErrorCode)
	}
	return nil
}

// --- Channel 7: WeCom Group Bot Webhook ---

func sendWeComWebhook(client *http.Client, params map[string]interface{}, title, content string) error {
	webhook := paramString(params, "url", "webhook")
	msgType := strings.ToLower(paramString(params, "msgtype"))
	var body map[string]interface{}
	if msgType == "text" {
		body = map[string]interface{}{
			"msgtype": "text",
			"text": map[string]string{
				"content": title + "\n" + content,
			},
		}
	} else {
		body = map[string]interface{}{
			"msgtype": "markdown",
			"markdown": map[string]string{
				"content": "**" + title + "**\n" + content,
			},
		}
	}
	_, err := sendJSONNotify(client, webhook, body, "errcode")
	return err
}

// --- Channel 8: SMTP Email ---

func validateEmailHeaderValue(v string) bool {
	return v != "" && !strings.ContainsAny(v, "\r\n\x00")
}

func parseEmailRecipients(raw string) []string {
	raw = strings.ReplaceAll(raw, ";", ",")
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		addr := strings.TrimSpace(p)
		if addr == "" {
			continue
		}
		out = append(out, addr)
	}
	return out
}

func smtpPortFromParams(params map[string]interface{}) (int, error) {
	raw, ok := params["port"]
	if !ok || raw == nil {
		tlsMode := strings.ToLower(paramString(params, "tls"))
		if tlsMode == "ssl" {
			return 465, nil
		}
		return 587, nil
	}
	port := util.ToInt(raw)
	if port < 1 || port > 65535 {
		return 0, errors.New(msg.T(msg.NotifyParamInvalid))
	}
	return port, nil
}

func sendSMTP(params map[string]interface{}, title, content string) error {
	host := paramString(params, "host")
	port, err := smtpPortFromParams(params)
	if err != nil {
		return err
	}
	username := paramString(params, "username")
	password := paramString(params, "password")
	from := paramString(params, "from")
	recipients := parseEmailRecipients(paramString(params, "to"))
	tlsMode := strings.ToLower(paramString(params, "tls"))
	if tlsMode == "" {
		if port == 465 {
			tlsMode = "ssl"
		} else {
			tlsMode = "starttls"
		}
	}

	ctx, cancel := context.WithTimeout(notifyBaseContext(), 30*time.Second)
	defer cancel()

	dial := validatedNotifyDialContext(
		webhookLookupIPAddr,
		(&net.Dialer{Timeout: 20 * time.Second}).DialContext,
	)
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp connect failed: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	tlsCfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if tlsMode == "ssl" {
		tlsConn := tls.Client(conn, tlsCfg)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return fmt.Errorf("smtp tls handshake failed: %w", err)
		}
		conn = tlsConn
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp client init failed: %w", err)
	}
	defer client.Close()

	if tlsMode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("smtp starttls failed: %w", err)
			}
		}
	}

	if username != "" || password != "" {
		auth := smtp.PlainAuth("", username, password, host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth failed: %w", err)
		}
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("smtp mail from failed: %w", err)
	}
	for _, rcpt := range recipients {
		if err := client.Rcpt(rcpt); err != nil {
			return fmt.Errorf("smtp rcpt failed: %w", err)
		}
	}
	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("smtp data failed: %w", err)
	}
	cleanTitle := strings.NewReplacer("\r", " ", "\n", " ").Replace(title)
	encodedSubject := mime.QEncoding.Encode("utf-8", cleanTitle)
	var msgBuf bytes.Buffer
	msgBuf.WriteString("From: " + from + "\r\n")
	msgBuf.WriteString("To: " + strings.Join(recipients, ", ") + "\r\n")
	msgBuf.WriteString("Subject: " + encodedSubject + "\r\n")
	msgBuf.WriteString("MIME-Version: 1.0\r\n")
	msgBuf.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	msgBuf.WriteString(strings.ReplaceAll(content, "\r\n", "\n"))
	if _, err := wc.Write(msgBuf.Bytes()); err != nil {
		_ = wc.Close()
		return fmt.Errorf("smtp write failed: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("smtp close data failed: %w", err)
	}
	_ = client.Quit()
	return nil
}

// --- Channel 9: ntfy ---

func sendNtfy(client *http.Client, params map[string]interface{}, title, content string) error {
	server := paramString(params, "server")
	if server == "" {
		server = defaultNtfyServer
	}
	topic := paramString(params, "topic")
	payload := map[string]interface{}{
		"topic":   topic,
		"title":   title,
		"message": content,
	}
	if priority := util.ToInt(params["priority"]); priority >= 1 && priority <= 5 {
		payload["priority"] = priority
	}
	if rawTags := paramString(params, "tags"); rawTags != "" {
		if tags := parseStringSlice(rawTags); len(tags) > 0 {
			payload["tags"] = tags
		}
	}
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal ntfy body: %w", err)
	}
	req, err := buildNotifyRequest(http.MethodPost, strings.TrimRight(server, "/"), bytes.NewReader(jsonData), "application/json")
	if err != nil {
		return fmt.Errorf("build ntfy request: %w", err)
	}
	if token := paramString(params, "token"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return sendNotifyRequest(client, req)
}

// --- Channel 10: Gotify ---

func sendGotify(client *http.Client, params map[string]interface{}, title, content string) error {
	server := strings.TrimRight(paramString(params, "server"), "/")
	token := paramString(params, "token")
	priority := 5
	if _, ok := params["priority"]; ok && params["priority"] != nil && paramString(params, "priority") != "" {
		priority = util.ToInt(params["priority"])
	}
	payload := map[string]interface{}{
		"title":    title,
		"message":  content,
		"priority": priority,
	}
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal gotify body: %w", err)
	}
	req, err := buildNotifyRequest(http.MethodPost, server+"/message", bytes.NewReader(jsonData), "application/json")
	if err != nil {
		return fmt.Errorf("build gotify request: %w", err)
	}
	req.Header.Set("X-Gotify-Key", token)
	return sendNotifyRequest(client, req)
}

// --- Channel 11: PushPlus ---

func sendPushPlus(client *http.Client, params map[string]interface{}, title, content string) error {
	token := paramString(params, "token")
	template := paramString(params, "template")
	if template == "" {
		template = "txt"
	}
	payload := map[string]interface{}{
		"token":    token,
		"title":    title,
		"content":  content,
		"template": template,
	}
	if topic := paramString(params, "topic"); topic != "" {
		payload["topic"] = topic
	}
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal pushplus body: %w", err)
	}
	req, err := buildNotifyRequest(http.MethodPost, pushPlusSendURL, bytes.NewReader(jsonData), "application/json")
	if err != nil {
		return fmt.Errorf("build pushplus request: %w", err)
	}
	respBody, err := sendNotifyRequestBytes(client, req)
	if err != nil {
		return err
	}
	var m map[string]interface{}
	if json.Unmarshal(respBody, &m) == nil {
		if v, ok := m["code"]; ok {
			if code := util.ToInt(v); code != http.StatusOK {
				if message := notifyProviderMessage(m); message != "" {
					return fmt.Errorf("pushplus code=%d: %s", code, message)
				}
				return fmt.Errorf("pushplus code=%d", code)
			}
		}
	}
	return nil
}
