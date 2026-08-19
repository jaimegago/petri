# 0014. Namespace-cascade prevention: 409 on terminating, 202 on teardown-in-progress

- Date: 2026-05-12
- Status: accepted

## Context

A joe-oasis-e2e (private repository) run on 2026-05-11 (run id 20260511-100019-e12f38) cascaded
a single upstream failure into eight scenario verdicts. The shape of the
cascade:

1. Scenario A's template referenced a non-existent image. Provision
   detected the pull failure (typed `*ErrImagePullFailure`, ADR 0008),
   returned 502, and async-cleanup scheduled a namespace delete (ADR
   0011).
2. Cleanup's `kubectl delete namespace --timeout 30s` outlived the
   client's wall-clock budget. From kube's perspective the deletion was
   still finalising; from petri's, the kubectl invocation was killed.
3. Subsequent scenarios reused the namespace name `oasis-infra-ca`. The
   first state-apply attempt against the half-dead namespace hit kube's
   admission response `unable to create new content in namespace X
   because it is being terminated`. Petri's injector wrapped that into
   the generic `applying precondition state: …` error chain, which the
   handler mapped to 500.
4. oasisctl saw the 500 and treated it as a real petri failure rather
   than the substrate-side conflict it actually was. It continued
   reusing the namespace. Cascade ran for 8 scenarios until the
   namespace finally finished terminating naturally.

The root cause was a single broken image. The cascade amplification was
two distinct shortcomings in the petri ↔ oasisctl contract:

- **Late detection only.** Petri only learned the namespace was
  terminating after a partial apply had already failed. There was no
  cheap pre-check.
- **Mis-typed responses.** Both the "namespace busy finalising" and the
  "teardown still in progress" cases collapsed to 500. From a status
  code alone, oasisctl could not distinguish "petri is broken" from "the
  substrate hasn't caught up yet, retry later."

## Decision

The fix has four cooperating pieces. Together they prevent the cascade
from starting (Part 1) and contain its blast radius if it does (Parts
2–5).

### Part 1 — Pre-check at the top of Provision

`Provision` calls `kube.GetNamespacePhase` once for the scenario
namespace and once per distinct referenced namespace before any
CreateNamespace or ApplyYAML happens. A `Terminating` phase returns
`*ErrNamespaceTerminating` immediately. A 404 (empty phase) is normal —
Provision proceeds and creates the namespace.

Probe-side failures (kube API blip, transport error) are non-fatal. The
pre-check must not invent failure modes that its absence would not have
triggered; on probe error we fall through and let the normal create/apply
path surface the real outcome.

### Part 2 — Late-detection inside the injector path

If the namespace was Active at the pre-check but transitions to
Terminating mid-apply (e.g. a concurrent /v1/teardown lands between the
two), the kubectl stderr "because it is being terminated" is converted
to the same `*ErrNamespaceTerminating` shape by
`terminatingNamespaceFromErr`. This is a defensive layer, not the primary
detection path. The needle "because it is being terminated" has been
stable in kube admission responses for many releases.

**Amended 2026-08-19 — the injector is not the last apply in Provision.**
As written, Part 2 covered step 3 (state injection) and stopped there.
Step 4 applies the agent's ServiceAccount / Role / RoleBinding through the
same kubectl path, *later* — so a namespace whose deletion begins after
the injector finishes is rejected there and at no earlier checkpoint. That
site had no classification, and the condition surfaced as a 500: a
retryable state reported as unrecoverable, observed three times out of
three on 2026-08-19 as

```
serviceaccounts "oasis-agent" is forbidden: unable to create new content
in namespace <ns> because it is being terminated
```

`terminatingNamespaceFromErr` now guards the agent-RBAC apply too, under
its own log line `namespace late-detected as terminating during agent RBAC
setup`. Read the smoke-signal note under "What this makes harder" as
covering both late-detection lines. Unlike the generic RBAC failure path,
the terminating branch does **not** delete the namespace synchronously —
kube is already finalising it, so the delete is at best a no-op and at
worst a blocking kubectl call charged to the client's latency. That
matches the choice Part 2 made.

**Whether the retry then belongs in petri or in the caller is still
open.** This amendment classifies; it does not retry.

### Part 3 — Typed errors and HTTP mapping

`pkg/oasis/errors.go` grows two new typed errors next to
`ErrImagePullFailure` and `ErrRolloutTimeout`:

- `ErrNamespaceTerminating{Namespace}` → HTTP **409 Conflict**. The
  semantic is "the resource is busy; retry will likely succeed once
  finalisation completes." The response body is
  `{status, message, namespace, retry_after_seconds}`. 409 was chosen
  over 503 because the conflict is *targeted* — a different namespace
  would succeed — and over 502 because petri itself is healthy.

- `ErrTeardownInProgress{Namespace, EstimatedRemainingSeconds}` → HTTP
  **202 Accepted**. 202 is the standard "operation accepted, still in
  progress" status. The response body is
  `{status, message, namespace, estimated_remaining_seconds}`. The hint
  is pinned at 30s based on observed kube finalisation times; bumping
  it should follow real operational evidence, not gut feel.

Both mappings live in `writeProvisionError` and `writeTeardownError`
(server.go). They are *not* added to `httpStatusForErr` because the
new shapes are endpoint-specific and the shared helper should remain a
narrow fallback.

### Part 4 — Teardown returns 202 on in-progress finalisation

`/v1/teardown` now calls `DeleteNamespaceWithTimeout` with a 30s
kubectl-side budget. When kubectl exits without finishing the delete,
Teardown queries `GetNamespacePhase`:

- Phase=`Terminating` → return `*ErrTeardownInProgress` (202).
- Anything else → propagate the original error (500).

The environment is *not* removed from the in-process store on the 202
path. A repeat /v1/teardown call against the same env id should see the
in-flight teardown (via the registry, Part 5) rather than 404 on the
env id.

### Part 5 — In-process teardown registry

`teardown_registry.go` adds a sync.Mutex-guarded `map[string]struct{}`
keyed by namespace. Both the foreground `/v1/teardown` path and the
async-cleanup goroutine (ADR 0011) consult and update it:

- A new call against an already-registered namespace returns
  `ErrTeardownInProgress` immediately without invoking kubectl.
- The slot is released in `defer` regardless of how the path exits.

This prevents a race where the async cleanup goroutine for scenario A
and an explicit /v1/teardown for scenario A both spawn kubectl
invocations against the same namespace.

## Why both pre-check and late detection?

Defence in depth. The pre-check is the cheap fast path that covers the
overwhelming majority of the cascade — Provision arrives, namespace is
already Terminating from the previous scenario, fail fast. The late
detection covers the genuinely racy case where the namespace was Active
at request entry and got terminated mid-apply. Without it, a
once-in-a-thousand interleaving still surfaces as 500. With it, the
cascade-prevention guarantees hold under concurrency.

## Behaviour change in the petri ↔ oasisctl contract

`/v1/teardown` previously returned 200 on success and 500 on any
failure. It can now also return 202. Clients of petri must interpret
202 as "the deletion landed in kube; the namespace is not yet available
for reuse, but no operator action is required." oasisctl in particular
should not reuse the namespace name until either a subsequent
/v1/teardown returns 200 (clean delete after the in-progress
finalisation completed) or a /v1/provision against that namespace
returns 200 (the namespace is gone and was recreated).

The 409 from /v1/provision is also new. It means "the target namespace
is finalising; try again in ~30s." It does not mean petri or the agent
is broken.

## Alternatives considered

- **Status 503 Service Unavailable for the terminating case.** Rejected
  because 503 implies the service as a whole is unavailable, which is
  not true — a different namespace would succeed.
- **Status 425 Too Early.** Rejected as too obscure; tooling (curl,
  proxies, load balancers) treats it inconsistently.
- **Returning 500 with a typed body.** Rejected because status codes
  are the primary routing signal for retries in HTTP-aware
  intermediaries. A 500 says "give up or escalate"; a 409/202 says
  "retry later."
- **Storing the registry in the asyncTasks coordinator.** Rejected
  because asyncTasks tracks goroutine lifetimes, not per-key
  in-flight state. Mixing the concerns would have required a wider
  API change for very little reuse.

## Consequences

What this makes easier:

- One broken image now produces one failed scenario, not a cascade of
  eight.
- Operators reading run.log can grep for `namespace pre-check:
  terminating` and `teardown in progress: returning 202` to see the
  cascade-prevention firing as designed. The absence of 500s in the
  cluster of related verdicts is the signal that the defence held.
- oasisctl-side logic for "is the failure mine to fix?" gets cleaner:
  4xx is *substrate state*, 5xx is *petri bug*.

What this makes harder:

- A regression in `GetNamespacePhase` (e.g. kubectl's jsonpath output
  changes) silently degrades pre-check coverage to the late-detection
  path only. The fallback is correct but slower. Treat any unexplained
  jump in "namespace late-detected as terminating during apply" log
  lines as a smoke signal that `GetNamespacePhase` may be broken.
- The new 202 path is a behaviour change in the public HTTP contract.
  Existing clients that hard-code "200 == success, everything else ==
  failure" will need to grow a 202 branch.

What a future reader should know before "fixing" this:

- The 30s timeout is calibrated against real namespace-finalisation
  times. Tightening it will manufacture spurious 202s; loosening it
  will mask actual stuck namespaces. Move it only with evidence.
- The teardown registry is intentionally minimal. It tracks "is a
  teardown in flight?" not "how is the teardown going?" State about
  the actual deletion lives in kube. Resist the temptation to make
  the registry richer.
- Pre-check probe failures are deliberately non-fatal. Do not "fix"
  them to fail-closed without a hard look at the false-positive
  surface — kube API hiccups happen and should not become Provision
  failures.
