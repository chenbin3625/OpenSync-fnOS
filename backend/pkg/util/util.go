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

// ToInt converts various types to int. Numeric values that do not fit in int
// (NaN, ±Inf, fractional or out-of-range float64) are rejected as 0 instead of
// relying on Go's implementation-defined float-to-int conversion, which would
// otherwise yield a surprising huge negative value from JSON numbers such as
// 1e300.
func ToInt(v interface{}) int {
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int64ToInt(val)
	case float64:
		n, ok := integralFloatToInt64(val)
		if !ok {
			return 0
		}
		return int64ToInt(n)
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
		n, ok := integralFloatToInt64(val)
		if !ok {
			return 0
		}
		return n
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

// integralFloatToInt64 converts v when it is a finite integer representable as
// int64. NaN, ±Inf, fractional values and values outside the int64 range return
// ok=false; the caller maps that to its documented zero value.
func integralFloatToInt64(v float64) (int64, bool) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) {
		return 0, false
	}
	// 2^63 is exactly representable as float64 and is one past math.MaxInt64.
	const maxInt64Exclusive = float64(1 << 63)
	if v < math.MinInt64 || v >= maxInt64Exclusive {
		return 0, false
	}
	return int64(v), true
}

// int64ToInt narrows n to int, returning 0 when the value does not fit (only
// possible on 32-bit platforms, but keeps the helper safe everywhere).
func int64ToInt(n int64) int {
	i := int(n)
	if int64(i) != n {
		return 0
	}
	return i
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

// ValidateHTTPURL checks that rawURL is a syntactically valid http or https
// URL. Returns an error with errMsg if invalid.
//
// Embedded credentials ("http://user:pass@host") are rejected so a stored URL
// cannot smuggle a secret that would later be logged or sent. Note that this
// only validates syntax: loopback/private hosts are accepted on purpose because
// AList is commonly deployed on the same NAS (a malicious administrator could
// otherwise use the URL as an SSRF primitive). Callers must keep this input
// administrator-only.
func ValidateHTTPURL(rawURL, errMsg string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil {
		return errors.New(errMsg)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return errors.New(errMsg)
	}
	return nil
}

// ToBool converts various types to bool. Strings are trimmed and accept the
// same spellings as handler.parseEnableValue ("1"/"true"/"yes"/"on" and
// "0"/"false"/"no"/"off") so a stored JSON value such as " true " is not
// silently treated as false.
func ToBool(v interface{}) bool {
	switch val := v.(type) {
	case bool:
		return val
	case int:
		return val != 0
	case int8:
		return val != 0
	case int16:
		return val != 0
	case int32:
		return val != 0
	case int64:
		return val != 0
	case uint:
		return val != 0
	case uint8:
		return val != 0
	case uint16:
		return val != 0
	case uint32:
		return val != 0
	case uint64:
		return val != 0
	case float32:
		return val != 0
	case float64:
		return val != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(val)) {
		case "1", "true", "yes", "on":
			return true
		default:
			return false
		}
	default:
		return false
	}
}
