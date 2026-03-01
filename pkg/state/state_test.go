package state_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
)

// newTestManager creates a fresh in-memory SQLite Manager for each test.
func newTestManager(t *testing.T) state.Manager {
	t.Helper()
	m, err := state.NewSQLiteManager(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// testLab returns a valid Lab ready for insertion.
func testLab(name string) *types.Lab {
	now := time.Now().UTC().Truncate(time.Second)
	return &types.Lab{
		ID:            uuid.New(),
		Name:          name,
		Company:       "acme",
		Level:         1,
		CloudProvider: types.CloudProviderLocal,
		Status:        types.LabStatusCreating,
		CreatedAt:     now,
		TTLHours:      4,
		ExpiresAt:     now.Add(4 * time.Hour),
	}
}

func TestCreateAndGetLab(t *testing.T) {
	tests := []struct {
		name    string
		labName string
	}{
		{"basic", "test-lab-1"},
		{"with_hyphens", "my-acme-lab-abc123"},
		{"with_metadata", "lab-with-meta"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestManager(t)
			ctx := context.Background()

			lab := testLab(tt.labName)
			if tt.name == "with_metadata" {
				lab.Metadata = types.LabMetadata{
					WorkDir:      "/tmp/labs/test",
					ErrorMessage: "",
					ObservabilityURLs: map[string]string{
						"grafana":    "http://localhost:3000",
						"prometheus": "http://localhost:9090",
					},
				}
			}

			if err := m.CreateLab(ctx, lab); err != nil {
				t.Fatalf("CreateLab: %v", err)
			}

			got, err := m.GetLab(ctx, lab.ID)
			if err != nil {
				t.Fatalf("GetLab: %v", err)
			}
			if got.Name != lab.Name {
				t.Errorf("Name: got %q, want %q", got.Name, lab.Name)
			}
			if got.Company != lab.Company {
				t.Errorf("Company: got %q, want %q", got.Company, lab.Company)
			}
			if got.Status != lab.Status {
				t.Errorf("Status: got %q, want %q", got.Status, lab.Status)
			}

			byName, err := m.GetLabByName(ctx, lab.Name)
			if err != nil {
				t.Fatalf("GetLabByName: %v", err)
			}
			if byName.ID != lab.ID {
				t.Errorf("GetLabByName ID: got %v, want %v", byName.ID, lab.ID)
			}
		})
	}
}

func TestCreateLabDuplicateName(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	lab := testLab("duplicate-name")
	if err := m.CreateLab(ctx, lab); err != nil {
		t.Fatalf("first CreateLab: %v", err)
	}

	lab2 := testLab("duplicate-name")
	lab2.ID = uuid.New()
	if err := m.CreateLab(ctx, lab2); err == nil {
		t.Error("expected error for duplicate lab name, got nil")
	}
}

func TestGetLabNotFound(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	_, err := m.GetLab(ctx, uuid.New())
	if err == nil {
		t.Fatal("expected error for unknown lab ID, got nil")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows in error chain, got: %v", err)
	}
}

func TestGetLabByNameNotFound(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	_, err := m.GetLabByName(ctx, "does-not-exist")
	if err == nil {
		t.Fatal("expected error for unknown lab name, got nil")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows in error chain, got: %v", err)
	}
}

func TestUpdateLab(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	lab := testLab("update-test")
	if err := m.CreateLab(ctx, lab); err != nil {
		t.Fatalf("CreateLab: %v", err)
	}

	lab.Status = types.LabStatusActive
	lab.Metadata = types.LabMetadata{WorkDir: "/tmp/updated"}
	if err := m.UpdateLab(ctx, lab); err != nil {
		t.Fatalf("UpdateLab: %v", err)
	}

	got, err := m.GetLab(ctx, lab.ID)
	if err != nil {
		t.Fatalf("GetLab after update: %v", err)
	}
	if got.Status != types.LabStatusActive {
		t.Errorf("Status: got %q, want %q", got.Status, types.LabStatusActive)
	}
	if got.Metadata.WorkDir != "/tmp/updated" {
		t.Errorf("Metadata.WorkDir: got %q, want %q", got.Metadata.WorkDir, "/tmp/updated")
	}
}

func TestUpdateLabNotFound(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	lab := testLab("ghost")
	if err := m.UpdateLab(ctx, lab); err == nil {
		t.Error("expected error updating non-existent lab, got nil")
	}
}

func TestDeleteLab(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	lab := testLab("delete-me")
	if err := m.CreateLab(ctx, lab); err != nil {
		t.Fatalf("CreateLab: %v", err)
	}
	if err := m.DeleteLab(ctx, lab.ID); err != nil {
		t.Fatalf("DeleteLab: %v", err)
	}

	_, err := m.GetLab(ctx, lab.ID)
	if err == nil {
		t.Error("expected error after deletion, got nil")
	}
}

func TestListLabs(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	acme1 := testLab("acme-l1")
	acme1.Company = "acme"
	acme1.Level = 1

	acme2 := testLab("acme-l2")
	acme2.Company = "acme"
	acme2.Level = 2

	techflow := testLab("techflow-l1")
	techflow.Company = "techflow"
	techflow.Level = 1

	for _, lab := range []*types.Lab{acme1, acme2, techflow} {
		if err := m.CreateLab(ctx, lab); err != nil {
			t.Fatalf("CreateLab %q: %v", lab.Name, err)
		}
	}

	tests := []struct {
		name      string
		filter    state.ListFilter
		wantCount int
	}{
		{"no_filter", state.ListFilter{IncludeExpired: true}, 3},
		{"by_company_acme", state.ListFilter{Company: "acme", IncludeExpired: true}, 2},
		{"by_company_techflow", state.ListFilter{Company: "techflow", IncludeExpired: true}, 1},
		{"by_level_1", state.ListFilter{Level: 1, IncludeExpired: true}, 2},
		{"by_company_and_level", state.ListFilter{Company: "acme", Level: 2, IncludeExpired: true}, 1},
		{"none_match", state.ListFilter{Company: "cloudnative", IncludeExpired: true}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labs, err := m.ListLabs(ctx, tt.filter)
			if err != nil {
				t.Fatalf("ListLabs: %v", err)
			}
			if len(labs) != tt.wantCount {
				t.Errorf("count: got %d, want %d", len(labs), tt.wantCount)
			}
		})
	}
}

func TestListLabsExpiredFilter(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	active := testLab("active-lab")
	active.ExpiresAt = time.Now().Add(1 * time.Hour)

	expired := testLab("expired-lab")
	expired.ExpiresAt = time.Now().Add(-1 * time.Hour)

	for _, lab := range []*types.Lab{active, expired} {
		if err := m.CreateLab(ctx, lab); err != nil {
			t.Fatalf("CreateLab %q: %v", lab.Name, err)
		}
	}

	t.Run("exclude_expired", func(t *testing.T) {
		labs, err := m.ListLabs(ctx, state.ListFilter{IncludeExpired: false})
		if err != nil {
			t.Fatalf("ListLabs: %v", err)
		}
		if len(labs) != 1 {
			t.Errorf("got %d labs, want 1 (only active)", len(labs))
		}
		if len(labs) > 0 && labs[0].Name != "active-lab" {
			t.Errorf("got %q, want %q", labs[0].Name, "active-lab")
		}
	})

	t.Run("include_expired", func(t *testing.T) {
		labs, err := m.ListLabs(ctx, state.ListFilter{IncludeExpired: true})
		if err != nil {
			t.Fatalf("ListLabs: %v", err)
		}
		if len(labs) != 2 {
			t.Errorf("got %d labs, want 2", len(labs))
		}
	})
}

func TestFindExpiredLabs(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	active := testLab("still-active")
	active.ExpiresAt = time.Now().Add(2 * time.Hour)
	active.Status = types.LabStatusActive

	justExpired := testLab("just-expired")
	justExpired.ExpiresAt = time.Now().Add(-5 * time.Minute)
	justExpired.Status = types.LabStatusActive

	longExpired := testLab("long-expired")
	longExpired.ExpiresAt = time.Now().Add(-2 * time.Hour)
	longExpired.Status = types.LabStatusActive

	destroyed := testLab("already-destroyed")
	destroyed.ExpiresAt = time.Now().Add(-3 * time.Hour)
	destroyed.Status = types.LabStatusDestroyed

	for _, lab := range []*types.Lab{active, justExpired, longExpired, destroyed} {
		if err := m.CreateLab(ctx, lab); err != nil {
			t.Fatalf("CreateLab %q: %v", lab.Name, err)
		}
	}

	t.Run("grace_period_30m", func(t *testing.T) {
		// grace = 30m: only labs expired > 30m ago qualify.
		labs, err := m.FindExpiredLabs(ctx, 30*time.Minute)
		if err != nil {
			t.Fatalf("FindExpiredLabs: %v", err)
		}
		// longExpired qualifies; justExpired does not (expired only 5m ago, within grace).
		if len(labs) != 1 {
			t.Errorf("got %d labs, want 1", len(labs))
		}
		if len(labs) > 0 && labs[0].Name != "long-expired" {
			t.Errorf("got %q, want %q", labs[0].Name, "long-expired")
		}
	})

	t.Run("grace_period_0", func(t *testing.T) {
		// grace = 0: all expired non-destroyed labs qualify.
		labs, err := m.FindExpiredLabs(ctx, 0)
		if err != nil {
			t.Fatalf("FindExpiredLabs: %v", err)
		}
		if len(labs) != 2 {
			t.Errorf("got %d labs, want 2 (justExpired + longExpired)", len(labs))
		}
	})
}

func TestLabResources(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	lab := testLab("resource-lab")
	if err := m.CreateLab(ctx, lab); err != nil {
		t.Fatalf("CreateLab: %v", err)
	}

	r1 := &types.LabResource{
		ID:           uuid.New(),
		LabID:        lab.ID,
		ResourceType: "kind_cluster",
		ResourceID:   "petri-test-control-plane",
		Metadata:     map[string]string{"region": "local"},
	}
	r2 := &types.LabResource{
		ID:              uuid.New(),
		LabID:           lab.ID,
		ResourceType:    "git_repo",
		ResourceID:      "acme-lab-infra",
		CloudResourceID: "github:org/acme-lab-infra",
	}

	for _, r := range []*types.LabResource{r1, r2} {
		if err := m.CreateResource(ctx, r); err != nil {
			t.Fatalf("CreateResource %q: %v", r.ResourceID, err)
		}
	}

	resources, err := m.ListResources(ctx, lab.ID)
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(resources) != 2 {
		t.Errorf("got %d resources, want 2", len(resources))
	}

	if err := m.DeleteResources(ctx, lab.ID); err != nil {
		t.Fatalf("DeleteResources: %v", err)
	}
	resources, err = m.ListResources(ctx, lab.ID)
	if err != nil {
		t.Fatalf("ListResources after delete: %v", err)
	}
	if len(resources) != 0 {
		t.Errorf("got %d resources after delete, want 0", len(resources))
	}
}

func TestLabCredentials(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	lab := testLab("cred-lab")
	if err := m.CreateLab(ctx, lab); err != nil {
		t.Fatalf("CreateLab: %v", err)
	}

	cred := &types.LabCredential{
		ID:             uuid.New(),
		LabID:          lab.ID,
		CredentialType: "aws_secret_key",
		EncryptedValue: "base64encryptedvalue==",
		CreatedAt:      time.Now().UTC().Truncate(time.Second),
	}

	if err := m.StoreCredential(ctx, cred); err != nil {
		t.Fatalf("StoreCredential: %v", err)
	}

	got, err := m.GetCredential(ctx, lab.ID, "aws_secret_key")
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if got.EncryptedValue != cred.EncryptedValue {
		t.Errorf("EncryptedValue: got %q, want %q", got.EncryptedValue, cred.EncryptedValue)
	}

	// Upsert: store updated value for same type.
	cred.EncryptedValue = "newencryptedvalue=="
	if err := m.StoreCredential(ctx, cred); err != nil {
		t.Fatalf("StoreCredential upsert: %v", err)
	}
	updated, err := m.GetCredential(ctx, lab.ID, "aws_secret_key")
	if err != nil {
		t.Fatalf("GetCredential after upsert: %v", err)
	}
	if updated.EncryptedValue != "newencryptedvalue==" {
		t.Errorf("upsert: got %q, want %q", updated.EncryptedValue, "newencryptedvalue==")
	}

	if err := m.DeleteCredentials(ctx, lab.ID); err != nil {
		t.Fatalf("DeleteCredentials: %v", err)
	}
	_, err = m.GetCredential(ctx, lab.ID, "aws_secret_key")
	if err == nil {
		t.Error("expected error after deleting credentials, got nil")
	}
}

func TestGetCredentialNotFound(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	lab := testLab("no-cred-lab")
	if err := m.CreateLab(ctx, lab); err != nil {
		t.Fatalf("CreateLab: %v", err)
	}

	_, err := m.GetCredential(ctx, lab.ID, "nonexistent_type")
	if err == nil {
		t.Error("expected error for missing credential, got nil")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows in error chain, got: %v", err)
	}
}
