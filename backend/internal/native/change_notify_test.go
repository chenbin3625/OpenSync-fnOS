package native

import (
	"context"
	"reflect"
	"testing"
)

type stubRemoteChangeProber struct {
	calls int
	fn    func(ctx context.Context, cfg Config, ep Endpoint, rootVirtual string, opts RemoteDeltaOptions) (RemoteDeltaResult, error)
}

func (s *stubRemoteChangeProber) PollRemoteChanges(ctx context.Context, cfg Config, ep Endpoint, rootVirtual string, opts RemoteDeltaOptions) (RemoteDeltaResult, error) {
	s.calls++
	return s.fn(ctx, cfg, ep, rootVirtual, opts)
}

func TestRemoteChangeNotifyCapabilitiesAndNormalization(t *testing.T) {
	for _, backend := range []string{"drive", "onedrive", "dropbox", "box", "pcloud"} {
		if !BackendSupportsChangeNotify(backend) {
			t.Fatalf("expected %s to support ChangeNotify", backend)
		}
		if !(Endpoint{Backend: backend}).SupportsChangeNotify() {
			t.Fatalf("expected Endpoint{%s}.SupportsChangeNotify() == true", backend)
		}
	}
	if BackendSupportsChangeNotify("local") {
		t.Fatalf("local must not report ChangeNotify backend support")
	}

	res := RemoteDeltaResult{
		ChangedFiles: []string{"docs/q1/report.pdf", "/readme.md", "docs/q1/notes.txt", "media/a.jpg"},
	}
	dirs := res.NormalizedChangedDirs()
	want := []string{"", "docs/q1", "media"}
	if !reflect.DeepEqual(dirs, want) {
		t.Fatalf("NormalizedChangedDirs = %v, want %v", dirs, want)
	}
}

func TestConfigPollRemoteChangesDispatch(t *testing.T) {
	cfg := Config{
		"cloud": {"type": "webdav", "url": "https://dav.example.com"},
	}
	// Default without prober -> Supported: false (falls back cleanly to ListTree)
	res, err := cfg.PollRemoteChanges(context.Background(), "/cloud/backup", RemoteDeltaOptions{})
	if err != nil {
		t.Fatalf("PollRemoteChanges fallback error: %v", err)
	}
	if res.Supported {
		t.Fatalf("expected Supported=false when no prober registered, got %+v", res)
	}

	// Custom prober override returns changed files and derives ChangedDirs.
	prober := &stubRemoteChangeProber{
		fn: func(ctx context.Context, c Config, ep Endpoint, rootVirtual string, opts RemoteDeltaOptions) (RemoteDeltaResult, error) {
			if ep.RemoteName != "cloud" || opts.Cursor != "c-1" {
				t.Fatalf("unexpected prober args: ep=%+v opts=%+v", ep, opts)
			}
			return RemoteDeltaResult{
				Supported:    true,
				HasChanges:   true,
				NextCursor:   "c-2",
				ChangedFiles: []string{"sub/item.txt"},
			}, nil
		},
	}
	defer SetRemoteChangeProberForTest(prober)()

	res, err = cfg.PollRemoteChanges(context.Background(), "/cloud/backup", RemoteDeltaOptions{Cursor: "c-1"})
	if err != nil {
		t.Fatalf("PollRemoteChanges with prober: %v", err)
	}
	if !res.Supported || !res.HasChanges || res.NextCursor != "c-2" || !reflect.DeepEqual(res.ChangedDirs, []string{"sub"}) || prober.calls != 1 {
		t.Fatalf("unexpected PollRemoteChanges result: %+v calls=%d", res, prober.calls)
	}
}
