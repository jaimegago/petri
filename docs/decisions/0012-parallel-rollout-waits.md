# 0012. Parallel rollout waits in /v1/provision

- Date: 2026-05-11
- Status: accepted

## Context

`petriProvider.waitForHealthyDeployments` previously iterated pending
deployments sequentially, calling `waitForRolloutWithFastFail` on each.
Each wait carries a 60-second rollout timeout, so a scenario like
`infra.safety.be.zone-violation-001` with two deployments could spend up
to 120 seconds waiting — past the oasisctl client-side request timeout.
The end-to-end test saw "context deadline exceeded" client errors
instead of the typed server responses petri was carefully designed to
emit. [ADR 0010](0010-defer-parallel-rollout-waits.md) deferred the fix
so the typed-error work could land cleanly first; this ADR lands the
deferred change.

With the typed-error fast-fail watcher already in place
([ADR 0008](0008-typed-image-pull-failures-502.md)), each per-deployment
wait is fully encapsulated: `waitForRolloutWithFastFail` already
coordinates a `kubectl rollout status` subprocess and a pod-event
watcher, respects context cancellation, and returns either nil,
`*ErrImagePullFailure`, or a single-deployment `*ErrRolloutTimeout`.

## Decision

Run the per-deployment waits concurrently using
`golang.org/x/sync/errgroup`, with first-error-wins semantics and a
small concurrency cap.

Specifics:

- **errgroup with `SetLimit`.** The derived context cancels the moment
  any goroutine returns a non-nil error; sibling waits exit promptly
  because `waitForRolloutWithFastFail` respects `ctx.Done()`.
- **Concurrency cap of 8 (`rolloutWaitConcurrency`).** Real OASIS
  scenarios today declare at most two Deployments
  (`infra.safety.be.zone-violation-001`); a cap of 8 covers all of them
  with headroom but prevents a hypothetical 50-deployment scenario from
  spawning 50 `kubectl rollout status` subprocesses. The number is a
  round headroom-friendly default, not the product of a benchmark.
- **First-error-wins for the HTTP response.** The errgroup returns
  whichever error landed first. If multiple deployments fail
  simultaneously, the other failures are visible in `run.log` via the
  per-deployment `"deployment rollout failed"` WARN line each goroutine
  emits before returning. We do not aggregate or wrap into a
  multi-error type.
- **`ErrRolloutTimeout` shape unchanged.** Its `Deployments` slice
  still exists and still carries `namespace/name` strings; it just
  typically contains one entry now instead of N. The `Error()` format
  is unchanged, so `run.log` parsers grepping for
  `"deployments did not become ready within"` keep working.
- **`ErrImagePullFailure` aggregation policy: none.** A scenario can in
  principle have multiple image-pull failures; with parallel waits the
  first watcher to fire wins and the others are cancelled. This is
  acceptable because (a) the watcher fires within ~10s of kubelet
  emitting the event, so "first to fire" closely tracks "first to be
  unreachable," and (b) the typed error already includes the specific
  image, namespace, pod, and reason — enough diagnostic context for
  the operator. Other broken images surface via their own
  `"image pull failure detected"` WARN lines as their watchers fire.
- **`rolloutTimeout` stays hardcoded at 60s.** Making it configurable
  is still out of scope; the `docs/backlog/` entry for that survives.

Alternatives considered and rejected:

- **Aggregating into a multi-error `*ErrRolloutTimeout` with the slice
  populated by all failures.** Tempting because the type already
  supports it, but the request handler returns one response — the
  client gets the first error regardless. Aggregating would mean
  waiting for all goroutines to complete (or settle), which defeats
  the parallel-wait latency win on the failure path. Per-deployment
  log lines already give operators visibility into sibling failures.
- **A worst-class-wins selector (e.g. always prefer
  `ErrImagePullFailure` over `ErrRolloutTimeout` even if the timeout
  was reported first).** Adds complexity for a case that does not
  occur in practice: a real pull failure fires the watcher within ~3s
  of kubelet's event, while a rollout timeout takes the full 60s.
  First-error-wins already produces the "right" classification by
  ordering.
- **Unbounded fan-out (no `SetLimit`).** Fine for today's scenario
  shapes but a footgun for future ones. The cap is cheap insurance.

## Consequences

- A scenario with N healthy-but-slow deployments now waits roughly the
  duration of the single slowest rollout, not N × that duration. This
  brings multi-deployment scenarios comfortably inside oasisctl's
  client-side timeout.
- The typed `*ErrRolloutTimeout` in a failure response now usually
  lists one deployment rather than a comma-separated list. The
  `Error()` format is preserved, so any log parser grepping for the
  historical phrasing still matches. See `docs/troubleshooting.md`.
- Per-deployment WARN log lines are the canonical record of sibling
  failures. Operators correlating a run-log slice to a 5xx response
  should expect 1 typed error in the response body but up to N WARN
  lines in `run.log`.
- The post-failure namespace cleanup path
  ([ADR 0011](0011-async-cleanup-and-probe.md)) is unchanged. The
  errgroup error becomes its input exactly as before.
- The pod-event watcher's per-deployment ReplicaSet-prefix scoping
  ([ADR 0008](0008-typed-image-pull-failures-502.md)) is now exercised
  by concurrent goroutines sharing a namespace. The prefix isolation
  was a pre-requirement for this change; this ADR is what cashes that
  in.
