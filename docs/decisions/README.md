# Architecture Decision Records

This directory records non-obvious architectural choices made in petri. Each
ADR captures a decision that future-you (or a new contributor) is likely to
question — choices whose rationale won't be obvious from the code or commit
message a year later.

## File convention

- One decision per file, named `NNNN-slug.md` where `NNNN` is a four-digit
  zero-padded sequence number assigned at the time the ADR is written.
- Numbers are append-only. Never reuse a number, even if the underlying
  decision is later reversed (use the `Status` field for that).

## File structure

Each ADR has the following sections:

```markdown
# NNNN. Short title

- Date: YYYY-MM-DD
- Status: accepted | superseded by NNNN | deprecated

## Context

What problem is this ADR addressing? What constraints, observations, or
incidents motivated the decision?

## Decision

The choice that was made. State it plainly. If alternatives were considered
and rejected, name them and say why.

## Consequences

What does this make easier? What does it make harder? Anything a future
reader should know before "fixing" the decision?
```

The `Date` field is the date the ADR was written, not necessarily the date
the decision was made. ADRs may be backfilled for earlier decisions; the
prose should make it clear when this is the case.

## When to add an ADR

Add a new ADR when:

- A choice constrains future work or is load-bearing for a subsystem
  (e.g. "all credentials encrypted at rest with AES-256-GCM").
- The reasoning isn't obvious from reading the code.
- A reasonable engineer would consider an alternative and need context to
  understand why this path was taken.
- The decision is the kind of thing that would be "fixed" back if removed
  without context (the named-return Duration gotcha is a small example).

Do not add an ADR for routine implementation details, refactors, or choices
the language/framework makes for you. ADRs are for cross-cutting decisions,
not changelogs.

## Superseding an ADR

If a later decision overrides an earlier one, write a new ADR and set the
older one's status to `superseded by NNNN`. Do not edit the old ADR's prose
in place — its job is to record what was true at the time it was accepted.
