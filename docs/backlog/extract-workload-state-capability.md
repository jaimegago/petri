extract-workload-state-capability
open

Follow-on work deferred out of the workload-state extraction (ADR 0015). The
core extraction is complete: `pkg/workloadstate` owns born-into-state Deployment
synthesis, `pkg/oasis` delegates, `pkg/manifest` holds the shared YAML helpers,
and an unrecognized state is a hard provision error.

Deferred:

- **No standalone Petri CLI surface for the capability.** `pkg/workloadstate` is
  currently driven only by the OASIS provider. A future `petri` subcommand (or a
  flag on an existing one) could let an operator stand up a born-into-state
  workload directly against a lab cluster without going through an OASIS
  scenario. The capability's `Provision(ctx, kube, spec)` entry point already
  supports this; only the CLI wiring is missing.

- **OASIS observability / mock-server builders remain OASIS-coupled.** The
  metrics, traces, alert, events, and logs builders in `pkg/oasis/translate.go`
  (mock Prometheus/Jaeger/Alertmanager pods, injected log lines, Event objects)
  were left in place — they are signal/observation fixtures, a different concern
  from born-into-state workloads, and were out of scope here. If a second
  consumer ever needs them, evaluate lifting the observability-signal fixtures
  into their own capability package the same way (pure render + narrow apply
  client), rather than reaching into `pkg/oasis`.

- **`defaultUtilImage` duplicated across packages.** `pkg/oasis` keeps its own
  `defaultUtilImage` (for the `logs` builder that stays in OASIS) while
  `pkg/workloadstate` has `DefaultUtilImage`; both hold the same pinned
  registry.k8s.io busybox. Intentional today (avoids an import edge), but if the
  logs builder is ever extracted, collapse the two onto one source of truth.
