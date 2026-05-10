# petri verify

`petri verify` runs preflight substrate-readiness checks and reports whether
this host can drive an OASIS evaluation. The same checks can be run as a
gate on `petri serve` via `petri serve --verify`.

OASIS evaluation runs cost 7-10 minutes of wall-clock time. When the
substrate is unhealthy (kubeconfig stale, cluster unreachable, registry
blocked, RBAC misconfigured), the run fails midway with errors that look
like petri or scenario bugs. `petri verify` surfaces those issues in
seconds, before the run is started.

## Entry points

### Subcommand

```bash
petri verify                              # default mode, ~5-10s
petri verify --deep                       # also pulls images on the cluster, ~30-90s
petri verify --json                       # CI-friendly machine-readable output
petri verify --kubeconfig=/path/to/kc     # explicit kubeconfig
petri verify --lab my-lab                 # resolve kubeconfig from a petri lab
petri verify --audit-log-path /var/log/k8s/audit.log
```

Exit code is 0 if every check passes, non-zero on any failure.

### Serve flag

```bash
petri serve --lab my-lab --verify             # gate startup on preflight
petri serve --lab my-lab --verify --deep      # same, with cluster-side pull test
petri serve --kubeconfig=/path/to/kc --verify # arbitrary kubeconfig, no lab
```

When `--verify` is set, `petri serve` runs the same checks before binding
the HTTP listener. On failure it renders the human-readable report directly
to stderr (the same output `petri verify` produces) and emits a single
structured `WARN` log line summarising the failure (`failed_checks`,
`total_checks`, `duration_ms`), then exits non-zero — the listen address is
never bound, so a downstream client trying to connect gets connection
refused. On success it logs `verify checks passed` at INFO and continues
normal startup.

`petri serve` accepts the same `--kubeconfig` override as `petri verify`.
Precedence: `--kubeconfig` > `--lab` > `KUBECONFIG` env / default kubeconfig
loading rules. If both `--kubeconfig` and `--lab` are supplied, the explicit
path wins and an `INFO` log line records that the lab was ignored.

## Checks

The checks run in this order; later checks may skip when earlier ones fail.

| # | Name                | What it verifies |
|---|---------------------|------------------|
| 1 | `kubeconfig`        | The kubeconfig path exists and parses. |
| 2 | `cluster_reachable` | client-go `ServerVersion()` succeeds within 5s. Failures classify as network / TLS / auth. |
| 3 | `rbac`              | The kubeconfig identity has `create` and `delete` on namespaces (`SelfSubjectAccessReview`, no namespace is actually created). |
| 4 | `image_default`     | Registry-side HEAD on `oasis.default_image` (manifest + a referenced blob) — see [Image-pull failure mode](#image-pull-failure-mode). |
| 5 | `image_util`        | Same as `image_default` for the internal busybox util image used by unhealthy-state builders. |
| 6 | `audit_log_path`    | Skipped if not configured. Otherwise verifies the parent directory exists and is writable by the current user. |

With `--deep`, additional checks run:

| # | Name                   | What it verifies |
|---|------------------------|------------------|
| 7 | `image_default_deep`   | Cluster-side: creates a temporary namespace + Pod (`sleep 30`, `restartPolicy=Never`) using the default image, waits up to 60s for it to reach Running or fail, deletes the namespace. |
| 8 | `image_util_deep`      | Same for the util image. |

Deep mode is opt-in because it costs ~30-60s per image and creates real
cluster state. It catches cases where the petri host can reach the
registry but kind nodes cannot — a corporate proxy on the host with no
proxy in kind, for example.

## Image-pull failure mode

The `image_default` and `image_util` checks do **two** HEAD requests per
image:

1. `HEAD /v2/<repo>/manifests/<tag>` — the manifest endpoint.
2. `HEAD /v2/<repo>/blobs/<digest>` on the first referenced blob.

The second request matters: a working manifest endpoint with broken blob
fetches is the canonical R2 failure mode. Docker Hub's blob storage runs
on Cloudflare R2 (the `172.64.0.0/13` netblock), which is null-routed by
some ISPs, corporate networks, and mobile carriers. Manifests come from a
different path that often resolves to a different netblock. So a host
with R2 blocked can fetch manifests but fail every blob fetch — and the
resulting `ImagePullBackOff` looks like a petri or scenario bug rather
than a network issue.

When `image_default` fails with the manifest reachable but the blob
unreachable, `petri verify` produces output like:

```
[FAIL] Default OASIS image is pullable (registry-side)        fail  (3.20s)
       image pull check failed: manifest reachable but blob fetch failed
       registry-1.docker.io/library/nginx:1.27
       manifest endpoint: https://registry-1.docker.io/v2/library/nginx/manifests/1.27 (OK)
       blob endpoint:     https://registry-1.docker.io/v2/library/nginx/blobs/sha256:...
       blob error:        i/o timeout

       This commonly indicates an upstream block on the registry's CDN.
       Docker Hub blobs are served from Cloudflare R2 (172.64.0.0/13),
       which is null-routed by some ISPs, corporate networks, and mobile
       carriers. If the manifest works but the blob does not, the petri
       host cannot reach the CDN even though it can reach the registry.
       → Test from the host: `curl -I <blob_url>`. If it hangs, route
         172.64.0.0/13 around the block, switch to a registry not backed
         by R2 (registry.k8s.io), or pre-pull the image from a working
         network.
```

## JSON output

`--json` writes a single JSON object to stdout. The shape is stable enough
to parse in CI:

```json
{
  "started_at": "2026-05-10T13:55:31.012Z",
  "duration": 4123456789,
  "deep": false,
  "passed": false,
  "checks": [
    {
      "name": "kubeconfig",
      "title": "Kubeconfig readable and parseable",
      "status": "pass",
      "duration": 1200000,
      "summary": "kubeconfig parsed (server: https://127.0.0.1:6443)"
    },
    {
      "name": "image_default",
      "title": "Default OASIS image is pullable (registry-side)",
      "status": "fail",
      "duration": 3210000000,
      "summary": "image pull check failed: manifest reachable but blob fetch failed",
      "diagnostic": "registry-1.docker.io/library/nginx:1.27\n...",
      "next_steps": "..."
    }
  ]
}
```

Status values are `pass`, `fail`, or `skip`. `duration` is nanoseconds
(Go `time.Duration`).

## Configuration

`petri verify` reads `oasis.default_image` and `oasis.audit_log_path` from
your config file (`~/.petri/config.yaml` by default). Both can be
overridden by flag — see the entry points above.

```yaml
oasis:
  default_image: registry.k8s.io/nginx-slim:0.27
  audit_log_path: /var/log/k8s/audit.log    # optional
```

## Limitations (v1)

- `petri verify` does not run automatically on `petri serve`; opt-in via
  `--verify`.
- It does not verify per-scenario images that override `spec.image` —
  scenario-image errors are surfaced by the typed image-pull error path
  in the OASIS provider instead.
- It does not verify company/lab templates; those are heavyweight and
  out of scope.
