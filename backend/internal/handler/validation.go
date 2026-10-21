package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"math"
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

// parseIDValue parses a positive id from a decoded JSON value. Clients send
// ids both as strings ("12") and as numbers (12); a float64 must be an exact
// integer, so 1.5 or "abc" is rejected instead of truncated or zeroed.
func parseIDValue(value interface{}) (int64, error) {
	n, ok := strictInt64(value)
	if !ok || n <= 0 {
		return 0, errors.New(msg.T(msg.LostPart))
	}
	return n, nil
}

// strictInt64 converts a decoded JSON number or a base-10 integer string.
// It reports false for anything else: non-numeric strings, fractional or
// out-of-range numbers, booleans, objects. util.ToInt silently mapped all of
// those to 0 (or truncated 1.9 to 1), letting them pass range checks.
func strictInt64(value interface{}) (int64, bool) {
	switch v := value.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		if v != math.Trunc(v) || math.IsInf(v, 0) || math.IsNaN(v) || v > math.MaxInt64 || v < math.MinInt64 {
			return 0, false
		}
		return int64(v), true
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n, err == nil
	}
	return 0, false
}

// flexibleID accepts a JSON string or number for an id field.
type flexibleID struct {
	set   bool
	value interface{}
}

func (f *flexibleID) UnmarshalJSON(data []byte) error {
	f.set = true
	if string(data) == "null" {
		f.value = nil
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return dec.Decode(&f.value)
}

// parse returns the positive id, or an error when the field is absent,
// null, or not an integer.
func (f *flexibleID) parse() (int64, error) {
	if f == nil || !f.set || f.value == nil {
		return 0, errors.New(msg.T(msg.LostPart))
	}
	return parseIDValue(f.value)
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
