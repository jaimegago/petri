Goal: make petri wait on multiple deployments in parallel during /v1/provision rather than sequentially. Today the wait is serialized per deployment, so a scenario with N deployments × 60s rollout timeout = up to N × 60s total. This exceeds oasisctl's client-side request timeout for any scenario with more than one deployment, producing "context deadline exceeded" client errors instead of clean typed server responses.

Background: this work was deferred from the typed-image-pull-failures change documented in ADR 0010 (docs/decisions/0010-defer-parallel-rollout-waits.md). With typed errors and the fast-fail watcher now in place, the per-deployment unit of work is well-encapsulated: each deployment runs through waitForRolloutWithFastFail, which already coordinates a kubectl rollout-status subprocess and a pod-event watcher and returns a typed error or nil. Parallelizing the loop over deployments is the remaining step.

The original observation that motivated this: infra.safety.be.zone-violation-001 declares two Deployments (payments/checkout-api and frontend/web-app). On the recent failed run, those deployments each timed out at 60s, the second only starting after the first returned, so total wait was ~120s — past oasisctl's request timeout — and the client saw "context deadline exceeded" rather than petri's typed error response. With parallel waits, total wait for that scenario shrinks to ~60s (the time of the slowest deployment), comfortably inside the client budget.

Read pkg/oasis/provider.go (waitForHealthyDeployments around line 630-686), pkg/oasis/pull_watcher.go (the per-deployment fast-fail wrapper), pkg/oasis/errors.go (ErrRolloutTimeout, ErrImagePullFailure), and pkg/oasis/async.go (the asyncTasks coordinator added recently) before making changes.

Core change — parallel waits with first-failure-wins semantics
Today: waitForHealthyDeployments builds a slice of pending deployments (Deployment kinds with spec.status: "running"), then iterates over them serially, calling p.kube.WaitForRollout (now waitForRolloutWithFastFail) one at a time. On failure, it aggregates failed deployment names into a slice and returns a single ErrRolloutTimeout listing all of them.
Fix: run the per-deployment waits concurrently. Use golang.org/x/sync/errgroup (already in go.mod per ADR 0010 — model should verify) with a derived context.
Semantics to preserve and to clarify:
- Each per-deployment wait is independent: one deployment hitting ImagePullBackOff doesn't affect the others' wait progress.
- The function returns as soon as ANY deployment fails, with that deployment's typed error. Sibling waits in flight at that moment are cancelled via the errgroup's context cancellation; they exit cleanly because waitForRolloutWithFastFail respects ctx cancellation (verified during prompt #2 manual repro).
- If multiple deployments fail simultaneously, the errgroup returns the first error it received. This is acceptable — the others will be visible in run.log via the per-deployment "deployment rollout failed" WARN line each watcher emits before returning.
- The aggregate ErrRolloutTimeout that today lists multiple deployments (e.g. "deployments did not become ready within 1m0s: payments/checkout-api, frontend/web-app") becomes a single-deployment error in the typical failure case. This is a real change in the error message format. See "Error message compatibility" below.

Error message compatibility — IMPORTANT
The current ErrRolloutTimeout message format is "deployments did not become ready within %s: %s" with a comma-separated deployment list. Several places parse run.log expecting this exact format (the original investigation conversation grepped for "deployments did not become ready within" — that string must continue to appear).
Decisions to make explicitly:
- Keep ErrRolloutTimeout's Error() string format unchanged. When only one deployment fails (the new common case with parallel waits), the list contains exactly one entry: "deployments did not become ready within 1m0s: payments/checkout-api". The grep string ("deployments did not become ready within") still matches. The Deployments slice in the typed error is still a slice; consumers using errors.As will see a one-element slice.
- The HTTP response body shape for ErrRolloutTimeout is unchanged.
- The "deployment rollout failed" per-deployment WARN log line emitted by each waitForRolloutWithFastFail is unchanged. Operators see one such line per failing deployment, in temporal order.

Image-pull failure aggregation
A scenario can in principle have multiple deployments fail with image-pull errors. Today's code returns a single ErrImagePullFailure when one such failure is detected; with parallel waits, the first one to fire wins and the others are cancelled.
Decision: keep this behavior. Returning the first-detected ErrImagePullFailure is correct because (a) the watcher fires within ~10s of kubelet emitting the event, so "first to fire" closely tracks "first to be unreachable", and (b) ErrImagePullFailure already includes the specific image, namespace, pod, and reason — sufficient diagnostic context. If multiple images are broken, the operator will see "image pull failure detected" WARN lines for each as their watchers fire (in temporal order in run.log) even though only the first becomes the HTTP response. The "image pull failure: registry probe result" async log line will also fire for each detected failure independently per the existing async path.
Do NOT add an aggregate ErrImagePullFailure type. Keep the existing typed error shape; the multi-failure case is handled by per-deployment log lines.

Concurrency bounds
Add a small concurrency cap on the errgroup to prevent pathological scenarios with many deployments from overwhelming the kube API. errgroup.SetLimit is the right tool. Cap of 8 concurrent waits is a reasonable default — covers all real OASIS scenarios (none have >8 deployments today, the most is be.zone-violation-001 at 2) with headroom, but prevents a 50-deployment future scenario from spawning 50 goroutines each running their own kubectl subprocess.
Make the cap a const at the top of the file with a comment explaining the choice and pointing to the ADR.

Tests
- New unit test: scenario with three healthy deployments → parallel wait completes in roughly the time of the slowest single rollout, not 3× that time. Use a mockKubeClient whose WaitForRollout sleeps for a configurable per-deployment duration. Assert wall-clock total is roughly max(d1, d2, d3) plus a small overhead, not sum.
- New unit test: scenario with three deployments, second one hits ImagePullBackOff while the others would have eventually succeeded. Assert: function returns the typed ErrImagePullFailure for deployment-2 within 100ms of the watcher firing (not after waiting for d1 and d3 to complete). Assert d1 and d3's waits were cancelled (their goroutines exited within a reasonable bound after the function returned).
- New unit test: scenario with two deployments, both fail with ErrRolloutTimeout simultaneously. Assert: function returns an ErrRolloutTimeout containing exactly one deployment (the first one to be reported by errgroup); the other deployment's failure is visible only via its per-deployment "deployment rollout failed" WARN log line.
- New unit test: concurrency cap is honored. Build a scenario with 16 deployments (synthetic, doesn't need to be a real OASIS scenario shape — just exercises waitForHealthyDeployments). Use a mockKubeClient that tracks max concurrent WaitForRollout invocations. Assert the observed max is ≤ 8.
- Existing tests must continue to pass. The "deployments did not become ready within" string assertion in any existing test must continue to hold for single-deployment failures.

Documentation
- Update ADR 0010 (docs/decisions/0010-defer-parallel-rollout-waits.md) — mark it as superseded by a new ADR, and add a short note pointing at the new ADR. Per the docs/decisions/README.md convention, "superseded" is a real status.
- New ADR documenting the parallel-wait design choices: errgroup with concurrency cap of 8, first-error-wins semantics, error message format preservation for grep compatibility, image-pull failure aggregation policy (none — first to fire wins, others visible via per-deployment logs). Filename pattern matches existing entries.
- Update docs/troubleshooting.md if any user-facing behavior changed beyond "things are faster" — specifically: explain that ErrRolloutTimeout error messages will commonly list a single deployment now rather than a comma-separated list, and that other deployments' failures are visible in run.log via "deployment rollout failed" lines in temporal order.

Cross-cutting requirements
- go fmt ./... && go vet ./... && go test ./... && go test -race ./pkg/oasis clean.
- The hardcoded const rolloutTimeout = 60 * time.Second at provider.go:639 stays at 60s. Making it configurable is still out of scope. If there's an existing docs/backlog/ entry for this, leave it; if not, create one.
- No new external dependencies. errgroup is in go.mod already (verify); if not, surface that and stop before adding it.
- The async cleanup path on failure (added in the async-cleanup-and-probe work) is unchanged. The errgroup error becomes the input to that path exactly as before.

Acceptance criteria
- A scenario declaring three healthy Deployments (each taking ~5s to roll out) completes provision in roughly 5-7s wall-clock, not 15+.
- A scenario declaring two Deployments where both reference unreachable images returns a typed ErrImagePullFailure within ~30s of provision start (the kubelet containerd timeout), not 60s+.
- Existing single-deployment scenarios behave identically to before in both timing and error shape.
- Manual repro on a real lab: provision a synthetic scenario with three Deployments referencing the registry.k8s.io/nginx-slim:0.27 default image. Confirm total wait time is roughly the slowest single rollout, not the sum.
- The grep string "deployments did not become ready within" continues to match in run.log on rollout-timeout failures.

Archival
- Save this prompt as docs/prompts/parallel-rollout-waits.prompt.md.
