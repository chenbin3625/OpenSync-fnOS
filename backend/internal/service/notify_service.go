package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"opensync/internal/mapper"
	"opensync/internal/model"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
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

// recordNotifySendOutcome is a seam so the recording rules can be tested without
// a database, following the same pattern as the task item persistence helpers.
var recordNotifySendOutcome = mapper.UpdateNotifySendResult

// notifyErrorURLPattern finds URLs inside an error message so they can be
// masked before the text is stored. The character class stops at quotes because
// net/http formats its errors as `Post "<url>": <cause>`.
var notifyErrorURLPattern = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.\-]*://[^\s"']+`)

var notifyHTTPClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		// Webhook destinations may be self-hosted on a LAN or loopback address;
		// do not reject them after DNS resolution.
		MaxIdleConns:        50,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	},
}

// GetNotifyList returns notify list with secret fields redacted so tokens
// never reach the client DOM. Raw secrets are still held in the DB and used
// internally by SendTaskNotification; only the list response is masked.
func GetNotifyList() []map[string]interface{} {
	list, err := mapper.GetNotifyList(false)
	if err != nil {
		panic(err.Error())
	}
	for _, notify := range list {
		method := util.ToInt(notify["method"])
		notify["params"] = redactNotifyParams(method, fmt.Sprintf("%v", notify["params"]))
	}
	return list
}

// notifySecretKeys are param keys whose values are redacted in list responses.
// body is included because request-body templates commonly embed tokens; the
// stored value is restored for editing via resolveNotifyParams.
var notifySecretKeys = map[int][]string{
	0: {"url", "headers", "body"},   // custom webhook: URL may embed token, headers/body carry auth
	1: {"sendKey"},                  // Server酱
	2: {"url", "webhook"},           // 钉钉 (access_token in URL query)
	3: {"corpsecret", "corpSecret"}, // 企业微信应用密钥
	4: {"url", "webhook"},           // 飞书 (token in URL path)
}

const notifyRedactionMarker = "****"

// notifyBodyRedacted replaces a request-body template in list responses. It
// contains the standard marker so resolveNotifyParams restores the stored
// template on edit.
const notifyBodyRedacted = "******"

func maskSecretValue(value string) string {
	if len(value) <= 4 {
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
		return paramsStr
	}
	// Redaction is decided by key, never by the value's Go type. headers is
	// stored as a JSON object, so the old `s, _ := v.(string); if s == ""
	// { continue }` guard skipped it entirely and returned Authorization in
	// cleartext — the case "headers" branch below was unreachable.
	for _, key := range notifySecretKeys[method] {
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
		case "url", "webhook":
			params[key] = maskNotifyURL(s)
		default:
			params[key] = maskSecretValue(s)
		}
	}
	out, err := json.Marshal(params)
	if err != nil {
		return paramsStr
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
	existing, err := mapper.GetNotifyByID(notifyID)
	if err != nil || existing == nil {
		return incoming, nil
	}
	existingParams, _ := parseNotifyParams(fmt.Sprintf("%v", existing["params"]))
	// Stored secrets are only merged back when the delivery target is unchanged.
	// Otherwise a caller could submit {"id":<existing>,"params":{"url":"https://
	// attacker/hook"}} and have the saved Authorization header merged in, so a
	// test send (or a PUT) ships the credential to an address of their choosing.
	if notifyTargetChanged(incoming, existingParams) {
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
func notifyTargetChanged(incoming, existing map[string]interface{}) bool {
	incomingURL := paramString(incoming, "url", "webhook")
	if incomingURL == "" || isMaskedSecretValue(incomingURL) {
		return false
	}
	existingURL := paramString(existing, "url", "webhook")
	if existingURL == "" {
		return false
	}
	return incomingURL != existingURL
}

func validateNotifyParams(method int, params map[string]interface{}) error {
	switch method {
	case 0:
		if err := validateNotifyWebhookURL(paramString(params, "url", "webhook")); err != nil {
			return err
		}
		return validateWebhookMethod(paramString(params, "method", "httpMethod"))
	case 1:
		if paramString(params, "sendKey") == "" {
			return errors.New(msg.NotifyParamInvalid)
		}
		return nil
	case 2, 4:
		return validateNotifyHTTPSURL(paramString(params, "url", "webhook"))
	case 3:
		if paramString(params, "corpid", "corpId") == "" ||
			paramString(params, "corpsecret", "corpSecret") == "" ||
			paramString(params, "agentid", "agentId") == "" {
			return errors.New(msg.NotifyParamInvalid)
		}
		return nil
	default:
		return errors.New(msg.NotifyMethodInvalid)
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
		return errors.New(msg.NotifyParamInvalid)
	}
}

func validateNotifyWebhookURL(rawURL string) error {
	return util.ValidateHTTPURL(rawURL, msg.NotifyURLInvalid)
}

func validateNotifyHTTPSURL(rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New(msg.NotifyURLInvalid)
	}
	if strings.ToLower(u.Scheme) != "https" {
		return errors.New(msg.NotifyURLInvalid)
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
func AddNewNotify(notify map[string]interface{}) int64 {
	params, err := notifyParamsValue(notify["params"])
	if err != nil {
		panic(err.Error())
	}
	method := util.ToInt(notify["method"])
	notify["method"] = method
	notify["enable"] = util.ToInt(notify["enable"])
	if err := validateNotifyParams(method, params); err != nil {
		panicPublic(err.Error())
	}
	out, err := json.Marshal(params)
	if err != nil {
		panic(err.Error())
	}
	notify["params"] = string(out)
	id, err := mapper.AddNotify(notify)
	if err != nil {
		panic(err.Error())
	}
	return id
}

// EditNotify updates a notify config. Secret fields that were redacted in
// the list view (or left empty) are preserved from the stored config so the
// user can edit other fields without re-entering credentials.
func EditNotify(notify map[string]interface{}) {
	resolved, err := resolveNotifyParams(notify)
	if err != nil {
		panic(err.Error())
	}
	if err := validateNotifyParams(util.ToInt(notify["method"]), resolved); err != nil {
		panicPublic(err.Error())
	}
	out, err := json.Marshal(resolved)
	if err != nil {
		panic(err.Error())
	}
	notify["params"] = string(out)
	if err := mapper.EditNotify(notify); err != nil {
		panic(err.Error())
	}
}

// UpdateNotifyStatus updates notify enable status
func UpdateNotifyStatus(notifyID int64, enable int) {
	err := mapper.UpdateNotifyStatus(notifyID, enable)
	panicPublicIf(err, msg.NotifyNotFound)
}

// DeleteNotify deletes a notify config
func DeleteNotify(notifyID int64) {
	err := mapper.DeleteNotify(notifyID)
	panicPublicIf(err, msg.NotifyNotFound)
}

// TestNotify sends a test notification. Secrets that were redacted in the
// list view are restored from the stored config (when an id is given) so the
// test sends with real credentials.
func TestNotify(notify map[string]interface{}) {
	defer func() {
		if r := recover(); r != nil {
			if publicErr, ok := r.(model.PublicError); ok {
				panic(publicErr)
			}
			// A runtime error here is a bug in our own code, not a bad webhook
			// config. Reporting it as "check your configuration" sent users
			// hunting through settings for something they cannot fix, so it is
			// re-panicked and surfaces as a generic 500 instead.
			if runtimeErr, ok := r.(runtime.Error); ok {
				log.Printf("notify test failed with a runtime error: %v", runtimeErr)
				panic(r)
			}
			log.Printf("notify test failed: %v", r)
			panicPublic(msg.NotifySendFail)
		}
	}()
	resolved, err := resolveNotifyParams(notify)
	if err != nil {
		panic(err.Error())
	}
	if err := validateNotifyParams(util.ToInt(notify["method"]), resolved); err != nil {
		panicPublic(err.Error())
	}
	out, err := json.Marshal(resolved)
	if err != nil {
		panic(err.Error())
	}
	notify["params"] = string(out)
	testMsg := msg.NotifyTestMsg
	sendNotify(notify, "OpenSync Test", testMsg, false)
}

// SendTaskNotification sends notification after task completion.
//
// This is the synchronous delivery path. The task completion path does not call
// it directly — it hands the work to the background dispatcher via
// QueueTaskNotification, so a slow webhook cannot hold up the finishing task.
func SendTaskNotification(taskID int64, status int, taskNum map[string]interface{}, duration int, createTime float64) {
	notifyList, err := mapper.GetNotifyList(true)
	if err != nil || len(notifyList) == 0 {
		return
	}

	job, err := mapper.GetJobByTaskID(taskID)
	if err != nil {
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

// trySendNotify turns the panic-based send path into a value. The outcome has to
// survive as data, not just as a log line: a config whose token expired kept
// reporting nothing at all through the API, so the UI showed a healthy channel
// while every message was being rejected.
func trySendNotify(notify map[string]interface{}, title, content string, needNotSync bool) (sent bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			sent = false
			err = notifySendFailure(r)
		}
	}()
	return sendNotify(notify, title, content, needNotSync), nil
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
	if err := recordNotifySendOutcome(notifyID, status, time.Now().Unix(), reason); err != nil {
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
		return msg.NotifySendFail
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
func sendNotify(notify map[string]interface{}, title, content string, needNotSync bool) bool {
	params, err := notifyParamsValue(notify["params"])
	if err != nil {
		panic(err.Error())
	}

	method := util.ToInt(notify["method"])
	if err := validateNotifyParams(method, params); err != nil {
		panicPublic(err.Error())
	}

	// Check notSendNull flag
	if needNotSync {
		if v, ok := params["notSendNull"]; ok {
			if util.ToBool(v) {
				return false
			}
		}
	}

	switch method {
	case 0: // Custom webhook
		sendWebhook(notifyHTTPClient, params, title, content)
	case 1: // ServerChan
		sendServerChan(notifyHTTPClient, params, title, content)
	case 2: // DingTalk
		sendDingTalk(notifyHTTPClient, params, title, content)
	case 3: // WeCom (Enterprise WeChat)
		sendWeCom(notifyHTTPClient, params, title, content)
	case 4: // Lark (Feishu)
		sendLark(notifyHTTPClient, params, title, content)
	default:
		// validateNotifyParams rejects unknown methods, so this is unreachable
		// unless a new method is added without a send branch. Reporting it as a
		// failure beats recording a delivery that never happened.
		panicPublic(msg.NotifyMethodInvalid)
	}
	return true
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
	req, err := http.NewRequest(method, urlStr, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req, nil
}

func sendNotifyRequest(client *http.Client, req *http.Request) {
	sendNotifyRequestBytes(client, req)
}

// sendNotifyRequestBytes sends the request, validates the HTTP status, and
// returns the response body so callers can inspect provider-specific error
// codes. DingTalk/Lark/ServerChan/WeCom return HTTP 200 with a non-zero
// errcode/code/errno in the body on failure, which a status-only check misses.
func sendNotifyRequestBytes(client *http.Client, req *http.Request) []byte {
	resp := doNotifyRequest(client, req)
	defer resp.Body.Close()
	bodyBytes, err := readAllWithLimit(resp.Body, maxNotifyResponseBytes)
	if err != nil {
		panic(err.Error())
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		msg := strings.TrimSpace(string(bodyBytes))
		if msg != "" {
			log.Printf("notify request failed: status=%s body=%q", resp.Status, msg)
		}
		panic(fmt.Sprintf("notify request failed: %s", resp.Status))
	}
	return bodyBytes
}

func sendJSONNotify(client *http.Client, urlStr string, body interface{}, errorFields ...string) []byte {
	jsonData, err := json.Marshal(body)
	if err != nil {
		panic(err.Error())
	}
	req, err := buildNotifyRequest(http.MethodPost, urlStr, bytes.NewReader(jsonData), "application/json")
	if err != nil {
		panic(err.Error())
	}
	respBody := sendNotifyRequestBytes(client, req)
	if err := notifyProviderError(respBody, errorFields...); err != nil {
		panic(err.Error())
	}
	return respBody
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
			return fmt.Errorf("notify %s=%d: %s", field, code, strings.TrimSpace(string(body)))
		}
	}
	return nil
}

func doNotifyRequest(client *http.Client, req *http.Request) *http.Response {
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
		panicPublic(msg.NotifySendFail)
	}
	return resp
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

func sendWebhook(client *http.Client, params map[string]interface{}, title, content string) {
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
		bodyStr := fmt.Sprintf("%v", customBody)
		bodyStr = strings.ReplaceAll(bodyStr, "{title}", jsonStringContent(title))
		bodyStr = strings.ReplaceAll(bodyStr, "{content}", jsonStringContent(content))
		body = nil
		if err := json.Unmarshal([]byte(bodyStr), &body); err != nil {
			panic(err.Error())
		}
	}

	var req *http.Request
	var err error
	if method == "GET" {
		req, err = buildNotifyRequest(http.MethodGet, urlStr, nil, "")
		if err != nil {
			panic(err.Error())
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
				panic(marshalErr.Error())
			}
			req, err = buildNotifyRequest(method, urlStr, bytes.NewReader(jsonData), contentType)
		}
		if err != nil {
			panic(err.Error())
		}
	}

	if headers, ok := params["headers"]; ok && headers != nil {
		if hMap, ok := headers.(map[string]interface{}); ok {
			for k, v := range hMap {
				req.Header.Set(k, fmt.Sprintf("%v", v))
			}
		}
	}

	sendNotifyRequest(client, req)
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

func sendServerChan(client *http.Client, params map[string]interface{}, title, content string) {
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
	sendJSONNotify(client, urlStr, body, "code", "errno")
}

func sendDingTalk(client *http.Client, params map[string]interface{}, title, content string) {
	webhook := paramString(params, "url", "webhook")
	body := map[string]interface{}{
		"msgtype": "text",
		"text": map[string]string{
			"content": title + "\n" + content,
		},
	}
	sendJSONNotify(client, webhook, body, "errcode")
}

func sendWeCom(client *http.Client, params map[string]interface{}, title, content string) {
	corpID := paramString(params, "corpid", "corpId")
	corpSecret := paramString(params, "corpsecret", "corpSecret")
	agentID := paramString(params, "agentid", "agentId")
	toUser := "@all"
	if u := paramString(params, "touser", "toUser"); u != "" {
		toUser = u
	}

	// Get access token
	tokenURL := "https://qyapi.weixin.qq.com/cgi-bin/gettoken?" + url.Values{
		"corpid":     {corpID},
		"corpsecret": {corpSecret},
	}.Encode()
	req, err := buildNotifyRequest(http.MethodGet, tokenURL, nil, "")
	if err != nil {
		panic(err.Error())
	}
	resp := doNotifyRequest(client, req)
	defer resp.Body.Close()
	tokenBody, err := readAllWithLimit(resp.Body, maxNotifyResponseBytes)
	if err != nil {
		panic(err.Error())
	}
	var tokenResult struct {
		AccessToken string `json:"access_token"`
		ErrCode     int    `json:"errcode"`
	}
	if err := json.Unmarshal(tokenBody, &tokenResult); err != nil {
		panic(err.Error())
	}
	if tokenResult.ErrCode != 0 {
		panic(fmt.Sprintf("WeCom token error: %s", strings.TrimSpace(string(tokenBody))))
	}

	// Send message
	msgBody := map[string]interface{}{
		"touser":  toUser,
		"msgtype": "text",
		"agentid": agentID,
		"text": map[string]string{
			"content": title + "\n" + content,
		},
	}
	msgURL := "https://qyapi.weixin.qq.com/cgi-bin/message/send?" + url.Values{
		"access_token": {tokenResult.AccessToken},
	}.Encode()
	sendJSONNotify(client, msgURL, msgBody, "errcode")
}

func sendLark(client *http.Client, params map[string]interface{}, title, content string) {
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
	sendJSONNotify(client, webhook, body, "code", "StatusCode")
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
