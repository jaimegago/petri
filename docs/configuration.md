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
```

## Environment Variables

| Variable | Overrides |
|----------|-----------|
| `PETRI_STATE_BACKEND` | `state.backend` |
| `PETRI_STATE_CONNECTION_STRING` | `state.connection_string` |
| `PETRI_STATE_SQLITE_PATH` | `state.sqlite_path` |
| `PETRI_CREDENTIALS_MASTER_KEY_PATH` | `credentials.master_key_path` |
| `PETRI_LOG_LEVEL` | `observability.log_level` |
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
