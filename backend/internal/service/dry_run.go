package service

import (
	"context"
	"fmt"
	"opensync/internal/mapper"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"sync"
	"sync/atomic"
	"time"
)

const (
	dryRunItemLimit  = 500
	dryRunRunTimeout = 15 * time.Minute
	dryRunMaxTracked = 64
)

// DryRunItem represents a single planned action in a read-only dry-run preview.
type DryRunItem struct {
	Action   string `json:"action"` // "copy" | "update" | "delete" | "move" | "mkdir" | "skip"
	SrcPath  string `json:"srcPath"`
	DstPath  string `json:"dstPath"`
	FileName string `json:"fileName"`
	FileSize int64  `json:"fileSize"`
	IsPath   bool   `json:"isPath"`
	Reason   string `json:"reason,omitempty"`
}

// DryRunResult is the snapshot returned by POST/GET /svr/job/dry-run.
type DryRunResult struct {
	JobID          int64        `json:"jobId"`
	Status         string       `json:"status"` // "running" | "completed" | "failed" | "cancelled"
	Method         int          `json:"method"`
	StartedAt      int64        `json:"startedAt"`
	FinishedAt     int64        `json:"finishedAt"`
	ScannedDirs    int64        `json:"scannedDirs"`
	CopyCount      int          `json:"copyCount"`
	CopySize       int64        `json:"copySize"`
	UpdateCount    int          `json:"updateCount"`
	UpdateSize     int64        `json:"updateSize"`
	DeleteCount    int          `json:"deleteCount"`
	DeleteSize     int64        `json:"deleteSize"`
	MoveCount      int          `json:"moveCount"`
	MoveSize       int64        `json:"moveSize"`
	MkdirCount     int          `json:"mkdirCount"`
	SkipCount      int          `json:"skipCount"`
	TotalItems     int          `json:"totalItems"`
	ItemsTruncated bool         `json:"itemsTruncated"`
	Items          []DryRunItem `json:"items"`
	Error          string       `json:"error,omitempty"`
}

type dryRunSession struct {
	mu          sync.Mutex
	result      DryRunResult
	scannedDirs atomic.Int64
	cancel      context.CancelFunc
}

func (s *dryRunSession) snapshot() *DryRunResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := s.result
	cp.ScannedDirs = s.scannedDirs.Load()
	cp.Items = append([]DryRunItem(nil), s.result.Items...)
	if cp.Items == nil {
		cp.Items = []DryRunItem{}
	}
	return &cp
}

func (s *dryRunSession) addItem(item DryRunItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.result.TotalItems++
	size := item.FileSize
	if size < 0 {
		size = 0
	}
	switch item.Action {
	case "copy":
		s.result.CopyCount++
		s.result.CopySize += size
	case "update":
		s.result.UpdateCount++
		s.result.UpdateSize += size
	case "delete":
		s.result.DeleteCount++
		s.result.DeleteSize += size
	case "move":
		s.result.MoveCount++
		s.result.MoveSize += size
	case "mkdir":
		s.result.MkdirCount++
	case "skip":
		s.result.SkipCount++
	}
	if len(s.result.Items) < dryRunItemLimit {
		s.result.Items = append(s.result.Items, item)
	} else {
		s.result.ItemsTruncated = true
	}
}

func (s *dryRunSession) finish(status, errText string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.result.Status != "running" {
		return
	}
	s.result.Status = status
	s.result.Error = errText
	s.result.FinishedAt = time.Now().Unix()
	s.result.ScannedDirs = s.scannedDirs.Load()
}

var (
	dryRunMu       sync.Mutex
	dryRunSessions = make(map[int64]*dryRunSession)
)

// StartDryRun launches an asynchronous read-only preview scan for jobID.
// It never mutates any remote file or directory and creates no job_task rows.
func StartDryRun(jobID int64) (*DryRunResult, error) {
	job, err := mapper.GetJobByID(jobID)
	if err != nil {
		return nil, publicErrorIf(err, msg.T(msg.JobNotFound))
	}
	if err := CleanJobInput(job); err != nil {
		return nil, err
	}
	alistID := util.ToInt64(job["alistId"])
	cfg, err := loadNative(alistID)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, publicError(legacyOpenListJobDisabledMsg)
	}

	dryRunMu.Lock()
	if existing, ok := dryRunSessions[jobID]; ok {
		if existing.snapshot().Status == "running" {
			dryRunMu.Unlock()
			return nil, publicError(msg.T(msg.DryRunAlreadyRunning))
		}
	}
	// Bound stored finished sessions to avoid unbounded memory growth.
	if len(dryRunSessions) >= dryRunMaxTracked {
		var oldestID int64
		var oldestAt int64
		for id, sess := range dryRunSessions {
			snap := sess.snapshot()
			if snap.Status == "running" {
				continue
			}
			if oldestID == 0 || snap.StartedAt < oldestAt {
				oldestID = id
				oldestAt = snap.StartedAt
			}
		}
		if oldestID != 0 {
			delete(dryRunSessions, oldestID)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), dryRunRunTimeout)
	sess := &dryRunSession{
		result: DryRunResult{
			JobID:     jobID,
			Status:    "running",
			Method:    util.ToInt(job["method"]),
			StartedAt: time.Now().Unix(),
			Items:     make([]DryRunItem, 0, 64),
		},
		cancel: cancel,
	}
	dryRunSessions[jobID] = sess
	dryRunMu.Unlock()

	go func() {
		defer cancel()
		defer func() {
			if r := recover(); r != nil {
				sess.finish("failed", fmt.Sprintf("dry-run panic: %v", r))
			}
		}()
		runNativeDryRunSession(ctx, sess, cfg, job)
	}()
	return sess.snapshot(), nil
}

// GetDryRun returns the latest dry-run result snapshot for jobID.
func GetDryRun(jobID int64) (*DryRunResult, error) {
	dryRunMu.Lock()
	sess, ok := dryRunSessions[jobID]
	dryRunMu.Unlock()
	if !ok || sess == nil {
		return nil, publicError(msg.T(msg.DryRunNotFound))
	}
	return sess.snapshot(), nil
}

// CancelDryRun cancels an in-progress dry-run for jobID.
func CancelDryRun(jobID int64) error {
	dryRunMu.Lock()
	sess, ok := dryRunSessions[jobID]
	dryRunMu.Unlock()
	if !ok || sess == nil {
		return nil
	}
	if sess.cancel != nil {
		sess.cancel()
	}
	sess.finish("cancelled", "")
	return nil
}
