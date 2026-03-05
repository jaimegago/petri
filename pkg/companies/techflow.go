package companies

// techflowProfile returns the CompanyProfile for TechFlow (Azure / Pulumi / Flux).
// Apps are .NET microservices following an API-gateway pattern.
func techflowProfile() CompanyProfile {
	return CompanyProfile{
		Language:            "dotnet",
		ImageRegistryPrefix: "mcr.microsoft.com/dotnet/samples",
		Apps: map[string]AppProfile{
			"api-gateway": {
				Port:       8080,
				Image:      "mcr.microsoft.com/dotnet/samples",
				ImageTag:   "aspnetapp",
				Language:   "dotnet",
				IsFrontend: true,
			},
			"auth-service": {
				Port:     8081,
				Image:    "mcr.microsoft.com/dotnet/samples",
				ImageTag: "aspnetapp",
				Language: "dotnet",
			},
			"user-service": {
				Port:     8082,
				Image:    "mcr.microsoft.com/dotnet/samples",
				ImageTag: "aspnetapp",
				Language: "dotnet",
			},
			"order-service": {
				Port:     8083,
				Image:    "mcr.microsoft.com/dotnet/samples",
				ImageTag: "aspnetapp",
				Language: "dotnet",
			},
			"product-service": {
				Port:     8084,
				Image:    "mcr.microsoft.com/dotnet/samples",
				ImageTag: "aspnetapp",
				Language: "dotnet",
			},
			"notification-service": {
				Port:     8085,
				Image:    "mcr.microsoft.com/dotnet/samples",
				ImageTag: "aspnetapp",
				Language: "dotnet",
				FailureScenarios: []string{
					"message-queue-backlog",
					"retry-storm",
				},
			},
			"reporting-service": {
				Port:     8086,
				Image:    "mcr.microsoft.com/dotnet/samples",
				ImageTag: "aspnetapp",
				Language: "dotnet",
				FailureScenarios: []string{
					"long-running-query",
					"connection-pool-exhaustion",
				},
			},
			"audit-service": {
				Port:     8087,
				Image:    "mcr.microsoft.com/dotnet/samples",
				ImageTag: "aspnetapp",
				Language: "dotnet",
				FailureScenarios: []string{
					"write-amplification",
					"disk-pressure",
				},
			},
		},
	}
}
