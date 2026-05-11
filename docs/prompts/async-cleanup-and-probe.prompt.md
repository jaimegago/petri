# Prompt: Make post-detection registry probe and namespace cleanup async on the image-pull failure path

Generated: 2026-05-11
Model: claude-opus-4-7 (1M context)
Target:
- pkg/oasis/async.go (new)
- pkg/oasis/pull_watcher.go
- pkg/oasis/provider.go
- pkg/oasis/server.go
- pkg/oasis/async_test.go (new)
- pkg/oasis/mock_test.go
- pkg/oasis/provider_test.go
- docs/decisions/0011-async-cleanup-and-probe.md (new)
- docs/troubleshooting.md
- docs/prompts/async-cleanup-and-probe.prompt.md

## Specification

Goal: cut petri's post-detection response latency on the image-pull failure path from ~10s to sub-second by making two synchronous-on-failure steps run asynchronously: the registry probe (currently adds ~5s) and the namespace cleanup (currently adds ~5s). The HTTP 502 response must return to the client immediately once the watcher's typed error is in hand. The probe still runs and still logs its result; the namespace still gets cleaned up; both just happen after the response has been sent.

Background: a recent manual repro on a real lab against an unrouted RFC1918 image showed the following timing breakdown (numbers approximate, log timestamps in /tmp/petri-serve.log):

  0s          provision request received
  ~25s        kubelet emits ErrImagePull (containerd i/o timeout against unreachable host)
  ~26s        petri watcher detects the failure, fires "image pull failure detected" WARN log
  ~26-31s     ProbeImage helper runs from petri host, fires "image pull failure: registry probe result" WARN log after its own ~5s HTTP timeout
  ~31s        watcher returns typed ErrImagePullFailure, "deployment rollout failed" WARN log fires
  ~31-36s     waitForHealthyDeployments' caller in Provision calls p.kube.DeleteNamespace synchronously to clean up the failed namespace; this blocks ~5s waiting for the kube API to confirm namespace deletion
  ~36s        HTTP 502 returned with structured response body

Three of the four phases are dominated by external timeouts that petri cannot control (kubelet's containerd timeout, the registry's own HTTP timeout, the kube API's namespace deletion latency). Of those, two are happening AFTER petri already has all the information it needs to respond to the client. That's the optimization.

Read pkg/oasis/provider.go (Provision and waitForHealthyDeployments) and pkg/oasis/pull_watcher.go in full before making changes.

Item 1 — Make the registry probe asynchronous
Current behavior: when the watcher detects an image-pull failure, it synchronously calls preflight.ProbeImage to confirm registry reachability from the petri host. The probe takes ~5s on unreachable hosts (its own HTTP client timeout). Only after the probe returns does the watcher return its typed error.
Why this is wrong: the typed error is fully populated the moment the watcher detects the kubelet event. The probe adds diagnostic value (a "registry probe result" WARN log line) but is not load-bearing for the error decision or the response body. Blocking the response on the probe means clients wait 5s longer for no semantic benefit.
Fix:
- Move the ProbeImage call into a background goroutine that the watcher kicks off immediately upon detecting a pull failure. The watcher returns its typed error without waiting.
- The background goroutine logs the "image pull failure: registry probe result" WARN line when it completes. This happens after the HTTP 502 has been sent, but operators reading run.log still see both diagnostic lines (the detection line and the probe result) in chronological order.
- The background goroutine must respect a bounded timeout (use a 30s ctx.WithTimeout, decoupled from the request ctx so the probe outlives the HTTP response). If the probe doesn't complete within 30s, log a single WARN line indicating the probe was abandoned and exit.
- Use a package-level sync.WaitGroup or a small registry of in-flight probes so that on graceful shutdown of petri serve, in-flight probes either complete or are abandoned cleanly. Don't let probes leak indefinitely on SIGTERM.
- The background goroutine must not panic the process if the probe library returns unexpected errors; wrap its logic in a recover().

Item 2 — Make the namespace cleanup asynchronous on the image-pull failure path
Current behavior: Provision in pkg/oasis/provider.go calls `_ = p.kube.DeleteNamespace(ctx, namespace)` synchronously before returning the error to the HTTP handler. This blocks ~5s on average for namespaces containing failed pods.
Why this is wrong: cleanup is housekeeping, not part of the error contract with the client. The 502 response is fully formed once the typed error is in hand. The client doesn't need (and shouldn't wait for) cleanup confirmation to act on the failure.
Fix:
- On the waitForHealthyDeployments error path specifically (NOT on other error paths in Provision, which may have different semantics — model should audit each existing `_ = p.kube.DeleteNamespace(ctx, namespace)` site and decide case-by-case), kick off the namespace deletion in a background goroutine.
- The background cleanup goroutine must:
  - Use a fresh ctx.WithTimeout (60s is reasonable) decoupled from the request ctx so cleanup outlives the HTTP response.
  - Log a single INFO line when cleanup starts: msg="async cleanup: deleting namespace after provision failure" with fields {namespace, env_id, reason}.
  - Log a single INFO or WARN line when cleanup completes or times out: msg="async cleanup: namespace deletion succeeded" / "...timed out" / "...failed" with fields {namespace, duration_ms, error (if applicable)}.
  - Wrap its logic in a recover() so a panic in cleanup doesn't crash petri serve.
  - Coordinate with the same shutdown waitgroup as item 1 — petri serve graceful shutdown should wait for in-flight cleanups up to a bounded period (say 30s) before forcing exit.
- The Provision function returns immediately after handing cleanup to the goroutine. The wrapped ErrImagePullFailure is returned unchanged; the response shape is identical to today.

Item 3 — Audit other Provision error paths for similar sync-cleanup waste
Look at the other `_ = p.kube.DeleteNamespace(ctx, namespace)` call sites in Provision (the ones for "creating referenced namespace", "applying precondition state", "setting up agent RBAC"). For each, decide whether the same async treatment is warranted. Some of these may be different — for instance, if "applying precondition state" fails partway through, the partially-applied state may matter for the response semantics in a way that the image-pull case doesn't.
Default position: only apply the async treatment to the waitForHealthyDeployments path for this PR. For the others, add a code comment at each site noting the trade-off and reference an ADR to be created (item 5 below) that explains why we chose the per-site default.
Do not apply async cleanup to the success path. The current code doesn't have one there anyway, but be explicit in the implementation that the new background goroutine is only spawned on failure.

Item 4 — Tests
- New unit test: waitForRolloutWithFastFail returns the typed pull error within 100ms of the watcher firing, even when ProbeImage is configured to take 5+ seconds. This guards against future regressions where someone re-introduces a synchronous probe call.
- New unit test: Provision returns its HTTP-mappable error within 100ms of waitForHealthyDeployments returning, even when the kube client's DeleteNamespace is configured to take 5+ seconds. Uses a mockKubeClient with a controllable delay on DeleteNamespace (the existing mock_test.go infrastructure already has the pattern; extend it).
- New unit test: after Provision returns on the error path, the namespace deletion goroutine eventually fires and logs success (use a test slog handler that captures records, assert the "async cleanup: namespace deletion succeeded" log line appears within a reasonable bound).
- New unit test: after Provision returns on the error path, if the cleanup goroutine's DeleteNamespace fails, the failure is logged at WARN with the error. (Mock kube client returns an error from DeleteNamespace; assert log line.)
- New unit test: graceful-shutdown coordination — when the shutdown waitgroup is waited on, in-flight cleanup/probe goroutines either complete or are abandoned within the bounded period. (This may require exposing a small shutdown helper from the pkg/oasis layer; model decides the cleanest shape.)
- Existing tests must continue to pass without modification.

Item 5 — ADR
- Add an ADR to docs/decisions/ documenting the decision to make cleanup and probe async on the image-pull-failure path. Capture:
  - Why blocking the response on cleanup and probe is wasted client wait time (the response body is fully formed before either runs).
  - Why we limit the async treatment to the waitForHealthyDeployments path for this PR rather than applying it uniformly to all Provision error paths.
  - The graceful-shutdown contract: petri serve waits up to N seconds for in-flight async work on shutdown before forcing exit.
  - The trade-off: async cleanup means the operator's run.log will sometimes show namespace-deletion logs arriving AFTER the corresponding HTTP response log, which is unfamiliar but correct. Make this explicit so future readers don't think it's a bug.

Item 6 — Documentation
- Update docs/troubleshooting.md to note the new async log lines ("async cleanup: ...") and that they may appear after the corresponding HTTP 502 response. Operators investigating a failure should look for the cleanup confirmation in the next ~30 seconds of logs rather than expecting it inline.

Cross-cutting requirements
- go fmt ./... && go vet ./... && go test ./... clean.
- The "image pull failure detected" WARN log fires synchronously (unchanged). The "image pull failure: registry probe result" WARN log now fires asynchronously and may arrive after the HTTP response log. The "deployment rollout failed" WARN log continues to fire on the synchronous path before the response.
- The HTTP 502 response body is unchanged. The "deployment rollout failed" log line text is unchanged. Existing run.log parsers that grep for these continue to work.
- No new external dependencies. Use stdlib sync.WaitGroup or a single channel-based coordinator; no new libraries.

Archival
- Save this prompt as docs/prompts/async-cleanup-and-probe.prompt.md. Match the existing naming pattern.

Acceptance criteria
- Manual repro on a real lab against the unrouted-RFC1918 image: the gap between the "image pull failure detected" WARN log and the HTTP 502 response log shrinks from ~10s to well under 1s.
- The "image pull failure: registry probe result" WARN log still appears in /tmp/petri-serve.log, but now AFTER the HTTP 502 response log, within ~10s.
- The "async cleanup: namespace deletion succeeded" INFO log appears in /tmp/petri-serve.log within ~30s of the HTTP 502 response.
- The HTTP 502 response body and the existing log lines are byte-identical to before (modulo timestamps).
- Killing petri serve with SIGTERM mid-flight waits for in-flight async work up to the bounded period before exiting, and logs the wait outcome.
