package handler

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"opensync/internal/model"
	"opensync/internal/msg"
	"opensync/internal/service"
	"sync/atomic"

	"time"

	"github.com/gin-gonic/gin"
)

var globalSSEConns atomic.Int32

const maxGlobalSSEConns = 64

func reserveSSEConnection() bool {
	for {
		count := globalSSEConns.Load()
		if count >= maxGlobalSSEConns {
			return false
		}
		if globalSSEConns.CompareAndSwap(count, count+1) {
			return true
		}
	}
}

// StreamJobCurrent handles GET /svr/job/stream as Server-Sent Events.
func StreamJobCurrent(c *gin.Context) {
	jobID, err := parseRequiredID(c.Query("id"))
	if err != nil {
		c.JSON(http.StatusOK, model.Error(msg.T(msg.LostPart)))
		return
	}

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, model.Error("streaming unsupported"))
		return
	}

	// Validate the job and build the first frame BEFORE switching the response
	// to SSE, so an unknown job still returns a normal JSON error instead of a
	// JSON body glued onto an event-stream response.
	initialPayload, err := service.BuildJobProgressStreamPayload(jobID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, model.Error("failed to build progress payload"))
		return
	}

	if !reserveSSEConnection() {
		c.JSON(http.StatusTooManyRequests, model.Error(msg.T(msg.SSEConnLimit)))
		return
	}
	defer globalSSEConns.Add(-1)

	updates := service.SubscribeJobProgress(jobID)
	if updates == nil {
		c.JSON(http.StatusTooManyRequests, model.Error("too many progress streams"))
		return
	}
	defer func() {
		service.UnsubscribeJobProgress(jobID, updates)
	}()

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("X-Accel-Buffering", "no")

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	rc := http.NewResponseController(c.Writer)
	// The server has no WriteTimeout, so a deadline set here would outlive the
	// stream on a kept-alive connection; clear it on the way out.
	defer func() { _ = rc.SetWriteDeadline(time.Time{}) }()

	writeEvent := func(payload []byte) bool {
		return writeSSEFrame(c.Writer, rc, flusher, fmt.Sprintf("data: %s\n\n", payload))
	}

	service.TouchJobWatching(jobID)
	if !writeEvent(initialPayload) {
		return
	}

	for {
		select {
		case <-c.Request.Context().Done():
			return
		case payload, ok := <-updates:
			if !ok {
				return
			}
			service.TouchJobWatching(jobID)
			if !writeEvent(payload) {
				return
			}
		case <-heartbeat.C:
			service.TouchJobWatching(jobID)
			if !writeSSEFrame(c.Writer, rc, flusher, ": heartbeat\n\n") {
				return
			}
		}
	}
}

// sseWriteTimeout bounds each SSE write. Without it a client that stops reading
// (a suspended tab, a dead NAT mapping) fills the socket buffer and the write
// blocks forever, pinning the goroutine, the progress subscription and one of
// the maxGlobalSSEConns slots.
const sseWriteTimeout = 30 * time.Second

// sseDeadlineSetter is the part of http.ResponseController used here, so tests
// can observe or fail the deadline call.
type sseDeadlineSetter interface {
	SetWriteDeadline(time.Time) error
}

// writeSSEFrame arms the write deadline, writes one frame and flushes it. A
// writer that cannot take deadlines (ErrNotSupported, e.g. a test recorder) is
// still written to; any other deadline error means the connection is unusable.
func writeSSEFrame(w io.Writer, rc sseDeadlineSetter, flusher http.Flusher, frame string) bool {
	if err := rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return false
	}
	if _, err := io.WriteString(w, frame); err != nil {
		return false
	}
	flusher.Flush()
	return true
}
