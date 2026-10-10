package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	// modernc.org/sqlite also registers itself as the "sqlite" driver for database/sql.
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// sqliteBusyTimeoutMS is how long a connection waits on another process's
// lock before returning SQLITE_BUSY. Petri's writes are short; this bounds a
// wait behind a concurrent `petri create` or the `petri serve` reaper.
const sqliteBusyTimeoutMS = 10000

// walRetryInterval paces enableWAL's retries while another process holds the
// lock it needs.
const walRetryInterval = 10 * time.Millisecond

// sqliteDSNParams are applied by the driver to every connection it opens, so
// they survive a pooled connection being replaced:
//   - busy_timeout: wait on a held lock instead of failing immediately.
//   - foreign_keys: off by default in SQLite.
//   - _txlock=immediate: BEGIN takes the write lock up front, so a
//     transaction that reads then writes waits under busy_timeout rather than
//     failing its lock upgrade with SQLITE_BUSY (which the busy handler cannot
//     resolve).
var sqliteDSNParams = fmt.Sprintf(
	"_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)&_txlock=immediate",
	sqliteBusyTimeoutMS,
)

// sqliteDSN appends sqliteDSNParams to path. The driver splits the DSN at the
// first '?', so a path that already carries one is refused rather than
// misparsed.
func sqliteDSN(path string) (string, error) {
	if strings.Contains(path, "?") {
		return "", fmt.Errorf("sqlite path %q must not contain '?'", path)
	}
	return path + "?" + sqliteDSNParams, nil
}

// NewSQLiteManager opens (or creates) a SQLite database at path and runs schema migrations.
// Pass ":memory:" for an ephemeral in-memory database suitable for tests.
//
// Several petri processes may share one file (two `petri create` runs, or
// `petri create` alongside `petri serve`); sqliteDSNParams and enableWAL are
// what make that safe.
func NewSQLiteManager(ctx context.Context, path string) (Manager, error) {
	dsn, err := sqliteDSN(path)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite database at %s: %w", path, err)
	}

	// SQLite allows one writer at a time; within a process, serialise on a
	// single connection. Cross-process contention is handled by the DSN params.
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connecting to sqlite at %s: %w", path, err)
	}

	if err := enableWAL(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}

	m := &dbManager{db: db, driver: "sqlite"}
	if err := m.migrate(ctx, sqliteMigrations); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("running sqlite migrations: %w", err)
	}
	return m, nil
}

// enableWAL switches the file to write-ahead logging, so readers no longer
// block the writer or vice versa. The mode is persistent in the file, so once
// any process has switched it the pragma is a read; it is a no-op for
// ":memory:".
//
// The switch itself is not covered by busy_timeout: it upgrades a shared lock
// to an exclusive one, and SQLite fails a contended upgrade with SQLITE_BUSY
// immediately rather than risk deadlock. Two processes opening a fresh file
// at once hit exactly that, so the switch retries on SQLITE_BUSY for as long
// as busy_timeout would have waited.
func enableWAL(ctx context.Context, db *sql.DB) error {
	deadline := time.Now().Add(sqliteBusyTimeoutMS * time.Millisecond)
	for {
		var mode string
		err := db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&mode)
		if err == nil {
			return nil
		}
		if !isSQLiteBusy(err) || time.Now().After(deadline) {
			return fmt.Errorf("enabling sqlite WAL journal mode: %w", err)
		}
		t := time.NewTimer(walRetryInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("enabling sqlite WAL journal mode: %w", ctx.Err())
		case <-t.C:
		}
	}
}

// isSQLiteBusy reports whether err is SQLITE_BUSY or one of its extended codes.
func isSQLiteBusy(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code()&0xff == sqlite3.SQLITE_BUSY
}
