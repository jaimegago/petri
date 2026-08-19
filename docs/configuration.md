# Configuration

## Config File

Default location: `~/.petri/config.yaml` (created by `petri init`).

```yaml
state:
  backend: sqlite            # or postgresql
  sqlite_path: ~/.petri/petri.db
  # connection_string: "postgres://localhost/petri?sslmode=disable"  # for postgresql

credentials:
  master_key_path: ~/.petri/master.key

observability:
  metrics_enabled: true
  metrics_port: 9090
  tracing_enabled: false
  log_level: info

git:
  default_provider: github

cloud:
  terraform_version: 1.7.0
  pulumi_version: 3.100.0

cleanup:
  check_interval: 5m
  grace_period: 30m

oasis:
  default_image: registry.k8s.io/nginx-slim:0.27
  image_pull_timeout: 5m
```

## OASIS default image

`oasis.default_image` is the OCI image used by `petri serve` when an OASIS scenario declares a Deployment or Pod state entry without setting `spec.image`. The default points at **registry.k8s.io** (the CNCF community registry) instead of Docker Hub.

This avoids a real-world failure mode: Docker Hub blob storage is served from Cloudflare R2 (`172.64.0.0/13`). That range is null-routed by some ISPs, corporate networks, and mobile carriers. When R2 is unreachable, every default-fallback Deployment fails with `ImagePullBackOff` and the failure is misattributed to a petri or template bug.

Override behaviour:

- Scenarios that explicitly set `spec.image` are unaffected — that value is honored as-is.
- Empty / unset `oasis.default_image` falls back to the built-in default (`registry.k8s.io/nginx-slim:0.27`). Setting `default_image: ""` in YAML restores the default rather than disabling it.
- Pin to a specific tag. `:latest` is allowed but not recommended — kind clusters cache by tag and you may end up with an unpredictable version.

Internal builders (CrashLoopBackOff, OOMKilled, log emission) use `registry.k8s.io/e2e-test-images/busybox:1.37.0-2` and are not configurable through this field — they are implementation details of how petri synthesises specific pod behaviours.

## OASIS image-pull budget

`oasis.image_pull_timeout` bounds how long a scenario's Deployments may spend fetching images during `/v1/provision`. It defaults to **5m** and is separate from the 60-second rollout budget, which is not configurable and is not affected by this key.

The two are separate because one deadline could not serve both. A first image pull on a cold node is bounded by image size and link speed and is paid once per node; a Deployment that is genuinely stuck — crashlooping, unschedulable, failing its probes — should fail fast. While a single 60s deadline covered both, the first scenario run against a fresh lab failed to provision, reported as `deployments did not become ready within 1m0s`, because the pull alone exhausted the budget.

Petri now charges time to whichever budget the pod state says it is in: a container waiting in `ContainerCreating` with no resolved `imageID` is pulling, and everything else — scheduling, sandbox setup, probes, crashloops — is rollout time.

Raise it when scenario images are large or the link to the registry is slow. Signs you need to: `/v1/provision` returns 502 with `"reason": "ImagePullTimeout"`, or run.log carries `image pull budget exhausted`.

- Set it in `~/.petri/config.yaml` as above, or per-invocation with `petri serve --image-pull-timeout 10m`. The flag wins.
- Zero or negative falls back to the 5m default rather than meaning "no budget".
- The default is set against a measurement: the 66 MB default image pulled cold in 19.9s on an unloaded host. 5m is roughly 15x that, covering concurrent pulls on a loaded host while still bounding a pull that has hung.

## Environment Variables

| Variable | Overrides |
|----------|-----------|
| `PETRI_STATE_BACKEND` | `state.backend` |
| `PETRI_STATE_CONNECTION_STRING` | `state.connection_string` |
| `PETRI_STATE_SQLITE_PATH` | `state.sqlite_path` |
| `PETRI_CREDENTIALS_MASTER_KEY_PATH` | `credentials.master_key_path` |
| `PETRI_LOG_LEVEL` | `observability.log_level` |
| `PETRI_OASIS_DEFAULT_IMAGE` | `oasis.default_image` |
| `GITHUB_TOKEN` or `PETRI_GITHUB_TOKEN` | GitHub PAT for git operations |

## Credential Security

Petri never stores credentials in plaintext:

1. Credentials prompted at runtime (hidden input)
2. Encrypted with AES-256-GCM
3. Encryption key stored in `~/.petri/master.key`
4. Stored encrypted in SQLite/PostgreSQL
5. Used only during provisioning
6. Never logged or exposed
7. Deleted on lab destruction

**Protect your master key**: `~/.petri/master.key` is the only key to decrypt stored credentials. Back it up securely.

## Cloud Cost Estimates

Approximate hourly costs per complexity level:

| Provider | Level 1 | Level 2 | Level 3 |
|----------|---------|---------|---------|
| AWS (EKS) | ~$0.15/hr | ~$0.35/hr | ~$0.60/hr |
| Azure (AKS) | ~$0.12/hr | ~$0.30/hr | ~$0.55/hr |
| GCP (GKE) | ~$0.10/hr | ~$0.28/hr | ~$0.50/hr |
| Local (kind) | $0 | $0 | $0 |

These are estimates. Actual costs vary by region and usage.
