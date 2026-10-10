# provision-ready-before-pods
Status: open

`/v1/provision` was observed once returning `ready` while the scenario's pods
were still starting (2026-10-10, `joe-pm/threads/node-allocatable-shaping.md`,
a healthy-state provision on a fresh OASIS lab). Not investigated. Reproduce,
and if confirmed, readiness must wait for every declared workload's rollout —
an agent handed a half-started environment reads a different cluster than the
scenario declares.
