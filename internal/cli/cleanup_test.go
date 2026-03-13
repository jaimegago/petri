package cli

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaimegago/petri/pkg/config"
	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
)

// newTestCLIWithConfig returns a CLI pre-wired with a MockManager and a config.
func newTestCLIWithConfig(mgr state.Manager, companiesFile string, cfg *config.Config) *CLI {
	c := newTestCLI(mgr, companiesFile)
	c.cfg = cfg
	return c
}

func minimalConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Cleanup.GracePeriod = 0
	return cfg
}

func TestRunCleanup_NoFlag(t *testing.T) {
	mgr := state.NewMockManager()
	c := newTestCLIWithConfig(mgr, companiesYAML(t), minimalConfig())

	// Without --expired the function prints a hint and returns nil.
	err := c.runCleanup(false)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunCleanup_NoExpiredLabs(t *testing.T) {
	mgr := state.NewMockManager()
	// Add a non-expired lab.
	lab := &types.Lab{
		ID:        uuid.New(),
		Name:      "alive-lab",
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

	c := newTestCLIWithConfig(mgr, companiesYAML(t), minimalConfig())
	err := c.runCleanup(true)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunCleanup_LabInNonDestroyableStatus_IsSkipped(t *testing.T) {
	mgr := state.NewMockManager()
	// A DESTROYING lab cannot be transitioned again — it should be skipped.
	lab := &types.Lab{
		ID:        uuid.New(),
		Name:      "already-destroying",
		Company:   "acme",
		Level:     1,
		Status:    types.LabStatusDestroying,
		CreatedAt: time.Now().Add(-5 * time.Hour),
		ExpiresAt: time.Now().Add(-2 * time.Hour),
		TTLHours:  3,
	}
	if err := mgr.CreateLab(nil, lab); err != nil { //nolint:staticcheck
		t.Fatalf("fixture: %v", err)
	}

	cfg := minimalConfig()
	// Grace period 0 so the lab is immediately eligible.
	cfg.Cleanup.GracePeriod = 0
	c := newTestCLIWithConfig(mgr, companiesYAML(t), cfg)

	// The mock FindExpiredLabs skips DESTROYING labs, so no labs are returned
	// and no orchestrator is needed.
	err := c.runCleanup(true)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunCleanup_ExpiredLabReachesOrchestratorStep(t *testing.T) {
	mgr := state.NewMockManager()
	lab := &types.Lab{
		ID:            uuid.New(),
		Name:          "expired-for-cleanup",
		Company:       "acme",
		Level:         1,
		CloudProvider: types.CloudProviderLocal,
		Status:        types.LabStatusActive,
		CreatedAt:     time.Now().Add(-5 * time.Hour),
		ExpiresAt:     time.Now().Add(-2 * time.Hour),
		TTLHours:      3,
	}
	if err := mgr.CreateLab(nil, lab); err != nil { //nolint:staticcheck
		t.Fatalf("fixture: %v", err)
	}

	cfg := minimalConfig()
	cfg.Cleanup.GracePeriod = 0
	// No master key path → buildOrchestrator will fail, but cleanup should
	// accumulate errors and return nil (it just prints the summary).
	c := newTestCLIWithConfig(mgr, companiesYAML(t), cfg)

	// cleanup always returns nil even when individual destroys fail.
	err := c.runCleanup(true)
	if err != nil {
		t.Errorf("runCleanup should not propagate individual destroy errors, got: %v", err)
	}
}
