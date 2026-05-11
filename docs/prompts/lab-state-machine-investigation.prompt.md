# Prompt: Audit petri's lab state machine and TTL reaping

Generated: 2026-05-11
Model: claude-opus-4-7 (1M context)
Target:
- docs/investigations/lab-state-machine.md (new)
- docs/prompts/lab-state-machine-investigation.prompt.md (this file)

## Specification

Goal: investigate petri's lab state machine and produce a written analysis (no code changes) of how lab Status, ExpiresAt, and the various reaping/transition mechanisms interact today. Output is a single markdown document I can read and then drive a fix prompt from.

Context: a recent observation showed `petri info <name>` reporting `Status: ACTIVE` and `Expires: <past-timestamp> (EXPIRED)` for a lab whose TTL had elapsed 11 hours earlier. Separately, an earlier session observed a different expired lab transitioning from ACTIVE to DESTROYED between sessions, without any explicit `petri cleanup --expired` invocation. These two observations are in tension: sometimes expired labs get reaped automatically, sometimes they don't. The investigation needs to explain why.

Read pkg/state/db.go, pkg/state/state.go, pkg/state/mock.go, internal/cli/create.go, internal/cli/info.go (if it exists; otherwise the equivalent), internal/cli/list.go, internal/cli/cleanup.go, internal/cli/destroy.go, internal/cli/serve.go, and any other files that reference Status, ExpiresAt, FindExpiredLabs, MarkExpired, or DESTROYED.

Produce docs/investigations/lab-state-machine.md with the following sections (create docs/investigations/ if it doesn't exist; if there's a more appropriate location per existing repo conventions, use that and explain the choice).

1. State machine as currently implemented. Enumerate every Status value that exists in the code. For each, describe the conditions under which a lab enters and leaves that status. Include the relevant file:line references.

2. Every code path that mutates lab Status. List every call site of UpdateLab (or whichever function persists status changes) and what status it sets, in what circumstance. Include CLI command paths, server paths, and any background work.

3. Every code path that reads ExpiresAt. List every call site that consults the TTL field and describe how it uses it — purely informational, gate on read, trigger reaping, etc.

4. The "automatic transition" mystery. Identify the specific code path that transitioned the previously-observed expired lab from ACTIVE to DESTROYED without explicit `cleanup --expired`. The leading hypothesis is internal/cli/create.go around line 146-153 ("removing previous terminal lab record" — but that requires re-creating with the same name; if not that, what?). Confirm or refute with a clear reference to the responsible code.

5. Inconsistencies. List every situation where the current implementation produces user-visible confusing or contradictory state. The "ACTIVE (EXPIRED)" display is one. Are there others? Stranded CREATING labs after a crash? DESTROYED labs that still have kubeconfig files on disk? Inconsistencies between in-memory mock and on-disk DB state?

6. Design space. Enumerate the reasonable design options for fixing the inconsistencies. For each, list pros, cons, and rough implementation effort. Examples:
   - Background reaper goroutine on petri serve startup
   - Lazy on-read reaping in info/list/serve handlers
   - Explicit-only reaping via cleanup --expired plus a fix to info's display
   - Hybrid (background for ACTIVE-past-TTL, lazy for status display refresh)
   Do not recommend one. The recommendation will be made after reading the analysis. The investigation's job is to expose the tradeoff space, not to choose.

7. Open questions. Anything the investigation surfaced that you couldn't determine by code reading and would need a runtime test or a design discussion to resolve.

Hard constraints:
- No code changes in this session. The deliverable is the markdown document.
- No new tests. The deliverable is the markdown document.
- Cite file:line for every claim. Treat this like a small audit.
- The document should be short and dense, not exhaustive. If a section runs over ~500 words, the analysis is probably losing focus — push specifics into appendices.

Archival
- Save this prompt as docs/prompts/lab-state-machine-investigation.prompt.md per the existing convention.
