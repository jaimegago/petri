# 0016. One-shot chaos fault injection is a CLI that calls Execute directly

- Date: 2026-06-27
- Status: accepted

## Context

`pkg/chaos` ships a complete fault catalog (`DefaultFaults()`) and a
`ChaosRunner` that fires faults *continuously and randomly* according to a
`ChaosProfile` probability/timing policy. Until now the only thing wired to the
package was `petri serve`, which borrows `chaos.NewKubeClient` to build its
OASIS kube client — there was no user-facing way to inject a single, specific
fault against a running lab on demand.

Operators and scenario authors frequently want exactly that: "make
`boutique-frontend` restart, now" or "put CPU pressure on this pod for 30s" —
a deterministic, one-shot perturbation, not a probabilistic session. The
`ChaosRunner` is the wrong tool for this: it owns interval scheduling,
probability rolls, target pools, and an `EventEmitter` timeline, none of which a
single deliberate injection needs.

The catalog already exposes the one-shot seam cleanly: a `Fault` is looked up by
`FaultType` in the registry and executed via `fault.Execute(ctx, kube, target,
params)`. `ChaosRunner.injectFault` is itself just a logging wrapper around that
one call. So a one-shot trigger needs only the registry, a kube client, a
target, and params — no runner.

Lab-resolution plumbing also already exists: `petri serve` resolves `--lab` to a
local kubeconfig, applies the lazy on-read expiry transition, and refuses a
non-ACTIVE lab via `serveLabStatusError`, with an explicit `--kubeconfig`
override that bypasses lab resolution entirely.

## Decision

Expose one-shot fault injection as a new `petri inject <fault-type>` command
that calls `Fault.Execute` directly, bypassing `ChaosRunner`.

- **Direct Execute, no runner.** The command looks the fault up in the registry
  and calls `Execute` once, mirroring only the logging `ChaosRunner.injectFault`
  performs (fault_type, namespace, target, kind, outcome). Continuous/random
  injection (`ChaosRunner`), scripted scenario-file injection (the scenarios
  runner), and the provision-time `pkg/workloadstate` source are explicitly out
  of scope for this command today — see the backlog for the latter two as future
  modes under the same command.
- **Structural fault-type sourcing.** Both the validation set and the help-text
  enumeration derive from `chaos.DefaultFaults()` at runtime via a single
  `acceptedFaultTypes(registry)` helper. There is no hardcoded fault-type
  literal in the CLI, so the accepted surface cannot drift from the catalog and
  the fault count is never duplicated.
- **Reuse the lab-resolution guard.** A new shared helper
  `resolveActiveLabKubeconfig` resolves `--kubeconfig`/`--lab` with the same
  active-lab guard `petri serve` uses — it reuses `serveLabStatusError`,
  `localKubeconfigPath`, and `state.TransitionIfExpired` rather than
  reimplementing resolution. `--kubeconfig` overrides `--lab` exactly as serve
  and verify do.
- **CLI validates shape, the fault validates semantics.** `--target` is parsed
  as a `namespace/kind/name` triple into `chaos.TargetResource`; `--param
  key=value` pairs are parsed into a string map. Kind, name, and params pass
  through verbatim — per-fault target/parameter validation belongs to the fault,
  which resolves and validates its own target.
- **Testable core.** The registry and the kube-client constructor are injected
  into `runInject`, so parsing, validation, the active-lab guard, dry-run, and
  single-dispatch are unit-tested with a fake fault and fake kube client, no real
  cluster. `--dry-run` resolves and validates everything (lab, fault type,
  target, params) and prints the plan without constructing a kube client or
  calling `Execute`.

## Consequences

- `petri inject` is the runtime counterpart to the provision-time
  `pkg/workloadstate` capability: chaos perturbs an already-running resource,
  workloadstate synthesises a workload born into a named state. The two are
  documented side by side so they stay distinguishable.
- Adding a fault to `DefaultFaults()` automatically extends both `inject`
  validation and its help text — no CLI change required.
- The command is local-cluster-oriented (it resolves a local kubeconfig like
  serve); a non-local ACTIVE lab surfaces as "no kubeconfig path in metadata".
- An unrecognized fault type is a hard error enumerating the accepted set; a
  malformed target or param is a hard error; a missing/non-active/unresolvable
  lab is a clear error with a non-zero exit.
- Scripted scenario-file injection and a `pkg/workloadstate` source are recorded
  in the backlog as future modes under this same `inject` command rather than as
  separate commands.
