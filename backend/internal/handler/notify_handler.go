package handler

import (
	"log"
	"opensync/internal/msg"
	"opensync/internal/service"
	"time"

	"github.com/gin-gonic/gin"
)

// notifyTestTimeout bounds the synchronous test send so a slow or wedged
// provider cannot hold the HTTP request (and the admin UI's 90s timeout) for
// the worst case of two WeCom round-trips. The outbound requests keep their own
// client timeout and stay bounded by the notification dispatcher's context.
var notifyTestTimeout = 45 * time.Second

// testNotifyService is swapped in tests to exercise the timeout/error mapping
// without a real provider.
var testNotifyService = service.TestNotify

// GetNotify handles GET /svr/notify
func GetNotify(c *gin.Context) {
	handleService(c, func() (interface{}, error) {
		return service.GetNotifyList()
	})
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
		respondError(c, msg.T(msg.LostPart))
		return
	}

	notify := *req.Notify
	if _, hasEnable := notify["enable"]; !hasEnable {
		// A create request without `enable` is ambiguous; test sends belong on
		// POST /svr/notify/test so this cannot silently fire a real message.
		respondError(c, msg.T(msg.LostPart))
		return
	}
	// The created id is returned so a client can address the new row directly
	// instead of guessing it from the tail of GET /svr/notify.
	handleService(c, func() (interface{}, error) {
		id, err := service.AddNewNotify(notify)
		if err != nil {
			return nil, err
		}
		return gin.H{"id": id}, nil
	})
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
		respondError(c, msg.T(msg.LostPart))
		return
	}
	// Capture the seam before starting the goroutine: tests swap it and restore
	// it in a cleanup, and the goroutine may outlive the handler on timeout.
	testService := testNotifyService
	done := make(chan error, 1)
	go func() { done <- testService(*req.Notify) }()
	timer := time.NewTimer(notifyTestTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			respondServiceError(c, err)
			return
		}
		respondOK(c, nil)
	case <-c.Request.Context().Done():
		// The client disconnected; the send keeps its own bounded lifetime but
		// there is nobody left to write a response to.
		log.Printf("notify test abandoned by client: %v", c.Request.Context().Err())
	case <-timer.C:
		log.Printf("notify test timed out after %s", notifyTestTimeout)
		respondError(c, msg.T(msg.NotifySendFail))
	}
}

// UpdateNotify handles PUT /svr/notify
func UpdateNotify(c *gin.Context) {
	var req map[string]interface{}
	if !bindJSON(c, &req) {
		return
	}

	if notifyIDStr, ok := req["notifyId"]; ok {
		// Update status
		notifyID, err := parseIDValue(notifyIDStr)
		if err != nil {
			respondError(c, err.Error())
			return
		}
		enableRaw, ok := req["enable"]
		if !ok {
			respondError(c, msg.T(msg.LostPart))
			return
		}
		enable, err := parseEnableValue(enableRaw)
		if err != nil {
			respondError(c, err.Error())
			return
		}
		handleServiceVoid(c, func() error {
			return service.UpdateNotifyStatus(notifyID, enable)
		})
	} else if notify, ok := req["notify"]; ok {
		// Edit notify
		if nMap, ok := notify.(map[string]interface{}); ok {
			// Editing needs the row id. Without this check a missing id ran
			// "UPDATE ... WHERE id=NULL", matched nothing and surfaced as a
			// generic internal error instead of a parameter error.
			if _, err := parseIDValue(nMap["id"]); err != nil {
				respondError(c, err.Error())
				return
			}
			handleServiceVoid(c, func() error {
				return service.EditNotify(nMap)
			})
		} else {
			respondError(c, msg.T(msg.LostPart))
			return
		}
	} else {
		respondError(c, msg.T(msg.LostPart))
		return
	}
}

// DeleteNotify handles DELETE /svr/notify
func DeleteNotify(c *gin.Context) {
	notifyIDStr := c.Query("notifyId")
	if notifyIDStr == "" {
		respondError(c, msg.T(msg.LostPart))
		return
	}
	notifyID, err := parseRequiredID(notifyIDStr)
	if err != nil {
		respondError(c, err.Error())
		return
	}
	handleServiceVoid(c, func() error {
		return service.DeleteNotify(notifyID)
	})
}
