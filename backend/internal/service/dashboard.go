package service

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"opensync/internal/mapper"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"strconv"
	"strings"
	"time"
)

// GetDashboardOverview aggregates KPI counters, pending full-sync approvals,
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
	since7d := time.Now().AddDate(0, 0, -7).Unix()
	var totalRuns, successRuns, failRuns, stoppedRuns, totalFiles, totalBytes int64
	if err := mapper.ForEachRecentTask(since7d, func(status int, taskNum string) error {
		totalRuns++
		switch status {
		case 2, 8:
			successRuns++
		case 3, 5, 6, 7, 10:
			failRuns++
		case 4:
			stoppedRuns++
		}
		if parsed := parseTaskNumJSON(taskNum); parsed != nil {
			totalFiles += util.ToInt64(parsed["successNum"])
			totalBytes += util.ToInt64(parsed["sumSize"])
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("dashboard load recent tasks: %w", err)
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
	jobClientListMu.RLock()
	clientsCopy := make(map[int64]*JobClient, len(jobClientList))
	for id, c := range jobClientList {
		clientsCopy[id] = c
	}
	jobClientListMu.RUnlock()

	enabledJobs := 0
	runningJobs := 0
	queuedJobs := 0
	pendingApprovals := make([]map[string]interface{}, 0)
	jobSummaries := make([]map[string]interface{}, 0, len(jobs))

	for _, job := range jobs {
		jobID := util.ToInt64(job["id"])
		alistID := util.ToInt64(job["alistId"])
		enabled := util.ToInt(job["enable"]) == 1
		if enabled {
			enabledJobs++
		}
		queued := queuedSet[jobID]
		if queued {
			queuedJobs++
		}
		nextRunTime := int64(0)
		running := false
		currentTaskID := int64(0)
		var approval *FullSyncApproval
		var liveNum *taskNumStats
		liveProgress := 0.0

		if client := clientsCopy[jobID]; client != nil {
			if nextTime, ok := client.nextRunTime(); ok {
				nextRunTime = nextTime.Unix()
			}
			if task := client.currentTask(); task != nil {
				running = true
				runningJobs++
				currentTaskID = task.TaskID
				approval = task.fullSyncApprovalSnapshot()
				payload := task.getCurrentPayload()
				numCopy := payload.Num
				liveNum = &numCopy
				total := payload.DoneSize + payload.RemainSize
				if total > 0 {
					liveProgress = float64(payload.DoneSize) * 100.0 / float64(total)
				}
				if approval != nil {
					jobName := strings.TrimSpace(util.StringValue(job["remark"]))
					if jobName == "" {
						jobName = fmt.Sprintf("任务 #%d", jobID)
					}
					pendingApprovals = append(pendingApprovals, map[string]interface{}{
						"jobId":       jobID,
						"jobName":     jobName,
						"taskId":      task.TaskID,
						"deleteCount": approval.DeleteCount,
						"deleteSize":  approval.DeleteSize,
						"pathCount":   approval.PathCount,
					})
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

		jobSummaries = append(jobSummaries, map[string]interface{}{
			"id":              jobID,
			"remark":          job["remark"],
			"srcPath":         job["srcPath"],
			"dstPath":         job["dstPath"],
			"alistId":         alistID,
			"alistName":       engineNameByID[alistID],
			"method":          util.ToInt(job["method"]),
			"enable":          util.ToInt(job["enable"]),
			"isCron":          util.ToInt(job["isCron"]),
			"interval":        job["interval"],
			"windowEnable":    util.ToInt(job["windowEnable"]),
			"windowStart":     job["windowStart"],
			"windowEnd":       job["windowEnd"],
			"nextRunTime":     nextRunTime,
			"running":         running,
			"queued":          queued,
			"currentTaskId":   currentTaskID,
			"pendingApproval": approval,
			"liveNum":         liveNum,
			"liveProgress":    liveProgress,
			"lastTask":        lastTaskSummary,
		})
	}

	return map[string]interface{}{
		"kpi": map[string]interface{}{
			"totalJobs":        len(jobs),
			"enabledJobs":      enabledJobs,
			"runningJobs":      runningJobs,
			"queuedJobs":       queuedJobs,
			"pendingApprovals": len(pendingApprovals),
			"totalEngines":     len(engines),
			"onlineEngines":    onlineEngines,
			"offlineEngines":   offlineEngines,
		},
		"recent7d": map[string]interface{}{
			"totalRuns":   totalRuns,
			"successRuns": successRuns,
			"failRuns":    failRuns,
			"stoppedRuns": stoppedRuns,
			"totalFiles":  totalFiles,
			"totalBytes":  totalBytes,
		},
		"pendingApprovals": pendingApprovals,
		"jobs":             jobSummaries,
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

// ExportTaskItems exports filtered task items for a task as CSV or TXT.
func ExportTaskItems(params map[string]interface{}, format string) (string, string, []byte, error) {
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

	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "csv"
	}

	if format == "txt" {
		var buf strings.Builder
		for _, item := range items {
			st := taskItemStatusLabel(util.ToInt(item["status"]))
			tp := taskItemTypeLabel(util.ToInt(item["type"]))
			src := util.StringValue(item["srcPath"])
			dst := util.StringValue(item["dstPath"])
			name := util.StringValue(item["fileName"])
			sizeStr := util.ConvertBytes(util.ToInt64(item["fileSize"]))
			errMsg := util.StringValue(item["errMsg"])
			ts := ""
			if ct := util.ToInt64(item["createTime"]); ct > 0 {
				ts = time.Unix(ct, 0).In(schedulerLocation()).Format("2006-01-02 15:04:05")
			}
			line := fmt.Sprintf("[%s] [%s] [%s] %s | %s -> %s | %s", ts, st, tp, name, src, dst, sizeStr)
			if errMsg != "" {
				line += " | 错误: " + errMsg
			}
			buf.WriteString(line + "\n")
		}
		filename := fmt.Sprintf("opensync-task-%d-items.txt", taskID)
		return filename, "text/plain; charset=utf-8", []byte(buf.String()), nil
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
