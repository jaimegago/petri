// Package state manages lab persistence in PostgreSQL and SQLite.
// All Manager implementations are safe for concurrent use.
package state

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/jaimegago/petri/pkg/config"
	"github.com/jaimegago/petri/pkg/types"
)

// Manager defines all state persistence operations for Petri labs.
// Callers should use the Manager interface, not the concrete types,
// to allow swapping backends and for easier testing.
type Manager interface {
	// CreateLab inserts a new lab record. Returns an error if the name is already taken.
	CreateLab(ctx context.Context, lab *types.Lab) error

	// GetLab retrieves a lab by its UUID.
	GetLab(ctx context.Context, id uuid.UUID) (*types.Lab, error)

	// GetLabByName retrieves a lab by its unique name.
	GetLabByName(ctx context.Context, name string) (*types.Lab, error)

	// UpdateLab persists status, metadata, and TTL changes to an existing lab.
	UpdateLab(ctx context.Context, lab *types.Lab) error

	// DeleteLab removes the lab record permanently (cascades to resources and credentials).
	DeleteLab(ctx context.Context, id uuid.UUID) error

	// ListLabs returns all labs matching the given filter, newest first.
	ListLabs(ctx context.Context, filter ListFilter) ([]*types.Lab, error)

	// CreateResource records a provisioned cloud or Kubernetes resource for cleanup tracking.
	CreateResource(ctx context.Context, resource *types.LabResource) error

	// ListResources returns all resource records associated with the given lab.
	ListResources(ctx context.Context, labID uuid.UUID) ([]*types.LabResource, error)

	// DeleteResources removes all resource records for a lab.
	DeleteResources(ctx context.Context, labID uuid.UUID) error

	// StoreCredential saves an encrypted credential scoped to a lab.
	// If a credential of the same type already exists it is replaced.
	StoreCredential(ctx context.Context, cred *types.LabCredential) error

	// GetCredential retrieves an encrypted credential by lab ID and type.
	GetCredential(ctx context.Context, labID uuid.UUID, credType string) (*types.LabCredential, error)

	// DeleteCredentials removes all credential records for a lab.
	DeleteCredentials(ctx context.Context, labID uuid.UUID) error

	// FindExpiredLabs returns active labs whose TTL has elapsed by more than gracePeriod.
	// DESTROYED and DESTROYING labs are excluded.
	FindExpiredLabs(ctx context.Context, gracePeriod time.Duration) ([]*types.Lab, error)

	// Close releases any underlying database connections.
	Close() error
}

// ListFilter restricts which labs are returned by ListLabs.
type ListFilter struct {
	// Company filters by exact company name when non-empty.
	Company string
	// Level filters by complexity level; 0 means no level filter.
	Level int
	// Status filters by lab status when non-empty.
	Status types.LabStatus
	// IncludeExpired includes labs past their ExpiresAt when true.
	// By default only unexpired labs are returned.
	IncludeExpired bool
}

// New constructs a Manager from the given state configuration.
// It returns a PostgresManager when cfg.Backend is "postgresql" or "postgres",
// and a SQLiteManager for "sqlite" or an empty backend string.
func New(ctx context.Context, cfg config.StateConfig) (Manager, error) {
	switch cfg.Backend {
	case "postgresql", "postgres":
		return NewPostgresManager(ctx, cfg.ConnectionString)
	case "sqlite", "":
		path := cfg.SQLitePath
		if path == "" {
			return nil, fmt.Errorf("sqlite_path must be configured for the sqlite backend")
		}
		return NewSQLiteManager(ctx, path)
	default:
		return nil, fmt.Errorf("unknown state backend %q (supported: postgresql, sqlite)", cfg.Backend)
	}
}
