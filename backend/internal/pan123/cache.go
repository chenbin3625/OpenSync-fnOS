package pan123

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

// dirCache caches parentID -> directory children and cleanRelPath -> fileID
// within a client session to eliminate redundant listing calls during scans.
type dirCache struct {
	mu       sync.RWMutex
	rootFID  int64
	ttl      time.Duration
	dirs     map[int64]*dirListing
	pathFID  map[string]int64
	inFlight map[int64]*dirFlight
}

func newDirCache(rootFID int64, ttl time.Duration) *dirCache {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &dirCache{
		rootFID:  rootFID,
		ttl:      ttl,
		dirs:     make(map[int64]*dirListing),
		pathFID:  map[string]int64{"": rootFID},
		inFlight: make(map[int64]*dirFlight),
	}
}

func cleanRelPath(rel string) string {
	rel = strings.Trim(path.Clean("/"+rel), "/")
	if rel == "." {
		return ""
	}
	return rel
}

func (dc *dirCache) getDir(parentFID int64) ([]Entry, bool) {
	dc.mu.RLock()
	listing, ok := dc.dirs[parentFID]
	dc.mu.RUnlock()
	if !ok || time.Now().After(listing.expireAt) {
		return nil, false
	}
	out := make([]Entry, len(listing.entries))
	copy(out, listing.entries)
	return out, true
}

func (dc *dirCache) getChild(parentFID int64, name string) (Entry, bool, bool) {
	dc.mu.RLock()
	listing, ok := dc.dirs[parentFID]
	if !ok || time.Now().After(listing.expireAt) {
		dc.mu.RUnlock()
		return Entry{}, false, false
	}
	e, found := listing.byName[name]
	dc.mu.RUnlock()
	return e, found, true
}

func (dc *dirCache) setDir(parentFID int64, dirRel string, entries []Entry) {
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
	dc.dirs[parentFID] = listing
	if dirRel != "\x00" {
		dc.pathFID[cleanDir] = parentFID
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

func (dc *dirCache) getPathFID(rel string) (int64, bool) {
	clean := cleanRelPath(rel)
	if clean == "" {
		return dc.rootFID, true
	}
	dc.mu.RLock()
	fid, ok := dc.pathFID[clean]
	dc.mu.RUnlock()
	return fid, ok
}

func (dc *dirCache) setPathFID(rel string, fid int64) {
	clean := cleanRelPath(rel)
	if clean == "" || fid <= 0 {
		return
	}
	dc.mu.Lock()
	dc.pathFID[clean] = fid
	dc.mu.Unlock()
}

func (dc *dirCache) invalidateDir(parentFID int64, relPrefix string) {
	cleanPrefix := cleanRelPath(relPrefix)
	dc.mu.Lock()
	delete(dc.dirs, parentFID)
	if cleanPrefix == "" {
		dc.pathFID = map[string]int64{"": dc.rootFID}
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

func (dc *dirCache) invalidateSubtree(parentFID int64, targetRel string) {
	cleanTarget := cleanRelPath(targetRel)
	dc.mu.Lock()
	delete(dc.dirs, parentFID)
	if cleanTarget == "" {
		dc.dirs = make(map[int64]*dirListing)
		dc.pathFID = map[string]int64{"": dc.rootFID}
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
	dc.dirs = make(map[int64]*dirListing)
	dc.pathFID = map[string]int64{"": dc.rootFID}
	dc.mu.Unlock()
}
