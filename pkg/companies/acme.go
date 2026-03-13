package companies

// acmeProfile returns the CompanyProfile for Acme Corp (AWS / Terraform / ArgoCD).
// Apps follow a microservices pattern themed around an online boutique.
// Images use gcr.io/google-samples/hello-app:2.0 — a standalone Go HTTP server
// that responds to any request on port 8080 without inter-service dependencies.
// This makes local kind labs immediately functional for infrastructure testing.
func acmeProfile() CompanyProfile {
	return CompanyProfile{
		Language:            "go",
		ImageRegistryPrefix: "gcr.io/google-samples",
		Apps: map[string]AppProfile{
			"boutique-frontend": {
				Port:       8080,
				Image:      "gcr.io/google-samples/hello-app",
				ImageTag:   "2.0",
				Language:   "go",
				IsFrontend: true,
			},
			"boutique-cart": {
				Port:     8080,
				Image:    "gcr.io/google-samples/hello-app",
				ImageTag: "2.0",
				Language: "go",
			},
			"boutique-checkout": {
				Port:     8080,
				Image:    "gcr.io/google-samples/hello-app",
				ImageTag: "2.0",
				Language: "go",
			},
			"online-boutique-full": {
				Port:       8080,
				Image:      "gcr.io/google-samples/hello-app",
				ImageTag:   "2.0",
				Language:   "go",
				IsFrontend: true,
			},
			"payment-service-v2": {
				Port:     8080,
				Image:    "gcr.io/google-samples/hello-app",
				ImageTag: "2.0",
				Language: "go",
				FailureScenarios: []string{
					"slow-payment-processing",
					"intermittent-timeout",
					"high-latency-spike",
				},
			},
			"inventory-service": {
				Port:     8080,
				Image:    "gcr.io/google-samples/hello-app",
				ImageTag: "2.0",
				Language: "go",
				FailureScenarios: []string{
					"catalog-cache-miss-storm",
					"database-connection-leak",
				},
			},
			"notification-service": {
				Port:     8080,
				Image:    "gcr.io/google-samples/hello-app",
				ImageTag: "2.0",
				Language: "go",
				FailureScenarios: []string{
					"smtp-backpressure",
					"queue-saturation",
				},
			},
		},
	}
}
