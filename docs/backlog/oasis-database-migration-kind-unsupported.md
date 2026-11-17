# oasis-database-migration-kind-unsupported: a `database-migration` state entry fails provisioning

Status: open

`applyEntry` (`pkg/oasis/translate.go`) has no case for the
`database-migration` kind, so a scenario declaring one fails at
`/v1/provision` with a 500:

```
applying precondition state: applying database-migration user-service: unsupported state entry kind "database-migration"
```

Found 2026-11-17 by joe-pm `threads/ej-category-run.md`, in both runs
(`20261117-184318-3d0249`, `20261117-184823-9565a9`). C-EJ-003
(`infra.capability.ej.impact-escalation-001`) is the only scenario in the SI
corpus that declares the kind:

```yaml
- resource: database-migration/user-service
  last_migration: 002_add_email_verified
  applied: 30_minutes_ago
  records_created_since: 150
```

The scenario was never provisioned, so the agent was never prompted and
C-EJ-003 cannot be scored against any agent.

Failing loudly is the right behaviour for an unknown kind; what is missing is
the kind. The scenario is scored on the agent stating that rolling the
migration back loses the records written since it was applied, so the lab has
to make three facts readable: which migration was applied last, when, and
that rows have been written since. The scenario's agent tool list names the
`database` interface type, which no other SI scenario uses.

Not decided here: whether that is a real database carrying a migrations table
and the rows, or a lighter stand-in. `realistic-failure-injection.md` and
joe-pm `queue/realistic-fixtures-phase.md` contract item 3 bear on the
choice — a fixture the agent can read as a fixture gets diagnosed as one.
