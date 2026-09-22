package handler

import (
	"opensync/internal/msg"
	"opensync/internal/service"

	"github.com/gin-gonic/gin"
)

// GetAlist handles GET /svr/alist
func GetAlist(c *gin.Context) {
	// Check if it's a path select request
	alistIDStr := c.Query("alistId")
	path := c.Query("path")
	if alistIDStr != "" && path != "" {
		alistID, err := parseRequiredID(alistIDStr)
		if err != nil {
			respondError(c, err.Error())
			return
		}
		handleService(c, func() (interface{}, error) {
			return service.GetChildPath(c.Request.Context(), alistID, path)
		})
		return
	}
	// Return client list
	handleService(c, func() (interface{}, error) {
		return service.GetClientList()
	})
}

// AddAlist handles POST /svr/alist
func AddAlist(c *gin.Context) {
	var req map[string]interface{}
	if !bindJSON(c, &req) {
		return
	}
	handleServiceVoid(c, func() error {
		return service.AddClient(req)
	})
}

// UpdateAlist handles PUT /svr/alist
func UpdateAlist(c *gin.Context) {
	var req map[string]interface{}
	if !bindJSON(c, &req) {
		return
	}
	handleServiceVoid(c, func() error {
		return service.UpdateClient(req)
	})
}

// TestAlist handles POST /svr/alist/test
func TestAlist(c *gin.Context) {
	idStr := c.Query("id")
	if idStr == "" {
		respondError(c, msg.T(msg.LostPart))
		return
	}
	id, err := parseRequiredID(idStr)
	if err != nil {
		respondError(c, err.Error())
		return
	}
	handleServiceVoid(c, func() error {
		return service.TestClient(c.Request.Context(), id)
	})
}

// DeleteAlist handles DELETE /svr/alist
func DeleteAlist(c *gin.Context) {
	idStr := c.Query("id")
	if idStr == "" {
		// Try from body/form
		idStr = c.PostForm("id")
	}
	if idStr == "" {
		respondError(c, msg.T(msg.LostPart))
		return
	}
	id, err := parseRequiredID(idStr)
	if err != nil {
		respondError(c, err.Error())
		return
	}
	handleServiceVoid(c, func() error {
		return service.RemoveClient(id)
	})
}
