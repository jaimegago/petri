package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestOASISDefaultImage(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		envValue  string
		wantImage string
	}{
		{
			name:      "no oasis section uses default",
			yaml:      "state:\n  backend: sqlite\n",
			wantImage: DefaultOASISImage,
		},
		{
			name:      "empty default_image falls back to default",
			yaml:      "oasis:\n  default_image: \"\"\n",
			wantImage: DefaultOASISImage,
		},
		{
			name:      "explicit default_image is honored",
			yaml:      "oasis:\n  default_image: registry.k8s.io/e2e-test-images/agnhost:2.45\n",
			wantImage: "registry.k8s.io/e2e-test-images/agnhost:2.45",
		},
		{
			name:      "PETRI_OASIS_DEFAULT_IMAGE overrides yaml",
			yaml:      "oasis:\n  default_image: registry.k8s.io/nginx-slim:0.27\n",
			envValue:  "registry.k8s.io/e2e-test-images/agnhost:2.45",
			wantImage: "registry.k8s.io/e2e-test-images/agnhost:2.45",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(tt.yaml), 0o600); err != nil {
				t.Fatalf("writing config: %v", err)
			}
			if tt.envValue != "" {
				t.Setenv("PETRI_OASIS_DEFAULT_IMAGE", tt.envValue)
			} else {
				t.Setenv("PETRI_OASIS_DEFAULT_IMAGE", "")
			}

			cfg, err := Load(path)
			if err != nil {
				t.Fatalf("Load() error: %v", err)
			}
			if cfg.OASIS.DefaultImage != tt.wantImage {
				t.Errorf("OASIS.DefaultImage = %q, want %q", cfg.OASIS.DefaultImage, tt.wantImage)
			}
		})
	}
}

func TestDefaultOASISImageNotDockerHub(t *testing.T) {
	// The whole point of this config field is to avoid Docker Hub —
	// guard against an accidental regression to docker.io defaults.
	if !strings.HasPrefix(DefaultOASISImage, "registry.k8s.io/") {
		t.Errorf("DefaultOASISImage = %q must be hosted on registry.k8s.io to avoid Docker Hub R2 dependency", DefaultOASISImage)
	}
	if strings.HasSuffix(DefaultOASISImage, ":latest") {
		t.Errorf("DefaultOASISImage = %q must be pinned to a specific tag, not :latest", DefaultOASISImage)
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

// TestImagePullTimeoutDefaults pins that the image-pull budget is reachable as
// configuration and falls back to a sane default rather than to zero. A zero
// here would mean "no budget at all", which the provider treats as "use the
// default" — but only because it guards for it, and this test is what keeps
// that guard honest from the config side.
func TestImagePullTimeoutDefaults(t *testing.T) {
	if DefaultImagePullTimeout <= 0 {
		t.Fatalf("DefaultImagePullTimeout = %s, must be positive", DefaultImagePullTimeout)
	}
	// The whole point of the split is that the pull budget is not the rollout
	// budget. 60s is the rollout budget; a pull budget at or below it would
	// reintroduce the cold-start failure this was written to remove.
	if DefaultImagePullTimeout <= 60*time.Second {
		t.Errorf("DefaultImagePullTimeout = %s, must exceed the 60s rollout budget", DefaultImagePullTimeout)
	}
	if got := DefaultConfig().OASIS.ImagePullTimeout; got != DefaultImagePullTimeout {
		t.Errorf("DefaultConfig OASIS.ImagePullTimeout = %s, want %s", got, DefaultImagePullTimeout)
	}
}
