package cli

import (
	"strings"
	"testing"

	"github.com/jaimegago/petri/pkg/state"
)

func TestRunCreate_InvalidLevel_TooLow(t *testing.T) {
	mgr := state.NewMockManager()
	c := newTestCLI(mgr, companiesYAML(t))

	err := c.runCreate(&createOptions{company: "acme", level: 0, local: true})
	if err == nil {
		t.Fatal("expected error for level=0, got nil")
	}
}

func TestRunCreate_InvalidLevel_TooHigh(t *testing.T) {
	mgr := state.NewMockManager()
	c := newTestCLI(mgr, companiesYAML(t))

	err := c.runCreate(&createOptions{company: "acme", level: 4, local: true})
	if err == nil {
		t.Fatal("expected error for level=4, got nil")
	}
}

func TestRunCreate_UnknownCompany(t *testing.T) {
	mgr := state.NewMockManager()
	c := newTestCLI(mgr, companiesYAML(t))

	err := c.runCreate(&createOptions{company: "nonexistent", level: 1, local: true})
	if err == nil {
		t.Fatal("expected error for unknown company, got nil")
	}
	if !strings.Contains(err.Error(), "unknown company") {
		t.Errorf("error = %q, want it to mention 'unknown company'", err.Error())
	}
}

func TestRunCreate_InvalidTTL(t *testing.T) {
	mgr := state.NewMockManager()
	c := newTestCLI(mgr, companiesYAML(t))

	err := c.runCreate(&createOptions{
		company: "acme",
		level:   1,
		local:   true,
		ttl:     "notaduration",
	})
	if err == nil {
		t.Fatal("expected parse error for invalid TTL, got nil")
	}
}

func TestRunCreate_DryRun(t *testing.T) {
	// dry-run returns before touching state or the orchestrator.
	mgr := state.NewMockManager()
	c := newTestCLI(mgr, companiesYAML(t))

	err := c.runCreate(&createOptions{
		company: "acme",
		level:   1,
		local:   true,
		name:    "dry-lab",
		dryRun:  true,
	})
	if err != nil {
		t.Errorf("unexpected error in dry-run: %v", err)
	}

	// Nothing should have been stored in state.
	labs, _ := mgr.ListLabs(nil, state.ListFilter{IncludeExpired: true}) //nolint:staticcheck
	if len(labs) != 0 {
		t.Errorf("expected no labs stored after dry-run, got %d", len(labs))
	}
}

func TestRunCreate_DryRun_WithTTL(t *testing.T) {
	mgr := state.NewMockManager()
	c := newTestCLI(mgr, companiesYAML(t))

	err := c.runCreate(&createOptions{
		company: "acme",
		level:   1,
		local:   true,
		name:    "dry-ttl-lab",
		dryRun:  true,
		ttl:     "2h",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveCompaniesFile_DefaultPath(t *testing.T) {
	c := &CLI{}
	got := c.resolveCompaniesFile()
	if !strings.HasSuffix(got, "companies.yaml") {
		t.Errorf("default path %q should end with companies.yaml", got)
	}
}

func TestResolveCompaniesFile_Override(t *testing.T) {
	want := "/custom/path/companies.yaml"
	c := &CLI{companiesFile: want}
	got := c.resolveCompaniesFile()
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRandomSuffix_LengthAndCharset(t *testing.T) {
	for i := 0; i < 10; i++ {
		s := randomSuffix(6)
		if len(s) != 6 {
			t.Errorf("randomSuffix(6) length = %d, want 6", len(s))
		}
		for _, ch := range s {
			if !((ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')) {
				t.Errorf("unexpected character %q in suffix", ch)
			}
		}
	}
}
