package quark

import (
	"path"
	"strings"
	"sync"
	"time"
)

type dirListing struct {
	entries  []Entry
	byName   map[string]Entry
	expireAt time.Time
}

type dirFlight struct {
	done    chan struct{}
	entries []Entry
	err     error
}

// dirCache caches pdir_fid -> directory children and cleanRelPath -> fid
// within a client session to avoid redundant /file/sort calls during scans.
type dirCache struct {
	mu       sync.RWMutex
	rootFID  string
	ttl      time.Duration
	dirs     map[string]*dirListing
	pathFID  map[string]string
	inFlight map[string]*dirFlight
}

func newDirCache(rootFID string, ttl time.Duration) *dirCache {
	if rootFID == "" {
		rootFID = DefaultRootFolderID
	}
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &dirCache{
		rootFID:  rootFID,
		ttl:      ttl,
		dirs:     make(map[string]*dirListing),
		pathFID:  map[string]string{"": rootFID},
		inFlight: make(map[string]*dirFlight),
	}
}

func cleanRelPath(rel string) string {
	rel = strings.TrimSpace(rel)
	rel = strings.Trim(path.Clean("/"+rel), "/")
	if rel == "." {
		return ""
	}
	return rel
}

func (dc *dirCache) getDir(pdirFID string) ([]Entry, bool) {
	dc.mu.RLock()
	listing, ok := dc.dirs[pdirFID]
	dc.mu.RUnlock()
	if !ok || time.Now().After(listing.expireAt) {
		return nil, false
	}
	out := make([]Entry, len(listing.entries))
	copy(out, listing.entries)
	return out, true
}

func (dc *dirCache) getChild(pdirFID, name string) (Entry, bool, bool) {
	dc.mu.RLock()
	listing, ok := dc.dirs[pdirFID]
	if !ok || time.Now().After(listing.expireAt) {
		dc.mu.RUnlock()
		return Entry{}, false, false
	}
	e, found := listing.byName[name]
	dc.mu.RUnlock()
	return e, found, true
}

func (dc *dirCache) setDir(pdirFID, dirRel string, entries []Entry) {
	copied := make([]Entry, len(entries))
	copy(copied, entries)
	byName := make(map[string]Entry, len(entries))
	for _, e := range entries {
		byName[e.Name] = e
	}
	listing := &dirListing{
		entries:  copied,
		byName:   byName,
		expireAt: time.Now().Add(dc.ttl),
	}
	cleanDir := cleanRelPath(dirRel)

	dc.mu.Lock()
	dc.dirs[pdirFID] = listing
	if dirRel != "\x00" {
		dc.pathFID[cleanDir] = pdirFID
		for _, e := range entries {
			childRel := e.Name
			if cleanDir != "" {
				childRel = cleanDir + "/" + e.Name
			}
			dc.pathFID[childRel] = e.FID
		}
	}
	dc.mu.Unlock()
}

func (dc *dirCache) getPathFID(rel string) (string, bool) {
	clean := cleanRelPath(rel)
	if clean == "" {
		return dc.rootFID, true
	}
	dc.mu.RLock()
	fid, ok := dc.pathFID[clean]
	dc.mu.RUnlock()
	return fid, ok
}

func (dc *dirCache) setPathFID(rel, fid string) {
	clean := cleanRelPath(rel)
	if clean == "" || fid == "" {
		return
	}
	dc.mu.Lock()
	dc.pathFID[clean] = fid
	dc.mu.Unlock()
}

// invalidateDir clears cached directory listing for pdirFID and removes pathFID
// entries under relPrefix (when provided).
func (dc *dirCache) invalidateDir(pdirFID, relPrefix string) {
	cleanPrefix := cleanRelPath(relPrefix)
	dc.mu.Lock()
	if pdirFID != "" {
		delete(dc.dirs, pdirFID)
	}
	if cleanPrefix == "" {
		// Root-level mutation: clear all pathFID except root itself.
		dc.pathFID = map[string]string{"": dc.rootFID}
	} else {
		prefixSlash := cleanPrefix + "/"
		for p, fid := range dc.pathFID {
			if p == cleanPrefix || strings.HasPrefix(p, prefixSlash) {
				delete(dc.pathFID, p)
				if p != cleanPrefix {
					delete(dc.dirs, fid)
				}
			}
		}
	}
	dc.mu.Unlock()
}

// invalidateSubtree removes the cached listing for targetRel's parent and all
// descendant paths/dirs under targetRel.
func (dc *dirCache) invalidateSubtree(parentFID, targetRel string) {
	cleanTarget := cleanRelPath(targetRel)
	dc.mu.Lock()
	if parentFID != "" {
		delete(dc.dirs, parentFID)
	}
	if cleanTarget == "" {
		dc.dirs = make(map[string]*dirListing)
		dc.pathFID = map[string]string{"": dc.rootFID}
		dc.mu.Unlock()
		return
	}
	prefixSlash := cleanTarget + "/"
	for p, fid := range dc.pathFID {
		if p == cleanTarget || strings.HasPrefix(p, prefixSlash) {
			delete(dc.pathFID, p)
			delete(dc.dirs, fid)
		}
	}
	dc.mu.Unlock()
}

func (dc *dirCache) invalidateAll() {
	dc.mu.Lock()
	dc.dirs = make(map[string]*dirListing)
	dc.pathFID = map[string]string{"": dc.rootFID}
	dc.mu.Unlock()
}
