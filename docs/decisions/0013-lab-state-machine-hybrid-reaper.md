# 0013. Hybrid lab-state-machine reconciliation: background reaper in serve + lazy on-read transitions

- Date: 2026-05-11
- Status: accepted

## Context

Pre-fix, `Lab.Status` and `Lab.ExpiresAt` drifted independently. A code
audit (`docs/investigations/lab-state-machine.md`) found three intertwined
problems:

1. **Expired labs were never reaped automatically.** `Orchestrator.StartCleanupLoop`
   existed and was tested, but no production code path invoked it. `petri info`
   and `petri list` rendered the contradictory line `Status: ACTIVE
   Expires: <past> (EXPIRED)` and the system never reconciled the two.

2. **`petri serve --lab` ignored TTL and disk reality.** It refused
   non-`ACTIVE` labs but happily bound the OASIS HTTP listener against a
   lab past its TTL or — worse — an `ACTIVE` lab whose kubeconfig file
   had been removed out-of-band (e.g. by `kind delete cluster`). Both
   produced silent miscompare against a dead substrate. Scenario results
   were technically wrong without any error surface.

3. **Labs stranded in `CREATING` were unreapable.** If petri crashed
   mid-create, the lab stayed `CREATING` forever. `cleanup --expired`
   reached it only after TTL elapsed, then logged "cannot transition to
   DESTROYING" and skipped (`CanTransitionTo` did not permit
   `CREATING → DESTROYING`). The record was a permanent ghost.

The declared-but-unused `EXPIRING` status was a fourth, structural,
problem: a state that the type system advertised but no writer ever set,
inviting future contributors to assume it meant something it didn't.

## Decision

Adopt the hybrid design (Option F in the investigation document):
**background reaper in `petri serve` + lazy on-read transitions in CLI
subcommands**. The two tiers cover both long-running-serve and
CLI-only-operator workflows.

Specifically:

1. **`pkg/orchestrator/cleanup.go`** — `StartCleanupLoop` is wired into
   `petri serve` after preconditions and before the HTTP listener
   binds. The goroutine is tracked through a new
   [`pkg/asynctasks.Tasks`](../../pkg/asynctasks/asynctasks.go)
   coordinator (extracted from the existing private `oasis.asyncTasks` —
   see ADR 0011) so SIGTERM waits up to 30s for the reaper to honour
   ctx cancellation before forcing exit. Cadence is configurable via
   `oasis.lab_reaper_interval` in `~/.petri/config.yaml`, defaulting to
   5 minutes. `--no-reaper` on the command line and
   `oasis.disable_lab_reaper` in config both disable it.

2. **`pkg/state/transitions.go`** — A new shared helper,
   `TransitionIfExpired(ctx, store, lab)`, performs the lazy `ACTIVE`
   → `EXPIRED` write whenever any CLI reader sees a stale-ACTIVE lab.
   `petri info`, `petri list`, and `petri serve --lab` all call it
   before rendering or acting on `lab.Status`. The reaper handles the
   eventual `DESTROYED` transition; the lazy step only relabels. A lab
   may sit in `EXPIRED` for up to one reaper interval before its
   substrate is torn down, which is acceptable — the rendering is no
   longer self-contradictory the moment any reader touches it.

3. **`petri serve --lab` preconditions.** Before binding the listener,
   serve requires (a) `lab.Status == ACTIVE` after the lazy transition,
   and (b) the kubeconfig file referenced by the lab record exists on
   disk. A missing file marks the lab `ERROR` with a recorded reason so
   future reads reflect the divergence. Both failures exit non-zero
   with a message that names the lab, the current status, and a
   concrete next-step command (`petri destroy <name>`, `petri cleanup
   --expired`, or `petri create --name <name> …`). The listener is
   never bound on the refusal path.

4. **`EXPIRED` replaces `EXPIRING`.** The dead `LabStatusExpiring`
   constant is removed; the new `LabStatusExpired` ("EXPIRED") is the
   target of the lazy transition. The transition table updates to
   `ACTIVE → EXPIRED → DESTROYING → DESTROYED`.

5. **`CREATING → DESTROYING` becomes permitted, age-gated.** The
   transition is allowed in the table, but the orchestrator reaper and
   the CLI `destroy` command both consult `Lab.IsStrandedCreating(30m)`
   before taking it. A fresh `CREATING` lab is never destroyed; one
   that has been stuck for 30+ minutes (well past any real create) is
   recovered automatically. `petri cleanup --expired` and the reaper
   both pick stranded labs up. The 30 min threshold is generous
   relative to real create times (kind + apps + observability tops out
   around 5 min) and tight relative to operator memory.

### Why hybrid over alternatives

- **Reaper-only (Option C).** CLI-only operators who don't run `petri
  serve` would keep seeing `ACTIVE (EXPIRED)` indefinitely. The reaper
  fires only inside serve.
- **Lazy-only (Option B).** Long-lived serve processes accumulate
  `EXPIRED` records that never become `DESTROYED`. Kind clusters
  outlive their owners and consume disk + Docker resources. The reaper
  closes that loop.
- **Refuse-vs-warn on dead substrates.** The investigation called out
  silent miscompare as the worst failure mode: scenarios passing or
  failing against a substrate the operator believes is the right one
  when in fact it isn't. A warning is too easy to miss; a hard refusal
  forces the operator to confront the divergence. The error messages
  carry the next-step command so the friction is bounded.
- **`CREATING → DESTROYING`.** A real "petri create crashed midway"
  scenario was previously unrecoverable without manual SQLite surgery.
  The age gate keeps the door closed for labs still mid-create.

## Consequences

- `petri info` and `petri list` against a stale-`ACTIVE` lab now render
  `Status: EXPIRED` and write that status back to the DB on the spot.
  The previously-contradictory `ACTIVE (EXPIRED)` line is gone.
- A long-running `petri serve` reaps any lab that elapses TTL during
  its lifetime, within `oasis.lab_reaper_interval` (default 5 min).
  Operators relying on labs outliving a scenario must run `petri
  extend` before TTL fires or set `--no-reaper`.
- `petri serve --lab <expired>` and `petri serve --lab <kubeconfig
  missing>` both refuse to bind and exit non-zero with a clear,
  actionable error. The OASIS HTTP listener is never opened in either
  case.
- Stranded `CREATING` labs are recovered by both the reaper and `petri
  cleanup --expired` after 30 minutes. `petri destroy` against a
  young `CREATING` lab is rejected to guard against yanking a real
  in-flight create.
- The transition table now lives in two places that share a single
  source of truth: `CanTransitionTo` (the structural check) and the
  age-gating helpers `IsExpired` / `IsStrandedCreating` (the runtime
  checks). All status mutations elsewhere in the codebase go through
  these; new mutation sites should land alongside them in
  `pkg/state/transitions.go` rather than scattered across the CLI.
- The previous `LabStatusExpiring` constant is removed. External
  callers (none in-tree) would need to migrate to `LabStatusExpired`.
- `pkg/asynctasks` is now public so the lab reaper and the OASIS
  provider share the same panic-recovering goroutine coordinator.
  `pkg/oasis/async.go` is a thin alias to keep the existing call sites
  unchanged.
- Manual repro: create a lab with `--ttl=1m`, wait 2 minutes, run
  `petri info` — status renders `EXPIRED`. Start `petri serve --lab
  <name>` against a fresh lab, let TTL elapse during the session, and
  the lab is destroyed within the reaper interval; the log line `lab
  reaper: destroying expired lab` confirms.
