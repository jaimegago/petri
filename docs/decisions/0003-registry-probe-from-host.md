# 0003. Default image probe runs from the petri host, not the cluster

- Date: 2026-05-10
- Status: accepted

This ADR records a decision made during the initial implementation of
`petri verify`.

## Context

Verifying that an OASIS-bound image is pullable can be done from two
places:

1. From the petri host: open a TLS connection to the registry, fetch the
   manifest, HEAD a blob.
2. From inside the cluster: schedule a pod that uses the image, wait for
   it to come up.

These probes catch overlapping but distinct failure modes:

- A host-side probe catches host-network breakage: ISP / corporate proxy
  blocking the registry CDN, DNS misconfigured on the host, the registry
  itself returning errors. This is the canonical "Docker Hub blobs served
  from Cloudflare R2 are null-routed" case that motivated the work.
- A cluster-side probe catches divergence between the host and the cluster:
  kind nodes using a different resolver, an HTTP proxy applied to the host
  but not the cluster, registry mirrors visible to one and not the other.

In practice the host-side failure mode is the one operators hit. Cluster-
side failures are rarer (kind shares the host's network in most
configurations) and have less ambiguous remediation when they do occur.

## Decision

The default image check is host-side. Cluster-side pull testing is opt-in
via `--deep` — see ADR 0002.

The host-side probe lives in `pkg/preflight/registry.go`. It speaks the
OCI distribution API directly (manifest GET → blob HEAD) instead of
shelling out to `crane` or `docker`, so it works without those tools on
the path.

## Consequences

- The default `petri verify` is fast (≈1–2s for both images) and produces
  the diagnostic the operator usually needs.
- The probe correctly resolves manifest lists / image indexes (selecting
  a per-arch child manifest) before HEADing a blob — see ADR 0006 for
  the platform selection logic.
- The probe will not catch the rarer host-vs-cluster divergence case;
  operators who suspect it run `--deep`.
- The probe is implemented in pure Go and adds no external dependencies.
  This is consistent with the repo invariant that templates/IaC be
  generated, not implemented in-process — but the probe itself is doing
  IO, not generating artifacts.

If a future incident shows that host-vs-cluster divergence is more common
than expected, the default could shift toward "host probe + a fast
cluster probe (e.g. exec into an existing daemonset pod)" rather than
flipping `--deep` on by default.
