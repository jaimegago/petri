package state

import (
	"context"
	"database/sql"
	"fmt"

	// pgx registers itself as the "pgx" driver for database/sql.
	_ "github.com/jackc/pgx/v5/stdlib"
)

// NewPostgresManager opens a PostgreSQL connection pool and runs schema migrations.
// connStr should be a libpq-style connection string, e.g.
// "postgres://user:pass@localhost/petri?sslmode=disable".
func NewPostgresManager(ctx context.Context, connStr string) (Manager, error) {
	db, err := sql.Open("pgx", connStr)
	if err != nil {
		return nil, fmt.Errorf("opening postgres connection: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connecting to postgres (%s): %w", connStr, err)
	}

	m := &dbManager{db: db, driver: "pgx"}
	if err := m.migrate(ctx, pgMigrations); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("running postgres migrations: %w", err)
	}
	return m, nil
}
