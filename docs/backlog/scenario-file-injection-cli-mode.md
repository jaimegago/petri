scenario-file-injection-cli-mode
open

No CLI surface for scripted scenario-file fault injection.
`pkg/scenarios` ships a `ScenarioRunner` (with `LoadFile`) that drives a sequence
of faults from a scenario YAML (with expected diagnoses), but the only
user-facing chaos trigger today is `petri inject <fault-type>` (ADR 0016), which
injects exactly one fault.

A future mode should let an operator drive the existing scenarios runner from
the CLI — e.g. `petri inject --scenario <file.yaml>` — to replay a scripted
sequence of faults against a running lab. This belongs as an **additional mode
under the same `inject` command**, not a standalone command, alongside the
deferred born-into-state workloadstate source
([petri-cli-surface-for-workload-state](petri-cli-surface-for-workload-state.md)).

Out of scope for the `petri-inject-cli` slug, which is single-fault one-shot
only. Open and deferred.
