package platform

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGatewayRequiresAdministrator(t *testing.T) {
	for _, tc := range []struct {
		uid, admin string
		status     int
	}{{"", "", 401}, {"bad", "true", 401}, {"1000", "false", 403}, {"1000", "true", 200}} {
		r := gin.New()
		r.Use(GatewayRequired(false))
		r.GET("/", func(c *gin.Context) { c.Status(200) })
		req := httptest.NewRequest("GET", "/", nil)
		req.Header.Set("X-Trim-Userid", tc.uid)
		req.Header.Set("X-Trim-Isadmin", tc.admin)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("uid=%q admin=%q status=%d", tc.uid, tc.admin, w.Code)
		}
	}
}

func TestGatewayRejectsCrossOriginMutations(t *testing.T) {
	r := gin.New()
	r.Use(GatewayRequired(false))
	r.POST("/", func(c *gin.Context) { c.Status(200) })
	req := httptest.NewRequest(http.MethodPost, "http://nas.test/", nil)
	req.Header.Set("X-Trim-Userid", "1000")
	req.Header.Set("X-Trim-Isadmin", "true")
	req.Header.Set("Origin", "https://evil.test")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("cross-origin status=%d", w.Code)
	}
}
