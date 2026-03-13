package cli

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
)

func TestRunList_EmptyList(t *testing.T) {
	mgr := state.NewMockManager()
	c := newTestCLI(mgr, companiesYAML(t))

	err := c.runList("", 0, "", false, "table")
	if err != nil {
		t.Errorf("expected nil error for empty list, got: %v", err)
	}
}

func TestRunList_WithLabs_Table(t *testing.T) {
	mgr := state.NewMockManager()
	lab := &types.Lab{
		ID:            uuid.New(),
		Name:          "test-lab",
		Company:       "acme",
		Level:         1,
		CloudProvider: types.CloudProviderLocal,
		Status:        types.LabStatusActive,
		CreatedAt:     time.Now(),
		ExpiresAt:     time.Now().Add(4 * time.Hour),
		TTLHours:      4,
	}
	if err := mgr.CreateLab(nil, lab); err != nil { //nolint:staticcheck
		t.Fatalf("fixture: %v", err)
	}

	c := newTestCLI(mgr, companiesYAML(t))
	err := c.runList("", 0, "", false, "table")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunList_WithLabs_JSON(t *testing.T) {
	mgr := state.NewMockManager()
	lab := &types.Lab{
		ID:        uuid.New(),
		Name:      "json-lab",
		Company:   "acme",
		Level:     1,
		Status:    types.LabStatusActive,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(4 * time.Hour),
		TTLHours:  4,
	}
	if err := mgr.CreateLab(nil, lab); err != nil { //nolint:staticcheck
		t.Fatalf("fixture: %v", err)
	}

	c := newTestCLI(mgr, companiesYAML(t))
	err := c.runList("", 0, "", false, "json")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunList_FilterByCompany(t *testing.T) {
	mgr := state.NewMockManager()

	for i, name := range []string{"acme-lab", "techflow-lab"} {
		companies := []string{"acme", "techflow"}
		lab := &types.Lab{
			ID:        uuid.New(),
			Name:      name,
			Company:   companies[i],
			Level:     1,
			Status:    types.LabStatusActive,
			CreatedAt: time.Now(),
			ExpiresAt: time.Now().Add(4 * time.Hour),
			TTLHours:  4,
		}
		if err := mgr.CreateLab(nil, lab); err != nil { //nolint:staticcheck
			t.Fatalf("fixture: %v", err)
		}
	}

	c := newTestCLI(mgr, companiesYAML(t))

	// Filter to acme only — should not error.
	err := c.runList("acme", 0, "", false, "table")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunList_FilterByStatus(t *testing.T) {
	mgr := state.NewMockManager()
	lab := &types.Lab{
		ID:        uuid.New(),
		Name:      "active-lab",
		Company:   "acme",
		Level:     1,
		Status:    types.LabStatusActive,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(4 * time.Hour),
		TTLHours:  4,
	}
	if err := mgr.CreateLab(nil, lab); err != nil { //nolint:staticcheck
		t.Fatalf("fixture: %v", err)
	}

	c := newTestCLI(mgr, companiesYAML(t))

	// Filter to CREATING — no match, should print "No labs found."
	err := c.runList("", 0, string(types.LabStatusCreating), false, "table")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunList_AliveOnly_ExcludesExpired(t *testing.T) {
	mgr := state.NewMockManager()
	expiredLab := &types.Lab{
		ID:        uuid.New(),
		Name:      "expired-lab",
		Company:   "acme",
		Level:     1,
		Status:    types.LabStatusActive,
		CreatedAt: time.Now().Add(-5 * time.Hour),
		ExpiresAt: time.Now().Add(-1 * time.Hour), // already expired
		TTLHours:  4,
	}
	if err := mgr.CreateLab(nil, expiredLab); err != nil { //nolint:staticcheck
		t.Fatalf("fixture: %v", err)
	}

	c := newTestCLI(mgr, companiesYAML(t))

	// aliveOnly=true should exclude the expired lab → "No labs found."
	err := c.runList("", 0, "", true, "table")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
