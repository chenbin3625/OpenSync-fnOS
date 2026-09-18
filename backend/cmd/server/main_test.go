package main

import (
	"net/http/httptest"
	"testing"
)

func TestStaticRoutesRejectNeighborPrefix(t *testing.T) {
	r := newRouter(false, nil)
	for _, path := range []string{"/app/opensync-other", "/app/opensync/svr", "/app/opensync/svr/missing"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatalf("path %s status=%d", path, w.Code)
		}
	}
}

func TestNativeRoutesDoNotExposeLegacyLogin(t *testing.T) {
	r := newRouter(false, nil)
	for _, path := range []string{"/svr/noAuth/login", "/app/opensync/svr/noAuth/init"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatalf("legacy route %s status=%d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/app/opensync/svr/session", nil))
	if w.Code != 401 {
		t.Fatalf("missing gateway identity status=%d", w.Code)
	}
	req := httptest.NewRequest("GET", "/app/opensync/svr/session", nil)
	req.Header.Set("X-Trim-Userid", "1000")
	req.Header.Set("X-Trim-Isadmin", "true")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("gateway session status=%d", w.Code)
	}
}
