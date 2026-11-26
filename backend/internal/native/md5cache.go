package native

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	md5CacheMaxEntries    = 300000
	md5CacheFlushInterval = 15 * time.Second
)

type md5CacheKey struct {
	Dev       uint64 `json:"d"`
	Ino       uint64 `json:"i"`
	Size      int64  `json:"s"`
	MtimeNano int64  `json:"m"`
	CtimeNano int64  `json:"c"`
}

type md5CacheEntry struct {
	Key        md5CacheKey `json:"k"`
	MD5        string      `json:"h"`
	LastUsedAt int64       `json:"t"`
}

type localMD5Cache struct {
	mu         sync.Mutex
	entries    map[md5CacheKey]md5CacheEntry
	filePath   string
	dirty      bool
	flushTimer *time.Timer
}

var defaultMD5Cache = &localMD5Cache{
	entries: make(map[md5CacheKey]md5CacheEntry),
}

// SetMD5CachePath configures persistent storage for the local file MD5 metadata
// cache and best-effort loads any previously cached entries.
func SetMD5CachePath(filePath string) {
	defaultMD5Cache.setPath(filePath)
}

// FlushMD5Cache immediately writes any pending MD5 cache entries to disk.
func FlushMD5Cache() {
	defaultMD5Cache.flush()
}

// ResetMD5CacheForTest clears the in-memory cache and unsets the persistence path.
func ResetMD5CacheForTest() {
	defaultMD5Cache.mu.Lock()
	defer defaultMD5Cache.mu.Unlock()
	if defaultMD5Cache.flushTimer != nil {
		defaultMD5Cache.flushTimer.Stop()
		defaultMD5Cache.flushTimer = nil
	}
	defaultMD5Cache.entries = make(map[md5CacheKey]md5CacheEntry)
	defaultMD5Cache.filePath = ""
	defaultMD5Cache.dirty = false
}

func md5CacheKeyFromFileInfo(st os.FileInfo) (md5CacheKey, bool) {
	if st == nil || st.IsDir() || !st.Mode().IsRegular() {
		return md5CacheKey{}, false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || sys == nil || sys.Ino == 0 {
		return md5CacheKey{}, false
	}
	return md5CacheKey{
		Dev:       uint64(sys.Dev),
		Ino:       sys.Ino,
		Size:      st.Size(),
		MtimeNano: st.ModTime().UnixNano(),
		CtimeNano: int64(sys.Ctim.Sec)*1e9 + int64(sys.Ctim.Nsec),
	}, true
}

func (c *localMD5Cache) setPath(filePath string) {
	c.mu.Lock()
	c.filePath = filePath
	c.mu.Unlock()
	if filePath == "" {
		return
	}
	raw, err := os.ReadFile(filePath)
	if err != nil || len(raw) == 0 {
		return
	}
	var saved []md5CacheEntry
	if err := json.Unmarshal(raw, &saved); err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range saved {
		if entry.MD5 == "" || entry.Key.Ino == 0 {
			continue
		}
		if len(c.entries) >= md5CacheMaxEntries {
			break
		}
		c.entries[entry.Key] = entry
	}
}

func (c *localMD5Cache) get(key md5CacheKey) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || entry.MD5 == "" {
		return "", false
	}
	entry.LastUsedAt = time.Now().Unix()
	c.entries[key] = entry
	return entry.MD5, true
}

func (c *localMD5Cache) set(key md5CacheKey, sum string) {
	if sum == "" || key.Ino == 0 {
		return
	}
	c.mu.Lock()
	if len(c.entries) >= md5CacheMaxEntries {
		c.evictLocked(md5CacheMaxEntries / 5)
	}
	c.entries[key] = md5CacheEntry{
		Key:        key,
		MD5:        sum,
		LastUsedAt: time.Now().Unix(),
	}
	c.dirty = true
	if c.filePath != "" && c.flushTimer == nil {
		c.flushTimer = time.AfterFunc(md5CacheFlushInterval, func() {
			c.flush()
		})
	}
	c.mu.Unlock()
}

func (c *localMD5Cache) evictLocked(count int) {
	if count <= 0 || len(c.entries) == 0 {
		return
	}
	// Drop oldest entries by timestamp (approximate cutoff in two linear passes).
	var minTS, maxTS int64
	first := true
	for _, e := range c.entries {
		if first {
			minTS, maxTS, first = e.LastUsedAt, e.LastUsedAt, false
			continue
		}
		if e.LastUsedAt < minTS {
			minTS = e.LastUsedAt
		}
		if e.LastUsedAt > maxTS {
			maxTS = e.LastUsedAt
		}
	}
	cutoff := minTS + (maxTS-minTS)/4
	removed := 0
	for k, e := range c.entries {
		if e.LastUsedAt <= cutoff {
			delete(c.entries, k)
			removed++
			if removed >= count {
				return
			}
		}
	}
	for k := range c.entries {
		delete(c.entries, k)
		removed++
		if removed >= count {
			return
		}
	}
}

func (c *localMD5Cache) flush() {
	c.mu.Lock()
	if c.flushTimer != nil {
		c.flushTimer.Stop()
		c.flushTimer = nil
	}
	if !c.dirty || c.filePath == "" {
		c.mu.Unlock()
		return
	}
	path := c.filePath
	snapshot := make([]md5CacheEntry, 0, len(c.entries))
	for _, e := range c.entries {
		snapshot = append(snapshot, e)
	}
	c.dirty = false
	c.mu.Unlock()

	raw, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	dir := filepath.Dir(path)
	_ = os.MkdirAll(dir, 0700)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}
