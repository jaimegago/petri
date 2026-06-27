petri-cli-surface-for-workload-state
open

No standalone Petri CLI surface for the workload-state capability.
`pkg/workloadstate` is currently driven only by the OASIS provider. A future
`petri` subcommand (or a flag on an existing one) could let an operator stand up
a born-into-state workload directly against a lab cluster without going through
an OASIS scenario. The capability's `Provision(ctx, kube, spec)` entry point
already supports this; only the CLI wiring is missing.

Split out of the completed `extract-workload-state-capability` slug (ADR 0015) —
the extraction itself is done; this is net-new CLI work.
