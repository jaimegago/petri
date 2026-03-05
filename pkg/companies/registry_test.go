package companies_test

import (
	"testing"

	"github.com/jaimegago/petri/pkg/companies"
)

func TestRegistry_HasCompany(t *testing.T) {
	r := companies.New()
	tests := []struct {
		name    string
		company string
		want    bool
	}{
		{"acme exists", "acme", true},
		{"techflow exists", "techflow", true},
		{"cloudnative exists", "cloudnative", true},
		{"unknown returns false", "unknown-corp", false},
		{"empty string returns false", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := r.HasCompany(tt.company); got != tt.want {
				t.Errorf("HasCompany(%q) = %v, want %v", tt.company, got, tt.want)
			}
		})
	}
}

func TestRegistry_AppProfile_KnownApps(t *testing.T) {
	r := companies.New()
	tests := []struct {
		name     string
		company  string
		app      string
		wantPort int
		wantFE   bool
	}{
		{"acme boutique-frontend", "acme", "boutique-frontend", 8080, true},
		{"acme boutique-cart", "acme", "boutique-cart", 7070, false},
		{"acme boutique-checkout", "acme", "boutique-checkout", 5050, false},
		{"acme payment-service-v2", "acme", "payment-service-v2", 50051, false},
		{"acme inventory-service", "acme", "inventory-service", 8080, false},
		{"acme notification-service", "acme", "notification-service", 8080, false},
		{"techflow api-gateway", "techflow", "api-gateway", 8080, true},
		{"techflow auth-service", "techflow", "auth-service", 8081, false},
		{"techflow reporting-service", "techflow", "reporting-service", 8086, false},
		{"techflow audit-service", "techflow", "audit-service", 8087, false},
		{"cloudnative spring-frontend", "cloudnative", "spring-frontend", 8080, true},
		{"cloudnative spring-payments", "cloudnative", "spring-payments", 8084, false},
		{"cloudnative spring-shipping", "cloudnative", "spring-shipping", 8085, false},
		{"cloudnative spring-notifications", "cloudnative", "spring-notifications", 8086, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.AppProfile(tt.company, tt.app)
			if got.Port != tt.wantPort {
				t.Errorf("AppProfile(%q, %q).Port = %d, want %d", tt.company, tt.app, got.Port, tt.wantPort)
			}
			if got.IsFrontend != tt.wantFE {
				t.Errorf("AppProfile(%q, %q).IsFrontend = %v, want %v", tt.company, tt.app, got.IsFrontend, tt.wantFE)
			}
		})
	}
}

func TestRegistry_AppProfile_Defaults(t *testing.T) {
	r := companies.New()

	t.Run("unknown company falls back to defaults", func(t *testing.T) {
		got := r.AppProfile("unknown-corp", "my-service")
		if got.Port != 8080 {
			t.Errorf("expected default port 8080, got %d", got.Port)
		}
		if got.Image == "" {
			t.Error("expected non-empty default image")
		}
	})

	t.Run("unknown app within known company falls back to defaults", func(t *testing.T) {
		got := r.AppProfile("acme", "some-new-service")
		if got.Port != 8080 {
			t.Errorf("expected default port 8080, got %d", got.Port)
		}
	})
}

func TestRegistry_AppProfile_Images(t *testing.T) {
	r := companies.New()
	tests := []struct {
		company string
		app     string
	}{
		{"acme", "boutique-frontend"},
		{"techflow", "api-gateway"},
		{"cloudnative", "spring-frontend"},
	}
	for _, tt := range tests {
		t.Run(tt.company+"/"+tt.app, func(t *testing.T) {
			got := r.AppProfile(tt.company, tt.app)
			if got.Image == "" {
				t.Error("expected non-empty Image")
			}
			if got.ImageTag == "" {
				t.Error("expected non-empty ImageTag")
			}
		})
	}
}

func TestRegistry_FailureScenarios_LevelThree(t *testing.T) {
	r := companies.New()
	tests := []struct {
		company string
		app     string
	}{
		{"acme", "payment-service-v2"},
		{"acme", "inventory-service"},
		{"techflow", "reporting-service"},
		{"cloudnative", "spring-payments"},
	}
	for _, tt := range tests {
		t.Run(tt.company+"/"+tt.app, func(t *testing.T) {
			got := r.AppProfile(tt.company, tt.app)
			if len(got.FailureScenarios) == 0 {
				t.Errorf("expected failure scenarios for L3 app %s/%s", tt.company, tt.app)
			}
		})
	}
}

func TestRegistry_CompanyProfile(t *testing.T) {
	r := companies.New()
	tests := []struct {
		company      string
		wantLanguage string
	}{
		{"acme", "go"},
		{"techflow", "dotnet"},
		{"cloudnative", "java"},
	}
	for _, tt := range tests {
		t.Run(tt.company, func(t *testing.T) {
			got := r.CompanyProfile(tt.company)
			if got.Language != tt.wantLanguage {
				t.Errorf("CompanyProfile(%q).Language = %q, want %q", tt.company, got.Language, tt.wantLanguage)
			}
			if len(got.Apps) == 0 {
				t.Errorf("CompanyProfile(%q).Apps is empty", tt.company)
			}
		})
	}
}
