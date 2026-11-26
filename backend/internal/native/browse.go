package native

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"opensync/internal/pan123"
	"opensync/internal/quark"
)

type browseClient struct {
	quark   *quark.Client
	pan123  *pan123.Client
	expires time.Time
	timer   *time.Timer
}

var browseClients = struct {
	sync.Mutex
	byConfig map[[32]byte]browseClient
}{byConfig: make(map[[32]byte]browseClient)}

func (client browseClient) close() {
	if client.timer != nil {
		client.timer.Stop()
	}
	if client.quark != nil {
		_ = client.quark.Close()
	}
	if client.pan123 != nil {
		_ = client.pan123.Close()
	}
}

func getBrowseClient(name string, params map[string]string, opts SessionOptions, fresh bool) (browseClient, bool, error) {
	// Custom token handlers belong to their caller, and must never be reused by
	// another request. The configuration key covers credentials, root and transport.
	cacheable := opts.OnTokenUpdate == nil && !opts.DisableTokenPersist
	if cacheable {
		browseClients.Lock()
		defer browseClients.Unlock()
	}
	raw, _ := json.Marshal(struct {
		Name, Proxy string
		Insecure    bool
		Params      map[string]string
	}{name, opts.ProxyURL, opts.InsecureSkipVerify, params})
	key := sha256.Sum256(raw)
	now := time.Now()
	if cacheable {
		for k, client := range browseClients.byConfig {
			if !now.Before(client.expires) || (fresh && k == key) {
				delete(browseClients.byConfig, k)
				client.close()
			}
		}
		if client, ok := browseClients.byConfig[key]; ok {
			return client, true, nil
		}
	}

	orig := maps.Clone(params)
	// Keep cached directory IDs short-lived because a folder can move between
	// two Browse requests. Sync and writes use their own session, never this cache.
	client := browseClient{expires: now.Add(5 * time.Second)}
	var err error
	if params["type"] == "quark" {
		client.quark, err = newQuarkClientFromParams(orig, opts, func(cookie string) {
			dispatchTokenUpdate(name, orig, "cookie", cookie, opts)
		})
	} else {
		client.pan123, err = newPan123ClientFromParams(orig, opts, func(token string, expires time.Time) {
			dispatchPan123TokenUpdate(name, orig, token, expires, opts)
		})
	}
	if err != nil {
		return browseClient{}, false, err
	}
	if cacheable {
		if len(browseClients.byConfig) >= 32 {
			var oldestKey [32]byte
			var oldest time.Time
			for k, cached := range browseClients.byConfig {
				if oldest.IsZero() || cached.expires.Before(oldest) {
					oldestKey, oldest = k, cached.expires
				}
			}
			browseClients.byConfig[oldestKey].close()
			delete(browseClients.byConfig, oldestKey)
		}
		expires := client.expires
		client.timer = time.AfterFunc(time.Until(expires), func() {
			browseClients.Lock()
			defer browseClients.Unlock()
			if cached, ok := browseClients.byConfig[key]; ok && cached.expires.Equal(expires) {
				delete(browseClients.byConfig, key)
				cached.close()
			}
		})
		browseClients.byConfig[key] = client
	}
	return client, cacheable, nil
}

// BrowseDirEntries lists one directory for interactive browsing. Bare cloud
// backends can bypass rclone and use their own pagination. Wrappers and local
// paths keep the same resolution and authorization as ListDirEntries.
func (c Config) BrowseDirEntries(ctx context.Context, virtualPath string, paged bool, cursor string, fresh bool, opts SessionOptions) ([]DirEntry, string, bool, error) {
	v := strings.TrimSuffix(strings.TrimSpace(virtualPath), "/")
	name, rel := parts(v)
	canonical, found := c.FindKey(name)
	params := c[canonical]
	typ := params["type"]
	quarkBrowse := found && typ == "quark"
	panBrowse := found && typ == "pan123"
	canPage := quarkBrowse || (panBrowse && pan123.NormalizeProtocol(params["protocol"]) == pan123.ProtocolOpenAPI)
	if cursor != "" && (!paged || !canPage) {
		return nil, "", false, errors.New("目录分页位置无效，请刷新目录")
	}
	if !quarkBrowse && !panBrowse {
		entries, exists, err := c.ListDirEntries(ctx, virtualPath, opts)
		return entries, "", exists, err
	}
	if !cleanRelative(rel) {
		return nil, "", false, errors.New("远端路径无效")
	}
	var position int64
	if cursor != "" {
		var err error
		position, err = strconv.ParseInt(cursor, 10, 64)
		if err != nil || position < 1 || (quarkBrowse && position > 2000) {
			return nil, "", false, errors.New("目录分页位置无效，请刷新目录")
		}
	}
	client, cached, err := getBrowseClient(canonical, params, opts, fresh)
	if err != nil {
		return nil, "", false, classifyRcloneError(typ, err.Error())
	}
	if !cached {
		defer client.close()
	}

	entries := []DirEntry{}
	next := ""
	if quarkBrowse {
		var rows []quark.Entry
		if paged {
			page := int(max(1, position))
			var more bool
			rows, more, err = client.quark.ListDirPage(ctx, rel, page)
			if more {
				next = strconv.Itoa(page + 1)
			}
		} else {
			rows, err = client.quark.ListDir(ctx, rel)
		}
		entries = make([]DirEntry, 0, len(rows))
		for _, row := range rows {
			entries = append(entries, DirEntry{Name: row.Name, Path: path.Join(v, row.Name), IsDir: row.IsDir, Size: row.Size, ModTime: row.ModTime.Unix()})
		}
	} else {
		var rows []pan123.Entry
		if paged && canPage {
			var nextID int64
			rows, nextID, err = client.pan123.ListDirPage(ctx, rel, position)
			if nextID > 0 {
				next = strconv.FormatInt(nextID, 10)
			}
		} else {
			rows, err = client.pan123.ListDir(ctx, rel)
		}
		entries = make([]DirEntry, 0, len(rows))
		for _, row := range rows {
			entries = append(entries, DirEntry{Name: row.Name, Path: path.Join(v, row.Name), IsDir: row.IsDir, Size: row.Size, ModTime: row.ModTime.Unix()})
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", false, ctx.Err()
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", false, nil
		}
		return nil, "", false, classifyRcloneError(typ, err.Error())
	}
	sortDirEntries(entries)
	return entries, next, true, nil
}
