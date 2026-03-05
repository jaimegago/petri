// Package companies provides company-specific application profiles and metadata
// for each of Petri's built-in company configurations (Acme, TechFlow, CloudNative).
// The Registry maps company names to per-app configurations used by generators
// and the orchestrator when building labs.
package companies

// AppProfile holds company-specific configuration for a single application.
type AppProfile struct {
	// Port is the primary container port the application listens on.
	Port int
	// Image is the full container image reference (without tag).
	Image string
	// ImageTag is the default image tag (e.g. "v0.8.0", "latest").
	ImageTag string
	// Language identifies the primary programming language.
	Language string
	// IsFrontend marks apps that should receive an Ingress resource.
	IsFrontend bool
	// FailureScenarios lists Level-3 failure injection scenario tags.
	FailureScenarios []string
}

// CompanyProfile holds company-wide defaults and per-app profiles.
type CompanyProfile struct {
	// Language is the primary language for backend services (go, dotnet, java).
	Language string
	// ImageRegistryPrefix is prepended to app image names when no explicit image is set.
	ImageRegistryPrefix string
	// Apps maps application names to their profiles.
	Apps map[string]AppProfile
}

// Registry maps company names to their CompanyProfile.
type Registry struct {
	companies map[string]CompanyProfile
}

// New returns a Registry pre-populated with all built-in company profiles.
func New() *Registry {
	r := &Registry{
		companies: make(map[string]CompanyProfile),
	}
	r.companies["acme"] = acmeProfile()
	r.companies["techflow"] = techflowProfile()
	r.companies["cloudnative"] = cloudnativeProfile()
	return r
}

// AppProfile returns the configuration for the given company and application.
// If the company or app is not found, sensible defaults are returned.
func (r *Registry) AppProfile(company, app string) AppProfile {
	cp, ok := r.companies[company]
	if !ok {
		return defaultAppProfile(app)
	}
	ap, ok := cp.Apps[app]
	if !ok {
		return defaultAppProfile(app)
	}
	return ap
}

// CompanyProfile returns the profile for the given company.
// If the company is not found, a zero-value profile is returned.
func (r *Registry) CompanyProfile(company string) CompanyProfile {
	return r.companies[company]
}

// HasCompany reports whether the registry contains a profile for the named company.
func (r *Registry) HasCompany(name string) bool {
	_, ok := r.companies[name]
	return ok
}

// defaultAppProfile returns reasonable defaults for unknown apps.
func defaultAppProfile(app string) AppProfile {
	return AppProfile{
		Port:     8080,
		Image:    "ghcr.io/petri-labs/" + app,
		ImageTag: "latest",
		Language: "go",
	}
}
