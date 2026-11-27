package native

import (
	"context"
	"path"
	"sort"
	"strings"
	"sync"
)

// RemoteDeltaOptions configures incremental change probing on a remote root.
type RemoteDeltaOptions struct {
	FastList       bool
	FollowSymlinks bool
	IncludeHash    bool
	ProxyURL       string
	Cursor         string
	Controller     *RCController
}

// RemoteDeltaResult describes incremental changes discovered by a remote delta probe.
// When Supported is false or FullRescan is true, callers fall back to full ListTree.
type RemoteDeltaResult struct {
	Supported    bool
	HasChanges   bool
	FullRescan   bool
	NextCursor   string
	ChangedDirs  []string // relative dir paths ("" for root, "a/b" for subdirs) to refresh via ListDirEntries
	ChangedFiles []string // relative file paths when directly reported by backend
}

// RemoteChangeProber provides backend-specific incremental change detection
// (e.g., rclone ChangeNotify / cursor delta tokens) to avoid redundant full-tree scans.
type RemoteChangeProber interface {
	PollRemoteChanges(ctx context.Context, cfg Config, ep Endpoint, rootVirtual string, opts RemoteDeltaOptions) (RemoteDeltaResult, error)
}

var (
	remoteProberMu          sync.RWMutex
	remoteBackendProbers    = make(map[string]RemoteChangeProber)
	remoteProberOverride    RemoteChangeProber
	changeNotifyBackendSets = map[string]struct{}{
		"drive":       {},
		"onedrive":    {},
		"dropbox":     {},
		"box":         {},
		"pcloud":      {},
		"yandex":      {},
		"koofr":       {},
		"protondrive": {},
		"seafile":     {},
		"hidrive":     {},
	}
)

// RegisterRemoteChangeProber registers an incremental change prober for a backend type.
func RegisterRemoteChangeProber(backend string, prober RemoteChangeProber) {
	key := strings.ToLower(strings.TrimSpace(backend))
	if key == "" {
		return
	}
	remoteProberMu.Lock()
	defer remoteProberMu.Unlock()
	if prober == nil {
		delete(remoteBackendProbers, key)
		return
	}
	remoteBackendProbers[key] = prober
}

// SetRemoteChangeProberForTest installs a global delta prober override for tests
// and returns a restore callback.
func SetRemoteChangeProberForTest(prober RemoteChangeProber) func() {
	remoteProberMu.Lock()
	prev := remoteProberOverride
	remoteProberOverride = prober
	remoteProberMu.Unlock()
	return func() {
		remoteProberMu.Lock()
		remoteProberOverride = prev
		remoteProberMu.Unlock()
	}
}

// BackendSupportsChangeNotify reports whether an rclone backend type natively
// supports ChangeNotify / cursor-based incremental change reporting.
func BackendSupportsChangeNotify(backend string) bool {
	_, ok := changeNotifyBackendSets[strings.ToLower(strings.TrimSpace(backend))]
	return ok
}

// SupportsChangeNotify reports whether the resolved endpoint natively supports
// ChangeNotify / delta probing (or has a registered incremental prober).
func (ep Endpoint) SupportsChangeNotify() bool {
	if BackendSupportsChangeNotify(ep.Backend) {
		return true
	}
	remoteProberMu.RLock()
	_, hasRegistered := remoteBackendProbers[strings.ToLower(strings.TrimSpace(ep.Backend))]
	remoteProberMu.RUnlock()
	return hasRegistered
}

// NormalizedChangedDirs returns unique, canonical relative directory paths for
// targeted ListDirEntries incremental refreshes (deriving parent dirs from
// ChangedFiles when ChangedDirs is empty).
func (r RemoteDeltaResult) NormalizedChangedDirs() []string {
	seen := make(map[string]struct{})
	addDir := func(raw string) {
		trimmed := strings.TrimSpace(raw)
		trimmed = strings.TrimPrefix(trimmed, "/")
		trimmed = strings.TrimSuffix(trimmed, "/")
		if trimmed == "." {
			trimmed = ""
		}
		if trimmed != "" {
			clean := path.Clean("/" + trimmed)
			clean = strings.TrimPrefix(clean, "/")
			if clean == "." || strings.HasPrefix(clean, "../") {
				return
			}
			trimmed = clean
		}
		seen[trimmed] = struct{}{}
	}
	if len(r.ChangedDirs) > 0 {
		for _, d := range r.ChangedDirs {
			addDir(d)
		}
	} else {
		for _, f := range r.ChangedFiles {
			clean := strings.TrimPrefix(path.Clean("/"+strings.TrimSpace(f)), "/")
			if clean == "" || clean == "." || strings.HasPrefix(clean, "../") {
				continue
			}
			dir := path.Dir(clean)
			if dir == "." {
				dir = ""
			}
			addDir(dir)
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for d := range seen {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func activeRemoteChangeProber(backend string) RemoteChangeProber {
	remoteProberMu.RLock()
	defer remoteProberMu.RUnlock()
	if remoteProberOverride != nil {
		return remoteProberOverride
	}
	return remoteBackendProbers[strings.ToLower(strings.TrimSpace(backend))]
}

// PollRemoteChanges queries an incremental delta prober for rootVirtual if available.
// When no incremental prober is available for the endpoint, it returns
// RemoteDeltaResult{Supported: false}, signaling the caller to fall back to ListTree.
func (c Config) PollRemoteChanges(ctx context.Context, rootVirtual string, opts RemoteDeltaOptions) (RemoteDeltaResult, error) {
	ep, err := c.Resolve(rootVirtual)
	if err != nil {
		return RemoteDeltaResult{}, err
	}
	prober := activeRemoteChangeProber(ep.Backend)
	if prober == nil {
		return RemoteDeltaResult{Supported: false}, nil
	}
	res, err := prober.PollRemoteChanges(ctx, c, ep, rootVirtual, opts)
	if err != nil || !res.Supported {
		return res, err
	}
	if res.HasChanges && !res.FullRescan && len(res.ChangedDirs) == 0 && len(res.ChangedFiles) > 0 {
		res.ChangedDirs = res.NormalizedChangedDirs()
	}
	return res, nil
}
