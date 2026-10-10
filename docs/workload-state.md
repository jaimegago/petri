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
`petri inject --state <state>` is the other consumer: it provisions a workload
born into a state directly against a lab, without a scenario (see
[cli-reference.md](cli-reference.md#petri-inject)).

## Recognized states

The vocabulary is the source of truth in code (`workloadstate.AcceptedStates()`);
the rule for what each materialises and the mechanism it uses:

| State | Materializes | Mechanism |
|---|---|---|
| `running` | a healthy Deployment whose pods reach Ready | container `app` on the spec image, exposing containerPort 80 |
| `crashloopbackoff` | a pod stuck in CrashLoopBackOff | with a **`Fault`**: the application image (`docs/application-image.md`) with the declared env, misconfigured as the fault declares, failing its own startup validation; with declared env and no fault: the spec image with each ConfigMap reference required (`optional: false`) so the kubelet names an absent key; otherwise a symptom-only exit on the util image |
| `oomkilled` | a container the kernel OOM-kills on start | an unbounded `tail /dev/zero` allocation under a `memory: 16Mi` limit (`workloadstate.OOMKilledMemoryLimit`; below it the container's own init is OOM-killed first) |
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
  reaches the application, a class whose symptom is memory exhaustion renders
  its memory limit, and the state must be the one the fault's class produces
  (`config.missing-key` under `crashloopbackoff`, `config.unbounded-cache`
  under `oomkilled`). A declared `Env` is rendered under a fault whatever the
  state, since the application is always the container. The caller verifies the declared symptom afterwards with
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
- `Spec.Convergence() (Convergence, error)` — what the rendered workload looks
  like once it has reached its state; see below.

## Convergence: what readiness waits for

Applying a manifest is not reaching a state: until the kubelet has acted, every
state looks like `ContainerCreating`. `Spec.Convergence` says, for the workload
`Render` produces, what the caller waits for before handing it over — a
**rollout** for a healthy state, or a **symptom** every pod exhibits, matched by
`fault.WaitForSymptom`, for an unhealthy one. It follows the renderer, not the
state's name, and a test holds every accepted state to having one.

| State | Converges when |
|---|---|
| `running` (and an omitted state) | the rollout completes |
| `elevated_error_rate` | the rollout completes — the 5xx is in the responses, not the pod |
| `crashloopbackoff` | every pod is `CrashLoopBackOff`; with declared env and no fault, `CreateContainerConfigError` |
| `oomkilled` | every pod has recorded `OOMKilled` |
| `pending` | every pod is `Unschedulable` (the PodScheduled condition) |
| `degraded` | every pod's container is running — readiness is what fails |
| `error` | every pod is `ErrImagePull` / `ImagePullBackOff` |
| any state with a `Fault` | the class's symptom; the OASIS provider waits on the scenario's declared `expect` instead |

`petri serve` waits on every declared Deployment this way before `/v1/provision`
reports `ready`, and on every bare `pod` entry's container running. A workload
that does not converge within the image-pull plus rollout budget fails the
provision.

See [ADR 0015](decisions/0015-workload-state-capability.md) for why this was
extracted into a Petri-core capability and the seam choices, and
[ADR 0017](decisions/0017-causes-through-the-application.md) for why a cause
under diagnosis is materialised by the application rather than a stand-in.

## Causes, not just symptoms

A state names a symptom. When the *cause* of that symptom is what an agent is
evaluated on diagnosing, the symptom alone is not enough — a container built
to crash announces that it was built to crash. `pkg/fault` is the catalog of
causes: a `Spec` names a misconfiguration (`config.missing-key` carries the
ConfigMap and key; `config.unbounded-cache` the setting the application reads
as its cache bound) and the symptom it produces, and `Spec.Fault` hands it to
this package, which renders the application born misconfigured. The runtime
trigger, `petri inject <class>`, applies the same misconfiguration to a
healthy running application. Both verify the symptom before reporting
success.
