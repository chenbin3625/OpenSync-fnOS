package native

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixtureCopyURLTransport(t *testing.T, fixtureURL string) *copyURLTransport {
	t.Helper()
	fixture, err := url.Parse(fixtureURL)
	if err != nil {
		t.Fatal(err)
	}
	return &copyURLTransport{
		lookupIP: func(_ context.Context, host string) ([]net.IPAddr, error) {
			if host == "private.test" {
				return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
			}
			return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
		},
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != "8.8.8.8:80" {
				t.Errorf("download dialed an unpinned address: %q", address)
				return nil, errors.New("unexpected download address")
			}
			return (&net.Dialer{}).DialContext(ctx, network, fixture.Host)
		},
	}
}

func TestCopyURLRedirectAndDNSRebindingBlocked(t *testing.T) {
	var requests atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Host != "public.test" || r.Method != http.MethodGet {
			t.Errorf("unexpected upstream request %s %s", r.Method, r.Host)
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://private.test/secret", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "public-content")
	}))
	defer fixture.Close()
	for _, tc := range []string{"redirect", "rebinding", "mixed-answers"} {
		t.Run(tc, func(t *testing.T) {
			transport := fixtureCopyURLTransport(t, fixture.URL)
			var lookups atomic.Int32
			if tc != "redirect" {
				transport.lookupIP = func(_ context.Context, _ string) ([]net.IPAddr, error) {
					if lookups.Add(1) > 1 {
						return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
					}
					ips := []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}
					if tc == "mixed-answers" {
						ips = append(ips, net.IPAddr{IP: net.ParseIP("10.0.0.1")})
					}
					return ips, nil
				}
			}
			source, err := newCopyURLSource(context.Background(), "http://public.test/"+tc, transport)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			attempts := 1
			if tc == "rebinding" {
				attempts = 2
			}
			for i := 0; i < attempts; i++ {
				response, err := http.Get(source.URL)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				response.Body.Close()
				if err != nil {
					t.Fatal(err)
				}
				if tc == "rebinding" && i == 0 {
					if response.StatusCode != http.StatusOK || string(body) != "public-content" || lookups.Load() != 1 {
						t.Fatalf("first request did not pin its single DNS answer: status=%d body=%q lookups=%d", response.StatusCode, body, lookups.Load())
					}
				} else if response.StatusCode != http.StatusBadGateway || !errors.Is(source.Error(), errCopyURLBlocked) {
					t.Fatalf("unsafe fetch was accepted: status=%d error=%v", response.StatusCode, source.Error())
				}
			}
		})
	}
	if requests.Load() != 2 {
		t.Fatalf("unsafe requests reached the upstream: requests=%d, want 2", requests.Load())
	}
}

func TestCopyURLProxyReceivesPinnedTarget(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "public.test" || r.URL.RawQuery != "signature=original" {
			t.Errorf("proxy changed origin Host or query: Host=%q query=%q", r.Host, r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, "proxied")
	}))
	defer fixture.Close()
	origin, _ := url.Parse(fixture.URL)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != "8.8.8.8:80" {
			t.Errorf("proxy received an unpinned target: %s %q", r.Method, r.Host)
			http.Error(w, "invalid target", http.StatusBadRequest)
			return
		}
		upstream, err := net.Dial("tcp", origin.Host)
		if err != nil {
			t.Error(err)
			return
		}
		defer upstream.Close()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		go io.Copy(upstream, conn)
		_, _ = io.Copy(conn, upstream)
	}))
	defer proxy.Close()
	transport := &copyURLTransport{
		proxyURL: proxy.URL,
		lookupIP: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
		},
	}
	client := &http.Client{Transport: transport}
	response, err := client.Get("http://public.test/file?signature=original")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "proxied" {
		t.Fatalf("proxy fetch failed: %q, %v", body, err)
	}
}

func TestCopyURLRealRcloneTransferAndUnsafeRedirect(t *testing.T) {
	requireRclone(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := t.TempDir()
	t.Setenv("TRIM_PKGTMP", "")
	t.Setenv("TRIM_DATA_ACCESSIBLE_PATHS", root)
	virtual, err := virtualLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	var gets atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("source received a preflight request: %s", r.Method)
		}
		gets.Add(1)
		if r.URL.Path == "/unsafe" {
			http.Redirect(w, r, "http://private.test/secret", http.StatusFound)
			return
		}
		if r.URL.RawQuery != "signature=one-shot" {
			t.Errorf("signed query changed: %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Disposition", `attachment; filename="report.txt"`)
		_, _ = io.WriteString(w, "verified-download")
	}))
	defer fixture.Close()
	cfg := Config{}
	transport := fixtureCopyURLTransport(t, fixture.URL)
	name, err := cfg.copyURL(ctx, "http://public.test/file?signature=one-shot", virtual, CopyURLOptions{AutoFilename: true}, SessionOptions{}, transport)
	if err != nil || !strings.Contains(name, "report.txt") {
		t.Fatalf("real rclone copyurl failed: name=%q, error=%v", name, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "report.txt")); err != nil || string(data) != "verified-download" {
		t.Fatalf("download mismatch: %q, %v", data, err)
	}
	if gets.Load() != 1 {
		t.Fatalf("signed URL was fetched more than once: %d", gets.Load())
	}
	_, err = cfg.copyURL(ctx, "http://public.test/unsafe", virtual, CopyURLOptions{FileName: "unsafe.txt"}, SessionOptions{}, transport)
	if !errors.Is(err, errCopyURLBlocked) {
		t.Fatalf("private redirect should fail closed: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "unsafe.txt")); err == nil && len(data) != 0 {
		t.Fatalf("unsafe response was saved: %q", data)
	}
}

func TestCopyURLKeepsTLSHostnameAndRejectsUntrustedCertificate(t *testing.T) {
	var sni atomic.Value
	fixture := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("an untrusted TLS source was fetched")
	}))
	fixture.TLS = &tls.Config{GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		sni.Store(hello.ServerName)
		return nil, nil
	}}
	fixture.StartTLS()
	defer fixture.Close()
	origin, _ := url.Parse(fixture.URL)
	transport := &copyURLTransport{
		lookupIP: func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
		},
		dialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != "8.8.8.8:443" {
				t.Errorf("TLS dial target was not pinned: %s", address)
			}
			return (&net.Dialer{}).DialContext(ctx, network, origin.Host)
		},
	}
	client := &http.Client{Transport: transport}
	response, err := client.Get("https://public.test/file")
	if err == nil {
		response.Body.Close()
		t.Fatal("untrusted source TLS certificate was accepted")
	}
	if sni.Load() != "public.test" {
		t.Fatalf("TLS SNI changed while pinning the address: %v", sni.Load())
	}
}

func TestCopyURLCancellationStopsSourceStream(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		_, _ = io.WriteString(w, "begin")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(stopped)
	}))
	defer fixture.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source, err := newCopyURLSource(ctx, "http://public.test/file", fixtureCopyURLTransport(t, fixture.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	result := make(chan error, 1)
	go func() {
		response, err := client.Get(source.URL)
		if err == nil {
			_, err = io.ReadAll(response.Body)
			response.Body.Close()
		}
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("source stream did not start")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("upstream connection did not stop on cancellation")
	}
	select {
	case <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("download stream did not stop on cancellation")
	}
}
