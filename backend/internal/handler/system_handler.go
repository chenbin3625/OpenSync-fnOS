package handler

import (
	"errors"
	"log"
	"opensync/internal/config"
	"opensync/internal/model"
	"opensync/internal/msg"
	"opensync/internal/service"

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
	TaskTimeout     *int `json:"taskTimeout"`
	TaskSave        *int `json:"taskSave"`
	CopyConcurrency *int `json:"copyConcurrency"`
	ScanConcurrency *int `json:"scanConcurrency"`
	MaxRetries      *int `json:"maxRetries"`
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
	if c.ShouldBindJSON(&patch) != nil {
		c.JSON(400, model.Error(msg.T(msg.InvalidConfigParams)))
		return
	}
	next := patch.applyTo(config.GetSystemSettings())
	if err := config.UpdateSystemSettings(next); err != nil {
		if errors.Is(err, config.ErrConfigWrite) {
			// The cause names the config path and the OS error; keep that in the
			// service log rather than handing it to the browser.
			log.Printf("save system settings failed: %v", err)
			c.JSON(500, model.Error(msg.T(msg.ConfigSaveFail)))
			return
		}
		c.JSON(400, model.Error(err.Error()))
		return
	}
	// Applying the new retention window can delete a large backlog and VACUUM
	// the database. That must not hold the config response open, and nothing in
	// the response depends on it.
	service.StartTaskRetentionCleanupAsync()
	respondOK(c, config.GetSystemSettings())
}
