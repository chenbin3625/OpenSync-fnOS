package handler

import (
	"net/http"
	"opensync/internal/config"
	"opensync/internal/model"
	"opensync/internal/msg"
	"opensync/internal/service"

	"github.com/gin-gonic/gin"
)

// GetSystemConfig handles GET /svr/system/config
func GetSystemConfig(c *gin.Context) {
	c.JSON(http.StatusOK, model.Success(config.GetSystemSettings()))
}

// UpdateSystemConfig handles PUT /svr/system/config
func UpdateSystemConfig(c *gin.Context) {
	var req config.SystemSettings
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, model.Error(msg.LostPart))
		return
	}
	if err := config.UpdateSystemSettings(req); err != nil {
		c.JSON(http.StatusOK, model.Error(err.Error()))
		return
	}
	service.RunTaskRetentionCleanup()
	c.JSON(http.StatusOK, model.Success(config.GetSystemSettings()))
}
