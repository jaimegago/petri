package state_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaimegago/petri/pkg/config"
	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
)

// TestNew_SQLite verifies state.New returns a working SQLiteManager for the
// sqlite backend, using an in-memory database to avoid disk I/O.
func TestNew_SQLite(t *testing.T) {
	cfg := config.StateConfig{
		Backend:    "sqlite",
		SQLitePath: ":memory:",
	}
	mgr, err := state.New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("state.New(sqlite): %v", err)
	}
	defer mgr.Close()

	// Basic smoke test: create a lab and retrieve it.
	lab := &types.Lab{
		ID:            uuid.New(),
		Name:          "new-factory-lab",
		Company:       "acme",
		Level:         1,
		CloudProvider: types.CloudProviderLocal,
		Status:        types.LabStatusCreating,
		CreatedAt:     time.Now().UTC().Truncate(time.Second),
		TTLHours:      2,
		ExpiresAt:     time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second),
	}
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatalf("CreateLab: %v", err)
	}
	got, err := mgr.GetLab(context.Background(), lab.ID)
	if err != nil {
		t.Fatalf("GetLab: %v", err)
	}
	if got.Name != lab.Name {
		t.Errorf("Name: got %q, want %q", got.Name, lab.Name)
	}
}

// TestNew_DefaultBackend verifies that an empty backend string defaults to sqlite.
func TestNew_DefaultBackend(t *testing.T) {
	cfg := config.StateConfig{
		Backend:    "", // empty → defaults to sqlite
		SQLitePath: ":memory:",
	}
	mgr, err := state.New(context.Background(), cfg)
	if err != nil {
		t.Fatalf("state.New(default backend): %v", err)
	}
	defer mgr.Close()
}

// TestNew_SQLite_MissingPath verifies that state.New returns an error when
// sqlite is configured without a path.
func TestNew_SQLite_MissingPath(t *testing.T) {
	cfg := config.StateConfig{
		Backend:    "sqlite",
		SQLitePath: "",
	}
	_, err := state.New(context.Background(), cfg)
	if err == nil {
		t.Error("expected error when sqlite_path is empty")
	}
}

// TestNew_UnknownBackend verifies that state.New rejects unknown backends.
func TestNew_UnknownBackend(t *testing.T) {
	cfg := config.StateConfig{Backend: "badbackend"}
	_, err := state.New(context.Background(), cfg)
	if err == nil {
		t.Error("expected error for unknown backend")
	}
}

// ── MockManager ────────────────────────────────────────────────────────────────

func TestMockManager_BasicCRUD(t *testing.T) {
	mgr := state.NewMockManager()
	ctx := context.Background()

	lab := &types.Lab{
		ID:            uuid.New(),
		Name:          "mock-lab",
		Company:       "acme",
		Level:         1,
		CloudProvider: types.CloudProviderLocal,
		Status:        types.LabStatusCreating,
		CreatedAt:     time.Now().UTC(),
		TTLHours:      2,
		ExpiresAt:     time.Now().UTC().Add(2 * time.Hour),
	}

	// CreateLab
	if err := mgr.CreateLab(ctx, lab); err != nil {
		t.Fatalf("CreateLab: %v", err)
	}

	// GetLab
	got, err := mgr.GetLab(ctx, lab.ID)
	if err != nil {
		t.Fatalf("GetLab: %v", err)
	}
	if got.Name != lab.Name {
		t.Errorf("Name: got %q, want %q", got.Name, lab.Name)
	}

	// GetLabByName
	byName, err := mgr.GetLabByName(ctx, lab.Name)
	if err != nil {
		t.Fatalf("GetLabByName: %v", err)
	}
	if byName.ID != lab.ID {
		t.Errorf("GetLabByName ID mismatch")
	}

	// UpdateLab
	lab.Status = types.LabStatusActive
	if err := mgr.UpdateLab(ctx, lab); err != nil {
		t.Fatalf("UpdateLab: %v", err)
	}
	updated, _ := mgr.GetLab(ctx, lab.ID)
	if updated.Status != types.LabStatusActive {
		t.Errorf("UpdateLab: status not updated")
	}

	// ListLabs
	labs, err := mgr.ListLabs(ctx, state.ListFilter{IncludeExpired: true})
	if err != nil {
		t.Fatalf("ListLabs: %v", err)
	}
	if len(labs) != 1 {
		t.Errorf("ListLabs: got %d, want 1", len(labs))
	}

	// DeleteLab
	if err := mgr.DeleteLab(ctx, lab.ID); err != nil {
		t.Fatalf("DeleteLab: %v", err)
	}
	_, err = mgr.GetLab(ctx, lab.ID)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected ErrNoRows after delete, got: %v", err)
	}
}

func TestMockManager_Resources(t *testing.T) {
	mgr := state.NewMockManager()
	ctx := context.Background()

	lab := &types.Lab{
		ID:      uuid.New(),
		Name:    "res-lab",
		Company: "acme",
		Status:  types.LabStatusActive,
	}
	if err := mgr.CreateLab(ctx, lab); err != nil {
		t.Fatal(err)
	}

	r := &types.LabResource{
		ID:           uuid.New(),
		LabID:        lab.ID,
		ResourceType: "kind_cluster",
		ResourceID:   "cluster-1",
	}
	if err := mgr.CreateResource(ctx, r); err != nil {
		t.Fatalf("CreateResource: %v", err)
	}

	resources, err := mgr.ListResources(ctx, lab.ID)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(resources) != 1 {
		t.Errorf("got %d resources, want 1", len(resources))
	}

	if err := mgr.DeleteResources(ctx, lab.ID); err != nil {
		t.Fatalf("DeleteResources: %v", err)
	}
	resources, _ = mgr.ListResources(ctx, lab.ID)
	if len(resources) != 0 {
		t.Errorf("got %d resources after delete, want 0", len(resources))
	}
}

func TestMockManager_Credentials(t *testing.T) {
	mgr := state.NewMockManager()
	ctx := context.Background()

	lab := &types.Lab{ID: uuid.New(), Name: "cred-mock-lab", Status: types.LabStatusActive}
	if err := mgr.CreateLab(ctx, lab); err != nil {
		t.Fatal(err)
	}

	cred := &types.LabCredential{
		ID:             uuid.New(),
		LabID:          lab.ID,
		CredentialType: "kubeconfig",
		EncryptedValue: "base64data==",
		CreatedAt:      time.Now().UTC(),
	}
	if err := mgr.StoreCredential(ctx, cred); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}

	got, err := mgr.GetCredential(ctx, lab.ID, "kubeconfig")
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if got.EncryptedValue != cred.EncryptedValue {
		t.Errorf("EncryptedValue mismatch")
	}

	if err := mgr.DeleteCredentials(ctx, lab.ID); err != nil {
		t.Fatalf("DeleteCredentials: %v", err)
	}
	_, err = mgr.GetCredential(ctx, lab.ID, "kubeconfig")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected ErrNoRows after delete, got: %v", err)
	}
}

func TestMockManager_FindExpiredLabs(t *testing.T) {
	mgr := state.NewMockManager()
	ctx := context.Background()

	expired := &types.Lab{
		ID:        uuid.New(),
		Name:      "expired-mock",
		Company:   "acme",
		Status:    types.LabStatusActive,
		ExpiresAt: time.Now().Add(-1 * time.Hour),
	}
	active := &types.Lab{
		ID:        uuid.New(),
		Name:      "active-mock",
		Company:   "acme",
		Status:    types.LabStatusActive,
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}
	for _, lab := range []*types.Lab{expired, active} {
		if err := mgr.CreateLab(ctx, lab); err != nil {
			t.Fatalf("CreateLab: %v", err)
		}
	}

	labs, err := mgr.FindExpiredLabs(ctx, 0)
	if err != nil {
		t.Fatalf("FindExpiredLabs: %v", err)
	}
	if len(labs) != 1 {
		t.Errorf("got %d labs, want 1", len(labs))
	}
	if len(labs) > 0 && labs[0].Name != "expired-mock" {
		t.Errorf("got %q, want expired-mock", labs[0].Name)
	}
}

func TestMockManager_Close(t *testing.T) {
	mgr := state.NewMockManager()
	if err := mgr.Close(); err != nil {
		t.Errorf("Close returned error: %v", err)
	}
}

func TestMockManager_GetLabNotFound(t *testing.T) {
	mgr := state.NewMockManager()
	_, err := mgr.GetLab(context.Background(), uuid.New())
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected ErrNoRows, got: %v", err)
	}
}

func TestMockManager_GetLabByNameNotFound(t *testing.T) {
	mgr := state.NewMockManager()
	_, err := mgr.GetLabByName(context.Background(), "does-not-exist")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected ErrNoRows, got: %v", err)
	}
}

func TestMockManager_UpdateLabNotFound(t *testing.T) {
	mgr := state.NewMockManager()
	lab := &types.Lab{ID: uuid.New(), Name: "ghost"}
	err := mgr.UpdateLab(context.Background(), lab)
	if err == nil {
		t.Error("expected error for non-existent lab update")
	}
}

func TestMockManager_ListLabs_Filter(t *testing.T) {
	mgr := state.NewMockManager()
	ctx := context.Background()

	acme := &types.Lab{
		ID:        uuid.New(),
		Name:      "acme-lab",
		Company:   "acme",
		Level:     1,
		Status:    types.LabStatusActive,
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}
	techflow := &types.Lab{
		ID:        uuid.New(),
		Name:      "techflow-lab",
		Company:   "techflow",
		Level:     2,
		Status:    types.LabStatusActive,
		ExpiresAt: time.Now().Add(1 * time.Hour),
	}
	for _, lab := range []*types.Lab{acme, techflow} {
		if err := mgr.CreateLab(ctx, lab); err != nil {
			t.Fatalf("CreateLab: %v", err)
		}
	}

	// Filter by company.
	labs, err := mgr.ListLabs(ctx, state.ListFilter{Company: "acme", IncludeExpired: true})
	if err != nil {
		t.Fatalf("ListLabs: %v", err)
	}
	if len(labs) != 1 || labs[0].Name != "acme-lab" {
		t.Errorf("filter by company: got %v", labs)
	}

	// Filter by level.
	labs, err = mgr.ListLabs(ctx, state.ListFilter{Level: 2, IncludeExpired: true})
	if err != nil {
		t.Fatalf("ListLabs: %v", err)
	}
	if len(labs) != 1 || labs[0].Name != "techflow-lab" {
		t.Errorf("filter by level: got %v", labs)
	}
}
