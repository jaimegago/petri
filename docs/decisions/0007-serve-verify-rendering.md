# 0007. serve --verify writes the report to stderr and a single WARN to slog

- Date: 2026-05-10
- Status: accepted

## Context

`petri serve --verify` runs preflight before binding the listener and
aborts on failure. The standalone `petri verify` subcommand renders the
report to stdout via `preflight.Render` and exits non-zero on failure;
its output is human-friendly ASCII with per-check status, diagnostics,
and a `Result: PASS / FAIL` footer.

The original `serve --verify` failure path looped over the rendered
report and wrote each line through `c.log.Warn(line)`. That worked but
was substantially less readable than the standalone subcommand:

- Every line picked up a slog timestamp prefix.
- Blank lines in the report (between checks, before the footer) became
  `level=WARN msg=""` entries.
- Process supervisors capturing structured logs saw N WARN events per
  failed run with no single summary entry to alert on.

## Decision

On preflight failure, `runServeVerify` does two distinct things:

1. Writes the human-readable report directly to `os.Stderr` (or a
   test-injected writer via `c.serveVerifyOut`). This uses the same
   `preflight.Render` path as the standalone `petri verify` subcommand,
   so the on-screen output is identical.
2. Emits exactly one structured WARN log line summarising the failure
   with fields `failed_checks`, `total_checks`, `duration_ms`. The
   rendered report text is NOT included in this log entry.

Then it returns the existing `"preflight failed: refusing to start the
OASIS server"` error so `runServe` aborts before binding any port.

## Consequences

The split lets each sink see what it needs:

- A human watching the terminal sees the same clean ASCII report
  `petri verify` produces, on stderr where the operator is already
  looking. No timestamp prefixes, no empty WARN entries.
- A structured-logs consumer (a log aggregator, a process supervisor)
  sees one WARN with the fields it needs to alert on, without having
  to reassemble many entries into a single event. The rendered
  diagnostics remain available on stderr if a human investigates.
- Tests inject a buffer via `c.serveVerifyOut` so the rendered output
  is inspectable without globally swapping `os.Stderr`.

A future change to `preflight.Render` automatically benefits both call
sites; the rendering is a single code path. A future log-shipping setup
that wants the full report in structured form can capture stderr and
attach it to the WARN entry — but doing that in petri itself would have
re-introduced the original problem.
