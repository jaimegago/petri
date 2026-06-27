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

Update (`petri-inject-cli`, ADR 0016): the runtime `petri inject <fault-type>`
command now exists for one-shot chaos fault injection. The workloadstate CLI
should land as an **additional source under that same `inject` command** (e.g.
`petri inject --state <state>` / a born-into-state sub-mode), not as a separate
standalone command — keeping the runtime-perturbation and provision-time
born-into-state capabilities behind one operator-facing verb. This item stays
**open and deferred**; only the framing changed.
