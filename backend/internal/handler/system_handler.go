package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"opensync/internal/config"
	"opensync/internal/model"
	"opensync/internal/msg"
	"opensync/internal/service"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// GetSystemConfig handles GET /svr/system/config
func GetSystemConfig(c *gin.Context) {
	respondOK(c, config.GetSystemSettings())
}

// systemSettingsPatch mirrors config.SystemSettings with pointer fields so an
// omitted key can be told apart from an explicit 0. Binding straight into the
// value struct turned every missing field into 0, which silently set the task
// timeout and retention to "unlimited"/"off" and failed range checks for the
// concurrency fields.
type systemSettingsPatch struct {
	TaskTimeout               *int `json:"taskTimeout"`
	TaskSave                  *int `json:"taskSave"`
	CopyConcurrency           *int `json:"copyConcurrency"`
	ScanConcurrency           *int `json:"scanConcurrency"`
	MaxRetries                *int `json:"maxRetries"`
	ScheduledFullSyncConfirm  *int `json:"scheduledFullSyncConfirm"`
	FullSyncApprovalTimeout   *int `json:"fullSyncApprovalTimeout"`
	EngineHealthCheckInterval *int `json:"engineHealthCheckInterval"`
}

// applyTo overlays the provided fields on the current settings.
func (p systemSettingsPatch) applyTo(current config.SystemSettings) config.SystemSettings {
	next := current
	for _, field := range []struct {
		src *int
		dst *int
	}{
		{p.TaskTimeout, &next.TaskTimeout},
		{p.TaskSave, &next.TaskSave},
		{p.CopyConcurrency, &next.CopyConcurrency},
		{p.ScanConcurrency, &next.ScanConcurrency},
		{p.MaxRetries, &next.MaxRetries},
		{p.ScheduledFullSyncConfirm, &next.ScheduledFullSyncConfirm},
		{p.FullSyncApprovalTimeout, &next.FullSyncApprovalTimeout},
		{p.EngineHealthCheckInterval, &next.EngineHealthCheckInterval},
	} {
		if field.src != nil {
			*field.dst = *field.src
		}
	}
	return next
}

// UpdateSystemConfig handles PUT /svr/system/config. Only the fields present in
// the body change; the rest keep their current values.
func UpdateSystemConfig(c *gin.Context) {
	var patch systemSettingsPatch
	if err := c.ShouldBindJSON(&patch); err != nil {
		if isBodyTooLargeError(err) {
			respondBodyTooLarge(c)
			return
		}
		respondError(c, msg.T(msg.InvalidConfigParams))
		return
	}
	// The merge runs inside the config lock: reading the current settings here
	// and writing later would let two concurrent partial updates each restore
	// the other's fields to their old values.
	if err := config.UpdateSystemSettingsWith(patch.applyTo); err != nil {
		if errors.Is(err, config.ErrConfigWrite) {
			// The cause names the config path and the OS error; keep that in the
			// service log rather than handing it to the browser.
			log.Printf("save system settings failed: %v", err)
			c.JSON(500, model.Error(msg.T(msg.ConfigSaveFail)))
			return
		}
		respondError(c, err.Error())
		return
	}
	// Applying the new retention window can delete a large backlog and VACUUM
	// the database. That must not hold the config response open, and nothing in
	// the response depends on it.
	service.StartTaskRetentionCleanupAsync()
	respondOK(c, config.GetSystemSettings())
}

// GetDashboard handles GET /svr/dashboard
func GetDashboard(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	handleService(c, func() (interface{}, error) {
		return service.GetDashboardOverview()
	})
}

// ExportSystemConfig handles GET /svr/system/export
func ExportSystemConfig(c *gin.Context) {
	includeSecrets := c.Query("includeSecrets") == "1" || c.Query("includeSecrets") == "true"
	download := c.Query("download") == "1" || c.Query("download") == "true"
	payload, err := service.ExportSystemBackup(includeSecrets)
	if err != nil {
		respondServiceError(c, err)
		return
	}
	if download {
		data, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			respondInternalError(c, err)
			return
		}
		filename := fmt.Sprintf("opensync-backup-%s.json", time.Now().Format("20060102-150405"))
		c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
		c.Header("Cache-Control", "no-store")
		c.Data(http.StatusOK, "application/json; charset=utf-8", data)
		return
	}
	c.Header("Cache-Control", "no-store")
	respondOK(c, payload)
}

// ImportSystemConfig handles POST /svr/system/import
func ImportSystemConfig(c *gin.Context) {
	var req struct {
		ConflictMode  string                       `json:"conflictMode"`
		Payload       *service.SystemBackupPayload `json:"payload"`
		Version       int                          `json:"version"`
		ExportedAt    int64                        `json:"exportedAt"`
		HasSecrets    bool                         `json:"hasSecrets"`
		Settings      *config.SystemSettings       `json:"settings"`
		Engines       []map[string]interface{}     `json:"engines"`
		Notifications []map[string]interface{}     `json:"notifications"`
		Jobs          []map[string]interface{}     `json:"jobs"`
	}
	if !bindJSON(c, &req) {
		return
	}
	var payload service.SystemBackupPayload
	if req.Payload != nil {
		payload = *req.Payload
	} else {
		payload = service.SystemBackupPayload{
			Version:       req.Version,
			ExportedAt:    req.ExportedAt,
			HasSecrets:    req.HasSecrets,
			Settings:      req.Settings,
			Engines:       req.Engines,
			Notifications: req.Notifications,
			Jobs:          req.Jobs,
		}
	}
	if payload.Settings == nil && len(payload.Engines) == 0 && len(payload.Notifications) == 0 && len(payload.Jobs) == 0 {
		respondError(c, msg.T(msg.BackupPayloadInvalid))
		return
	}
	handleService(c, func() (interface{}, error) {
		return service.ImportSystemBackup(payload, req.ConflictMode)
	})
}

// GetSystemLogs handles GET /svr/system/logs
func GetSystemLogs(c *gin.Context) {
	lines := 200
	if raw := c.Query("lines"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			lines = parsed
		}
	}
	level := c.Query("level")
	keyword := c.Query("keyword")
	c.Header("Cache-Control", "no-store")
	respondOK(c, service.GetAppLogs(lines, level, keyword))
}

// DownloadSystemLogs handles GET /svr/system/logs/download
func DownloadSystemLogs(c *gin.Context) {
	lines := 2000
	if raw := c.Query("lines"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			lines = parsed
		}
	}
	level := c.Query("level")
	keyword := c.Query("keyword")
	text := service.ExportAppLogs(lines, level, keyword)
	filename := fmt.Sprintf("opensync-logs-%s.log", time.Now().Format("20060102-150405"))
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(text))
}
