package mapper

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"math"
	"net/url"
	"opensync/internal/config"
	"opensync/internal/msg"
	"opensync/pkg/util"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

var (
	db   *sql.DB
	once = &sync.Once{}
	dbMu sync.RWMutex
	// closedDB is the handle ShutdownDB tore down. GetDB hands it back instead
	// of opening a new one, so a straggler goroutine gets "sql: database is
	// closed" rather than a nil handle to dereference.
	closedDB *sql.DB
	// shutdown records that the process is exiting for good. Without it any
	// caller reaching GetDB after CloseDB — a debounced persist timer, a cron
	// tick, an in-flight request — silently re-opened the database, recreating
	// the file and its WAL after shutdown had deliberately released them.
	shutdown bool
)

// currentOnce reads the once guard under dbMu. CloseDB replaces it, so reading
// the package variable unsynchronized would race with shutdown.
func currentOnce() *sync.Once {
	dbMu.RLock()
	defer dbMu.RUnlock()
	return once
}

// MaxPageSize is the largest page a caller can request, and also the cap applied
// to a request that omits pagination entirely.
const MaxPageSize = 500

const maxPageSize = MaxPageSize
const defaultUnpagedLimit = MaxPageSize
const sqliteMaxOpenConns = 12

// InitDB initializes the database connection
func InitDB() *sql.DB {
	currentOnce().Do(func() {
		cfg := config.GetConfig()
		ensureSQLiteFileMode(cfg.DB.DBName)
		// The handle is configured through a local variable and only published
		// to the package-level db under dbMu. Assigning db first would race with
		// the lock-protected readers in GetDB (and with CloseDB during shutdown),
		// because sync.Once only orders the initialization against other
		// once.Do callers, not against readers that never enter it.
		handle, err := sql.Open("sqlite", sqliteDSN(cfg.DB.DBName))
		if err != nil {
			log.Fatalf("Failed to open database: %v", err)
		}
		handle.SetMaxOpenConns(sqliteMaxOpenConns)
		handle.SetMaxIdleConns(sqliteMaxOpenConns)
		// Keep explicit PRAGMAs as a startup sanity pass; sqliteDSN applies
		// them to each new pooled connection.
		if _, err := handle.Exec("PRAGMA journal_mode=WAL"); err != nil {
			log.Printf("Failed to set sqlite journal_mode: %v", err)
		}
		if _, err := handle.Exec("PRAGMA busy_timeout=5000"); err != nil {
			log.Printf("Failed to set sqlite busy_timeout: %v", err)
		}
		for _, pragma := range []string{"PRAGMA foreign_keys=ON", "PRAGMA temp_store=MEMORY", "PRAGMA cache_size=-16384", "PRAGMA mmap_size=67108864"} {
			if _, err := handle.Exec(pragma); err != nil {
				log.Printf("Failed to set sqlite %s: %v", pragma, err)
			}
		}
		dbMu.Lock()
		db = handle
		dbMu.Unlock()
	})
	dbMu.RLock()
	defer dbMu.RUnlock()
	return db
}

func ensureSQLiteFileMode(dbName string) {
	path, ok := sqliteDBPath(dbName)
	if !ok {
		return
	}
	if dir := filepath.Dir(path); dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		log.Printf("Failed to prepare sqlite database file permissions: %v", err)
		return
	}
	_ = file.Close()
	if err := os.Chmod(path, 0600); err != nil {
		log.Printf("Failed to set sqlite database file permissions: %v", err)
	}
}

func sqliteDBPath(dbName string) (string, bool) {
	if dbName == "" || dbName == ":memory:" {
		return "", false
	}
	if strings.HasPrefix(dbName, "file:") {
		u, err := url.Parse(dbName)
		if err != nil || strings.Contains(u.RawQuery, "mode=memory") {
			return "", false
		}
		if u.Path != "" {
			return u.Path, true
		}
		if u.Opaque != "" && !strings.HasPrefix(u.Opaque, ":memory:") {
			return u.Opaque, true
		}
		return "", false
	}
	return dbName, true
}

func sqliteDSN(dbName string) string {
	if dbName == ":memory:" {
		return dbName
	}
	// Every PRAGMA here applies to each pooled connection. Executing them via
	// db.Exec instead would configure whichever single connection served that
	// call, leaving the other 11 in the pool on defaults.
	pragmas := url.Values{}
	pragmas.Add("_pragma", "busy_timeout(5000)")
	pragmas.Add("_pragma", "journal_mode(WAL)")
	pragmas.Add("_pragma", "foreign_keys(ON)")
	pragmas.Add("_pragma", "temp_store(MEMORY)")
	pragmas.Add("_pragma", "cache_size(-16384)")
	pragmas.Add("_pragma", "mmap_size(67108864)")
	query := pragmas.Encode()
	if strings.HasPrefix(dbName, "file:") {
		sep := "?"
		if strings.Contains(dbName, "?") {
			sep = "&"
		}
		return dbName + sep + query
	}
	return "file:" + dbName + "?" + query
}

// GetDB returns the database connection.
//
// After ShutdownDB it returns the closed handle instead of opening a new one:
// every query then fails with "sql: database is closed", which is what a caller
// running past shutdown should see.
func GetDB() *sql.DB {
	dbMu.RLock()
	if db != nil {
		handle := db
		dbMu.RUnlock()
		return handle
	}
	if shutdown {
		handle := closedDB
		dbMu.RUnlock()
		return handle
	}
	dbMu.RUnlock()
	return InitDB()
}

// CloseDB closes the global database handle and allows later reinitialization.
// Use ShutdownDB on the process exit path; this exists for callers that intend
// to open the database again afterwards.
func CloseDB() error {
	dbMu.Lock()
	defer dbMu.Unlock()
	return closeDBLocked(false)
}

// ShutdownDB closes the handle for good: a later GetDB will not reopen it.
//
// CloseDB alone only cleared the handle and reset the once guard, so anything
// still running — a debounced persist flush, the retention cron, a request that
// outlived the server — reopened the database through GetDB and recreated the
// file and WAL that shutdown had just released.
func ShutdownDB() error {
	dbMu.Lock()
	defer dbMu.Unlock()
	return closeDBLocked(true)
}

func closeDBLocked(forGood bool) error {
	if forGood {
		shutdown = true
	}
	if db == nil {
		return nil
	}
	handle := db
	err := handle.Close()
	db = nil
	once = &sync.Once{}
	if forGood {
		closedDB = handle
	}
	return err
}

// SetDBForTest swaps the package database handle and returns a restore function.
func SetDBForTest(testDB *sql.DB) func() {
	dbMu.Lock()
	oldDB := db
	db = testDB
	dbMu.Unlock()
	return func() {
		dbMu.Lock()
		db = oldDB
		dbMu.Unlock()
	}
}

// FetchAllToTable executes a query and returns results as []map[string]interface{}
const maxFetchAllRows = 10000

func FetchAllToTable(query string, args ...interface{}) ([]map[string]interface{}, error) {
	rows, err := GetDB().Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	results := make([]map[string]interface{}, 0, 16)
	n := len(columns)
	values := make([]interface{}, n)
	valuePtrs := make([]interface{}, n)
	for i := range valuePtrs {
		valuePtrs[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(valuePtrs...); err != nil {
			return nil, err
		}
		row := make(map[string]interface{}, n)
		for i, col := range columns {
			val := values[i]
			if b, ok := val.([]byte); ok {
				row[col] = string(b)
			} else {
				row[col] = val
			}
		}
		results = append(results, row)
		if len(results) > maxFetchAllRows {
			rows.Close()
			return nil, fmt.Errorf("query returned more than %d rows; use pagination", maxFetchAllRows)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// FetchFirstVal executes a query and returns the first column of first row
func FetchFirstVal(query string, args ...interface{}) (interface{}, error) {
	var result interface{}
	err := GetDB().QueryRow(query, args...).Scan(&result)
	return result, err
}

// ExecuteInsert executes an insert and returns last insert id
func ExecuteInsert(query string, args ...interface{}) (int64, error) {
	result, err := GetDB().Exec(query, args...)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// ExecuteUpdate executes an update/delete query
func ExecuteUpdate(query string, args ...interface{}) error {
	_, err := GetDB().Exec(query, args...)
	return err
}

// ExecuteMany executes batch operations
func ExecuteMany(query string, argsList [][]interface{}) error {
	if len(argsList) == 0 {
		return nil
	}
	// withTx rolls back via defer: the previous explicit Rollback() calls were
	// skipped by a panic inside the loop, which left the transaction (and the
	// SQLite write lock) open until the process exited — every later write then
	// failed on busy_timeout.
	return withTx(func(tx *sql.Tx) error {
		stmt, err := tx.Prepare(query)
		if err != nil {
			return err
		}
		defer stmt.Close()

		for _, args := range argsList {
			if _, err := stmt.Exec(args...); err != nil {
				log.Printf("Database batch execute failed (size %d): %v", len(argsList), err)
				return err
			}
		}
		return nil
	})
}

// FetchAllToPage executes a paginated query with a window count when SQLite
// can keep the list and total in one round-trip. A request that omits pagination
// is capped at defaultUnpagedLimit rows and fails when the result would not fit,
// rather than returning a short list next to the full count.
func FetchAllToPage(baseSQL string, params map[string]interface{}, sqlArgs ...interface{}) (map[string]interface{}, error) {
	ps, pn, paginated, err := parsePageParams(params)
	if err != nil {
		return nil, err
	}
	if !paginated {
		return fetchPage(baseSQL, defaultUnpagedLimit, 0, false, sqlArgs)
	}
	offset, err := pageOffset(ps, pn)
	if err != nil {
		return nil, err
	}
	return fetchPage(baseSQL, ps, offset, true, sqlArgs)
}

const pageTotalColumn = "__opensync_page_total"

func fetchPage(baseSQL string, limit int, offset int64, paginated bool, sqlArgs []interface{}) (map[string]interface{}, error) {
	query, args, _ := pageQuery(baseSQL, limit, offset, paginated, sqlArgs)
	dataList, err := FetchAllToTable(query, args...)
	if err != nil {
		return nil, err
	}
	total, hasTotal := takePageTotal(dataList)
	if !hasTotal {
		if len(dataList) == 0 && offset == 0 {
			total = 0
		} else {
			count, err := FetchFirstVal("SELECT COUNT(*) FROM ("+stripOrderBy(baseSQL)+")", sqlArgs...)
			if err != nil {
				return nil, err
			}
			total = util.ToInt64(count)
		}
	}
	if !paginated && total > int64(len(dataList)) {
		// An unpaginated request that hits the cap used to answer with the first
		// `limit` rows and the real total, plus a "truncated" flag no caller ever
		// read: the response looked complete while rows were missing. Refusing it
		// puts the choice back with the caller, which can page or narrow the
		// filter.
		return nil, errors.New(msg.T(msg.ListTooLarge))
	}
	return map[string]interface{}{"dataList": dataList, "count": total}, nil
}

func pageQuery(baseSQL string, limit int, offset int64, paginated bool, sqlArgs []interface{}) (string, []interface{}, bool) {
	if !strings.Contains(strings.ToUpper(baseSQL), " UNION ") {
		query := withPageTotal(baseSQL)
		if paginated {
			return query + " LIMIT ? OFFSET ?", appendSQLArgs(sqlArgs, limit, offset), true
		}
		return query + " LIMIT ?", appendSQLArgs(sqlArgs, limit), true
	}
	wrapped := "SELECT * FROM (" + baseSQL + ")"
	if paginated {
		return wrapped + " LIMIT ? OFFSET ?", appendSQLArgs(sqlArgs, limit, offset), false
	}
	return wrapped + " LIMIT ?", appendSQLArgs(sqlArgs, limit), false
}

func appendSQLArgs(args []interface{}, extra ...interface{}) []interface{} {
	result := make([]interface{}, 0, len(args)+len(extra))
	result = append(result, args...)
	return append(result, extra...)
}

func withPageTotal(baseSQL string) string {
	trimmed := strings.TrimSpace(baseSQL)
	if len(trimmed) < 7 || !strings.EqualFold(trimmed[:6], "SELECT") || (trimmed[6] != ' ' && trimmed[6] != '\t' && trimmed[6] != '\n') {
		return trimmed
	}
	return "SELECT COUNT(*) OVER() AS " + pageTotalColumn + "," + trimmed[6:]
}

func takePageTotal(rows []map[string]interface{}) (int64, bool) {
	if len(rows) == 0 {
		return 0, false
	}
	var total int64
	found := false
	for _, row := range rows {
		if value, ok := row[pageTotalColumn]; ok {
			total = util.ToInt64(value)
			delete(row, pageTotalColumn)
			found = true
		}
	}
	return total, found
}

func pageOffset(pageSize, pageNum int) (int64, error) {
	if pageSize <= 0 || pageNum <= 0 {
		return 0, errors.New(msg.T(msg.LostPart))
	}
	index := int64(pageNum) - 1
	size := int64(pageSize)
	maxInt64 := int64(^uint64(0) >> 1)
	if index > maxInt64/size {
		return 0, errors.New(msg.T(msg.LostPart))
	}
	return index * size, nil
}

func parsePageParams(params map[string]interface{}) (pageSize, pageNum int, paginated bool, err error) {
	pageSizeVal, hasPageSize := params["pageSize"]
	pageNumVal, hasPageNum := params["pageNum"]
	if !hasPageSize && !hasPageNum {
		return 0, 0, false, nil
	}
	if !hasPageSize || !hasPageNum {
		return 0, 0, false, errors.New(msg.T(msg.LostPart))
	}

	pageSize, err = positiveInt(pageSizeVal)
	if err != nil {
		return 0, 0, false, errors.New(msg.T(msg.LostPart))
	}
	pageNum, err = positiveInt(pageNumVal)
	if err != nil {
		return 0, 0, false, errors.New(msg.T(msg.LostPart))
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return pageSize, pageNum, true, nil
}

func positiveInt(v interface{}) (int, error) {
	if value, ok := v.(float64); ok && math.Trunc(value) != value {
		return 0, errors.New(msg.T(msg.LostPart))
	}
	n := util.ToInt64(v)
	if n <= 0 || n > int64(math.MaxInt) {
		return 0, errors.New(msg.T(msg.LostPart))
	}
	return int(n), nil
}

func isSafeSQLIdentifier(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// stripOrderBy removes a trailing top-level ORDER BY so the statement can be
// wrapped in SELECT COUNT(*) FROM (...). Only depth-zero clauses outside string
// literals are considered: a plain LastIndex search would cut at an ORDER BY
// belonging to a subquery (or sitting inside a quoted value) and produce
// unbalanced SQL.
func stripOrderBy(sql string) string {
	const clause = " ORDER BY "
	depth := 0
	idx := -1
	var quote byte
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if quote != 0 {
			// Doubled quotes are an escaped quote inside the literal, not its end.
			if c == quote {
				if i+1 < len(sql) && sql[i+1] == quote {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 && c == ' ' && strings.EqualFold(sqlSlice(sql, i, len(clause)), clause) {
				idx = i
			}
		}
	}
	if idx == -1 {
		return sql
	}
	return sql[:idx]
}

// sqlSlice returns the length-byte window of sql starting at i, or "" when the
// statement is too short for one.
func sqlSlice(sql string, i, length int) string {
	if i+length > len(sql) {
		return ""
	}
	return sql[i : i+length]
}
