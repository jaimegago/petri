# lab-host-port-collision
Status: done — ingress host ports allocated per lab and recorded in lab state

**Done 2026-10-10** (`joe-pm/threads/lab-host-port-collision.md`). The mapping
is used — ingress-nginx's NodePorts 30080/30443 are how `petri info` reaches
the frontend, Prometheus and Grafana on a local lab — so it was kept and its
host side made per-lab: two free ephemeral ports are drawn at create, written
into the kind config, recorded as `Cluster.IngressHostPorts`, printed by
`petri create` and `petri info`, and rendered into Prometheus's and Grafana's
external URLs. Labs recorded before the change report the legacy pair. No
consumer outside petri depended on 30080/30443. Live: two 1-node labs ran at
once, each serving Prometheus on its own port. The untagged kind-creating
`TestIntegration_CreateDeleteCluster` (and the live-cluster
`TestIntegration_GetNodeCount`) moved behind `//go:build integration`.


petri's kind config maps fixed host ports 30080/30443, so a second petri lab on
the same host fails to start while one is running. Found 2026-10-10 by
`joe-pm/threads/node-allocatable-shaping.md`, whose live check had to use an
uncommitted build without those mappings to avoid breaking a concurrent e2e lab.
Blocks running two labs at once (cf. `joe-pm/queue/parallel-scenario-execution.md`).
Allocate host ports per lab (or drop the mapping where nothing uses it) and
record them in lab state.
