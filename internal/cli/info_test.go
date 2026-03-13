package cli

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
)

func TestRunInfo_LabNotFound(t *testing.T) {
	mgr := state.NewMockManager()
	c := newTestCLI(mgr, companiesYAML(t))

	err := c.runInfo("no-such-lab")
	if err == nil {
		t.Fatal("expected error for missing lab, got nil")
	}
}

func TestRunInfo_Found_NoResources(t *testing.T) {
	mgr := state.NewMockManager()
	lab := &types.Lab{
		ID:            uuid.New(),
		Name:          "info-lab",
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
	err := c.runInfo("info-lab")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunInfo_Found_WithResources(t *testing.T) {
	mgr := state.NewMockManager()
	lab := &types.Lab{
		ID:            uuid.New(),
		Name:          "resource-lab",
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
	res := &types.LabResource{
		ID:           uuid.New(),
		LabID:        lab.ID,
		ResourceType: "cluster",
		ResourceID:   "petri-resource-lab",
	}
	if err := mgr.CreateResource(nil, res); err != nil { //nolint:staticcheck
		t.Fatalf("fixture resource: %v", err)
	}

	c := newTestCLI(mgr, companiesYAML(t))
	err := c.runInfo("resource-lab")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunInfo_WithClusterMetadata(t *testing.T) {
	mgr := state.NewMockManager()
	lab := &types.Lab{
		ID:            uuid.New(),
		Name:          "cluster-lab",
		Company:       "acme",
		Level:         1,
		CloudProvider: types.CloudProviderLocal,
		Status:        types.LabStatusActive,
		CreatedAt:     time.Now(),
		ExpiresAt:     time.Now().Add(4 * time.Hour),
		TTLHours:      4,
		Metadata: types.LabMetadata{
			Clusters: []types.Cluster{
				{Name: "petri-cluster-lab", NodeCount: 1, KubeconfigPath: "/tmp/kube.yaml"},
			},
		},
	}
	if err := mgr.CreateLab(nil, lab); err != nil { //nolint:staticcheck
		t.Fatalf("fixture: %v", err)
	}

	c := newTestCLI(mgr, companiesYAML(t))
	err := c.runInfo("cluster-lab")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRunInfo_Expired(t *testing.T) {
	mgr := state.NewMockManager()
	lab := &types.Lab{
		ID:            uuid.New(),
		Name:          "expired-info-lab",
		Company:       "acme",
		Level:         1,
		CloudProvider: types.CloudProviderLocal,
		Status:        types.LabStatusActive,
		CreatedAt:     time.Now().Add(-5 * time.Hour),
		ExpiresAt:     time.Now().Add(-1 * time.Hour),
		TTLHours:      4,
	}
	if err := mgr.CreateLab(nil, lab); err != nil { //nolint:staticcheck
		t.Fatalf("fixture: %v", err)
	}

	c := newTestCLI(mgr, companiesYAML(t))
	err := c.runInfo("expired-info-lab")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// ── Helper function unit tests ────────────────────────────────────────────────

func TestLocalKubeconfigPath(t *testing.T) {
	t.Run("non-local provider returns empty", func(t *testing.T) {
		lab := &types.Lab{CloudProvider: types.CloudProvider("aws")}
		if got := localKubeconfigPath(lab); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("local with no clusters returns empty", func(t *testing.T) {
		lab := &types.Lab{CloudProvider: types.CloudProviderLocal}
		if got := localKubeconfigPath(lab); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("local with empty kubeconfig path returns empty", func(t *testing.T) {
		lab := &types.Lab{
			CloudProvider: types.CloudProviderLocal,
			Metadata: types.LabMetadata{
				Clusters: []types.Cluster{{Name: "foo", KubeconfigPath: ""}},
			},
		}
		if got := localKubeconfigPath(lab); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("local with kubeconfig path returns path", func(t *testing.T) {
		want := "/tmp/kube.yaml"
		lab := &types.Lab{
			CloudProvider: types.CloudProviderLocal,
			Metadata: types.LabMetadata{
				Clusters: []types.Cluster{{Name: "foo", KubeconfigPath: want}},
			},
		}
		if got := localKubeconfigPath(lab); got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestAppInfoPort(t *testing.T) {
	tests := []struct {
		company string
		app     string
		want    int
	}{
		{"acme", "boutique-frontend", 8080},
		{"acme", "boutique-cart", 8080},
		{"acme", "boutique-checkout", 8080},
		{"acme", "payment-service-v2", 8080},
		{"techflow", "api-gateway", 8080},
		{"techflow", "auth-service", 8081},
		{"techflow", "audit-service", 8087},
		{"cloudnative", "spring-frontend", 8080},
		{"cloudnative", "spring-payments", 8084},
		{"cloudnative", "spring-notifications", 8086},
		{"unknown", "unknown-app", 8080}, // fallback
	}

	for _, tc := range tests {
		t.Run(tc.company+"/"+tc.app, func(t *testing.T) {
			got := appInfoPort(tc.company, tc.app)
			if got != tc.want {
				t.Errorf("appInfoPort(%q, %q) = %d, want %d", tc.company, tc.app, got, tc.want)
			}
		})
	}
}

func TestIsFrontendByName(t *testing.T) {
	tests := []struct {
		app  string
		want bool
	}{
		{"boutique-frontend", true},
		{"online-boutique-full", true},
		{"api-gateway", true},
		{"spring-frontend", true},
		{"frontend", true},
		{"BOUTIQUE-FRONTEND", true}, // case-insensitive
		{"boutique-cart", false},
		{"auth-service", false},
		{"", false},
	}

	for _, tc := range tests {
		t.Run(tc.app, func(t *testing.T) {
			got := isFrontendByName(tc.app)
			if got != tc.want {
				t.Errorf("isFrontendByName(%q) = %v, want %v", tc.app, got, tc.want)
			}
		})
	}
}

func TestAppInfoNamespace(t *testing.T) {
	t.Run("returns first namespace", func(t *testing.T) {
		spec := &types.LevelSpec{Namespaces: []string{"apps", "default"}}
		if got := appInfoNamespace(spec); got != "apps" {
			t.Errorf("got %q, want %q", got, "apps")
		}
	})

	t.Run("falls back to default when no namespaces", func(t *testing.T) {
		spec := &types.LevelSpec{}
		if got := appInfoNamespace(spec); got != "default" {
			t.Errorf("got %q, want %q", got, "default")
		}
	})
}
