package mapper

import (
	"errors"
	"log"
	"strings"
	"time"
)

// sqliteBusyTimeoutMS is how long SQLite itself waits for the write lock before
// returning SQLITE_BUSY. 5s was too short on NAS spinning disks, where a few
// concurrent fsync-bound commits (several jobs flushing items, the poller,
// a user action) routinely queue past it.
const sqliteBusyTimeoutMS = 15000

// writeBusyRetryDelays are the waits between attempts of a mapper-level write
// that still hit lock contention after busy_timeout. Kept short: each attempt
// already waited busy_timeout inside SQLite, and callers with their own retry
// loop (task item persistence) layer on top of this.
var writeBusyRetryDelays = []time.Duration{500 * time.Millisecond}

// sqliteCodeError is satisfied by modernc.org/sqlite's *Error.
type sqliteCodeError interface {
	Code() int
}

// IsBusyError reports whether err is lock contention (SQLITE_BUSY or
// SQLITE_LOCKED, including their extended codes) rather than a real failure.
func IsBusyError(err error) bool {
	if err == nil {
		return false
	}
	var coded sqliteCodeError
	if errors.As(err, &coded) {
		switch coded.Code() & 0xff {
		case 5, 6: // SQLITE_BUSY, SQLITE_LOCKED
			return true
		}
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "database is locked") ||
		strings.Contains(text, "database table is locked") ||
		strings.Contains(text, "sqlite_busy")
}

// retryOnBusy runs fn and repeats it while it fails with lock contention.
// fn must be safe to repeat: a single statement, or a whole transaction that
// was rolled back.
func retryOnBusy(fn func() error) error {
	err := fn()
	for attempt := 0; attempt < len(writeBusyRetryDelays) && IsBusyError(err); attempt++ {
		log.Printf("Database busy, retrying write (%d/%d): %v", attempt+1, len(writeBusyRetryDelays), err)
		time.Sleep(writeBusyRetryDelays[attempt])
		err = fn()
	}
	return err
}
