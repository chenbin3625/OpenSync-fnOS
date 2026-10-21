package mapper

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math"
	"net/url"
	"opensync/internal/config"
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
	// closedPlaceholderDB is a process-wide, already-closed handle returned by
	// GetDB when ShutdownDB ran before any database was ever opened. Without it
	// GetDB could return nil (its contract says it never does) and callers that
	// do GetDB().QueryRow(...) would panic instead of seeing "database is
	// closed".
	closedPlaceholderDB   *sql.DB
	closedPlaceholderOnce sync.Once
)

// initDBPauseForTest is nil in production. Tests set it to run code while
// InitDB has opened a handle but not published it yet, so the CloseDB race has
// a deterministic seam.
var initDBPauseForTest func()

// currentOnce reads the once guard under dbMu. CloseDB replaces it, so reading
// the package variable unsynchronized would race with shutdown.
func currentOnce() *sync.Once {
	dbMu.RLock()
	defer dbMu.RUnlock()
	return once
}

// MaxPageSize is the largest page a caller can request, and also the cap applied
// to a request that omits pagination entirely. A request larger than this is
// capped, and the paginated response reports the effective pageSize so a client
// can tell that its requested window was narrowed.
const MaxPageSize = 500

const maxPageSize = MaxPageSize
const defaultUnpagedLimit = MaxPageSize
const sqliteMaxOpenConns = 12
const initDBMaxAttempts = 5

// InitDB initializes the database connection.
//
// Initialization runs through a sync.Once, but the once guard is replaced by
// CloseDB/ShutdownDB, so a close that lands while a handle is being opened must
// not let that handle get published afterwards (it would recreate the file and
// WAL the close just released). The opened handle is therefore only published
// under dbMu if the once guard is still the one this attempt started with and
// the process is not shutting down; otherwise it is closed and a fresh attempt
// is made (or the closed handle is returned during shutdown).
func InitDB() *sql.DB {
	for attempt := 0; attempt < initDBMaxAttempts; attempt++ {
		self := currentOnce()
		self.Do(func() { initDBOnce(self) })

		dbMu.RLock()
		handle := db
		down := shutdown
		closed := closedDB
		dbMu.RUnlock()
		if handle != nil {
			return handle
		}
		if down {
			if closed != nil {
				return closed
			}
			return closedPlaceholder()
		}
		// The initialization was discarded because CloseDB raced it. Try again
		// with the fresh once guard.
	}
	// CloseDB kept winning the race; hand back a closed handle so callers see
	// an error instead of a nil dereference.
	log.Printf("WARNING: database initialization kept racing CloseDB; returning a closed handle")
	return closedPlaceholder()
}

func initDBOnce(self *sync.Once) {
	cfg := config.GetConfig()
	dbName := cfg.DB.DBName
	ensureSQLiteFileMode(dbName)
	// The handle is configured through a local variable and only published to
	// the package-level db under dbMu. Assigning db first would race with the
	// lock-protected readers in GetDB (and with CloseDB during shutdown),
	// because sync.Once only orders the initialization against other once.Do
	// callers, not against readers that never enter it.
	handle, err := sql.Open("sqlite", sqliteDSN(dbName))
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	memory := isMemoryDBName(dbName)
	if memory {
		// Every connection to a :memory: database gets its own private database,
		// so a pool with more than one connection would hand out connections that
		// do not see the schema the first one created.
		handle.SetMaxOpenConns(1)
		handle.SetMaxIdleConns(1)
	} else {
		handle.SetMaxOpenConns(sqliteMaxOpenConns)
		handle.SetMaxIdleConns(sqliteMaxOpenConns)
	}
	if err := configureDBHandle(handle, memory); err != nil {
		_ = handle.Close()
		log.Fatalf("Failed to configure sqlite database %q: %v", dbName, err)
	}
	if initDBPauseForTest != nil {
		initDBPauseForTest()
	}
	dbMu.Lock()
	if once != self || shutdown {
		if shutdown && closedDB == nil {
			// The very handle that was being opened is already closed, which is
			// exactly what post-shutdown callers must observe.
			closedDB = handle
		}
		dbMu.Unlock()
		_ = handle.Close()
		return
	}
	db = handle
	dbMu.Unlock()
}

// configureDBHandle validates the settings that the whole concurrency/retry
// design depends on instead of only logging a warning and running degraded:
// WAL (file databases), busy_timeout and foreign_keys are read back from the
// connection that will serve queries and a mismatch aborts startup loudly.
func configureDBHandle(handle *sql.DB, memory bool) error {
	ctx := context.Background()
	conn, err := handle.Conn(ctx)
	if err != nil {
		return fmt.Errorf("get connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout=%d", sqliteBusyTimeoutMS)); err != nil {
		return fmt.Errorf("set busy_timeout: %w", err)
	}
	var busyTimeout int
	if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		return fmt.Errorf("read busy_timeout: %w", err)
	}
	if busyTimeout != sqliteBusyTimeoutMS {
		return fmt.Errorf("busy_timeout = %d, want %d", busyTimeout, sqliteBusyTimeoutMS)
	}

	var journalMode string
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return fmt.Errorf("read journal_mode: %w", err)
	}
	if !memory && !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("journal_mode = %q, want WAL (is the database on a filesystem that supports it?)", journalMode)
	}

	var foreignKeys int
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("read foreign_keys: %w", err)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}

	pragmas := []string{"PRAGMA temp_store=MEMORY", "PRAGMA cache_size=-16384"}
	if !memory {
		pragmas = append(pragmas, "PRAGMA mmap_size=67108864")
	}
	for _, pragma := range pragmas {
		if _, err := conn.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("set %q: %w", pragma, err)
		}
	}
	return nil
}

// closedPlaceholder returns a process-wide handle that is already closed, so
// GetDB never returns nil and every query on it fails with "sql: database is
// closed".
func closedPlaceholder() *sql.DB {
	closedPlaceholderOnce.Do(func() {
		handle, err := sql.Open("sqlite", "file::memory:")
		if err != nil {
			// Only a missing driver can fail here, and that is a programming
			// error rather than a runtime condition.
			log.Fatalf("Failed to create closed database placeholder: %v", err)
		}
		_ = handle.Close()
		closedPlaceholderDB = handle
	})
	return closedPlaceholderDB
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

// isMemoryDBName reports whether the configured database is an in-memory one.
// A :memory: database must not be given WAL or a multi-connection pool.
func isMemoryDBName(dbName string) bool {
	if dbName == "" || dbName == ":memory:" {
		return true
	}
	if strings.HasPrefix(dbName, "file:") {
		return strings.Contains(dbName, "mode=memory") || strings.Contains(dbName, ":memory:")
	}
	return false
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
	// Every PRAGMA here applies to each pooled connection. Executing them via
	// db.Exec instead would configure whichever single connection served that
	// call, leaving the other 11 in the pool on defaults.
	pragmas := url.Values{}
	pragmas.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", sqliteBusyTimeoutMS))
	pragmas.Add("_pragma", "foreign_keys(ON)")
	// Write transactions start with BEGIN IMMEDIATE so they take the write
	// lock up front and wait busy_timeout for it. A deferred transaction that
	// reads first and then writes is handed SQLITE_BUSY at once (no waiting)
	// when another writer committed in between. Read-only transactions
	// (sql.TxOptions{ReadOnly: true}) still begin deferred.
	pragmas.Add("_txlock", "immediate")
	if isMemoryDBName(dbName) {
		// A memory database cannot use WAL and does not benefit from the page
		// cache/mmap tuning, but it still needs busy_timeout, foreign_keys and
		// (most importantly) immediate transactions.
		pragmas.Add("_pragma", "temp_store(MEMORY)")
		target := dbName
		if target == "" || target == ":memory:" || strings.Contains(target, ":memory:") {
			target = "file::memory:"
		}
		sep := "?"
		if strings.Contains(target, "?") {
			sep = "&"
		}
		return target + sep + pragmas.Encode()
	}
	pragmas.Add("_pragma", "journal_mode(WAL)")
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
// After ShutdownDB it returns a closed handle instead of opening a new one:
// every query then fails with "sql: database is closed", which is what a caller
// running past shutdown should see. It never returns nil.
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
		if handle != nil {
			return handle
		}
		return closedPlaceholder()
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
	handle := db
	db = nil
	// Reset the guard even when no handle is published yet: an initialization
	// in flight holds the old guard and will notice the swap and discard its
	// freshly opened handle instead of publishing it after this close returned.
	once = &sync.Once{}
	if handle == nil {
		return nil
	}
	err := handle.Close()
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
	return fetchAllToTable(GetDB(), query, args...)
}

// fetchAllToTable is FetchAllToTable against an explicit querier, so a caller
// holding a transaction can read several statements from one snapshot.
func fetchAllToTable(q querier, query string, args ...interface{}) ([]map[string]interface{}, error) {
	rows, err := q.Query(query, args...)
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
	var id int64
	err := retryOnBusy(func() error {
		result, err := GetDB().Exec(query, args...)
		if err != nil {
			return err
		}
		id, err = result.LastInsertId()
		return err
	})
	return id, err
}

// ExecuteUpdate executes an update/delete query, retrying lock contention.
func ExecuteUpdate(query string, args ...interface{}) error {
	return retryOnBusy(func() error {
		_, err := GetDB().Exec(query, args...)
		return err
	})
}

// executeUpdateAffected is ExecuteUpdate plus the number of rows it changed, so
// callers can turn "nothing matched" into a not-found error instead of silently
// reporting success.
func executeUpdateAffected(query string, args ...interface{}) (int64, error) {
	var affected int64
	err := retryOnBusy(func() error {
		result, err := GetDB().Exec(query, args...)
		if err != nil {
			return err
		}
		affected, err = result.RowsAffected()
		return err
	})
	return affected, err
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

// FetchAllToPage executes a paginated query. The total comes from a separate
// COUNT(*) wrapped around baseSQL; both statements run in one read-only
// transaction so the page and the total are taken from the same snapshot.
//
// The previous implementation injected a COUNT(*) OVER() window column by
// rewriting the SQL text. That made every page materialise and sort the whole
// match set, and it broke on perfectly legal statements: `SELECT DISTINCT`
// became a syntax error and a UNION separated by newlines lost the total.
// Wrapping the statement is shape agnostic and cannot corrupt its syntax.
//
// A request that omits pagination is capped at defaultUnpagedLimit rows and
// fails when the result would not fit, rather than returning a short list next
// to the full count. A request whose pageSize exceeds MaxPageSize is capped;
// the paginated response reports the effective pageSize.
func FetchAllToPage(baseSQL string, params map[string]interface{}, sqlArgs ...interface{}) (map[string]interface{}, error) {
	return FetchAllToPageWithCount(baseSQL, wrappedCountSQL(baseSQL), params, sqlArgs...)
}

// wrappedCountSQL builds a shape-agnostic COUNT(*) for a single complete
// statement (SELECT/UNION/WITH). baseSQL is always built internally; a trailing
// semicolon is stripped so the statement can be nested in a subquery.
func wrappedCountSQL(baseSQL string) string {
	return "SELECT COUNT(*) FROM (" + strings.TrimSuffix(strings.TrimSpace(baseSQL), ";") + ")"
}

// FetchAllToPageWithCount pages baseSQL like FetchAllToPage, but takes the total
// from countSQL instead of wrapping baseSQL, so a caller can hand it an
// index-friendly COUNT (for job_task_item, a plain COUNT(*) over the same WHERE
// clause is answered from an index while the page itself stops after LIMIT
// rows). countSQL must take exactly sqlArgs.
//
// Both statements run in one read transaction so the page and the total come
// from the same snapshot even while a running task keeps inserting items.
func FetchAllToPageWithCount(baseSQL, countSQL string, params map[string]interface{}, sqlArgs ...interface{}) (map[string]interface{}, error) {
	ps, pn, paginated, err := parsePageParams(params)
	if err != nil {
		return nil, err
	}
	if !paginated {
		return fetchPageSeparateCount(baseSQL, countSQL, defaultUnpagedLimit, 0, false, sqlArgs)
	}
	offset, err := pageOffset(ps, pn)
	if err != nil {
		return nil, err
	}
	return fetchPageSeparateCount(baseSQL, countSQL, ps, offset, true, sqlArgs)
}

func fetchPageSeparateCount(baseSQL, countSQL string, limit int, offset int64, paginated bool, sqlArgs []interface{}) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := withReadTx(func(tx *sql.Tx) error {
		var total int64
		if err := tx.QueryRow(countSQL, sqlArgs...).Scan(&total); err != nil {
			return err
		}
		if !paginated && total > int64(limit) {
			// Same contract as fetchPage: an unpaginated request that does not
			// fit is refused rather than answered with a silently short list.
			return listTooLargeError()
		}
		dataList := make([]map[string]interface{}, 0)
		if total > 0 && offset < total {
			query := baseSQL + " LIMIT ?"
			args := appendSQLArgs(sqlArgs, limit)
			if paginated {
				query += " OFFSET ?"
				args = appendSQLArgs(args, offset)
			}
			pageQueryTrace(query)
			rows, err := fetchAllToTable(tx, query, args...)
			if err != nil {
				return err
			}
			dataList = rows
		}
		result = map[string]interface{}{"dataList": dataList, "count": total}
		if paginated {
			// Report the effective page size: a caller that asked for more than
			// MaxPageSize silently got a narrower window, and without this it
			// cannot tell that its page offsets no longer match its request.
			result["pageSize"] = limit
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// fetchPage is the entry point kept for callers/tests that page by explicit
// limit/offset; it derives the total the same way FetchAllToPage does.
func fetchPage(baseSQL string, limit int, offset int64, paginated bool, sqlArgs []interface{}) (map[string]interface{}, error) {
	return fetchPageSeparateCount(baseSQL, wrappedCountSQL(baseSQL), limit, offset, paginated, sqlArgs)
}

// pageQueryTrace observes the data query of a separate-count page; tests use it
// to assert which SQL a list endpoint actually runs.
var pageQueryTrace = func(string) {}

func appendSQLArgs(args []interface{}, extra ...interface{}) []interface{} {
	result := make([]interface{}, 0, len(args)+len(extra))
	result = append(result, args...)
	return append(result, extra...)
}

func pageOffset(pageSize, pageNum int) (int64, error) {
	if pageSize <= 0 || pageNum <= 0 {
		return 0, invalidPaginationError()
	}
	index := int64(pageNum) - 1
	size := int64(pageSize)
	maxInt64 := int64(^uint64(0) >> 1)
	if index > maxInt64/size {
		return 0, invalidPaginationError()
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
		return 0, 0, false, invalidPaginationError()
	}

	pageSize, err = positiveInt(pageSizeVal)
	if err != nil {
		return 0, 0, false, invalidPaginationError()
	}
	pageNum, err = positiveInt(pageNumVal)
	if err != nil {
		return 0, 0, false, invalidPaginationError()
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return pageSize, pageNum, true, nil
}

func positiveInt(v interface{}) (int, error) {
	if value, ok := v.(float64); ok && math.Trunc(value) != value {
		return 0, invalidPaginationError()
	}
	n := util.ToInt64(v)
	if n <= 0 || n > int64(math.MaxInt) {
		return 0, invalidPaginationError()
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
