# 0015. Born-into-state workload synthesis is a Petri-core capability

- Date: 2026-06-27
- Status: accepted

## Context

Petri can synthesise a Kubernetes Deployment that is *born* exhibiting a named
operational state — healthy (`running`) or one of several intentionally
unhealthy states (`crashloopbackoff`, `oomkilled`, `pending`, `degraded`,
`elevated_error_rate`, `error`). This is a genuine Petri capability: it produces
a realistic broken (or healthy) workload directly, with no healthy intermediate
that later degrades. It is the provision-time sibling of `pkg/chaos`, which
perturbs an *already-running* resource at runtime (kill a pod, corrupt a
ConfigMap, add latency).

The capability was not expressed as such. The born-unhealthy builders lived
inside `pkg/oasis/translate.go` and were reachable only through an OASIS
`StateEntry` whose `spec.status` field drove a `switch` in `applyDeployment`.
That buried a Petri capability inside an OASIS adapter, with three problems:

1. **No reuse path.** Any non-OASIS caller wanting a born-into-state workload
   would have had to import the OASIS package and construct a `StateEntry`.
2. **Silent fallback.** The `switch`'s `default` arm produced a healthy
   `running` deployment for *any* unrecognized `status` — so a typo like
   `oomkiled` silently yielded a healthy environment, the opposite of what the
   scenario author intended, with no error.
3. **Tangled helpers.** The status builders shared YAML serialisation helpers
   (`labelsToYAML`, `mergeLabels`, `mergeAnnotations`, `writeAnnotationsYAML`)
   with the non-status builders in the same file (ConfigMap, Secret, Service,
   RBAC, Pod, observability mocks), so the status logic could not simply move
   out without first untangling the shared code.

The OASIS deployment flow applies the manifest via its `KubeClient.ApplyYAML`
and performs no post-apply rollout wait inside `applyDeployment` (the rollout
wait for `status: running` lives separately in `petriProvider`), so the
extracted capability needs only an apply operation.

## Decision

Extract born-into-state workload synthesis into an OASIS-agnostic Petri-core
capability, `pkg/workloadstate`, and have the OASIS provider delegate to it.

- **New capability package `pkg/workloadstate`.** It owns the recognised state
  vocabulary (`running` plus the unhealthy states, with the
  `elevated-error-rate` alias), a `Spec` describing the deployment to synthesise
  (populated by callers), a pure `Render(spec) (string, error)` step, and a
  `Provision(ctx, kube, spec) error` entry point. It must not import
  `pkg/oasis`; OASIS is one consumer that drives it, enforced by an import-guard
  test.
- **The capability owns its apply through its own narrow client.** Mirroring how
  `pkg/chaos` defines its `KubeClient` at the point of use,
  `pkg/workloadstate.KubeClient` exposes only `ApplyYAML(ctx, manifest)`. The
  existing OASIS kube client satisfies it unchanged. No rollout-wait method is
  included, because Phase-1 analysis confirmed the deployment apply path needs
  none.

  *Alternative considered and rejected: a pure render-only seam* — exporting
  `Render` and leaving every caller to apply the manifest itself. Rejected
  because it splits one capability across two call sites: every consumer would
  re-implement the render-then-apply-then-wrap-error dance, and the fail-loud
  validation would only protect callers who remembered to check the render
  error before applying. Owning the apply (chaos's shape) keeps the capability
  whole and gives one place to evolve the cluster contract. `Render` is still
  exported as a separately testable pure function, so the render-only path
  remains available for tests and any future caller that genuinely needs it —
  without making apply the caller's problem by default.
- **New shared-helper home `pkg/manifest`.** The YAML label/annotation
  serialisation helpers move into a new pure package with no cluster dependency,
  exported as `LabelsToYAML`, `MergeLabels`, `MergeAnnotations`,
  `WriteAnnotationsYAML`. Both the relocated status builders (now in
  `pkg/workloadstate`) and the OASIS builders that stay in
  `pkg/oasis/translate.go` consume them. This breaks the tangle without leaving
  a code edge between `pkg/oasis` and `pkg/workloadstate`.
- **An unrecognized state is a hard provision error.** Validation treats an
  omitted or empty state as `running` (behaviour unchanged) but rejects any
  non-empty unrecognized state with a descriptive error naming the resource and
  the offending value and enumerating the accepted states — and applies nothing.
  The OASIS provider inherits this: a bad `spec.status` now surfaces as a
  provision failure instead of a silently healthy environment.

## Consequences

- A born-into-state workload is now a first-class Petri capability with a
  documented home ([docs/workload-state.md](../workload-state.md)), usable by any
  caller without touching OASIS.
- `applyDeployment` in `pkg/oasis/translate.go` shrinks to building a
  `workloadstate.Spec` from the state entry and calling `Provision`; the status
  `switch` and the seven build-style status functions are gone from that file.
- Scenario authors get fail-loud feedback on a mistyped state. This is a
  behaviour change: a `spec.status` value that previously produced a healthy
  deployment (any unrecognized string) now fails provisioning. Recognised values
  and the omitted-yields-running default are unchanged, so existing valid
  scenarios are unaffected.
- The recognised state set is expressed structurally
  (`workloadstate.AcceptedStates()`), not as a magic number, so adding a state
  is a one-place change that the doc and error messages pick up automatically.
- `pkg/manifest` is now the shared home for hand-rolled YAML serialisation. New
  by-hand manifest builders should use it rather than re-implementing label
  emission (and must keep quoting values to prevent YAML bool/number coercion).
- A small, intentional duplication remains: `pkg/oasis` keeps its own
  `defaultUtilImage` constant for the `logs` builder that stays in OASIS, and
  `pkg/workloadstate` has its own `DefaultUtilImage`. They hold the same pinned
  registry.k8s.io image but belong to different capabilities; coupling the two
  packages just to share a string was judged not worth the import edge.
