# 0011. Post-failure cleanup and registry probe run asynchronously on the image-pull path

- Date: 2026-05-11
- Status: accepted

## Context

The typed-error fast-fail work in ADR 0008 cut the inner detection loop from
"wait 60s for kubectl rollout" to "watcher fires within ~3s of kubelet's
event." But the end-to-end latency seen by a client of `/v1/provision`
against an unreachable image was still ~36s on a real lab. A manual repro
with an unrouted RFC 1918 image surfaced this breakdown:

```
 0s          provision request received
~25s        kubelet emits ErrImagePull (containerd TCP retries against unrouted host)
~26s        watcher detects, "image pull failure detected" WARN fires
~26-31s     preflight.ProbeImage runs from the petri host, "registry probe
            result" WARN fires after its ~5s HTTP-client timeout
~31s        watcher returns typed *ErrImagePullFailure
~31-36s     Provision calls kube.DeleteNamespace synchronously; blocks ~5s
            waiting for the kube API to ack namespace finalization
~36s        HTTP 502 returned with structured body
```

Three of the four phases are dominated by external timeouts petri does not
control (kubelet/containerd, the registry's own HTTPS timeout, kube
namespace finalization). Of those, two — the probe and the cleanup — are
happening **after** petri already has all the information it needs to
populate the response. They were serialized in front of the response by
accident of how the code grew, not because anything in the response shape
depended on them.

## Decision

On the `waitForHealthyDeployments` failure path, both the registry probe
and the namespace cleanup move into tracked background goroutines. The
HTTP response returns the moment the watcher's typed error is in hand.

Specifically:

1. **`pkg/oasis/pull_watcher.go`** — When the pod-event watcher detects a
   pull failure, it schedules the registry probe via `p.tasks.Go(...)` and
   returns the typed `*ErrImagePullFailure` immediately. The probe runs in
   the background under a detached context bounded by
   `registryProbeTimeout` (30s). Its "registry probe result" WARN log
   line still fires; it just arrives in run.log **after** the
   corresponding "deployment rollout failed" + HTTP-502 lines.

2. **`pkg/oasis/provider.go`** — On the `waitForHealthyDeployments` error
   path (both `*ErrImagePullFailure` and `*ErrRolloutTimeout` shapes), the
   namespace delete is dispatched via `scheduleNamespaceCleanup` to the
   same tracked goroutine pool. The function returns the wrapped error
   without waiting. The new background goroutine uses a detached context
   bounded by `asyncCleanupTimeout` (60s).

3. **`pkg/oasis/server.go`** — Graceful shutdown waits for in-flight async
   tasks. After `http.Server.Shutdown`, `drainAsyncTasks` runs the
   provider's `WaitAsyncTasks(ctx)` with a 30s budget and logs the
   outcome (clean drain vs. abandonment) so operators reading run.log
   after a SIGTERM can tell whether cleanups landed.

The other `_ = p.kube.DeleteNamespace(ctx, namespace)` call sites in
Provision — referenced-namespace pre-creation, precondition state
injection, agent RBAC setup — keep their synchronous cleanup. The reasons
are per-site:

- **Setup-phase failures** happen before any kubelet-side latency
  accumulates; the response is already fast and operators expect the
  namespace to be gone by the time the 500 lands. There is no latency to
  hide.
- **Precondition state injection** failures may matter for response
  semantics in a way the image-pull case does not: a client may want to
  introspect the partial state that landed. Tearing it down synchronously
  preserves the historical contract.

The async treatment is therefore deliberately scoped to the one path
where (a) the typed error is fully formed before cleanup starts and (b)
the kube call is genuinely slow on the failure path.

### Why not apply uniformly

A blanket "all cleanups are async" change would have been the same lines
of code, but it bundles three distinct trade-offs into one decision:
latency vs. response-semantics vs. operator-visible inversion of log
ordering. The other cleanup sites are not on the latency-critical path,
and changing their semantics without evidence that anyone needs that
change is premature. The fast-fail path is the only path with measured
client-visible waste; that is the one we are fixing.

## Consequences

- The image-pull-failure HTTP 502 returns in well under a second after
  the watcher's typed error, down from ~10s. The probe and the cleanup
  still happen — just in the background.
- Operators reading run.log will sometimes see the registry-probe log
  line and the namespace-deletion log line arrive **after** the
  corresponding HTTP 502 response log. This is correct but unfamiliar.
  Operators investigating an image-pull failure should look forward
  ~30s in the log for the "registry probe result" and "async cleanup:
  namespace deletion succeeded" lines rather than expecting them inline.
- The new log lines are:
  - `msg="async cleanup: deleting namespace after provision failure"`
    (INFO, fired synchronously when the goroutine is scheduled)
  - `msg="async cleanup: namespace deletion succeeded"` (INFO, fired
    after the kube call returns nil)
  - `msg="async cleanup: namespace deletion failed"` (WARN, with `error`
    attr carrying the underlying message)
  - `msg="async cleanup: namespace deletion timed out"` (WARN, when the
    60s budget elapses)
  - `msg="image pull failure: registry probe abandoned"` (WARN, when the
    30s probe budget elapses)
- The HTTP 502 response body shape and the existing log lines that
  run.log parsers grep for (`"image pull failure detected"`,
  `"image pull failure: registry probe result"`,
  `"deployment rollout failed"`) are byte-identical to before, modulo
  timestamps.
- Graceful-shutdown contract: petri serve waits up to 30s for in-flight
  async tasks after `http.Server.Shutdown` returns. The server logs
  `"async tasks drained cleanly on shutdown"` or
  `"async tasks did not drain within shutdown budget; abandoning"` so
  the operator can correlate with whatever cleanup may or may not have
  finished. A SIGTERM that fires immediately after an HTTP 502 will
  normally land within the 30s budget; a SIGTERM during a stalled kube
  API will exit the process with cleanups abandoned (the namespace
  remains on the cluster and the operator must `kubectl delete ns` it,
  or the next petri serve run will reuse it for a future scenario).
- Panic safety: every tracked goroutine runs under a `recover()` in
  `asyncTasks.Go`. A panic in cleanup or in the probe library never
  crashes petri serve; it logs at ERROR with the task label and the
  recovered value.
- The `probeImage` function is injectable on `petriProvider` so unit
  tests can exercise the async-dispatch contract without making real
  HTTP calls. Production wiring uses `preflight.ProbeImage` with
  `http.DefaultClient`.
