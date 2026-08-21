# Workload State Capability

`pkg/workloadstate` provisions a Kubernetes **Deployment that is born exhibiting
a named operational state** — healthy, or one of several intentionally unhealthy
states. The caller hands it a spec describing the deployment plus the requested
state; the capability renders the corresponding manifest and applies it. The pod
materialises directly into the requested state — there is no healthy
intermediate that later degrades.

This is the **provision-time** half of a pair:

| Capability | When it acts | What it does |
|---|---|---|
| [`pkg/workloadstate`](../pkg/workloadstate) | provision time | synthesises a workload that is *born* in a named state |
| [`pkg/chaos`](../pkg/chaos) | runtime | *perturbs* an already-running resource (kill a pod, corrupt a ConfigMap, add latency) |

The two axes are distinct: chaos answers "what happens to a healthy system when
something goes wrong"; workload-state answers "what does a system that is already
in a known-bad shape look like". They share a shape — each owns a narrow
`KubeClient` interface defined at the point of use and an entry point taking a
context, that client, and a spec.

The capability is described here in the abstract. OASIS (`pkg/oasis`) is one
consumer that drives it from state-entry specs, but `pkg/workloadstate` does not
import or know about OASIS; any caller can populate a `Spec` and provision.

## Recognized states

The vocabulary is the source of truth in code (`workloadstate.AcceptedStates()`);
the rule for what each materialises and the mechanism it uses:

| State | Materializes | Mechanism |
|---|---|---|
| `running` | a healthy Deployment whose pods reach Ready | container `app` on the spec image, exposing containerPort 80 |
| `crashloopbackoff` | a pod stuck in CrashLoopBackOff | with a **`Fault`**: the application image (`docs/application-image.md`) with the declared env, misconfigured as the fault declares, failing its own startup validation; with declared env and no fault: the spec image with each ConfigMap reference required (`optional: false`) so the kubelet names an absent key; otherwise a symptom-only exit on the util image |
| `oomkilled` | a container the kernel OOM-kills on start | a busy `dd` allocation under a `memory: 4Mi` limit |
| `pending` | pods that never schedule, stuck Pending | an impossible `cpu: "100"` request → "Insufficient cpu" |
| `degraded` | a partially-available Deployment | forces ≥2 replicas with a failing `/healthz` readiness probe so a subset of pods never become Ready |
| `elevated_error_rate` (alias `elevated-error-rate`) | a service returning errors for a share of traffic | a Python HTTP server that returns HTTP 500 for a configurable percentage of requests (default 50%) |
| `error` | pods stuck ErrImagePull / ImagePullBackOff | a reference to a non-existent image |

The unhealthy states deliberately avoid Docker Hub for their utility images:
`crashloopbackoff` and `oomkilled` source their shell image from
`registry.k8s.io` (Docker Hub blob storage runs on Cloudflare R2, which is
null-routed by some networks).

## Spec fields

`workloadstate.Spec` is populated by the caller:

- `Name`, `Namespace` — Deployment identity.
- `Replicas` — desired count (values < 1 become 1; `degraded` forces ≥ 2).
- `Image` — container image for states that run a real workload (`running`,
  `pending`, `degraded`, and the declared-env variant of `crashloopbackoff`).
  States that supply their own image (`oomkilled`, `error`, the exit-1 variant
  of `crashloopbackoff`, `elevated_error_rate`) ignore it, and so does a
  `Fault`, which runs the application image.
- `Labels`, `Annotations` — extra pod/template labels and Deployment
  annotations.
- `ManagedBy` — when set, adds an `app.kubernetes.io/managed-by` annotation.
- `MatchLabels` — selector; defaults to `{"app": Name}`.
- `State` — the requested state as a raw string (see rules below).
- `Env` — the container environment as declared; rendered verbatim.
- `Fault` — a `fault.Spec` from the cause catalog (`pkg/fault`). When set the
  state is materialised through the application rather than a synthetic
  container, ConfigMap references render `optional: true` so the absence
  reaches the application, and the state must be the one the fault's class
  produces. The caller verifies the declared symptom afterwards with
  `fault.WaitForSymptom`; `Render` only materialises. See
  [ADR 0017](decisions/0017-causes-through-the-application.md).
- `AppImage` — override for the application image; defaults to `fault.AppImage`.
- `ErrorRate` — 5xx percentage for `elevated_error_rate` (defaults to 50).
- `UtilImage` — override for the shell image; defaults to
  `workloadstate.DefaultUtilImage`.

## State resolution rules

- **Omitted or empty state yields `running`.** A missing or whitespace-only
  `State` renders a healthy deployment. Matching is case-insensitive and trims
  surrounding whitespace; documented aliases resolve to their canonical state.
- **An unrecognized non-empty state is a hard error.** Any value outside the
  accepted vocabulary causes `Render`/`Provision` to return a descriptive error
  naming the resource and the offending value and enumerating the accepted
  states — and **nothing is applied to the cluster**. There is no silent
  fallback to a healthy deployment; a typo in a state name fails loud at
  provision time rather than producing a misleadingly healthy environment.

## Entry points

- `Render(spec) (string, error)` — pure: validates the state and returns the
  Deployment manifest with no cluster access. Unit-testable on manifest output
  alone.
- `Provision(ctx, kube, spec) error` — renders then applies via the narrow
  `KubeClient` (`ApplyYAML`). Returns the validation error without applying when
  the state is unrecognized.

See [ADR 0015](decisions/0015-workload-state-capability.md) for why this was
extracted into a Petri-core capability and the seam choices, and
[ADR 0017](decisions/0017-causes-through-the-application.md) for why a cause
under diagnosis is materialised by the application rather than a stand-in.

## Causes, not just symptoms

A state names a symptom. When the *cause* of that symptom is what an agent is
evaluated on diagnosing, the symptom alone is not enough — a container built
to crash announces that it was built to crash. `pkg/fault` is the catalog of
causes: a `Spec` names a misconfiguration (`config.missing-key` carries the
ConfigMap and key) and the symptom it produces, and `Spec.Fault` hands it to
this package, which renders the application born misconfigured. The runtime
trigger, `petri inject <class>`, applies the same misconfiguration to a
healthy running application. Both verify the symptom before reporting
success.
