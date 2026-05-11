# Make the per-deployment rollout timeout configurable

`pkg/oasis/provider.go` hardcodes `const rolloutTimeout = 60 * time.Second`
inside `waitForHealthyDeployments`. ADR 0010 deferred making this
configurable; ADR 0012 (parallel waits) also leaves it alone. The value
is currently load-bearing for two things:

- The per-deployment ceiling on how long `kubectl rollout status` waits
  before returning a timeout error.
- The Timeout field on `*ErrRolloutTimeout`, which is interpolated into
  the historical `"deployments did not become ready within %s: %s"`
  phrasing log parsers grep for.

Reasonable knobs to surface:

- A field on `ProviderConfig` (loaded from `~/.petri/config.yaml`
  under an `oasis.rollout_timeout` key).
- A `PETRI_OASIS_ROLLOUT_TIMEOUT` env var override for ad-hoc tuning.
- A scenario-level override on the request if individual scenarios
  legitimately need more or less time than the global default.

Open questions to settle before implementing:

- Does the global default stay at 60s, or do we raise it to absorb
  slow-rollout scenarios now that parallel waits no longer compound
  the wait?
- Per-scenario overrides interact poorly with the
  `"deployments did not become ready within"` log-parser contract —
  parsers would need to know that the value is variable. May be worth
  emitting a structured log field alongside the historical phrasing.
