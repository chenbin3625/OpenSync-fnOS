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

func respondError(c *gin.Context, message string) {
	c.JSON(http.StatusOK, model.Error(message))
}

func bindJSON(c *gin.Context, target interface{}) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		respondError(c, msg.T(msg.LostPart))
		return false
	}
	return true
}

func handleService(c *gin.Context, fn func() (interface{}, error)) {
	data, err := fn()
	if err != nil {
		var pubErr model.PublicError
		if errors.As(err, &pubErr) {
			respondError(c, pubErr.Error())
			return
		}
		log.Printf("request failed: %v", err)
		respondError(c, msg.T(msg.InternalError))
		return
	}
	respondOK(c, data)
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
