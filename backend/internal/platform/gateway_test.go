package platform

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func gatewayTestRouter(t *testing.T, development bool, allowedOrigins []string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GatewayRequired(development, allowedOrigins))
	r.GET("/", func(c *gin.Context) { c.Status(200) })
	r.HEAD("/", func(c *gin.Context) { c.Status(200) })
	r.POST("/", func(c *gin.Context) { c.Status(200) })
	return r
}

func serveGatewayRequest(t *testing.T, r *gin.Engine, method, target string, headers map[string]string, withIdentity bool) int {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if withIdentity {
		req.Header.Set("X-Trim-Userid", "1000")
		req.Header.Set("X-Trim-Isadmin", "true")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

func TestGatewayRequiresAdministrator(t *testing.T) {
	for _, tc := range []struct {
		uid, admin string
		status     int
	}{{"", "", 401}, {"bad", "true", 401}, {"1000", "false", 403}, {"1000", "true", 200}} {
		r := gatewayTestRouter(t, false, nil)
		code := serveGatewayRequest(t, r, http.MethodGet, "/", map[string]string{
			"X-Trim-Userid":  tc.uid,
			"X-Trim-Isadmin": tc.admin,
		}, false)
		if code != tc.status {
			t.Fatalf("uid=%q admin=%q status=%d", tc.uid, tc.admin, code)
		}
	}
}

func TestGatewayAllowsWritesFromTheAppItself(t *testing.T) {
	for name, tc := range map[string]struct {
		target  string
		allowed []string
		headers map[string]string
	}{
		// The device regression: the gateway rewrites Host on the hop to the app
		// socket, so the public origin never equals c.Request.Host. The browser's
		// own same-origin claim is what keeps the UI working.
		"browser reports same-origin while host was rewritten": {
			target: "http://app.sock/",
			headers: map[string]string{
				"Origin":         "http://10.10.11.250:5666",
				"Sec-Fetch-Site": "same-origin",
			},
		},
		// The reported repro: a client that names its origin but sends no fetch
		// metadata, which no modern browser does.
		"client sends origin without fetch metadata": {
			target: "http://app.sock/",
			headers: map[string]string{
				"Origin":  "http://10.10.11.250:5666",
				"Referer": "http://10.10.11.250:5666/app/opensync/engines",
			},
		},
		"no origin at all from a non-browser client": {
			target:  "http://app.sock/",
			headers: map[string]string{},
		},
		"browser navigated to the app": {
			target: "http://app.sock/",
			headers: map[string]string{
				"Origin":         "http://10.10.11.250:5666",
				"Sec-Fetch-Site": "none",
			},
		},
		"allow-list entry with port": {
			target:  "http://app.sock/",
			allowed: []string{"http://10.10.11.250:5666"},
			headers: map[string]string{"Origin": "http://10.10.11.250:5666"},
		},
		"allow-list bare host accepts any port": {
			target:  "http://app.sock/",
			allowed: []string{"10.10.11.250"},
			headers: map[string]string{"Origin": "http://10.10.11.250:5666"},
		},
		"allow-list explicit default port": {
			target:  "http://app.sock/",
			allowed: []string{"nas.example.com:443"},
			headers: map[string]string{"Origin": "https://nas.example.com"},
		},
		"allow-list host is case insensitive": {
			target:  "http://app.sock/",
			allowed: []string{"NAS.Example.com"},
			headers: map[string]string{"Origin": "https://nas.example.com:5666"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := gatewayTestRouter(t, false, tc.allowed)
			if code := serveGatewayRequest(t, r, http.MethodPost, tc.target, tc.headers, true); code != http.StatusOK {
				t.Fatalf("status=%d, want 200", code)
			}
		})
	}
}

func TestGatewayRejectsCrossSiteWrites(t *testing.T) {
	for name, tc := range map[string]struct {
		target  string
		allowed []string
		headers map[string]string
	}{
		"third-party page": {
			target: "http://app.sock/",
			headers: map[string]string{
				"Origin":         "https://evil.test",
				"Sec-Fetch-Site": "cross-site",
			},
		},
		"cross-site without an origin header": {
			target:  "http://app.sock/",
			headers: map[string]string{"Sec-Fetch-Site": "cross-site"},
		},
		// Another fnOS app on the same NAS is same-site, and the browser still
		// attaches the gateway session cookie to it.
		"another app on the same nas": {
			target: "http://app.sock/",
			headers: map[string]string{
				"Origin":         "http://10.10.11.250:8096",
				"Sec-Fetch-Site": "same-site",
			},
		},
		"allow-list rejects a foreign origin": {
			target:  "http://app.sock/",
			allowed: []string{"http://10.10.11.250:5666"},
			headers: map[string]string{
				"Origin":         "https://evil.test",
				"Sec-Fetch-Site": "same-origin",
			},
		},
		"allow-list rejects the same host on another port": {
			target:  "http://app.sock/",
			allowed: []string{"http://10.10.11.250:5666"},
			headers: map[string]string{"Origin": "http://10.10.11.250:8096"},
		},
		"allow-list rejects an unverifiable origin": {
			target:  "http://app.sock/",
			allowed: []string{"http://10.10.11.250:5666"},
			headers: map[string]string{"Origin": "http://"},
		},
		"allow-list rejects a request without an origin": {
			target:  "http://app.sock/",
			allowed: []string{"http://10.10.11.250:5666"},
			headers: map[string]string{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := gatewayTestRouter(t, false, tc.allowed)
			if code := serveGatewayRequest(t, r, http.MethodPost, tc.target, tc.headers, true); code != http.StatusForbidden {
				t.Fatalf("status=%d, want 403", code)
			}
		})
	}
}

// Reads never mutate, and the UI loads through them before any write happens.
func TestGatewayReadsAreNotSubjectToOriginChecks(t *testing.T) {
	r := gatewayTestRouter(t, false, []string{"http://10.10.11.250:5666"})
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		code := serveGatewayRequest(t, r, method, "http://app.sock/", map[string]string{
			"Origin":         "https://evil.test",
			"Sec-Fetch-Site": "cross-site",
		}, true)
		if code != http.StatusOK {
			t.Fatalf("%s status=%d, want 200", method, code)
		}
	}
}

func TestParseOriginValue(t *testing.T) {
	for value, want := range map[string]struct {
		host, port string
		ok         bool
	}{
		"http://10.10.11.250:5666": {host: "10.10.11.250", port: "5666", ok: true},
		"https://nas.example.com":  {host: "nas.example.com", ok: true},
		"http://NAS.Example.com":   {host: "nas.example.com", ok: true},
		"http://[fd00::1]:5666":    {host: "fd00::1", port: "5666", ok: true},
		"null":                     {},
		"":                         {},
		"http://":                  {},
		"evil.test":                {},
		"file:///tmp/x":            {},
	} {
		got, ok := parseOriginValue(value)
		if ok != want.ok {
			t.Fatalf("parseOriginValue(%q) ok=%v, want %v", value, ok, want.ok)
		}
		if want.ok && (got.host != want.host || got.port != want.port) {
			t.Fatalf("parseOriginValue(%q) = %q:%q, want %q:%q", value, got.host, got.port, want.host, want.port)
		}
	}
}

func TestAllowlistEntryIgnoresInvalidValues(t *testing.T) {
	origin, _ := parseOriginValue("http://nas.example:5666")
	for _, entry := range []string{"", " ", "/app/opensync", "nas.example/../evil", "http://"} {
		if originAllowed(origin, []string{entry}) {
			t.Fatalf("entry %q should not have matched", entry)
		}
	}
}
