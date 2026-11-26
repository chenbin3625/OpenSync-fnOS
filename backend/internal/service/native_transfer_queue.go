package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"opensync/internal/native"
)

const (
	maxTrackedNativeTransfers = 50
	maxActiveNativeTransfers  = 4
)

type NativeTransferTask struct {
	ID             string  `json:"id"`
	Kind           string  `json:"kind"` // restore | copyurl | batch_restore | batch_delete | batch_move | trash_restore
	Label          string  `json:"label"`
	Status         string  `json:"status"` // running | completed | failed | cancelled
	DoneBytes      int64   `json:"doneBytes"`
	TotalBytes     int64   `json:"totalBytes"`
	Speed          float64 `json:"speed"`
	Percent        float64 `json:"percent"`
	CompletedItems int     `json:"completedItems"`
	TotalItems     int     `json:"totalItems"`
	StartedAt      int64   `json:"startedAt"`
	FinishedAt     int64   `json:"finishedAt"`
	Error          string  `json:"error,omitempty"`
	Detail         string  `json:"detail,omitempty"`
}

type nativeTransferRecord struct {
	task   NativeTransferTask
	cancel context.CancelFunc
}

type nativeTransferManager struct {
	mu      sync.Mutex
	records map[string]*nativeTransferRecord
	order   []string
	sem     chan struct{}
}

var nativeTransfers = &nativeTransferManager{
	records: make(map[string]*nativeTransferRecord),
	sem:     make(chan struct{}, maxActiveNativeTransfers),
}

func newNativeTransferID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (m *nativeTransferManager) start(kind, label string, totalItems int, run func(ctx context.Context, update func(func(*NativeTransferTask))) error) (*NativeTransferTask, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	id := newNativeTransferID()
	rec := &nativeTransferRecord{
		task: NativeTransferTask{
			ID:         id,
			Kind:       kind,
			Label:      label,
			Status:     "running",
			TotalItems: max(1, totalItems),
			StartedAt:  time.Now().Unix(),
		},
		cancel: cancel,
	}

	m.mu.Lock()
	m.records[id] = rec
	m.order = append(m.order, id)
	m.pruneLocked()
	snap := rec.task
	m.mu.Unlock()

	update := func(fn func(*NativeTransferTask)) {
		m.mu.Lock()
		if cur, ok := m.records[id]; ok {
			fn(&cur.task)
		}
		m.mu.Unlock()
	}

	go func() {
		defer cancel()
		select {
		case m.sem <- struct{}{}:
			defer func() { <-m.sem }()
		case <-ctx.Done():
			update(func(t *NativeTransferTask) {
				if t.Status == "running" {
					t.Status = "cancelled"
					t.FinishedAt = time.Now().Unix()
				}
			})
			return
		}

		err := run(ctx, update)
		update(func(t *NativeTransferTask) {
			if t.Status != "running" {
				return
			}
			t.FinishedAt = time.Now().Unix()
			t.Speed = 0
			if err != nil {
				if ctx.Err() != nil {
					t.Status = "cancelled"
					t.Error = ""
				} else {
					t.Status = "failed"
					t.Error = err.Error()
				}
				return
			}
			t.Status = "completed"
			t.Percent = 100
			if t.CompletedItems < t.TotalItems {
				t.CompletedItems = t.TotalItems
			}
		})
	}()

	return &snap, nil
}

func (m *nativeTransferManager) pruneLocked() {
	for len(m.order) > maxTrackedNativeTransfers {
		oldestIdx := -1
		for i, id := range m.order {
			if rec, ok := m.records[id]; ok && rec.task.Status != "running" {
				oldestIdx = i
				break
			}
		}
		if oldestIdx < 0 {
			break
		}
		dropID := m.order[oldestIdx]
		m.order = append(m.order[:oldestIdx], m.order[oldestIdx+1:]...)
		delete(m.records, dropID)
	}
}

func ListNativeTransfers() []NativeTransferTask {
	nativeTransfers.mu.Lock()
	defer nativeTransfers.mu.Unlock()
	out := make([]NativeTransferTask, 0, len(nativeTransfers.order))
	for i := len(nativeTransfers.order) - 1; i >= 0; i-- {
		if rec, ok := nativeTransfers.records[nativeTransfers.order[i]]; ok {
			out = append(out, rec.task)
		}
	}
	return out
}

func CancelNativeTransfer(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return publicError("缺少传输任务 ID")
	}
	nativeTransfers.mu.Lock()
	rec, ok := nativeTransfers.records[id]
	var cancel context.CancelFunc
	if ok && rec != nil {
		cancel = rec.cancel
		if rec.task.Status == "running" {
			rec.task.Status = "cancelled"
			rec.task.FinishedAt = time.Now().Unix()
			rec.task.Speed = 0
		}
	}
	nativeTransfers.mu.Unlock()
	if !ok {
		return publicError("未找到对应的后台传输任务")
	}
	if cancel != nil {
		cancel()
	}
	return nil
}

func ClearCompletedNativeTransfers() {
	nativeTransfers.mu.Lock()
	defer nativeTransfers.mu.Unlock()
	kept := make([]string, 0, len(nativeTransfers.order))
	for _, id := range nativeTransfers.order {
		if rec, ok := nativeTransfers.records[id]; ok && rec.task.Status == "running" {
			kept = append(kept, id)
		} else {
			delete(nativeTransfers.records, id)
		}
	}
	nativeTransfers.order = kept
}

func ShutdownNativeTransfers() {
	nativeTransfers.mu.Lock()
	var cancels []context.CancelFunc
	for _, rec := range nativeTransfers.records {
		if rec.cancel != nil {
			cancels = append(cancels, rec.cancel)
		}
	}
	nativeTransfers.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// StartAsyncRestoreNativeFile launches a background restore task with live progress.
func StartAsyncRestoreNativeFile(in NativeRestoreInput) (*NativeTransferTask, error) {
	src := strings.TrimSpace(in.SrcPath)
	dst := strings.TrimSpace(in.DstLocalDir)
	if src == "" || dst == "" {
		return nil, publicError("请提供要恢复的远端路径和本地目标目录")
	}
	_, c, err := loadExistingNativeRow()
	if err != nil {
		return nil, err
	}
	label := fmt.Sprintf("恢复 %s → %s", path.Base(strings.TrimRight(src, "/")), dst)
	return nativeTransfers.start("restore", label, 1, func(ctx context.Context, update func(func(*NativeTransferTask))) error {
		if err := c.Restore(ctx, src, dst, in.IsDir, in.Overwrite, native.SessionOptions{
			ProxyURL: globalNativeProxyURL(),
			BwLimit:  globalDefaultBwLimit(),
		}); err != nil {
			return err
		}
		update(func(t *NativeTransferTask) {
			t.CompletedItems = 1
			t.Percent = 100
			t.Detail = fmt.Sprintf("已恢复至 %s", dst)
		})
		return nil
	})
}

// StartAsyncCopyURLNativeStorage launches a background HTTP(S) offline download with Range resume & progress.
func StartAsyncCopyURLNativeStorage(in NativeCopyURLInput) (*NativeTransferTask, error) {
	rawURL := strings.TrimSpace(in.URL)
	dstDir := strings.TrimSpace(in.DstDir)
	if rawURL == "" || dstDir == "" {
		return nil, publicError("请提供下载链接与目标目录")
	}
	_, c, err := loadExistingNativeRow()
	if err != nil {
		return nil, err
	}
	nameHint := strings.TrimSpace(in.FileName)
	if nameHint == "" {
		nameHint = path.Base(strings.Split(rawURL, "?")[0])
	}
	label := fmt.Sprintf("离线下载 %s → %s", nameHint, dstDir)
	return nativeTransfers.start("copyurl", label, 1, func(ctx context.Context, update func(func(*NativeTransferTask))) error {
		savedName, copyErr := c.CopyURLWithProgress(ctx, rawURL, dstDir, native.CopyURLOptions{
			FileName:     in.FileName,
			AutoFilename: in.AutoFilename,
			NoClobber:    in.NoClobber,
		}, native.SessionOptions{
			ProxyURL: globalNativeProxyURL(),
			BwLimit:  globalDefaultBwLimit(),
		}, func(doneBytes, totalBytes int64, speed float64) {
			update(func(t *NativeTransferTask) {
				t.DoneBytes = doneBytes
				t.TotalBytes = totalBytes
				t.Speed = speed
				if totalBytes > 0 {
					pct := float64(doneBytes) * 100.0 / float64(totalBytes)
					if pct > 100 {
						pct = 100
					}
					t.Percent = pct
				}
			})
		})
		if copyErr != nil {
			return copyErr
		}
		update(func(t *NativeTransferTask) {
			t.CompletedItems = 1
			t.Percent = 100
			t.Detail = fmt.Sprintf("已保存为 %s", savedName)
		})
		return nil
	})
}

type NativeBatchItem struct {
	Path  string `json:"path"`
	IsDir bool   `json:"isDir"`
}

type NativeBatchInput struct {
	Action      string            `json:"action"` // delete | restore | move
	Items       []NativeBatchItem `json:"items"`
	DstLocalDir string            `json:"dstLocalDir,omitempty"`
	DstDir      string            `json:"dstDir,omitempty"`
	Overwrite   bool              `json:"overwrite,omitempty"`
}

// StartBatchNativeStorageAction executes multi-select delete/restore/move asynchronously in the transfer queue.
func StartBatchNativeStorageAction(in NativeBatchInput) (*NativeTransferTask, error) {
	action := strings.ToLower(strings.TrimSpace(in.Action))
	if len(in.Items) == 0 {
		return nil, publicError("请至少选择一个文件或目录")
	}
	if len(in.Items) > 500 {
		return nil, publicError("单次批量操作最多支持 500 项")
	}
	switch action {
	case "delete":
	case "restore":
		if strings.TrimSpace(in.DstLocalDir) == "" {
			return nil, publicError("请选择恢复目标本地目录")
		}
	case "move":
		if strings.TrimSpace(in.DstDir) == "" {
			return nil, publicError("请输入目标目录路径")
		}
	default:
		return nil, publicError("不支持的批量操作类型")
	}
	_, c, err := loadExistingNativeRow()
	if err != nil {
		return nil, err
	}
	actionLabels := map[string]string{
		"delete":  "批量删除",
		"restore": "批量恢复",
		"move":    "批量移动",
	}
	label := fmt.Sprintf("%s %d 项", actionLabels[action], len(in.Items))
	items := append([]NativeBatchItem(nil), in.Items...)
	opts := native.SessionOptions{
		ProxyURL: globalNativeProxyURL(),
		BwLimit:  globalDefaultBwLimit(),
	}

	return nativeTransfers.start("batch_"+action, label, len(items), func(ctx context.Context, update func(func(*NativeTransferTask))) error {
		var failedCount int
		var firstErr error
		for i, item := range items {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			p := strings.TrimSpace(item.Path)
			if p == "" {
				continue
			}
			var opErr error
			switch action {
			case "delete":
				opErr = c.DeletePath(ctx, p, item.IsDir, opts)
			case "restore":
				opErr = c.Restore(ctx, p, in.DstLocalDir, item.IsDir, in.Overwrite, opts)
			case "move":
				dstTarget := strings.TrimRight(strings.TrimSpace(in.DstDir), "/") + "/" + path.Base(strings.TrimRight(p, "/"))
				opErr = c.MovePath(ctx, p, dstTarget, item.IsDir, opts)
			}
			if opErr != nil {
				failedCount++
				if firstErr == nil {
					firstErr = opErr
				}
			}
			done := i + 1
			update(func(t *NativeTransferTask) {
				t.CompletedItems = done
				t.Percent = float64(done) * 100.0 / float64(len(items))
			})
		}
		if failedCount > 0 {
			return fmt.Errorf("%d / %d 项失败：%v", failedCount, len(items), firstErr)
		}
		return nil
	})
}

// --- Visual Trash Browser & One-Click Restore ---

type NativeTrashBrowseInput struct {
	DstRoot  string `json:"dstRoot"`
	TrashDir string `json:"trashDir,omitempty"`
}

type NativeTrashItem struct {
	TrashPath    string `json:"trashPath"`
	OriginalPath string `json:"originalPath"`
	BucketDate   string `json:"bucketDate"`
	Version      string `json:"version"`
	Name         string `json:"name"`
	IsDir        bool   `json:"isDir"`
	Size         int64  `json:"size"`
	ModTime      int64  `json:"modTime"`
}

type NativeTrashRestoreInput struct {
	TrashPath    string `json:"trashPath"`
	OriginalPath string `json:"originalPath"`
	IsDir        bool   `json:"isDir"`
	Overwrite    bool   `json:"overwrite"`
}

// BrowseNativeTrash lists recoverable items inside <dstRoot>/<trashDir> organized by date & version bucket.
func BrowseNativeTrash(ctx context.Context, in NativeTrashBrowseInput) ([]NativeTrashItem, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	dstRoot := strings.TrimRight(strings.TrimSpace(in.DstRoot), "/")
	if dstRoot == "" || dstRoot == "/" {
		return nil, publicError("请指定要浏览回收站的存储目录")
	}
	trashDir, err := normalizeTrashDir(in.TrashDir)
	if err != nil {
		trashDir = defaultTrashDir
	}
	_, c, err := loadExistingNativeRow()
	if err != nil {
		return nil, err
	}
	trashRoot := dstRoot + "/" + trashDir
	listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	entries, exists, err := c.ListTree(listCtx, trashRoot, native.TreeOptions{
		ProxyURL: globalNativeProxyURL(),
	})
	if err != nil {
		return nil, publicError(err.Error())
	}
	if !exists || len(entries) == 0 {
		return []NativeTrashItem{}, nil
	}

	items := make([]NativeTrashItem, 0, len(entries))
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		rel := strings.TrimPrefix(path.Clean("/"+e.RelPath), "/")
		segs := strings.Split(rel, "/")
		// Structure created by nativeDeleteOrTrash / rclone --backup-dir:
		// <YYYY-MM-DD>/<versionBucket>/<relPath...>
		bucketDate := ""
		version := ""
		origRel := rel
		if len(segs) >= 3 {
			bucketDate = segs[0]
			version = segs[1]
			origRel = strings.Join(segs[2:], "/")
		} else if len(segs) == 2 {
			bucketDate = segs[0]
			origRel = segs[1]
		}
		items = append(items, NativeTrashItem{
			TrashPath:    trashRoot + "/" + rel,
			OriginalPath: dstRoot + "/" + origRel,
			BucketDate:   bucketDate,
			Version:      version,
			Name:         e.Name,
			IsDir:        false,
			Size:         e.Size,
			ModTime:      e.ModTime,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].BucketDate != items[j].BucketDate {
			return items[i].BucketDate > items[j].BucketDate
		}
		if items[i].ModTime != items[j].ModTime {
			return items[i].ModTime > items[j].ModTime
		}
		return items[i].OriginalPath < items[j].OriginalPath
	})
	return items, nil
}

// RestoreNativeTrashItem restores a file/dir from .opensync-trash back to its original path.
func RestoreNativeTrashItem(ctx context.Context, in NativeTrashRestoreInput) error {
	if ctx == nil {
		ctx = context.Background()
	}
	src := strings.TrimRight(strings.TrimSpace(in.TrashPath), "/")
	dst := strings.TrimRight(strings.TrimSpace(in.OriginalPath), "/")
	if src == "" || dst == "" {
		return publicError("请提供回收站文件路径与原路径")
	}
	if !strings.Contains(src, "/"+defaultTrashDir+"/") && !strings.Contains(src, "trash") {
		return publicError("仅允许从回收站目录还原文件")
	}
	_, c, err := loadExistingNativeRow()
	if err != nil {
		return err
	}
	opCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	opts := native.SessionOptions{ProxyURL: globalNativeProxyURL()}
	if parent := path.Dir(dst); parent != "" && parent != "/" && parent != "." {
		_ = c.Mkdir(opCtx, parent, opts)
	}
	if err := c.MovePath(opCtx, src, dst, in.IsDir, opts); err != nil {
		return publicError(err.Error())
	}
	return nil
}

// OpenNativeStorageDownload opens a direct stream for downloading a single file from storage browser.
func OpenNativeStorageDownload(ctx context.Context, virtualPath string) (io.ReadCloser, string, int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	_, c, err := loadExistingNativeRow()
	if err != nil {
		return nil, "", 0, err
	}
	rc, name, size, err := c.StreamDownload(ctx, virtualPath, native.SessionOptions{ProxyURL: globalNativeProxyURL()})
	if err != nil {
		return nil, "", 0, publicError(err.Error())
	}
	return rc, name, size, nil
}

// UploadNativeStorageFile streams an uploaded browser file directly into dirPath/fileName.
func UploadNativeStorageFile(ctx context.Context, dirPath, fileName string, reader io.Reader) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if reader == nil {
		return 0, publicError("上传内容为空")
	}
	_, c, err := loadExistingNativeRow()
	if err != nil {
		return 0, err
	}
	n, err := c.StreamUpload(ctx, dirPath, fileName, reader, native.SessionOptions{ProxyURL: globalNativeProxyURL()})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return n, publicError("上传已取消")
		}
		return n, publicError(err.Error())
	}
	return n, nil
}
