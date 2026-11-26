package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"opensync/internal/msg"
	"opensync/internal/service"
	"opensync/pkg/util"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// GetJob handles GET /svr/job
func GetJob(c *gin.Context) {
	idStr := c.Query("id")
	taskIDStr := c.Query("taskId")
	current := c.Query("current")
	if current != "" {
		c.Header("Cache-Control", "no-store")
	}

	if idStr != "" {
		id, err := parseRequiredID(idStr)
		if err != nil {
			respondError(c, msg.T(msg.LostPart))
			return
		}
		// Check for current (real-time progress)
		if current != "" {
			req := map[string]interface{}{
				"status":             c.Query("status"),
				"pageSize":           c.Query("pageSize"),
				"pageNum":            c.Query("pageNum"),
				"expectedTaskId":     c.Query("expectedTaskId"),
				"expectedCreateTime": c.Query("expectedCreateTime"),
			}
			removeEmptyStringValues(req)
			handleService(c, func() (interface{}, error) { return service.GetJobCurrent(id, req) })
			return
		}
		// Task list for this job
		req := map[string]interface{}{
			"id":               id,
			"pageSize":         c.Query("pageSize"),
			"pageNum":          c.Query("pageNum"),
			"status":           c.Query("status"),
			"keyword":          c.Query("keyword"),
			"startTime":        c.Query("startTime"),
			"endTime":          c.Query("endTime"),
			"endTimeExclusive": c.Query("endTimeExclusive"),
		}
		if statusIn := c.QueryArray("statusIn"); len(statusIn) > 0 {
			req["statusIn"] = statusIn
		}
		removeEmptyStringValues(req)
		handleService(c, func() (interface{}, error) { return service.GetTaskList(req) })
		return
	}

	if taskIDStr != "" {
		// Task item list
		req := map[string]interface{}{
			"taskId":   taskIDStr,
			"pageSize": c.Query("pageSize"),
			"pageNum":  c.Query("pageNum"),
			"status":   c.Query("status"),
			"type":     c.Query("type"),
			"isPath":   c.Query("isPath"),
			"hasError": c.Query("hasError"),
			"keyword":  c.Query("keyword"),
		}
		// Remove empty params
		removeEmptyStringValues(req)
		handleService(c, func() (interface{}, error) { return service.GetTaskItemList(req) })
		return
	}

	// Job list (paginated)
	req := map[string]interface{}{
		"pageSize": c.Query("pageSize"),
		"pageNum":  c.Query("pageNum"),
	}
	removeEmptyStringValues(req)
	handleService(c, func() (interface{}, error) { return service.GetJobList(req) })
}

func removeEmptyStringValues(req map[string]interface{}) {
	for k, v := range req {
		if v == "" {
			delete(req, k)
		}
	}
}

func validateJobFields(req map[string]interface{}) error {
	if v, ok := req["enable"]; ok {
		if _, err := parseEnableValue(v); err != nil {
			return errors.New(msg.T(msg.LostPart))
		}
	}
	// Numeric fields must really be integers. util.ToInt turned "abc" into 0
	// and 1.9 into 1, so junk passed the range checks and was stored. null
	// and "" mean "not set" and are left to the service defaults.
	if v, ok := req["interval"]; ok && v != nil && v != "" {
		interval, isInt := strictInt64(v)
		if !isInt || interval < 0 {
			return errors.New(msg.T(msg.LostPart))
		}
	}
	if v, ok := req["method"]; ok && v != nil && v != "" {
		method, isInt := strictInt64(v)
		if !isInt || method < 0 || method > 3 {
			return errors.New(msg.T(msg.LostPart))
		}
	}
	for _, field := range []string{"srcPath", "dstPath"} {
		if v, ok := req[field]; ok {
			if hasParentPathSegment(v) {
				return errors.New(msg.T(msg.LostPart))
			}
		}
	}
	return nil
}

// hasParentPathSegment reports whether one of the selected paths contains a
// ".." path segment, the only form that can traverse out of the share. A raw
// strings.Contains(s, "..") rejected legal directory names such as
// "backup..2024"; delegation to the service layer cleans paths but keeps a
// leading "..", so the guard stays here as a segment comparison.
func hasParentPathSegment(value interface{}) bool {
	switch v := value.(type) {
	case string:
		raw := strings.TrimSpace(v)
		if strings.HasPrefix(raw, "[") {
			var paths []string
			if err := json.Unmarshal([]byte(raw), &paths); err == nil {
				return hasParentPathSegment(paths)
			}
		}
		return pathHasParentSegment(raw)
	case []string:
		for _, item := range v {
			if pathHasParentSegment(item) {
				return true
			}
		}
		return false
	case []interface{}:
		for _, item := range v {
			if hasParentPathSegment(item) {
				return true
			}
		}
		return false
	default:
		return pathHasParentSegment(util.StringValue(value))
	}
}

func pathHasParentSegment(value string) bool {
	for _, segment := range strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '\\' }) {
		if segment == ".." {
			return true
		}
	}
	return false
}

// AddJob handles POST /svr/job
var stopTaskService = service.StopTask
var pauseTaskService = service.PauseTaskTransfer
var resumeTaskService = service.ResumeTaskTransfer
var createJobService = service.CreateJobClient

func AddJob(c *gin.Context) {
	var req map[string]interface{}
	if !bindJSON(c, &req) {
		return
	}
	if req == nil {
		respondError(c, msg.T(msg.LostPart))
		return
	}
	if err := validateJobFields(req); err != nil {
		respondError(c, err.Error())
		return
	}
	// POST with an "id" edits that job. PUT /svr/job is reserved for actions
	// (run/pause/resume/stop/retry), and the bundled UI saves both new and
	// edited jobs through POST (frontend/src/api/client.ts saveJob), so this
	// upsert behaviour is kept for compatibility. The id must be a valid
	// positive integer; a malformed one is rejected instead of being treated as
	// a create or reaching the service layer.
	if rawID, hasID := req["id"]; hasID && rawID != nil && rawID != "" {
		if _, err := parseIDValue(rawID); err != nil {
			respondError(c, err.Error())
			return
		}
		handleServiceVoid(c, func() error { return service.EditJobClient(req) })
	} else {
		// Return the created id so the UI can navigate straight to the new task
		// (matching POST /svr/notify).
		handleService(c, func() (interface{}, error) {
			id, err := createJobService(req)
			if err != nil {
				return nil, err
			}
			return gin.H{"id": id}, nil
		})
	}
}

// UpdateJob handles PUT /svr/job
func UpdateJob(c *gin.Context) {
	var req struct {
		// ids arrive as strings from the bundled UI and as numbers from
		// other clients; both are accepted, fractions and junk are not.
		ID      *flexibleID  `json:"id"`
		TaskID  *flexibleID  `json:"taskId"`
		Action  *string      `json:"action"`
		Pause   *bool        `json:"pause"`
		Abort   *bool        `json:"abort"`
		ItemIDs []flexibleID `json:"itemIds"`
		BwLimit *string      `json:"bwLimit"`
		Paths   []string     `json:"paths"`
	}
	if !bindJSONStrict(c, &req) {
		return
	}

	if req.TaskID != nil {
		if req.ID != nil || req.Pause != nil || req.Abort != nil || req.Action == nil {
			respondError(c, msg.T(msg.LostPart))
			return
		}
		if len(req.ItemIDs) > 0 && *req.Action != "retry" {
			respondError(c, msg.T(msg.LostPart))
			return
		}
		if req.BwLimit != nil && *req.Action != "bwlimit" {
			respondError(c, msg.T(msg.LostPart))
			return
		}
		if len(req.Paths) > 0 && *req.Action != "approve" {
			respondError(c, msg.T(msg.LostPart))
			return
		}
		taskID, err := req.TaskID.parse()
		if err != nil {
			respondError(c, err.Error())
			return
		}
		switch *req.Action {
		case "stop":
			handleServiceVoid(c, func() error { return stopTaskService(taskID) })
		case "pause":
			handleServiceVoid(c, func() error { return pauseTaskService(taskID) })
		case "resume":
			handleServiceVoid(c, func() error { return resumeTaskService(taskID) })
		case "bwlimit":
			rate := ""
			if req.BwLimit != nil {
				rate = *req.BwLimit
			}
			handleServiceVoid(c, func() error { return service.SetTaskLiveBwLimit(c.Request.Context(), taskID, rate) })
		case "retry":
			var itemIDs []int64
			if len(req.ItemIDs) > 0 {
				itemIDs = make([]int64, 0, len(req.ItemIDs))
				for i := range req.ItemIDs {
					itemID, err := req.ItemIDs[i].parse()
					if err != nil {
						respondError(c, err.Error())
						return
					}
					itemIDs = append(itemIDs, itemID)
				}
			}
			handleServiceVoid(c, func() error { return service.RetryFailedTaskItems(taskID, itemIDs) })
		case "approve":
			if len(req.Paths) > 0 {
				handleServiceVoid(c, func() error { return service.ApproveFullSyncTaskPaths(taskID, req.Paths) })
			} else {
				handleServiceVoid(c, func() error { return service.ApproveFullSyncTask(taskID) })
			}
		case "reject":
			handleServiceVoid(c, func() error { return service.RejectFullSyncTask(taskID) })
		default:
			respondError(c, msg.T(msg.LostPart))
		}
		return
	}

	if req.Action != nil || len(req.ItemIDs) > 0 || req.BwLimit != nil || len(req.Paths) > 0 {
		respondError(c, msg.T(msg.LostPart))
		return
	}

	if req.Pause == nil {
		if req.Abort != nil {
			respondError(c, msg.T(msg.LostPart))
			return
		}
		// Manual execution. The id is mandatory: an empty body ({}), or any
		// malformed request that happened to drop it, used to fall through to
		// service.DoAllJobManual and start every enabled job at once.
		if req.ID == nil {
			respondError(c, msg.T(msg.LostPart))
			return
		}
		id, err := req.ID.parse()
		if err != nil {
			respondError(c, err.Error())
			return
		}
		handleServiceVoid(c, func() error { return service.DoJobManual(id) })
	} else if *req.Pause {
		// Disable or abort
		if req.ID == nil {
			respondError(c, msg.T(msg.LostPart))
			return
		}
		id, err := req.ID.parse()
		if err != nil {
			respondError(c, err.Error())
			return
		}
		if req.Abort != nil && *req.Abort {
			handleServiceVoid(c, func() error { return service.AbortJob(id) })
		} else {
			handleServiceVoid(c, func() error { return service.PauseJob(id) })
		}
	} else {
		// Enable
		if req.Abort != nil && *req.Abort {
			respondError(c, msg.T(msg.LostPart))
			return
		}
		if req.ID == nil {
			respondError(c, msg.T(msg.LostPart))
			return
		}
		id, err := req.ID.parse()
		if err != nil {
			respondError(c, err.Error())
			return
		}
		handleServiceVoid(c, func() error { return service.ContinueJob(id) })
	}
}

// DeleteJob handles DELETE /svr/job
func DeleteJob(c *gin.Context) {
	idStr := c.Query("id")
	taskIDStr := c.Query("taskId")

	if idStr != "" {
		id, err := parseRequiredID(idStr)
		if err != nil {
			respondError(c, err.Error())
			return
		}
		handleServiceVoid(c, func() error { return service.RemoveJobClient(id) })
	} else if taskIDStr != "" {
		taskID, err := parseRequiredID(taskIDStr)
		if err != nil {
			respondError(c, err.Error())
			return
		}
		handleServiceVoid(c, func() error { return service.RemoveTask(taskID) })
	} else {
		respondError(c, msg.T(msg.LostPart))
	}
}

// BatchJobAction handles POST /svr/job/batch
func BatchJobAction(c *gin.Context) {
	var req struct {
		Action string       `json:"action"`
		IDs    []flexibleID `json:"ids"`
	}
	if !bindJSONStrict(c, &req) {
		return
	}
	if strings.TrimSpace(req.Action) == "" {
		respondError(c, msg.T(msg.BatchActionInvalid))
		return
	}
	jobIDs := make([]int64, 0, len(req.IDs))
	for i := range req.IDs {
		id, err := req.IDs[i].parse()
		if err != nil {
			respondError(c, err.Error())
			return
		}
		jobIDs = append(jobIDs, id)
	}
	handleService(c, func() (interface{}, error) {
		return service.BatchJobAction(req.Action, jobIDs)
	})
}

// DeleteTasksBatch handles POST /svr/job/tasks/delete
func DeleteTasksBatch(c *gin.Context) {
	var req struct {
		TaskIDs    []flexibleID `json:"taskIds"`
		ClearJobID *flexibleID  `json:"clearJobId"`
	}
	if !bindJSONStrict(c, &req) {
		return
	}
	if req.ClearJobID != nil {
		jobID, err := req.ClearJobID.parse()
		if err != nil {
			respondError(c, err.Error())
			return
		}
		handleService(c, func() (interface{}, error) {
			deleted, err := service.ClearJobHistory(jobID)
			if err != nil {
				return nil, err
			}
			return gin.H{"deleted": deleted}, nil
		})
		return
	}
	if len(req.TaskIDs) == 0 {
		respondError(c, msg.T(msg.LostPart))
		return
	}
	taskIDs := make([]int64, 0, len(req.TaskIDs))
	for i := range req.TaskIDs {
		tid, err := req.TaskIDs[i].parse()
		if err != nil {
			respondError(c, err.Error())
			return
		}
		taskIDs = append(taskIDs, tid)
	}
	handleService(c, func() (interface{}, error) {
		deleted, err := service.RemoveTasksBatch(taskIDs)
		if err != nil {
			return nil, err
		}
		return gin.H{"deleted": deleted}, nil
	})
}

// ExportTaskItems handles GET /svr/job/items/export
func ExportTaskItems(c *gin.Context) {
	taskIDStr := c.Query("taskId")
	if _, err := parseRequiredID(taskIDStr); err != nil {
		respondError(c, msg.T(msg.LostPart))
		return
	}
	req := map[string]interface{}{
		"taskId":   taskIDStr,
		"status":   c.Query("status"),
		"type":     c.Query("type"),
		"isPath":   c.Query("isPath"),
		"hasError": c.Query("hasError"),
		"keyword":  c.Query("keyword"),
	}
	removeEmptyStringValues(req)
	format := c.Query("format")
	filename, contentType, data, err := service.ExportTaskItems(req, format)
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, contentType, data)
}

// StartDryRun handles POST /svr/job/dry-run
func StartDryRun(c *gin.Context) {
	var jobID int64
	if idStr := c.Query("id"); idStr != "" {
		id, err := parseRequiredID(idStr)
		if err != nil {
			respondError(c, err.Error())
			return
		}
		jobID = id
	} else {
		var req struct {
			ID *flexibleID `json:"id"`
		}
		if !bindJSONStrict(c, &req) {
			return
		}
		id, err := req.ID.parse()
		if err != nil {
			respondError(c, err.Error())
			return
		}
		jobID = id
	}
	handleService(c, func() (interface{}, error) {
		return service.StartDryRun(jobID)
	})
}

// GetDryRun handles GET /svr/job/dry-run
func GetDryRun(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, err := parseRequiredID(c.Query("id"))
	if err != nil {
		respondError(c, err.Error())
		return
	}
	handleService(c, func() (interface{}, error) {
		return service.GetDryRun(id)
	})
}

// CancelDryRun handles DELETE /svr/job/dry-run
func CancelDryRun(c *gin.Context) {
	id, err := parseRequiredID(c.Query("id"))
	if err != nil {
		respondError(c, err.Error())
		return
	}
	handleServiceVoid(c, func() error {
		return service.CancelDryRun(id)
	})
}

// StartJobCheck handles POST /svr/job/check
func StartJobCheck(c *gin.Context) {
	var req struct {
		ID         *flexibleID `json:"id"`
		Download   bool        `json:"download"`
		CryptCheck bool        `json:"cryptCheck"`
	}
	if !bindJSONStrict(c, &req) {
		return
	}
	if req.ID == nil {
		respondError(c, msg.T(msg.LostPart))
		return
	}
	jobID, err := req.ID.parse()
	if err != nil {
		respondError(c, err.Error())
		return
	}
	handleService(c, func() (interface{}, error) {
		return service.StartJobCheck(jobID, req.Download, req.CryptCheck)
	})
}

// ResetBisyncJob handles POST /svr/job/bisync/resync
func ResetBisyncJob(c *gin.Context) {
	var req struct {
		ID     *flexibleID `json:"id"`
		RunNow bool        `json:"runNow"`
	}
	if !bindJSONStrict(c, &req) {
		return
	}
	if req.ID == nil {
		respondError(c, msg.T(msg.LostPart))
		return
	}
	jobID, err := req.ID.parse()
	if err != nil {
		respondError(c, err.Error())
		return
	}
	handleServiceVoid(c, func() error {
		return service.ResetBisyncJob(jobID, req.RunNow)
	})
}

// GetJobCheck handles GET /svr/job/check
func GetJobCheck(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	id, err := parseRequiredID(c.Query("id"))
	if err != nil {
		respondError(c, err.Error())
		return
	}
	handleService(c, func() (interface{}, error) {
		return service.GetJobCheck(id)
	})
}

// CancelJobCheck handles DELETE /svr/job/check
func CancelJobCheck(c *gin.Context) {
	id, err := parseRequiredID(c.Query("id"))
	if err != nil {
		respondError(c, err.Error())
		return
	}
	handleServiceVoid(c, func() error {
		return service.CancelJobCheck(id)
	})
}

// ExportJobCheck handles GET /svr/job/check/export
func ExportJobCheck(c *gin.Context) {
	id, err := parseRequiredID(c.Query("id"))
	if err != nil {
		respondError(c, err.Error())
		return
	}
	filename, contentType, data, err := service.ExportJobCheckReport(id, c.Query("format"))
	if err != nil {
		respondServiceError(c, err)
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, contentType, data)
}

// RepairJobCheck handles POST /svr/job/check/repair
func RepairJobCheck(c *gin.Context) {
	var req struct {
		ID *flexibleID `json:"id"`
	}
	if !bindJSONStrict(c, &req) {
		return
	}
	if req.ID == nil {
		respondError(c, msg.T(msg.LostPart))
		return
	}
	jobID, err := req.ID.parse()
	if err != nil {
		respondError(c, err.Error())
		return
	}
	handleService(c, func() (interface{}, error) {
		count, repairErr := service.RepairJobCheckResult(jobID)
		if repairErr != nil {
			return nil, repairErr
		}
		return gin.H{"repairCount": count}, nil
	})
}

// ExcludePendingApproval handles POST /svr/job/approval/exclude
func ExcludePendingApproval(c *gin.Context) {
	var req struct {
		TaskID *flexibleID `json:"taskId"`
		Paths  []string    `json:"paths"`
	}
	if !bindJSONStrict(c, &req) {
		return
	}
	if req.TaskID == nil || len(req.Paths) == 0 {
		respondError(c, msg.T(msg.LostPart))
		return
	}
	taskID, err := req.TaskID.parse()
	if err != nil {
		respondError(c, err.Error())
		return
	}
	handleService(c, func() (interface{}, error) {
		added, exErr := service.ExcludeFullSyncApprovalPaths(taskID, req.Paths)
		if exErr != nil {
			return nil, exErr
		}
		return gin.H{"excludedCount": added}, nil
	})
}

// TestJobFilter handles POST /svr/job/filter/test
func TestJobFilter(c *gin.Context) {
	var req map[string]interface{}
	if !bindJSON(c, &req) {
		return
	}
	if req == nil {
		respondError(c, msg.T(msg.LostPart))
		return
	}
	handleService(c, func() (interface{}, error) {
		return service.TestJobFilter(req)
	})
}

// GetPendingApproval handles GET /svr/job/approval.
// With jobId it returns that job's pending deletion approval (or null);
// optional query params offset, limit, q, type filter and paginate the target paths.
// Without jobId it returns all pending deletion approvals.
func GetPendingApproval(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if rawJobID := c.Query("jobId"); rawJobID != "" {
		jobID, err := parseRequiredID(rawJobID)
		if err != nil {
			respondError(c, err.Error())
			return
		}
		rawOffset := strings.TrimSpace(c.Query("offset"))
		rawLimit := strings.TrimSpace(c.Query("limit"))
		query := strings.TrimSpace(c.Query("q"))
		kind := strings.TrimSpace(c.Query("type"))
		if rawOffset != "" || rawLimit != "" || query != "" || kind != "" {
			offset, _ := strconv.Atoi(rawOffset)
			limit, _ := strconv.Atoi(rawLimit)
			handleService(c, func() (interface{}, error) {
				return service.GetPendingApprovalFiltered(jobID, offset, limit, query, kind), nil
			})
			return
		}
		handleService(c, func() (interface{}, error) {
			return service.GetPendingApproval(jobID), nil
		})
		return
	}
	handleService(c, func() (interface{}, error) {
		return service.ListPendingApprovals(), nil
	})
}
