package companies

// acmeProfile returns the CompanyProfile for Acme Corp (AWS / Terraform / ArgoCD).
// Apps follow the Google Online Boutique microservices pattern, written in Go.
func acmeProfile() CompanyProfile {
	return CompanyProfile{
		Language:            "go",
		ImageRegistryPrefix: "gcr.io/google-samples/microservices-demo",
		Apps: map[string]AppProfile{
			"boutique-frontend": {
				Port:       8080,
				Image:      "gcr.io/google-samples/microservices-demo/frontend",
				ImageTag:   "v0.8.0",
				Language:   "go",
				IsFrontend: true,
			},
			"boutique-cart": {
				Port:     7070,
				Image:    "gcr.io/google-samples/microservices-demo/cartservice",
				ImageTag: "v0.8.0",
				Language: "csharp",
			},
			"boutique-checkout": {
				Port:     5050,
				Image:    "gcr.io/google-samples/microservices-demo/checkoutservice",
				ImageTag: "v0.8.0",
				Language: "go",
			},
			"online-boutique-full": {
				Port:       8080,
				Image:      "gcr.io/google-samples/microservices-demo/frontend",
				ImageTag:   "v0.8.0",
				Language:   "go",
				IsFrontend: true,
			},
			"payment-service-v2": {
				Port:     50051,
				Image:    "gcr.io/google-samples/microservices-demo/paymentservice",
				ImageTag: "v0.8.0",
				Language: "nodejs",
				FailureScenarios: []string{
					"slow-payment-processing",
					"intermittent-timeout",
					"high-latency-spike",
				},
			},
			"inventory-service": {
				Port:     8080,
				Image:    "gcr.io/google-samples/microservices-demo/productcatalogservice",
				ImageTag: "v0.8.0",
				Language: "go",
				FailureScenarios: []string{
					"catalog-cache-miss-storm",
					"database-connection-leak",
				},
			},
			"notification-service": {
				Port:     8080,
				Image:    "gcr.io/google-samples/microservices-demo/emailservice",
				ImageTag: "v0.8.0",
				Language: "python",
				FailureScenarios: []string{
					"smtp-backpressure",
					"queue-saturation",
				},
			},
		},
	}
}
