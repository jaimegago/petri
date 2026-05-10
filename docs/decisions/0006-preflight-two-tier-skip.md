# 0006. Two-tier skip on kubeconfig failure

- Date: 2026-05-10
- Status: accepted

## Context

`pkg/preflight.Run` executes a sequence of checks: kubeconfig, cluster
reachable, RBAC, image probes, audit log writability, and (in `--deep`
mode) cluster-side image pulls. The kubeconfig check is the first one;
later checks depend on it to varying degrees.

When the kubeconfig check fails, the obvious naive policy is "skip every
later check" — but this overshoots in a common case. A user with a
broken cluster (kubeconfig parses but the API server is unreachable)
benefits from learning about a registry-network problem in the same run;
those checks are genuinely independent.

A separate failure mode is "the kubeconfig file does not exist." This is
almost always a typo or forgotten flag. Manual validation showed that in
this case the registry probes still ran and added ≈860ms to the run
before failing with output the user couldn't act on.

## Decision

`Run` distinguishes two failure shapes for the kubeconfig check and
applies different short-circuit policies:

- **File-not-found** (`os.Stat` returned `fs.ErrNotExist`). All
  subsequent checks are skipped. Cluster reachability and RBAC keep
  their existing skip reason `"skipped: kubeconfig check did not pass"`
  (their own check ran with a nil client). The registry image probes and
  the audit-log writability check carry a distinct reason,
  `"skipped: kubeconfig file does not exist"`. The runner applies this
  at the `Run` level rather than asking each check to re-classify.
- **Other failures** (file exists but does not parse; file exists but
  the server it references is unreachable later). Only the
  cluster-dependent checks (cluster reachability, RBAC, deep pull) are
  skipped. Registry probes and audit-log writability run normally.

`runKubeconfigCheck` returns a `fileMissing bool` so the runner can
decide which policy to apply without re-classifying errors after the
fact.

## Consequences

- Manual run with a missing kubeconfig path completes in well under 1s
  instead of ~1.5s, and the report contains no probe results that
  weren't going to inform action.
- The two skip reasons are visible to JSON consumers as well as the
  rendered text, so CI can distinguish "user typo'd a flag" from
  "registry probe was skipped because the cluster check passed".
- Adding new downstream checks requires picking a side: if the check is
  genuinely independent of whether the kubeconfig file exists, run it
  on parse-error; if it is only meaningful when the user has a real
  cluster in mind, skip it on file-missing too. The convention is
  enforced in `Run`, not in each check.
- The asymmetry is intentional. A future maintainer noticing it might
  be tempted to "fix" the inconsistency by either always-skipping or
  always-running. Both are wrong: always-skip wastes cycles for the
  user with a malformed kubeconfig, always-run produces useless output
  for the user with a missing kubeconfig.
