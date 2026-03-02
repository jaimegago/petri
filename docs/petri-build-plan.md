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

## Phase 3: Template System & Generators 🚧 IN PROGRESS

### Progress

Package stubs and directory structure created. No template content or generator logic implemented yet.

**Done:**

- Package stubs created: `pkg/generators/iac`, `pkg/generators/gitops`, `pkg/generators/apps`, `pkg/generators/commits`
- Provisioner stubs created: `pkg/provisioners/git`, `pkg/provisioners/local`, `pkg/provisioners/terraform`, `pkg/provisioners/pulumi`, `pkg/provisioners/kubectl`
- Orchestrator stub created: `pkg/orchestrator`
- Company registry stub created: `pkg/companies`
- Template directory structure created: `templates/terraform/`, `templates/pulumi/`, `templates/gitops/`, `templates/apps/`

**Remaining:**

- All template files (terraform, pulumi, gitops, apps)
- embed.FS wiring and template loading/caching
- IaC generator implementation
- GitOps generator implementation
- Apps generator implementation
- Commits generator implementation
- Unit tests for all generators

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

- [ ] Templates load from embedded filesystem
- [ ] Can render Terraform templates for all clouds
- [ ] Can render Pulumi templates
- [ ] Can render ArgoCD/Flux manifests
- [ ] Generated IaC is valid (terraform validate passes)
- [ ] Generated K8s manifests are valid (kubectl dry-run)
- [ ] Commit history looks realistic (proper timestamps, authors, messages)
- [ ] Templates respect level conditionals
- [ ] Company-specific customizations work
- [ ] Unit tests for all generators
- [ ] Template rendering has proper error handling

### Estimated Duration

4-5 days

---

## Phase 4: Git Provisioner

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
- Can create GitHub repository via API
- Can create initial commit
- Can create multiple commits with custom authors/timestamps
- Generated git history looks realistic (git log output)
- Can delete repository via API
- Handles authentication errors gracefully
- Handles rate limiting appropriately
- Repository cleanup works even if partially created
- Unit tests with mocked GitHub API
- Integration tests with real GitHub (test repos)

### Estimated Duration
3-4 days

---

## Phase 5: Local Provisioner (kind/k3s)

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
- Can create kind cluster with specified node count
- Can retrieve kubeconfig
- Can wait for cluster ready state
- Can apply Kubernetes manifests
- Can check deployment rollout status
- Can delete cluster cleanly
- Handles Docker daemon not running
- Proper error messages for common failures
- Unit tests with mocked kubectl
- Integration tests creating real kind clusters

### Estimated Duration
2-3 days

---

## Phase 6: Terraform Provisioner

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
- Can execute full Terraform lifecycle (init/plan/apply/destroy)
- Can parse Terraform outputs
- Can configure remote state backend
- Can create AWS infrastructure (VPC, EKS)
- Can create Azure infrastructure (VNet, AKS)
- Can create GCP infrastructure (VPC, GKE)
- Handles Terraform errors with clear messages
- Captures resource IDs for tracking
- State backend cleanup works
- Unit tests with mocked terraform commands
- Integration tests with real cloud providers (expensive, optional)

### Estimated Duration
5-6 days

---

## Phase 7: Pulumi Provisioner

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
Similar to Phase 6 but for Pulumi
- Can execute Pulumi lifecycle
- Can provision cloud infrastructure
- Proper error handling
- Unit and integration tests

### Estimated Duration
3-4 days

---

## Phase 8: Orchestrator

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
- petri create works end-to-end for local labs
- petri create works end-to-end for AWS labs
- petri destroy cleans up all resources
- Failed creation rolls back properly
- TTL auto-cleanup works
- Export credentials creates usable bundle
- Progress reporting clear during creation
- Error messages actionable
- Rollback on failures tested
- E2E tests for full workflows

### Estimated Duration
5-6 days

---

## Phase 9: Company Implementations

### Deliverables

Acme Corp:
- AWS Terraform templates
- ArgoCD configuration
- Google Online Boutique deployment
- Custom Go services (Level 3)
- All three complexity levels

TechFlow:
- Azure Pulumi templates
- Flux configuration
- .NET microservices
- All three complexity levels

CloudNative Inc:
- GCP Terraform templates
- Anthos Config Mgmt
- Java Spring Boot services
- All three complexity levels

Company-specific logic:
- Author personas
- Commit message patterns
- Organizational conventions

### Dependencies
Phases 3, 4, 6, 7 complete (templates, git, IaC)

### Acceptance Criteria
- Can create Acme labs at all levels
- Can create TechFlow labs at all levels
- Can create CloudNative labs at all levels
- Each company has distinct characteristics
- Generated infrastructure matches company patterns
- All apps deploy successfully
- Observability stacks functional

### Estimated Duration
6-8 days (parallel work possible)

---

## Phase 10: Observability & Polish

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
- Metrics exposed and scrapeable
- Traces exported (if enabled)
- CLI feels polished and professional
- Help text comprehensive
- Error messages actionable
- All docs complete and accurate
- Test coverage >80%
- All tests pass
- Performance meets bootstrap targets (within 20%)

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
