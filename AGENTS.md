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
- Lab `Status` transitions live behind two shared entry points: the `Lab.CanTransitionTo` table in `pkg/types/lab.go` and the lazy `TransitionIfExpired` helper in `pkg/state/transitions.go`. CLI readers (`petri info`, `petri list`, `petri serve --lab`) call `TransitionIfExpired` before rendering; the background lab reaper in `petri serve` (wired via `Orchestrator.StartCleanupLoop` and `pkg/asynctasks`) handles the eventual `EXPIRED → DESTROYED` step. New status mutations belong alongside these, not scattered across command files — see ADR 0013.
- Provisioning a workload that is **born exhibiting a named operational state** (healthy or intentionally unhealthy) is an OASIS-agnostic capability owned by `pkg/workloadstate` — the provision-time sibling of `pkg/chaos` (which perturbs already-running resources at runtime). It owns the recognised state vocabulary, a pure `Render(spec)`, and a `Provision(ctx, kube, spec)` entry point against its own narrow point-of-use `KubeClient` (`ApplyYAML`), mirroring chaos's shape. The OASIS provider's `applyDeployment` builds a `workloadstate.Spec` and delegates; the born-into-state builders must not be re-added to `pkg/oasis`. An omitted/empty state means `running`; a non-empty unrecognized state is a **hard provision error**, never a silent healthy fallback. The recognised state set is structural — see `workloadstate.AcceptedStates()` and the capability reference at `docs/workload-state.md` — do not hardcode its members or count elsewhere. `/v1/provision` reports `ready` only once every declared workload has converged: `workloadstate.Spec.Convergence` decides, per state, whether that is a completed rollout or a symptom every pod exhibits (`docs/workload-state.md`); `pkg/oasis` must not re-derive it from the status string. See ADR 0015.
- `pkg/manifest` is the shared home for hand-rolled Kubernetes YAML serialisation (`LabelsToYAML`, `MergeLabels`, `MergeAnnotations`, `WriteAnnotationsYAML`). Both `pkg/oasis/translate.go` and `pkg/workloadstate` consume it. New by-hand manifest builders use it rather than re-implementing label/annotation emission. It is a pure package with no cluster dependency.
- A **cause under diagnosis** — a misconfiguration an agent is scored on finding — is materialised by the petri-owned application (`images/svc`, a nested Go module; pin `fault.AppImage`), never by a stand-in container that announces itself. `pkg/fault` is the catalog, defined once: `fault.Parse` is the single construction path for a scenario's `fault` block and for `petri inject --param`, `fault.Catalog()` is the structural source of classes and of what the pinned image validates, and `fault.WaitForSymptom` is the one symptom check both triggers use. `workloadstate.Spec.Fault` renders the application born misconfigured with ConfigMap references `optional: true`; `petri serve` refuses readiness until every pod exhibits the declared `expect`. Nothing the agent can read may name the mechanism — tests use a word-bounded match (`default` contains `fault`). A new fault class is a new image version plus a catalog row; do not add synthetic cause paths back to `pkg/workloadstate`. See ADR 0017 and `docs/application-image.md`.
- `petri inject <fault-type>` triggers a single one-shot chaos fault against a running lab by looking the fault up in the catalog and calling `Fault.Execute` directly — it does **not** start the continuous random `chaos.ChaosRunner`. It is the runtime counterpart to the provision-time `pkg/workloadstate` capability. The accepted fault-type surface (both validation and help text) is sourced **structurally** from `chaos.DefaultFaults()` at runtime via `acceptedFaultTypes` — never a hardcoded literal list, and never a hardcoded count. Cause classes from `fault.Catalog()` share the command and dispatch to `runInjectCause`; a test asserts the two catalogs are disjoint. The command reuses the shared active-lab resolution guard (`resolveActiveLabKubeconfig`, which itself reuses `serveLabStatusError` / `localKubeconfigPath` / `state.TransitionIfExpired`) and mirrors serve's `--kubeconfig` override. The CLI validates only target/param *shape*; per-fault target and parameter semantics belong to the fault. `petri inject --scenario <file>` is a second mode of the same command, not a new one: it replays a `pkg/scenarios` file through `scenarios.LoadFile`/`Validate`/`ScenarioRunner` with the same lab guard; and `petri inject --state <state> --target ns/Deployment/name` is a third, driving `workloadstate.Provision` (states from `workloadstate.AcceptedStates()`, never a literal list; symptom half only — causes stay on cause classes per ADR 0017). Exactly one mode is selected, and a flag the selected mode would ignore is refused rather than dropped. See ADR 0016.

## Repo Dependencies

- **docs/oasis-spec** (git submodule) — upstream OASIS evaluation spec, domain profiles, and architectural decisions. When starting work that touches OASIS types or scenarios, check if this submodule is behind upstream with `git submodule status` and update with `git submodule update --remote docs/oasis-spec` if needed.

## Build / Test / Lint

```bash
go build ./cmd/petri/
go test ./...
go test -tags integration ./...   # requires Docker + kind
go vet ./...
go fmt ./...
make svc-test                     # the nested images/svc module; root go test does not cover it
```

## Repo-Specific Conventions

- Go standards: follow the **go-backend** skill, vendored at `agent-skills/go-backend/SKILL.md` (the `agent-skills` submodule, pinned to a tag; a skill is edited in that repository, never here). Read it and its `references/` before writing, reviewing or scaffolding Go code.
- Architecture details: `docs/petri-architecture.md`.
- Company definitions: `configs/companies.yaml` (acme/aws/terraform/argocd, techflow/azure/pulumi/flux, cloudnative/gcp/terraform/anthos).
- Config loaded from `~/.petri/config.yaml`; default state backend is SQLite (`~/.petri/petri.db`).
- Three complexity levels: L1 (single cluster, basic), L2 (multi-cluster, realistic), L3 (production-realistic, full platform).
- CLI commands: create, destroy, list, info, health, init, export, extend, cleanup, serve, verify, inject, completion.
- OASIS kind clusters (`--oasis` flag on `petri create`) disable the default CNI and install Calico for NetworkPolicy enforcement, then shape every worker's allocatable to exactly `cpu: 4000m`, `memory: 4Gi` via kubelet `systemReserved` (`pkg/provisioners/local/shape.go`; target constants `OASISNodeAllocatableCPU`/`OASISNodeAllocatableMemory`). A host too small for the target yields an **unshaped** lab with a warning, not a failure; the outcome is recorded as `Cluster.NodeShape` in lab state.
- Node state entries (`pkg/oasis/nodestate.go`) bind declared node names to real lab nodes and route facts by reader: usage (`cpu_usage`, `current_utilization`) is served via mock Prometheus under the bound identity; conditions (`memory_pressure: false`) and allocatable (`allocatable_cpu`, `allocatable_memory`) are **verified** against the real node — exact quantity equality for allocatable — and a mismatch is a loud provision error. Allocatable is a lab property: provision never reconfigures a node. The fact vocabulary is closed; `memory_pressure: true` stays refused.
- YAML manifests built by hand (not templates) in `pkg/chaos/kube.go`, `pkg/oasis/translate.go`, and `pkg/workloadstate` must quote all label/annotation values to prevent YAML bool/number coercion — the shared `pkg/manifest` helpers do this for you.
- OASIS Deployment/Pod state entries that omit `spec.image` resolve to `config.OASIS.DefaultImage` (default: `registry.k8s.io/nginx-slim:0.27`). Internal builders for unhealthy states (CrashLoop, OOMKilled, logs) use `registry.k8s.io/e2e-test-images/busybox:1.37.0-2`; `elevated_error_rate` and the mock Prometheus/Jaeger/Alertmanager servers run nginx from `workloadstate.HTTPServerImage` (`registry.k8s.io/nginx-slim:0.27`). Never default to Docker Hub images — its blob storage runs on Cloudflare R2, which is null-routed by some ISPs and corporate networks. `TestNoDefaultImageIsOnDockerHub` (`pkg/oasis`) and `TestRender_NoSuppliedImageIsOnDockerHub` (`pkg/workloadstate`) hold every rendered default to it.
- A bare `pod` state entry is rendered through `pkg/workloadstate` (`Spec.Kind = KindPod`): its `status` is its state, readiness waits on its `Convergence`, and a state a bare pod cannot be (`degraded`; `pending` with a `node`) is a provision error.

## Project Management

Work is ordered from the maintainer's ledger (`joe-pm`), which dispatches sessions into
this repository. The repository's own surfaces:

- **Backlog**: `docs/backlog/`, one file per item, indexed by `docs/backlog/INDEX.md`
  (the item format lives there). Regenerate the index whenever an item is created,
  changed, or archived; a finished item moves to `docs/backlog/done/`.
- **Decisions**: `docs/decisions/NNNN-*.md`, petri's own numbering.
- **Issues**: GitHub Issues are inbound reports, not work orders — work on one starts by
  filing or citing a backlog item.
- **Commits** begin with the work's slug (`<slug>: summary`). Push to `origin main` is
  done; there is no PR gate.
