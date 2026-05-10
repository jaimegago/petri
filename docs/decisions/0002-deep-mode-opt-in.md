# 0002. --deep is a separate opt-in flag

- Date: 2026-05-10
- Status: accepted

This ADR records a decision made during the initial implementation of
`petri verify`.

## Context

The default preflight image checks are host-side: they HEAD the registry
manifest and a referenced blob from the petri host. This catches the
common failure mode (host network blocks the registry CDN, e.g. the
Docker Hub R2 case) cheaply.

There is a less common failure mode where the petri host can reach the
registry but cluster nodes cannot — kind worker containers using a
different DNS resolver, an HTTP proxy configured on the host but not
inside the cluster, or a registry mirror that only certain nodes can
reach. The way to catch it definitively is to actually pull the image
on the cluster: create a namespace, schedule a pod that uses the image,
wait for `Running`, tear it all down.

## Decision

The cluster-side pull test is gated behind a separate `--deep` flag.
Default `petri verify` runs only the host-side probe; `petri verify --deep`
adds the cluster pull test for each verified image, and `petri serve --deep`
does the same when paired with `--verify`.

## Consequences

Costs that justify the separation:

- Wall-clock time. Each deep image check costs roughly 30–60 seconds — the
  pod has to schedule, the kubelet has to pull, and we poll until ready or
  a definitive pull failure. With two images verified by default (default
  + util), that's a minute or two per run, an order of magnitude longer
  than the host-side probe.
- Cluster state. Deep mode creates a real namespace and pod. Cleanup is
  best-effort but the namespace can survive an interrupted run, leaving
  stray resources. Default-on would mean every `verify` invocation
  potentially leaves cluster state behind.
- Limited additional signal. The host-vs-cluster network divergence case
  is genuine but rare in petri's primary deployment (kind labs). Most
  failures the host-side probe doesn't catch are cluster-internal
  policy/admission issues, which deep mode also doesn't catch.

The asymmetry is intentional: we make the cheap, broadly-useful check the
default, and the expensive, narrow-but-definitive check the explicit
opt-in.
