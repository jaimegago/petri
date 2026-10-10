# mock-metrics-realism
Status: open

Found 2026-10-10 by `joe-pm/threads/da3-real-app.md` on the DA-3 run. Two
ways the mock-Prometheus fixture contradicts or announces itself once the
workload is a real application:

- **The metrics disagree with the workload.** The real `svc` OOM cycle is ~13s
  per run; the served `memory_usage_trend` / `last_oom_kill` series are the
  scenario's declared fiction and do not match the restarts and timestamps the
  agent can read from the pod. An agent cross-checking metrics against pod
  status finds two stories.
- **The mock names itself.** The serving pod is `mock-prometheus-*`, readable
  by the agent — a caption, under `realistic-fixtures-phase` contract item 3.

Relates to `lift-observability-signal-fixtures.md` and slice 3 of
`joe-pm/queue/realistic-fixtures-phase.md`, which reconciles declared signals
with measured ones.
