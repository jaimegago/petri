# 0001. Preflight is opt-in, not automatic

- Date: 2026-05-10
- Status: accepted

This ADR records a decision made during the initial implementation of
`petri verify` and `petri serve --verify`.

## Context

petri's `serve` subcommand starts an OASIS provider against an existing
Kubernetes substrate. A misconfigured substrate (unreachable cluster,
missing RBAC, registry blocked at the network edge) produces opaque failures
much later — typically when a scenario tries to apply manifests and the
errors land in scenario logs rather than in the operator's terminal.

Adding readiness checks to catch these problems up front was clearly worth
doing. The question was whether to run them automatically on every
`petri serve` invocation, or to gate them behind explicit opt-in.

## Decision

Preflight is opt-in. There are two entry points:

- `petri verify` — a standalone subcommand the operator runs explicitly
  (e.g. before starting an evaluation, or to debug a specific failure).
- `petri serve --verify` — opt-in flag that runs preflight before binding
  the listener and aborts on failure.

`petri serve` without `--verify` starts the server immediately, exactly as
before this work landed.

## Consequences

The cost-benefit favours opt-in:

- A full preflight run is ~1–2 seconds on a healthy lab and minutes in
  `--deep` mode. Adding 1–2s to every `petri serve` invocation is
  noticeable for operators who run it tens of times a day during
  development, and minutes is unacceptable.
- Preflight makes outbound network calls to registries. Forcing every
  `serve` invocation to do this surprises offline / air-gapped users who
  may have pre-pulled images.
- Operators who want the safety net can wire `--verify` into their
  scripts; we don't take the choice away from them.
- The cost of "I forgot to run verify and got an opaque failure" is low —
  the failure surface is the existing OASIS scenario logs, which we
  haven't regressed. The cost of "verify ran when I didn't want it to" is
  higher because there's no easy way to skip it once it's wired into the
  default path.

If a future incident shows that operators routinely forget `--verify` and
hit preventable failures, this is the ADR to revisit. The migration path
would be flipping the default and adding a `--no-verify` opt-out.
