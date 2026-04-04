# Petri Architecture

## Overview

Petri is a framework that spawns complete, realistic infrastructure labs for testing Joe (an LLM-based infrastructure copilot). Each lab mimics a real company's infrastructure including cloud resources, Kubernetes clusters, applications, IaC repositories, and git history.

**Name Origin:** Like a Petri dish cultures bacteria in controlled conditions, Petri grows complete infrastructure environments in isolation for observation and testing.

## Design Principles

1. **Realism**: Labs mirror actual production environments - multi-cluster, multi-tenant, realistic git history
2. **Isolation**: Each lab is completely independent, ephemeral, and cleanly destroyable
3. **Composability**: Company × Level matrix allows mix-and-match testing scenarios
4. **Observability**: Petri instruments itself and the labs it creates
5. **Security**: Credentials encrypted at rest, never logged, scoped per lab

## Core Concepts

### Company Profiles

Each company represents a different organizational pattern:

- **Acme Corp**: AWS, Terraform, ArgoCD, Go microservices, cloud-native startup
- **TechFlow**: Azure, Pulumi, Flux, .NET stack, enterprise patterns
- **CloudNative Inc**: GCP, Terraform, Anthos Config Mgmt, Java Spring Boot

Companies have different:
- Cloud providers
- IaC tools (Terraform, Pulumi, CloudFormation)
- GitOps tools (ArgoCD, Flux, Anthos)
- Application stacks
- Operational patterns

### Complexity Levels

**Level 1 - Validation**
- Single K8s cluster (1 node)
- 1-3 microservices
- Minimal observability (Prometheus + Grafana)
- Basic platform (ArgoCD, cert-manager, ingress)
- Target: <5m local, <10m cloud

**Level 2 - Integration**
- 2 clusters (prod 3 nodes, staging 2 nodes)
- ~11 microservices (Google Online Boutique)
- Enhanced observability (Prometheus, Grafana, Loki, OTel)
- Extended platform (+ Vault, External-DNS)
- Multi-tenancy (3 namespaces)
- Target: 8m local, 12m cloud

**Level 3 - Production-Realistic**
- 3 clusters (prod 4 nodes, staging 2 nodes, management 2 nodes)
- ~15 microservices (Boutique + custom failure-prone services)
- Full observability (Prometheus, Grafana, Loki, Tempo, Jaeger)
- Complete platform (+ Crossplane, Istio, Velero, Kyverno)
- Multi-tenancy (5 namespaces)
- Service mesh, policy enforcement
- Target: 10m local, 15m cloud

### Lab Types

- `local`: kind/k3s clusters on developer machine
- `aws`: EKS clusters with managed services
- `azure`: AKS clusters with Azure integrations
- `gcp`: GKE clusters with GCP services

## Architecture Layers

### Layer 0: Petri Framework (Persistent)

The framework itself - NOT part of labs, orchestrates everything.

```
petri/
├── cmd/petri/              # CLI entry point
├── pkg/
│   ├── orchestrator/       # Lab lifecycle management
│   ├── provisioners/       # Cloud/git/k8s operations
│   ├── generators/         # Template rendering
│   ├── companies/          # Company-specific logic
│   ├── state/              # State management (SQLite/PostgreSQL)
│   └── crypto/             # Credential encryption
└── templates/              # Embedded Go templates
```

### Layer 1: Generated Artifacts (Per Lab)

Created by Petri for each lab instance:

**Git Repositories** (3 per lab):
- `{company}-lab-{id}-infra`: IaC code (Terraform/Pulumi)
- `{company}-lab-{id}-gitops`: ArgoCD/Flux manifests
- `{company}-lab-{id}-apps`: Application code/configs

**Infrastructure Resources**:
- Cloud provider resources (VPCs, clusters, managed services)
- Kubernetes clusters
- Platform components
- Applications

**Realistic Git History**:
- Multiple authors with company email addresses
- Commit messages following company patterns
- Timestamps spanning weeks/months (backdated)
- Realistic evolution: initial setup → fixes → features → incident responses

### Layer 2: Lab Runtime (Living System)

Once created, labs run independently:
- K8s clusters serve applications
- ArgoCD/Flux sync from git repos
- Observability collects metrics/logs/traces
- Platform components manage infrastructure
- Joe can interact with all components

## Component Details

### Orchestrator

**Responsibilities:**
- Parse user commands
- Validate prerequisites
- Coordinate provisioners
- Track lab lifecycle
- Manage TTLs
- Handle cleanup

**State Machine:**
```
CREATING → ACTIVE → EXPIRING → DESTROYING → DESTROYED
                  ↓
                ERROR
```

### Provisioners

**Git Provisioner:**
- Creates ephemeral repos (GitHub API)
- Generates realistic commit history
- Sets up branch protection, CI/CD configs
- Deletes repos on lab destroy

**Terraform Provisioner:**
- Renders Terraform templates
- Executes terraform init/plan/apply
- Manages remote state (S3/GCS/Azure Storage)
- Captures outputs for other components

**Pulumi Provisioner:**
- Renders Pulumi programs
- Executes pulumi up
- Manages Pulumi state
- Similar to Terraform provisioner

**Kubectl Provisioner:**
- Generates kubeconfigs
- Waits for cluster readiness
- Applies initial manifests
- Validates deployments

**Local Provisioner:**
- Manages kind/k3s clusters
- Uses Docker/containerd
- Simulates multi-cluster locally

### Generators

**IaC Generator:**
- Renders Terraform/Pulumi templates
- Company-specific modules
- Level-appropriate complexity
- Realistic variable patterns

**GitOps Generator:**
- Creates ArgoCD Application manifests
- Flux Kustomization/HelmRelease
- App-of-apps patterns
- Sync waves for dependencies

**Apps Generator:**
- Application manifests (Deployments, Services, Ingress)
- ConfigMaps, Secrets
- HPA, PDB, NetworkPolicies
- Custom failure-prone apps (Level 3)

**Commits Generator:**
- Realistic author personas
- Timestamped commit history
- Varied commit messages
- Evolution patterns (setup → stabilization → features → incidents)

### State Management

**Database Schema (SQLite default, PostgreSQL supported):**

```sql
-- Lab instances
labs(
    id UUID PRIMARY KEY,
    name VARCHAR UNIQUE,
    company VARCHAR,
    level INT,
    cloud_provider VARCHAR,
    status VARCHAR,
    created_at TIMESTAMP,
    ttl_hours INT,
    expires_at TIMESTAMP,
    metadata JSONB
)

-- Lab resources (clusters, repos, etc)
lab_resources(
    id UUID PRIMARY KEY,
    lab_id UUID REFERENCES labs,
    resource_type VARCHAR,
    resource_id VARCHAR,
    cloud_resource_id VARCHAR,
    metadata JSONB
)

-- Encrypted credentials
lab_credentials(
    id UUID PRIMARY KEY,
    lab_id UUID REFERENCES labs,
    credential_type VARCHAR,
    encrypted_value TEXT,
    created_at TIMESTAMP
)
```

**State Operations:**
- Create lab record before provisioning
- Update status through lifecycle
- Track all cloud resources for cleanup
- Store encrypted credentials
- Query active/expired labs

### Credential Management

**Flow:**
1. User runs `petri create`
2. Petri prompts for required credentials (GitHub token, AWS keys, etc)
3. User enters credentials interactively (hidden input)
4. Petri encrypts with AES-256-GCM
5. Encryption key derived from master passphrase in `~/.petri/master.key`
6. Encrypted credentials stored in PostgreSQL
7. Used during provisioning, never logged
8. Deleted on lab destroy

**Export for Joe:**
```bash
petri export-creds lab-name --output=joe-bundle.enc
```
Creates encrypted bundle with:
- Kubeconfigs for all clusters
- Git repository tokens
- Cloud provider read-only credentials
- Observability dashboard URLs/tokens

### Template System

**Go Templates with Custom Functions:**

```go
// Example: VPC template with level-aware features
resource "aws_vpc" "main" {
  cidr_block = "{{ .VPC.CIDR }}"
  
  {{- if ge .Level 2 }}
  enable_dns_hostnames = true
  enable_flow_logs     = true
  {{- end }}
  
  tags = {
    Company = "{{ .Company }}"
    Lab     = "{{ .LabID }}"
    Level   = "{{ .Level }}"
  }
}
```

**Template Categories:**
- Infrastructure (VPC, subnets, clusters, managed services)
- Platform (ArgoCD, Vault, cert-manager configs)
- Applications (Deployments, Services, Ingress)
- GitOps (Application manifests, Kustomizations)
- CI/CD (GitHub Actions, GitLab CI configs)

### Observability

**Petri Self-Instrumentation:**

Prometheus Metrics:
- `petri_labs_created_total{company, level, provider}` — total labs created
- `petri_labs_destroyed_total{company, level, provider, reason}` — total labs destroyed
- `petri_labs_active` — current number of active labs
- `petri_lab_create_duration_seconds{company, level, provider}` — lab creation duration
- `petri_lab_destroy_duration_seconds{company, level, provider}` — lab destruction duration

Structured Logging:
- JSON logs with lab context
- Sensitive data redaction
- Log levels (debug, info, warn, error)

## Data Flow

### Lab Creation

```
User: petri create --company=acme --level=2 --name=test

1. ORCHESTRATOR
   ├─ Parse command, validate
   ├─ Load company config (acme.yaml)
   ├─ Prompt for credentials
   └─ Create lab record in PostgreSQL

2. GIT PROVISIONER
   ├─ Create 3 repos on GitHub
   ├─ Generate initial commit (empty)
   └─ Store repo URLs in state

3. IaC GENERATOR
   ├─ Render Terraform templates
   ├─ Commit to infra repo
   └─ Generate realistic history (10 commits)

4. TERRAFORM PROVISIONER
   ├─ Clone infra repo
   ├─ terraform init (remote state S3)
   ├─ terraform apply
   │  ├─ Create VPC, subnets
   │  ├─ Create EKS clusters (prod, staging)
   │  └─ Create RDS instance
   └─ Capture outputs (cluster endpoints)

5. GitOps GENERATOR
   ├─ Render ArgoCD manifests
   ├─ Commit to gitops repo
   └─ Generate evolution commits

6. KUBECTL PROVISIONER
   ├─ Get kubeconfigs from EKS
   ├─ Wait for clusters ready
   ├─ Install ArgoCD
   ├─ Create ArgoCD Applications
   └─ Wait for sync (apps deployed)

7. APPS GENERATOR
   ├─ Render app manifests
   ├─ Commit to apps repo
   └─ ArgoCD auto-syncs

8. ORCHESTRATOR
   ├─ Set lab status = ACTIVE
   ├─ Calculate expires_at from TTL
   ├─ Export connection details
   └─ Return success to user

User receives:
- Kubeconfig files
- Git repository URLs
- Observability dashboard URLs
- Lab expiration time
```

### Lab Destruction

```
User: petri destroy test

1. ORCHESTRATOR
   ├─ Load lab from state
   ├─ Set status = DESTROYING
   └─ Load encrypted credentials

2. KUBECTL PROVISIONER
   ├─ Delete all ArgoCD Applications
   └─ Wait for finalizers

3. TERRAFORM PROVISIONER
   ├─ Clone infra repo
   ├─ terraform init
   ├─ terraform destroy
   └─ Delete remote state

4. GIT PROVISIONER
   └─ Delete all 3 repos

5. STATE MANAGER
   ├─ Delete credentials
   ├─ Delete lab_resources
   ├─ Set lab status = DESTROYED
   └─ Archive lab record (soft delete)

6. ORCHESTRATOR
   └─ Return success to user
```

## Configuration

### Company Definition (YAML)

```yaml
companies:
  - name: acme
    description: "Cloud-native startup, AWS-based"
    cloud_provider: aws
    iac_tool: terraform
    gitops_tool: argocd
    git_provider: github
    github_org: jaimegag-labs
    ci_cd: github_actions
    
    authors:
      - name: Sarah Chen
        email: sarah@acme.corp
        role: platform_lead
      - name: Mike Rodriguez
        email: mike@acme.corp
        role: sre
    
    levels:
      1:
        clusters: 1
        nodes_per_cluster: 3
        node_instance_type: t3.medium
        apps:
          - boutique-frontend
          - boutique-cart
          - boutique-checkout
        platform:
          - argocd
          - cert-manager
          - ingress-nginx
        observability:
          - prometheus
          - grafana
        databases:
          - type: postgresql
            engine_version: "15"
            instance_class: db.t3.micro
        ttl_default: 4h
        
      2:
        clusters: 2
        cluster_names: [prod, staging]
        nodes_per_cluster: [4, 2]
        node_instance_types: [t3.large, t3.medium]
        apps:
          - online-boutique-full
        platform:
          - argocd
          - cert-manager
          - ingress-nginx
          - vault
          - external-dns
        observability:
          - prometheus
          - grafana
          - loki
          - otel-collector
        databases:
          - type: postgresql
            engine_version: "15"
            instance_class: db.t3.small
          - type: redis
            engine_version: "7.0"
            node_type: cache.t3.micro
        namespaces:
          - team-platform
          - team-checkout
          - team-recommendations
        ttl_default: 6h
```

### Petri Configuration (~/.petri/config.yaml)

```yaml
state:
  backend: sqlite                # or postgresql
  sqlite_path: ~/.petri/petri.db
  # connection_string: "postgres://localhost/petri?sslmode=disable"  # for postgresql
  
credentials:
  master_key_path: ~/.petri/master.key
  
observability:
  metrics_enabled: true
  metrics_port: 9090
  tracing_enabled: true
  tracing_endpoint: localhost:4317
  log_level: info
  
git:
  default_provider: github
  
cloud:
  terraform_version: 1.7.0
  pulumi_version: 3.100.0
  
cleanup:
  check_interval: 5m
  grace_period: 30m
```

## Security Considerations

### Credential Storage
- AES-256-GCM encryption
- Key derivation from master passphrase
- Master key stored in `~/.petri/master.key` (600 permissions)
- User responsible for securing master key
- Credentials scoped per lab
- Automatic deletion on lab destroy

### Cloud Permissions
- Petri uses credentials with full provisioning access
- Generated labs give Joe read-only access
- Separate IAM roles/service principals for Petri vs Joe
- Principle of least privilege for exported credentials

### Git Repository Security
- Ephemeral repos (auto-deleted)
- Private visibility
- No sensitive data in templates
- Secrets referenced from Vault/cloud secret managers

### Network Isolation
- Each lab gets isolated VPC/VNet
- No cross-lab network connectivity
- Ingress limited to necessary ports
- NetworkPolicies in multi-tenant namespaces

## Performance Optimizations

### Bootstrap Speed
- Pre-baked node images with cached containers
- Parallel provisioning where possible
- Lazy loading non-critical components
- Helm chart caching
- Container image mirrors per region

### Resource Efficiency
- Spot/preemptible instances where appropriate
- Rightsize node types per level
- Shared observability cluster (Level 2+)
- Auto-scaling for cost optimization

### Template Rendering
- Compiled Go templates (embed.FS)
- Template caching
- Parallel rendering

## Testing Strategy

### Unit Tests
- Template rendering correctness
- Credential encryption/decryption
- State management operations
- Config parsing

### Integration Tests
- End-to-end lab creation (local)
- Terraform/Pulumi execution
- Git operations
- Kubectl operations

### Validation Tests
- Created labs match specifications
- All apps deployed successfully
- Observability stack functional
- Realistic git history generated

## Future Enhancements

### Multi-Region
- Labs spanning multiple regions
- Cross-region observability
- Disaster recovery scenarios

### Additional Companies
- FinanceOne (highly regulated, AWS CloudFormation)
- StartupXYZ (serverless-heavy, CDK)
- Legacy Corp (VM-based transitioning to K8s)

### Advanced Scenarios
- Version skew testing
- Certificate rotation failures
- Capacity planning scenarios
- Security incident simulations
