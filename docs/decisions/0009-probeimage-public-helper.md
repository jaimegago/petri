# 0009. pkg/preflight.ProbeImage is the single public entry point for substrate registry probing

- Date: 2026-05-10
- Status: accepted

## Context

`pkg/preflight` was originally introduced as the single source of truth for
substrate-readiness checks (kubeconfig parseability, cluster reachability,
RBAC, image pullability, audit log path). Its registry probe — a
manifest-then-blob HEAD with TCP-vs-HTTP error classification and multi-arch
manifest-list resolution — was deliberately implemented inside the package
with unexported types because at the time it had exactly one caller:
`petri verify` (and, transitively, `petri serve --verify`).

The typed-image-pull-failure work in ADR 0008 introduced a second caller:
the fast-fail pod-event watcher in `pkg/oasis`. When kubelet reports an
`ImagePullBackOff`, the watcher logs the failure plus a follow-up
"registry probe result" line that tells the operator whether the registry
itself is unreachable or whether the failure is kubelet-specific. That
follow-up needs the same probe logic preflight already implements.

Two options were considered:

1. Lift the probe code out of `pkg/preflight` into a new substrate-probing
   package consumed by both.
2. Keep the probe code in `pkg/preflight` and expose a narrow public API
   (a single `ProbeImage(ctx, client, ref)` function returning a typed
   result) for cross-package consumption.

Option 1 doubles the surface area: callers now choose which package to
import. Option 2 keeps `pkg/preflight` authoritative and forces new
substrate checks to land in one place.

## Decision

`pkg/preflight` is the canonical home for petri's substrate-probing logic.
Cross-package consumers reach it through `preflight.ProbeImage(ctx, client,
ref) (ImageProbeResult, error)` and the `ImageProbeResult.Outcome()` /
`.Detail()` accessors. The underlying `probeImagePull` function and its
helper types remain unexported.

The contract:

- `ProbeImage` takes a context, an optional `*http.Client` (nil →
  `http.DefaultClient`), and an OCI reference. A non-nil error means the
  reference itself was unparseable; all other failure shapes are reflected
  in the returned `ImageProbeResult`.
- `ImageProbeResult.Outcome()` collapses the result to one of `pass`,
  `manifest-fail`, `perarch-fail`, `blob-tcp-fail`, `blob-http-fail`. This
  is what callers should put in structured log fields and dashboards.
- `ImageProbeResult.Detail()` returns the first non-empty failure message,
  safe for inclusion in a slog field.

Petri's substrate-probing surface area is intentionally narrow:
`ProbeImage` is the only public probe today. New substrate-reachability
checks (e.g. a kubelet-side image-pull probe via Job, or a control-plane
reachability probe from inside a kind node) must land in `pkg/preflight`
and follow the same "one public entry point per substrate concern"
pattern.

## Consequences

- `pkg/oasis` consumes `pkg/preflight.ProbeImage` directly. No probe logic
  is duplicated.
- Callers do not get access to the internal manifest-list resolution
  details, the registry-bearer-token machinery, or the per-arch digest
  fallback heuristics. If a new caller needs those, that's a signal the
  public API needs widening — and that widening goes through this ADR's
  pattern, not by exporting more internals ad-hoc.
- A future change to the probe (e.g. supporting `@sha256:` digest
  references, or adding HEAD-with-Accept negotiation) ships once and
  benefits every caller.
- The integration test surface for the probe stays in `pkg/preflight`'s
  existing `registry_test.go` / `fake_kube_test.go` infrastructure. New
  consumers (like `pkg/oasis`) get to mock `ProbeImage` at the call site
  via `http.Client` injection or by stubbing it out behind an interface
  if they need to test failure paths without spinning up a fake registry.
