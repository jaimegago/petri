package cli

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
)

// newTestCLI returns a CLI pre-wired with a MockManager and a no-op logger.
// companiesFile should point at configs/companies.yaml (relative to repo root).
func newTestCLI(mgr state.Manager, companiesFile string) *CLI {
	return &CLI{
		stateMgr:      mgr,
		companiesFile: companiesFile,
		log:           slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
}

// companiesYAML returns the path to the real companies fixture.
// Tests are run from the package directory, so we walk up two levels.
func companiesYAML(t *testing.T) string {
	t.Helper()
	// internal/cli -> repo root is ../../
	return "../../configs/companies.yaml"
}

func TestRunCreate_DuplicateActiveLabRejected(t *testing.T) {
	mgr := state.NewMockManager()
	existing := &types.Lab{
		ID:        uuid.New(),
		Name:      "my-lab",
		Company:   "acme",
		Level:     1,
		Status:    types.LabStatusActive,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(4 * time.Hour),
		TTLHours:  4,
	}
	if err := mgr.CreateLab(nil, existing); err != nil { //nolint:staticcheck
		t.Fatalf("fixture setup: %v", err)
	}

	c := newTestCLI(mgr, companiesYAML(t))
	err := c.runCreate(&createOptions{
		company: "acme",
		level:   1,
		name:    "my-lab",
		local:   true,
	})

	if err == nil {
		t.Fatal("expected error for duplicate active lab, got nil")
	}
	const want = `lab "my-lab" already exists (status: ACTIVE)`
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}

func TestRunCreate_DestroyedLabAllowsRecreation(t *testing.T) {
	mgr := state.NewMockManager()
	existing := &types.Lab{
		ID:        uuid.New(),
		Name:      "my-lab",
		Company:   "acme",
		Level:     1,
		Status:    types.LabStatusDestroyed,
		CreatedAt: time.Now().Add(-2 * time.Hour),
		ExpiresAt: time.Now().Add(-1 * time.Hour),
		TTLHours:  1,
	}
	if err := mgr.CreateLab(nil, existing); err != nil { //nolint:staticcheck
		t.Fatalf("fixture setup: %v", err)
	}

	c := newTestCLI(mgr, companiesYAML(t))
	err := c.runCreate(&createOptions{
		company: "acme",
		level:   1,
		name:    "my-lab",
		local:   true,
	})

	// The old record must have been deleted before the INSERT.
	// The error we expect here is from buildOrchestrator (no config loaded), NOT a
	// UNIQUE constraint or "already exists" error.
	if err != nil {
		const uniqueErr = "recording lab in state"
		const alreadyExists = "already exists"
		msg := err.Error()
		for _, bad := range []string{uniqueErr, alreadyExists} {
			if contains(msg, bad) {
				t.Errorf("expected duplicate-guard to be bypassed, but got: %v", err)
			}
		}
	}

	// The terminal record must have been replaced with a new CREATING lab.
	found, err2 := mgr.GetLabByName(nil, "my-lab") //nolint:staticcheck
	if err2 != nil {
		t.Fatalf("lab not found after re-creation attempt: %v", err2)
	}
	if found.ID == existing.ID {
		t.Error("expected new lab UUID after re-creation, got the same one as the destroyed lab")
	}
	if found.Status != types.LabStatusCreating {
		t.Errorf("new lab status = %s, want CREATING", found.Status)
	}
}

func TestRunCreate_ErrorLabAllowsRecreation(t *testing.T) {
	mgr := state.NewMockManager()
	existing := &types.Lab{
		ID:        uuid.New(),
		Name:      "my-lab",
		Company:   "acme",
		Level:     1,
		Status:    types.LabStatusError,
		CreatedAt: time.Now().Add(-1 * time.Hour),
		ExpiresAt: time.Now().Add(3 * time.Hour),
		TTLHours:  4,
	}
	if err := mgr.CreateLab(nil, existing); err != nil { //nolint:staticcheck
		t.Fatalf("fixture setup: %v", err)
	}

	c := newTestCLI(mgr, companiesYAML(t))
	err := c.runCreate(&createOptions{
		company: "acme",
		level:   1,
		name:    "my-lab",
		local:   true,
	})

	if err != nil {
		msg := err.Error()
		for _, bad := range []string{"recording lab in state", "already exists"} {
			if contains(msg, bad) {
				t.Errorf("expected duplicate-guard to be bypassed, but got: %v", err)
			}
		}
	}

	found, err2 := mgr.GetLabByName(nil, "my-lab") //nolint:staticcheck
	if err2 != nil {
		t.Fatalf("lab not found after re-creation attempt: %v", err2)
	}
	if found.ID == existing.ID {
		t.Error("expected new lab UUID after re-creation, got the same one as the error lab")
	}
}

// contains is a simple substring check used to avoid importing strings in test helper.
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
