# 0005. Multi-arch manifest resolution defaults to linux/amd64

- Date: 2026-05-10
- Status: accepted

This ADR records a decision made during the bug-fix work that taught the
registry probe to resolve manifest lists / image indexes.

## Context

The host-side image probe (see ADR 0003) does manifest GET → blob HEAD.
When the manifest is a manifest list (Docker `manifest.list.v2+json`) or
image index (OCI `image.index.v1+json`), the response body contains
per-architecture child manifest digests rather than a config / layer
reference. HEADing one of those child digests at `/v2/.../blobs/<digest>`
returns 404 because per-arch digests address manifests, not blobs — the
original bug.

The probe needs to:

1. Detect that the top-level manifest is a list / index.
2. Pick a per-arch child manifest digest.
3. GET that per-arch manifest.
4. Extract a blob digest from the per-arch manifest and HEAD it.

Step 2 needs a policy: which child manifest do we pick?

## Decision

`selectPerArchDigest` in `pkg/preflight/registry.go` picks `linux/amd64`
first. If no `linux/amd64` entry exists, it falls back to
`runtime.GOOS`/`runtime.GOARCH`. If neither matches, it returns an error
naming both attempts.

The platform we resolved to is recorded on the probe result
(`PerArchPlatform`) and surfaced on the `Check.Platform` field, which
the renderer shows on the success line and the JSON output emits as a
`platform` field.

## Consequences

Why `linux/amd64` is the right default:

- petri's primary substrate is kind, which runs on Linux node containers.
  Kind on macOS / arm64 still typically uses `linux/amd64` images for
  upstream kubernetes pods (the busybox util image, registry.k8s.io
  images) because most upstream multi-arch images publish `linux/amd64`.
- Most CI environments where petri runs are `linux/amd64`.
- Most public registry images publish `linux/amd64`. Falling back to
  `runtime.GOARCH` first would resolve to `darwin/arm64` on a developer's
  Mac, which is uncommon and frequently absent from public manifest
  lists — producing a false-positive probe failure.
- The failure mode of "probe resolved a platform the cluster won't pull"
  is rare and self-correcting: the kubelet will report it definitively in
  `--deep` mode, and the host-side probe is meant to catch network
  problems, not platform mismatches.
- The fallback to `runtime.GOOS`/`runtime.GOARCH` covers the case where
  someone is running petri against a cluster whose nodes do not have a
  `linux/amd64` image available (e.g. arm64-only registries) — the
  probe will at least pick something the local arch can be expected to
  resolve, and if that fails, the explicit error names both attempts so
  the diagnostic is clear.

The renderer surfaces the resolved platform on success (e.g.
`pass (linux/amd64, 472ms)`) so an operator who suspects the wrong arch
was probed can spot it without reaching for the JSON output. Single-arch
images leave the platform empty — there is no choice to surface.
