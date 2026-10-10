node-state-entry-kind
Status: done — both halves built; manufacturing true pressure dropped

**Done 2026-10-10.** The node operation of SI provider guide §1.6 is complete
to the extent any scenario declares it. The first half landed 2026-08-24
(`joe-pm/threads/petri-node-state-kind.md`); the second half — allocatable —
landed with `joe-pm/threads/node-allocatable-shaping.md`, to rulings settled
with the maintainer that day.

## First half — the `node` kind and its fact routing (2026-08-24)

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

## Second half — allocatable (2026-10-10)

- **Allocatable is a lab property, shaped once; a scenario only verifies it.**
  No provision reconfigures a node — nodes are shared by every scenario on the
  lab.
- **Shaping at `petri create --oasis` (local/kind).** After Calico is ready,
  each worker's kubelet is given a `systemReserved` sized so `status.allocatable`
  is exactly `cpu: 4000m`, `memory: 8Gi` (`OASISNodeAllocatableCPU` /
  `OASISNodeAllocatableMemory`, `pkg/provisioners/local/shape.go`) — C-OE-002's
  homogeneous shape. The arithmetic subtracts the kubelet's live
  `evictionHard[memory.available]` and any `kubeReserved`, read from configz.
  A host too small to reach the target still gets its lab, **unshaped**, with
  a warning; the outcome (shaped/unshaped, reason, resulting allocatable per
  worker) is recorded in lab state as `Cluster.NodeShape`.
- **Verification at provision.** `allocatable_cpu` / `allocatable_memory` on a
  node entry are compared by exact quantity equality with the bound node's
  real `status.allocatable`; a mismatch — including an unshaped lab — is a loud
  provision error naming declared vs actual and the lab's recorded shaping.
- **`current_utilization: <N>%`** is the node CPU usage fact, served exactly as
  `cpu_usage` is. Declaring both on one entry is refused as ambiguous.
- **Manufacturing `memory_pressure: true` is dropped** — no scenario declares
  it. It stays refused.

The node fact vocabulary is now closed at `cpu_usage`, `current_utilization`,
`memory_pressure`, `allocatable_cpu`, `allocatable_memory`; anything else is a
loud error. In `oasis-spec`, C-AU-002 no longer declares `allocatable_cpu: low`
nodes, and guide §1.6 states allocatable as a lab property the scenario asserts.

Still not built, because no scenario declares it: trend vocabulary beyond
`monotonically_increasing`.

## Related

- `docs/backlog/realistic-failure-injection.md` — the same realism question for
  workload failure states.
- `docs/backlog/lift-observability-signal-fixtures.md` — the metrics path any
  `cpu_usage` figure would come from.
