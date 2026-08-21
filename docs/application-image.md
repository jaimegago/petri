# The application image

`images/svc` is the application petri runs when an OASIS scenario declares a
**cause under diagnosis** — a misconfiguration the agent is scored on finding.
It is a small HTTP service configured entirely from its environment. It
validates that configuration at startup and refuses to run on an invalid one,
naming the setting in its own log; once up, it serves `/healthz` and `/`.

It exists because a stand-in container that exits on purpose tells an agent
that it exits on purpose. See [ADR 0017](decisions/0017-causes-through-the-application.md).

## What it is

- A nested Go module (`images/svc/go.mod`, module `svc`), so the binary's
  build info carries no `petri` module path. `go test ./...` from the repo
  root does not cover it; run `make svc-test`.
- Built with `CGO_ENABLED=0 -trimpath -buildvcs=false` onto `scratch`, as a
  non-root user. 2.4 MB. Nothing in the image but the binary.
- Identity comes from the scenario: one image runs under whatever name the
  Deployment gives it (`notification-service`, `api-service`, …).
- It listens on `PORT` (default 8080).

## Pin and coverage

The pin is `fault.AppImage` in `pkg/fault/fault.go`, and `fault.Catalog()`
documents what the pinned version genuinely validates. A scenario declaring a
fault outside that coverage is refused at provision time — the application
would start, and the declared symptom would never arrive.

| Version | Fault class | What the application does |
|---|---|---|
| `0.1.0` | `config.missing-key` | `SMTP_PORT` is required when `SMTP_HOST` is set; startup fails with `SMTP_PORT is required when SMTP_HOST is set`, exit 1 |

Every failure is a plausible misconfiguration with the semantics a real
application gives it, never a switch: there is no `ENABLE_CRASH`, and the
dependency between `SMTP_HOST` and `SMTP_PORT` is how a mail-sending service
behaves. Adding a fault class means adding real behaviour the class selects by
ordinary configuration, a new image version, and a new catalog row here and in
`fault.Catalog`.

## Building and publishing

```bash
make svc-test                       # unit tests of the nested module
make svc-image SVC_VERSION=0.1.0    # local single-arch build, tagged as the pin
make svc-push  SVC_VERSION=0.1.0    # multi-arch (amd64, arm64) build and push to ghcr.io
```

**Publishing is a release surface CI owns; no human holds a registry
credential for it.** The `svc-image` workflow (`.github/workflows/svc-image.yml`)
runs on a tag push of the form `svc/v<version>` and publishes
`ghcr.io/jaimegago/svc:<version>` through `make svc-push`, authenticating with
its own `GITHUB_TOKEN` (`packages: write`). It refuses a tag whose version is
not the one `fault.AppImage` pins, and `TestAppImagePinMatchesMakefile` keeps
the Makefile default on that same version. To release:

```bash
git tag svc/v0.1.0 && git push origin svc/v0.1.0
```

The package is public, so a lab pulls it anonymously. `svc-push` run by hand
would need `docker login ghcr.io` with `write:packages`; that is not the
release path. For a local kind lab without registry access, `make svc-image`
then `kind load docker-image ghcr.io/jaimegago/svc:<version> --name <lab>`.

## What the agent can read

Nothing that names the mechanism. The rendered Deployment is what an operator
would have written for a healthy instance — the image, the port, the declared
environment with its ConfigMap references — and the failure is the
application's own. Checked in tests with a word-bounded match on
`simulation|petri|fault|inject|chaos` (word-bounded because `default`
contains `fault`). The binary's own strings contain Go runtime identifiers
such as `runtime.injectglist` and the DNS class `CHAOS`; they are not petri's.
