Goal: fix petri's lab state machine so expired labs are reliably reaped, the ACTIVE (EXPIRED) display contradiction is eliminated, and operations against dead substrates fail fast instead of silently misbehaving. Implement the hybrid design from docs/investigations/lab-state-machine.md §6: background reaper in petri serve plus lazy on-read transitions in CLI subcommands. Make serve --lab block on expired or substrate-dead labs.

Read docs/investigations/lab-state-machine.md in full before making changes — it has the file-by-file map of every Status mutation and every ExpiresAt read site. Also read pkg/orchestrator/cleanup.go (specifically StartCleanupLoop, which exists but is never invoked from production code), pkg/types/lab.go (IsExpired), internal/cli/serve.go, internal/cli/info.go (or the equivalent file the investigation identified), internal/cli/list.go, and internal/cli/create.go.

Part 1 — Background reaper in petri serve
- Wire StartCleanupLoop into petri serve's startup. On serve boot, after the OASIS server's HTTP listener is up but before serving any traffic, spawn the cleanup loop in a goroutine. It should run on a reasonable cadence (suggest 5 minutes; make this configurable via the existing petri config file under a new oasis.lab_reaper_interval field, defaulting to 5m).
- The reaper goroutine must coordinate with petri serve's graceful shutdown. The existing asyncTasks coordinator from pkg/oasis/async.go is the right place to register the reaper so SIGTERM waits up to the shutdown budget before forcing exit. Use it.
- Log loudly when the reaper starts ("lab reaper started, interval=5m"), when it actions a lab ("lab reaper: destroying expired lab" with lab name, expiration timestamp, age-past-expiry), and when it stops on shutdown ("lab reaper stopped").
- Add a config flag --no-reaper (or oasis.disable_lab_reaper in config) to disable the reaper for tests, development, or operators who want to manage cleanup themselves. Default is enabled.
- StartCleanupLoop currently relies on test-only injection patterns based on the investigation; the production wiring may need a small adapter or constructor change. Use the minimum-surface change that makes it consumable from internal/cli/serve.go without breaking its existing test coverage.

Part 2 — Lazy on-read transitions in CLI subcommands
- In petri info, petri list, and any other CLI subcommand that reads lab Status, perform a lazy expiry check before displaying. If a lab's Status is ACTIVE and IsExpired() returns true, transition it to EXPIRED in the state DB (via UpdateLab) before rendering the output. This means a user running petri info on a stale-ACTIVE lab will see EXPIRED immediately, and the underlying record is corrected on the spot.
- Important: lazy transitions only happen on READ. The reaper handles the eventual DESTROY. The two-tier model is: ACTIVE → EXPIRED (cheap, lazy) → DESTROYED (real teardown work, eventual via reaper). This means a lab can sit in EXPIRED for up to one reaper interval before its kind cluster is torn down, which is fine.
- This requires introducing a new Status value if it doesn't already exist. The investigation noted EXPIRING is declared but unused — repurpose it as EXPIRED if the semantics fit cleanly, otherwise add a fresh EXPIRED status and clean up the unused EXPIRING separately (see Part 4).
- The lazy-read code must be a small helper function shared across all readers, not duplicated. Likely home: pkg/state/transitions.go or a similarly named file. Function shape: TransitionIfExpired(ctx, store, lab) returns the lab with potentially-updated Status. Each reader calls it before rendering.

Part 3 — serve --lab blocks on expired or substrate-dead labs
- Today, petri serve --lab=<name> resolves the lab record's kubeconfig path and starts serving regardless of whether the lab is past TTL or whether the kubeconfig file even exists on disk. Both produce silent miscompare risk.
- Fix: before binding the HTTP listener, serve --lab must validate two preconditions:
  (a) The lab's Status is ACTIVE (after applying the lazy on-read transition from Part 2). EXPIRED, DESTROYED, ERROR, CREATING, DESTROYING — all refused with a clear error explaining which status and what the user should do.
  (b) The kubeconfig file on disk exists. If the record says ACTIVE but the file is missing (e.g. cluster was deleted out-of-band, disk cleaned, etc.), refuse with a clear error and mark the lab as ERROR in the state DB so future reads reflect the divergence.
- The error message must include the lab name, current status, and a concrete next-step command. Examples:
  - "lab 'verify-smoke' is EXPIRED (past TTL since 2026-05-11T01:27:57Z). Run 'petri destroy verify-smoke && petri create --name verify-smoke ...' to recreate, or 'petri cleanup --expired' to clean up all expired labs."
  - "lab 'verify-smoke' is ACTIVE but kubeconfig file '/Users/.../verify-smoke.kubeconfig' is missing. The cluster may have been deleted out-of-band. Lab record marked ERROR. Run 'petri destroy verify-smoke' to clean up the record."
- Exit non-zero in both cases. Do not bind the HTTP listener.

Part 4 — Clean up the dead EXPIRING status
- The investigation identified EXPIRING as a declared-but-unwritten status. Decide based on the Part 2 implementation:
  - If EXPIRING is repurposed as the lazy-transitioned EXPIRED status, rename it in the state machine documentation (pkg/types/lab.go state diagram, any related docs).
  - If a separate EXPIRED status is introduced, remove EXPIRING from the state machine declaration entirely. Update the state-transition table comment, any switch statements, and any tests that reference it.
- This is housekeeping but worth doing while the state machine is being modified. Future readers won't ask "where does EXPIRING get set?" because it won't be there.

Part 5 — Stranded CREATING after a crash
- The investigation noted that labs stuck in CREATING after a crash are skipped by cleanup --expired because the transition table doesn't allow CREATING → DESTROYING.
- Fix: extend the cleanup --expired logic (and the reaper from Part 1) to also reap labs that have been in CREATING for longer than a generous timeout (suggest 30 minutes — way longer than a real create takes, comfortably shorter than "user forgot for a day"). Log these distinctly: "lab reaper: cleaning up stranded CREATING lab" with the lab's age.
- Add CREATING → DESTROYING as a permitted transition in the state machine, with the reasoning documented in the transition table comment ("Permitted only for labs stranded in CREATING longer than the stranded-create timeout, to recover from petri-create crashes").

Tests
- Unit test: reaper goroutine started on serve boot, fires on its interval, calls Destroy on expired labs. Use a small interval (e.g. 100ms) and assert the expired lab gets destroyed within a few intervals.
- Unit test: reaper coordinates with shutdown via asyncTasks — graceful shutdown waits for the reaper goroutine to exit before serve returns.
- Unit test: --no-reaper flag disables the reaper goroutine entirely. Assert no reaper goroutine is spawned and no expired labs are reaped during serve's lifetime.
- Unit test: TransitionIfExpired helper — ACTIVE+past-TTL transitions to EXPIRED in DB, ACTIVE+unexpired no-ops, EXPIRED no-ops, DESTROYED no-ops.
- Unit test: petri info on an ACTIVE+past-TTL lab triggers TransitionIfExpired and renders EXPIRED. Verify by checking the underlying DB record after the command.
- Unit test: petri serve --lab on EXPIRED lab refuses to bind and exits non-zero with a clear error. The HTTP listener is never bound (verify by attempting to connect to :8090 and confirming connection refused).
- Unit test: petri serve --lab on ACTIVE lab with missing kubeconfig file refuses to bind, marks the lab as ERROR in DB, exits non-zero with the expected error message.
- Unit test: reaper cleans up stranded CREATING lab past the timeout. Lab created with Status=CREATING and a created_at 31 minutes ago; reaper transitions it through DESTROYING to DESTROYED.
- Unit test: CREATING → DESTROYING transition is now permitted only when the lab has been in CREATING longer than the stranded threshold; rejected otherwise. Defensive guard against accidentally destroying labs mid-create.
- Existing tests: any test that previously relied on "ACTIVE labs past TTL stay ACTIVE indefinitely" must be updated to reflect the new lazy-transition behavior. Run `go test ./...` and audit failures.

Documentation
- New ADR documenting the hybrid design choice: background reaper in serve plus lazy on-read transitions in CLI subcommands. Capture: why hybrid over reaper-only (CLI-only operators get correct status without running serve), why hybrid over lazy-only (long-lived serve processes get automatic teardown), why serve --lab blocks rather than warns (silent miscompare risk on dead substrates), why CREATING → DESTROYING is now permitted (stranded-lab recovery).
- Supersede the prior ADRs if any of them touched lab lifecycle. The investigation didn't identify a prior ADR on this specifically, but check.
- Update docs/investigations/lab-state-machine.md with a one-line note at the top: "Status: investigation closed. See docs/decisions/NNNN-lab-state-machine-hybrid-reaper.md for the fix."
- Update CLAUDE.md if there's an invariant worth recording — specifically, that lab Status transitions live in pkg/state/transitions.go (or wherever Part 2's helper lands), not scattered across CLI commands.

Cross-cutting requirements
- go fmt ./... && go vet ./... && go test ./... && go test -race ./pkg/state ./pkg/orchestrator ./internal/cli all clean.
- No new external dependencies.
- The reaper goroutine must not panic the process on errors. Log + continue is the right discipline; panic recovery wrapping per the existing async.go pattern.
- Manual repro path documented: create a lab with --ttl=1m, wait 2 minutes, run petri info — confirm status renders EXPIRED. Wait another 5 minutes with petri serve running — confirm the lab is destroyed by the reaper. Documented in docs/troubleshooting.md or a new docs/lab-lifecycle.md if the topic warrants its own page.

Archival
- Save this prompt as docs/prompts/lab-state-machine-hybrid-fix.prompt.md.

Acceptance criteria
- Running petri info on a lab past its TTL renders Status: EXPIRED, not ACTIVE (EXPIRED).
- Running petri serve --lab=<expired> refuses to bind and exits non-zero with the expected message.
- Running petri serve --lab=<active-but-kubeconfig-missing> refuses to bind, marks the lab as ERROR, exits non-zero.
- Running petri serve --lab=<active> with the reaper enabled, then letting the lab pass TTL during serve's lifetime, results in the lab being destroyed within the reaper interval. The destruction is logged.
- Running petri serve --no-reaper disables the goroutine entirely; expired labs sit untouched for the serve lifetime.
- A lab stranded in CREATING past the stranded threshold gets cleaned up by the reaper or by petri cleanup --expired.
- The investigation document is updated to point at the closing ADR.
- No regression in any existing test.
