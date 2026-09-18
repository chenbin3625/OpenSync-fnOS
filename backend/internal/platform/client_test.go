package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPlatformAPIEnvelopeUsesEnvironmentTokenAndRequestID(t *testing.T) {
	t.Setenv("TRIM_API_TOKEN", "fixture-token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/trimapp" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("missing environment token")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if id, ok := body["reqId"].(string); !ok || id == "" {
			t.Error("missing unique reqId")
		}
		if body["appName"] != "opensync" || body["req"] != "trim.file.getSharedAccessibleFolders" {
			t.Errorf("unexpected envelope %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"code":0,"data":{"paths":["/vol1/data"]}}`))
	}))
	defer server.Close()
	old := apiClient
	apiClient = &http.Client{Transport: redirectTransport{server.Client().Transport, server.URL}}
	defer func() { apiClient = old }()
	paths, err := AuthorizedPaths(context.Background())
	if err != nil || len(paths) != 1 || paths[0] != "/vol1/data" {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
}

type redirectTransport struct {
	transport http.RoundTripper
	base      string
}

func (rt redirectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	next, err := http.NewRequestWithContext(r.Context(), r.Method, rt.base+r.URL.Path, r.Body)
	if err != nil {
		return nil, err
	}
	next.Header = r.Header.Clone()
	return rt.transport.RoundTrip(next)
}
func TestPlatformMissingTokenDoesNotAttemptNetwork(t *testing.T) {
	t.Setenv("TRIM_API_TOKEN", "")
	if _, err := AuthorizedPaths(context.Background()); err == nil {
		t.Fatal("expected unavailable API error")
	}
}
