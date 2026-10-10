# Backlog Index

Active open-work items, one row per file directly under `docs/backlog/` (excluding
this index and the `done/` archive). This is a **derived summary, not a source of
truth** — regenerate it from the directory whenever items are added, removed, or
their status changes.

Per row: **slug** = filename without `.md`; **title** = the file's first line (heading
marker stripped); **status** = the leading clause of the file's `Status:` line;
**priority** = the value of the file's `Priority:` line — an item with no `Priority:`
line renders as a **blank cell, never a default value**.

## Item file format

An item file's first line is its title, with or without a leading heading marker. A
`Status:` line follows, carrying `open` or `in-progress`. Two optional lines may follow
the status line, before the body:

- **`Priority:`** — exactly one of `now`, `next`, `later`. Absent means untriaged.
- **`Blocked-by:`** — either a backlog slug or the form `external (short note)`. One-
  directional; no reverse edge is maintained.

## Archiving a completed item

On completion, move the item's file to `done/`, regenerate this index, and repoint any
relative link the move breaks.

| Slug | Title | Status | Priority |
|------|-------|--------|----------|
| [`azure-postgres-reachability`](azure-postgres-reachability.md) | azure-postgres-reachability | open | |
| [`lift-observability-signal-fixtures`](lift-observability-signal-fixtures.md) | lift-observability-signal-fixtures | open | |
| [`ministack-aws-lab`](ministack-aws-lab.md) | ministack-aws-lab | open | |
| [`mock-metrics-realism`](mock-metrics-realism.md) | mock-metrics-realism | open | |
| [`realistic-failure-injection`](realistic-failure-injection.md) | realistic-failure-injection | open | |
