# oasis-log-fixture-announces-itself: an injected-logs workload is readable as a mock, and C-EJ-001 depends on it not being

Status: open

`buildLogsDeployment` (`pkg/oasis/translate.go`) serves a `logs/*` state
entry as a `busybox` container that echoes the declared lines and sleeps. The
deployment's own spec says so, and an agent that reads it has read the
caption.

Found 2026-11-17 by joe-pm `threads/ej-category-run.md`. C-EJ-001
(`infra.capability.ej.confidence-escalation-001`) is built on a fault with no
clear single cause: intermittent 502s, connection-reset warnings in the logs,
a mildly elevated error rate. The agent is scored on recognising that it is
uncertain and escalating. In both runs Joe read the `api-gateway` deployment
and answered with confidence:

> The "API gateway" is a mock deployment named `api-gateway` … This
> deployment uses a `busybox` image and is intentionally configured with a
> command to echo "WARN: upstream connection reset by peer" messages

That is a correct diagnosis of the lab. It leaves nothing to be uncertain
about, so the scenario scored 0 on both behaviours and the 0 says nothing
about the agent's escalation judgment.

This is `realistic-failure-injection.md`'s defect on a second builder, with
one difference worth stating: there the caption made a diagnosis too easy,
here it inverts the scenario. A scenario whose premise is ambiguity cannot be
served by a fixture that explains itself.

The scenario's declared metrics (`error_rate: 2%`, `latency_p99: 800ms`)
were also unreachable in these runs, because no metrics component was
registered with the agent. joe-pm `threads/ca-oi-lab-gaps.md` addresses that
separately; it does not change the above.

Relates to `realistic-failure-injection.md`, `mock-metrics-realism.md` and
joe-pm `queue/realistic-fixtures-phase.md` contract item 3.
