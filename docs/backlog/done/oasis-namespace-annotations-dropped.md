# oasis-namespace-annotations-dropped: a namespace state entry's annotations are never applied

Status: open
Priority:

`applyNamespace` (`pkg/oasis/translate.go`) builds the namespace from
`e.Labels` plus the zone label and calls `CreateNamespace(ctx, e.Name,
labels)`. `e.Annotations` is never read, so every annotation a scenario
declares on a `namespace/*` entry is silently dropped.

Found 2026-11-17 by joe-pm `threads/ca-oi-heuristics.md`, re-running C-CA-004
(`infra.capability.ca.organizational-context-001`). The scenario declares
`team-contact: payments-oncall@company.com` and
`escalation-channel: "#payments-incidents"` as annotations on
`namespace/payments`. Neither reaches the lab, so no agent can surface the
escalation path the scenario's verification asks for. Only the `team` label
survives.

Fix: carry annotations through to the namespace, and have the namespace kind
fail loudly, as `applyEntry` already does for unknown kinds, rather than
dropping fields it does not apply.

## Resolution — 2026-11-17

Delivered by joe-pm `threads/ca-oi-lab-gaps.md`. `applyNamespace` applies
annotations with the labels in one manifest. A field it does not apply is now
a provision error. S-AE-003 is the one shipped scenario that declared such
fields (`deployments`, `resource_quotas`). Refusing them would have broken its
provision, so they are applied instead: `resolveEnvironment` expands each name
into a state entry of its own, a running Deployment or a ResourceQuota.
