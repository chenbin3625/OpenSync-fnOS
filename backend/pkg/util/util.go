package util

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
)

// ConvertBytes converts bytes to human-readable string
func ConvertBytes(val int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	size := float64(val)
	absSize := math.Abs(size)
	for absSize >= 1024 && i < len(units)-1 {
		size /= 1024
		absSize /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%d %s", val, units[i])
	}
	return fmt.Sprintf("%.2f %s", size, units[i])
}

// ConvertSeconds converts seconds to hours, minutes, seconds
func ConvertSeconds(seconds int) (int, int, int) {
	hours := seconds / 3600
	remaining := seconds % 3600
	minutes := remaining / 60
	secs := remaining % 60
	return hours, minutes, secs
}

// ToInt converts various types to int
func ToInt(v interface{}) int {
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	case string:
		val = strings.TrimSpace(val)
		if val == "" {
			return 0
		}
		n, err := strconv.ParseInt(val, 10, 0)
		if err != nil {
			return 0
		}
		return int(n)
	default:
		return 0
	}
}

// ToInt64 converts various types to int64
func ToInt64(v interface{}) int64 {
	switch val := v.(type) {
	case int64:
		return val
	case int:
		return int64(val)
	case float64:
		return int64(val)
	case string:
		val = strings.TrimSpace(val)
		if val == "" {
			return 0
		}
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

// ToFloat64 converts various types to float64
func ToFloat64(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int:
		return float64(val)
	case int64:
		return float64(val)
	case string:
		val = strings.TrimSpace(val)
		if val == "" {
			return 0
		}
		n, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

// StringValue converts various types to string
func StringValue(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// ValidateHTTPURL checks that rawURL is a valid http or https URL.
// Returns an error with errMsg if invalid.
func ValidateHTTPURL(rawURL, errMsg string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New(errMsg)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return errors.New(errMsg)
	}
	return nil
}

// ToBool converts various types to bool
func ToBool(v interface{}) bool {
	switch val := v.(type) {
	case bool:
		return val
	case int:
		return val != 0
	case int64:
		return val != 0
	case float64:
		return val != 0
	case string:
		return val == "1" || strings.EqualFold(val, "true")
	default:
		return false
	}
}
