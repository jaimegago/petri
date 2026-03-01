You are building Petri, an infrastructure lab framework that spawns complete, realistic company infrastructures for testing Joe, an LLM-based infrastructure copilot.

PROJECT GOAL

Create a production-grade Go CLI application that generates ephemeral infrastructure labs mimicking real company environments. Each lab includes Kubernetes clusters, applications, IaC repositories with realistic git history, observability stacks, and platform components.

WHAT PETRI DOES

Petri orchestrates the creation of complete infrastructure environments:

1. Creates git repositories (GitHub/GitLab) with realistic commit history spanning weeks/months
2. Generates IaC code (Terraform, Pulumi, CloudFormation) from templates
3. Provisions cloud infrastructure (AWS EKS, Azure AKS, GCP GKE) or local clusters (kind/k3s)
4. Deploys platform components (ArgoCD, Vault, Istio, cert-manager, observability)
5. Deploys applications (microservices with realistic failure scenarios)
6. Tracks lab state in PostgreSQL with encrypted credentials
7. Provides TTL-based automatic cleanup
8. Exports credentials for Joe to access labs

KEY ARCHITECTURE DECISIONS

Company Profiles: Each company (Acme, TechFlow, CloudNative) represents different organizational patterns - different cloud providers, IaC tools, GitOps tools, and application stacks.

Complexity Levels: Three levels of progressive complexity:
- Level 1: Single cluster, basic apps, minimal observability (validation testing)
- Level 2: Multi-cluster, realistic apps, enhanced observability (integration testing)
- Level 3: Production-realistic, full platform, complete observability (stress testing)

Three-Phase Bootstrap:
- Phase 0: Petri framework (persistent, orchestrates everything)
- Phase 1: Generated artifacts (git repos, IaC code, realistic history)
- Phase 2: Lab runtime (running infrastructure Joe interacts with)

Credential Security: All credentials encrypted at rest with AES-256-GCM, prompted at runtime, never logged, scoped per lab, deleted on destroy.

State Management: PostgreSQL tracks labs, resources, and encrypted credentials. Clean separation between lab lifecycle states.

DOCUMENTATION STRUCTURE

Read these files in order:

1. petri-architecture.md - Complete technical architecture, component specifications, data flows, design decisions
2. README.md - User-facing documentation, CLI commands, examples
3. petri-build-plan.md - Phased implementation plan with dependencies and acceptance criteria
4. go-standards.md - Go code quality and architecture standards (MUST follow for all Go code)

GO STANDARDS

All Go code in this project MUST follow docs/go-standards.md. Key rules enforced here:

Architecture:

- Use dependency injection via constructor parameters. No package-level singletons or global state.
- Organize internal packages by domain (e.g., internal/orders/), not by layer (not internal/handlers/).
- Define interfaces at the point of use (in the consuming package), not at the provider.
- Business logic must never import transport, database, or instrumentation packages directly.

Error Handling:

- Always wrap errors with context using the `%w` verb: `fmt.Errorf("doing X: %w", err)`.
- Use errors.Is() / errors.As() when inspecting error types in callers.
- No panics in production code paths.

Testing:

- Unit tests use table-driven patterns with t.Run() sub-tests.
- Mock interfaces (defined in business logic), not concrete types. Use testify/mock or simple hand-rolled mocks.
- Unit tests must achieve >80% code coverage.
- Integration tests use build tag `//go:build integration` and run separately from unit tests.

Observability:

- Instrumentation (metrics, logs, traces) is added via middleware/decorator wrappers — NOT embedded in business logic.
- Pass context.Context through all call chains for trace propagation.
- Structured logging only (zerolog). Never use fmt.Println for operational output in library code.

Code Style:

- Follow Effective Go: clear names, small interfaces, explicit errors, defer for cleanup.
- One type per file unless types are tightly coupled.
- All exported functions, types, and packages must have doc comments.
- Run go fmt, go vet, and golangci-lint before committing.

IMPLEMENTATION REQUIREMENTS

Language: Go 1.21+

Required Packages:
- CLI framework: cobra + viper for configuration
- PostgreSQL: pgx or GORM
- Templating: text/template and html/template (stdlib)
- Encryption: crypto/aes, crypto/cipher (stdlib)
- Git operations: go-git or exec git commands
- HTTP clients: net/http (stdlib) for GitHub/GitLab APIs
- Logging: zerolog or zap for structured JSON logs
- Metrics: prometheus/client_golang
- Tracing: go.opentelemetry.io/otel

Project Structure:
Follow the structure outlined in petri-architecture.md. Key packages: orchestrator, provisioners (git, terraform, pulumi, kubectl, local), generators (iac, gitops, apps, commits), companies, state, crypto.

Templates:
Use Go's embed package to embed all templates in the binary. Templates live in templates/ directory, organized by tool (terraform/, pulumi/, gitops/, apps/).

Configuration:
YAML-based configuration for companies and Petri settings. Load from ~/.petri/config.yaml and configs/companies.yaml.

Error Handling:
Explicit error handling, no panics in production code. Wrap errors with context. Clean rollback on failures (delete created resources).

Testing:
Unit tests for all packages. Integration tests for provisioners (requires Docker). E2E tests create real labs (expensive, optional).

Security:
Never log credentials. Redact sensitive data in logs. File permissions 600 for master key. Input validation on all user inputs. Secure random generation for lab IDs.

Observability:
Prometheus metrics exposed on configurable port. Structured JSON logging. OpenTelemetry tracing optional. All operations instrumented with duration, status, error tracking.

CRITICAL BEHAVIORS

Idempotency: Lab creation checks if lab with name already exists. Resource creation checks existing state.

Atomicity: Failed lab creation rolls back all created resources. State updates happen in transactions.

Credential Flow:
1. Prompt user for credentials (hidden terminal input)
2. Encrypt immediately with master key
3. Store encrypted in PostgreSQL
4. Use decrypted in-memory during operations
5. Never persist decrypted
6. Delete on lab destroy

Realistic Git History:
Generate commits with varied authors, realistic timestamps (backdated), authentic commit messages, proper evolution pattern (initial setup, then fixes, then features, then incidents).

Template Rendering:
Templates support conditionals based on company, level, cloud provider. Custom template functions for common patterns. Validation after rendering.

State Tracking:
Every cloud resource tracked in lab_resources table with cloud-specific ID. Enables proper cleanup even if local state corrupted.

Cleanup:
TTL checked by background goroutine every 5 minutes. Grace period before destruction. Destroy process retries on transient failures. Orphaned resource detection and cleanup.

WHAT NOT TO DO

Do not implement actual Terraform/Pulumi code - only generate it from templates.
Do not implement Kubernetes applications - only generate manifests.
Do not implement observability backends - only provision them via IaC.
Do not store credentials in plaintext anywhere.
Do not skip cleanup on errors - always attempt rollback.
Do not use global state - pass dependencies explicitly.

BUILD APPROACH

Follow petri-build-plan.md for phased implementation. Each phase has clear acceptance criteria. Start with Phase 1 (CLI scaffold and core types), validate before moving to next phase.

Test incrementally. Each provisioner should be testable in isolation. Use interfaces for all external dependencies (cloud providers, git, IaC tools) to enable mocking.

Focus on correctness first, then optimization. Bootstrap time targets are goals, not hard requirements initially.

ACCEPTANCE CRITERIA

Successful build completion when:

1. User can run: petri create --company=acme --level=1 --local --name=test
2. This creates a working kind cluster with deployed applications
3. User can run: petri info test and see all connection details
4. User can run: petri destroy test and everything is cleaned up
5. User can run: petri create --company=acme --level=2 (cloud) successfully
6. Lab auto-destroys after TTL expires
7. User can export credentials for Joe: petri export-creds test
8. All unit tests pass
9. Integration tests pass (with Docker available)
10. Code follows Go best practices (go fmt, go vet, golangci-lint)

SUCCESS METRICS

Code quality: Clean architecture, testable, maintainable
Reliability: Handles failures gracefully, cleans up properly
Security: Credentials never exposed
Performance: Meets bootstrap time targets within 20%
Usability: Clear error messages, helpful output, intuitive commands

Read petri-architecture.md and petri-build-plan.md carefully before starting implementation.
