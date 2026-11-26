package native

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"opensync/internal/pan123"
	"opensync/internal/quark"
)

func TestBrowseDirEntriesUsesProviderPages(t *testing.T) {
	t.Setenv("OPENSYNC_RCLONE_BIN", "/missing-rclone")
	var panCalls, quarkCalls, panClients atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/file/list":
			panCalls.Add(1)
			parent, _ := strconv.Atoi(query.Get("parentFileId"))
			id, next := 11, 11
			if query.Get("lastFileId") == "11" {
				id, next = 12, -1
			}
			_, _ = fmt.Fprintf(w, `{"code":0,"data":{"lastFileId":%d,"fileList":[{"fileId":%d,"filename":"pan-%d-%d","parentFileId":%d}]}}`, next, id, parent, id, parent)
		case "/1/clouddrive/file/sort":
			quarkCalls.Add(1)
			start, count := 1, 100
			if query.Get("_page") == "2" {
				start, count = 101, 1
			}
			items := make([]map[string]any, 0, count)
			for i := start; i < start+count; i++ {
				items = append(items, map[string]any{
					"fid": strconv.Itoa(i), "file_name": fmt.Sprintf("quark-%03d", i), "file": true,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": 200, "code": 0, "data": map[string]any{"list": items},
				"metadata": map[string]any{"_total": 101},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	restorePan := setPan123OptionsHookForTest(func(o *pan123.Options) {
		panClients.Add(1)
		o.OpenAPIBaseURL = server.URL
		o.TPSLimit = 20
	})
	t.Cleanup(restorePan)
	restoreQuark := setQuarkOptionsHookForTest(func(o *quark.Options) {
		o.APIURL = server.URL + "/1/clouddrive"
		o.TPSLimit = 20
	})
	t.Cleanup(restoreQuark)
	cfg := Config{
		"paged-pan":   {"type": "pan123", "access_token": "browse-test", "root_folder_id": "7"},
		"paged-quark": {"type": "quark", "cookie": "__pus=browse-test"},
	}
	ctx := context.Background()
	check := func(path string, paged bool, cursor string, fresh bool, count int, prefix, next string) {
		t.Helper()
		entries, gotNext, exists, err := cfg.BrowseDirEntries(ctx, path, paged, cursor, fresh, SessionOptions{})
		if err != nil || !exists || len(entries) != count || (len(entries) > 0 && !strings.HasPrefix(entries[0].Name, prefix)) || gotNext != next {
			t.Fatalf("browse %s cursor %q: %v, %d entries, next %q, exists=%v, err=%v", path, cursor, entries, len(entries), gotNext, exists, err)
		}
	}

	check("/paged-pan", true, "", true, 1, "pan-7-11", "11")
	if panCalls.Load() != 1 {
		t.Fatalf("first page used %d upstream requests", panCalls.Load())
	}
	check("/paged-pan", true, "11", false, 1, "pan-7-12", "")
	if panCalls.Load() != 2 || panClients.Load() != 1 {
		t.Fatalf("second page: %d upstream requests, %d clients", panCalls.Load(), panClients.Load())
	}
	check("/paged-pan", false, "", false, 2, "pan-7-11", "")
	cfg["paged-pan"]["root_folder_id"] = "8"
	check("/paged-pan", true, "", false, 1, "pan-8-11", "11")
	if panClients.Load() != 2 {
		t.Fatalf("changing configured root should create a new client, got %d", panClients.Load())
	}

	check("/paged-quark", true, "", true, 100, "quark-001", "2")
	if quarkCalls.Load() != 1 {
		t.Fatalf("first Quark page used %d upstream requests", quarkCalls.Load())
	}
	check("/paged-quark", true, "2", false, 1, "quark-101", "")
	check("/paged-quark", false, "", false, 101, "quark-001", "")
}
