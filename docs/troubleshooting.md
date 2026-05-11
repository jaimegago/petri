# Troubleshooting

## OASIS evaluation produces unexplained provision failures

**Run `petri verify` first.** It checks the substrate (kubeconfig parseability,
cluster reachability, RBAC, default image pullability, audit log path
writability) in under 10 seconds and surfaces issues that otherwise show
up mid-evaluation as ImagePullBackOff or RBAC errors that look like
petri/scenario bugs.

```bash
petri verify --lab my-lab            # default mode, ~5-10s
petri verify --lab my-lab --deep     # also pulls images on the cluster (~60s/image)
petri verify --json                  # machine-readable output for CI
```

See [verify.md](verify.md) for the full check list and output format.

### Catching the R2 / Cloudflare image-pull failure

A common failure mode is that the host's network blackholes Cloudflare R2
(`172.64.0.0/13`). Docker Hub manifests fetch fine, but blob fetches time
out — and the resulting `ImagePullBackOff` looks like a scenario bug.
`petri verify` is designed to catch this: it does a registry-side HEAD on
the default image's manifest **and** a referenced blob, so a working
manifest with a hung blob produces a clear "registry reachable but blob
fetch from R2 timed out" failure.

To synthesize the failure mode locally and confirm `petri verify` catches
it, configure a default image whose blobs live on Docker Hub / R2, then
block the netblock from inside a kind node:

```bash
# Configure a default image whose blobs come from Docker Hub (R2-backed):
PETRI_OASIS_DEFAULT_IMAGE=docker.io/library/nginx:1.27 petri verify --lab my-lab

# Or, after `petri create --oasis --lab my-lab`, block R2 inside the kind node
# and re-run verify; the image_default check should fail with the R2-specific
# diagnostic:
docker exec my-lab-control-plane iptables -A OUTPUT -d 172.64.0.0/13 -j REJECT
petri verify --lab my-lab --deep
docker exec my-lab-control-plane iptables -D OUTPUT -d 172.64.0.0/13 -j REJECT
```

The host-side check runs from the petri host, not the kind node, so the
iptables synthetic-repro inside the kind node only catches the failure in
`--deep` mode (cluster-side pod pull). For host-side reproduction, block
R2 on the host (e.g. `pfctl` on macOS, `iptables` on Linux) before running
`petri verify`.

## /v1/provision returns 502: image pull failure

When `petri serve` returns HTTP **502 Bad Gateway** from `/v1/provision`,
the scenario's deployment referenced an image that kubelet couldn't pull.
Petri detects this via a pod-event watcher that runs alongside the
60-second rollout wait, and short-circuits the wait as soon as kubelet
reports the failure (typically within ~3 seconds).

The 502 response body is the standard `{status, message}` envelope plus
structured fields:

```json
{
  "status": "error",
  "message": "image pull failure for example.invalid/x:1.0 in pod ns/web-app-abc-1: ImagePullBackOff: ...",
  "image": "example.invalid/x:1.0",
  "namespace": "ns",
  "pod": "web-app-abc-1",
  "reason": "ImagePullBackOff",
  "kubelet_message": "Back-off pulling image \"example.invalid/x:1.0\""
}
```

`reason` is one of: `ImagePullBackOff`, `ErrImagePull`, `ErrImageNeverPull`,
`RegistryUnavailable`, `InvalidImageName`, `CreateContainerConfigError`,
`ImageInspectError`, `SignatureValidationFailed`.

`petri serve` also emits two structured WARN log lines you can grep in
run.log:

```
msg="image pull failure detected" deployment=web-app namespace=ns image=... pod_name=web-app-abc-1 reason=ImagePullBackOff message="..."
msg="image pull failure: registry probe result" image=... probe_outcome=manifest-fail probe_detail="..."
```

The follow-up "registry probe result" line runs the same registry probe
that [`petri verify`](verify.md) uses, so you get an immediate answer to
"is the registry itself unreachable, or is this kubelet-specific?" without
having to manually re-run verify. `probe_outcome` is one of `pass`,
`manifest-fail`, `perarch-fail`, `blob-tcp-fail`, `blob-http-fail`.

### Async log ordering: probe and cleanup arrive after the 502 response

As of [ADR 0011](decisions/0011-async-cleanup-and-probe.md), the registry
probe and the post-failure namespace deletion run in background goroutines
so the HTTP 502 returns within ~1s of the watcher firing instead of ~10s.
This means the `"image pull failure: registry probe result"` line and the
`"async cleanup: namespace deletion succeeded"` line can — and usually do
— appear in run.log **after** the corresponding HTTP-request log line for
the 502. Look forward ~30s in the log for the async confirmations, not
inline.

The async lines you can grep for:

```
msg="async cleanup: deleting namespace after provision failure" namespace=oasis-xxx env_id=... reason=image-pull-failure
msg="async cleanup: namespace deletion succeeded" namespace=oasis-xxx duration_ms=...
msg="async cleanup: namespace deletion failed" namespace=oasis-xxx error=...
msg="async cleanup: namespace deletion timed out" namespace=oasis-xxx
msg="image pull failure: registry probe abandoned" image=... timeout=30s
```

On SIGTERM, `petri serve` waits up to 30s for in-flight async tasks
before exiting and logs the outcome:

```
msg="draining async tasks on shutdown" timeout=30s
msg="async tasks drained cleanly on shutdown"
# or, if the budget elapsed:
msg="async tasks did not drain within shutdown budget; abandoning" timeout=30s
```

If the abandonment line fires, the namespace was not torn down by petri.
Either delete it manually with `kubectl delete ns oasis-xxx` or let the
next `petri serve` run consume it (scenario namespaces include the env
ID, so re-use is safe).

When the failure is registry-side (`probe_outcome=manifest-fail` or
`blob-tcp-fail`), see the [R2 / Cloudflare](#catching-the-r2--cloudflare-image-pull-failure)
section above — that's the most common kind. When the probe passes but
kubelet still fails, the cluster nodes have a different network view than
the petri host (e.g. a kind node missing a private-registry credential,
or an HTTP proxy on the host that the kind node doesn't honor).

The 502 status is distinct from the 500 returned by readiness failures
that are NOT image-pull related (failing liveness probe, scheduling
pressure, etc.). Those keep the historical phrasing in their response
body — `deployments did not become ready within 1m0s: ns/name` — so run.log
parsers that grep for that string continue to work.

## Lab creation fails at Terraform apply

```bash
# Get detailed logs
PETRI_LOG_LEVEL=debug petri create --company=acme --level=2

# Check Terraform state
# State location in lab metadata: petri info <lab-name>
cd ~/.petri/workdir/<lab-id>/infra
terraform state list
terraform state show <resource>
```

## Lab won't destroy cleanly

```bash
# Force destroy (continues past individual cleanup failures)
petri destroy <lab-name> --force

# Manual cleanup if force fails:
# 1. Get resource IDs: petri info <lab-name>
# 2. Delete via cloud console/CLI
# 3. Delete git repos manually
# 4. Clean state DB manually
```

## Expired labs not cleaning up

```bash
# Check system health
petri health

# Manual cleanup
petri cleanup --expired

# Set up cron job for automatic cleanup
crontab -e
# Add: */10 * * * * /usr/local/bin/petri cleanup --expired
```

## Can't connect to created cluster

```bash
# Get kubeconfig path from lab info
petri info <lab-name>

# Set kubeconfig
export KUBECONFIG=~/.petri/labs/<lab-name>/kubeconfig-prod

# Verify
kubectl cluster-info
kubectl get nodes
```
