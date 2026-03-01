package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCompanies(t *testing.T) {
	// Use the real companies file relative to project root.
	// Adjust path based on where tests run from (pkg/config/).
	path := filepath.Join("..", "..", "configs", "companies.yaml")

	companies, err := LoadCompanies(path)
	if err != nil {
		t.Fatalf("LoadCompanies() error: %v", err)
	}

	if len(companies) == 0 {
		t.Fatal("expected at least one company")
	}

	companyNames := map[string]bool{}
	for _, c := range companies {
		companyNames[c.Name] = true
		if len(c.Levels) == 0 {
			t.Errorf("company %q has no levels", c.Name)
		}
	}

	for _, expected := range []string{"acme", "techflow", "cloudnative"} {
		if !companyNames[expected] {
			t.Errorf("expected company %q not found", expected)
		}
	}
}

func TestLoadCompanies_NotFound(t *testing.T) {
	_, err := LoadCompanies("/nonexistent/path/companies.yaml")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.State.Backend == "" {
		t.Error("expected default state backend")
	}
	if cfg.Observability.MetricsPort == 0 {
		t.Error("expected default metrics port")
	}
	if cfg.Cleanup.CheckInterval == 0 {
		t.Error("expected default cleanup interval")
	}
}

func TestPetriDir(t *testing.T) {
	dir, err := PetriDir()
	if err != nil {
		t.Fatalf("PetriDir() error: %v", err)
	}

	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".petri")
	if dir != expected {
		t.Errorf("PetriDir() = %q, want %q", dir, expected)
	}
}
