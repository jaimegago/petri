package companies

// cloudnativeProfile returns the CompanyProfile for CloudNative Inc (GCP / Terraform / Anthos).
// Apps are Java Spring Boot services following a microservices architecture.
func cloudnativeProfile() CompanyProfile {
	return CompanyProfile{
		Language:            "java",
		ImageRegistryPrefix: "gcr.io/cloudnative-labs",
		Apps: map[string]AppProfile{
			"spring-frontend": {
				Port:       8080,
				Image:      "gcr.io/google-samples/spring-petclinic",
				ImageTag:   "latest",
				Language:   "java",
				IsFrontend: true,
			},
			"spring-catalog": {
				Port:     8081,
				Image:    "gcr.io/google-samples/spring-petclinic",
				ImageTag: "latest",
				Language: "java",
			},
			"spring-cart": {
				Port:     8082,
				Image:    "gcr.io/google-samples/spring-petclinic",
				ImageTag: "latest",
				Language: "java",
			},
			"spring-orders": {
				Port:     8083,
				Image:    "gcr.io/google-samples/spring-petclinic",
				ImageTag: "latest",
				Language: "java",
			},
			"spring-payments": {
				Port:     8084,
				Image:    "gcr.io/google-samples/spring-petclinic",
				ImageTag: "latest",
				Language: "java",
				FailureScenarios: []string{
					"payment-gateway-timeout",
					"duplicate-charge-detection",
				},
			},
			"spring-shipping": {
				Port:     8085,
				Image:    "gcr.io/google-samples/spring-petclinic",
				ImageTag: "latest",
				Language: "java",
				FailureScenarios: []string{
					"carrier-api-degradation",
					"address-validation-failure",
				},
			},
			"spring-notifications": {
				Port:     8086,
				Image:    "gcr.io/google-samples/spring-petclinic",
				ImageTag: "latest",
				Language: "java",
				FailureScenarios: []string{
					"pubsub-subscription-lag",
					"notification-deduplication-failure",
				},
			},
		},
	}
}
