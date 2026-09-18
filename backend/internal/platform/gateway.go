package platform

import (
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// GatewayRequired enforces the fnOS gateway contract: mutations must not be
// reachable from a third-party page, and every request must carry the identity
// headers the gateway injects.
//
// Why the origin check no longer compares Origin against c.Request.Host: the
// app is reachable only through the fnOS unified gateway, which rewrites Host on
// the hop to the app socket. On a real device c.Request.Host is therefore not
// the public host the browser used, so that comparison rejected the app's own
// writes with 403. Host is consequently never used as evidence here; the
// browser's own Sec-Fetch-Site metadata decides instead.
//
// Threat model: this app has no cookie login of its own — the gateway validates
// the fnOS session and injects X-Trim-Userid / X-Trim-Isadmin, which only it can
// set. The origin check remains as defence in depth, because a browser that
// reaches the app's public URL carries the fnOS session cookie: without it, any
// page in the administrator's browser could drive the app as the administrator.
// Note that a non-browser client can forge every header it likes and is not
// constrained here at all; the gateway's session check is the real boundary.
func GatewayRequired(development bool, allowedOrigins []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !sameOriginWrite(c, allowedOrigins) {
			logRejectedWrite(c, allowedOrigins)
			c.AbortWithStatusJSON(403, gin.H{"code": 403, "msg": "不允许跨站操作"})
			return
		}
		if development {
			c.Set("uid", int64(1000))
			c.Next()
			return
		}
		uid, err := strconv.ParseInt(c.GetHeader("X-Trim-Userid"), 10, 64)
		if err != nil || uid <= 0 {
			c.AbortWithStatusJSON(401, gin.H{"code": 401, "msg": "请从飞牛桌面打开应用"})
			return
		}
		if c.GetHeader("X-Trim-Isadmin") != "true" {
			c.AbortWithStatusJSON(403, gin.H{"code": 403, "msg": "仅管理员可使用此应用"})
			return
		}
		c.Set("uid", uid)
		c.Next()
	}
}

// LogOriginPolicy reports the active cross-origin policy once at startup.
func LogOriginPolicy(allowedOrigins []string) {
	if len(allowedOrigins) == 0 {
		log.Printf("跨域策略：未配置 allowed_origins，仅拦截浏览器声明为跨站（Sec-Fetch-Site）的写入请求。" +
			"如需严格模式，请在 config.ini 的 [opensync] 中把访问本应用的地址写入 allowed_origins（例如 http://10.10.11.250:5666）后重启应用")
		return
	}
	log.Printf("跨域策略：仅接受 %q 中列出的来源发起写入请求（allowed_origins）", allowedOrigins)
}

// sameOriginWrite reports whether a request may perform a mutation. Reads are
// always allowed; the gateway identity check still applies to them.
func sameOriginWrite(c *gin.Context, allowedOrigins []string) bool {
	if method := c.Request.Method; method == http.MethodGet || method == http.MethodHead {
		return true
	}

	// Sec-Fetch-Site is set by the browser itself and cannot be forged by a page.
	// "cross-site" is the classic CSRF shape; "same-site" is another origin on
	// the same NAS (a different fnOS app on another port is same-site, and the
	// browser still sends the session cookie), which is just as much an attack.
	// The app's own UI always speaks to its own origin, so it reports
	// "same-origin" and keeps working.
	switch c.GetHeader("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return false
	}

	if len(allowedOrigins) == 0 {
		// Auto mode. The remaining callers carry no browser verdict: modern
		// browsers always send Sec-Fetch-Site above, so what is left is
		// non-browser clients (curl, monitoring) and browsers too old to send
		// fetch metadata. Their Origin cannot be verified — the gateway rewrote
		// Host — so they are accepted rather than breaking legitimate writes.
		// Configuring allowed_origins replaces this with an exact match.
		return true
	}

	origin, ok := parseOriginValue(c.GetHeader("Origin"))
	if !ok {
		return false
	}
	return originAllowed(origin, allowedOrigins)
}

// callerOrigin is the browser-supplied origin of a request.
type callerOrigin struct {
	scheme string // "http" | "https" | "" when the value carried no scheme
	host   string // lowercase, no port
	port   string // "" when absent or equal to the scheme's default port
}

// parseOriginValue parses an Origin header value into its host and port.
// ok is false for empty, "null", relative, non-http(s) and host-less values —
// all of which are unverifiable and therefore rejected in strict mode.
func parseOriginValue(value string) (callerOrigin, bool) {
	value = strings.TrimSpace(value)
	if value == "" || strings.EqualFold(value, "null") {
		return callerOrigin{}, false
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return callerOrigin{}, false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" || parsed.Host == "" {
		return callerOrigin{}, false
	}
	host, port := splitHostPort(parsed.Host)
	if host == "" {
		return callerOrigin{}, false
	}
	if port == defaultPort(scheme) {
		port = ""
	}
	return callerOrigin{scheme: scheme, host: host, port: port}, true
}

// parseAllowlistEntry parses one allowed_origins entry. Entries may be a full
// origin ("http://nas.example:5666") or a bare authority ("nas.example",
// "10.10.11.250:5666"); a bare authority has no scheme, so its port is compared
// against the request's explicit or implicit default port.
func parseAllowlistEntry(value string) (callerOrigin, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return callerOrigin{}, false
	}
	if strings.Contains(value, "://") {
		return parseOriginValue(value)
	}
	// A bare authority must not smuggle a path or query past the host comparison.
	if strings.ContainsAny(value, "/?#") {
		return callerOrigin{}, false
	}
	host, port := splitHostPort(value)
	if host == "" {
		return callerOrigin{}, false
	}
	return callerOrigin{host: host, port: port}, true
}

// originAllowed reports whether the request origin matches the allow-list. An
// entry without a port accepts any port; an entry with one accepts only that
// port (or the origin's implicit default), so listing http://nas:5666 does not
// also admit another app on http://nas:8096.
func originAllowed(origin callerOrigin, allowed []string) bool {
	for _, value := range allowed {
		entry, ok := parseAllowlistEntry(value)
		if !ok {
			continue
		}
		if entry.host != origin.host {
			continue
		}
		if entry.port == "" || entry.port == origin.port || entry.port == defaultPort(origin.scheme) {
			return true
		}
	}
	return false
}

// splitHostPort separates an optional port and strips IPv6 brackets.
func splitHostPort(value string) (host, port string) {
	if h, p, err := net.SplitHostPort(value); err == nil {
		return strings.ToLower(h), p
	}
	return strings.ToLower(strings.Trim(value, "[]")), ""
}

func defaultPort(scheme string) string {
	switch scheme {
	case "http":
		return "80"
	case "https":
		return "443"
	}
	return ""
}

// logRejectedWrite records the headers the decision was made from. Without this
// a 403 is unactionable: the operator cannot see which origin was rejected, or
// that an allow-list needs the address they actually browse to.
func logRejectedWrite(c *gin.Context, allowedOrigins []string) {
	log.Printf("rejected cross-site %s %s: host=%q origin=%q referer=%q x-forwarded-host=%q sec-fetch-site=%q sec-fetch-mode=%q allowed_origins=%q",
		c.Request.Method, c.Request.URL.Path, c.Request.Host,
		c.GetHeader("Origin"), c.GetHeader("Referer"), c.GetHeader("X-Forwarded-Host"),
		c.GetHeader("Sec-Fetch-Site"), c.GetHeader("Sec-Fetch-Mode"), allowedOrigins)
}
