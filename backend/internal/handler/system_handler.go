package handler

import (
	"opensync/internal/config"
	"opensync/internal/service"

	"github.com/gin-gonic/gin"
)

// GetSystemConfig handles GET /svr/system/config
func GetSystemConfig(c *gin.Context) {
	respondOK(c, config.GetSystemSettings())
}

// UpdateSystemConfig handles PUT /svr/system/config
func UpdateSystemConfig(c *gin.Context) {
	var req config.SystemSettings
	if !bindJSON(c, &req) {
		return
	}
	if err := config.UpdateSystemSettings(req); err != nil {
		respondError(c, err.Error())
		return
	}
	service.RunTaskRetentionCleanup()
	respondOK(c, config.GetSystemSettings())
}
