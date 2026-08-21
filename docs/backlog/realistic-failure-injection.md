realistic-failure-injection
open

Injected failures announce themselves. An agent that reads the cluster finds a
stand-in container whose whole purpose is visibly to fail, rather than an
application that is failing — so a diagnostic evaluation measures whether the
agent can read a caption, not whether it can diagnose.

## The evidence

The first OASIS DA-1 capability run against joe (2026-08-21) scored `incorrect`
on `infra.capability.da.single-signal-diagnosis-001`. The scenario provisions a
`notification-service` deployment in CrashLoopBackOff and a `smtp-config`
ConfigMap missing `SMTP_PORT`, and scores the agent on naming the missing key as
root cause. joe answered:

> The `notification-service` pods are crashing because they are intentionally
> configured to do so. The pod specification includes a command
> `sh -c "echo CrashLoopBackOff simulation; exit 1"` which causes the container
> to exit immediately after starting … This is a simulation, likely for testing
> purposes.

**That is a correct reading of the cluster petri built.** The agent was marked
wrong for describing what was actually there.

## Where it comes from

Two layers that do not meet.

`pkg/generators/apps` renders plausible applications — Deployments, Services,
Ingresses, HPAs, PDBs, NetworkPolicies — from per-company templates
(`pkg/companies`: acme, cloudnative, techflow). That layer produces something
that looks like a real estate.

`pkg/workloadstate/render.go` then implements a failure by **replacing the
workload with a synthetic stand-in**:

| state | what actually runs |
|---|---|
| `CrashLoopBackOff` | busybox `sh -c "echo CrashLoopBackOff simulation; exit 1"`, or a container referencing `__petri_missing_key__` |
| `OOMKilled` | busybox `dd if=/dev/zero of=/dev/null bs=1M` |
| `ElevatedErrorRate` | an inline `python3 -c` script |

Each is a container that exists in order to fail, and two of them say so in
their own text. The app the scenario is nominally about is gone.

## What is wanted

1. **Hide the injection.** A failure should be discoverable by diagnosis —
   reading logs, events, config, resource limits — and not by reading a
   self-describing command line. Nothing in the manifest should name the
   mechanism or the word "simulation".
2. **A real application that can have bugs.** Rather than substituting a
   stand-in per failure state, run an actual application whose failure modes can
   be switched on: a missing config key it genuinely reads at startup, a memory
   leak it genuinely has, an endpoint that genuinely errors under load. The
   failure then emerges from the app's own behaviour, and the evidence an agent
   collects is the evidence a real incident would leave.

## Open questions this item does not decide

Deliberately left for the session that takes this, because each is a design
choice with a cost:

- **What the app is.** Built and published by this project, or an existing
  sample application adopted? A published image is a release surface and a
  supply-chain dependency; a built one is maintenance.
- **How a bug is switched on.** Environment variables, a config file, a build
  flag, or a sidecar. This decides whether the injection is invisible in the pod
  spec, which is the point of (1).
- **How much realism each state needs.** `Pending` and `ImagePullBackOff` are
  properties of scheduling and registries rather than of an app, and may
  legitimately stay synthetic. The states that need an app are the ones where
  the *cause* is the thing under diagnosis.
- **Relationship to the existing state vocabulary.** Whether `StateCrashLoopBackOff`
  and friends survive as the interface with new implementations behind them, or
  whether scenarios come to declare a fault rather than a symptom.
- **Cost per lab.** A real app image is a pull and a startup on every lab
  creation, against a cold-start budget that has already been a problem —
  see the closed `configurable-rollout-timeout` work.

## Not the same as the narrow DA-1 fix

`joe-pm/queue/da1-fixture-does-not-materialise-the-cause.md` tracks the tactical
half: DA-1 declares no `spec.configMapRef`, so `renderCrashLoop` takes the
`exit 1` path rather than the ConfigMap path, and the ConfigMap path itself
references a synthetic `__petri_missing_key__` instead of the key the scenario
declares absent. **That fix makes DA-1 answerable; it does not make the fixture
realistic** — the ConfigMap path is still a container built to fail. This item is
the strategic half and does not block that one.

## Provenance

Raised by the maintainer on 2026-08-21 from the DA-1 result. Recorded in
`joe-pm/threads/da1-capability-run.md`.
