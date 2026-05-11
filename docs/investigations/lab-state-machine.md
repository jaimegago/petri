# Lab state machine — audit

> **Status: investigation closed.** See [docs/decisions/0013-lab-state-machine-hybrid-reaper.md](../decisions/0013-lab-state-machine-hybrid-reaper.md) for the fix.

- Date: 2026-05-11
- Scope: how `Lab.Status` and `Lab.ExpiresAt` interact today across CLI, orchestrator, and state layers.
- Method: code reading only. No runtime verification.

> **Location note.** `docs/investigations/` is new. Repo convention has `docs/decisions/` for ADRs and `docs/prompts/` for prompt archives, but no prior home for pre-decision diagnostic write-ups. A separate folder keeps these diffable from formal ADRs while staying out of the human-docs tree.

## 1. State machine as implemented

Statuses are declared in [pkg/types/lab.go:14-21](../../pkg/types/lab.go#L14-L21); the allowed transition table lives in [pkg/types/lab.go:80-95](../../pkg/types/lab.go#L80-L95).

| Status | Entered from | Entered by | Exited to | Exit triggers |
|---|---|---|---|---|
| `CREATING` | (none — initial) | [internal/cli/create.go:133](../../internal/cli/create.go#L133) on `petri create` | `ACTIVE`, `ERROR` | success path in [pkg/orchestrator/create.go:209](../../pkg/orchestrator/create.go#L209) / [:502](../../pkg/orchestrator/create.go#L502); failure path in [:89](../../pkg/orchestrator/create.go#L89) |
| `ACTIVE` | `CREATING` | orchestrator finalisation (above) | `EXPIRING`, `DESTROYING`, `ERROR` (per table) | only `DESTROYING` and `ERROR` are actually written anywhere; see §2 |
| `EXPIRING` | `ACTIVE` (per table) | **no writer exists in the codebase** | `DESTROYING`, `ACTIVE`, `ERROR` | dead state |
| `DESTROYING` | `ACTIVE` / `EXPIRING` / `ERROR` | [internal/cli/destroy.go:50](../../internal/cli/destroy.go#L50), [internal/cli/cleanup.go:75](../../internal/cli/cleanup.go#L75), [pkg/orchestrator/cleanup.go:60](../../pkg/orchestrator/cleanup.go#L60) | `DESTROYED`, `ERROR` | [pkg/orchestrator/destroy.go:75](../../pkg/orchestrator/destroy.go#L75) / [:69](../../pkg/orchestrator/destroy.go#L69) |
| `DESTROYED` | `DESTROYING` | as above | (terminal) | only via `DeleteLab` from a re-create with same name (see §4) |
| `ERROR` | `CREATING` / `ACTIVE` / `EXPIRING` / `DESTROYING` (per table) | [pkg/orchestrator/create.go:89](../../pkg/orchestrator/create.go#L89), [pkg/orchestrator/destroy.go:69](../../pkg/orchestrator/destroy.go#L69), [pkg/orchestrator/cleanup.go:70](../../pkg/orchestrator/cleanup.go#L70) | `DESTROYING` | re-running destroy/cleanup |

Key findings:
- `EXPIRING` is declared and allowed in the transition table but **no code ever sets it**. It is conceptually dead.
- `IsExpired()` ([pkg/types/lab.go:75-77](../../pkg/types/lab.go#L75-L77)) is purely informational; expiry never changes `Status`.

## 2. Every code path that mutates `Status`

| Site | New status | Trigger |
|---|---|---|
| [internal/cli/create.go:133](../../internal/cli/create.go#L133) | `CREATING` | initial `CreateLab` insert |
| [pkg/orchestrator/create.go:91](../../pkg/orchestrator/create.go#L91) | `ERROR` | any failure during `Create` (after rollback) |
| [pkg/orchestrator/create.go:209](../../pkg/orchestrator/create.go#L209) | `ACTIVE` | local-lab finalisation |
| [pkg/orchestrator/create.go:502](../../pkg/orchestrator/create.go#L502) | `ACTIVE` | cloud-lab finalisation |
| [internal/cli/destroy.go:50](../../internal/cli/destroy.go#L50) | `DESTROYING` | user runs `petri destroy <name>` |
| [pkg/orchestrator/destroy.go:69](../../pkg/orchestrator/destroy.go#L69) | `ERROR` | non-force destroy hit errors |
| [pkg/orchestrator/destroy.go:75](../../pkg/orchestrator/destroy.go#L75) | `DESTROYED` | destroy completed (or `--force` swallowed errors) |
| [internal/cli/cleanup.go:75](../../internal/cli/cleanup.go#L75) | `DESTROYING` | user runs `petri cleanup --expired` |
| [pkg/orchestrator/cleanup.go:60](../../pkg/orchestrator/cleanup.go#L60) | `DESTROYING` | `StartCleanupLoop` tick (not wired — see §4) |
| [pkg/orchestrator/cleanup.go:70](../../pkg/orchestrator/cleanup.go#L70) | `ERROR` | background teardown failed |
| [pkg/orchestrator/cleanup.go:80](../../pkg/orchestrator/cleanup.go#L80) | `DESTROYED` | background teardown succeeded |

`UpdateLab` is also called without changing status — to persist `ExpiresAt` in `extend` ([internal/cli/extend.go:58](../../internal/cli/extend.go#L58)) and to persist metadata mid-create ([pkg/orchestrator/create.go:166](../../pkg/orchestrator/create.go#L166), [:437](../../pkg/orchestrator/create.go#L437)).

`DeleteLab` (record removal, not status change) is called only at [internal/cli/create.go:152](../../internal/cli/create.go#L152) — the re-create-with-same-name guard.

## 3. Every code path that reads `ExpiresAt`

| Site | Use |
|---|---|
| [internal/cli/create.go:136](../../internal/cli/create.go#L136) | set on insert |
| [internal/cli/extend.go:54-55](../../internal/cli/extend.go#L54-L55) | extend by delta |
| [internal/cli/info.go:66-69](../../internal/cli/info.go#L66-L69) | **display only** — appends `(EXPIRED)` suffix; does **not** mutate status |
| [internal/cli/list.go:81-84](../../internal/cli/list.go#L81-L84) | **display only** — appends `(expired)` suffix; does **not** mutate status |
| [pkg/state/db.go:175-178](../../pkg/state/db.go#L175-L178) | `ListLabs` filter: `expires_at > now` when `IncludeExpired=false` (default) |
| [pkg/state/mock.go:106](../../pkg/state/mock.go#L106) | mock equivalent of the above |
| [pkg/state/db.go:206-211](../../pkg/state/db.go#L206-L211) | `FindExpiredLabs` for cleanup |
| [pkg/state/mock.go:115-129](../../pkg/state/mock.go#L115-L129) | mock equivalent |
| [pkg/orchestrator/create.go:412](../../pkg/orchestrator/create.go#L412), [:779](../../pkg/orchestrator/create.go#L779) | print expiry in success banner |
| [pkg/orchestrator/export.go:44](../../pkg/orchestrator/export.go#L44), [:89](../../pkg/orchestrator/export.go#L89) | included in credential-bundle JSON / printed |

No read site outside the cleanup helpers gates behaviour on TTL. In particular, `petri serve --lab <name>` checks `lab.Status != ACTIVE` ([internal/cli/serve.go:183](../../internal/cli/serve.go#L183)) but never consults `ExpiresAt`, so an expired-but-still-`ACTIVE` lab will happily back a serve session.

## 4. The "automatic transition" mystery

**There is no production code path that transitions a lab from `ACTIVE` → `DESTROYED` without an explicit user invocation.**

The leading hypothesis (create.go around line 146–155) is **refuted**:
- [internal/cli/create.go:147-155](../../internal/cli/create.go#L147-L155) only fires when an existing lab record is `DESTROYED` or `ERROR`; for anything else (`ACTIVE`, `CREATING`, …) it errors out with `"lab %q already exists"`. It also doesn't transition — it **`DeleteLab`s** the prior record. So that branch can't be how the user saw `ACTIVE` → `DESTROYED`.

The only `ACTIVE` → `DESTROYED` writers in the tree are:
1. `petri destroy <name>` ([internal/cli/destroy.go:33-91](../../internal/cli/destroy.go#L33-L91) → [pkg/orchestrator/destroy.go:75](../../pkg/orchestrator/destroy.go#L75)).
2. `petri cleanup --expired` ([internal/cli/cleanup.go:30-115](../../internal/cli/cleanup.go#L30-L115)).
3. `Orchestrator.StartCleanupLoop` ([pkg/orchestrator/cleanup.go:15](../../pkg/orchestrator/cleanup.go#L15)) — **but `grep` confirms this is never invoked outside `pkg/orchestrator/*_test.go`.** `petri serve` ([internal/cli/serve.go](../../internal/cli/serve.go)) does not start it; neither does `buildOrchestrator` ([internal/cli/root.go:196-241](../../internal/cli/root.go#L196-L241)).

Conclusion: the second observation cannot be explained by the current code. Likely causes, in decreasing order: (a) a `petri destroy` or `petri cleanup --expired` invocation the user has since forgotten — perhaps from a shell history line, an editor task, or a separate terminal; (b) a previous build of petri that did wire the background loop and persisted state across binary versions; (c) recall error. A `git log -- pkg/orchestrator/cleanup.go internal/cli/cleanup.go` and the user's shell history would settle it.

The **first** observation (`Status: ACTIVE`, `Expires: <past> (EXPIRED)`, no reap) is the expected behaviour given the code: nothing in this repo reaps without an explicit `petri cleanup --expired`.

## 5. Inconsistencies

1. **`ACTIVE (EXPIRED)` display.** `info` and `list` both print `(EXPIRED)`/`(expired)` next to a status that the system itself never reconciles. Same observation in two formats — the second observation refutes the first.  ([internal/cli/info.go:64-70](../../internal/cli/info.go#L64-L70), [internal/cli/list.go:81-88](../../internal/cli/list.go#L81-L88))
2. **`petri list` hides expired labs by default.** `IncludeExpired: !aliveOnly` with `aliveOnly` defaulting to `false` actually means *include* expired, so the default list **does** show them. But — confusingly — the flag is named `--alive` and the default `ListFilter.IncludeExpired` is `false`, so anyone calling `ListLabs` from a future Go caller without setting `IncludeExpired=true` will silently lose past-TTL records.  ([internal/cli/list.go:38,54](../../internal/cli/list.go#L38), [pkg/state/db.go:175](../../pkg/state/db.go#L175))
3. **`EXPIRING` is a dead status.** Declared, allowed in transitions, never written. New contributors will assume it represents the past-TTL-not-yet-reaped state and write code accordingly.  ([pkg/types/lab.go:17,83-84](../../pkg/types/lab.go#L17))
4. **Stranded `CREATING` labs.** If the petri process crashes mid-`Create`, the lab is left in `CREATING` forever. Nothing reconciles it; `cleanup --expired` will pick it up only after TTL elapses (and `CanTransitionTo(DESTROYING)` returns `false` for `CREATING`, so cleanup will log "cannot transition to DESTROYING" and skip — see [pkg/types/lab.go:82](../../pkg/types/lab.go#L82) and [pkg/orchestrator/cleanup.go:55](../../pkg/orchestrator/cleanup.go#L55) / [internal/cli/cleanup.go:70](../../internal/cli/cleanup.go#L70)). Permanent stuck record.
5. **`ERROR` after destroy is hard to clear.** `ERROR` → `DESTROYING` is allowed, but the destroy command requires `CanTransitionTo(DESTROYING)` which is true. Fine — except `cleanup --expired` and `destroy` both pass through `DESTROYING`; if it fails again the cycle repeats with no way to manually mark a lab DESTROYED without infrastructure cleanup. (Minor.)
6. **`petri serve --lab` and TTL.** Serve refuses non-`ACTIVE` labs but is happy to serve expired ones. A scenario running against an expired lab will (eventually) be reaped under the user's feet if a cleanup is run — but right now nothing reaps, so the only failure mode is human surprise.  ([internal/cli/serve.go:183](../../internal/cli/serve.go#L183))
7. **`DESTROYED` records linger on disk.** `removeLabWorkDir` is called by `Destroy` and `destroyExpiredLab` ([pkg/orchestrator/destroy.go:65](../../pkg/orchestrator/destroy.go#L65), [pkg/orchestrator/cleanup.go:78](../../pkg/orchestrator/cleanup.go#L78)) — but if a manual `kind delete cluster` was done without `petri destroy`, the lab stays `ACTIVE` in state forever with a stale kubeconfig path. `petri info` will still emit `export KUBECONFIG=…` pointing at a dead file.
8. **Mock vs DB filter divergence.** `dbManager.ListLabs` uses `expires_at > ?` ([pkg/state/db.go:177](../../pkg/state/db.go#L177)); `MockManager.ListLabs` uses `time.Now().After(lab.ExpiresAt)` ([pkg/state/mock.go:106](../../pkg/state/mock.go#L106)). Functionally equivalent at the boundary, but the mock's behaviour at exact-equality differs (SQL strict-greater-than excludes the boundary; `After` excludes it too — actually consistent). No bug; flagging only because the two should be kept aligned.

## 6. Design space for reconciling `Status` with TTL

| Option | Pros | Cons | Effort |
|---|---|---|---|
| **A. Fix display only.** Make `info`/`list` show `EXPIRED` *as* the status when `IsExpired() && Status==ACTIVE`. Leave reaping explicit. | Smallest change. No behaviour shift. Removes the most visible contradiction. | Lab is still `ACTIVE` in the DB; downstream consumers (`serve --lab`, future API) still see stale truth. | XS (one-line per call site) |
| **B. Lazy on-read reconciliation.** In `GetLab`/`GetLabByName`/`ListLabs`, if `Status==ACTIVE && IsExpired()`, write back `EXPIRING` (or a new `EXPIRED` status). | Self-healing without a daemon. No new lifecycle process to operate. | Writes on read are surprising; concurrent readers race. Doesn't actually destroy infra — just relabels. Needs `EXPIRING` to come alive (or new status added). Adds a `UpdateLab` to every read. | S (state layer + status semantics) |
| **C. Background reaper in `petri serve`.** Call `StartCleanupLoop` from `runServe` after preflight succeeds. | Reuses code that already exists and is tested. Natural for OASIS users since serve is the long-lived process. | Only fires when serve is running. CLI-only users (no serve) still see the inconsistency. Reaper has no company config so uses metadata-only teardown ([pkg/orchestrator/cleanup.go:91](../../pkg/orchestrator/cleanup.go#L91)) — fine for local labs, lossy for cloud labs that need IaC `destroy`. | S (one wire-up + lifecycle wiring with serve's signal-cancel ctx) |
| **D. Standalone `petri reaper` daemon (or systemd timer).** Long-running process that loops `cleanup --expired`. | Decoupled from serve. Operates regardless of who's running what. | New deployment surface. Single-writer assumption gets harder to enforce (operator must not run two). | M |
| **E. Explicit-only + UX nudges.** Keep reaping manual; make `info`/`list` shout louder when expired; add a "you have N expired labs, run `petri cleanup --expired`" hint at the top of any command that touches state. | Honest about who owns the lifecycle. No surprise destruction of infra a user wanted to keep. | Drift continues to accumulate; "petri info shows ACTIVE for an expired lab" is still technically true. | XS-S |
| **F. Hybrid: A + lazy-mark-EXPIRING on read + reaper in serve.** Display fix immediately, lazy status reconciliation for visibility, background reap when serve is running. | Layered defence; works for all command surfaces. | Most moving parts. `EXPIRING` semantics need to be specified (does `petri serve --lab` allow it? does `petri destroy` accept it? what does `list` show by default?). | M |
| **G. Drop `EXPIRING` and `ExpiresAt`-as-state; treat TTL purely as a hint.** Status is only what someone wrote. TTL is metadata. `(EXPIRED)` is a *render* annotation, never a state. | Most internally consistent — TTL never lies because it doesn't claim anything. | Reaping has to come from a separate mechanism (any of B/C/D). Existing `--alive` / `IncludeExpired` filter semantics need re-examining. | S |

## 7. Open questions

1. **Was the prior auto-destroy observation real?** Code says no. Confirming or refuting needs the user's shell history or a `git log pkg/orchestrator/cleanup.go internal/cli/cleanup.go` walk to see if `StartCleanupLoop` was ever wired into `serve` and later removed.
2. **What should `petri serve --lab <expired-active-lab>` do?** Refuse? Warn and continue? Extend on the fly? The current behaviour (silently serve) is almost certainly wrong, but the "right" answer depends on whether labs are expected to outlive scenarios.
3. **Does the existing `--force` semantics in `cleanup`/`destroy` ([internal/cli/cleanup.go:86](../../internal/cli/cleanup.go#L86), [internal/cli/destroy.go:28](../../internal/cli/destroy.go#L28)) match the intent for a background reaper?** The auto-cleanup loop already forces; making serve drive it means scenario users implicitly opt into force-destroy on TTL.
4. **Is a `DELETED`/`PURGED` distinct from `DESTROYED` worth introducing?** Today `DESTROYED` covers both "we destroyed it" and "we kind-of gave up." If we add lazy reconciliation, distinguishing "TTL elapsed, infra still present" from "TTL elapsed, infra confirmed gone" would simplify recovery flows.
5. **What's the contract between `petri info` and disk reality?** Should it stat the kubeconfig before printing the `export KUBECONFIG=` hint? Out of scope for this audit but adjacent to inconsistency #7.

---

## Appendix: command-by-command status mutations

```
petri create           insert CREATING                                       ── create.go:133
                       └─ orchestrator.Create
                          ├─ success   → ACTIVE                              ── orchestrator/create.go:209, :502
                          └─ failure   → ERROR                               ── orchestrator/create.go:89
                       (re-create same name & prior status ∈ {DESTROYED, ERROR})
                          → DeleteLab prior record                           ── internal/cli/create.go:152

petri destroy <n>      ACTIVE/EXPIRING/ERROR → DESTROYING                    ── internal/cli/destroy.go:50
                       └─ orchestrator.Destroy
                          ├─ success   → DESTROYED                           ── orchestrator/destroy.go:75
                          └─ errors    → ERROR                               ── orchestrator/destroy.go:69

petri cleanup --expired  loop over FindExpiredLabs():
                       ACTIVE/EXPIRING/ERROR → DESTROYING                    ── internal/cli/cleanup.go:75
                       └─ orchestrator.Destroy (Force=true)                  ── internal/cli/cleanup.go:86
                          → DESTROYED (or ERROR if non-force; never here)

petri extend           UpdateLab (ExpiresAt only; Status unchanged)          ── internal/cli/extend.go:58

petri info             read-only; renders (EXPIRED) suffix                   ── internal/cli/info.go:67
petri list             read-only; renders (expired) suffix                   ── internal/cli/list.go:82

petri serve            read-only on status; refuses non-ACTIVE; ignores TTL  ── internal/cli/serve.go:183

Orchestrator.StartCleanupLoop   ── NOT WIRED in production. Only invoked by tests.
```
