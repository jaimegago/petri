# Companies & Complexity Levels

## Company Profiles

Companies are defined in `configs/companies.yaml`. Each profile maps to a specific cloud provider, IaC tool, GitOps tool, and application set.

### Acme Corp

- **Cloud**: AWS (EKS)
- **IaC**: Terraform
- **GitOps**: ArgoCD
- **Apps**: Go microservices (Google Online Boutique base)
- **Pattern**: Cloud-native startup

### TechFlow

- **Cloud**: Azure (AKS)
- **IaC**: Pulumi
- **GitOps**: Flux
- **Apps**: .NET microservices
- **Pattern**: Enterprise Azure shop

### CloudNative Inc

- **Cloud**: GCP (GKE)
- **IaC**: Terraform
- **GitOps**: Anthos Config Management
- **Apps**: Java Spring Boot
- **Pattern**: GCP-native organization

## Complexity Levels

### Level 1 — Validation

- 1 cluster, 1 node (local) or small cloud nodes
- 1-3 microservices
- Basic platform + observability
- Single namespace
- **Use case**: Quick iteration, basic functionality testing

### Level 2 — Integration

- 2 clusters (prod, staging)
- ~11 microservices
- Extended platform (Vault, External-DNS)
- Multi-tenancy (3 namespaces)
- Enhanced observability (logs, traces)
- **Use case**: Multi-cluster scenarios, team isolation testing

### Level 3 — Production-Realistic

- 3 clusters (prod, staging, management)
- ~15 microservices (includes custom failure-prone services)
- Complete platform (Crossplane, Istio, Velero, policies)
- Full observability stack
- Multi-tenancy (5 namespaces)
- **Use case**: Complex failure scenarios, stress testing
