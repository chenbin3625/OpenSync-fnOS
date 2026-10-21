package util

import (
	"math"
	"testing"
)

func TestConvertBytesUsesBytesForValuesUnderOneKB(t *testing.T) {
	if got := ConvertBytes(512); got != "512 B" {
		t.Fatalf("ConvertBytes(512) = %q, want 512 B", got)
	}
}

func TestConvertBytesUsesLargerUnits(t *testing.T) {
	if got := ConvertBytes(1536); got != "1.50 KB" {
		t.Fatalf("ConvertBytes(1536) = %q, want 1.50 KB", got)
	}
}

func TestToIntTrimsWhitespaceStrings(t *testing.T) {
	if got := ToInt(" 42 "); got != 42 {
		t.Fatalf("ToInt(%q) = %d, want 42", " 42 ", got)
	}
	if got := ToInt(" \t "); got != 0 {
		t.Fatalf("ToInt(%q) = %d, want 0", " \t ", got)
	}
}

func TestToIntRejectsPartiallyNumericStrings(t *testing.T) {
	if got := ToInt("12abc"); got != 0 {
		t.Fatalf("ToInt(%q) = %d, want 0", "12abc", got)
	}
	if got := ToInt64("12abc"); got != 0 {
		t.Fatalf("ToInt64(%q) = %d, want 0", "12abc", got)
	}
}

func TestToFloat64Conversions(t *testing.T) {
	if got := ToFloat64(float32(3.14)); got < 3.13 || got > 3.15 {
		t.Fatalf("ToFloat64(float32(3.14)) = %v", got)
	}
	if got := ToFloat64(" 42.5 "); got != 42.5 {
		t.Fatalf("ToFloat64(%q) = %v, want 42.5", " 42.5 ", got)
	}
	if got := ToFloat64("invalid"); got != 0 {
		t.Fatalf("ToFloat64(%q) = %v, want 0", "invalid", got)
	}
}

// JSON numbers arrive as float64; values that cannot be represented as an
// int must not rely on implementation-defined conversion (review P3-7).
func TestToIntRejectsOutOfRangeAndNonIntegralFloats(t *testing.T) {
	for _, value := range []float64{1e300, -1e300, math.Inf(1), math.Inf(-1), math.NaN(), 1.9, -1.9} {
		if got := ToInt(value); got != 0 {
			t.Fatalf("ToInt(%v) = %d, want 0", value, got)
		}
		if got := ToInt64(value); got != 0 {
			t.Fatalf("ToInt64(%v) = %d, want 0", value, got)
		}
	}
	if got := ToInt(42.0); got != 42 {
		t.Fatalf("ToInt(42.0) = %d, want 42", got)
	}
	if got := ToInt64(float64(math.MaxInt32)); got != math.MaxInt32 {
		t.Fatalf("ToInt64(MaxInt32) = %d, want %d", got, int64(math.MaxInt32))
	}
}

// handler.parseEnableValue accepts "false"/"yes"/...; the shared converter must
// agree, and must trim whitespace (review P3-8).
func TestToBoolAcceptsTrimmedAndHumanValues(t *testing.T) {
	for _, value := range []interface{}{"1", " true ", "TRUE", "yes", "on", true, 1, int64(1), 1.0, float32(1), uint8(1)} {
		if got := ToBool(value); !got {
			t.Fatalf("ToBool(%#v) = false, want true", value)
		}
	}
	for _, value := range []interface{}{"0", " false ", "no", "off", "", "garbage", false, 0, int64(0), 0.0, float32(0), uint8(0), nil} {
		if got := ToBool(value); got {
			t.Fatalf("ToBool(%#v) = true, want false", value)
		}
	}
}

// A URL with embedded credentials must be rejected; plain http(s) URLs (even
// loopback ones, where AList commonly runs) stay valid (review P3-9).
func TestValidateHTTPURLRejectsCredentials(t *testing.T) {
	valid := []string{
		"http://nas.example:5666", "https://nas.example", "http://127.0.0.1:5244", " https://nas.example/app ",
	}
	for _, raw := range valid {
		if err := ValidateHTTPURL(raw, "invalid"); err != nil {
			t.Fatalf("ValidateHTTPURL(%q) = %v, want nil", raw, err)
		}
	}
	invalid := []string{
		"http://user:pass@nas.example", "https://user@nas.example", "ftp://nas.example",
		"nas.example", "http://", "http://nas.example\nX-Evil: 1",
	}
	for _, raw := range invalid {
		if err := ValidateHTTPURL(raw, "invalid"); err == nil {
			t.Fatalf("ValidateHTTPURL(%q) = nil, want error", raw)
		}
	}
}
