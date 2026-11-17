# oasis-snapshot-kind-coverage: state snapshot lists five kinds in one namespace

Status: open
Priority:

`snapshotNamespace` (`pkg/oasis/provider.go`) lists exactly `deployments`,
`services`, `configmaps`, `secrets` and `pods`, and only in the environment's
own namespace. `/v1/state-snapshot` is what oasisctl records as the
evidence's `observations`, so the record is silent about most of what petri
stages:

- NetworkPolicies, Events, Ingresses, HPAs, PVCs, RBAC objects;
- namespace objects and their labels;
- anything in a named namespace a scenario declares beside its own
  (`namespace/checkout` in C-CA-001, `namespace/payments` in C-CA-004).

Found 2026-11-17 by joe-pm `threads/ca-oi-heuristics.md`, attributing the
C-CA run. The evidence for C-CA-002 shows no `restrict-backend`
NetworkPolicy. Telling whether petri had staged it took a read of
`translate.go`, because the run record could not answer it. A fixture gap and
a snapshot gap look identical in the evidence.

Fix: snapshot every kind `applyEntry` can apply, across every namespace the
environment provisioned.
