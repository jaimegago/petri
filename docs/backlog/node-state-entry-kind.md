node-state-entry-kind
open

`node` is not a supported OASIS state entry kind, so any scenario declaring node
state fails provisioning outright:

    POST /v1/provision status=500
    error="applying precondition state: applying node node-1:
           unsupported state entry kind \"node\""

`pkg/oasis/translate.go` switches on `e.Kind` over namespace, deployment,
configmap, secret, service, serviceaccount, role, rolebinding, hpa, pvc, pod,
dashboard, gitops-application, metrics, traces, alert, events, runbook, ingress,
networkpolicy and logs, and returns that error for anything else.

Blocks `infra.capability.da.misleading-signal-001` (archetype C-DA-003), which
declares:

```yaml
- resource: node/node-1
  cpu_usage: 97%
  memory_pressure: false
```

The scenario is a red-herring test: a `user-service` is OOMKilled while a
neighbouring pod pegs node CPU, and the agent must not blame the CPU. **The node
is the red herring**, so the scenario cannot be provisioned without it.

Observed 2026-08-21 by the first OASIS run over the diagnostic-accuracy
category; recorded in `joe-pm/threads/da-corpus-scope-check.md`.

## What the design has to decide

A kind lands in a kind cluster, and node properties are not freely settable the
way a Deployment's are:

- **`cpu_usage: 97%`** is an observed metric, not a spec field. Producing it
  means scheduling real load onto that node, or serving the figure from the
  metrics fixture path rather than from the cluster.
- **`memory_pressure: false`** is a node condition the kubelet owns.
- **Node identity.** A kind lab's nodes are named by kind. Either the scenario's
  `node-1` maps onto an existing node, or labs gain nodes on demand — which is a
  cluster-topology change, not a manifest.

The cheapest honest option may be that node entries are **annotations onto an
existing node plus metrics fixtures**, rather than a provisioned node. That
choice decides whether an agent inspecting the node sees a consistent story or a
labelled fiction — which is the same question
`docs/backlog/realistic-failure-injection.md` asks about workloads, and the two
should be decided together.

## Related

- `docs/backlog/realistic-failure-injection.md` — the same realism question for
  workload failure states.
- `docs/backlog/lift-observability-signal-fixtures.md` — the metrics path any
  `cpu_usage` figure would come from.
