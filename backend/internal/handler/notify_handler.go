package handler

import (
	"opensync/internal/msg"
	"opensync/internal/service"
	"opensync/pkg/util"

	"github.com/gin-gonic/gin"
)

// GetNotify handles GET /svr/notify
func GetNotify(c *gin.Context) {
	result := service.GetNotifyList()
	respondOK(c, result)
}

// AddNotify handles POST /svr/notify
func AddNotify(c *gin.Context) {
	var req struct {
		Notify *map[string]interface{} `json:"notify"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if req.Notify == nil {
		respondError(c, msg.LostPart)
		return
	}

	notify := *req.Notify
	if _, hasEnable := notify["enable"]; !hasEnable {
		// A create request without `enable` is ambiguous; test sends belong on
		// POST /svr/notify/test so this cannot silently fire a real message.
		respondError(c, msg.LostPart)
		return
	}
	// The created id is returned so a client can address the new row directly
	// instead of guessing it from the tail of GET /svr/notify.
	respondOK(c, gin.H{"id": service.AddNewNotify(notify)})
}

// TestNotify handles POST /svr/notify/test
func TestNotify(c *gin.Context) {
	var req struct {
		Notify *map[string]interface{} `json:"notify"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if req.Notify == nil {
		respondError(c, msg.LostPart)
		return
	}
	service.TestNotify(*req.Notify)
	respondOK(c, nil)
}

// UpdateNotify handles PUT /svr/notify
func UpdateNotify(c *gin.Context) {
	var req map[string]interface{}
	if !bindJSON(c, &req) {
		return
	}

	if notifyIDStr, ok := req["notifyId"]; ok {
		// Update status
		notifyID, err := parseRequiredID(util.StringValue(notifyIDStr))
		if err != nil {
			respondError(c, err.Error())
			return
		}
		enableRaw, ok := req["enable"]
		if !ok {
			respondError(c, msg.LostPart)
			return
		}
		enable, err := parseEnableValue(enableRaw)
		if err != nil {
			respondError(c, err.Error())
			return
		}
		service.UpdateNotifyStatus(notifyID, enable)
	} else if notify, ok := req["notify"]; ok {
		// Edit notify
		if nMap, ok := notify.(map[string]interface{}); ok {
			service.EditNotify(nMap)
		} else {
			respondError(c, msg.LostPart)
			return
		}
	} else {
		respondError(c, msg.LostPart)
		return
	}
	respondOK(c, nil)
}

// DeleteNotify handles DELETE /svr/notify
func DeleteNotify(c *gin.Context) {
	notifyIDStr := c.Query("notifyId")
	if notifyIDStr == "" {
		respondError(c, msg.LostPart)
		return
	}
	notifyID, err := parseRequiredID(notifyIDStr)
	if err != nil {
		respondError(c, err.Error())
		return
	}
	service.DeleteNotify(notifyID)
	respondOK(c, nil)
}
