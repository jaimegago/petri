# 0021. One version, with the build as semver build metadata

- Date: 2026-11-20
- Status: accepted

## Context

Every petri binary reported `0.1.0`. The CLI's version was a constant in
`internal/cli/root.go`, and the provider version declared at the OASIS
conformance handshake was a second constant in `pkg/oasis/provider.go`. An
evaluator writes the declared provider version into its verdict, so every
verdict named the provider as `petri 0.1.0` whatever commit had produced the
environment, and results from two different petri builds could not be told
apart. The joe-pm ledger's `threads/petri-build-stamp.md` is the order this
ADR answers.

Two facts shape the answer:

- OASIS Provider Conformance §3.8.2 asks a provider for one field about
  itself, `provider_version`, "the provider's own version, in semver". It
  defines no second field for a build, and no separate contract version: the
  contract a provider implements is declared by `oasis_core_spec_versions`
  and `profile_version`.
- petri has no version tag. The repository's only tags are `svc/v*`, the
  application image's, so `git describe` yields a string that is neither
  petri's version nor a semantic version.

## Decision

**petri has one version string, `<version>+<build>`, and declares it in both
places.** `internal/version.String()` returns petri's semantic version with
the build as semver build metadata: `0.1.0+b46fec5`, `0.1.0+b46fec5-dirty`.
`petri --version` prints it and `petri serve` passes it to the provider as
`ProviderConfig.Version`, which `Conformance` declares as `provider_version`.

- **The build is the short commit, then `-dirty`** when
  `git status --porcelain` prints anything. That counts an untracked file,
  which is compiled in like a modified one.
- **The build is stamped, with the toolchain's stamp as the fallback.**
  `make build` and `make install` set `internal/version.Build` with
  `-ldflags -X`. Left empty, the commit and dirty marker are read from the
  VCS stamp `go build` embeds. That stamp is absent in a git worktree, in a
  submodule checkout, and under `go run`, which is why the explicit stamp
  exists; a binary with neither reports `0.1.0+unknown`.
- **A builder may append its own markers.** joe-oasis-e2e builds petri from a
  submodule and appends `-unpinned` when the checkout is not the commit the
  harness pins. Markers are letters, digits and hyphens, so the string stays
  a semantic version.
- **The version core stays declared in source**, as a variable a release
  build can set. It moves when the maintainer decides petri's version moved,
  not on every commit.

Rejected: a separate contract-version constant at the handshake beside a
build field. The spec gives a provider nowhere to declare a build except the
version itself, and an evaluator carries only `provider_version` into a
verdict, so a second field would be read by nothing. Build metadata is the
part of a semantic version that takes no part in precedence, which is the
separation wanted: two builds of `0.1.0` are the same version and different
builds.

Rejected: `git describe` as the version, the form the vendored go-backend
skill suggests. It yields `svc/v0.2.0-31-gb46fec5` here, which names the
application image and is not semver.

## Consequences

- A verdict names the petri build that served it, with no change to the
  evaluator: oasisctl copies `provider_version` through without parsing it.
- `provider_version` is no longer the bare `0.1.0`. A consumer that compares
  it for equality against `0.1.0` has to compare the version core instead.
- `Conformance` declares whatever `ProviderConfig.Version` holds, including
  the empty string. `petri serve` is the one production caller and sets it.
- A bare `go build ./cmd/petri/` in a worktree or submodule produces
  `0.1.0+unknown`. That is honest and not useful; build with `make build` or
  pass the `-ldflags` yourself.
- No build time is stamped. It would make two builds of one commit differ,
  and nothing reads it.
