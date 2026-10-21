package handler

import (
	"errors"
	"opensync/internal/msg"
	"opensync/internal/service"
	"opensync/pkg/util"
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
		if !isInt || method < 0 || method > 2 {
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
	case []string:
		for _, item := range v {
			if pathHasParentSegment(item) {
				return true
			}
		}
		return false
	case []interface{}:
		for _, item := range v {
			if pathHasParentSegment(util.StringValue(item)) {
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
func AddJob(c *gin.Context) {
	var req map[string]interface{}
	if !bindJSON(c, &req) {
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
		handleServiceVoid(c, func() error { return service.AddJobClient(req, false) })
	}
}

// UpdateJob handles PUT /svr/job
func UpdateJob(c *gin.Context) {
	var req struct {
		// ids arrive as strings from the bundled UI and as numbers from
		// other clients; both are accepted, fractions and junk are not.
		ID     *flexibleID `json:"id"`
		TaskID *flexibleID `json:"taskId"`
		Action string      `json:"action"`
		Pause  *bool       `json:"pause"`
		Abort  *bool       `json:"abort"`
	}
	if !bindJSON(c, &req) {
		return
	}

	if req.TaskID != nil {
		taskID, err := req.TaskID.parse()
		if err != nil {
			respondError(c, err.Error())
			return
		}
		switch req.Action {
		case "stop":
			handleServiceVoid(c, func() error { return service.StopTask(taskID) })
		case "retry":
			handleServiceVoid(c, func() error { return service.RetryFailedTask(taskID) })
		default:
			respondError(c, msg.T(msg.LostPart))
		}
		return
	}

	if req.Pause == nil {
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
