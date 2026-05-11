# Prompt: Typed image-pull failures + fast-fail pod-event watcher

Generated: 2026-05-10
Model: claude-opus-4-7 (1M context)
Target:
- pkg/oasis/provider.go
- pkg/oasis/server.go
- pkg/oasis/types.go
- pkg/oasis/errors.go (new)
- pkg/oasis/pull_watcher.go (new)
- pkg/oasis/pull_watcher_test.go (new)
- pkg/oasis/server_test.go
- pkg/oasis/mock_test.go
- pkg/preflight/probe.go (new — ProbeImage helper)
- docs/decisions/0008-typed-image-pull-failures-502.md (new)
- docs/decisions/0009-probeimage-public-helper.md (new)
- docs/decisions/0010-defer-parallel-rollout-waits.md (new)
- docs/troubleshooting.md
- CLAUDE.md
- docs/prompts/typed-image-pull-failures.prompt.md

## Specification

Goal: when an OASIS scenario's deployment fails to roll out because of an image-pull problem (registry unreachable, image not found, auth failed, manifest unparseable), petri must surface that as a categorically different error from a generic readiness timeout. Today both collapse into "deployments did not become ready within 1m0s", which gives evaluators no way to tell substrate problems from real platform or scenario issues. Petri must also fail-fast: detect the image-pull failure as soon as kubelet emits the corresponding event, not after the full rollout timeout has elapsed.

Read pkg/oasis/provider.go (deployment-wait code around line 670–685), pkg/oasis/server.go (the /v1/provision handler at line 96–108), pkg/provisioners/kubectl/kubectl.go (rollout-status implementation around line 91–110), pkg/preflight/registry.go (the registry probing helper from the verify work), and pkg/preflight/checks.go (how the image checks are wired) before making changes. The verify work in pkg/preflight introduced a registry-side probe that classifies failures into TCP-level vs HTTP-level; this prompt explicitly reuses that helper.

Reuse vs. duplication
- pkg/preflight/registry.go contains a manifest+blob HEAD probe with TCP-vs-HTTP error classification, multi-arch manifest list resolution, and R2-aware diagnostics. The fast-fail logic in this prompt must call into that helper, not reimplement it. If the helper currently sits at a visibility level that doesn't allow cross-package consumption (e.g. unexported types), expose a minimal API surface — a single ProbeImage(ctx, ref, httpClient) function returning a typed result is enough. Do not lift the entire pkg/preflight package; keep its internals private.
- The pod-event watching logic (the new piece this prompt adds) is petri-cluster-side, not registry-side, and belongs in petri's provisioner layer. Likely home: a new helper in pkg/provisioners/kubectl or a sibling file under pkg/oasis. Model decides.

Bug 1 — Detect image-pull failures via pod events and short-circuit the rollout wait
Today: pkg/oasis/provider.go calls p.kube.WaitForRollout(ctx, namespace, deployment, 60*time.Second) sequentially per deployment. The wait is implemented as `kubectl rollout status --timeout 1m` and only knows about the rollout's progress, not pod-event reasons. When kubelet hits ImagePullBackOff at second 2, petri still waits the full 60 seconds before returning a generic timeout error.
Fix: add a pod-event watcher that runs concurrently with the rollout-status wait. The watcher polls (or uses a list-watch on) pod events for pods owned by the deployment's current ReplicaSet, looking for these signals:
- Container status `waiting` with reason in: ImagePullBackOff, ErrImagePull, ErrImageNeverPull, RegistryUnavailable, InvalidImageName, CreateContainerConfigError (the last is a catch-all that often manifests as image config errors)
- Pod events with reason in: Failed, FailedPullImage, BackOff, with a message containing "pull" or "image"
The watcher polls every ~3 seconds (the typical kubelet event-flush cadence). If any signal fires, it returns immediately with a typed image-pull error containing: image reference, namespace, pod name, the kubelet reason string, the kubelet message string. The rollout-status wait is cancelled.
The watcher must distinguish image-pull failures from other transient pod problems. CrashLoopBackOff, ContainerCreating (without image-pull errors), Pending due to scheduling — these are NOT image-pull failures and the watcher must not short-circuit on them.
Concurrency: kubectl rollout status and the pod-event watcher run concurrently using errgroup or equivalent. First to return wins. If the watcher fires first, cancel the rollout-status command's context. If the rollout completes successfully first, cancel the watcher.

Bug 2 — Define typed errors and propagate them through the HTTP layer
Today: any error from Provision in the OASIS server returns HTTP 500 (pkg/oasis/server.go:96–108 hardcodes http.StatusInternalServerError). The handler does not differentiate cause.
Fix: define typed errors in pkg/oasis (or wherever the cleanest home is — model decides; the verify package's pattern of typed sentinel errors is a reasonable reference). Required errors:
- ErrImagePullFailure — wraps the kubelet-reported reason and registry probe result. Constructors must accept (image, namespace, pod, reason, message) at minimum.
- ErrRolloutTimeout — the existing "deployment did not become ready" failure mode, now formalized as a typed error.
- These errors must implement standard interfaces: errors.Is for sentinel matching, errors.As for unwrapping, and an Error() method that produces a stable, log-grep-friendly string (the existing "deployments did not become ready within %s: %s" format is fine for ErrRolloutTimeout; ErrImagePullFailure needs its own format that includes image ref and reason).
Then update the /v1/provision handler:
- ErrImagePullFailure → HTTP 502 (Bad Gateway). Petri is acting as a proxy to the registry, which is the unreachable upstream. This is semantically correct and lets oasisctl distinguish "petri itself is broken" from "petri couldn't reach a dependency."
- ErrRolloutTimeout → HTTP 500 (current behavior, now explicit).
- Any other provider error → HTTP 500 (current behavior, unchanged).
- The error response body for ErrImagePullFailure must include the structured fields (image, namespace, pod, reason, message) as JSON, not just a flat error string. Extend writeError or add a writeStructuredError helper that takes a typed error and serializes its fields. The existing {"status":"error","message":"..."} envelope is preserved; new fields go alongside.
Symmetry note: the verify HTTP behavior is irrelevant here — preflight runs in-process, not over HTTP. This is purely about /v1/provision.

Bug 3 — Image-pull failures must produce a structured petri log line
Today: when a deployment fails to roll out, petri logs "deployment rollout failed" with the deployment name and the kubectl error. Image-pull problems are buried inside that string.
Fix: before/during the wait, if the pod-event watcher detects an image-pull failure, emit a new structured log line at WARN level: msg="image pull failure detected" with fields {deployment, namespace, image, pod_name, reason (kubelet reason string), message (kubelet message string)}. This is in addition to the existing "deployment rollout failed" line, which stays for backward compatibility with whatever parses run.log today.
For maximum diagnostic value: when an image-pull failure is detected, also call into pkg/preflight's ProbeImage helper to do an immediate registry-side probe of the same image and include its result (TCP-fail / HTTP-fail / pass) in a follow-up structured log line msg="image pull failure: registry probe result" with fields {image, probe_outcome, probe_detail}. This gives the operator an immediate answer to "is the registry itself unreachable, or is this kubelet-specific?" without needing to manually re-run verify.

Bug 4 — Optional: parallel rollout waits
Today: pkg/oasis/provider.go iterates over pending deployments sequentially, so N deployments × 60s timeout = total wait. A scenario like infra.safety.be.zone-violation-001 with two deployments takes ~120s total, exceeding oasisctl's client-side request timeout.
Decision: scope this prompt to the image-pull fast-fail and typed-error work. Defer the parallel-wait change to a separate prompt (it was originally listed as prompt #3 in the planning conversation). Mention this explicitly so the model doesn't try to do both. Rationale: parallel waits change the semantics of error aggregation across multiple deployments, and conflating that change with the typed-error introduction makes both harder to review.
However: ensure the fast-fail watcher works correctly with future parallel waits. Specifically, the watcher must scope its pod-event polling to a single deployment's pods (filter by ownerReferences or label selector), not "all pods in the namespace." This way, when parallel waits land later, the watchers don't cross-contaminate.

Cross-cutting requirements
- The existing "deployment rollout failed" WARN log (with the kubectl error string) must continue to fire for backward-compat with run.log parsers.
- The existing "deployments did not become ready within 1m0s" error string is preserved verbatim (now produced by ErrRolloutTimeout.Error()).
- The hardcoded const rolloutTimeout = 60 * time.Second at provider.go:639 stays at 60s for now. Making it configurable is out of scope here; capture it as a docs/backlog/ item if there isn't already one.
- The pod-event watcher must respect ctx cancellation. If the surrounding request is cancelled (e.g. oasisctl client timeout), the watcher must stop within ~3s of cancellation, not hold goroutines.
- All new code paths must be exercised by unit tests using fake kube clients. The existing fake-kube test infrastructure in pkg/preflight (fake_kube_test.go) is a reasonable reference for how to build a fake here, though that fake is preflight-specific; the OASIS-side fake will need additional capabilities (pod-list, pod-watch).

Tests
- Unit test: deployment with an unreachable image (pod-event watcher reports ImagePullBackOff at ~3s into the wait). Assert ErrImagePullFailure is returned within 10s, not 60s. Assert the typed error contains the right fields.
- Unit test: deployment with a real readiness problem (pod is Running but failing liveness probe — no image-pull events fire). Assert ErrRolloutTimeout is returned after the full 60s, not short-circuited.
- Unit test: deployment that succeeds normally. Assert no error, no log noise from the watcher.
- Unit test: HTTP layer round-trip — provider returns ErrImagePullFailure, the /v1/provision handler returns 502 with the structured fields in the response body.
- Unit test: HTTP layer round-trip — provider returns ErrRolloutTimeout, handler returns 500 with the existing string format.
- Unit test: ctx cancellation during a watch — cancelling the parent context stops the watcher within 3s.
- Integration test (or documented manual repro): point petri at a kind cluster, provision a scenario whose deployment references an image at a hostname that resolves but doesn't accept connections (e.g. an unrouted RFC1918 address like 10.255.255.1/32). Confirm petri logs the "image pull failure detected" + "registry probe result" pair within ~10s and returns 502 from /v1/provision.

Archival
- Save this prompt as docs/prompts/typed-image-pull-failures.prompt.md. Match the existing naming pattern (<slug>.prompt.md).

ADRs
- Add an ADR documenting the choice of HTTP 502 for ErrImagePullFailure (vs 500 for ErrRolloutTimeout). Capture the "petri is acting as a proxy to the registry; registry-unreachable is an upstream-gateway failure" reasoning.
- Add an ADR documenting the ProbeImage helper exposed from pkg/preflight for cross-package consumption. Capture the rule: petri's substrate-probing logic has exactly one home (pkg/preflight); the typed-error fast-fail path consumes it via a narrow, explicit API.
- Add an ADR documenting why parallel rollout waits are deferred to a separate change rather than landed alongside this one (review-burden / semantic-orthogonality reasoning).
- Each ADR follows the existing convention in docs/decisions/.

Documentation
- Update docs/troubleshooting.md to describe the new "image pull failure detected" log line and the 502 response shape, alongside the existing 500 / "deployments did not become ready" entry. Cross-reference docs/verify.md.
- Update CLAUDE.md if there's an invariant worth recording — specifically, that pkg/preflight is the single source of truth for substrate-probing logic, and any new substrate-reachability check must live there and be consumed via its public API rather than reimplemented elsewhere.

Acceptance criteria
- go fmt ./... && go vet ./... && go test ./... clean.
- Manual repro (image at unrouted address) returns 502 within 10s with structured response body and structured WARN logs.
- Existing run.log parsers (anything that greps for "deployments did not become ready within" or "deployment rollout failed") continue to find their lines.
- pkg/preflight's ProbeImage is consumed from pkg/oasis (or wherever the typed-error code lands) without lifting any other internals from pkg/preflight.
