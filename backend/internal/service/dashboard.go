package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"math"
	"opensync/internal/mapper"
	"opensync/internal/msg"
	"opensync/internal/native"
	"opensync/pkg/util"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type dashboardSpeedSample struct {
	Timestamp int64   `json:"ts"`
	Speed     float64 `json:"speed"`
}

const (
	dashboardSpeedHistoryMax    = 30
	dashboardSpeedHistoryMaxAge = 180
)

var (
	dashboardSpeedHistoryMu sync.Mutex
	dashboardSpeedHistory   []dashboardSpeedSample
)

func sanitizeSpeed(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}

func resetDashboardSpeedHistory() {
	dashboardSpeedHistoryMu.Lock()
	dashboardSpeedHistory = nil
	dashboardSpeedHistoryMu.Unlock()
}

func recordDashboardSpeedSample(nowUnix int64, speed float64) ([]dashboardSpeedSample, float64) {
	speed = sanitizeSpeed(speed)
	dashboardSpeedHistoryMu.Lock()
	defer dashboardSpeedHistoryMu.Unlock()

	cutoff := nowUnix - dashboardSpeedHistoryMaxAge
	kept := dashboardSpeedHistory[:0]
	for _, s := range dashboardSpeedHistory {
		if s.Timestamp >= cutoff && s.Timestamp <= nowUnix+5 {
			kept = append(kept, s)
		}
	}
	dashboardSpeedHistory = kept

	if n := len(dashboardSpeedHistory); n > 0 && dashboardSpeedHistory[n-1].Timestamp >= nowUnix {
		dashboardSpeedHistory[n-1].Speed = speed
	} else {
		dashboardSpeedHistory = append(dashboardSpeedHistory, dashboardSpeedSample{
			Timestamp: nowUnix,
			Speed:     speed,
		})
	}
	if len(dashboardSpeedHistory) > dashboardSpeedHistoryMax {
		dashboardSpeedHistory = append([]dashboardSpeedSample(nil), dashboardSpeedHistory[len(dashboardSpeedHistory)-dashboardSpeedHistoryMax:]...)
	}

	peak := speed
	out := make([]dashboardSpeedSample, len(dashboardSpeedHistory))
	for i, s := range dashboardSpeedHistory {
		out[i] = s
		if s.Speed > peak {
			peak = s.Speed
		}
	}
	return out, peak
}

type dashboardQuotaItem struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Supported bool   `json:"supported"`
	Total     *int64 `json:"total,omitempty"`
	Used      *int64 `json:"used,omitempty"`
	Free      *int64 `json:"free,omitempty"`
	Message   string `json:"message,omitempty"`
}

type dashboardQuotaSnapshot struct {
	db         *sql.DB
	proxy      string
	at         time.Time
	items      []dashboardQuotaItem
	refreshing bool
}

var (
	dashboardQuotaCacheMu sync.Mutex
	dashboardQuotaCache   *dashboardQuotaSnapshot
)

func invalidateDashboardStorageQuotas() {
	dashboardQuotaCacheMu.Lock()
	dashboardQuotaCache = nil
	dashboardQuotaCacheMu.Unlock()
}

func getCachedDashboardStorageQuotas() []dashboardQuotaItem {
	const ttl = 5 * time.Minute
	db, proxy := mapper.GetDB(), globalNativeProxyURL()
	dashboardQuotaCacheMu.Lock()
	defer dashboardQuotaCacheMu.Unlock()
	if dashboardQuotaCache == nil || dashboardQuotaCache.db != db || dashboardQuotaCache.proxy != proxy {
		dashboardQuotaCache = &dashboardQuotaSnapshot{db: db, proxy: proxy, items: []dashboardQuotaItem{}}
	}
	snapshot := dashboardQuotaCache
	if !snapshot.refreshing && time.Since(snapshot.at) >= ttl {
		_, cfg, err := loadExistingNativeRow()
		if err != nil {
			snapshot.at = time.Now().Add(-ttl + 30*time.Second)
			return snapshot.items
		}
		snapshot.refreshing = true
		go func(previous []dashboardQuotaItem) {
			items := probeDashboardStorageQuotas(cfg, proxy, previous)
			dashboardQuotaCacheMu.Lock()
			defer dashboardQuotaCacheMu.Unlock()
			if dashboardQuotaCache != snapshot {
				return
			}
			snapshot.refreshing = false
			snapshot.items = items
			snapshot.at = time.Now()
		}(snapshot.items)
	}
	return snapshot.items
}

func probeDashboardStorageQuotas(cfg native.Config, proxy string, previous []dashboardQuotaItem) []dashboardQuotaItem {
	type target struct {
		name string
		typ  string
	}
	targets := []target{{name: native.Local, typ: "local"}}
	names := make([]string, 0, len(cfg))
	for name := range cfg {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		typ := strings.TrimSpace(cfg[name]["type"])
		if typ == "" {
			continue
		}
		targets = append(targets, target{name: name, typ: typ})
	}
	// Cap concurrent probes so dashboard remains fast even with many remotes.
	if len(targets) > 12 {
		targets = targets[:12]
	}
	items := make([]dashboardQuotaItem, len(targets))
	previousByName := make(map[string]dashboardQuotaItem, len(previous))
	for _, item := range previous {
		previousByName[item.Name] = item
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	opts := native.SessionOptions{ProxyURL: proxy}
	for i, t := range targets {
		wg.Add(1)
		go func(idx int, tgt target) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			probeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			info, aboutErr := cfg.About(probeCtx, tgt.name, opts)
			if aboutErr != nil || info == nil {
				items[idx] = dashboardQuotaItem{Name: tgt.name, Type: tgt.typ, Supported: false}
				if item, ok := previousByName[tgt.name]; ok && item.Type == tgt.typ {
					items[idx] = item
				}
				return
			}
			items[idx] = dashboardQuotaItem{
				Name:      tgt.name,
				Type:      tgt.typ,
				Supported: info.Supported,
				Total:     info.Total,
				Used:      info.Used,
				Free:      info.Free,
				Message:   info.Message,
			}
		}(i, t)
	}
	wg.Wait()
	return items
}

type dailyBucket struct {
	Date        string `json:"date"`
	Runs        int64  `json:"runs"`
	SuccessRuns int64  `json:"successRuns"`
	FailRuns    int64  `json:"failRuns"`
	Files       int64  `json:"files"`
	Bytes       int64  `json:"bytes"`
}

var dashboardHistoryCache struct {
	mu        sync.Mutex
	db        *sql.DB
	day, zone string
	at        time.Time
	recent    map[string]interface{}
	daily     []dailyBucket
}

func getDashboardHistory(now time.Time) (map[string]interface{}, []dailyBucket, error) {
	const ttl = 30 * time.Second
	db := mapper.GetDB()
	day, zone := now.Format("2006-01-02"), now.Location().String()
	cache := &dashboardHistoryCache
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.db == db && cache.day == day && cache.zone == zone && time.Since(cache.at) < ttl {
		return cache.recent, cache.daily, nil
	}
	since7d := now.AddDate(0, 0, -7).Unix()
	since30d := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -29).Unix()

	dailyBuckets := make([]dailyBucket, 30)
	bucketIndex := make(map[string]int, 30)
	for i := 0; i < 30; i++ {
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -(29 - i))
		key := day.Format("2006-01-02")
		dailyBuckets[i] = dailyBucket{Date: key}
		bucketIndex[key] = i
	}

	var totalRuns, successRuns, failRuns, stoppedRuns, totalFiles, totalBytes int64
	if err := mapper.ForEachRecentTaskWithTime(since30d, func(ts int64, status int, taskNum string) error {
		var files, sz int64
		if parsed := parseTaskNumJSON(taskNum); parsed != nil {
			files = util.ToInt64(parsed["successNum"])
			sz = util.ToInt64(parsed["sumSize"])
		}
		if ts >= since7d {
			totalRuns++
			switch status {
			case 2, 8:
				successRuns++
			case 3, 5, 6, 7, 10:
				failRuns++
			case 4:
				stoppedRuns++
			}
			totalFiles += files
			totalBytes += sz
		}
		dayKey := time.Unix(ts, 0).In(now.Location()).Format("2006-01-02")
		if idx, ok := bucketIndex[dayKey]; ok {
			dailyBuckets[idx].Runs++
			switch status {
			case 2, 8:
				dailyBuckets[idx].SuccessRuns++
			case 3, 5, 6, 7, 10:
				dailyBuckets[idx].FailRuns++
			}
			dailyBuckets[idx].Files += files
			dailyBuckets[idx].Bytes += sz
		}
		return nil
	}); err != nil {
		return nil, nil, fmt.Errorf("dashboard load recent tasks: %w", err)
	}
	cache.db, cache.day, cache.zone, cache.at = db, day, zone, time.Now()
	cache.daily = dailyBuckets
	cache.recent = map[string]interface{}{
		"totalRuns":   totalRuns,
		"successRuns": successRuns,
		"failRuns":    failRuns,
		"stoppedRuns": stoppedRuns,
		"totalFiles":  totalFiles,
		"totalBytes":  totalBytes,
	}
	return cache.recent, cache.daily, nil
}

// GetDashboardOverview aggregates KPI counters,
// recent 7-day execution metrics, and per-job status cards for /dashboard.
func GetDashboardOverview() (map[string]interface{}, error) {
	jobs, err := mapper.GetJobListAll()
	if err != nil {
		return nil, fmt.Errorf("dashboard load jobs: %w", err)
	}
	engines, err := GetClientList()
	if err != nil {
		return nil, fmt.Errorf("dashboard load engines: %w", err)
	}
	latestTasks, err := mapper.GetLatestTaskPerJob()
	if err != nil {
		latestTasks = map[int64]map[string]interface{}{}
	}
	now := time.Now().In(schedulerLocation())
	recent, dailyBuckets, err := getDashboardHistory(now)
	if err != nil {
		return nil, err
	}

	engineNameByID := make(map[int64]string, len(engines))
	onlineEngines := 0
	offlineEngines := 0
	for _, eng := range engines {
		id := util.ToInt64(eng["id"])
		name := strings.TrimSpace(util.StringValue(eng["remark"]))
		if name == "" {
			name = util.StringValue(eng["url"])
		}
		engineNameByID[id] = name
		if h, ok := eng["health"].(EngineHealth); ok {
			switch h.Status {
			case "online":
				onlineEngines++
			case "offline":
				offlineEngines++
			}
		}
	}

	queuedSet := engineQueue.AllQueuedJobIDs()
	syncStates, syncStateErr := mapper.ListSyncStates()
	conflictCounts, _ := mapper.ListUnresolvedBisyncConflictCounts()
	legacyEngineIDs := legacyOpenListAlistIDs()
	jobClientListMu.RLock()
	clientsCopy := make(map[int64]*JobClient, len(jobClientList))
	for id, c := range jobClientList {
		clientsCopy[id] = c
	}
	jobClientListMu.RUnlock()

	enabledJobs := 0
	runningJobs := 0
	pausedJobs := 0
	activeNonPausedJobs := 0
	queuedJobs := 0
	totalSpeed := 0.0
	totalSpeedAvg := 0.0
	totalDoneBytes := int64(0)
	totalRemainBytes := int64(0)
	jobSummaries := make([]map[string]interface{}, 0, len(jobs))

	for _, job := range jobs {
		jobID := util.ToInt64(job["id"])
		alistID := util.ToInt64(job["alistId"])
		legacyOpenList := legacyEngineIDs[alistID]
		enabled := !legacyOpenList && util.ToInt(job["enable"]) == 1
		if enabled {
			enabledJobs++
		}
		queued := queuedSet[jobID]
		if queued {
			queuedJobs++
		}
		nextRunTime := int64(0)
		running := false
		paused := false
		currentTaskID := int64(0)
		var liveNum *taskNumStats
		liveProgress := 0.0
		liveDoneSize := int64(0)
		liveTotalSize := int64(0)
		liveSpeed := 0.0
		liveSpeedAvg := 0.0
		liveRemainTime := 0
		liveBwLimit := ""
		liveScanFinish := true
		var liveScan *scanProgressPayload

		if client := clientsCopy[jobID]; client != nil {
			if nextTime, ok := client.nextRunTime(); ok {
				nextRunTime = nextTime.Unix()
			}
			if task := client.currentTask(); task != nil {
				running = true
				runningJobs++
				currentTaskID = task.TaskID
				payload := task.getCurrentPayload()
				paused = payload.Paused
				numCopy := payload.Num
				liveNum = &numCopy
				liveDoneSize = max(0, payload.DoneSize)
				remainSize := max(0, payload.RemainSize)
				liveTotalSize = liveDoneSize + remainSize
				liveSpeed = sanitizeSpeed(payload.Speed)
				liveSpeedAvg = sanitizeSpeed(payload.SpeedAvg)
				liveRemainTime = max(0, payload.RemainTime)
				liveBwLimit = strings.TrimSpace(payload.LiveBwLimit)
				liveScanFinish = payload.ScanFinish
				liveScan = payload.Scan
				liveProgress = calcTaskProgressPercent(payload)
				if paused {
					pausedJobs++
					liveSpeed = 0
					liveRemainTime = 0
				} else {
					activeNonPausedJobs++
					totalSpeed += liveSpeed
					totalSpeedAvg += liveSpeedAvg
					totalDoneBytes += liveDoneSize
					totalRemainBytes += remainSize
				}
			}
		}

		var lastTaskSummary map[string]interface{}
		if lt, ok := latestTasks[jobID]; ok && lt != nil {
			lastTaskSummary = map[string]interface{}{
				"id":         util.ToInt64(lt["id"]),
				"status":     util.ToInt(lt["status"]),
				"runTime":    util.ToInt64(lt["runTime"]),
				"createTime": util.ToInt64(lt["createTime"]),
				"errMsg":     lt["errMsg"],
			}
			if parsed := parseTaskNumJSON(lt["taskNum"]); parsed != nil {
				lastTaskSummary["allNum"] = util.ToInt64(parsed["allNum"])
				lastTaskSummary["successNum"] = util.ToInt64(parsed["successNum"])
				lastTaskSummary["failNum"] = util.ToInt64(parsed["failNum"])
				lastTaskSummary["sumSize"] = util.ToInt64(parsed["sumSize"])
				lastTaskSummary["duration"] = util.ToInt(parsed["duration"])
			}
		}

		alistName := engineNameByID[alistID]
		if legacyOpenList {
			alistName = "旧版 OpenList（已失效）"
		}
		effectiveEnable := util.ToInt(job["enable"])
		if legacyOpenList {
			effectiveEnable = 0
		}
		jobSummaries = append(jobSummaries, map[string]interface{}{
			"id":             jobID,
			"remark":         job["remark"],
			"srcPath":        job["srcPath"],
			"dstPath":        job["dstPath"],
			"alistId":        alistID,
			"alistName":      alistName,
			"legacyOpenList": legacyOpenList,
			"method":         util.ToInt(job["method"]),
			"enable":         effectiveEnable,
			"isCron":         util.ToInt(job["isCron"]),
			"interval":       job["interval"],
			"windowEnable":   util.ToInt(job["windowEnable"]),
			"windowStart":    job["windowStart"],
			"windowEnd":      job["windowEnd"],
			"extraFlags":     job["extraFlags"],
			"nextRunTime":    nextRunTime,
			"running":        running,
			"paused":         running && paused,
			"queued":         queued,
			"watching":       !legacyOpenList && jobFSWatchers.IsWatching(jobID),
			"checkRunning":   !legacyOpenList && IsJobCheckRunning(jobID),
			"currentTaskId":  currentTaskID,
			"liveNum":        liveNum,
			"liveProgress":   liveProgress,
			"liveDoneSize":   liveDoneSize,
			"liveTotalSize":  liveTotalSize,
			"liveSpeed":      liveSpeed,
			"liveSpeedAvg":   liveSpeedAvg,
			"liveRemainTime": liveRemainTime,
			"liveBwLimit":    liveBwLimit,
			"liveScanFinish": liveScanFinish,
			"liveScan":       liveScan,
			"lastTask":       lastTaskSummary,
		})
		if !legacyOpenList {
			statusRow := cloneJobConfig(job)
			addSyncStatus(statusRow, syncStates[jobID], syncStateErr, conflictCounts[jobID], clientsCopy[jobID])
			jobSummaries[len(jobSummaries)-1]["syncStatus"] = statusRow["syncStatus"]
		}
	}

	activeTransfers := 0
	for _, tr := range ListNativeTransfers() {
		if tr.Status != "running" {
			continue
		}
		activeTransfers++
		if sp := sanitizeSpeed(tr.Speed); sp > 0 {
			totalSpeed += sp
		}
		if tr.TotalBytes > 0 {
			done := clampInt64(tr.DoneBytes, 0, tr.TotalBytes)
			totalDoneBytes += done
			totalRemainBytes += tr.TotalBytes - done
		}
	}

	estimatedRemainTime := 0
	if (activeNonPausedJobs > 0 || activeTransfers > 0) && totalRemainBytes > 0 {
		rate := totalSpeedAvg
		if totalSpeed > 0 && totalSpeedAvg > 0 {
			rate = 0.6*totalSpeed + 0.4*totalSpeedAvg
		} else if totalSpeed > 0 {
			rate = totalSpeed
		}
		if rate > 0 {
			estimatedRemainTime = int(math.Ceil(float64(totalRemainBytes) / rate))
		}
	}
	speedHistory, peakSpeed := recordDashboardSpeedSample(now.Unix(), totalSpeed)

	return map[string]interface{}{
		"kpi": map[string]interface{}{
			"totalJobs":      len(jobs),
			"enabledJobs":    enabledJobs,
			"runningJobs":    runningJobs,
			"queuedJobs":     queuedJobs,
			"totalEngines":   len(engines),
			"onlineEngines":  onlineEngines,
			"offlineEngines": offlineEngines,
		},
		"realtime": map[string]interface{}{
			"totalSpeed":          totalSpeed,
			"totalSpeedAvg":       totalSpeedAvg,
			"peakSpeed":           peakSpeed,
			"activeJobs":          activeNonPausedJobs,
			"pausedJobs":          pausedJobs,
			"activeTransfers":     activeTransfers,
			"totalDoneBytes":      totalDoneBytes,
			"totalRemainBytes":    totalRemainBytes,
			"estimatedRemainTime": estimatedRemainTime,
			"globalBwLimit":       globalDefaultBwLimit(),
			"sampledAt":           now.Unix(),
			"speedHistory":        speedHistory,
		},
		"recent7d":      recent,
		"jobs":          jobSummaries,
		"dailyTrend":    dailyBuckets,
		"storageQuotas": getCachedDashboardStorageQuotas(),
	}, nil
}

// BatchJobAction executes a bulk operation across specified (or all) jobs.
// Supported actions: "run", "stop", "enable", "disable", "run_all", "stop_all".
func BatchJobAction(action string, jobIDs []int64) (map[string]interface{}, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "run", "stop", "enable", "disable", "run_all", "stop_all":
	default:
		return nil, publicError(msg.T(msg.BatchActionInvalid))
	}

	targetIDs := jobIDs
	if action == "run_all" || action == "stop_all" {
		allJobs, err := mapper.GetJobListAll()
		if err != nil {
			return nil, fmt.Errorf("batch load jobs: %w", err)
		}
		targetIDs = make([]int64, 0, len(allJobs))
		for _, j := range allJobs {
			id := util.ToInt64(j["id"])
			if id <= 0 {
				continue
			}
			if action == "run_all" && util.ToInt(j["enable"]) != 1 {
				continue
			}
			targetIDs = append(targetIDs, id)
		}
	}

	results := make([]map[string]interface{}, 0, len(targetIDs))
	succeeded := 0
	failed := 0
	for _, id := range targetIDs {
		if id <= 0 {
			continue
		}
		var err error
		switch action {
		case "run", "run_all":
			err = DoJobManual(id)
		case "stop", "stop_all":
			err = AbortJob(id)
		case "enable":
			err = ContinueJob(id)
		case "disable":
			err = PauseJob(id)
		}
		if err != nil {
			failed++
			results = append(results, map[string]interface{}{
				"jobId": id,
				"ok":    false,
				"error": err.Error(),
			})
		} else {
			succeeded++
			results = append(results, map[string]interface{}{
				"jobId": id,
				"ok":    true,
			})
		}
	}
	return map[string]interface{}{
		"action":    action,
		"succeeded": succeeded,
		"failed":    failed,
		"results":   results,
	}, nil
}

// RemoveTasksBatch deletes a set of finished history tasks (rejecting running or active retry sources).
func RemoveTasksBatch(taskIDs []int64) (int, error) {
	if len(taskIDs) == 0 {
		return 0, publicError(msg.T(msg.LostPart))
	}
	tasks, err := mapper.GetJobTasksByIDs(taskIDs)
	if err != nil {
		return 0, fmt.Errorf("load tasks for batch delete: %w", err)
	}
	deletable := make([]int64, 0, len(tasks))
	for _, task := range tasks {
		tid := util.ToInt64(task["id"])
		status := taskStatusFromValue(task["status"])
		if status == taskStatusWaiting || status == taskStatusRunning {
			return 0, publicError(msg.T(msg.TaskRunningCannotDelete))
		}
		jobID := util.ToInt64(task["jobId"])
		jobClientListMu.RLock()
		client := jobClientList[jobID]
		jobClientListMu.RUnlock()
		if client != nil {
			if cur := client.currentTask(); cur != nil && cur.TaskID == tid {
				return 0, publicError(msg.T(msg.TaskRunningCannotDelete))
			}
			if client.retrySource() == tid {
				return 0, publicError(msg.T(msg.TaskRunningCannotDelete))
			}
		}
		deletable = append(deletable, tid)
	}
	if len(deletable) == 0 {
		return 0, nil
	}
	if err := mapper.DeleteJobTasksByIDs(deletable); err != nil {
		return 0, fmt.Errorf("batch delete tasks: %w", err)
	}
	return len(deletable), nil
}

// ClearJobHistory deletes all finished historical tasks for jobID (skipping any
// task currently running or locked as a retry source).
func ClearJobHistory(jobID int64) (int, error) {
	if jobID <= 0 {
		return 0, publicError(msg.T(msg.LostPart))
	}
	if _, err := mapper.GetJobByID(jobID); err != nil {
		return 0, publicErrorIf(err, msg.T(msg.JobNotFound))
	}
	ids, err := mapper.GetFinishedTaskIDsByJobID(jobID)
	if err != nil {
		return 0, fmt.Errorf("load finished tasks for clear: %w", err)
	}
	jobClientListMu.RLock()
	client := jobClientList[jobID]
	jobClientListMu.RUnlock()

	var protectedCurrent, protectedRetry int64
	if client != nil {
		if cur := client.currentTask(); cur != nil {
			protectedCurrent = cur.TaskID
		}
		protectedRetry = client.retrySource()
	}
	filtered := make([]int64, 0, len(ids))
	for _, tid := range ids {
		if tid == protectedCurrent || tid == protectedRetry {
			continue
		}
		filtered = append(filtered, tid)
	}
	if len(filtered) == 0 {
		return 0, nil
	}
	if err := mapper.DeleteJobTasksByIDs(filtered); err != nil {
		return 0, fmt.Errorf("clear job history: %w", err)
	}
	return len(filtered), nil
}

// csvSafeCell neutralizes spreadsheet formula injection (=, +, -, @, tab, CR).
func csvSafeCell(v string) string {
	if v == "" {
		return ""
	}
	switch v[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + v
	}
	return v
}

func taskItemTypeLabel(t int) string {
	switch t {
	case 0:
		return "复制"
	case 1:
		return "删除"
	case 2:
		return "移动(目标端)"
	case 3:
		return "移动"
	default:
		return strconv.Itoa(t)
	}
}

func taskItemStatusLabel(s int) string {
	switch s {
	case 0:
		return "等待中"
	case 1:
		return "执行中"
	case 2:
		return "成功"
	case 4:
		return "已中止"
	case 5:
		return "超时"
	case 6:
		return "系统失败"
	case 7:
		return "失败"
	case 8:
		return "重试中"
	default:
		return strconv.Itoa(s)
	}
}

// ExportTaskItems exports filtered task items for a task as CSV.
func ExportTaskItems(params map[string]interface{}, format string) (string, string, []byte, error) {
	if format := strings.ToLower(strings.TrimSpace(format)); format != "" && format != "csv" {
		return "", "", nil, publicError("仅支持 CSV 导出")
	}
	taskID := util.ToInt64(params["taskId"])
	if taskID <= 0 {
		return "", "", nil, publicError(msg.T(msg.LostPart))
	}
	if _, err := mapper.GetJobTaskByID(taskID); err != nil {
		return "", "", nil, publicErrorIf(err, msg.T(msg.TaskNotFound))
	}
	items, err := mapper.GetJobTaskItemsForExport(params, 50000)
	if err != nil {
		return "", "", nil, fmt.Errorf("export task items: %w", err)
	}

	var buf bytes.Buffer
	// UTF-8 BOM for clean Chinese display in Excel.
	buf.Write([]byte{0xEF, 0xBB, 0xBF})
	w := csv.NewWriter(&buf)
	_ = w.Write([]string{"ID", "时间", "操作类型", "对象类型", "文件名", "源路径", "目标路径", "文件大小(B)", "状态", "错误信息"})
	for _, item := range items {
		ts := ""
		if ct := util.ToInt64(item["createTime"]); ct > 0 {
			ts = time.Unix(ct, 0).In(schedulerLocation()).Format("2006-01-02 15:04:05")
		}
		objType := "文件"
		if util.ToInt(item["isPath"]) == 1 {
			objType = "目录"
		}
		_ = w.Write([]string{
			strconv.FormatInt(util.ToInt64(item["id"]), 10),
			ts,
			taskItemTypeLabel(util.ToInt(item["type"])),
			objType,
			csvSafeCell(util.StringValue(item["fileName"])),
			csvSafeCell(util.StringValue(item["srcPath"])),
			csvSafeCell(util.StringValue(item["dstPath"])),
			strconv.FormatInt(util.ToInt64(item["fileSize"]), 10),
			taskItemStatusLabel(util.ToInt(item["status"])),
			csvSafeCell(util.StringValue(item["errMsg"])),
		})
	}
	w.Flush()
	filename := fmt.Sprintf("opensync-task-%d-items.csv", taskID)
	return filename, "text/csv; charset=utf-8", buf.Bytes(), nil
}
