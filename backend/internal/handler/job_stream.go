package handler

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"opensync/internal/mapper"
	"opensync/internal/model"
	"opensync/internal/msg"
	"opensync/internal/service"
	"sync/atomic"

	"time"

	"github.com/gin-gonic/gin"
)

var globalSSEConns atomic.Int32

// subscribeJobProgress is swapped in tests.
var subscribeJobProgress = service.SubscribeJobProgressWithSnapshot

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
		respondError(c, msg.T(msg.LostPart))
		return
	}

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		log.Printf("SSE unavailable: response writer does not support flushing")
		c.JSON(http.StatusInternalServerError, model.Error(msg.T(msg.InternalError)))
		return
	}

	if !reserveSSEConnection() {
		c.JSON(http.StatusTooManyRequests, model.Error(msg.T(msg.SSEConnLimit)))
		return
	}
	defer globalSSEConns.Add(-1)

	// Subscribe and build the first frame atomically, BEFORE switching the
	// response to SSE, so an unknown job still returns a normal JSON error
	// instead of a JSON body glued onto an event-stream response. Building
	// first and subscribing afterwards lost any frame published in between —
	// including the task's final one, which left the page showing "running".
	updates, initialPayload, ok, err := subscribeJobProgress(jobID)
	if err != nil {
		var notFound *model.NotFoundError
		var pubErr model.PublicError
		if errors.As(err, &notFound) || errors.Is(err, mapper.ErrJobNotFound) ||
			(errors.As(err, &pubErr) && string(pubErr) == msg.T(msg.JobNotFound)) {
			c.JSON(http.StatusNotFound, model.Error(msg.T(msg.JobNotFound)))
			return
		}
		if errors.As(err, &pubErr) {
			respondError(c, pubErr.Error())
			return
		}
		var public model.PublicMessager
		if errors.As(err, &public) {
			respondError(c, public.PublicMessage())
			return
		}
		log.Printf("SSE initial payload for job %d failed: %v", jobID, err)
		c.JSON(http.StatusInternalServerError, model.Error(msg.T(msg.InternalError)))
		return
	}
	if !ok {
		c.JSON(http.StatusTooManyRequests, model.Error(msg.T(msg.SSEStreamLimit)))
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
type sseFlushError interface {
	FlushError() error
}

type sseUnwrapper interface {
	Unwrap() http.ResponseWriter
}

type sseControllerFlusher interface {
	Flush() error
}

func flushSSE(w io.Writer, rc sseDeadlineSetter, flusher http.Flusher) error {
	if fe, ok := flusher.(sseFlushError); ok {
		return fe.FlushError()
	}
	if rw, ok := w.(http.ResponseWriter); ok {
		for rw != nil {
			if fe, ok := rw.(sseFlushError); ok {
				return fe.FlushError()
			}
			u, ok := rw.(sseUnwrapper)
			if !ok {
				break
			}
			rw = u.Unwrap()
		}
	}
	if cf, ok := rc.(sseControllerFlusher); ok {
		if err := cf.Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
			return err
		}
		return nil
	}
	flusher.Flush()
	return nil
}

func writeSSEFrame(w io.Writer, rc sseDeadlineSetter, flusher http.Flusher, frame string) bool {
	if err := rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return false
	}
	n, err := io.WriteString(w, frame)
	if err != nil || n != len(frame) {
		return false
	}
	if err := flushSSE(w, rc, flusher); err != nil {
		return false
	}
	return true
}
