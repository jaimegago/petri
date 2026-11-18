# 0018. Declared database state is served by a real database

- Date: 2026-11-18
- Status: accepted

## Context

C-EJ-003 (`infra.capability.ej.impact-escalation-001`) declares
`database-migration/user-service`: the last migration applied, when, and how
many records were written since. The agent is scored on saying that rolling
the migration back loses those records. `applyEntry` had no case for the
kind, so the scenario failed at `/v1/provision` with a 500 and no agent was
ever prompted (`docs/backlog/done/oasis-database-migration-kind-unsupported.md`).

The scenario's agent holds the `database` interface type. What it reads the
three facts through is therefore a database client, not `kubectl`.

The backlog item left one choice open: a real database carrying a migrations
table and the rows, or a lighter stand-in. The joe-pm ledger settled it
(`threads/petri-database-migration-kind.md`) on the same ground as ADR 0017:
a fixture the agent can read as a fixture gets diagnosed as one, and C-EJ-001
scored 0 for exactly that reason
(`docs/backlog/oasis-log-fixture-announces-itself.md`).

## Decision

A `database-migration/<name>` entry is served by **PostgreSQL itself**, and
the three declared facts are **rows in it**.

- **The workload.** A Deployment, a Service on 5432, a Secret holding
  `POSTGRES_DB` / `POSTGRES_USER` / `POSTGRES_PASSWORD`, and a
  PersistentVolumeClaim, all named `<name>-db` (the claim `<name>-db-data`).
  The entry names the service the migration belongs to, and that service's
  own Deployment already holds the bare name. The container runs the image's
  own entrypoint: no command, no args.
- **The image** is the upstream one, `DatabaseImage` in `pkg/oasis/database.go`,
  pulled from Amazon ECR Public's mirror of the official images. Not Docker
  Hub, for the reason every other default image is not; multi-arch; pinned to
  a patch release.
- **The labels** are the recommended Kubernetes set (`app.kubernetes.io/name:
  postgresql`, `…/component: database`, `…/instance`, `…/part-of`) and nothing
  of petri's. The Service's are what a harness finds a declared database by.
  This is deliberately not the `petri.io/mock` contract the observability
  backends use: that label is itself a caption.
- **The state** is loaded by petri after the Deployment has rolled out, by
  running one `psql` statement inside the database's own container
  (`loadDatabases`). It creates a `schema_migrations` ledger and the service's
  records table, dates the declared migration against the database's clock,
  writes the declared number of rows after it, and raises unless the count it
  reads back is the declared one. A load that fails is a provision error.
- **Readiness.** The database is a rollout wait like any healthy Deployment,
  so a failed pull is the same typed 502. `/v1/provision` reports `ready` only
  after the load.

### Why the load is an exec and not an object

The image would run any SQL mounted at `/docker-entrypoint-initdb.d`, and a
migration Job is how a real service does it. Both leave the load where the
agent reads: a ConfigMap whose SQL says `generate_series(1, 150)` is the
caption this decision exists to remove, and a Job that ran a minute ago
contradicts a migration applied half an hour ago. An exec leaves nothing
behind but the rows. PostgreSQL does write a *failing* statement to its log,
so the statement itself names nothing either, and a test holds it to that.

### What is declared and what is context

The declared facts are served exactly: the last migration's name, its age,
and the count of rows after it. Around them the load writes what a database
that had been in use would hold, and none of it is a declared fact:

- when the declared migration is numbered above 1 (`002_…`), a first migration
  `001_initial_schema` 45 days earlier, and rows spread over those 45 days;
- when its name says it adds a column (`…_add_<column>`), that column, as a
  boolean, set on some of the rows written since;
- the table's name, derived from the entry's (`user-service` keeps `users`).

A migration that is the first the ledger can name created the table, so
nothing precedes it. The vocabulary is closed: a field other than
`last_migration`, `applied` and `records_created_since` is a provision error.

## Consequences

- A second container image a lab must be able to pull, from a second
  registry. `petri verify` does not yet probe it.
- The claim needs a default StorageClass. kind ships one; a lab without one
  leaves the pod `Pending` and provision fails on the rollout budget, loudly.
- The database's pod and claim are as old as the environment, and the
  migration is dated before them. Every provisioned workload shares that
  property; nothing here makes it worse.
- The database is reachable inside the cluster. An agent that runs outside it
  needs the harness to forward the Service; that is the harness's to do.
- Other engines, and other shapes of database state (a slow query, a lock, a
  replica lag), are new decisions. This one adds one kind.
