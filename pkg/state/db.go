package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/jaimegago/petri/pkg/types"
)

const timeFormat = time.RFC3339

// dbManager implements Manager on top of any database/sql compatible driver.
// Use NewPostgresManager or NewSQLiteManager to create instances.
type dbManager struct {
	db     *sql.DB
	driver string // "pgx" or "sqlite"
}

// rebind converts ? placeholders to $1, $2, ... for PostgreSQL.
// SQLite uses ? natively, so no conversion is needed for that driver.
func (m *dbManager) rebind(query string) string {
	if m.driver != "pgx" {
		return query
	}
	n := 0
	var buf strings.Builder
	buf.Grow(len(query) + 10)
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			n++
			fmt.Fprintf(&buf, "$%d", n)
		} else {
			buf.WriteByte(query[i])
		}
	}
	return buf.String()
}

// migrate runs all provided DDL statements. Each statement is idempotent
// (uses IF NOT EXISTS) so migrations can safely run on every start-up.
func (m *dbManager) migrate(ctx context.Context, migrations []string) error {
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning migration transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	for _, stmt := range migrations {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("executing migration statement: %w", err)
		}
	}
	return tx.Commit()
}

// Close releases the underlying database connection pool.
func (m *dbManager) Close() error {
	return m.db.Close()
}

// CreateLab inserts a new lab record.
func (m *dbManager) CreateLab(ctx context.Context, lab *types.Lab) error {
	meta, err := json.Marshal(lab.Metadata)
	if err != nil {
		return fmt.Errorf("marshaling lab metadata: %w", err)
	}
	q := m.rebind(`INSERT INTO labs
		(id, name, company, level, cloud_provider, status, created_at, ttl_hours, expires_at, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	_, err = m.db.ExecContext(ctx, q,
		lab.ID.String(),
		lab.Name,
		lab.Company,
		lab.Level,
		string(lab.CloudProvider),
		string(lab.Status),
		lab.CreatedAt.UTC().Format(timeFormat),
		lab.TTLHours,
		lab.ExpiresAt.UTC().Format(timeFormat),
		string(meta),
	)
	if err != nil {
		return fmt.Errorf("inserting lab: %w", err)
	}
	return nil
}

// GetLab retrieves a lab by UUID.
func (m *dbManager) GetLab(ctx context.Context, id uuid.UUID) (*types.Lab, error) {
	q := m.rebind(`SELECT id, name, company, level, cloud_provider, status, created_at, ttl_hours, expires_at, metadata
		FROM labs WHERE id = ?`)
	row := m.db.QueryRowContext(ctx, q, id.String())
	lab, err := scanLabRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("lab %s not found: %w", id, err)
		}
		return nil, fmt.Errorf("getting lab %s: %w", id, err)
	}
	return lab, nil
}

// GetLabByName retrieves a lab by its unique name.
func (m *dbManager) GetLabByName(ctx context.Context, name string) (*types.Lab, error) {
	q := m.rebind(`SELECT id, name, company, level, cloud_provider, status, created_at, ttl_hours, expires_at, metadata
		FROM labs WHERE name = ?`)
	row := m.db.QueryRowContext(ctx, q, name)
	lab, err := scanLabRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("lab %q not found: %w", name, err)
		}
		return nil, fmt.Errorf("getting lab %q: %w", name, err)
	}
	return lab, nil
}

// UpdateLab persists status, metadata, and TTL changes to a lab.
func (m *dbManager) UpdateLab(ctx context.Context, lab *types.Lab) error {
	meta, err := json.Marshal(lab.Metadata)
	if err != nil {
		return fmt.Errorf("marshaling lab metadata: %w", err)
	}
	q := m.rebind(`UPDATE labs SET status = ?, metadata = ?, expires_at = ? WHERE id = ?`)
	res, err := m.db.ExecContext(ctx, q,
		string(lab.Status),
		string(meta),
		lab.ExpiresAt.UTC().Format(timeFormat),
		lab.ID.String(),
	)
	if err != nil {
		return fmt.Errorf("updating lab %s: %w", lab.ID, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("lab %s not found for update", lab.ID)
	}
	return nil
}

// DeleteLab removes a lab record and cascades to resources and credentials.
func (m *dbManager) DeleteLab(ctx context.Context, id uuid.UUID) error {
	q := m.rebind(`DELETE FROM labs WHERE id = ?`)
	_, err := m.db.ExecContext(ctx, q, id.String())
	if err != nil {
		return fmt.Errorf("deleting lab %s: %w", id, err)
	}
	return nil
}

// ListLabs returns labs matching the filter, ordered newest first.
func (m *dbManager) ListLabs(ctx context.Context, filter ListFilter) ([]*types.Lab, error) {
	where := []string{"1=1"}
	args := []interface{}{}

	if filter.Company != "" {
		args = append(args, filter.Company)
		where = append(where, "company = ?")
	}
	if filter.Level > 0 {
		args = append(args, filter.Level)
		where = append(where, "level = ?")
	}
	if filter.Status != "" {
		args = append(args, string(filter.Status))
		where = append(where, "status = ?")
	}
	if !filter.IncludeExpired {
		args = append(args, time.Now().UTC().Format(timeFormat))
		where = append(where, "expires_at > ?")
	}

	q := fmt.Sprintf(
		`SELECT id, name, company, level, cloud_provider, status, created_at, ttl_hours, expires_at, metadata
		FROM labs WHERE %s ORDER BY created_at DESC`,
		strings.Join(where, " AND "),
	)
	q = m.rebind(q)

	rows, err := m.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("listing labs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var labs []*types.Lab
	for rows.Next() {
		lab, err := scanLabRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning lab: %w", err)
		}
		labs = append(labs, lab)
	}
	return labs, rows.Err()
}

// FindExpiredLabs returns labs whose TTL elapsed more than gracePeriod ago
// and that have not yet been destroyed or marked for destruction.
func (m *dbManager) FindExpiredLabs(ctx context.Context, gracePeriod time.Duration) ([]*types.Lab, error) {
	cutoff := time.Now().Add(-gracePeriod).UTC().Format(timeFormat)
	q := m.rebind(`SELECT id, name, company, level, cloud_provider, status, created_at, ttl_hours, expires_at, metadata
		FROM labs
		WHERE expires_at < ? AND status NOT IN ('DESTROYED', 'DESTROYING')
		ORDER BY expires_at ASC`)

	rows, err := m.db.QueryContext(ctx, q, cutoff)
	if err != nil {
		return nil, fmt.Errorf("querying expired labs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var labs []*types.Lab
	for rows.Next() {
		lab, err := scanLabRow(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning expired lab: %w", err)
		}
		labs = append(labs, lab)
	}
	return labs, rows.Err()
}

// CreateResource records a provisioned resource for cleanup tracking.
func (m *dbManager) CreateResource(ctx context.Context, r *types.LabResource) error {
	meta, err := json.Marshal(r.Metadata)
	if err != nil {
		return fmt.Errorf("marshaling resource metadata: %w", err)
	}
	q := m.rebind(`INSERT INTO lab_resources (id, lab_id, resource_type, resource_id, cloud_resource_id, metadata)
		VALUES (?, ?, ?, ?, ?, ?)`)
	_, err = m.db.ExecContext(ctx, q,
		r.ID.String(),
		r.LabID.String(),
		r.ResourceType,
		r.ResourceID,
		r.CloudResourceID,
		string(meta),
	)
	if err != nil {
		return fmt.Errorf("inserting resource: %w", err)
	}
	return nil
}

// ListResources returns all resource records for a lab.
func (m *dbManager) ListResources(ctx context.Context, labID uuid.UUID) ([]*types.LabResource, error) {
	q := m.rebind(`SELECT id, lab_id, resource_type, resource_id, cloud_resource_id, metadata
		FROM lab_resources WHERE lab_id = ?`)
	rows, err := m.db.QueryContext(ctx, q, labID.String())
	if err != nil {
		return nil, fmt.Errorf("listing resources for lab %s: %w", labID, err)
	}
	defer func() { _ = rows.Close() }()

	var resources []*types.LabResource
	for rows.Next() {
		r := &types.LabResource{}
		var idStr, labIDStr, cloudResID, metaStr string
		if err := rows.Scan(&idStr, &labIDStr, &r.ResourceType, &r.ResourceID, &cloudResID, &metaStr); err != nil {
			return nil, fmt.Errorf("scanning resource row: %w", err)
		}
		r.ID, _ = uuid.Parse(idStr)
		r.LabID, _ = uuid.Parse(labIDStr)
		r.CloudResourceID = cloudResID
		if err := json.Unmarshal([]byte(metaStr), &r.Metadata); err != nil {
			r.Metadata = map[string]string{}
		}
		resources = append(resources, r)
	}
	return resources, rows.Err()
}

// DeleteResources removes all resource records for a lab.
func (m *dbManager) DeleteResources(ctx context.Context, labID uuid.UUID) error {
	q := m.rebind(`DELETE FROM lab_resources WHERE lab_id = ?`)
	_, err := m.db.ExecContext(ctx, q, labID.String())
	if err != nil {
		return fmt.Errorf("deleting resources for lab %s: %w", labID, err)
	}
	return nil
}

// StoreCredential saves an encrypted credential for a lab.
// If a credential of the same type already exists it is replaced (upsert).
func (m *dbManager) StoreCredential(ctx context.Context, cred *types.LabCredential) error {
	q := m.rebind(`INSERT INTO lab_credentials (id, lab_id, credential_type, encrypted_value, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (lab_id, credential_type) DO UPDATE SET encrypted_value = excluded.encrypted_value`)
	_, err := m.db.ExecContext(ctx, q,
		cred.ID.String(),
		cred.LabID.String(),
		cred.CredentialType,
		cred.EncryptedValue,
		cred.CreatedAt.UTC().Format(timeFormat),
	)
	if err != nil {
		return fmt.Errorf("storing credential %q for lab %s: %w", cred.CredentialType, cred.LabID, err)
	}
	return nil
}

// GetCredential retrieves an encrypted credential by lab and type.
func (m *dbManager) GetCredential(ctx context.Context, labID uuid.UUID, credType string) (*types.LabCredential, error) {
	q := m.rebind(`SELECT id, lab_id, credential_type, encrypted_value, created_at
		FROM lab_credentials WHERE lab_id = ? AND credential_type = ?`)
	row := m.db.QueryRowContext(ctx, q, labID.String(), credType)

	var c types.LabCredential
	var idStr, labIDStr, createdAtStr string
	if err := row.Scan(&idStr, &labIDStr, &c.CredentialType, &c.EncryptedValue, &createdAtStr); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("credential %q not found for lab %s: %w", credType, labID, err)
		}
		return nil, fmt.Errorf("scanning credential: %w", err)
	}
	c.ID, _ = uuid.Parse(idStr)
	c.LabID, _ = uuid.Parse(labIDStr)
	c.CreatedAt, _ = time.Parse(timeFormat, createdAtStr)
	return &c, nil
}

// DeleteCredentials removes all credentials for a lab.
func (m *dbManager) DeleteCredentials(ctx context.Context, labID uuid.UUID) error {
	q := m.rebind(`DELETE FROM lab_credentials WHERE lab_id = ?`)
	_, err := m.db.ExecContext(ctx, q, labID.String())
	if err != nil {
		return fmt.Errorf("deleting credentials for lab %s: %w", labID, err)
	}
	return nil
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...interface{}) error
}

// scanLabRow scans a lab from any rowScanner (sql.Row or sql.Rows).
func scanLabRow(row rowScanner) (*types.Lab, error) {
	var lab types.Lab
	var idStr, cloudProvider, status, createdAtStr, expiresAtStr, metaStr string

	if err := row.Scan(
		&idStr, &lab.Name, &lab.Company, &lab.Level,
		&cloudProvider, &status,
		&createdAtStr, &lab.TTLHours, &expiresAtStr,
		&metaStr,
	); err != nil {
		return nil, err
	}

	var err error
	lab.ID, err = uuid.Parse(idStr)
	if err != nil {
		return nil, fmt.Errorf("parsing lab ID %q: %w", idStr, err)
	}
	lab.CloudProvider = types.CloudProvider(cloudProvider)
	lab.Status = types.LabStatus(status)

	lab.CreatedAt, err = time.Parse(timeFormat, createdAtStr)
	if err != nil {
		return nil, fmt.Errorf("parsing created_at %q: %w", createdAtStr, err)
	}
	lab.ExpiresAt, err = time.Parse(timeFormat, expiresAtStr)
	if err != nil {
		return nil, fmt.Errorf("parsing expires_at %q: %w", expiresAtStr, err)
	}

	if err := json.Unmarshal([]byte(metaStr), &lab.Metadata); err != nil {
		lab.Metadata = types.LabMetadata{}
	}
	return &lab, nil
}
