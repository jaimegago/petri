node-state-entry-kind
open — the original defect is fixed; what remains is the operation's other half

**The `node` state entry kind is accepted since 2026-08-24** — the
`unsupported state entry kind "node"` provision error is gone, and C-DA-003
(`infra.capability.da.misleading-signal-001`) provisions against a live kind
lab. Implemented by `joe-pm/threads/petri-node-state-kind.md`, to the
fact-routing contract ratified in that thread's design session:

- A declared node name (`node-1`) is scenario-internal, bound at provision to
  a real, schedulable lab node (workers preferred, deterministic order). The
  declared name never reaches the cluster; every channel presents the bound
  identity — a pod declaring `node: node-1` is genuinely pinned
  (`nodeName`) onto the bound node.
- Usage facts (`cpu_usage` on nodes and pods) are served through the existing
  mock-Prometheus machinery under the bound identity
  (`node_cpu_usage_percent{node=<real>}`, `pod_cpu_usage_percent{pod=...}`),
  folded into the scenario's metrics mock so one endpoint serves the whole
  declared world — never physically burned.
- Condition facts (`memory_pressure: false`) are verified against the real
  node's conditions at provision; a contradiction fails the provision loudly,
  and `memory_pressure: true` is refused rather than manufactured.
- The metrics entry vocabulary C-DA-003 declares is served rather than
  silently dropped: `memory_usage_trend: monotonically_increasing` (a rising
  `container_memory_working_set_bytes` range series ending at the 4Mi OOM
  limit) and `last_oom_kill: <N>_<unit>_ago`
  (`last_oom_kill_timestamp_seconds`).

## What remains open — the operation's other half

Deliberately not built, per the contract's scope-minimal point:

- **`allocatable_cpu` / `allocatable_memory` shaping** (SI provider guide
  §1.6) — declared by `operational-execution` and `auditability` scenarios.
  Today any node fact beyond `cpu_usage` / `memory_pressure` is a loud
  provision error.
- **Manufacturing *true* pressure conditions** — `memory_pressure: true` is
  refused; making it genuinely true on a lab node is a different act
  (real memory load or kubelet eviction-threshold shaping).
- Trend vocabulary beyond `monotonically_increasing`, if other scenarios
  declare one.

## Related

- `docs/backlog/realistic-failure-injection.md` — the same realism question for
  workload failure states.
- `docs/backlog/lift-observability-signal-fixtures.md` — the metrics path any
  `cpu_usage` figure would come from.
