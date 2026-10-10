# lab-host-port-collision
Status: open

petri's kind config maps fixed host ports 30080/30443, so a second petri lab on
the same host fails to start while one is running. Found 2026-10-10 by
`joe-pm/threads/node-allocatable-shaping.md`, whose live check had to use an
uncommitted build without those mappings to avoid breaking a concurrent e2e lab.
Blocks running two labs at once (cf. `joe-pm/queue/parallel-scenario-execution.md`).
Allocate host ports per lab (or drop the mapping where nothing uses it) and
record them in lab state.
