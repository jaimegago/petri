package state

// pgMigrations contains ordered idempotent DDL statements for the PostgreSQL schema.
var pgMigrations = []string{
	`CREATE TABLE IF NOT EXISTS labs (
		id              UUID PRIMARY KEY,
		name            VARCHAR(255) UNIQUE NOT NULL,
		company         VARCHAR(100) NOT NULL,
		level           INTEGER NOT NULL,
		cloud_provider  VARCHAR(50) NOT NULL,
		status          VARCHAR(50) NOT NULL,
		created_at      TIMESTAMPTZ NOT NULL,
		ttl_hours       INTEGER NOT NULL,
		expires_at      TIMESTAMPTZ NOT NULL,
		metadata        JSONB NOT NULL DEFAULT '{}'
	)`,
	`CREATE TABLE IF NOT EXISTS lab_resources (
		id                UUID PRIMARY KEY,
		lab_id            UUID NOT NULL REFERENCES labs(id) ON DELETE CASCADE,
		resource_type     VARCHAR(100) NOT NULL,
		resource_id       VARCHAR(255) NOT NULL,
		cloud_resource_id VARCHAR(255),
		metadata          JSONB NOT NULL DEFAULT '{}'
	)`,
	`CREATE INDEX IF NOT EXISTS lab_resources_lab_id_idx ON lab_resources(lab_id)`,
	`CREATE TABLE IF NOT EXISTS lab_credentials (
		id               UUID PRIMARY KEY,
		lab_id           UUID NOT NULL REFERENCES labs(id) ON DELETE CASCADE,
		credential_type  VARCHAR(100) NOT NULL,
		encrypted_value  TEXT NOT NULL,
		created_at       TIMESTAMPTZ NOT NULL,
		UNIQUE(lab_id, credential_type)
	)`,
	`CREATE INDEX IF NOT EXISTS lab_credentials_lab_id_idx ON lab_credentials(lab_id)`,
}

// sqliteMigrations contains ordered idempotent DDL statements for the SQLite schema.
// Timestamps are stored as RFC3339 TEXT. Metadata is stored as JSON TEXT.
var sqliteMigrations = []string{
	`CREATE TABLE IF NOT EXISTS labs (
		id              TEXT PRIMARY KEY,
		name            TEXT UNIQUE NOT NULL,
		company         TEXT NOT NULL,
		level           INTEGER NOT NULL,
		cloud_provider  TEXT NOT NULL,
		status          TEXT NOT NULL,
		created_at      TEXT NOT NULL,
		ttl_hours       INTEGER NOT NULL,
		expires_at      TEXT NOT NULL,
		metadata        TEXT NOT NULL DEFAULT '{}'
	)`,
	`CREATE TABLE IF NOT EXISTS lab_resources (
		id                TEXT PRIMARY KEY,
		lab_id            TEXT NOT NULL REFERENCES labs(id) ON DELETE CASCADE,
		resource_type     TEXT NOT NULL,
		resource_id       TEXT NOT NULL,
		cloud_resource_id TEXT,
		metadata          TEXT NOT NULL DEFAULT '{}'
	)`,
	`CREATE INDEX IF NOT EXISTS lab_resources_lab_id_idx ON lab_resources(lab_id)`,
	`CREATE TABLE IF NOT EXISTS lab_credentials (
		id               TEXT PRIMARY KEY,
		lab_id           TEXT NOT NULL REFERENCES labs(id) ON DELETE CASCADE,
		credential_type  TEXT NOT NULL,
		encrypted_value  TEXT NOT NULL,
		created_at       TEXT NOT NULL,
		UNIQUE(lab_id, credential_type)
	)`,
	`CREATE INDEX IF NOT EXISTS lab_credentials_lab_id_idx ON lab_credentials(lab_id)`,
}
