package handler

import (
	"opensync/internal/config"
	"opensync/internal/model"
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
	if c.ShouldBindJSON(&req) != nil {
		c.JSON(400, model.Error("配置参数无效"))
		return
	}
	if err := config.UpdateSystemSettings(req); err != nil {
		c.JSON(400, model.Error(err.Error()))
		return
	}
	service.RunTaskRetentionCleanup()
	respondOK(c, config.GetSystemSettings())
}
