# Petri

Petri is an infrastructure lab framework (Go CLI) that spawns ephemeral, realistic company infrastructures for testing Joe, an LLM-based infrastructure copilot. It also serves as an OASIS environment provider (`petri serve`) for scenario-based evaluation of AI agents against live Kubernetes clusters.

## Architectural Invariants

- Pure Go, no CGO dependencies (SQLite via modernc.org/sqlite).
- Dependency injection via constructor parameters. No package-level singletons or global state.
- All credentials encrypted at rest with AES-256-GCM, never logged, never persisted decrypted.
- Failed lab creation rolls back all created resources (LIFO rollback stack).
- Every cloud resource tracked in state DB for cleanup even if local state is corrupted.
- Petri generates IaC/manifests from templates — it does not implement Terraform modules, Kubernetes apps, or observability backends directly.
- Templates embedded in the binary via `//go:embed`.
- Interfaces defined at the point of use (consuming package), not at the provider.
- All errors wrapped with `fmt.Errorf("context: %w", err)`. No panics in production code.
- Integration tests use build tag `//go:build integration` and run separately.
- `pkg/preflight` is the single source of truth for substrate-readiness checks (kubeconfig, cluster reachability, RBAC, image pullability, audit log path). Both `petri verify` and `petri serve --verify` go through `preflight.Run`. New ad-hoc reachability probes elsewhere in the codebase should be migrated to it over time rather than reimplemented inline. Cross-package consumers (e.g. the typed-error fast-fail path in `pkg/oasis`) consume substrate probes via the public API (`preflight.ProbeImage` and friends), not by reimplementing the underlying logic. New substrate checks must land in `pkg/preflight` and expose a narrow public entry point — see ADR 0009.

## Repo Dependencies

- **docs/oasis-spec** (git submodule) — upstream OASIS evaluation spec, domain profiles, and architectural decisions. When starting work that touches OASIS types or scenarios, check if this submodule is behind upstream with `git submodule status` and update with `git submodule update --remote docs/oasis-spec` if needed.

## Build / Test / Lint

```bash
go build ./cmd/petri/
go test ./...
go test -tags integration ./...   # requires Docker + kind
go vet ./...
go fmt ./...
```

## Repo-Specific Conventions

- Go standards: follow the `go-backend` skill (`~/.claude/skills/go-backend/`).
- Architecture details: `docs/petri-architecture.md`.
- Company definitions: `configs/companies.yaml` (acme/aws/terraform/argocd, techflow/azure/pulumi/flux, cloudnative/gcp/terraform/anthos).
- Config loaded from `~/.petri/config.yaml`; default state backend is SQLite (`~/.petri/petri.db`).
- Three complexity levels: L1 (single cluster, basic), L2 (multi-cluster, realistic), L3 (production-realistic, full platform).
- CLI commands: create, destroy, list, info, health, init, export, extend, cleanup, serve, verify, completion.
- OASIS kind clusters (`--oasis` flag on `petri create`) disable the default CNI and install Calico for NetworkPolicy enforcement.
- YAML manifests built by hand (not templates) in `pkg/chaos/kube.go` and `pkg/oasis/translate.go` must quote all label/annotation values to prevent YAML bool/number coercion.
- OASIS Deployment/Pod state entries that omit `spec.image` resolve to `config.OASIS.DefaultImage` (default: `registry.k8s.io/nginx-slim:0.27`). Internal builders for unhealthy states (CrashLoop, OOMKilled, logs) use `registry.k8s.io/e2e-test-images/busybox:1.37.0-2`. Never default to Docker Hub images — its blob storage runs on Cloudflare R2, which is null-routed by some ISPs and corporate networks.
