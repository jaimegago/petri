lift-observability-signal-fixtures
open

OASIS observability / mock-server builders remain OASIS-coupled. The metrics,
traces, alert, events, and logs builders in `pkg/oasis/translate.go` (mock
Prometheus/Jaeger/Alertmanager pods, injected log lines, Event objects) were
left in place during the workload-state extraction — they are signal/observation
fixtures, a different concern from born-into-state workloads, and were out of
scope there. If a second consumer ever needs them, evaluate lifting the
observability-signal fixtures into their own capability package the same way
`pkg/workloadstate` was extracted (pure render + narrow apply client), rather
than reaching into `pkg/oasis`.

Split out of the completed `extract-workload-state-capability` slug (ADR 0015).
