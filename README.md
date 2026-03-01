# Petri

Infrastructure lab framework for testing Joe, an LLM-based infrastructure copilot.

Petri spawns complete, realistic company infrastructures including Kubernetes clusters, applications, IaC repositories, and observability stacks. Each lab mimics a real production environment for testing Joe's diagnostic and operational capabilities.

## Features

- **Multi-Cloud**: AWS (EKS), Azure (AKS), GCP (GKE), and local (kind/k3s)
- **Company Profiles**: Different organizational patterns (cloud-native startup, enterprise, etc)
- **Complexity Levels**: Progressive complexity from basic (1 cluster) to production-realistic (3+ clusters)
- **Realistic IaC**: Generated Terraform/Pulumi code with authentic git history
- **Full Platform**: ArgoCD/Flux, Vault, Istio, observability stacks
- **Ephemeral**: Automatic cleanup with TTL-based expiration
- **Secure**: Encrypted credential storage, clean teardown

## Quick Start

### Prerequisites

- Go 1.21+
- Docker (for local labs)
- PostgreSQL (or use SQLite for development)
- Git
- Cloud provider CLIs (aws-cli, az, gcloud) for respective labs
- Terraform 1.7+ (Petri will manage this but useful for debugging)

### Installation

```bash
# Clone repository
git clone https://github.com/yourusername/petri.git
cd petri

# Build
go build -o petri cmd/petri/main.go

# Move to PATH
sudo mv petri /usr/local/bin/

# Initialize Petri
petri init
```

The `init` command will:
1. Create `~/.petri/` directory
2. Generate master encryption key
3. Create PostgreSQL schema (or SQLite database)
4. Verify dependencies

### First Lab - Local

```bash
# Create a basic local lab
petri create --company=acme --level=1 --local --name=my-first-lab

# This prompts for:
# - GitHub personal access token (for creating repos)
# 
# Then creates:
# - 1 kind cluster with 3 nodes
# - 3 microservices (boutique frontend, cart, checkout)
# - ArgoCD, cert-manager, ingress-nginx
# - Prometheus + Grafana
# - 3 git repos with realistic history

# Get lab details
petri info my-first-lab

# Outputs kubeconfigs, git URLs, Grafana URL, etc
```

### Cloud Lab - AWS

```bash
# Create AWS lab
petri create --company=acme --level=2 --name=aws-test --ttl=4h

# Prompts for:
# - GitHub personal access token
# - AWS Access Key ID
# - AWS Secret Access Key
#
# Creates:
# - 2 EKS clusters (prod: 4 nodes, staging: 2 nodes)
# - 11 microservices (Google Online Boutique)
# - Full platform stack (ArgoCD, Vault, External-DNS, cert-manager)
# - RDS PostgreSQL, ElastiCache Redis
# - Enhanced observability (Prometheus, Grafana, Loki, OTel)
# - 3 git repos with multi-author history
#
# Automatically destroys after 4 hours
```

## Companies

### Acme Corp
- **Cloud**: AWS
- **IaC**: Terraform
- **GitOps**: ArgoCD
- **Apps**: Go microservices (Google Online Boutique base)
- **Pattern**: Cloud-native startup

### TechFlow
- **Cloud**: Azure
- **IaC**: Pulumi
- **GitOps**: Flux
- **Apps**: .NET microservices
- **Pattern**: Enterprise Azure shop

### CloudNative Inc
- **Cloud**: GCP
- **IaC**: Terraform
- **GitOps**: Anthos Config Management
- **Apps**: Java Spring Boot
- **Pattern**: GCP-native organization

## Complexity Levels

### Level 1 - Validation
- 1 cluster, 3 nodes
- 1-3 microservices
- Basic platform + observability
- Single namespace
- **Bootstrap**: <5m local, <10m cloud
- **Use Case**: Quick Joe iteration, basic functionality testing

### Level 2 - Integration
- 2 clusters (prod, staging)
- ~11 microservices
- Extended platform (Vault, External-DNS)
- Multi-tenancy (3 namespaces)
- Enhanced observability (logs, traces)
- **Bootstrap**: 8m local, 12m cloud
- **Use Case**: Multi-cluster scenarios, team isolation testing

### Level 3 - Production-Realistic
- 3 clusters (prod, staging, management)
- ~15 microservices (includes custom failure-prone services)
- Complete platform (Crossplane, Istio, Velero, policies)
- Full observability stack
- Multi-tenancy (5 namespaces)
- **Bootstrap**: 10m local, 15m cloud
- **Use Case**: Complex failure scenarios, Joe stress testing

## Commands

### Create Lab

```bash
petri create --company=<company> --level=<1|2|3> [options]

Options:
  --name=<name>         Custom lab name (default: auto-generated)
  --local               Use local kind/k3s instead of cloud
  --ttl=<duration>      Time-to-live (e.g., 4h, 30m, default: level-specific)
  --no-apps             Skip application deployment (platform only)
  --dry-run             Show what would be created
```

### List Labs

```bash
petri list [options]

Options:
  --company=<company>   Filter by company
  --level=<level>       Filter by level
  --expired             Show expired labs
  --format=<table|json> Output format
```

### Lab Info

```bash
petri info <lab-name>

# Displays:
# - Lab status and metadata
# - Kubeconfig paths
# - Git repository URLs
# - Observability dashboard URLs
# - Cloud resource IDs
# - Expiration time
```

### Extend TTL

```bash
petri extend <lab-name> --ttl=+2h

# Extends lab lifetime by 2 hours
```

### Export Credentials for Joe

```bash
petri export-creds <lab-name> --output=joe-bundle.enc

# Creates encrypted bundle with:
# - Kubeconfigs for all clusters
# - Git repository access tokens
# - Cloud provider read-only credentials
# - Observability URLs and tokens
#
# Joe can decrypt with same master key
```

### Destroy Lab

```bash
petri destroy <lab-name> [--force]

# Cleanly destroys:
# - All cloud resources
# - Git repositories
# - Stored credentials
# - State records
```

### Cleanup Expired Labs

```bash
petri cleanup --expired

# Automatically destroys labs past their TTL
# Run as cron job for automatic cleanup
```

### Health Check

```bash
petri health

# Checks:
# - PostgreSQL connectivity
# - Cloud provider credentials
# - Git provider access
# - Docker daemon (for local labs)
# - Required binaries (terraform, kubectl, etc)
```

## Configuration

### ~/.petri/config.yaml

```yaml
state:
  backend: postgresql  # or sqlite
  connection_string: "postgres://localhost/petri?sslmode=disable"

credentials:
  master_key_path: ~/.petri/master.key

observability:
  metrics_enabled: true
  metrics_port: 9090
  tracing_enabled: false
  log_level: info

git:
  default_provider: github

cloud:
  terraform_version: 1.7.0
  pulumi_version: 3.100.0

cleanup:
  check_interval: 5m    # How often to check for expired labs
  grace_period: 30m     # Grace period before destroying expired labs
```

### Company Customization

Companies are defined in `configs/companies.yaml`. You can add custom companies or modify existing ones.

See `petri-architecture.md` for company configuration schema.

## Credentials

Petri never stores credentials in plaintext:

1. Credentials prompted at runtime (hidden input)
2. Encrypted with AES-256-GCM
3. Encryption key derived from master passphrase in `~/.petri/master.key`
4. Stored encrypted in PostgreSQL
5. Used only during provisioning
6. Never logged or exposed
7. Deleted on lab destruction

**Protect your master key**: `~/.petri/master.key` is the only key to decrypt your stored credentials. Back it up securely.

## Cloud Costs

Approximate hourly costs per complexity level:

**AWS (EKS):**
- Level 1: ~$0.15/hr (1 cluster, t3.medium nodes, micro RDS)
- Level 2: ~$0.35/hr (2 clusters, mixed nodes, small RDS, Redis)
- Level 3: ~$0.60/hr (3 clusters, larger nodes, enhanced services)

**Azure (AKS):**
- Level 1: ~$0.12/hr
- Level 2: ~$0.30/hr
- Level 3: ~$0.55/hr

**GCP (GKE):**
- Level 1: ~$0.10/hr
- Level 2: ~$0.28/hr
- Level 3: ~$0.50/hr

**Local:**
- $0 (uses your machine's resources)

These are estimates. Actual costs vary by region and usage patterns.

## Observability

### Petri Metrics

Petri exposes Prometheus metrics on `localhost:9090` (configurable):

- `petri_labs_total` - Total labs created
- `petri_labs_active` - Currently active labs
- `petri_lab_creation_duration_seconds` - Lab creation time
- `petri_lab_creation_failures_total` - Creation failures
- `petri_git_operations_total` - Git operations count
- `petri_iac_execution_duration_seconds` - IaC execution time

### Petri Logs

Structured JSON logs (stdout):

```json
{
  "level": "info",
  "time": "2024-03-01T10:30:45Z",
  "lab_id": "abc123",
  "company": "acme",
  "level": 2,
  "phase": "terraform_apply",
  "duration_ms": 45231,
  "msg": "EKS cluster created"
}
```

Log level configurable via `~/.petri/config.yaml` or `PETRI_LOG_LEVEL` env var.

## Troubleshooting

### Lab creation fails at Terraform apply

```bash
# Get detailed logs
PETRI_LOG_LEVEL=debug petri create --company=acme --level=2

# Check Terraform state
# Petri stores state in S3/GCS/Azure Storage
# State location in lab metadata: petri info <lab-name>

# Manual Terraform inspection
cd ~/.petri/workdir/<lab-id>/infra
terraform state list
terraform state show <resource>
```

### Lab won't destroy cleanly

```bash
# Force destroy (skips some cleanup checks)
petri destroy <lab-name> --force

# Manual cleanup if force fails:
# 1. Get resource IDs: petri info <lab-name>
# 2. Delete via cloud console/CLI
# 3. Delete git repos manually
# 4. Clean Petri state: psql -c "DELETE FROM labs WHERE name='<lab-name>'"
```

### Expired labs not cleaning up

```bash
# Check cleanup service
petri health

# Manual cleanup
petri cleanup --expired

# Set up cron job
crontab -e
# Add: */10 * * * * /usr/local/bin/petri cleanup --expired
```

### Can't connect to created cluster

```bash
# Get kubeconfig
petri info <lab-name>

# Copy kubeconfig to default location
export KUBECONFIG=~/.petri/labs/<lab-name>/kubeconfig-prod

# Verify
kubectl cluster-info
kubectl get nodes
```

## Development

### Running from Source

```bash
git clone https://github.com/yourusername/petri.git
cd petri
go run cmd/petri/main.go create --company=acme --level=1 --local
```

### Running Tests

```bash
# Unit tests
go test ./...

# Integration tests (requires Docker)
go test ./... -tags=integration

# E2E tests (creates real labs)
go test ./... -tags=e2e
```

### Adding a Company

1. Create company config in `configs/companies/<company-name>.yaml`
2. Add templates in `templates/terraform/<company-name>/` (or pulumi/)
3. Add templates in `templates/gitops/<company-name>/`
4. Implement company-specific logic in `pkg/companies/<company-name>/`
5. Register in `pkg/companies/registry.go`

See `petri-architecture.md` for detailed company configuration schema.

## Contributing

See `CONTRIBUTING.md` for guidelines.

## License

Apache 2.0

## Support

- Issues: https://github.com/yourusername/petri/issues
- Discussions: https://github.com/yourusername/petri/discussions
- Docs: https://github.com/yourusername/petri/wiki
