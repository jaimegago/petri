# 0010. Parallel rollout waits are deferred to a separate change

- Date: 2026-05-10
- Status: accepted

## Context

`petriProvider.waitForHealthyDeployments` iterates pending deployments
sequentially, calling `kubectl rollout status --timeout=1m` on each. A
scenario like `infra.safety.be.zone-violation-001` with two deployments
takes ~120s in the worst case, which exceeds oasisctl's client-side
request timeout and breaks the end-to-end test.

The typed-image-pull-failure work in ADR 0008 fixes the most common
worst-case path (image-pull failures fast-fail in ~3 seconds rather than
sitting through 60s of waiting), but it does not address the N×60s ceiling
for scenarios with multiple healthy-but-slow deployments. Two changes were
candidates for the same PR:

1. Typed image-pull errors + fast-fail watcher (the work in ADR 0008).
2. Parallel rollout waits — run one rollout-watch goroutine per pending
   deployment, aggregate failures.

## Decision

Land the typed-error / fast-fail change first. Parallel rollout waits get
their own change, separate from this one.

The fast-fail watcher in ADR 0008 is structured so the future parallel-wait
work drops in without rework:

- Each watcher already scopes its pod-event polling to a single
  deployment (filter by `ownerReferences` → ReplicaSet name prefix). N
  concurrent watchers will not cross-contaminate.
- `ErrRolloutTimeout` already carries a slice of failed
  `namespace/deployment` identifiers, so the aggregator semantics are
  fixed in advance.
- The synchronous loop in `waitForHealthyDeployments` is the only
  serialization point; it can be lifted to an `errgroup` (or equivalent)
  without changing the per-deployment wait logic.

Rationale for splitting: parallel waits change the semantics of error
aggregation across deployments — when two deployments fail, do we return
the first error, all errors, the worst-class error? Bundling that decision
with the typed-error introduction makes both harder to review and risks
shipping a worse aggregation policy because it wasn't the focus of the
change. Doing them separately means each PR can be reasoned about on its
own and reverted independently if needed.

## Consequences

- Scenarios with N deployments × 60s timeout still risk hitting oasisctl's
  client-side timeout when none of them fail in the fast-fail-detectable
  ways. This is the trade-off: the 502 fast-path covers the common case
  (a broken substrate / wrong image), but a scenario that is genuinely
  slow to converge still takes N×60s.
- A follow-up prompt will tackle the parallel-wait change. That prompt
  must explicitly state the aggregation policy (return first failure
  fatally, or collect all and return a multi-error?) and how it interacts
  with `ErrImagePullFailure` (one watcher firing should still cancel the
  group, not just its sibling).
- The hardcoded `rolloutTimeout = 60 * time.Second` constant in
  `provider.go` is left alone; making it configurable is also part of the
  follow-up scope, not this change.
