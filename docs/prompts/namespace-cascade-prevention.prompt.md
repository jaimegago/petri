Goal: when an OASIS scenario's teardown leaves a namespace stuck in Terminating state, petri must (a) not allow the cascade where every subsequent scenario reusing the same namespace fails confusingly, and (b) surface the conflict as a typed error with the right HTTP status so oasisctl can recognize it and behave sensibly.

Background: a recent joe-oasis-e2e (private repository) run produced a cascade of 8 scenario failures, all in the same shape: a prior scenario's namespace (oasis-infra-ca) was stuck in Terminating because its teardown's `kubectl delete namespace --timeout 30s` got killed without completing. Every subsequent scenario tried to apply state into oasis-infra-ca and got back "namespace is being terminated" errors wrapped in petri's generic state-application error chain, surfacing as HTTP 500. oasisctl saw 500s but reused the same namespace name across scenarios, so the cascade rolled until the namespace finally finished terminating naturally. The root cause was a single upstream failure (a scenario template referencing a non-existent image), but the cascade amplified one bug into eight failures.

Read pkg/oasis/provider.go (Provision flow, especially the early state-application stage), pkg/oasis/server.go (HTTP error mapping), pkg/oasis/errors.go (typed error definitions added in the typed-image-pull-failures work), pkg/provisioners/kubectl/kubectl.go (the kubectl wrapper, including the delete-namespace path), and the existing teardown handler at /v1/teardown.

This fix has two parts: prevent the cascade from starting, and contain the damage if it does start.

Part 1 — Pre-check namespace state at the start of Provision (fast common-case detection)
- Before applying any state entries to the target namespace, query kube for the namespace's current Phase. If Phase is Terminating, return a new typed error ErrNamespaceTerminating immediately rather than attempting `kubectl apply` and failing partway through.
- The pre-check is a single GET against the kube API. If the namespace does not exist (404), proceed as today — Provision will create it.
- The pre-check applies to the namespace that Provision is about to use. If multiple namespaces are involved (rare for OASIS scenarios but possible), check each one.

Part 2 — Late-detection in the manifest-apply path (defensive coverage)
- The current `applying precondition state: ...` error chain wraps kubectl's "namespace is being terminated" message into a generic 500 response. Detect this specific kubectl failure mode by error-string match against the kubectl stderr (the message "namespace ... because it is being terminated" is stable across recent kubernetes versions), and convert it to the same typed ErrNamespaceTerminating used in Part 1.
- This catches the case where the namespace was Active at the start of Provision but got terminated by some other actor during the apply. Less common than the Part 1 case but worth defending against.

Part 3 — Typed error definition and HTTP mapping
- Define ErrNamespaceTerminating in pkg/oasis/errors.go alongside ErrImagePullFailure and ErrRolloutTimeout. Required fields: Namespace (string). The Error() string should be stable and grep-friendly: "namespace %s is terminating; reuse will fail until termination completes".
- Map ErrNamespaceTerminating to HTTP 409 Conflict in /v1/provision. The semantic is correct (resource state conflict, retryable after the conflict clears) and gives oasisctl an unambiguous signal distinct from 500 (server error) and 502 (upstream registry).
- The HTTP response body must include a structured field "namespace" and a "retry_after_seconds" field with a reasonable estimate (e.g. 30 — typical namespace termination time). oasisctl is free to use or ignore the hint.

Part 4 — Teardown must do better than returning 500 on namespace-delete timeout
- Today, `/v1/teardown` calls kubectl delete namespace with --timeout 30s and returns 500 when kubectl gets killed past that timeout. This is misleading: from petri's perspective, kubectl was killed; from kubernetes' perspective, the namespace is still terminating and will eventually complete. Returning 500 implies "this failed and won't fix itself" when the truth is "this is slow; it's still in progress."
- Fix: when kubectl delete namespace times out at the petri-side --timeout, do not return 500 immediately. Instead:
  1. Verify the namespace is at least in Terminating state (single GET).
  2. If it is, return 202 Accepted with a structured response indicating the teardown is in progress and providing a retry hint.
  3. If it is not (i.e. the delete actually failed for a different reason), return 500 with the original error.
- Define ErrTeardownInProgress as a typed error with Namespace (string) and EstimatedRemainingSeconds (int, e.g. 30). The HTTP handler maps it to 202.
- Note: 202 Accepted is the right semantic for "asynchronous operation in progress." oasisctl should treat 202 as "wait, then either re-check or move on with the understanding that the namespace is not yet available for reuse." This is a behavior change in the petri-oasisctl contract that needs documenting (see Documentation section below).

Part 5 — Concurrency safety on the in-flight teardown
- The async cleanup work (ADR 0011) already runs namespace deletion in a background goroutine on the image-pull-failure path. Audit whether that path and the new ErrTeardownInProgress path can produce conflicting in-flight deletes for the same namespace.
- If they can, add a simple in-process registry keyed by namespace name that tracks "we have a teardown in flight for this namespace." Both the async cleanup path and the /v1/teardown path consult and update this registry. A new /v1/teardown call for a namespace already in the registry returns ErrTeardownInProgress immediately without spawning a duplicate kubectl invocation.
- The registry must be cleaned up when teardowns complete (success or terminal failure). Use the existing asyncTasks coordinator's lifecycle hooks if possible.

Tests
- Unit test: Provision against a namespace that already has Phase=Terminating returns ErrNamespaceTerminating within ~100ms (no kubectl-apply attempted). The HTTP handler returns 409 with the structured response shape.
- Unit test: Provision against a namespace that transitions from Active to Terminating during state application surfaces the kubectl stderr match and returns ErrNamespaceTerminating (via Part 2's late-detection path). Test by mocking the kube client to return the namespace as Active on the pre-check but having ApplyYAML return the "is being terminated" error.
- Unit test: Teardown against a namespace where kubectl delete times out but the namespace is genuinely in Terminating state returns 202 with the typed ErrTeardownInProgress shape, not 500.
- Unit test: Teardown against a namespace where kubectl delete fails for a different reason (e.g. RBAC denied) still returns 500 with the original error.
- Unit test: Two concurrent /v1/teardown calls for the same namespace — the second one returns ErrTeardownInProgress immediately without invoking kubectl. Test using a mock kube client whose delete-namespace call blocks until released by the test.
- Integration-style: regression test that simulates the original cascade — call /v1/teardown for namespace N1 (which times out and returns 202), then immediately call /v1/provision targeting N1 (which returns 409). Both responses must surface their typed errors cleanly; neither should produce a 500.
- Existing tests: any test that asserted teardown returns 500 on timeout must be updated to reflect the new 202 behavior. Run go test ./... and audit failures.

Documentation
- New ADR documenting the design:
  - Why 409 for ErrNamespaceTerminating (conflict semantics, oasisctl-friendly distinct status code)
  - Why 202 for ErrTeardownInProgress (asynchronous operation in progress, more honest than 500)
  - Why both a pre-check (Part 1) and a late-detection (Part 2) exist — defense in depth
  - Why the in-process namespace-teardown registry exists (Part 5) and what its lifecycle is
  - The behavior change in the petri-oasisctl contract: oasisctl must now interpret 202 from /v1/teardown as "in progress, namespace not yet available for reuse"
- Update docs/troubleshooting.md with:
  - The new 409 and 202 response shapes and their meanings
  - How operators reading run.log can distinguish "namespace cascade prevented" (good: 409s appear, no cascade follows) from "real petri bug" (500s appear)
  - The diagnostic signature: a 202 from /v1/teardown followed by a 409 from a subsequent /v1/provision against the same namespace is the cascade-prevention working as designed

Cross-cutting requirements
- go fmt ./... && go vet ./... && go test ./... && go test -race ./pkg/oasis all clean.
- The existing typed errors (ErrImagePullFailure, ErrRolloutTimeout) and their HTTP mappings are unchanged.
- The existing "deployment rollout failed" and "image pull failure detected" WARN log lines are unchanged. Two new structured log lines: msg="namespace pre-check: terminating" with field {namespace} when Part 1 fires, and msg="teardown in progress: returning 202" with fields {namespace, kubectl_duration_ms} when Part 4 fires.
- No new external dependencies.

Archival
- Save this prompt as docs/prompts/namespace-cascade-prevention.prompt.md.

Acceptance criteria
- Manual repro on a real lab: provision a scenario that fails (use the unrouted-RFC1918 image from prior manual repros), let teardown time out, then immediately provision a second scenario targeting the same namespace. The second provision returns 409 within ~100ms with the structured ErrNamespaceTerminating response. The teardown returned 202.
- A regression test reproduces the 8-scenario cascade from joe-oasis-e2e (private repository) run 20260511-100019-e12f38 in mock form (sequence of provision/teardown calls) and asserts no 500s appear in the simulated petri responses.
- The run.log produced by the regression test contains exactly one "namespace pre-check: terminating" line per cascaded provision attempt and exactly one "teardown in progress: returning 202" line per cascaded teardown.
- oasisctl, when run against the patched petri, sees 409s and 202s instead of 500s for the same failure pattern. (This part is verified manually by running joe-oasis-e2e (private repository) and inspecting the verdict; oasisctl-side interpretation of 409/202 may need its own follow-up if oasisctl currently treats them poorly.)
