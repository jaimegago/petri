package state

import (
	"context"
	"database/sql"
	"fmt"

	// modernc.org/sqlite registers itself as the "sqlite" driver for database/sql.
	_ "modernc.org/sqlite"
)

// NewSQLiteManager opens (or creates) a SQLite database at path and runs schema migrations.
// Pass ":memory:" for an ephemeral in-memory database suitable for tests.
func NewSQLiteManager(ctx context.Context, path string) (Manager, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite database at %s: %w", path, err)
	}

	// SQLite does not support concurrent writers; restrict to a single connection.
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connecting to sqlite at %s: %w", path, err)
	}

	// Enable foreign key enforcement (off by default in SQLite).
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enabling sqlite foreign keys: %w", err)
	}

	m := &dbManager{db: db, driver: "sqlite"}
	if err := m.migrate(ctx, sqliteMigrations); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("running sqlite migrations: %w", err)
	}
	return m, nil
}
