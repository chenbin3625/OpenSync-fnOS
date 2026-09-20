package handler

import (
	"opensync/internal/msg"
	"opensync/internal/service"

	"github.com/gin-gonic/gin"
)

// GetJob handles GET /svr/job
func GetJob(c *gin.Context) {
	idStr := c.Query("id")
	taskIDStr := c.Query("taskId")

	if idStr != "" {
		id, err := parseRequiredID(idStr, "id")
		if err != nil {
			respondError(c, msg.LostPart)
			return
		}
		// Check for current (real-time progress)
		current := c.Query("current")
		if current != "" {
			req := map[string]interface{}{
				"status":             c.Query("status"),
				"pageSize":           c.Query("pageSize"),
				"pageNum":            c.Query("pageNum"),
				"expectedTaskId":     c.Query("expectedTaskId"),
				"expectedCreateTime": c.Query("expectedCreateTime"),
			}
			removeEmptyStringValues(req)
			result := service.GetJobCurrent(id, req)
			respondOK(c, result)
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
		result := service.GetTaskList(req)
		respondOK(c, result)
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
		result := service.GetTaskItemList(req)
		respondOK(c, result)
		return
	}

	// Job list (paginated)
	req := map[string]interface{}{
		"pageSize": c.Query("pageSize"),
		"pageNum":  c.Query("pageNum"),
	}
	removeEmptyStringValues(req)
	result := service.GetJobList(req)
	respondOK(c, result)
}

func removeEmptyStringValues(req map[string]interface{}) {
	for k, v := range req {
		if v == "" {
			delete(req, k)
		}
	}
}

// AddJob handles POST /svr/job
func AddJob(c *gin.Context) {
	var req map[string]interface{}
	if !bindJSON(c, &req) {
		return
	}
	// Check if it's an edit (has 'id') or add
	if _, hasID := req["id"]; hasID {
		service.EditJobClient(req)
	} else {
		service.AddJobClient(req, false)
	}
	respondOK(c, nil)
}

// UpdateJob handles PUT /svr/job
func UpdateJob(c *gin.Context) {
	var req struct {
		ID     *string `json:"id"`
		TaskID *string `json:"taskId"`
		Action string  `json:"action"`
		Pause  *bool   `json:"pause"`
		Abort  *bool   `json:"abort"`
	}
	if !bindJSON(c, &req) {
		return
	}

	if req.TaskID != nil {
		taskID, err := parseRequiredID(*req.TaskID, "taskId")
		if err != nil {
			respondError(c, err.Error())
			return
		}
		switch req.Action {
		case "stop":
			service.StopTask(taskID)
		case "retry":
			service.RetryFailedTask(taskID)
		default:
			respondError(c, msg.LostPart)
			return
		}
		respondOK(c, nil)
		return
	}

	if req.Pause == nil {
		// Manual execution
		if req.ID != nil {
			id, err := parseRequiredID(*req.ID, "id")
			if err != nil {
				respondError(c, err.Error())
				return
			}
			service.DoJobManual(id)
		} else {
			service.DoAllJobManual()
		}
	} else if *req.Pause {
		// Disable or abort
		if req.ID == nil {
			respondError(c, msg.LostPart)
			return
		}
		id, err := parseRequiredID(*req.ID, "id")
		if err != nil {
			respondError(c, err.Error())
			return
		}
		if req.Abort != nil && *req.Abort {
			service.AbortJob(id)
		} else {
			service.PauseJob(id)
		}
	} else {
		// Enable
		if req.ID == nil {
			respondError(c, msg.LostPart)
			return
		}
		id, err := parseRequiredID(*req.ID, "id")
		if err != nil {
			respondError(c, err.Error())
			return
		}
		service.ContinueJob(id)
	}
	respondOK(c, nil)
}

// DeleteJob handles DELETE /svr/job
func DeleteJob(c *gin.Context) {
	idStr := c.Query("id")
	taskIDStr := c.Query("taskId")

	if idStr != "" {
		id, err := parseRequiredID(idStr, "id")
		if err != nil {
			respondError(c, err.Error())
			return
		}
		service.RemoveJobClient(id)
	} else if taskIDStr != "" {
		taskID, err := parseRequiredID(taskIDStr, "taskId")
		if err != nil {
			respondError(c, err.Error())
			return
		}
		service.RemoveTask(taskID)
	} else {
		respondError(c, msg.LostPart)
		return
	}
	respondOK(c, nil)
}
