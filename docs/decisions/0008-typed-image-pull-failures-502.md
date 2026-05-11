# 0008. /v1/provision returns 502 for image-pull failures, 500 for rollout timeouts

- Date: 2026-05-10
- Status: accepted

## Context

Before this change, every error from `petriProvider.Provision` collapsed to
HTTP 500 with a generic message envelope. The most common failure mode in
practice was a kubelet `ImagePullBackOff` for a scenario's image — but it
showed up as the same opaque "deployments did not become ready within 1m0s"
string as a real readiness timeout. Evaluators (oasisctl, the OASIS runner)
had no way to tell substrate problems apart from real platform or scenario
issues without parsing free-form text out of the response body, and the
60-second wait masked failures that kubelet had already announced at second
two.

The fix introduces two typed errors — `ErrImagePullFailure` and
`ErrRolloutTimeout` — and a fast-fail pod-event watcher that runs alongside
the rollout wait. When the watcher fires, the handler must signal "the
upstream petri depends on is broken" without colliding with the existing
"petri itself is broken" 500 path.

## Decision

`/v1/provision` maps errors as follows:

- `*ErrImagePullFailure` → **HTTP 502 Bad Gateway** with the extended body
  shape `{status, message, image, namespace, pod, reason, kubelet_message}`.
- `*ErrRolloutTimeout` → **HTTP 500 Internal Server Error** with the
  generic `{status, message}` envelope. The message preserves the historical
  phrasing `deployments did not become ready within <d>: <list>` so log
  parsers continue to match.
- Any other provider error → **HTTP 500** with the generic envelope
  (unchanged behavior).

The mapping lives in `writeProvisionError` next to `handleProvision`. It is
not added to the shared `httpStatusForErr` helper because the
"unreachable-upstream-registry" failure mode is provision-specific — other
endpoints (Observe, StateSnapshot, …) don't surface it and shouldn't sprout
a 502 path.

Petri's role in the request graph is the rationale for 502 rather than 503:
when a scenario's pod cannot pull its image, petri itself is healthy but a
named upstream (the OCI registry) is not. That's the textbook definition of
a Bad Gateway response: the proxy reached the upstream and got a failure
signal back.

## Consequences

- Runners (oasisctl, the OASIS runner) can branch on status code alone:
  `502 → substrate / registry failure; retry policy and operator triage path
  differ from 500`. This is the original motivation — without it, every
  provision failure was attributed to "petri broke."
- The structured body for 502 is additive, not a breaking change. Clients
  that decoded only `{status, message}` still see those fields; the new
  fields are ignored. Clients that want the typed fields can decode into
  `imagePullErrorResponse`.
- `errors.As` is used to detect the typed error through any `fmt.Errorf`
  wrapping. The Provision wrapper (`fmt.Errorf("waiting for deployments:
  %w", err)`) preserves the chain, so wrapping is safe.
- Future endpoints with their own typed-error needs should follow the same
  per-handler-mapping pattern rather than overloading `httpStatusForErr`.
- The 502/500 split is deliberately a server-side classification; the
  client doesn't have to teach itself about substrate-failure heuristics
  from message-string parsing.
