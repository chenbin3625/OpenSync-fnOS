package handler

import (
	"errors"
	"log"
	"net/http"
	"opensync/internal/model"
	"opensync/internal/msg"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// parseRequiredID parses a positive int64 id. The caller's field name is
// deliberately not folded into the error: this message is returned verbatim to
// the browser, so it stays the generic user-facing text.
func parseRequiredID(value string) (int64, error) {
	if value == "" {
		return 0, errors.New(msg.T(msg.LostPart))
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New(msg.T(msg.LostPart))
	}
	return id, nil
}

func respondOK(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, model.Success(data))
}

// respondError answers a user-actionable failure. The HTTP status is 400 so
// transport-level clients, proxies and the browser can see the request failed,
// while the body keeps the {code,data,msg} envelope (code 500) the frontend and
// external clients already parse. Internal failures use respondInternalError
// (500) instead, so a single convention covers every handler.
func respondError(c *gin.Context, message string) {
	c.JSON(http.StatusBadRequest, model.Error(message))
}

func respondInternalError(c *gin.Context, err error) {
	if err != nil {
		log.Printf("request failed: %v", err)
	}
	c.JSON(http.StatusInternalServerError, model.Error(msg.T(msg.InternalError)))
}

// isBodyTooLargeError reports whether err came from the MaxBytesReader the
// request middleware installs. The Content-Length pre-check only catches
// clients that declare their size; a chunked/unknown-length body exceeds the
// limit while decoding and must still answer 413 rather than look like a
// malformed parameter.
func isBodyTooLargeError(err error) bool {
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr)
}

func respondBodyTooLarge(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
		"code": http.StatusRequestEntityTooLarge,
		"msg":  msg.T(msg.RequestTooLarge),
	})
}

func bindJSON(c *gin.Context, target interface{}) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		if isBodyTooLargeError(err) {
			respondBodyTooLarge(c)
			return false
		}
		respondError(c, msg.T(msg.LostPart))
		return false
	}
	return true
}

func handleService(c *gin.Context, fn func() (interface{}, error)) {
	data, err := fn()
	if err != nil {
		respondServiceError(c, err)
		return
	}
	respondOK(c, data)
}

func respondServiceError(c *gin.Context, err error) {
	var pubErr model.PublicError
	if errors.As(err, &pubErr) {
		respondError(c, pubErr.Error())
		return
	}
	// Typed public errors (e.g. mapper not-found) stay displayable even
	// when a service layer wrapped them with fmt.Errorf("%w").
	var public model.PublicMessager
	if errors.As(err, &public) {
		respondError(c, public.PublicMessage())
		return
	}
	respondInternalError(c, err)
}

func handleServiceVoid(c *gin.Context, fn func() error) {
	handleService(c, func() (interface{}, error) {
		return nil, fn()
	})
}

func parseEnableValue(value interface{}) (int, error) {
	switch v := value.(type) {
	case bool:
		if v {
			return 1, nil
		}
		return 0, nil
	case int:
		if v == 0 || v == 1 {
			return v, nil
		}
	case int64:
		if v == 0 || v == 1 {
			return int(v), nil
		}
	case float64:
		if v == 0 || v == 1 {
			return int(v), nil
		}
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "0", "false":
			return 0, nil
		case "1", "true":
			return 1, nil
		}
	}
	return 0, errors.New(msg.T(msg.LostPart))
}
