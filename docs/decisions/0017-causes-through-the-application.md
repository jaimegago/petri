# 0017. A cause under diagnosis is materialised by a petri-owned application, not by a stand-in

- Date: 2026-08-21
- Status: accepted

## Context

The first OASIS DA-1 capability run against joe (2026-08-21) scored the agent
`incorrect` for describing the cluster accurately. The scenario asks for a
missing `SMTP_PORT` key to be named as the root cause of a crash-looping
`notification-service`; the cluster petri built ran a busybox container whose
command line read `echo CrashLoopBackOff simulation; exit 1`. The agent read
the pod spec and said so. See `docs/backlog/realistic-failure-injection.md`.

`pkg/workloadstate` (ADR 0015) renders a *symptom* directly: a state entry
says `CrashLoopBackOff` and the package synthesises a container that will
crash. That is the right shape for a symptom that is scenery — a safety
scenario's broken service in a zone the agent must not enter — and the wrong
shape for a symptom whose *cause* the agent is scored on diagnosing. A
declared cause was never materialised; the only cause in the cluster was the
one petri wrote into the command line, and the agent was punished for
reading it.

The contract this ADR implements was ratified in the joe-pm ledger
(`queue/realistic-fixtures-phase.md`, 2026-08-21):

1. a scenario declares the fault, not the state;
2. a scenario declares the expected symptom, and the provider verifies it
   before readiness;
3. nothing the agent can read names the mechanism;
4. the existing state vocabulary survives as the symptom half.

## Decision

**A cause under diagnosis goes through a real application petri owns.**

- **`pkg/fault` is the catalog of causes**, defined once: each class names the
  misconfiguration it applies, the symptom it produces, and the parameters a
  declaration carries. It is distinct from `pkg/chaos` (perturbs a healthy
  system at runtime) and from `pkg/workloadstate`'s synthetic states (render a
  symptom directly). `fault.Parse` is the single construction path, so a
  scenario's `fault` block and a `petri inject --param` list are held to the
  same rules.
- **One catalog, two triggers.** `petri serve` materialises a fault at
  provision time through `workloadstate.Spec.Fault`; `petri inject <class>`
  applies the same misconfiguration to a healthy running application and
  leaves the trail a real bad change leaves — the ConfigMap edited, a rollout
  restarted, a previous ReplicaSet that was fine. Both verify the declared
  symptom with `fault.WaitForSymptom` before reporting success. `petri inject`
  keeps one CLI over two catalogs; a test asserts the names are disjoint, and
  the accepted set is sourced structurally from both.
- **The application is built and published by petri, not adopted.**
  `images/svc` is a small HTTP service configured from its environment that
  validates that configuration at startup and refuses to run on an invalid
  one, naming the setting in its own log. It is a nested Go module so the
  binary's build info carries no `petri` module path. A fault is a plausible
  misconfiguration with the semantics a real application gives it — `SMTP_PORT
  is required when SMTP_HOST is set` — never a switch. The image is pinned in
  `fault.AppImage` like every other image petri uses, and `fault.Catalog`
  documents per class what the pinned version genuinely validates; a
  declaration outside that coverage is refused rather than provisioned against
  an application that would start.
- **The symptom is verified, not assumed.** A deployment entry declaring a
  `fault` must declare an `expect`; `petri serve` waits for every pod of the
  workload to exhibit it and refuses readiness without it. An application that
  does not fail the way the scenario says is a provision-time error, never a
  cluster the agent is scored against. For `CrashLoopBackOff` the match is the
  kubelet's own condition — a container restarted after a failed termination —
  rather than the literal waiting reason: measured on kind v1.35.0, the
  `CrashLoopBackOff` waiting reason is present for under half of the backoff
  period and first appears after several restarts, while the restart count and
  failed termination are visible from the first restart.
- **ConfigMap references render optional on the application path.** The
  profile's rule that a `configMapKeyRef` is a required reference stands for
  entries without a fault: the kubelet then names the absent key. With a fault
  declared, a required reference would let the kubelet pre-empt the
  application — the pod settles in `CreateContainerConfigError` and the
  evidence is a kubelet message about a reference petri wrote. Optional
  references let the absence reach the application, whose own validation is
  the failure the agent sees, in the application's own log.
- **Consistency is checked before anything is applied.** A
  `config.missing-key` fault must name a ConfigMap the scenario declares, in
  the same namespace, without the key; `image` cannot be declared beside
  `fault`; a fault must sit under the state its class produces; and the
  declared environment must actually read the faulted ConfigMap. Each is a
  hard error with nothing applied.
- **The synthetic paths whose cause was a caption are gone.** The
  `__petri_missing_key__` ConfigMap reference and the `CrashLoopBackOff
  simulation` caption are removed. A cause-less `crashloopbackoff` — no fault,
  no declared env — still renders a symptom-only exit on the util image,
  uncaptioned, because `infra.safety.be.implicit-zone-crossing-001` declares
  exactly that for a service whose crash is scenery and whose namespace the
  agent must not enter. Under the contract that is the symptom half surviving
  for a symptom that is not under diagnosis.

*Alternatives considered and rejected.*

- **Adopting Online Boutique or another sample application.** The scored fault
  classes need code paths that exist as real behaviour selectable by ordinary
  configuration; an adopted application either lacks them or must be forked,
  which is the build cost without the ownership. Eleven image pulls from
  registries the lab has had null-routed, against one 2.4 MB image on a
  registry petri controls.
- **`envFrom` instead of optional per-key references.** Most realistic, but it
  drops the declared `containers[].env` the profile requires a provider to
  render, and the evidence path an agent follows — a named reference from the
  pod spec to the ConfigMap — is the same either way.
- **A required-keys variable such as `CONFIG_REQUIRED=SMTP_HOST,SMTP_PORT`.**
  Reads as a switch. The dependency between `SMTP_HOST` and `SMTP_PORT` is
  how a real mail-sending service behaves, and it needs no extra variable.

## Consequences

- DA-1 is diagnosable: the cluster carries a real application failing on a
  real absence, with `SMTP_PORT` in the application's own log and in the
  ConfigMap reference, and nothing in the manifest names the mechanism. What
  the agent answers is now a capability finding.
- The application image is a release surface. A new fault class, or a new key
  the application validates, is a new image version and a new `fault.Catalog`
  entry, documented in `docs/application-image.md`. Publishing requires
  registry credentials the build host must hold.
- Provisioning a faulted scenario costs one image pull (2.4 MB, scratch-based)
  and the application's first restart, which the symptom wait observes within
  seconds; the budget is the pull budget plus the rollout budget.
- `petri inject` is no longer chaos-only. A cause class targets a Deployment
  of the application image and waits for its symptom; `--timeout` bounds that
  wait and chaos faults ignore it.
- A word-bounded grep is what "nothing names the mechanism" means in tests:
  `default` — DA-1's namespace and a common field value — contains `fault`.
