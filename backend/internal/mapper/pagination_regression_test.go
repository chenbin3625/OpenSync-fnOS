package mapper

import (
	"errors"
	"opensync/internal/model"
	"opensync/internal/msg"
	"testing"
)

// The pagination helper used to rewrite the caller's SQL and inject a
// COUNT(*) OVER() window column. That broke legal statements (SELECT DISTINCT
// became a syntax error) and silently produced a wrong total for a UNION
// separated by newlines. The total now comes from a separate COUNT(*) wrapped
// around the original statement, which must be shape agnostic.
func TestPaginatedDistinctQuerySucceeds(t *testing.T) {
	restore := SetDBForTest(newPageLimitDB(t, 7))
	defer restore()

	if _, err := GetDB().Exec("UPDATE widget SET name='same'"); err != nil {
		t.Fatalf("normalize names: %v", err)
	}
	result, err := FetchAllToPage(
		"SELECT DISTINCT name FROM widget ORDER BY name",
		map[string]interface{}{"pageSize": 1, "pageNum": 1},
	)
	if err != nil {
		t.Fatalf("FetchAllToPage(SELECT DISTINCT) error = %v, want the old implementation's syntax error gone", err)
	}
	if result["count"] != int64(1) {
		t.Fatalf("count = %v, want 1 distinct name", result["count"])
	}
	rows := result["dataList"].([]map[string]interface{})
	if len(rows) != 1 || rows[0]["name"] != "same" {
		t.Fatalf("dataList = %#v, want the single distinct row", rows)
	}
}

func TestPaginatedUnionSeparatedByNewlinesGetsCorrectTotal(t *testing.T) {
	restore := SetDBForTest(newPageLimitDB(t, 5))
	defer restore()

	base := "SELECT id FROM widget WHERE id <= 3\nUNION\nSELECT id FROM widget WHERE id > 3"
	result, err := FetchAllToPage(base, map[string]interface{}{"pageSize": 10, "pageNum": 1})
	if err != nil {
		t.Fatalf("FetchAllToPage(newline UNION) error = %v", err)
	}
	if result["count"] != int64(5) {
		t.Fatalf("count = %v, want 5 (the per-UNION-arm window count used to report 3 or 2)", result["count"])
	}
	rows := result["dataList"].([]map[string]interface{})
	if len(rows) != 5 {
		t.Fatalf("dataList len = %d, want 5", len(rows))
	}
}

// An unpaginated DISTINCT result over the cap must be refused, not truncated.
func TestUnpaginatedDistinctOverLimitFails(t *testing.T) {
	restore := SetDBForTest(newPageLimitDB(t, 600))
	defer restore()

	_, err := FetchAllToPage("SELECT DISTINCT id FROM widget ORDER BY id", map[string]interface{}{})
	if err == nil || !errors.Is(err, ErrListTooLarge) {
		t.Fatalf("error = %v, want ErrListTooLarge", err)
	}
}

// pageSize above MaxPageSize is capped; the response must expose the effective
// size so a client can tell why its offsets no longer match its request.
func TestCappedPageSizeIsReportedInResponse(t *testing.T) {
	restore := SetDBForTest(newPageLimitDB(t, 7))
	defer restore()

	result, err := FetchAllToPage(
		"SELECT * FROM widget ORDER BY id",
		map[string]interface{}{"pageSize": 9999, "pageNum": 1},
	)
	if err != nil {
		t.Fatalf("FetchAllToPage() error = %v", err)
	}
	if got := result["pageSize"]; got != MaxPageSize {
		t.Fatalf("effective pageSize = %v, want %d", got, MaxPageSize)
	}
	if got := result["count"]; got != int64(7) {
		t.Fatalf("count = %v, want 7", got)
	}
}

// The rejection errors are typed: callers can branch on errors.Is and the
// API layers get the same localized message through model.PublicMessager.
func TestPaginationRejectionsAreTypedPublicErrors(t *testing.T) {
	restore := SetDBForTest(newPageLimitDB(t, 7))
	defer restore()

	_, err := fetchPage("SELECT * FROM widget ORDER BY id", 5, 0, false, nil)
	if !errors.Is(err, ErrListTooLarge) {
		t.Fatalf("error = %v, want errors.Is(ErrListTooLarge)", err)
	}
	assertPublicMessage(t, err, msg.T(msg.ListTooLarge))

	_, _, _, err = parsePageParams(map[string]interface{}{"pageSize": "0", "pageNum": "1"})
	if !errors.Is(err, ErrInvalidPagination) {
		t.Fatalf("parsePageParams error = %v, want errors.Is(ErrInvalidPagination)", err)
	}
	assertPublicMessage(t, err, msg.T(msg.LostPart))
}

func assertPublicMessage(t *testing.T, err error, want string) {
	t.Helper()
	var public model.PublicMessager
	if !errors.As(err, &public) {
		t.Fatalf("error %v does not implement model.PublicMessager", err)
	}
	if got := public.PublicMessage(); got != want {
		t.Fatalf("public message = %q, want %q", got, want)
	}
}
