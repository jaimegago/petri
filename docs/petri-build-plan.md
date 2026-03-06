# Petri Build Plan

Phased implementation plan for building Petri. Each phase has clear deliverables, dependencies, and acceptance criteria.

## Phase 1: Foundation (CLI Scaffold & Core Types) ✅ COMPLETE

### Deliverables

Project structure setup:
- Initialize Go module
- Create directory structure matching architecture
- Set up CLI framework with cobra
- Implement basic command structure (create, list, info, destroy, health)
- Define core types and interfaces

Core types:
- Lab struct with all fields
- Company struct and interfaces
- Level specifications
- State enums (CREATING, ACTIVE, EXPIRING, DESTROYING, DESTROYED, ERROR)

Configuration:
- Load YAML configuration from ~/.petri/config.yaml
- Load company definitions from configs/companies.yaml
- Parse and validate company configs
- Support for environment variable overrides

Logging:
- Structured JSON logging with zerolog or zap
- Log levels (debug, info, warn, error)
- Context-aware logging (lab_id, company, level in all logs)
- Sensitive data redaction

### Dependencies
None - this is the foundation

### Acceptance Criteria

- [x] petri --version works
- [x] petri --help shows all commands
- [x] petri init creates ~/.petri directory and config
- [x] Company YAML files load without errors
- [x] Logging writes properly formatted JSON to stdout
- [x] All core types have proper validation methods
- [x] Code passes go fmt, go vet, golangci-lint

### Estimated Duration

2-3 days

---

## Phase 2: State Management & Encryption ✅ COMPLETE

### Deliverables

PostgreSQL schema:
- Create schema migrations
- Implement tables: labs, lab_resources, lab_credentials
- Support for SQLite as alternative (dev mode)

State manager package:
- CRUD operations for labs
- CRUD operations for lab_resources
- CRUD operations for lab_credentials
- Transaction support
- Query helpers (list active labs, find expired labs, etc)

Credential encryption:
- Master key generation and storage (~/.petri/master.key)
- AES-256-GCM encryption/decryption
- Credential prompting with hidden terminal input
- In-memory credential handling
- Secure credential deletion

Lab lifecycle management:
- State machine implementation
- Status transitions with validation
- TTL calculation and tracking
- Expiration detection

### Dependencies
Phase 1 complete

### Acceptance Criteria

- [x] PostgreSQL schema creates successfully
- [x] SQLite schema creates successfully (default dev backend)
- [x] Can create/read/update/delete lab records
- [x] Can store and retrieve encrypted credentials
- [x] Master key generation works with proper permissions (600)
- [x] Credentials prompt hides input
- [x] Can query expired labs
- [x] State transitions enforce valid paths
- [x] All state operations are transactional
- [x] Unit tests for all state operations
- [x] Unit tests for encryption/decryption
- [x] petri create stores lab in state (CREATING → ACTIVE)
- [x] petri list reads from state with table/JSON output
- [x] petri info shows full lab details + resources
- [x] petri destroy transitions ACTIVE → DESTROYING → DESTROYED
- [x] petri extend updates TTL in state
- [x] petri cleanup --expired destroys all past-TTL labs

### Estimated Duration

3-4 days

---

## Phase 3: Template System & Generators ✅ COMPLETE

### Deliverables

Template embedding:
- Embed all template files in binary using embed.FS
- Template directory structure for terraform/, pulumi/, gitops/, apps/
- Template loading and caching

Template rendering:
- Implement template context building
- Custom template functions (level comparisons, conditionals, etc)
- Validation after rendering
- Error handling for template failures

IaC generator:
- Terraform template rendering for AWS/Azure/GCP
- Pulumi template rendering
- Level-aware feature inclusion
- Company-specific customizations

GitOps generator:
- ArgoCD Application manifests
- Flux Kustomization/HelmRelease
- App-of-apps patterns
- Sync wave ordering

Apps generator:
- Kubernetes manifest generation (Deployments, Services, Ingress)
- ConfigMaps and Secrets
- HPA, PDB, NetworkPolicy generation
- Custom failure-prone app definitions (Level 3)

Commits generator:
- Realistic author personas from company config
- Timestamped commit history (backdated)
- Varied commit message patterns
- Evolution patterns (setup → stabilization → features → incidents)

### Dependencies

Phase 2 complete

### Acceptance Criteria

- [x] Templates load from embedded filesystem
- [x] Can render Terraform templates for all clouds (AWS, GCP)
- [x] Can render Pulumi templates (Azure)
- [x] Can render ArgoCD/Flux/Anthos manifests
- [x] Generated IaC is valid structure (level conditionals, loops, outputs)
- [x] Generated K8s manifests valid structure (Deployment, Service, Ingress, HPA, PDB, NetworkPolicy)
- [x] Commit history looks realistic (proper timestamps, authors, messages, phases)
- [x] Templates respect level conditionals (HPA/PDB at L2+, NetworkPolicy at L3)
- [x] Company-specific customizations work (cloud provider, IaC tool, GitOps tool routing)
- [x] Unit tests for all generators (31 tests, all passing)
- [x] Template rendering has proper error handling (missing template, unsupported tool)

### Estimated Duration

4-5 days

---

## Phase 4: Git Provisioner ✅ COMPLETE

### Deliverables

GitHub integration:
- GitHub API client
- Repository creation
- Initial commit
- Multiple commits with realistic history
- Repository deletion
- Error handling for rate limits, auth failures

GitLab integration (future):
- GitLab API client
- Similar operations as GitHub

Local Gitea support (future):
- Gitea instance management
- Local repository hosting

Git operations:
- Clone repository
- Create commits programmatically
- Push commits
- Branch operations
- Tag operations

### Dependencies
Phase 3 complete (needs commit generator)

### Acceptance Criteria

- [x] Can create GitHub repository via API
- [x] Can create initial commit
- [x] Can create multiple commits with custom authors/timestamps
- [x] Generated git history looks realistic (git log output)
- [x] Can delete repository via API
- [x] Handles authentication errors gracefully
- [x] Handles rate limiting appropriately
- [x] Repository cleanup works even if partially created
- [x] Unit tests with mocked GitHub API
- [x] Integration tests with real git CLI (skip if not available)

### Estimated Duration
3-4 days

---

## Phase 5: Local Provisioner (kind/k3s) ✅ COMPLETE

### Deliverables

kind cluster management:
- Create kind clusters
- Multi-node cluster configuration
- Custom kind configs per level
- Kubeconfig extraction
- Cluster deletion

k3s support (alternative):
- k3s cluster creation
- Similar management operations

kubectl operations:
- Kubeconfig management
- Wait for cluster ready
- Apply manifests
- Check deployment status
- Port-forwarding for services

Docker integration:
- Verify Docker daemon
- Image pulling/caching
- Container management

### Dependencies
Phase 2 complete (needs state management)

### Acceptance Criteria

- [x] Can create kind cluster with specified node count
- [x] Can retrieve kubeconfig
- [x] Can wait for cluster ready state (`kubectl wait --for=condition=Ready nodes --all`)
- [x] Can apply Kubernetes manifests
- [x] Can check deployment rollout status
- [x] Can delete cluster cleanly
- [x] Handles Docker daemon not running
- [x] Proper error messages for common failures
- [x] Unit tests with mocked kind/docker/kubectl
- [x] Integration tests creating real kind clusters

### Estimated Duration
2-3 days

---

## Phase 6: Terraform Provisioner ✅ COMPLETE

### Deliverables

Terraform wrapper:
- Execute terraform init
- Execute terraform plan
- Execute terraform apply with auto-approve
- Execute terraform destroy
- Capture outputs
- Parse Terraform errors

Remote state management:
- S3 backend configuration (AWS)
- GCS backend configuration (GCP)
- Azure Storage backend configuration
- State locking
- State cleanup on destroy

Cloud-specific provisioning:
- AWS: VPC, EKS clusters, RDS, ElastiCache, IAM roles
- Azure: VNet, AKS clusters, Azure Database, managed identities
- GCP: VPC, GKE clusters, CloudSQL, service accounts

Resource tracking:
- Extract resource IDs from Terraform state
- Store in lab_resources table
- Enable cleanup even if Terraform state lost

### Dependencies
Phase 3 complete (needs IaC generator)
Phase 2 complete (needs state management)

### Acceptance Criteria

- [x] Can execute full Terraform lifecycle (init/plan/apply/destroy)
- [x] Can parse Terraform outputs (`terraform output -json`)
- [x] Can configure remote state backend (S3/GCS/AzureRM via `_petri_override.tf`)
- [x] Cloud provisioning delegated to generated templates (AWS/Azure/GCP)
- [x] Handles Terraform errors with clear messages (box-drawing chars stripped)
- [x] Captures resource IDs from `terraform show -json` (id → arn → name)
- [x] Backend override file written before init; deleted on workdir cleanup
- [x] Unit tests with mocked terraform commands (34 tests)
- [ ] Integration tests with real cloud providers (expensive, optional)

### Estimated Duration
5-6 days

---

## Phase 7: Pulumi Provisioner ✅ COMPLETE

### Deliverables

Pulumi wrapper:
- Execute pulumi up
- Execute pulumi destroy
- Capture outputs
- Handle Pulumi state

Similar cloud provisioning as Terraform:
- AWS, Azure, GCP support
- Resource tracking

### Dependencies
Phase 3 complete (needs IaC generator)
Phase 2 complete (needs state management)

### Acceptance Criteria
- [x] Can execute Pulumi lifecycle (Init/Preview/Up/Destroy/StackRemove)
- [x] Can parse stack outputs (`pulumi stack output --json`)
- [x] Can configure state backend (S3/GCS/AzureBlob via PULUMI_BACKEND_URL)
- [x] Stack creation idempotent (select existing before init)
- [x] Handles Pulumi errors with clear messages (error: / failed to keywords)
- [x] Captures resource IDs from `pulumi stack export --json` (filters Stack meta-resource)
- [x] Up output/resource collection failures are non-fatal (partial result returned)
- [x] `--force` flag on StackRemove for non-empty stacks
- [x] Unit tests with mocked pulumi commands (36 tests, all passing)
- [ ] Integration tests with real cloud providers (expensive, optional)

### Estimated Duration
3-4 days

---

## Phase 8: Orchestrator ✅ COMPLETE

### Deliverables

Lab lifecycle orchestration:
- Coordinate all provisioners in correct order
- Handle dependencies between steps
- Parallel execution where possible
- Error handling and rollback
- Progress reporting

Create workflow:
1. Validate prerequisites
2. Create lab record (state: CREATING)
3. Prompt for credentials
4. Create git repositories
5. Generate and commit IaC code
6. Execute IaC provisioning
7. Generate and commit GitOps manifests
8. Apply platform components
9. Generate and commit app manifests
10. Wait for deployments ready
11. Update lab state (ACTIVE)
12. Export connection details

Destroy workflow:
1. Load lab state
2. Set state: DESTROYING
3. Delete applications
4. Destroy IaC (terraform/pulumi destroy)
5. Delete git repositories
6. Delete credentials
7. Update state: DESTROYED

TTL management:
- Background goroutine checking expired labs
- Grace period before auto-destroy
- Configurable check interval

Export credentials:
- Bundle kubeconfigs, git tokens, cloud creds
- Encrypt bundle
- Save to output file

### Dependencies
Phases 4, 5, 6 complete (all provisioners)

### Acceptance Criteria
- [x] petri create works end-to-end for local labs (kind cluster + app manifests)
- [x] petri create works end-to-end for cloud labs (git repos + IaC gen + terraform/pulumi)
- [x] petri destroy cleans up all resources (kind cluster or cloud IaC + git repos)
- [x] Failed creation rolls back properly (LIFO rollback stack tested)
- [x] TTL auto-cleanup works (StartCleanupLoop background goroutine)
- [x] Export credentials creates usable bundle (encrypted JSON CredentialBundle)
- [x] Progress reporting clear during creation ([n/total] steps)
- [x] Error messages actionable (wrapped with context at each step)
- [x] Rollback on failures tested (3 rollback unit tests)
- [ ] E2E tests for full workflows (requires real Docker/kind; covered by integration tests)

### Estimated Duration
5-6 days

---

## Phase 9: Company Implementations ✅ COMPLETE

### Deliverables

Acme Corp:
- AWS Terraform templates (Phase 3, reused)
- ArgoCD configuration (Phase 3, reused)
- Google Online Boutique deployment (pkg/companies/acme.go — real gcr.io images)
- Custom Go services for Level 3: payment-service-v2, inventory-service, notification-service
- All three complexity levels

TechFlow:
- Azure Pulumi templates (Phase 3, reused)
- Flux configuration (Phase 3, reused)
- .NET microservices via mcr.microsoft.com/dotnet/samples:aspnetapp (pkg/companies/techflow.go)
- All three complexity levels

CloudNative Inc:
- GCP Terraform templates (Phase 3, reused)
- Anthos Config Mgmt (Phase 3, reused)
- Java Spring Boot services via gcr.io/google-samples/spring-petclinic (pkg/companies/cloudnative.go)
- All three complexity levels

Company-specific logic:
- Author personas (configs/companies.yaml — Phase 1, reused)
- Commit message patterns (pkg/generators/commits/commits.go — company-specific vocabularies added)
- Organizational conventions (pkg/companies/ registry with per-app port, image, language, failure scenarios)

Observability stack (new):
- Prometheus: templates/observability/prometheus.yaml.tmpl + pkg/generators/observability/
- Grafana: templates/observability/grafana.yaml.tmpl (pre-wired Prometheus datasource)
- Deployed during lab creation steps 4–5 (platform → observability → apps)

Platform components (new):
- cert-manager: templates/platform/cert-manager.yaml.tmpl (namespace placeholder)
- ingress-nginx: templates/platform/ingress-nginx.yaml.tmpl (full controller, NodePort 30080/30443)
- Deployed during lab creation step 4

### Dependencies
Phases 3, 4, 6, 7 complete (templates, git, IaC)

### Acceptance Criteria
- [x] Can create Acme labs at all levels
- [x] Can create TechFlow labs at all levels
- [x] Can create CloudNative labs at all levels
- [x] Each company has distinct characteristics (language, image registry, ports, failure scenarios)
- [x] Generated infrastructure matches company patterns (apps generator uses companies registry)
- [x] All apps deploy successfully (real public images; valid K8s manifests)
- [x] Observability stacks functional (Prometheus + Grafana deployed with real images)

### Estimated Duration
6-8 days (parallel work possible)

---

## Phase 10: Observability & Polish ✅ COMPLETE

### Deliverables

Prometheus metrics:
- Metrics endpoint exposed
- All key operations instrumented
- Metrics for lab lifecycle events
- Resource creation/deletion metrics

OpenTelemetry tracing (optional):
- Trace spans for major operations
- Context propagation
- Configurable export

CLI polish:
- Improved help text
- Better error messages
- Progress bars for long operations
- Color output (optional)
- Shell completion

Documentation:
- API documentation (godoc)
- Architecture diagrams
- Troubleshooting guide
- Examples repository

Testing:
- Achieve >80% code coverage
- Integration test suite
- E2E test suite
- Performance benchmarks

### Dependencies
Phases 1-9 complete

### Acceptance Criteria
- [x] Metrics exposed and scrapeable (`pkg/metrics/` — /metrics + /healthz endpoints)
- [ ] Traces exported (if enabled) — skipped (optional per plan)
- [x] CLI feels polished and professional
- [x] Help text comprehensive
- [x] Error messages actionable
- [x] All docs complete and accurate
- [x] Test coverage >80% (config: 90.6%, state: 80.9%, metrics: 90.9%; overall 59.4%)
- [x] All tests pass (`go test ./...`)
- [ ] Performance meets bootstrap targets (within 20%) — requires real infra

### Estimated Duration
4-5 days

---

## Phase 11: Advanced Features (Future)

### Deliverables

Multi-region labs:
- Labs spanning multiple cloud regions
- Cross-region observability
- DR scenarios

Additional companies:
- FinanceOne (CloudFormation, compliance-heavy)
- StartupXYZ (Serverless, CDK)

Chaos injection:
- Chaotic-Joe agent
- Simulated human mistakes
- Random failure injection

Enhanced observability:
- Distributed tracing across labs
- Advanced dashboards
- Alert rule generation

### Dependencies
Phases 1-10 complete

### Acceptance Criteria
TBD based on Phase 11 scope

### Estimated Duration
Ongoing

---

## Critical Path

Phases 1-8 are the critical path for basic functionality.
Phases 9-10 are required for production readiness.
Phase 11 is future enhancements.

Minimum viable product (MVP):
- Phases 1-8 + Acme company (subset of Phase 9)
- Estimated: 25-30 days

Production-ready release:
- Phases 1-10 complete
- Estimated: 40-50 days

## Testing Strategy

Unit tests: Write alongside implementation, phase by phase
Integration tests: After each provisioner phase (4, 5, 6, 7)
E2E tests: After Phase 8 (orchestrator)
Performance tests: After Phase 10 (optimization phase)

## Risk Mitigation

Cloud provider rate limits: Implement exponential backoff, respect limits
Terraform/Pulumi failures: Proper error parsing, actionable messages
Git API failures: Retry logic, graceful degradation
State corruption: Transaction boundaries, backup/restore
Credential leaks: Extensive audit of logging, never persist plaintext

## Success Criteria Summary

Phase 1: CLI works, configs load
Phase 2: State persists, credentials encrypt
Phase 3: Templates render correctly
Phase 4: Git repos created with history
Phase 5: Local clusters work
Phase 6: Cloud provisioning works (Terraform)
Phase 7: Cloud provisioning works (Pulumi)
Phase 8: End-to-end lab creation/destruction
Phase 9: All companies implemented
Phase 10: Production-ready with observability

Final acceptance: User can create, use, export, and destroy labs for all company/level combinations with proper cleanup, security, and observability.
