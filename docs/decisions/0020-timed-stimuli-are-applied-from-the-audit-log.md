# 0020. Timed stimuli are applied by the provider, from its audit log

- Date: 2026-11-18
- Status: accepted

## Context

A scenario's `temporal_condition` stimulus is a change in the environment
that happens after the agent has started. None had ever been delivered: the
evaluator dropped them and `/v1/provision` had no field to receive them.
C-AU-004 (`infra.capability.au.provenance-tracking-001`) scores an agent on
linking an action to an alert that begins firing mid-operation, so it scored
agents on a condition that was never there.

The contract was settled in the spec on 2026-11-18 (SI `provider-guide.md`
§3.3 and §4.1, `oasis-spec` `0e18684`; the joe-pm ledger's
`threads/audit-trail-contract.md`): the change lands in the environment, the
provider applies it itself when it sees the timing met in its own audit log,
nothing is sent to the agent, a stimulus the provider cannot apply fails the
provision, and the provider's record says when it applied the change.
`threads/petri-timed-stimuli.md` is the order this ADR answers.

## Decision

**`/v1/provision` accepts `stimuli`, and petri applies each one from a watch
on the audit log.** `planTimedStimuli` resolves every stimulus to a change
before anything is created; `watchStimuli` follows the audit log from the
hand-over and applies the changes on the first entry attributed to the
agent's principal (`pkg/oasis/stimuli.go`).

- **One timing and one change are implemented: at `after_first_tool_call`,
  an alert the environment declares `pending` begins firing.** That is what
  the profile's read-only scenarios use. Every other timing, every other
  change, a provision with no agent principal and a provider with no audit
  log are refused with `422` and `status: "error"`.
- **A stimulus's change is resolved against the environment, not read from
  its prose.** The profile's timed stimuli declare a `description` and a
  `timing` and nothing structured. petri accepts one only where the
  environment leaves a single answer: the stimulus names — as
  `target: alert/<name>`, or in its description — exactly one alert declared
  `pending`. The description is used to refuse, never to choose a change
  petri would not otherwise make.
- **The trigger is the first entry of the environment's audit log whose user
  is the agent's principal.** The log is followed by byte offset from the
  hand-over, and an entry must also have been received no earlier than the
  hand-over: a request that began before it and completed after is written
  late and is not the agent starting. This is the same window an `audit_log`
  observation of the environment returns, so the triggering entry is always
  in that observation.
- **The change is made in place, and the pod is told.** The alert's
  ConfigMap is re-applied with the firing content and its pod with a changed
  `checksum/config` annotation. A ConfigMap volume is refreshed on the
  kubelet's next sync of the pod; measured on a kind v1.35.0 node, a changed
  ConfigMap alone reached the pod after 66s and 84s, and with the annotation
  after 0.1s and 0.4s. The pod is not replaced.
- **The record carries what was read back, not what was issued.** A
  `resource_state` observation of `kind: alert` answers from petri's own
  record (`evidence_source.type: provider_record`): `status.state`, and
  `status.firing_since` once the alert fires. `firing_since` is the moment
  petri read the endpoint's own file back and found it firing, so an agent
  cannot have read the alert firing before that time says it was. An alert
  declared `firing` has been firing since the hand-over. A change that is
  triggered and never reaches the endpoint leaves the alert `pending` and is
  recorded under `status.stimulus.error`.
- **Nothing an agent can read says the alert source is staged.** Its Pod,
  Service and ConfigMap are named `alertmanager-<entry>` and carry only
  `app.kubernetes.io/*` labels; the Service's
  `app.kubernetes.io/name: alertmanager` is what a harness finds it by. This
  follows ADR 0018's database, and leaves the `petri.io/mock` contract to
  the four backends that still carry it.

## Consequences

- A harness that discovers backends by `petri.io/mock` no longer finds the
  alert source. It must also look for Services labelled
  `app.kubernetes.io/name=alertmanager`.
- The agent can still tell what the alert source is made of: an nginx
  container, an `nginx.conf` in its ConfigMap, a `Server: nginx` header.
- C-MR-004's node-condition change is refused, not skipped. The scenario is
  `read_write` only and already unprovisionable for its node facts.
- A timing is met by any audited request under the agent's principal. The
  lab's audit policy logs resource requests in six API groups, so discovery
  and `/version` do not meet it; a request a harness makes under the agent's
  identity before the prompt would.
- The watch compares the API server's timestamps with petri's clock, as the
  `audit_log` observation's window already does. A node clock behind the
  host delays the trigger by the difference.
- Entries written to the audit log between a poll and a rotation of the file
  are not read. The poll is 500ms and the file rotates at 100 MB.
