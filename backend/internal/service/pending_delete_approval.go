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
	client        *AlistClient       `json:"-"`
	expiryTimer   *time.Timer        `json:"-"`
	execCancel    context.CancelFunc `json:"-"`
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
	cp.Paths = append([]FullSyncApprovalPath(nil), r.Approval.Paths...)
	if cp.Paths == nil {
		cp.Paths = []FullSyncApprovalPath{}
	}
	return &cp
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
	// Preserve direct client override for isolated unit tests that construct a
	// JobTask against an httptest server without registering an engine in DB.
	if util.ToInt64(jobSnap["alistId"]) <= 0 && jt.AlistClient != nil {
		rec.client = jt.AlistClient
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
	})
}

func (m *pendingDeleteManager) ApproveByJobID(jobID int64) error {
	return m.startExecution(func(now time.Time) (*pendingDeleteRecord, error) {
		return m.findLockedByJobID(jobID, now)
	})
}

func (m *pendingDeleteManager) startExecution(lookup func(time.Time) (*pendingDeleteRecord, error)) error {
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
	if rec.expiryTimer != nil {
		rec.expiryTimer.Stop()
		rec.expiryTimer = nil
	}
	execCtx, cancel := context.WithCancel(context.Background())
	rec.Status = pendingApprovalStatusExecuting
	rec.PlanChanged = false
	rec.LastError = ""
	rec.execCancel = cancel
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
	client, err := m.resolveClient(ctx, rec, jobCfg)
	if err != nil {
		if ctx.Err() != nil {
			m.restorePendingAfterCancel(rec)
			return
		}
		log.Printf("Job %d (task %d) pending delete verification failed to connect engine: %v", rec.JobID, rec.TaskID, err)
		m.returnToPending(rec, rec.Targets, false, err.Error())
		return
	}

	toDelete, refreshedTargets, changed, verifyErr := verifyPendingDeleteTargets(ctx, client, jobCfg, rec.Targets)
	if ctx.Err() != nil {
		m.restorePendingAfterCancel(rec)
		return
	}
	if verifyErr != nil {
		log.Printf("Job %d (task %d) pending delete verification aborted: %v", rec.JobID, rec.TaskID, verifyErr)
		m.returnToPending(rec, rec.Targets, false, verifyErr.Error())
		return
	}
	if changed {
		if len(refreshedTargets) == 0 {
			m.completeRecord(rec)
			return
		}
		log.Printf("Job %d (task %d) pending delete target(s) changed since scan; re-prompting with %d updated path(s)", rec.JobID, rec.TaskID, len(refreshedTargets))
		m.returnToPending(rec, refreshedTargets, true, "")
		return
	}
	if len(toDelete) == 0 {
		log.Printf("Job %d (task %d) pending delete targets already resolved; nothing to delete", rec.JobID, rec.TaskID)
		m.completeRecord(rec)
		return
	}

	// Execute verified deletions with a lightweight JobTask runner so trash
	// handling, path safety checks, consecutive failure caps, and task-item
	// persistence share the exact same tested implementation as full sync.
	delCtx, delCancel := context.WithCancel(ctx)
	delRunner := &JobTask{
		TaskID:      rec.TaskID,
		Job:         jobCfg,
		AlistClient: client,
		ctx:         delCtx,
		cancel:      delCancel,
	}
	delRunner.initRuntime()
	defer delRunner.cancelTaskRuntime()

	// Group by DstRoot so each root uses its own fullSyncDeleteRun while
	// keeping deterministic target order.
	runsByRoot := make(map[string]*fullSyncDeleteRun)
	for _, target := range toDelete {
		if delCtx.Err() != nil {
			break
		}
		run := runsByRoot[target.DstRoot]
		if run == nil {
			run = &fullSyncDeleteRun{jt: delRunner, root: target.DstRoot}
			runsByRoot[target.DstRoot] = run
		}
		if run.aborted {
			continue
		}
		run.run(fullSyncDelete{
			dir:      target.DstDir,
			name:     target.Name,
			metadata: FileMetadata{Size: target.Size, Modified: target.Modified},
		})
	}

	if delCtx.Err() != nil {
		// Process shutdown or job deletion interrupted execution.
		_ = delRunner.persistRemainingTaskItems()
		m.restorePendingAfterCancel(rec)
		return
	}

	m.persistDeleteOutcome(rec, delRunner)
	m.completeRecord(rec)
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

func (m *pendingDeleteManager) resolveClient(ctx context.Context, rec *pendingDeleteRecord, jobCfg map[string]interface{}) (*AlistClient, error) {
	if rec.client != nil {
		return rec.client, nil
	}
	alistID := util.ToInt64(jobCfg["alistId"])
	if alistID <= 0 {
		return nil, errors.New("missing storage engine id for pending deletion")
	}
	return GetClientByIDContext(ctx, alistID)
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

// verifyPendingDeleteTargets performs targeted live verification of every
// approved target against source and destination before any deletion happens.
//
// Returns:
//   - toDelete: targets verified safe & unchanged, ready for deletion
//   - refreshed: updated targets list when changed == true
//   - changed: true if any still-extra target changed on destination since scan
//   - err: non-nil if source/destination could not be verified safely (network
//     error, empty source root guard, etc.); callers must delete nothing.
func verifyPendingDeleteTargets(
	ctx context.Context,
	client *AlistClient,
	jobCfg map[string]interface{},
	targets []pendingDeleteTarget,
) (toDelete []pendingDeleteTarget, refreshed []pendingDeleteTarget, changed bool, err error) {
	scanIntervalS := util.ToInt(jobCfg["scanIntervalS"])
	scanIntervalT := util.ToInt(jobCfg["scanIntervalT"])
	spec := buildJobExcludeMatcher(jobCfg)
	includeM := buildJobIncludeMatcher(jobCfg)
	protectConflicts := util.ToInt(jobCfg["conflictPolicy"]) == 2
	now := time.Now()

	type cachedList struct {
		items    FileListResult
		notFound bool
	}
	srcCache := make(map[string]cachedList)
	dstCache := make(map[string]cachedList)

	listSrc := func(dir string) (FileListResult, bool, error) {
		dir = normalizeDirPath(dir)
		if c, ok := srcCache[dir]; ok {
			return c.items, c.notFound, nil
		}
		items, listErr := client.FileListApiContext(ctx, dir, 0, scanIntervalS)
		if listErr != nil {
			if isAlistObjectNotFound(listErr) {
				srcCache[dir] = cachedList{notFound: true}
				return nil, true, nil
			}
			return nil, false, listErr
		}
		srcCache[dir] = cachedList{items: items}
		return items, false, nil
	}

	listDst := func(dir string) (FileListResult, bool, error) {
		dir = normalizeDirPath(dir)
		if c, ok := dstCache[dir]; ok {
			return c.items, c.notFound, nil
		}
		items, listErr := client.FileListApiContext(ctx, dir, 0, scanIntervalT)
		if listErr != nil {
			if isAlistObjectNotFound(listErr) {
				dstCache[dir] = cachedList{notFound: true}
				return nil, true, nil
			}
			return nil, false, listErr
		}
		dstCache[dir] = cachedList{items: items}
		return items, false, nil
	}

	// Step 1: Verify every referenced source root is reachable and did not
	// suddenly become empty (empty-root anomaly guard).
	checkedRoots := make(map[string]struct{})
	for _, target := range targets {
		srcRoot := normalizeDirPath(target.SrcRoot)
		if srcRoot == "" {
			continue
		}
		if _, done := checkedRoots[srcRoot]; done {
			continue
		}
		checkedRoots[srcRoot] = struct{}{}
		rootItems, notFound, listErr := listSrc(srcRoot)
		if listErr != nil {
			return nil, nil, false, fmt.Errorf("校验源根目录 %s 失败: %w", srcRoot, listErr)
		}
		if notFound {
			return nil, nil, false, errors.New(msg.MirrorDeleteGuard(fmt.Sprintf("source root %s is missing", srcRoot)))
		}
		if target.SrcRootNonEmpty && len(rootItems) == 0 {
			return nil, nil, false, errors.New(msg.MirrorDeleteGuard(fmt.Sprintf("source root %s listed empty during approval verification", srcRoot)))
		}
	}

	// Helper: confirm missing source directory is genuinely absent from its
	// nearest existing ancestor up to srcRoot (not hidden by a listing error).
	confirmSrcDirAbsent := func(srcRoot, srcDir string) error {
		srcRoot = normalizeDirPath(srcRoot)
		cur := strings.TrimRight(normalizeDirPath(srcDir), "/")
		rootTrim := strings.TrimRight(srcRoot, "/")
		for cur != "" && cur != "/" && cur != rootTrim && strings.HasPrefix(cur+"/", srcRoot) {
			parent := normalizeDirPath(path.Dir(cur))
			childDirName := path.Base(cur) + "/"
			parentItems, notFound, listErr := listSrc(parent)
			if listErr != nil {
				return fmt.Errorf("校验源上级目录 %s 失败: %w", parent, listErr)
			}
			if !notFound {
				// Parent exists: verify childDirName does not exist in parent
				// (using case/Unicode folding).
				childKey := foldedEntryName(canonicalFileName(childDirName))
				for name := range parentItems {
					if strings.HasSuffix(name, "/") && foldedEntryName(canonicalFileName(name)) == childKey {
						return fmt.Errorf("source directory %s unexpectedly listed in parent %s", childDirName, parent)
					}
				}
				return nil
			}
			cur = strings.TrimRight(parent, "/")
		}
		return nil
	}

	// Helper: filter source directory items the same way scanFullSyncTree does.
	filterSrcItems := func(srcRoot, srcDir string, raw FileListResult) FileListResult {
		relDir := strings.TrimPrefix(normalizeDirPath(srcDir), normalizeDirPath(srcRoot))
		filtered := make(FileListResult, len(raw))
		for name, meta := range raw {
			isDir := strings.HasSuffix(name, "/")
			relPath := relDir + name
			if spec != nil && spec.matchesListed(relPath, isDir) {
				continue
			}
			if !isDir && includeM != nil && !includeM.matchesListed(relPath) {
				continue
			}
			filtered[name] = meta
		}
		return filtered
	}

	// Helper: recursively inspect a destination directory subtree for digest &
	// protected-entry detection.
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
	for _, target := range targets {
		if ctx.Err() != nil {
			return nil, nil, false, ctx.Err()
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
			srcItems := filterSrcItems(target.SrcRoot, target.SrcDir, srcItemsRaw)
			sameTypeExists := false
			oppositeTypeExists := false
			for srcName, srcMeta := range srcItems {
				if foldedEntryName(canonicalFileName(srcName)) != targetFolded {
					continue
				}
				srcIsDir := strings.HasSuffix(srcName, "/")
				if srcIsDir == target.IsDir {
					sameTypeExists = true
				} else if srcIsDir || jobAllowsFileMeta(jobCfg, srcMeta, now) {
					oppositeTypeExists = true
				}
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
				srcItems := filterSrcItems(target.SrcRoot, target.SrcDir, srcItemsRaw)
				// Suspicious empty source subdirectory guard.
				if normalizeDirPath(target.SrcDir) != normalizeDirPath(target.SrcRoot) && len(srcItemsRaw) == 0 && len(dstItems) >= mirrorDeleteFloor {
					return nil, nil, false, errors.New(msg.MirrorDeleteGuard(fmt.Sprintf("source directory %s listed empty while destination holds %d entries", target.SrcDir, len(dstItems))))
				}
				// If source now has ANY matching entry (same or opposite type),
				// do not delete the destination entry.
				srcMatched := false
				for srcName := range srcItems {
					if foldedEntryName(canonicalFileName(srcName)) == targetFolded {
						srcMatched = true
						break
					}
				}
				if srcMatched {
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
