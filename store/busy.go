package store

import (
	"errors"
	"strings"
	"time"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// This file implements the SQLITE_BUSY retry policy: bounded retry
// with backoff for write operations whose lock acquisition failed
// because another process holds the WAL write lock.
//
// SQLITE_BUSY is transient by nature — the competing writer commits or
// rolls back and the lock is released. But it is demonstrably NOT
// serialized by busy_timeout on this driver's WAL write path across
// processes: the busy handler is not invoked when the WAL shm lock
// acquisition fails (empirically the statement returns SQLITE_BUSY
// immediately, regardless of the configured timeout). A bounded retry
// loop with fresh statement execution is therefore the reliable
// serialization mechanism for concurrent eka processes; busy_timeout
// stays in the DSN as defense in depth for the paths where the busy
// handler does engage (in-process contention).

// maxBusyAttempts is the total number of attempts of one retried
// write: the original attempt plus up to 4 retries.
const maxBusyAttempts = 5

// busyBackoff is the sleep between attempts (100ms, 200ms, 400ms,
// 800ms): the full retry sequence adds at most 1.5s of waiting.
var busyBackoff = [...]time.Duration{
	100 * time.Millisecond,
	200 * time.Millisecond,
	400 * time.Millisecond,
	800 * time.Millisecond,
}

// retryBusy runs op, re-running it when it fails with SQLITE_BUSY up
// to maxBusyAttempts times with a growing backoff. Any other error is
// returned immediately, never retried. Callers run their whole write
// transaction inside op: a busy failure aborts the transaction (the
// deferred Rollback releases the connection) and the retry starts a
// fresh one.
func retryBusy(op func() error) error {
	var err error
	for attempt := 0; attempt < maxBusyAttempts; attempt++ {
		err = op()
		if !isBusy(err) {
			return err
		}
		if attempt < len(busyBackoff) {
			time.Sleep(busyBackoff[attempt])
		}
	}
	return err
}

// isBusy reports whether err is SQLITE_BUSY ("database is locked").
// modernc surfaces it as *sqlite.Error with Code() SQLITE_BUSY; the
// string match is a fallback for any wrapping layer.
func isBusy(err error) bool {
	if err == nil {
		return false
	}
	var se *sqlite.Error
	if errors.As(err, &se) && se.Code() == sqlite3.SQLITE_BUSY {
		return true
	}
	return strings.Contains(err.Error(), "database is locked")
}
