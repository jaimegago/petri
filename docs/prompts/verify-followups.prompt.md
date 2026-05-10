# Prompt: Verify / preflight follow-up improvements

Generated: 2026-05-10
Model: claude-opus-4-7 (1M context)
Target:
- pkg/preflight/preflight.go
- pkg/preflight/checks.go
- pkg/preflight/registry.go
- pkg/preflight/render.go
- pkg/preflight/preflight_test.go
- internal/cli/verify.go
- internal/cli/verify_test.go
- internal/cli/serve.go
- internal/cli/kubeconfig.go
- docs/decisions/
- docs/prompts/verify-followups.prompt.md

## Specification

Goal: ship four follow-up improvements to the preflight/verify work, all found during manual validation against a real lab. None are blocking, all are localized to pkg/preflight and internal/cli/{verify,serve}.go.

Read pkg/preflight/preflight.go, pkg/preflight/checks.go, pkg/preflight/registry.go, pkg/preflight/render.go, internal/cli/verify.go, and internal/cli/serve.go before making changes — these were built across two prior sessions.

Item 1 — short-circuit all dependent checks when the kubeconfig file is missing
Symptom: when the user passes --kubeconfig to a path that doesn't exist (or --lab to a lab whose kubeconfig has been removed), the kubeconfig check fails in microseconds but the registry-side image checks still run for ~860ms total. The cluster-dependent checks (cluster reachable, RBAC) correctly skip; the registry checks do not.
Fix: when the kubeconfig check fails specifically because the file does not exist (file-not-found error from os.Stat or equivalent), all subsequent checks must be skipped, not just cluster-dependent ones. The rationale is that a missing-kubeconfig run is almost always a typo or forgotten flag, and running registry probes provides no value to the user. Other kubeconfig-check failure modes (file exists but does not parse, file exists but references unreachable server) keep the current behavior — they only short-circuit cluster-dependent checks, not registry checks, because registry reachability is genuinely independent in those cases.
Implementation notes:
- Distinguish "file-not-found" from "parse error" in the kubeconfig check result. A typed sentinel error or an exported boolean field on the check result is fine; model decides. The runner uses this signal to decide which downstream checks to skip.
- The skipped registry and audit-log-writability checks should render with reason "skipped: kubeconfig file does not exist" — distinct from the existing "skipped: kubeconfig check did not pass" used for cluster/RBAC skips. The two reasons are semantically different and the report should reflect that.
- Tests: extend the preflight tests with a case where the kubeconfig path does not exist; assert all five subsequent checks are skipped (cluster, RBAC, default image, util image, audit log) and the rendered text shows the new skip reason for the registry and audit checks.

Item 2 — serve --verify renders the report through slog instead of writing it to stderr
Symptom: when serve --verify aborts on a failed preflight, every line of the report is emitted as its own slog WARN entry with timestamp prefix. The output is much harder to read than the same report rendered by `petri verify` standalone. Empty lines in the report become `level=WARN msg=""` entries.
Fix: in the serve --verify failure path (in internal/cli/serve.go, where preflight is invoked), do two things on failure:
1. Write the human-readable report directly to os.Stderr using the same RenderText path the verify subcommand uses. No slog wrapping.
2. Additionally, log a single structured WARN line summarizing the failure with fields {failed_checks: int, total_checks: int, duration_ms: int}. This is the line a process supervisor capturing structured logs will see. Do not include the report text in this structured line.
Then return the existing error so serve exits non-zero before binding the listener.
Acceptance: running `serve --verify` against a broken substrate produces a clean, human-readable report on stderr (no per-line timestamp prefixes, no empty slog entries) followed by exactly one structured WARN log line summarizing the failure, then exits 1. The current "preflight failed: refusing to start the OASIS server" error wrapping is preserved.

Item 3 — petri serve should accept --kubeconfig for parity with petri verify
Symptom: `petri verify` accepts --kubeconfig as an override that takes precedence over --lab and KUBECONFIG. `petri serve` does not. This is awkward in CI and scripting contexts where the caller wants to point serve at an arbitrary kubeconfig without registering a lab.
Fix: add a --kubeconfig string flag to `petri serve`. Resolution precedence must match `petri verify` exactly: --kubeconfig overrides --lab overrides $KUBECONFIG. If both --kubeconfig and --lab are supplied, --kubeconfig wins and a single INFO log line is emitted noting that --lab was ignored. If neither is supplied, current behavior is unchanged (require --lab or fail).
Reuse: there should already be a kubeconfig-resolution helper introduced when verify was built. Both `verify` and `serve` must call the same helper. If the helper currently lives only in internal/cli/verify.go, lift it to a shared location (internal/cli/kubeconfig.go is fine) so both subcommands consume it.
Tests: extend internal/cli/verify_test.go (or add internal/cli/serve_test.go alongside) with cases that assert the --kubeconfig override is honored by serve, and that --kubeconfig + --lab together logs the ignored-lab message and uses --kubeconfig.

Item 4 — multi-arch image probe success doesn't surface the resolved platform in the rendered report
Symptom: when the registry probe successfully resolves a multi-arch manifest list down to a per-arch manifest (e.g. linux/amd64) and verifies a blob from it, the success line in the rendered report just says "pass" with no indication of which platform was probed. The probe result already carries PerArchPlatform from the prior bug-fix PR; the renderer just doesn't show it.
Fix: when a registry-side image check passes against a multi-arch image (i.e. PerArchPlatform is non-empty on the probe result), the rendered success line should include the platform. Format suggestion: "pass (linux/amd64, 472ms)" — model can choose the exact placement, but the platform must appear in both the human-readable text renderer and the JSON output (add a "platform" field on the per-check JSON record, populated only when applicable). Single-arch images render as today with no platform suffix.
Tests: extend the renderer tests to assert the platform is shown on multi-arch success and absent on single-arch success. Extend the JSON output test to assert the field is present and correctly populated.

Cross-cutting acceptance criteria
- `go fmt ./... && go vet ./... && go test ./...` clean.
- Manual run on a healthy lab still passes both default and --deep modes.
- Manual run with a missing kubeconfig path completes in well under 1s (since registry checks no longer run).
- Manual run of `serve --verify` on a broken substrate produces a clean stderr report and exactly one structured WARN summary line.
- `serve --kubeconfig=<path> --verify` works against an arbitrary kubeconfig with no lab registered.

Archival
- Save the full text of this prompt to docs/prompts/verify-followups.prompt.md. The repo already has a docs/prompts/ convention with files named <slug>.prompt.md (existing examples: remove-response-content.prompt.md, value-containment-support.prompt.md). Match that convention.

ADRs (architecture decision records)
- Create docs/decisions/ if it doesn't already exist.
- If docs/decisions/README.md does not exist, create it with a one-page explanation of the convention: filename pattern NNNN-slug.md (four-digit zero-padded sequence number), each file has a short title, date, status (accepted/superseded/deprecated), and prose covering context, decision, and consequences. Note when to add a new ADR (any non-obvious architectural choice, anything future-you would question).
- Add an ADR for item 1's two-tier skip behavior: kubeconfig file-not-found short-circuits all dependent checks (typo case); kubeconfig parse error or unreachable cluster short-circuits only cluster-dependent checks. Capture why the asymmetry is intentional and why "skip everything when the file is missing" is the right default for the typo case.
- Add an ADR for item 2's rendering choice: serve --verify writes the human-readable report to stderr directly and emits one structured WARN summary line via slog. Capture why slog-per-line was rejected (unreadable in interactive use) and why the structured summary is still emitted (process-supervisor capture).
- Backfill ADRs for the major decisions made across the verify work that aren't yet recorded:
  - Preflight is opt-in (--verify flag, petri verify subcommand) rather than automatic on every petri serve. Capture the cost-benefit reasoning.
  - --deep is a separate opt-in flag rather than always-on. Capture why (cost: 30-60s per image, creates real cluster state).
  - The registry probe runs from the petri host by default rather than from inside the cluster. Capture why (host-network breakage is the common failure mode; cluster-side pull is opt-in via --deep for the rarer host-vs-node network divergence case).
  - KubeClient is defined at point of use in pkg/preflight rather than imported from a shared package. Capture the per-repo invariant this follows.
  - Multi-arch manifest resolution picks linux/amd64 by default with fallback to runtime.GOOS/runtime.GOARCH. Capture why linux/amd64 is the right default (most kind nodes are linux/amd64, most CI is linux/amd64, the failure mode of "wrong arch resolved" is rare and has clear remediation).
  - runXxxCheck functions use named returns specifically so deferred Duration assignment lands on the value the caller sees (the Go gotcha with defer-on-non-named-return). This one belongs as an inline code comment near the named-return signature, not as an ADR — but record it somewhere so the next person doesn't "fix" it back.
- Each backfilled ADR should be dated with today's date but its prose should reflect that the decision was made earlier (use phrasing like "this records a decision made during the initial verify implementation").
