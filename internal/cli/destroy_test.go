package cli

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
)

func TestRunDestroy_LabNotFound(t *testing.T) {
	mgr := state.NewMockManager()
	c := newTestCLI(mgr, companiesYAML(t))

	err := c.runDestroy("no-such-lab", false)
	if err == nil {
		t.Fatal("expected error for missing lab, got nil")
	}
}

func TestRunDestroy_CannotTransition_AlreadyDestroyed(t *testing.T) {
	mgr := state.NewMockManager()
	lab := &types.Lab{
		ID:        uuid.New(),
		Name:      "done-lab",
		Company:   "acme",
		Level:     1,
		Status:    types.LabStatusDestroyed,
		CreatedAt: time.Now().Add(-4 * time.Hour),
		ExpiresAt: time.Now().Add(-1 * time.Hour),
		TTLHours:  3,
	}
	if err := mgr.CreateLab(nil, lab); err != nil { //nolint:staticcheck
		t.Fatalf("fixture: %v", err)
	}

	c := newTestCLI(mgr, companiesYAML(t))
	err := c.runDestroy("done-lab", false)
	if err == nil {
		t.Fatal("expected error for already-destroyed lab, got nil")
	}
}

func TestRunDestroy_CannotTransition_AlreadyDestroying(t *testing.T) {
	mgr := state.NewMockManager()
	lab := &types.Lab{
		ID:        uuid.New(),
		Name:      "destroying-lab",
		Company:   "acme",
		Level:     1,
		Status:    types.LabStatusDestroying,
		CreatedAt: time.Now().Add(-1 * time.Hour),
		ExpiresAt: time.Now().Add(3 * time.Hour),
		TTLHours:  4,
	}
	if err := mgr.CreateLab(nil, lab); err != nil { //nolint:staticcheck
		t.Fatalf("fixture: %v", err)
	}

	c := newTestCLI(mgr, companiesYAML(t))
	err := c.runDestroy("destroying-lab", false)
	if err == nil {
		t.Fatal("expected error for lab already destroying, got nil")
	}
}

func TestRunDestroy_ActiveLab_TransitionsBeforeOrchestratorFails(t *testing.T) {
	mgr := state.NewMockManager()
	lab := &types.Lab{
		ID:            uuid.New(),
		Name:          "active-lab",
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
	err := c.runDestroy("active-lab", false)

	// The orchestrator fails because there is no encryption key (no config).
	// But the lab must have been transitioned to DESTROYING in state first.
	if err == nil {
		t.Fatal("expected error from buildOrchestrator (no config), got nil")
	}

	updated, getErr := mgr.GetLabByName(nil, "active-lab") //nolint:staticcheck
	if getErr != nil {
		t.Fatalf("lab disappeared from state: %v", getErr)
	}
	if updated.Status != types.LabStatusDestroying {
		t.Errorf("expected status DESTROYING after transition, got %s", updated.Status)
	}
}

// ── resolveCompanyForLab ──────────────────────────────────────────────────────

func TestResolveCompanyForLab_Found(t *testing.T) {
	mgr := state.NewMockManager()
	c := newTestCLI(mgr, companiesYAML(t))

	lab := &types.Lab{Company: "acme", Level: 1}
	company, spec, err := c.resolveCompanyForLab(lab)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if company == nil {
		t.Fatal("expected non-nil company")
	}
	if spec.Clusters == 0 {
		t.Error("expected spec to be populated")
	}
}

func TestResolveCompanyForLab_NotFound(t *testing.T) {
	mgr := state.NewMockManager()
	c := newTestCLI(mgr, companiesYAML(t))

	lab := &types.Lab{Company: "nonexistent", Level: 1}
	_, _, err := c.resolveCompanyForLab(lab)
	if err == nil {
		t.Fatal("expected error for unknown company, got nil")
	}
}

// ── extractGitHubOrgFromRepos ─────────────────────────────────────────────────

func TestExtractGitHubOrgFromRepos(t *testing.T) {
	tests := []struct {
		name  string
		repos []types.GitRepo
		want  string
	}{
		{
			name: "extracts org from valid URL",
			repos: []types.GitRepo{
				{URL: "https://github.com/myorg/myrepo.git"},
			},
			want: "myorg",
		},
		{
			name:  "returns empty for no repos",
			repos: nil,
			want:  "",
		},
		{
			name: "returns empty for URL shorter than github prefix",
			repos: []types.GitRepo{
				{URL: "https://short"},
			},
			want: "",
		},
		{
			name: "uses first repo URL",
			repos: []types.GitRepo{
				{URL: "https://github.com/firstorg/repo.git"},
				{URL: "https://github.com/secondorg/repo.git"},
			},
			want: "firstorg",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lab := &types.Lab{Metadata: types.LabMetadata{GitRepos: tc.repos}}
			got := extractGitHubOrgFromRepos(lab)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
