package service

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type safeMoveTestClient struct {
	copyItemTestClient
	source     FileStat
	target     FileStat
	before     FileStat
	freshErr   error
	removeErr  error
	keepSource bool
	afterCopy  func()
	afterProbe func(string)
	removes    int
}

func (c *safeMoveTestClient) FreshFileStatContext(ctx context.Context, dir, name string, interval int) (FileStat, error) {
	if c.afterProbe != nil {
		c.afterProbe(dir)
	}
	if err := ctx.Err(); err != nil {
		return FileStat{}, err
	}
	if c.freshErr != nil {
		return FileStat{}, c.freshErr
	}
	if dir == "/src" {
		return c.source, nil
	}
	if c.copyCalls == 0 {
		return c.before, nil
	}
	return c.target, nil
}

func (c *safeMoveTestClient) CopyFileContext(ctx context.Context, src, dst, name string) (string, error) {
	c.copyCalls++
	if c.afterCopy != nil {
		c.afterCopy()
	}
	return c.transferResult()
}

func (c *safeMoveTestClient) DeleteFileContext(ctx context.Context, dir string, names []string, interval int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.removes++
	if dir != "/src" {
		return errors.New("must delete only source")
	}
	if !c.keepSource {
		c.source.Exists = false
	}
	return c.removeErr
}

func newSafeMoveTestClient() *safeMoveTestClient {
	return &safeMoveTestClient{
		copyItemTestClient: copyItemTestClient{fileExists: true},
		source:             FileStat{Exists: true, SizeKnown: true, Size: 10, Modified: 100},
		target:             FileStat{Exists: true, SizeKnown: true, Size: 10, Modified: 101},
	}
}

func runSafeMoveTest(c *safeMoveTestClient, runtime copyItemRuntime) *CopyItem {
	item := newCopyItem(runtime, c, "/src", "/dst", "file.txt", int64(10), taskItemType(3))
	item.DoIt()
	return item
}

func TestSafeMoveCopiesThenDeletesVerifiedSource(t *testing.T) {
	pinCopyTestDeps(t)
	c := newSafeMoveTestClient()
	c.afterCopy = func() {
		if c.removes != 0 {
			t.Fatal("deleted before copy")
		}
	}
	item := runSafeMoveTest(c, copyItemTestRuntime{})
	if item.status() != taskStatusSuccess || c.copyCalls != 1 || c.moveCalls != 0 || c.removes != 1 {
		t.Fatalf("status=%d copy=%d nativeMove=%d remove=%d error=%s", item.status(), c.copyCalls, c.moveCalls, c.removes, item.errorMessage())
	}
}

func TestSafeMovePreservesSourceOnUnverifiableTarget(t *testing.T) {
	for name, target := range map[string]FileStat{
		"missing":      {},
		"directory":    {Exists: true, IsDir: true, SizeKnown: true, Size: 10},
		"unknown size": {Exists: true},
		"wrong size":   {Exists: true, SizeKnown: true, Size: 9},
	} {
		t.Run(name, func(t *testing.T) {
			pinCopyTestDeps(t)
			c := newSafeMoveTestClient()
			c.target = target
			item := runSafeMoveTest(c, copyItemTestRuntime{})
			if item.status() != taskStatusFailed || c.removes != 0 {
				t.Fatalf("status=%d removes=%d", item.status(), c.removes)
			}
		})
	}
}

func TestSafeMovePreservesChangedSource(t *testing.T) {
	pinCopyTestDeps(t)
	c := newSafeMoveTestClient()
	c.afterCopy = func() { c.source.Modified++ }
	item := runSafeMoveTest(c, copyItemTestRuntime{})
	if item.status() != taskStatusFailed || c.removes != 0 || c.copyCalls != 1 {
		t.Fatalf("status=%d removes=%d copies=%d", item.status(), c.removes, c.copyCalls)
	}
}

func TestSafeMoveRequiresFreshSourceMetadata(t *testing.T) {
	for _, denied := range []bool{false, true} {
		pinCopyTestDeps(t)
		c := newSafeMoveTestClient()
		if denied {
			c.freshErr = errors.New("refresh permission denied")
		} else {
			c.source.Modified = 0
		}
		item := runSafeMoveTest(c, copyItemTestRuntime{})
		if item.status() != taskStatusFailed || c.removes != 0 || c.copyCalls != 0 {
			t.Fatalf("status=%d removes=%d copies=%d", item.status(), c.removes, c.copyCalls)
		}
	}
}

type safeMoveTestRuntime struct {
	copyItemTestRuntime
	ctx    context.Context
	onWait func(*CopyItem)
}

func (r safeMoveTestRuntime) context() context.Context                 { return r.ctx }
func (r safeMoveTestRuntime) isBreak() bool                            { return r.ctx.Err() != nil }
func (r safeMoveTestRuntime) waitForBreak(time.Duration) bool          { return !r.isBreak() }
func (r safeMoveTestRuntime) waitForRemoteCopyCompletion(ci *CopyItem) { r.onWait(ci) }

func TestSafeMoveVerifiesAsynchronousCompletion(t *testing.T) {
	pinCopyTestDeps(t)
	c := newSafeMoveTestClient()
	c.transferID = "remote-copy"
	r := safeMoveTestRuntime{ctx: context.Background(), onWait: func(ci *CopyItem) {
		if c.removes != 0 {
			t.Fatal("deleted while copy is running")
		}
		ci.setProgress(taskStatusSuccess, 100, nil)
	}}
	item := runSafeMoveTest(c, r)
	if item.status() != taskStatusSuccess || c.removes != 1 {
		t.Fatalf("status=%d removes=%d", item.status(), c.removes)
	}
}

func TestSafeMoveCancellationDuringVerificationPreservesSource(t *testing.T) {
	pinCopyTestDeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newSafeMoveTestClient()
	c.afterCopy = func() { c.afterProbe = func(string) { cancel() } }
	item := runSafeMoveTest(c, safeMoveTestRuntime{ctx: ctx})
	if item.status() != taskStatusStopped || c.removes != 0 {
		t.Fatalf("status=%d removes=%d", item.status(), c.removes)
	}
}

func TestSafeMoveLostDeleteResponseDoesNotCopyAgain(t *testing.T) {
	pinCopyTestDeps(t)
	c := newSafeMoveTestClient()
	c.removeErr = errors.New("connection reset by peer")
	item := runSafeMoveTest(c, copyItemTestRuntime{})
	if item.status() != taskStatusSuccess || c.removes != 1 || c.copyCalls != 1 {
		t.Fatalf("status=%d removes=%d copies=%d", item.status(), c.removes, c.copyCalls)
	}
}

func TestSafeMoveDeleteFailureDoesNotCopyAgain(t *testing.T) {
	pinCopyTestDeps(t)
	c := newSafeMoveTestClient()
	c.keepSource = true
	c.removeErr = errors.New("permission denied")
	item := runSafeMoveTest(c, copyItemTestRuntime{})
	if item.status() != taskStatusFailed || c.removes < 1 || c.copyCalls != 1 {
		t.Fatalf("status=%d removes=%d copies=%d", item.status(), c.removes, c.copyCalls)
	}
}

func TestSafeMoveRejectsMultipleDestinationsIncludingStoredJobs(t *testing.T) {
	job := validJobWithPaths(`["/src"]`, `["/a","/b"]`)
	job["method"] = 2
	for _, strict := range []bool{true, false} {
		if err := validateJobInput(job, strict); err == nil {
			t.Fatalf("accepted multiple move destinations (strict=%v)", strict)
		}
	}
}

func TestSafeMoveDoesNotRepeatSuccessfulRemovalWhenSourceReappears(t *testing.T) {
	pinCopyTestDeps(t)
	c := newSafeMoveTestClient()
	c.keepSource = true
	item := runSafeMoveTest(c, copyItemTestRuntime{})
	if item.status() != taskStatusFailed || c.removes != 1 {
		t.Fatalf("status=%d removes=%d; a successful removal must not delete a replacement", item.status(), c.removes)
	}
}

func TestSafeMoveRejectsHashMismatch(t *testing.T) {
	pinCopyTestDeps(t)
	c := newSafeMoveTestClient()
	c.source.MD5 = strings.Repeat("a", 32)
	c.target.MD5 = strings.Repeat("b", 32)
	item := runSafeMoveTest(c, copyItemTestRuntime{})
	if item.status() != taskStatusFailed || c.removes != 0 {
		t.Fatalf("status=%d removes=%d", item.status(), c.removes)
	}
}

func TestSafeMoveRequiresFreshClient(t *testing.T) {
	client := &copyItemTestClient{fileExists: true}
	item := newCopyItem(copyItemTestRuntime{}, client, "/src", "/dst", "file.txt", int64(1), taskItemTypeTransfer)
	item.DoIt()
	if item.status() != taskStatusFailed || client.copyCalls != 0 {
		t.Fatalf("status=%d copies=%d", item.status(), client.copyCalls)
	}
}

func TestSafeMoveNoTaskIDDoesNotTrustUnchangedSameSizeTarget(t *testing.T) {
	pinCopyTestDeps(t)
	c := newSafeMoveTestClient()
	c.before = c.target
	item := runSafeMoveTest(c, copyItemTestRuntime{})
	if item.status() != taskStatusFailed || c.removes != 0 {
		t.Fatalf("status=%d removes=%d; old same-size file does not prove synchronous copy", item.status(), c.removes)
	}
}

func TestSafeMoveLegacyMoveRetryUsesVerifiedTransfer(t *testing.T) {
	jt := &JobTask{Job: map[string]interface{}{"method": 2}, Waiting: newCopyQueue()}
	jt.initRuntime()
	jt.retryTaskItem(map[string]interface{}{"type": 2, "srcPath": "/src", "dstPath": "/dst", "fileName": "legacy.txt", "fileSize": 10})
	items := jt.Waiting.snapshot()
	if len(items) != 1 || items[0].CopyType != taskItemTypeTransfer {
		t.Fatal("legacy move job retry must use verified transfer")
	}
}

func TestSafeMoveQueuesUnchangedFilesAndReplaysPersistentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"data":{"total":1,"content":[{"name":"file.txt","size":10,"modified":100}]}}`))
	}))
	defer server.Close()
	jt := scanTestTask(t, server.URL, server.Client(), map[string]interface{}{"method": 2, "srcPath": "/src", "dstPath": "/dst"})
	jt.sync()
	items := jt.Waiting.snapshot()
	if len(items) != 1 || items[0].CopyType != taskItemTypeTransfer {
		t.Fatalf("queued=%v; want one safe move even when destination looks unchanged", items)
	}
	jt.retryTaskItem(map[string]interface{}{"type": 3, "srcPath": "/src", "dstPath": "/dst", "fileName": "retry.txt", "fileSize": 10})
	items = jt.Waiting.snapshot()
	if len(items) != 2 || items[1].CopyType != taskItemTypeTransfer {
		t.Fatalf("replayed=%v", items)
	}
}

func TestFreshMoveStatUsesRefreshedListAndPreservesUnknownSize(t *testing.T) {
	for _, size := range []string{`"size":0,`, ""} {
		t.Run(size, func(t *testing.T) {
			pages := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/fs/list" {
					t.Errorf("unexpected endpoint %s", r.URL.Path)
					w.WriteHeader(500)
					return
				}
				var req alistListRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				pages++
				if req.Refresh != (req.Page == 1) {
					t.Errorf("refresh=%v page=%d", req.Refresh, req.Page)
				}
				if req.Page == 1 {
					_, _ = w.Write([]byte(`{"code":200,"data":{"total":2,"content":[{"name":"other","size":1}]}}`))
				} else {
					_, _ = fmt.Fprintf(w, `{"code":200,"data":{"total":2,"content":[{"name":" file.txt ",%s"modified":"2026-10-29T00:00:00Z","hash_info":{"md5":"ABCDEF0123456789ABCDEF0123456789"}}]}}`, size)
				}
			}))
			defer server.Close()
			client := &AlistClient{URL: server.URL, client: server.Client()}
			stat, err := client.FreshFileStatContext(context.Background(), "/src", " file.txt ", 0)
			if err != nil || !stat.Exists || stat.SizeKnown != (size != "") || stat.Modified == 0 || stat.MD5 != "abcdef0123456789abcdef0123456789" || pages != 2 {
				t.Fatalf("stat=%+v pages=%d err=%v", stat, pages, err)
			}
		})
	}
}

func TestFreshMoveStatRejectsUnverifiableListings(t *testing.T) {
	for _, data := range []string{`null`, `{}`, `{"content":[]}`, `{"total":2,"content":[]}`, `{"total":-1,"content":[]}`} {
		t.Run(data, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprintf(w, `{"code":200,"data":%s}`, data)
			}))
			defer server.Close()
			client := &AlistClient{URL: server.URL, client: server.Client()}
			if _, err := client.FreshFileStatContext(context.Background(), "/src", "file.txt", 0); err == nil {
				t.Fatal("accepted unverifiable list as source absence")
			}
		})
	}
}

func TestSafeMoveHTTPWithRealFiles(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint("corrupt=", corrupt), func(t *testing.T) {
			pinCopyTestDeps(t)
			root := t.TempDir()
			for _, dir := range []string{"src", "dst"} {
				if err := os.Mkdir(filepath.Join(root, dir), 0700); err != nil {
					t.Fatal(err)
				}
			}
			payload := []byte("0123456789")
			source := filepath.Join(root, "src", "file.txt")
			target := filepath.Join(root, "dst", "file.txt")
			if err := os.WriteFile(source, payload, 0600); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct {
					Path string `json:"path"`
					Dir  string `json:"dir"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				var result interface{}
				switch r.URL.Path {
				case "/api/fs/list":
					file := target
					if req.Path == "/src" {
						file = source
					}
					content := []map[string]interface{}{}
					if info, err := os.Stat(file); err == nil {
						data, _ := os.ReadFile(file)
						content = append(content, map[string]interface{}{"name": "file.txt", "size": info.Size(), "modified": info.ModTime().Unix(), "hash_info": map[string]string{"md5": fmt.Sprintf("%x", md5.Sum(data))}})
					}
					result = map[string]interface{}{"total": len(content), "content": content}
				case "/api/fs/copy":
					data, err := os.ReadFile(source)
					if err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
					if corrupt {
						data = []byte("9876543210")
					}
					if err := os.WriteFile(target, data, 0600); err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
					result = map[string]interface{}{}
				case "/api/fs/get":
					info, err := os.Stat(target)
					if err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
					result = map[string]interface{}{"size": info.Size(), "modified": info.ModTime().Unix()}
				case "/api/fs/remove":
					if req.Dir != "/src" {
						t.Error("delete targeted destination")
						w.WriteHeader(500)
						return
					}
					if err := os.Remove(source); err != nil {
						t.Error(err)
						w.WriteHeader(500)
						return
					}
					result = map[string]interface{}{}
				default:
					t.Errorf("unexpected endpoint %s", r.URL.Path)
					w.WriteHeader(500)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"code": 200, "data": result})
			}))
			defer server.Close()
			client := &AlistClient{URL: server.URL, client: server.Client()}
			item := newCopyItem(copyItemTestRuntime{}, client, "/src", "/dst", "file.txt", int64(len(payload)), taskItemTypeTransfer)
			item.DoIt()
			_, sourceErr := os.Stat(source)
			if corrupt {
				if item.status() != taskStatusFailed || sourceErr != nil {
					t.Fatalf("corrupt copy status=%d sourceErr=%v", item.status(), sourceErr)
				}
			} else {
				data, err := os.ReadFile(target)
				if item.status() != taskStatusSuccess || !os.IsNotExist(sourceErr) || err != nil || string(data) != string(payload) {
					t.Fatalf("move status=%d sourceErr=%v targetErr=%v", item.status(), sourceErr, err)
				}
			}
		})
	}
}
