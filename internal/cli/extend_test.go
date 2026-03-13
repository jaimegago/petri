package cli

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
)

func TestRunExtend_InvalidTTLFormat(t *testing.T) {
	mgr := state.NewMockManager()
	c := newTestCLI(mgr, companiesYAML(t))

	err := c.runExtend("any-lab", "notaduration")
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

func TestRunExtend_NegativeTTL(t *testing.T) {
	mgr := state.NewMockManager()
	c := newTestCLI(mgr, companiesYAML(t))

	err := c.runExtend("any-lab", "-1h")
	if err == nil {
		t.Fatal("expected error for negative TTL, got nil")
	}
}

func TestRunExtend_ZeroTTL(t *testing.T) {
	mgr := state.NewMockManager()
	c := newTestCLI(mgr, companiesYAML(t))

	err := c.runExtend("any-lab", "0s")
	if err == nil {
		t.Fatal("expected error for zero TTL, got nil")
	}
}

func TestRunExtend_LabNotFound(t *testing.T) {
	mgr := state.NewMockManager()
	c := newTestCLI(mgr, companiesYAML(t))

	err := c.runExtend("no-such-lab", "+1h")
	if err == nil {
		t.Fatal("expected error for missing lab, got nil")
	}
}

func TestRunExtend_Success(t *testing.T) {
	mgr := state.NewMockManager()
	originalExpiry := time.Now().Add(2 * time.Hour)
	lab := &types.Lab{
		ID:        uuid.New(),
		Name:      "extend-lab",
		Company:   "acme",
		Level:     1,
		Status:    types.LabStatusActive,
		CreatedAt: time.Now(),
		ExpiresAt: originalExpiry,
		TTLHours:  2,
	}
	if err := mgr.CreateLab(nil, lab); err != nil { //nolint:staticcheck
		t.Fatalf("fixture: %v", err)
	}

	c := newTestCLI(mgr, companiesYAML(t))
	err := c.runExtend("extend-lab", "+2h")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	updated, err := mgr.GetLabByName(nil, "extend-lab") //nolint:staticcheck
	if err != nil {
		t.Fatalf("lab not found after extend: %v", err)
	}
	if !updated.ExpiresAt.After(originalExpiry) {
		t.Errorf("expected ExpiresAt to increase, got %v (was %v)", updated.ExpiresAt, originalExpiry)
	}
	if updated.TTLHours != 4 {
		t.Errorf("expected TTLHours=4, got %d", updated.TTLHours)
	}
}

func TestRunExtend_WithoutPlusPrefix(t *testing.T) {
	mgr := state.NewMockManager()
	lab := &types.Lab{
		ID:        uuid.New(),
		Name:      "extend-no-plus",
		Company:   "acme",
		Level:     1,
		Status:    types.LabStatusActive,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(1 * time.Hour),
		TTLHours:  1,
	}
	if err := mgr.CreateLab(nil, lab); err != nil { //nolint:staticcheck
		t.Fatalf("fixture: %v", err)
	}

	c := newTestCLI(mgr, companiesYAML(t))
	// Without the leading '+' — should also work.
	err := c.runExtend("extend-no-plus", "1h")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
