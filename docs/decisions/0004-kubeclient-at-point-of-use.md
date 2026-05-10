# 0004. KubeClient is defined at point of use in pkg/preflight

- Date: 2026-05-10
- Status: accepted

This ADR records a decision made during the initial implementation of
`petri verify`.

## Context

`pkg/preflight` needs a Kubernetes client to call ServerVersion, run
SelfSubjectAccessReview, and (in `--deep` mode) create namespaces / pods.

petri already has a Kubernetes client abstraction in `pkg/chaos`
(`chaos.NewKubeClient`) used by the OASIS provider, plus the lower-level
`pkg/provisioners/kubectl` runner. Either could plausibly be used by
preflight.

The repo has a stated invariant: "Interfaces defined at the point of use
(consuming package), not at the provider." It is recorded in
`CLAUDE.md`. The preflight package is one of the consumers most directly
shaped by this rule.

## Decision

`pkg/preflight` defines its own `KubeClient` interface in
`pkg/preflight/kube.go`. The interface lists exactly the methods preflight
calls (ServerVersion, CanI, CreateNamespace, DeleteNamespace,
CreatePullPod, PodPullStatus). The default production implementation
wraps client-go directly via `newClientGoKubeClient`, also in the
preflight package.

`pkg/chaos` and `pkg/provisioners/kubectl` are not imported.

## Consequences

- Tests inject a fake `KubeClient` (`pkg/preflight/fake_kube_test.go`)
  that satisfies only the methods preflight uses. This keeps the test
  surface small and avoids dragging in chaos / kubectl test fixtures.
- The interface evolves with preflight's needs, not with the broader
  cluster client. Adding a method to `chaos.KubeClient` does not
  destabilise preflight tests.
- Two slightly different client wrappers exist in the codebase. The cost
  is small: each wrapper is thin (≈100 lines) and maintained by its
  consuming package's tests. The repo invariant is the load-bearing
  rationale; without it, consolidating into a single `pkg/kube` would be
  a reasonable alternative.

If a future refactor consolidates Kubernetes access into a single
package, this ADR would be superseded; that refactor would need to
carry the trade-off (tighter coupling between consumers) explicitly.
