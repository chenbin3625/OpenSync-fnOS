package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"opensync/internal/mapper"
	"opensync/internal/msg"
	"opensync/internal/native"
	"opensync/pkg/util"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	pendingApprovalStatusPending   = "pending"
	pendingApprovalStatusExecuting = "executing"

	pendingExecPhaseVerifying = "verifying"
	pendingExecPhaseDeleting  = "deleting"

	pendingDeleteVerifyConcurrency = 6
	pendingDeleteDirConcurrency    = 4
	pendingDeleteBatchSize         = 100
	pendingApprovalMaxPageLimit    = 500
)

// pendingDeleteTarget describes a single verified destination file or directory
// whose removal was deferred by a full sync run until operator confirmation.
type pendingDeleteTarget struct {
	SrcRoot         string `json:"srcRoot"`
	DstRoot         string `json:"dstRoot"`
	SrcDir          string `json:"srcDir"`
	DstDir          string `json:"dstDir"`
	Name            string `json:"name"`
	IsDir           bool   `json:"isDir"`
	IsBlocker       bool   `json:"isBlocker,omitempty"`
	Size            int64  `json:"size"`
	Count           int    `json:"count"`
	Modified        int64  `json:"modified,omitempty"`
	SubtreeDigest   string `json:"subtreeDigest,omitempty"`
	SrcRootNonEmpty bool   `json:"srcRootNonEmpty,omitempty"`
}

func (t pendingDeleteTarget) objectPath() string {
	return fullSyncObjectPath(t.DstDir, t.Name)
}

type pendingDeleteRecord struct {
	JobID       int64                  `json:"jobId"`
	JobName     string                 `json:"jobName"`
	TaskID      int64                  `json:"taskId"`
	JobConfig   map[string]interface{} `json:"jobConfig"`
	Targets     []pendingDeleteTarget  `json:"targets"`
	Approval    FullSyncApproval       `json:"approval"`
	Status      string                 `json:"status"`
	CreatedAt   int64                  `json:"createdAt"`
	ExpiresAt   int64                  `json:"expiresAt"`
	PlanChanged bool                   `json:"planChanged,omitempty"`
	LastError   string                 `json:"lastError,omitempty"`

	// Runtime-only fields (never persisted to SQLite).
	expiresAtTime time.Time          `json:"-"`
	expiryTimer   *time.Timer        `json:"-"`
	execCancel    context.CancelFunc `json:"-"`
	execPhase     string             `json:"-"`
	execDone      int                `json:"-"`
	execTotal     int                `json:"-"`
	execFailed    int                `json:"-"`
	execCurrent   string             `json:"-"`
	execStartedAt int64              `json:"-"`
}

func (r *pendingDeleteRecord) approvalSnapshot() *FullSyncApproval {
	if r == nil {
		return nil
	}
	cp := r.Approval
	cp.JobID = r.JobID
	cp.JobName = r.JobName
	cp.TaskID = r.TaskID
	cp.Status = r.Status
	cp.CreatedAt = r.CreatedAt
	cp.ExpiresAt = r.ExpiresAt
	cp.PlanChanged = r.PlanChanged
	cp.LastError = r.LastError
	cp.ExecPhase = r.execPhase
	cp.ExecDone = r.execDone
	cp.ExecTotal = r.execTotal
	cp.ExecFailed = r.execFailed
	cp.ExecCurrent = r.execCurrent
	cp.ExecStartedAt = r.execStartedAt
	cp.Paths = append([]FullSyncApprovalPath(nil), r.Approval.Paths...)
	if cp.Paths == nil {
		cp.Paths = []FullSyncApprovalPath{}
	}
	cp.HasMore = cp.PathCount > len(cp.Paths)
	return &cp
}

func (r *pendingDeleteRecord) resetExecProgressLocked() {
	r.execPhase = ""
	r.execDone = 0
	r.execTotal = 0
	r.execFailed = 0
	r.execCurrent = ""
	r.execStartedAt = 0
}

type pendingDeleteManager struct {
	mu      sync.Mutex
	byJob   map[int64]*pendingDeleteRecord
	stopped bool
	wg      sync.WaitGroup
}

func newPendingDeleteManager() *pendingDeleteManager {
	return &pendingDeleteManager{
		byJob: make(map[int64]*pendingDeleteRecord),
	}
}

var pendingDeleteApprovals = newPendingDeleteManager()

// subtreeDigestEntry represents one non-excluded entry inside a directory
// scheduled for recursive deletion. Hashing the sorted entries detects any
// addition, removal, or modification between scan time and approval time.
type subtreeDigestEntry struct {
	relPath  string
	isDir    bool
	size     int64
	modified int64
}

func summarizeSubtreeEntries(entries []subtreeDigestEntry) (count int, size int64, digest string) {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].relPath < entries[j].relPath
	})
	count = 1 + len(entries)
	var b strings.Builder
	for _, entry := range entries {
		if !entry.isDir && entry.size > 0 {
			size += entry.size
		}
		fmt.Fprintf(&b, "%s\x00%t\x00%d\x00%d\n", entry.relPath, entry.isDir, entry.size, entry.modified)
	}
	sum := sha256.Sum256([]byte(b.String()))
	digest = fmt.Sprintf("%x", sum[:])
	return count, size, digest
}

func sortPendingDeleteTargets(targets []pendingDeleteTarget) {
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].IsBlocker != targets[j].IsBlocker {
			return targets[i].IsBlocker
		}
		if targets[i].DstRoot != targets[j].DstRoot {
			return targets[i].DstRoot < targets[j].DstRoot
		}
		pi, pj := targets[i].objectPath(), targets[j].objectPath()
		if pi != pj {
			return pi < pj
		}
		return targets[i].Name < targets[j].Name
	})
}

func buildApprovalFromTargets(jobID int64, jobName string, taskID int64, targets []pendingDeleteTarget) FullSyncApproval {
	sortPendingDeleteTargets(targets)
	approval := FullSyncApproval{
		JobID:   jobID,
		JobName: jobName,
		TaskID:  taskID,
		Paths:   make([]FullSyncApprovalPath, 0, min(len(targets), fullSyncApprovalPathLimit)),
	}
	sigParts := make([]string, 0, len(targets))
	for _, target := range targets {
		count := target.Count
		if count < 1 {
			count = 1
		}
		size := target.Size
		if size < 0 {
			size = 0
		}
		itemPath := target.objectPath()
		approval.PathCount++
		approval.DeleteCount += count
		approval.DeleteSize += size
		sigParts = append(sigParts, fmt.Sprintf("删除\x00%s\x00%d\x00%d\x00%d\x00%s", itemPath, count, size, target.Modified, target.SubtreeDigest))
		if len(approval.Paths) < fullSyncApprovalPathLimit {
			approval.Paths = append(approval.Paths, FullSyncApprovalPath{
				Path:   itemPath,
				IsPath: target.IsDir,
				Count:  count,
				Size:   size,
				Action: "删除",
			})
		}
	}
	sort.Strings(sigParts)
	sum := sha256.Sum256([]byte(strings.Join(sigParts, "\x00")))
	approval.signature = fmt.Sprintf("%x", sum[:])
	return approval
}

func approvalJobName(jobID int64, job map[string]interface{}) string {
	if job != nil {
		if name := strings.TrimSpace(util.StringValue(job["remark"])); name != "" {
			return name
		}
	}
	if jobID > 0 {
		return fmt.Sprintf("任务 #%d", jobID)
	}
	return "全量同步任务"
}

func (m *pendingDeleteManager) isExpiredLocked(rec *pendingDeleteRecord, now time.Time) bool {
	if rec == nil || rec.Status == pendingApprovalStatusExecuting {
		return false
	}
	if !rec.expiresAtTime.IsZero() {
		return !now.Before(rec.expiresAtTime)
	}
	if rec.ExpiresAt > 0 {
		return now.Unix() >= rec.ExpiresAt
	}
	return false
}

func (m *pendingDeleteManager) purgeExpiredLocked(jobID int64, now time.Time) bool {
	rec := m.byJob[jobID]
	if rec == nil || !m.isExpiredLocked(rec, now) {
		return false
	}
	if rec.expiryTimer != nil {
		rec.expiryTimer.Stop()
		rec.expiryTimer = nil
	}
	delete(m.byJob, jobID)
	if err := mapper.DeletePendingDeleteApproval(jobID); err != nil {
		log.Printf("Failed to delete expired pending approval for job %d: %v", jobID, err)
	}
	log.Printf("Pending deletion approval for job %d (task %d) expired; destination files kept untouched", jobID, rec.TaskID)
	return true
}

func (m *pendingDeleteManager) armExpiryTimerLocked(rec *pendingDeleteRecord, waitDur time.Duration) {
	if rec.expiryTimer != nil {
		rec.expiryTimer.Stop()
		rec.expiryTimer = nil
	}
	if waitDur <= 0 || m.stopped {
		rec.ExpiresAt = 0
		rec.expiresAtTime = time.Time{}
		return
	}
	deadline := time.Now().Add(waitDur)
	rec.expiresAtTime = deadline
	rec.ExpiresAt = deadline.Unix()
	if rec.ExpiresAt <= rec.CreatedAt {
		rec.ExpiresAt = rec.CreatedAt + 1
	}
	jobID := rec.JobID
	rec.expiryTimer = time.AfterFunc(waitDur, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.stopped {
			return
		}
		current := m.byJob[jobID]
		if current != rec || current.Status != pendingApprovalStatusPending {
			return
		}
		current.expiryTimer = nil
		delete(m.byJob, jobID)
		if err := mapper.DeletePendingDeleteApproval(jobID); err != nil {
			log.Printf("Failed to remove expired pending approval for job %d: %v", jobID, err)
		}
		log.Printf("Pending deletion approval for job %d (task %d) expired after %s; destination files kept untouched", jobID, rec.TaskID, waitDur)
	})
}

func persistPendingDeleteRecord(rec *pendingDeleteRecord) {
	if rec == nil || rec.JobID <= 0 {
		return
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		log.Printf("Failed to marshal pending delete approval for job %d: %v", rec.JobID, err)
		return
	}
	if err := mapper.UpsertPendingDeleteApproval(rec.JobID, rec.TaskID, string(raw), rec.CreatedAt, rec.ExpiresAt); err != nil {
		log.Printf("Failed to persist pending delete approval for job %d: %v", rec.JobID, err)
	}
}

// Stage stores (or updates) a post-sync pending deletion approval for jt without
// blocking the sync task. If an identical pending approval is already staged
// for the job, the duplicate notification is suppressed.
func (m *pendingDeleteManager) Stage(jt *JobTask, targets []pendingDeleteTarget) {
	if jt == nil || len(targets) == 0 {
		return
	}
	jobID := jt.approvalJobID()
	jobSnap := cloneJobConfig(jt.Job)
	jobName := approvalJobName(jobID, jobSnap)
	now := time.Now()
	approval := buildApprovalFromTargets(jobID, jobName, jt.TaskID, targets)
	waitDur := effectiveFullSyncApprovalWait()

	rec := &pendingDeleteRecord{
		JobID:     jobID,
		JobName:   jobName,
		TaskID:    jt.TaskID,
		JobConfig: jobSnap,
		Targets:   append([]pendingDeleteTarget(nil), targets...),
		Approval:  approval,
		Status:    pendingApprovalStatusPending,
		CreatedAt: now.Unix(),
	}

	shouldNotify := true
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	if prev := m.byJob[jobID]; prev != nil {
		if prev.Status == pendingApprovalStatusExecuting {
			// A previously approved delete batch is actively executing right
			// now; do not clobber it. The next sync will pick up any remaining
			// extra entries if needed.
			m.mu.Unlock()
			return
		}
		if prev.expiryTimer != nil {
			prev.expiryTimer.Stop()
			prev.expiryTimer = nil
		}
		if !m.isExpiredLocked(prev, now) && sameFullSyncApproval(&prev.Approval, &approval) {
			shouldNotify = false
		}
	}
	m.armExpiryTimerLocked(rec, waitDur)
	m.byJob[jobID] = rec
	persistPendingDeleteRecord(rec)
	snap := rec.approvalSnapshot()
	m.mu.Unlock()

	if shouldNotify && snap != nil && jt.TaskID > 0 {
		QueueApprovalNotification(jt.TaskID, jobID, jobSnap, snap)
	}
}

// ClearIfPending removes a pending approval for jobID only when it is not
// currently executing (e.g. a subsequent clean sync found nothing left to delete).
func (m *pendingDeleteManager) ClearIfPending(jobID int64) {
	if jobID <= 0 {
		return
	}
	m.mu.Lock()
	rec := m.byJob[jobID]
	if rec == nil || rec.Status == pendingApprovalStatusExecuting {
		m.mu.Unlock()
		return
	}
	if rec.expiryTimer != nil {
		rec.expiryTimer.Stop()
		rec.expiryTimer = nil
	}
	delete(m.byJob, jobID)
	m.mu.Unlock()
	if err := mapper.DeletePendingDeleteApproval(jobID); err != nil {
		log.Printf("Failed to clear pending delete approval for job %d: %v", jobID, err)
	}
}

// ClearForJob cancels any executing deletion worker for jobID and removes any
// pending deletion approval from memory and SQLite.
func (m *pendingDeleteManager) ClearForJob(jobID int64) {
	m.mu.Lock()
	rec := m.byJob[jobID]
	var cancel context.CancelFunc
	if rec != nil {
		if rec.expiryTimer != nil {
			rec.expiryTimer.Stop()
			rec.expiryTimer = nil
		}
		cancel = rec.execCancel
		delete(m.byJob, jobID)
	}
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if jobID > 0 {
		if err := mapper.DeletePendingDeleteApproval(jobID); err != nil {
			log.Printf("Failed to delete pending approval for job %d: %v", jobID, err)
		}
	}
}

// LoadFromDB restores persisted pending deletion approvals at startup, dropping
// expired records or records whose owning job no longer exists.
func (m *pendingDeleteManager) LoadFromDB() {
	rows, err := mapper.ListPendingDeleteApprovals()
	if err != nil {
		log.Printf("Failed to load pending delete approvals: %v", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	existingJobs := make(map[int64]bool)
	if jobs, err := mapper.GetJobListAll(); err == nil {
		for _, j := range jobs {
			existingJobs[util.ToInt64(j["id"])] = true
		}
	}
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped = false
	for _, row := range rows {
		jobID := util.ToInt64(row["jobId"])
		payload := util.StringValue(row["payload"])
		if jobID <= 0 || payload == "" || (len(existingJobs) > 0 && !existingJobs[jobID]) {
			_ = mapper.DeletePendingDeleteApproval(jobID)
			continue
		}
		var rec pendingDeleteRecord
		if err := json.Unmarshal([]byte(payload), &rec); err != nil {
			log.Printf("Dropping unreadable pending delete approval for job %d: %v", jobID, err)
			_ = mapper.DeletePendingDeleteApproval(jobID)
			continue
		}
		rec.JobID = jobID
		rec.Status = pendingApprovalStatusPending
		if len(rec.Targets) == 0 {
			_ = mapper.DeletePendingDeleteApproval(jobID)
			continue
		}
		rec.Approval = buildApprovalFromTargets(rec.JobID, rec.JobName, rec.TaskID, rec.Targets)
		if rec.ExpiresAt > 0 {
			remaining := time.Until(time.Unix(rec.ExpiresAt, 0))
			if remaining <= 0 {
				_ = mapper.DeletePendingDeleteApproval(jobID)
				continue
			}
			m.armExpiryTimerLocked(&rec, remaining)
		}
		if rec.CreatedAt <= 0 {
			rec.CreatedAt = now.Unix()
		}
		m.byJob[jobID] = &rec
	}
}

func (m *pendingDeleteManager) GetByJobID(jobID int64) *FullSyncApproval {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeExpiredLocked(jobID, now)
	return m.byJob[jobID].approvalSnapshot()
}

// GetByJobIDFiltered returns a job's pending approval with filtered and
// paginated paths sourced from the complete target list.
func (m *pendingDeleteManager) GetByJobIDFiltered(jobID int64, offset, limit int, query, kind string) *FullSyncApproval {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeExpiredLocked(jobID, now)
	rec := m.byJob[jobID]
	if rec == nil {
		return nil
	}
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = fullSyncApprovalPathLimit
	}
	if limit > pendingApprovalMaxPageLimit {
		limit = pendingApprovalMaxPageLimit
	}
	qLower := strings.ToLower(strings.TrimSpace(query))
	kind = strings.ToLower(strings.TrimSpace(kind))

	snap := rec.approvalSnapshot()
	matched := make([]pendingDeleteTarget, 0, len(rec.Targets))
	for _, target := range rec.Targets {
		switch kind {
		case "dir":
			if !target.IsDir {
				continue
			}
		case "file":
			if target.IsDir {
				continue
			}
		}
		if qLower != "" && !strings.Contains(strings.ToLower(target.objectPath()), qLower) {
			continue
		}
		matched = append(matched, target)
	}
	snap.FilteredCount = len(matched)
	if offset >= len(matched) {
		snap.Paths = []FullSyncApprovalPath{}
		snap.HasMore = false
		return snap
	}
	end := min(len(matched), offset+limit)
	slice := matched[offset:end]
	paths := make([]FullSyncApprovalPath, 0, len(slice))
	for _, target := range slice {
		count := target.Count
		if count < 1 {
			count = 1
		}
		size := target.Size
		if size < 0 {
			size = 0
		}
		paths = append(paths, FullSyncApprovalPath{
			Path:   target.objectPath(),
			IsPath: target.IsDir,
			Count:  count,
			Size:   size,
			Action: "删除",
		})
	}
	snap.Paths = paths
	snap.HasMore = end < len(matched)
	return snap
}

func (m *pendingDeleteManager) setExecProgress(rec *pendingDeleteRecord, phase string, done, total, failed int, current string) {
	m.mu.Lock()
	if m.byJob[rec.JobID] == rec {
		if phase != "" {
			rec.execPhase = phase
		}
		rec.execDone = done
		rec.execTotal = total
		rec.execFailed = failed
		rec.execCurrent = current
	}
	m.mu.Unlock()
}

func (m *pendingDeleteManager) GetByTaskID(taskID int64) *FullSyncApproval {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	for jobID, rec := range m.byJob {
		if m.purgeExpiredLocked(jobID, now) {
			continue
		}
		if rec.TaskID == taskID {
			return rec.approvalSnapshot()
		}
	}
	return nil
}

func (m *pendingDeleteManager) Has(jobID int64) bool {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.purgeExpiredLocked(jobID, now)
	return m.byJob[jobID] != nil
}

func (m *pendingDeleteManager) ListAll() []*FullSyncApproval {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]*FullSyncApproval, 0, len(m.byJob))
	for jobID := range m.byJob {
		if m.purgeExpiredLocked(jobID, now) {
			continue
		}
		if snap := m.byJob[jobID].approvalSnapshot(); snap != nil {
			result = append(result, snap)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt != result[j].CreatedAt {
			return result[i].CreatedAt > result[j].CreatedAt
		}
		return result[i].JobID < result[j].JobID
	})
	return result
}

func (m *pendingDeleteManager) findLockedByTaskID(taskID int64, now time.Time) (*pendingDeleteRecord, error) {
	for jobID, rec := range m.byJob {
		if m.purgeExpiredLocked(jobID, now) {
			continue
		}
		if rec.TaskID == taskID {
			return rec, nil
		}
	}
	return nil, publicError(msg.T(msg.FullSyncPending))
}

func (m *pendingDeleteManager) findLockedByJobID(jobID int64, now time.Time) (*pendingDeleteRecord, error) {
	if m.purgeExpiredLocked(jobID, now) {
		return nil, publicError(msg.T(msg.FullSyncPending))
	}
	rec := m.byJob[jobID]
	if rec == nil {
		return nil, publicError(msg.T(msg.FullSyncPending))
	}
	return rec, nil
}

func (m *pendingDeleteManager) ApproveByTaskID(taskID int64) error {
	return m.startExecution(func(now time.Time) (*pendingDeleteRecord, error) {
		return m.findLockedByTaskID(taskID, now)
	}, nil)
}

// ApproveByTaskIDPaths approves only the explicitly selected destination paths
// (leaving unselected pending items untouched on destination).
func (m *pendingDeleteManager) ApproveByTaskIDPaths(taskID int64, selectedPaths []string) error {
	selectedSet := make(map[string]struct{}, len(selectedPaths))
	for _, p := range selectedPaths {
		if cleaned := strings.TrimSpace(p); cleaned != "" {
			selectedSet[cleaned] = struct{}{}
		}
	}
	if len(selectedSet) == 0 {
		return publicError("请至少选择一个要删除的项目")
	}
	return m.startExecution(func(now time.Time) (*pendingDeleteRecord, error) {
		return m.findLockedByTaskID(taskID, now)
	}, selectedSet)
}

func (m *pendingDeleteManager) ApproveByJobID(jobID int64) error {
	return m.startExecution(func(now time.Time) (*pendingDeleteRecord, error) {
		return m.findLockedByJobID(jobID, now)
	}, nil)
}

// ExcludeByTaskIDPaths appends exclusion rules for the selected pending-delete
// destination paths to the job configuration and removes them from the pending
// approval list so future mirror runs will leave those entries untouched.
func (m *pendingDeleteManager) ExcludeByTaskIDPaths(taskID int64, selectedPaths []string) (int, error) {
	if len(selectedPaths) == 0 {
		return 0, publicError("请至少选择一项要加入排除规则的路径")
	}
	selectedSet := make(map[string]struct{}, len(selectedPaths))
	for _, p := range selectedPaths {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			selectedSet[trimmed] = struct{}{}
		}
	}
	now := time.Now()
	m.mu.Lock()
	rec, err := m.findLockedByTaskID(taskID, now)
	if err != nil {
		m.mu.Unlock()
		return 0, err
	}
	if rec.Status == pendingApprovalStatusExecuting {
		m.mu.Unlock()
		return 0, publicError(msg.T(msg.FullSyncApprovalExecuting))
	}
	jobID := rec.JobID
	var rulesToAdd []string
	var remaining []pendingDeleteTarget
	for _, t := range rec.Targets {
		objPath := t.objectPath()
		if _, chosen := selectedSet[objPath]; chosen {
			rel := strings.TrimPrefix(objPath, normalizeDirPath(t.DstRoot))
			rel = strings.TrimPrefix(rel, "/")
			if rel != "" {
				if t.IsDir && !strings.HasSuffix(rel, "/") {
					rel += "/"
				}
				rulesToAdd = append(rulesToAdd, rel)
			}
			continue
		}
		remaining = append(remaining, t)
	}
	m.mu.Unlock()

	if len(rulesToAdd) == 0 {
		return 0, publicError("未匹配到可加入排除规则的待删除项")
	}

	job, err := mapper.GetJobByID(jobID)
	if err != nil {
		return 0, publicErrorIf(err, msg.T(msg.JobNotFound))
	}
	existing := strings.TrimSpace(util.StringValue(job["exclude"]))
	existingLines := make(map[string]struct{})
	var mergedLines []string
	for _, line := range strings.Split(existing, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		existingLines[line] = struct{}{}
		mergedLines = append(mergedLines, line)
	}
	added := 0
	for _, rule := range rulesToAdd {
		if _, dup := existingLines[rule]; dup {
			continue
		}
		existingLines[rule] = struct{}{}
		mergedLines = append(mergedLines, rule)
		added++
	}
	if added > 0 {
		job["exclude"] = strings.Join(mergedLines, "\n")
		if err := EditJobClient(job); err != nil {
			return 0, err
		}
	}

	// Update or clear the pending deletion approval with the remaining unselected targets.
	if len(remaining) == 0 {
		m.ClearForJob(jobID)
	} else {
		m.mu.Lock()
		if !m.stopped {
			jobSnap := cloneJobConfig(job)
			jobName := approvalJobName(jobID, jobSnap)
			nextRec := &pendingDeleteRecord{
				JobID:     jobID,
				JobName:   jobName,
				TaskID:    taskID,
				JobConfig: jobSnap,
				Targets:   remaining,
				Approval:  buildApprovalFromTargets(jobID, jobName, taskID, remaining),
				Status:    pendingApprovalStatusPending,
				CreatedAt: now.Unix(),
			}
			m.armExpiryTimerLocked(nextRec, effectiveFullSyncApprovalWait())
			m.byJob[jobID] = nextRec
			persistPendingDeleteRecord(nextRec)
		}
		m.mu.Unlock()
	}
	return len(rulesToAdd), nil
}

func (m *pendingDeleteManager) startExecution(lookup func(time.Time) (*pendingDeleteRecord, error), selectedPaths map[string]struct{}) error {
	now := time.Now()
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return publicError(msg.T(msg.FullSyncPending))
	}
	rec, err := lookup(now)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if rec.Status == pendingApprovalStatusExecuting {
		m.mu.Unlock()
		return publicError(msg.T(msg.FullSyncApprovalExecuting))
	}
	if len(selectedPaths) > 0 {
		filtered := make([]pendingDeleteTarget, 0, len(selectedPaths))
		for _, t := range rec.Targets {
			if _, ok := selectedPaths[t.objectPath()]; ok {
				filtered = append(filtered, t)
			}
		}
		if len(filtered) == 0 {
			m.mu.Unlock()
			return publicError("未匹配到有效的待删除项")
		}
		rec.Targets = filtered
		rec.Approval = buildApprovalFromTargets(rec.JobID, rec.JobName, rec.TaskID, rec.Targets)
	}
	if rec.expiryTimer != nil {
		rec.expiryTimer.Stop()
		rec.expiryTimer = nil
	}
	execCtx, cancel := context.WithCancel(context.Background())
	rec.Status = pendingApprovalStatusExecuting
	rec.PlanChanged = false
	rec.LastError = ""
	rec.execCancel = cancel
	rec.execPhase = pendingExecPhaseVerifying
	rec.execDone = 0
	rec.execTotal = len(rec.Targets)
	rec.execFailed = 0
	rec.execCurrent = ""
	rec.execStartedAt = now.Unix()
	m.wg.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.wg.Done()
		defer cancel()
		m.executeRecord(execCtx, rec)
	}()
	return nil
}

func (m *pendingDeleteManager) RejectByTaskID(taskID int64) error {
	return m.rejectWith(func(now time.Time) (*pendingDeleteRecord, error) {
		return m.findLockedByTaskID(taskID, now)
	})
}

func (m *pendingDeleteManager) RejectByJobID(jobID int64) error {
	return m.rejectWith(func(now time.Time) (*pendingDeleteRecord, error) {
		return m.findLockedByJobID(jobID, now)
	})
}

func (m *pendingDeleteManager) rejectWith(lookup func(time.Time) (*pendingDeleteRecord, error)) error {
	now := time.Now()
	m.mu.Lock()
	rec, err := lookup(now)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if rec.Status == pendingApprovalStatusExecuting {
		m.mu.Unlock()
		return publicError(msg.T(msg.FullSyncApprovalExecuting))
	}
	if rec.expiryTimer != nil {
		rec.expiryTimer.Stop()
		rec.expiryTimer = nil
	}
	jobID := rec.JobID
	taskID := rec.TaskID
	delete(m.byJob, jobID)
	m.mu.Unlock()

	if err := mapper.DeletePendingDeleteApproval(jobID); err != nil {
		log.Printf("Failed to remove rejected pending delete approval for job %d: %v", jobID, err)
	}
	log.Printf("Pending deletion approval for job %d (task %d) skipped by user; destination files kept untouched", jobID, taskID)
	return nil
}

// Shutdown stops expiry timers, cancels any in-flight deletion executions, and
// waits for them within ctx. Unapproved pending approvals remain persisted in
// SQLite so they survive restarts.
func (m *pendingDeleteManager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	m.stopped = true
	var cancels []context.CancelFunc
	for _, rec := range m.byJob {
		if rec.expiryTimer != nil {
			rec.expiryTimer.Stop()
			rec.expiryTimer = nil
		}
		if rec.execCancel != nil {
			cancels = append(cancels, rec.execCancel)
		}
	}
	m.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}

	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// ResetForTest clears in-memory state and re-enables the manager for tests.
func (m *pendingDeleteManager) ResetForTest() {
	m.mu.Lock()
	var cancels []context.CancelFunc
	for _, rec := range m.byJob {
		if rec.expiryTimer != nil {
			rec.expiryTimer.Stop()
			rec.expiryTimer = nil
		}
		if rec.execCancel != nil {
			cancels = append(cancels, rec.execCancel)
		}
	}
	m.byJob = make(map[int64]*pendingDeleteRecord)
	m.stopped = false
	m.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	m.wg.Wait()
}

// executeRecord re-verifies every approved delete target against live source
// and destination listings before performing any deletion. If verification
// fails or an approved target changed, nothing is deleted and the approval
// returns to "pending" with updated details so the user can re-confirm.
func (m *pendingDeleteManager) executeRecord(ctx context.Context, rec *pendingDeleteRecord) {
	defer func() {
		if r := recover(); r != nil {
			errMsg := fmt.Sprintf("pending delete worker panic: %v", r)
			log.Printf("Job %d (task %d) %s", rec.JobID, rec.TaskID, errMsg)
			m.returnToPending(rec, rec.Targets, false, errMsg)
		}
	}()

	jobCfg := m.liveJobConfig(rec)
	alistID := util.ToInt64(jobCfg["alistId"])
	if alistID <= 0 {
		id, err := EnsureNativeEngine()
		if err != nil {
			m.returnToPending(rec, rec.Targets, false, err.Error())
			return
		}
		alistID = id
	}
	nativeCfg, nativeErr := loadNative(alistID)
	if nativeErr != nil {
		if ctx.Err() != nil {
			m.restorePendingAfterCancel(rec)
			return
		}
		m.returnToPending(rec, rec.Targets, false, nativeErr.Error())
		return
	}
	if nativeCfg == nil {
		m.returnToPending(rec, rec.Targets, false, legacyOpenListJobDisabledMsg)
		return
	}
	m.executeNativeRecord(ctx, rec, jobCfg, nativeCfg)
}

func (m *pendingDeleteManager) liveJobConfig(rec *pendingDeleteRecord) map[string]interface{} {
	cfg := cloneJobConfig(rec.JobConfig)
	if cfg == nil {
		cfg = map[string]interface{}{}
	}
	if rec.JobID <= 0 {
		return cfg
	}
	jobClientListMu.RLock()
	client := jobClientList[rec.JobID]
	jobClientListMu.RUnlock()
	if client == nil {
		return cfg
	}
	live := client.jobSnapshot()
	if live == nil {
		return cfg
	}
	// Honor updated trash / throttle settings if the user adjusted them before
	// confirming deletion.
	for _, key := range []string{"trashEnable", "trashDir", "trashRetentionDays", "scanIntervalS", "scanIntervalT", "remark"} {
		if v, ok := live[key]; ok {
			cfg[key] = v
		}
	}
	return cfg
}

func nativeSubtreeListToFileLists(ctx context.Context, cfg native.Config, dir string, fastList bool) (map[string]FileListResult, bool, error) {
	dirPath := normalizeDirPath(dir)
	entries, exists, err := cfg.ListTree(ctx, strings.TrimRight(dirPath, "/"), native.TreeOptions{
		FastList: fastList,
		ProxyURL: globalNativeProxyURL(),
	})
	if err != nil {
		return nil, false, err
	}
	if !exists {
		return nil, true, nil
	}
	dirs := map[string]FileListResult{
		dirPath: make(FileListResult),
	}
	ensureDir := func(d string) FileListResult {
		m, ok := dirs[d]
		if !ok {
			m = make(FileListResult)
			dirs[d] = m
		}
		return m
	}
	for _, e := range entries {
		rel := strings.Trim(path.Clean("/"+e.RelPath), "/")
		if rel == "" || rel == "." || strings.HasPrefix(rel, "../") {
			continue
		}
		parent := path.Dir(rel)
		relDir := ""
		if parent != "." && parent != "/" {
			relDir = parent + "/"
		}
		// Synthesize intermediate directory entries for flat object stores
		// (S3/OSS/B2) that do not emit explicit directory markers.
		if relDir != "" {
			cur := dirPath
			for _, seg := range strings.Split(strings.TrimSuffix(relDir, "/"), "/") {
				parentMap := ensureDir(cur)
				segKey := seg + "/"
				if _, ok := parentMap[segKey]; !ok {
					parentMap[segKey] = FileMetadata{}
				}
				cur += segKey
				ensureDir(cur)
			}
		}
		parentMap := ensureDir(dirPath + relDir)
		meta := FileMetadata{
			Size:     e.Size,
			Modified: e.ModTime,
			MD5:      e.MD5,
		}
		if e.IsDir {
			key := e.Name + "/"
			parentMap[key] = meta
			ensureDir(dirPath + relDir + key)
		} else {
			parentMap[e.Name] = meta
		}
	}
	return dirs, false, nil
}

func (m *pendingDeleteManager) executeNativeRecord(ctx context.Context, rec *pendingDeleteRecord, jobCfg map[string]interface{}, nativeCfg native.Config) {
	fastList := util.ToInt(jobCfg["fastList"]) == 1
	listNative := func(dir string) (FileListResult, bool, error) {
		return nativeDirListToFileList(ctx, nativeCfg, dir)
	}
	listSubtree := func(dir string) (map[string]FileListResult, bool, error) {
		return nativeSubtreeListToFileLists(ctx, nativeCfg, dir, fastList)
	}
	m.setExecProgress(rec, pendingExecPhaseVerifying, 0, len(rec.Targets), 0, "")
	toDelete, refreshedTargets, changed, verifyErr := verifyPendingDeleteTargetsWithOptions(ctx, jobCfg, rec.Targets, pendingDeleteVerifyOptions{
		listSrc:        listNative,
		listDst:        listNative,
		listDstSubtree: listSubtree,
		concurrency:    pendingDeleteVerifyConcurrency,
		onProgress: func(done, total int, current string) {
			m.setExecProgress(rec, pendingExecPhaseVerifying, done, total, 0, current)
		},
	})
	if ctx.Err() != nil {
		m.restorePendingAfterCancel(rec)
		return
	}
	if verifyErr != nil {
		log.Printf("Job %d (task %d) native pending delete verification aborted: %v", rec.JobID, rec.TaskID, verifyErr)
		m.returnToPending(rec, rec.Targets, false, verifyErr.Error())
		return
	}
	if changed {
		if len(refreshedTargets) == 0 {
			m.completeRecord(rec)
			return
		}
		log.Printf("Job %d (task %d) native pending delete target(s) changed since scan; re-prompting with %d updated path(s)", rec.JobID, rec.TaskID, len(refreshedTargets))
		m.returnToPending(rec, refreshedTargets, true, "")
		return
	}
	if len(toDelete) == 0 {
		m.completeRecord(rec)
		return
	}

	delCtx, delCancel := context.WithCancel(ctx)
	delRunner := &JobTask{
		TaskID:       rec.TaskID,
		Job:          jobCfg,
		nativeConfig: nativeCfg,
		ctx:          delCtx,
		cancel:       delCancel,
	}
	delRunner.initRuntime()
	defer delRunner.cancelTaskRuntime()

	totalCount := len(toDelete)
	m.setExecProgress(rec, pendingExecPhaseDeleting, 0, totalCount, 0, "")

	type batchFileTarget struct {
		target pendingDeleteTarget
		rel    string
	}
	rootOrder := make([]string, 0, 2)
	fileGroups := make(map[string][]batchFileTarget)
	individualTargets := make([]pendingDeleteTarget, 0)
	for _, target := range toDelete {
		if !target.IsDir && isSafeDeleteTargetName(target.Name) {
			dstRootNorm := normalizeDirPath(target.DstRoot)
			itemPath := strings.TrimRight(normalizeDirPath(target.DstDir), "/") + "/" + strings.TrimSuffix(target.Name, "/")
			if dstRootNorm != "" && strings.HasPrefix(itemPath, dstRootNorm) {
				rel := strings.TrimPrefix(itemPath, dstRootNorm)
				rel = strings.TrimPrefix(rel, "/")
				if rel != "" && rel != "." {
					if _, exists := fileGroups[dstRootNorm]; !exists {
						rootOrder = append(rootOrder, dstRootNorm)
					}
					fileGroups[dstRootNorm] = append(fileGroups[dstRootNorm], batchFileTarget{
						target: target,
						rel:    rel,
					})
					continue
				}
			}
		}
		individualTargets = append(individualTargets, target)
	}

	sessOpts := native.SessionOptions{ProxyURL: globalNativeProxyURL()}
	useTrash := trashEnabled(jobCfg)
	trashDir := ""
	trashDateBucket := ""
	trashVersionBucket := ""
	if useTrash {
		trashDir = jobTrashDir(jobCfg)
		nowLoc := time.Now().In(schedulerLocation())
		trashDateBucket = nowLoc.Format("2006-01-02")
		trashVersionBucket = delRunner.nextTrashVersionBucket(nowLoc, trashDateBucket, "", "pending-delete-batch")
	}

	var execMu sync.Mutex
	consecutiveFails := 0
	doneCount := 0
	failedCount := 0
	const failAbortMsg = "连续删除失败达到上限，已中止剩余删除操作"

	// Phase 1: Batch-delete or batch-trash regular files in bounded chunks per root.
	for _, dstRoot := range rootOrder {
		if delCtx.Err() != nil {
			break
		}
		group := fileGroups[dstRoot]
		dstRootTrim := strings.TrimRight(dstRoot, "/")
		trashBucketVirtual := ""
		if useTrash {
			trashBucketVirtual = dstRootTrim + "/" + strings.TrimPrefix(path.Join(trashDir, trashDateBucket, trashVersionBucket), "/")
		}
		for start := 0; start < len(group); start += pendingDeleteBatchSize {
			if delCtx.Err() != nil {
				break
			}
			end := min(len(group), start+pendingDeleteBatchSize)
			chunk := group[start:end]

			if consecutiveFails >= 5 {
				errMsg := failAbortMsg
				for _, item := range chunk {
					delRunner.DelHook(item.target.DstDir, item.target.Name, nil, taskStatusFailed, &errMsg, taskItemFile, time.Now().Unix())
					doneCount++
					failedCount++
				}
				m.setExecProgress(rec, pendingExecPhaseDeleting, doneCount, totalCount, failedCount, chunk[len(chunk)-1].target.objectPath())
				continue
			}

			rels := make([]string, len(chunk))
			for i, item := range chunk {
				rels[i] = item.rel
			}
			m.setExecProgress(rec, pendingExecPhaseDeleting, doneCount, totalCount, failedCount, chunk[0].target.objectPath())

			var (
				errMap   map[string]error
				batchErr error
			)
			if useTrash {
				errMap, batchErr = nativeCfg.MoveFilesBatch(delCtx, dstRoot, trashBucketVirtual, rels, sessOpts)
			} else {
				errMap, batchErr = nativeCfg.DeleteFilesBatch(delCtx, dstRoot, rels, sessOpts)
			}
			if delCtx.Err() != nil {
				break
			}

			nowUnix := time.Now().Unix()
			for _, item := range chunk {
				opErr := batchErr
				if opErr == nil && errMap != nil {
					opErr = errMap[item.rel]
				}
				if opErr != nil {
					consecutiveFails++
					failedCount++
					s := opErr.Error()
					delRunner.DelHook(item.target.DstDir, item.target.Name, nil, taskStatusFailed, &s, taskItemFile, nowUnix)
				} else {
					consecutiveFails = 0
					delRunner.DelHook(item.target.DstDir, item.target.Name, item.target.Size, taskStatusSuccess, nil, taskItemFile, nowUnix)
				}
				doneCount++
			}
			m.setExecProgress(rec, pendingExecPhaseDeleting, doneCount, totalCount, failedCount, chunk[len(chunk)-1].target.objectPath())
		}
	}

	// Phase 2: Delete or trash directory targets (and any fallback items) with
	// bounded concurrency while honoring the consecutive failure circuit breaker.
	if len(individualTargets) > 0 && delCtx.Err() == nil {
		workers := min(pendingDeleteDirConcurrency, len(individualTargets))
		jobsCh := make(chan pendingDeleteTarget, len(individualTargets))
		for _, target := range individualTargets {
			jobsCh <- target
		}
		close(jobsCh)

		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for target := range jobsCh {
					if delCtx.Err() != nil {
						return
					}
					execMu.Lock()
					aborted := consecutiveFails >= 5
					execMu.Unlock()
					if aborted {
						errMsg := failAbortMsg
						delRunner.DelHook(target.DstDir, target.Name, nil, taskStatusFailed, &errMsg, boolToTaskItemObject(target.IsDir), time.Now().Unix())
						execMu.Lock()
						doneCount++
						failedCount++
						curDone, curFail := doneCount, failedCount
						execMu.Unlock()
						m.setExecProgress(rec, pendingExecPhaseDeleting, curDone, totalCount, curFail, target.objectPath())
						continue
					}

					execMu.Lock()
					curDone, curFail := doneCount, failedCount
					execMu.Unlock()
					m.setExecProgress(rec, pendingExecPhaseDeleting, curDone, totalCount, curFail, target.objectPath())

					err := nativeDeleteOrTrash(delCtx, nativeCfg, delRunner, jobCfg, target.DstRoot, target.DstDir, target.Name, target.IsDir)
					if delCtx.Err() != nil {
						return
					}
					if err != nil {
						s := err.Error()
						delRunner.DelHook(target.DstDir, target.Name, nil, taskStatusFailed, &s, boolToTaskItemObject(target.IsDir), time.Now().Unix())
						execMu.Lock()
						consecutiveFails++
						failedCount++
						doneCount++
						curDone, curFail = doneCount, failedCount
						execMu.Unlock()
					} else {
						delRunner.DelHook(target.DstDir, target.Name, target.Size, taskStatusSuccess, nil, boolToTaskItemObject(target.IsDir), time.Now().Unix())
						execMu.Lock()
						consecutiveFails = 0
						doneCount++
						curDone, curFail = doneCount, failedCount
						execMu.Unlock()
					}
					m.setExecProgress(rec, pendingExecPhaseDeleting, curDone, totalCount, curFail, target.objectPath())
				}
			}()
		}
		wg.Wait()
	}

	if delCtx.Err() != nil {
		_ = delRunner.persistRemainingTaskItems()
		m.restorePendingAfterCancel(rec)
		return
	}

	m.persistDeleteOutcome(rec, delRunner)
	m.completeRecord(rec)
}

func (m *pendingDeleteManager) persistDeleteOutcome(rec *pendingDeleteRecord, delRunner *JobTask) {
	if rec.TaskID <= 0 {
		// Isolated unit tests with TaskID == 0 may still stub PersistJobTaskItems.
		if err := delRunner.persistRemainingTaskItems(); err != nil {
			log.Printf("Job %d pending delete persist items error: %v", rec.JobID, err)
		}
		return
	}
	// Check whether the historical task row still exists before writing items;
	// if the user already deleted the history row, skip DB updates cleanly.
	taskRow, err := mapper.GetJobTaskByID(rec.TaskID)
	if err != nil {
		if !errors.Is(err, mapper.ErrTaskNotFound) {
			// If DB is not initialized (unit test with stubbed persistence),
			// still flush via delRunner.persistRemainingTaskItems().
			if persistErr := delRunner.persistRemainingTaskItems(); persistErr != nil {
				log.Printf("Job %d (task %d) pending delete persist items error: %v", rec.JobID, rec.TaskID, persistErr)
			}
		}
		return
	}
	if err := delRunner.persistRemainingTaskItems(); err != nil {
		log.Printf("Job %d (task %d) failed to save confirmed delete items: %v", rec.JobID, rec.TaskID, err)
		return
	}
	refreshTaskAfterDeferredDeletes(rec.TaskID, taskRow)
}

func refreshTaskAfterDeferredDeletes(taskID int64, previousRow map[string]interface{}) {
	taskNum, err := GetCuTaskNum(taskID)
	if err != nil {
		log.Printf("Task %d failed to recount items after deferred deletes: %v", taskID, err)
		return
	}
	if prevNum := parseTaskNumJSON(previousRow["taskNum"]); prevNum != nil {
		for _, preserveKey := range []string{"duration", "scanFinish", "scan"} {
			if v, ok := prevNum[preserveKey]; ok {
				taskNum[preserveKey] = v
			}
		}
	}
	prevStatus := taskStatusFromValue(previousRow["status"])
	nextStatus := prevStatus
	switch prevStatus {
	case taskStatusSuccess, taskStatusPartialFail, taskStatusFailed, taskStatusNoSync:
		failOrOther := util.ToInt(taskNum["failNum"]) + util.ToInt(taskNum["otherNum"])
		successNum := util.ToInt(taskNum["successNum"])
		allNum := util.ToInt(taskNum["allNum"])
		nextStatus = finalTaskStatus(false, nil, allNum, successNum, failOrOther)
	}
	var errMsg *string
	if s := strings.TrimSpace(util.StringValue(previousRow["errMsg"])); s != "" {
		errMsg = &s
	}
	encoded, err := json.Marshal(taskNum)
	if err != nil {
		log.Printf("Task %d failed to marshal counters after deferred deletes: %v", taskID, err)
		return
	}
	if err := mapper.UpdateJobTaskStatusAndNumPreserveRunTime(taskID, nextStatus.Int(), errMsg, string(encoded)); err != nil {
		log.Printf("Task %d failed to update history counters after deferred deletes: %v", taskID, err)
	}
}

func (m *pendingDeleteManager) returnToPending(rec *pendingDeleteRecord, targets []pendingDeleteTarget, planChanged bool, lastErr string) {
	var notifySnap *FullSyncApproval
	var notifyJob map[string]interface{}
	var notifyTaskID, notifyJobID int64

	m.mu.Lock()
	current := m.byJob[rec.JobID]
	if current != rec || m.stopped {
		m.mu.Unlock()
		return
	}
	rec.Targets = append([]pendingDeleteTarget(nil), targets...)
	rec.Approval = buildApprovalFromTargets(rec.JobID, rec.JobName, rec.TaskID, rec.Targets)
	rec.Status = pendingApprovalStatusPending
	rec.PlanChanged = planChanged
	rec.LastError = lastErr
	rec.execCancel = nil
	rec.resetExecProgressLocked()
	rec.CreatedAt = time.Now().Unix()
	m.armExpiryTimerLocked(rec, effectiveFullSyncApprovalWait())
	persistPendingDeleteRecord(rec)
	if planChanged && rec.TaskID > 0 {
		notifySnap = rec.approvalSnapshot()
		notifyJob = cloneJobConfig(rec.JobConfig)
		notifyTaskID = rec.TaskID
		notifyJobID = rec.JobID
	}
	m.mu.Unlock()

	if notifySnap != nil {
		QueueApprovalNotification(notifyTaskID, notifyJobID, notifyJob, notifySnap)
	}
}

func (m *pendingDeleteManager) restorePendingAfterCancel(rec *pendingDeleteRecord) {
	m.mu.Lock()
	current := m.byJob[rec.JobID]
	if current == rec {
		rec.Status = pendingApprovalStatusPending
		rec.execCancel = nil
		rec.resetExecProgressLocked()
	}
	m.mu.Unlock()
}

func (m *pendingDeleteManager) completeRecord(rec *pendingDeleteRecord) {
	m.mu.Lock()
	if m.byJob[rec.JobID] == rec {
		if rec.expiryTimer != nil {
			rec.expiryTimer.Stop()
			rec.expiryTimer = nil
		}
		rec.execCancel = nil
		delete(m.byJob, rec.JobID)
	}
	m.mu.Unlock()
	if rec.JobID > 0 {
		if err := mapper.DeletePendingDeleteApproval(rec.JobID); err != nil {
			log.Printf("Failed to delete completed pending approval for job %d: %v", rec.JobID, err)
		}
	}
}

// pendingDeleteDirLister lists a directory during targeted pending-delete verification.
type pendingDeleteDirLister func(dir string) (FileListResult, bool, error)

// pendingDeleteTreeLister lists a full directory subtree in one recursive pass
// and returns directory listings keyed by normalized directory path.
type pendingDeleteTreeLister func(dir string) (map[string]FileListResult, bool, error)

type pendingDeleteVerifyOptions struct {
	listSrc        pendingDeleteDirLister
	listDst        pendingDeleteDirLister
	listDstSubtree pendingDeleteTreeLister
	concurrency    int
	onProgress     func(done, total int, current string)
}

func verifyPendingDeleteTargetsWithLister(
	ctx context.Context,
	listSrcRaw pendingDeleteDirLister,
	listDstRaw pendingDeleteDirLister,
	jobCfg map[string]interface{},
	targets []pendingDeleteTarget,
) (toDelete []pendingDeleteTarget, refreshed []pendingDeleteTarget, changed bool, err error) {
	return verifyPendingDeleteTargetsWithOptions(ctx, jobCfg, targets, pendingDeleteVerifyOptions{
		listSrc:     listSrcRaw,
		listDst:     listDstRaw,
		concurrency: 1,
	})
}

func verifyPendingDeleteTargetsWithOptions(
	ctx context.Context,
	jobCfg map[string]interface{},
	targets []pendingDeleteTarget,
	opt pendingDeleteVerifyOptions,
) (toDelete []pendingDeleteTarget, refreshed []pendingDeleteTarget, changed bool, err error) {
	spec := buildJobExcludeMatcher(jobCfg)
	includeM := buildJobIncludeMatcher(jobCfg)
	protectConflicts := util.ToInt(jobCfg["conflictPolicy"]) == 2
	now := time.Now()
	concurrency := opt.concurrency
	if concurrency < 1 {
		concurrency = 1
	}

	type cachedList struct {
		items    FileListResult
		notFound bool
	}
	var srcCacheMu sync.Mutex
	var dstCacheMu sync.Mutex
	srcCache := make(map[string]cachedList)
	dstCache := make(map[string]cachedList)

	listSrc := func(dir string) (FileListResult, bool, error) {
		dir = normalizeDirPath(dir)
		srcCacheMu.Lock()
		c, ok := srcCache[dir]
		srcCacheMu.Unlock()
		if ok {
			return c.items, c.notFound, nil
		}
		items, notFound, listErr := opt.listSrc(dir)
		if listErr != nil {
			return nil, false, listErr
		}
		srcCacheMu.Lock()
		srcCache[dir] = cachedList{items: items, notFound: notFound}
		srcCacheMu.Unlock()
		return items, notFound, nil
	}

	listDst := func(dir string) (FileListResult, bool, error) {
		dir = normalizeDirPath(dir)
		dstCacheMu.Lock()
		c, ok := dstCache[dir]
		dstCacheMu.Unlock()
		if ok {
			return c.items, c.notFound, nil
		}
		items, notFound, listErr := opt.listDst(dir)
		if listErr != nil {
			return nil, false, listErr
		}
		dstCacheMu.Lock()
		dstCache[dir] = cachedList{items: items, notFound: notFound}
		dstCacheMu.Unlock()
		return items, notFound, nil
	}

	runParallelDirs := func(dirs []string, limit int, fn func(dir string) error) error {
		if len(dirs) == 0 {
			return nil
		}
		if limit <= 1 || len(dirs) == 1 {
			for _, d := range dirs {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err := fn(d); err != nil {
					return err
				}
			}
			return nil
		}
		pctx, cancel := context.WithCancel(ctx)
		defer cancel()
		var firstErr error
		var errOnce sync.Once
		ch := make(chan string, len(dirs))
		for _, d := range dirs {
			ch <- d
		}
		close(ch)
		workers := min(limit, len(dirs))
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for d := range ch {
					if pctx.Err() != nil {
						return
					}
					if err := fn(d); err != nil {
						errOnce.Do(func() {
							firstErr = err
							cancel()
						})
						return
					}
				}
			}()
		}
		wg.Wait()
		if firstErr != nil {
			return firstErr
		}
		return ctx.Err()
	}

	// Step 1: Verify every referenced source root is reachable and did not
	// suddenly become empty (empty-root anomaly guard).
	type rootReq struct {
		srcRoot  string
		nonEmpty bool
	}
	rootReqs := make([]rootReq, 0, 2)
	rootIndex := make(map[string]int)
	for _, target := range targets {
		srcRoot := normalizeDirPath(target.SrcRoot)
		if srcRoot == "" {
			continue
		}
		if idx, exists := rootIndex[srcRoot]; exists {
			if target.SrcRootNonEmpty {
				rootReqs[idx].nonEmpty = true
			}
			continue
		}
		rootIndex[srcRoot] = len(rootReqs)
		rootReqs = append(rootReqs, rootReq{srcRoot: srcRoot, nonEmpty: target.SrcRootNonEmpty})
	}
	rootDirs := make([]string, len(rootReqs))
	for i, r := range rootReqs {
		rootDirs[i] = r.srcRoot
	}
	if err := runParallelDirs(rootDirs, concurrency, func(srcRoot string) error {
		_, _, listErr := listSrc(srcRoot)
		if listErr != nil {
			return fmt.Errorf("校验源根目录 %s 失败: %w", srcRoot, listErr)
		}
		return nil
	}); err != nil {
		return nil, nil, false, err
	}
	for _, r := range rootReqs {
		c := srcCache[r.srcRoot]
		if c.notFound {
			return nil, nil, false, errors.New(msg.MirrorDeleteGuard(fmt.Sprintf("source root %s is missing", r.srcRoot)))
		}
		if r.nonEmpty && len(c.items) == 0 {
			return nil, nil, false, errors.New(msg.MirrorDeleteGuard(fmt.Sprintf("source root %s listed empty during approval verification", r.srcRoot)))
		}
	}

	// Step 1b: Pre-fetch unique DstDir and SrcDir listings concurrently so
	// remote verification runs in parallel waves instead of serial subprocesses.
	if concurrency > 1 {
		type prefetchReq struct {
			isSrc bool
			dir   string
		}
		seenDst := make(map[string]struct{})
		seenSrc := make(map[string]struct{}, len(rootReqs))
		for _, r := range rootReqs {
			seenSrc[r.srcRoot] = struct{}{}
		}
		reqs := make([]prefetchReq, 0, len(targets))
		for _, target := range targets {
			if !isSafeDeleteTargetName(target.Name) {
				continue
			}
			dstDir := normalizeDirPath(target.DstDir)
			if dstDir != "" {
				if _, ok := seenDst[dstDir]; !ok {
					seenDst[dstDir] = struct{}{}
					reqs = append(reqs, prefetchReq{isSrc: false, dir: dstDir})
				}
			}
			srcDir := normalizeDirPath(target.SrcDir)
			if srcDir != "" {
				if _, ok := seenSrc[srcDir]; !ok {
					seenSrc[srcDir] = struct{}{}
					reqs = append(reqs, prefetchReq{isSrc: true, dir: srcDir})
				}
			}
		}
		if len(reqs) > 0 {
			pctx, cancel := context.WithCancel(ctx)
			defer cancel()
			var firstErr error
			var errOnce sync.Once
			var progMu sync.Mutex
			prefetched := 0
			totalReqs := len(reqs)
			ch := make(chan prefetchReq, len(reqs))
			for _, r := range reqs {
				ch <- r
			}
			close(ch)
			workers := min(concurrency, len(reqs))
			var wg sync.WaitGroup
			for i := 0; i < workers; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for r := range ch {
						if pctx.Err() != nil {
							return
						}
						var listErr error
						if r.isSrc {
							_, _, listErr = listSrc(r.dir)
							if listErr != nil {
								listErr = fmt.Errorf("校验源目录 %s 失败: %w", r.dir, listErr)
							}
						} else {
							_, _, listErr = listDst(r.dir)
							if listErr != nil {
								listErr = fmt.Errorf("校验目标目录 %s 失败: %w", r.dir, listErr)
							}
						}
						if listErr != nil {
							errOnce.Do(func() {
								firstErr = listErr
								cancel()
							})
							return
						}
						if opt.onProgress != nil {
							progMu.Lock()
							prefetched++
							done := (prefetched * len(targets)) / max(1, totalReqs*2)
							progMu.Unlock()
							opt.onProgress(done, len(targets), r.dir)
						}
					}
				}()
			}
			wg.Wait()
			if firstErr != nil {
				return nil, nil, false, firstErr
			}
			if ctx.Err() != nil {
				return nil, nil, false, ctx.Err()
			}
		}
	}

	// Step 1c: Pre-fetch directory target subtrees via recursive ListTree (1 call
	// per directory target instead of 1 call per nested subdirectory).
	if opt.listDstSubtree != nil {
		seenSub := make(map[string]struct{})
		subDirs := make([]string, 0)
		for _, target := range targets {
			if !target.IsDir || !isSafeDeleteTargetName(target.Name) {
				continue
			}
			dstDir := normalizeDirPath(target.DstDir)
			dstCacheMu.Lock()
			parentCached, hasParent := dstCache[dstDir]
			dstCacheMu.Unlock()
			if hasParent {
				if parentCached.notFound {
					continue
				}
				if _, exists := parentCached.items[target.Name]; !exists {
					continue
				}
			}
			subPath := normalizeDirPath(target.DstDir + target.Name)
			if _, ok := seenSub[subPath]; ok {
				continue
			}
			seenSub[subPath] = struct{}{}
			subDirs = append(subDirs, subPath)
		}
		if len(subDirs) > 0 {
			if err := runParallelDirs(subDirs, min(pendingDeleteDirConcurrency, concurrency), func(subPath string) error {
				treeDirs, notFound, treeErr := opt.listDstSubtree(subPath)
				if treeErr != nil {
					return fmt.Errorf("校验目标目录内容 %s 失败: %w", strings.TrimRight(subPath, "/"), treeErr)
				}
				dstCacheMu.Lock()
				if notFound {
					dstCache[subPath] = cachedList{notFound: true}
				} else {
					for d, items := range treeDirs {
						dstCache[normalizeDirPath(d)] = cachedList{items: items, notFound: false}
					}
				}
				dstCacheMu.Unlock()
				return nil
			}); err != nil {
				return nil, nil, false, err
			}
		}
	}

	// Helper: confirm missing source directory is genuinely absent from its
	// nearest existing ancestor up to srcRoot (not hidden by a listing error),
	// cached per (srcRoot, srcDir).
	absentCache := make(map[string]error)
	confirmSrcDirAbsent := func(srcRoot, srcDir string) error {
		srcRoot = normalizeDirPath(srcRoot)
		normSrcDir := normalizeDirPath(srcDir)
		cacheKey := srcRoot + "\x00" + normSrcDir
		if cachedErr, ok := absentCache[cacheKey]; ok {
			return cachedErr
		}
		cur := strings.TrimRight(normSrcDir, "/")
		rootTrim := strings.TrimRight(srcRoot, "/")
		for cur != "" && cur != "/" && cur != rootTrim && strings.HasPrefix(cur+"/", srcRoot) {
			parent := normalizeDirPath(path.Dir(cur))
			childDirName := path.Base(cur) + "/"
			parentItems, notFound, listErr := listSrc(parent)
			if listErr != nil {
				retErr := fmt.Errorf("校验源上级目录 %s 失败: %w", parent, listErr)
				absentCache[cacheKey] = retErr
				return retErr
			}
			if !notFound {
				// Parent exists: verify childDirName does not exist in parent
				// (using case/Unicode folding).
				childKey := foldedEntryName(canonicalFileName(childDirName))
				for name := range parentItems {
					if strings.HasSuffix(name, "/") && foldedEntryName(canonicalFileName(name)) == childKey {
						retErr := fmt.Errorf("source directory %s unexpectedly listed in parent %s", childDirName, parent)
						absentCache[cacheKey] = retErr
						return retErr
					}
				}
				absentCache[cacheKey] = nil
				return nil
			}
			cur = strings.TrimRight(parent, "/")
		}
		absentCache[cacheKey] = nil
		return nil
	}

	// Cached O(1) lookup index of filtered source entries per (srcRoot, srcDir).
	type srcDirIndex struct {
		allFolded         map[string]struct{}
		dirFolded         map[string]struct{}
		fileFolded        map[string]struct{}
		allowedFileFolded map[string]struct{}
	}
	srcIndexCache := make(map[string]*srcDirIndex)
	getSrcDirIndex := func(srcRoot, srcDir string, raw FileListResult) *srcDirIndex {
		key := normalizeDirPath(srcRoot) + "\x00" + normalizeDirPath(srcDir)
		if idx, ok := srcIndexCache[key]; ok {
			return idx
		}
		relDir := strings.TrimPrefix(normalizeDirPath(srcDir), normalizeDirPath(srcRoot))
		idx := &srcDirIndex{
			allFolded:         make(map[string]struct{}, len(raw)),
			dirFolded:         make(map[string]struct{}),
			fileFolded:        make(map[string]struct{}),
			allowedFileFolded: make(map[string]struct{}),
		}
		for name, meta := range raw {
			isDir := strings.HasSuffix(name, "/")
			relPath := relDir + name
			if spec != nil && spec.matchesListed(relPath, isDir) {
				continue
			}
			if !isDir && includeM != nil && !includeM.matchesListed(relPath) {
				continue
			}
			folded := foldedEntryName(canonicalFileName(name))
			idx.allFolded[folded] = struct{}{}
			if isDir {
				idx.dirFolded[folded] = struct{}{}
			} else {
				idx.fileFolded[folded] = struct{}{}
				if jobAllowsFileMeta(jobCfg, meta, now) {
					idx.allowedFileFolded[folded] = struct{}{}
				}
			}
		}
		srcIndexCache[key] = idx
		return idx
	}

	// Helper: recursively inspect a destination directory subtree for digest &
	// protected-entry detection (hits dstCache populated by ListTree when available).
	var scanDstSubtree func(dstRoot, dirPath string) ([]subtreeDigestEntry, bool, error)
	scanDstSubtree = func(dstRoot, dirPath string) ([]subtreeDigestEntry, bool, error) {
		dirPath = normalizeDirPath(dirPath)
		dstRoot = normalizeDirPath(dstRoot)
		baseRel := strings.TrimPrefix(dirPath, dstRoot)
		var entries []subtreeDigestEntry
		var walk func(subRel string) (bool, error)
		walk = func(subRel string) (bool, error) {
			items, notFound, listErr := listDst(dirPath + subRel)
			if listErr != nil {
				return false, listErr
			}
			if notFound {
				return false, nil
			}
			for _, name := range sortedFileListKeys(items) {
				meta := items[name]
				isDir := strings.HasSuffix(name, "/")
				relFromRoot := baseRel + subRel + name
				if (spec != nil && spec.matchesListed(relFromRoot, isDir)) ||
					(!isDir && includeM != nil && !includeM.matchesListed(relFromRoot)) ||
					(!isDir && !jobAllowsFileMeta(jobCfg, meta, now)) ||
					(protectConflicts && !isDir && isConflictBackupName(name)) {
					return true, nil
				}
				entries = append(entries, subtreeDigestEntry{
					relPath:  subRel + name,
					isDir:    isDir,
					size:     meta.Size,
					modified: meta.Modified,
				})
				if isDir {
					hasProt, subErr := walk(subRel + name)
					if subErr != nil {
						return false, subErr
					}
					if hasProt {
						return true, nil
					}
				}
			}
			return false, nil
		}
		hasProt, walkErr := walk("")
		if walkErr != nil {
			return nil, false, walkErr
		}
		return entries, hasProt, nil
	}

	// Step 2: Verify every individual target.
	for i, target := range targets {
		if ctx.Err() != nil {
			return nil, nil, false, ctx.Err()
		}
		if opt.onProgress != nil && (i == 0 || (i+1)%25 == 0 || i+1 == len(targets)) {
			opt.onProgress(i+1, len(targets), target.objectPath())
		}
		if !isSafeDeleteTargetName(target.Name) {
			continue
		}

		// 2a. Verify destination still has this entry and it is not protected.
		dstItems, dstNotFound, dstErr := listDst(target.DstDir)
		if dstErr != nil {
			return nil, nil, false, fmt.Errorf("校验目标目录 %s 失败: %w", target.DstDir, dstErr)
		}
		if dstNotFound {
			continue
		}
		dstMeta, exists := dstItems[target.Name]
		if !exists {
			continue
		}
		dstRelPath := strings.TrimPrefix(normalizeDirPath(target.DstDir), normalizeDirPath(target.DstRoot)) + target.Name
		if (spec != nil && spec.matchesListed(dstRelPath, target.IsDir)) ||
			(!target.IsDir && includeM != nil && !includeM.matchesListed(dstRelPath)) ||
			(!target.IsDir && !jobAllowsFileMeta(jobCfg, dstMeta, now)) ||
			(protectConflicts && !target.IsDir && isConflictBackupName(target.Name)) {
			continue
		}

		// 2b. Verify source counterpart state.
		srcItemsRaw, srcNotFound, srcErr := listSrc(target.SrcDir)
		if srcErr != nil {
			return nil, nil, false, fmt.Errorf("校验源目录 %s 失败: %w", target.SrcDir, srcErr)
		}
		targetFolded := foldedEntryName(canonicalFileName(target.Name))
		if target.IsBlocker {
			// Blocker requires opposite-type entry still present on source and
			// no same-type entry on source.
			if srcNotFound {
				continue
			}
			srcIdx := getSrcDirIndex(target.SrcRoot, target.SrcDir, srcItemsRaw)
			var sameTypeExists, oppositeTypeExists bool
			if target.IsDir {
				_, sameTypeExists = srcIdx.dirFolded[targetFolded]
				_, oppositeTypeExists = srcIdx.allowedFileFolded[targetFolded]
			} else {
				_, sameTypeExists = srcIdx.fileFolded[targetFolded]
				_, oppositeTypeExists = srcIdx.dirFolded[targetFolded]
			}
			if sameTypeExists || !oppositeTypeExists {
				continue
			}
		} else {
			if srcNotFound {
				if err := confirmSrcDirAbsent(target.SrcRoot, target.SrcDir); err != nil {
					return nil, nil, false, err
				}
			} else {
				// Suspicious empty source subdirectory guard.
				if normalizeDirPath(target.SrcDir) != normalizeDirPath(target.SrcRoot) && len(srcItemsRaw) == 0 && len(dstItems) >= mirrorDeleteFloor {
					return nil, nil, false, errors.New(msg.MirrorDeleteGuard(fmt.Sprintf("source directory %s listed empty while destination holds %d entries", target.SrcDir, len(dstItems))))
				}
				// If source now has ANY matching entry (same or opposite type),
				// do not delete the destination entry.
				srcIdx := getSrcDirIndex(target.SrcRoot, target.SrcDir, srcItemsRaw)
				if _, srcMatched := srcIdx.allFolded[targetFolded]; srcMatched {
					continue
				}
			}
		}

		// 2c. Verify destination entry contents did not change since approval.
		if !target.IsDir {
			if dstMeta.Size != target.Size || dstMeta.Modified != target.Modified {
				updated := target
				updated.Size = dstMeta.Size
				updated.Modified = dstMeta.Modified
				refreshed = append(refreshed, updated)
				changed = true
				continue
			}
			toDelete = append(toDelete, target)
			refreshed = append(refreshed, target)
			continue
		}

		entries, hasProtected, subErr := scanDstSubtree(target.DstRoot, normalizeDirPath(target.DstDir+target.Name))
		if subErr != nil {
			return nil, nil, false, fmt.Errorf("校验目标目录内容 %s 失败: %w", target.objectPath(), subErr)
		}
		if hasProtected {
			// A protected/excluded entry appeared inside the directory: refuse
			// recursive deletion.
			continue
		}
		freshCount, freshSize, freshDigest := summarizeSubtreeEntries(entries)
		if freshDigest != target.SubtreeDigest {
			updated := target
			updated.Count = freshCount
			updated.Size = freshSize
			updated.SubtreeDigest = freshDigest
			refreshed = append(refreshed, updated)
			changed = true
			continue
		}
		toDelete = append(toDelete, target)
		refreshed = append(refreshed, target)
	}

	return toDelete, refreshed, changed, nil
}
