collapse-duplicate-default-util-image
open

`defaultUtilImage` is duplicated across packages. `pkg/oasis` keeps its own
`defaultUtilImage` (for the `logs` builder that stays in OASIS) while
`pkg/workloadstate` has `DefaultUtilImage`; both hold the same pinned
registry.k8s.io busybox. This is intentional today — it avoids an import edge
between the two packages — but if the logs builder is ever extracted (see
[[lift-observability-signal-fixtures]]), collapse the two constants onto one
source of truth.

Split out of the completed `extract-workload-state-capability` slug (ADR 0015).
Contingent on the observability-fixtures lift; not actionable on its own.
