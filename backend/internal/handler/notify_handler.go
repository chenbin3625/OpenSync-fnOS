package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"opensync/internal/model"
	"opensync/internal/msg"
	"opensync/internal/service"
	"sync"
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

const maxConcurrentNotifyTests = 4

var (
	notifyTestSem     = make(chan struct{}, maxConcurrentNotifyTests)
	notifyTestWG      sync.WaitGroup
	notifyTestMu      sync.RWMutex
	notifyTestClosing bool
)

// ShutdownNotifyTests rejects new TestNotify invocations and waits for any
// already-running test goroutines to finish before ctx expires.
func ShutdownNotifyTests(ctx context.Context) {
	notifyTestMu.Lock()
	notifyTestClosing = true
	notifyTestMu.Unlock()

	done := make(chan struct{})
	go func() {
		notifyTestWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
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
	if c.Request.Context().Err() != nil {
		return
	}

	notifyTestMu.RLock()
	if notifyTestClosing {
		notifyTestMu.RUnlock()
		respondError(c, msg.T(msg.NotifySendFail))
		return
	}
	select {
	case notifyTestSem <- struct{}{}:
	default:
		notifyTestMu.RUnlock()
		c.JSON(http.StatusTooManyRequests, model.Error(msg.T(msg.NotifySendFail)))
		return
	}
	notifyTestWG.Add(1)
	notifyTestMu.RUnlock()

	ctx, cancel := context.WithTimeout(c.Request.Context(), notifyTestTimeout)
	defer cancel()

	// Capture the seam before starting the goroutine: tests swap it and restore
	// it in a cleanup, and the goroutine may outlive the handler on timeout.
	testService := testNotifyService
	payload := *req.Notify
	done := make(chan error, 1)
	go func() {
		defer notifyTestWG.Done()
		defer func() { <-notifyTestSem }()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("notify test panicked: %v", r)
				done <- fmt.Errorf("notify test panic: %v", r)
			}
		}()
		done <- testService(payload)
	}()

	select {
	case err := <-done:
		if err != nil {
			respondServiceError(c, err)
			return
		}
		respondOK(c, nil)
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && c.Request.Context().Err() == nil {
			log.Printf("notify test timed out after %s", notifyTestTimeout)
			respondError(c, msg.T(msg.NotifySendFail))
			return
		}
		// The client disconnected or the server is shutting down; the send keeps
		// its bounded lifetime and there is nobody left to write a response to.
		log.Printf("notify test abandoned: %v", ctx.Err())
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
