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
rollout wait, and short-circuits the wait as soon as kubelet
reports the failure (typically within ~3 seconds).

**Two different conditions return 502**, distinguished by the `reason`
field rather than the status line. `ImagePullBackOff` and its siblings
mean kubelet reported the pull as *failed* — terminal, and no retry
helps. `ImagePullTimeout` means the pull was still *running* when the
image-pull budget expired; the layers already fetched stay in the node
cache, so a retry resumes from further along. See
[Image pull budget](#image-pull-budget-separate-from-the-rollout-budget)
below.

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

### Multi-deployment scenarios: one timeout, several WARN lines

As of [ADR 0012](decisions/0012-parallel-rollout-waits.md), petri waits
on multiple deployments in parallel. The `ErrRolloutTimeout` response
body will commonly list a **single** deployment rather than a
comma-separated list, because the first goroutine to fail short-circuits
the rest. Sibling failures still emit a per-deployment WARN line in
`run.log` — in temporal order — so the full picture is recoverable
from the log even when the HTTP response carries only one.

```
msg="deployment rollout failed" deployment=checkout-api namespace=payments error="..."
msg="deployment rollout failed" deployment=web-app namespace=frontend error="..."
```

The grep string `"deployments did not become ready within"` still
matches on the response body for the surviving error; only the trailing
list shape changed.

### Image pull budget, separate from the rollout budget

The 60-second rollout budget covers only the time a Deployment spends
converging **after** its images are resolved on the node. Fetching the
images has its own budget, `oasis.image_pull_timeout`, defaulting to 5m
and settable with `petri serve --image-pull-timeout`.

They are separate because they want opposite things. A first pull on a
cold node is bounded by image size and link speed; a stuck rollout
should fail fast. While one 60s deadline served both, the first scenario
of every cold run failed to provision — reported as
`deployments did not become ready within 1m0s`, which named the wrong
cause. Raising the 60s would have bought the pull at the cost of every
genuinely stuck rollout taking longer to fail, so the budgets were split
instead.

Petri decides which budget the passing seconds belong to from pod state:
a container waiting in `ContainerCreating` with no resolved `imageID` is
pulling; anything else — scheduling pressure, sandbox setup, probes,
crashloops — is rollout time. An unschedulable pod reports no container
status at all and is therefore charged to the rollout budget, so it
still fails in ~60s rather than borrowing the pull budget on top.

When the pull budget is what expired, run.log carries:

```
msg="image pull budget exhausted" deployment=payment-gateway namespace=production pull_budget=5m0s images=...
```

and the 502 body carries `"reason": "ImagePullTimeout"`. That string is
deliberately not the rollout phrasing — parsers that count rollout
failures by grepping `"deployments did not become ready within"` must
not pick up pull timeouts.

### Effective rollout-timeout budget: per-deployment, not per-scenario

With parallel waits in place
([ADR 0012](decisions/0012-parallel-rollout-waits.md)), the hardcoded
60-second rollout budget is a **per-deployment** budget, not a
per-scenario one. Before parallelization, a scenario with N deployments
could consume up to N × 60s of wall clock during the healthy-rollout
phase, and oasisctl's client-side request timeout was the practical
ceiling. After parallelization, the wall-clock ceiling for that phase is
always ~60s regardless of deployment count, up to the concurrency cap of
8. Use this to set expectations when sizing client-side timeouts: budget
~60s of substrate-wait headroom per scenario, plus the cost of
non-rollout work (namespace creation, state injection, RBAC).

### Per-deployment rollout floor on kind clusters

On the kind-based local lab provisioner, a single Deployment rollout
takes ~10-20s of wall clock even when the container image is already
cached on the node. The time is spent on pod scheduling, Calico CNI
sandbox setup, projected-volume mounts (service account token,
`kube-root-ca.crt`), container start, and the Deployment controller
waiting for at least one pod to reach Ready before reporting the
rollout complete. This is the kind substrate's baseline, not a petri
regression — a single-deployment scenario provisioning in ~15s is
behaving normally. The 60s `rolloutTimeout` is sized around this
floor: a healthy deployment lands well inside it, a stuck one
exhausts it.

### Parallelism diagnostic signature in run.log

To confirm at a glance that parallel rollout waits are working as
intended, look at the `"waiting for deployment rollout"` INFO lines in
petri's log (typically `/tmp/petri-serve.log` or wherever the operator
redirects stdout). For a multi-deployment scenario, all those lines
should fire within a few milliseconds of each other:

```
2026-05-11T10:42:13.084 INFO waiting for deployment rollout deployment=app-a namespace=ns-a
2026-05-11T10:42:13.084 INFO waiting for deployment rollout deployment=app-b namespace=ns-b
2026-05-11T10:42:13.085 INFO waiting for deployment rollout deployment=app-c namespace=ns-c
```

If those lines are staggered by seconds in a multi-deployment scenario,
that is a regression signal: parallelism has been disabled or broken by
a refactor. The pre-ADR-0012 sequential implementation produced lines
staggered by whatever each rollout took (often 10+ seconds apart),
which is the shape to watch for.

## /v1/provision returns 409: namespace is terminating

The 409 Conflict response from `/v1/provision` means the target namespace
is in Kubernetes' `Terminating` phase — typically because the previous
scenario's teardown is still finalising. This is **not** a petri failure;
it is the cascade-prevention guard from ADR 0014 working as designed.
The response body is:

```json
{
  "status": "error",
  "message": "namespace <ns> is terminating; reuse will fail until termination completes",
  "namespace": "<ns>",
  "retry_after_seconds": 30
}
```

Clients (oasisctl, the OASIS runner, manual curl) should wait at least
`retry_after_seconds` before retrying with the same namespace, or
allocate a different namespace name.

### Distinguishing "cascade prevented" from "real petri bug"

When investigating a cluster of related verdicts, scan run.log for the
status codes:

- **409s and 202s appear, no 500s follow** — cascade-prevention is
  working. One upstream failure was contained to one scenario verdict.
  No petri-side action needed; investigate the root cause (broken
  scenario template, registry outage, etc.).
- **500s appear from /v1/provision or /v1/teardown** — petri-side bug.
  The cascade-prevention guard did not fire. File against pkg/oasis.

### Diagnostic signature

The canonical "cascade prevented" sequence in run.log is:

```
... msg="teardown in progress: returning 202" namespace=<ns> kubectl_duration_ms=...
... msg="namespace pre-check: terminating" namespace=<ns>
```

A 202 from `/v1/teardown` followed within seconds by a 409 from
`/v1/provision` against the same namespace is the prevention working as
designed. Both responses must come back with their typed shapes
(`status`, `namespace`, `retry_after_seconds` / `estimated_remaining_seconds`);
a generic `{status, message}` envelope at either of these positions is
a regression.

## /v1/teardown returns 202: deletion in progress

The 202 Accepted response from `/v1/teardown` means `kubectl delete
namespace` hit petri's wall-clock budget (30s) but the namespace is at
least in `Terminating` phase — kube has accepted the delete request and
is finalising. This is more honest than the pre-ADR-0014 500: the
deletion did not fail, it just isn't finished yet.

```json
{
  "status": "in_progress",
  "message": "teardown in progress for namespace <ns>; finalisation typically completes within 30s",
  "namespace": "<ns>",
  "estimated_remaining_seconds": 30
}
```

The client should treat 202 as "the namespace is not yet available for
reuse but no operator action is required." A subsequent `/v1/teardown`
against the same env id either:

- returns 200 once kube finishes finalisation, or
- returns 202 again with the registry-detected
  `ErrTeardownInProgress` (no duplicate kubectl invocation is spawned —
  see Part 5 of ADR 0014).

If a `/v1/teardown` instead returns 500, the underlying error in the
response body (`message` field) is a genuine kubectl failure (RBAC
denial, transport error, etc.) — not a slow finaliser — and warrants
investigation.

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

### Lab lifecycle and the background reaper

`petri info` and `petri list` apply a lazy `ACTIVE → EXPIRED` transition
when they encounter a stale-ACTIVE lab, so the rendered status always
matches the truth. The eventual `EXPIRED → DESTROYED` step is the
background reaper goroutine inside `petri serve` (default cadence: 5
min, configurable via `oasis.lab_reaper_interval`, disable with
`--no-reaper` or `oasis.disable_lab_reaper: true`).

Stranded `CREATING` labs (a `petri create` that crashed mid-provision)
are reaped automatically once the lab has been in `CREATING` for more
than 30 minutes. `petri cleanup --expired` will also pick them up.

See [ADR 0013](decisions/0013-lab-state-machine-hybrid-reaper.md) for
design rationale.

#### Manual repro

1. `petri create --company acme --level 1 --local --name lifecycle-demo --ttl 1m`
2. Wait 2 minutes.
3. `petri info lifecycle-demo` — `Status: EXPIRED`. The DB record has
   been updated.
4. Start `petri serve --lab <another-active-lab>` and let TTL elapse
   for `lifecycle-demo` while serve is running. Within
   `oasis.lab_reaper_interval` (default 5 min), serve's log emits
   `lab reaper: destroying expired lab` and the lab transitions to
   `DESTROYED`.

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
