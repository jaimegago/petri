# Make the per-deployment rollout timeout configurable

**Still open, but narrower than when this was filed.** The image-pull budget
was split out of the rollout budget and made configurable
(`oasis.image_pull_timeout`, `petri serve --image-pull-timeout`). The rollout
budget itself is still hardcoded, now as `defaultRolloutTimeout` at the package
level in `pkg/oasis/provider.go` rather than as a local const inside
`waitForHealthyDeployments`.

What that change settled, and what it left alone:

- **The pressure to raise the 60s default is gone.** The reason it was under
  pressure was cold-start image pulls blowing through it. Those now have their
  own budget, so 60s covers only post-pull convergence, which is what it was
  sized for.
- **The value is still load-bearing for the log-parser contract.** It is
  interpolated into the historical `"deployments did not become ready within
  %s: %s"` phrasing. Making it variable still means parsers see a value that
  moves.
- **It no longer bounds the image pull**, so the first bullet of the original
  framing below is now wrong: the constant is not the ceiling on how long
  `kubectl rollout status` waits. That ceiling is the sum of both budgets plus
  two watcher intervals, and it is a backstop rather than the operative limit.

Reasonable knobs still to surface:

- A field on `ProviderConfig` loaded from `~/.petri/config.yaml` under an
  `oasis.rollout_timeout` key, mirroring how `oasis.image_pull_timeout` is now
  wired.
- A `PETRI_OASIS_ROLLOUT_TIMEOUT` env var override for ad-hoc tuning. Note that
  `image_pull_timeout` deliberately did **not** add an env var — config key plus
  flag was enough — so adding one here is a choice, not a consistency
  requirement.
- A scenario-level override on the request if individual scenarios legitimately
  need more or less time than the global default.

Open questions still to settle before implementing:

- Does the global default stay at 60s? The cold-start argument for raising it is
  spent, and the fast-failure argument for keeping it low is now the only one
  left standing.
- Per-scenario overrides interact poorly with the `"deployments did not become
  ready within"` log-parser contract — parsers would need to know that the value
  is variable. May be worth emitting a structured log field alongside the
  historical phrasing. The pull-budget work took the other route for its own new
  condition: a distinct message (`image pull budget exhausted`) and a distinct
  error string, so nothing had to learn a variable value.
