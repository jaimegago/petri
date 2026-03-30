# CLI Reference

## Global Flags

```
--config string    Config file (default ~/.petri/config.yaml)
--companies string Companies YAML file (default configs/companies.yaml)
--log-level string Log level: debug, info, warn, error (default: info)
```

## Commands

### `petri init`

Initialize Petri configuration and encryption key.

```bash
petri init
```

Creates:
- `~/.petri/` directory
- `~/.petri/config.yaml` with default settings (SQLite backend)
- `~/.petri/master.key` (AES-256 encryption key)
- `~/.petri/labs/` directory

### `petri create`

Create a new infrastructure lab.

```bash
petri create --company=<company> --level=<1|2|3> [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--company` | string | *required* | Company profile (acme, techflow, cloudnative) |
| `--level` | int | *required* | Complexity level 1-3 |
| `--name` | string | auto-generated | Lab name |
| `--local` | bool | false | Use local kind cluster instead of cloud |
| `--cloud` | string | from company | Cloud provider override (aws, azure, gcp) |
| `--ttl` | string | level-specific | Time-to-live (e.g. 4h, 30m) |
| `--no-apps` | bool | false | Skip application deployment |
| `--dry-run` | bool | false | Print what would be created without creating it |

Local labs create git repos on the filesystem under `~/.petri/labs/<id>/repos/`.

### `petri list`

List labs.

```bash
petri list [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--company` | string | | Filter by company |
| `--level` | int | | Filter by level (1-3) |
| `--status` | string | | Filter by status (CREATING, ACTIVE, EXPIRING, DESTROYING, DESTROYED, ERROR) |
| `--alive` | bool | false | Show only non-expired labs |
| `--format` | string | table | Output format: table or json |

### `petri info`

Show full details for a lab.

```bash
petri info <lab-name>
```

Displays lab identity, clusters, git repos, applications, platform components, observability tools, resources, and access instructions.

### `petri destroy`

Destroy a lab and all its resources.

```bash
petri destroy <lab-name> [--force]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--force` | bool | false | Force destroy even if some cleanup steps fail |

### `petri extend`

Extend a lab's TTL.

```bash
petri extend <lab-name> [--ttl=+2h]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--ttl` | string | +1h | Duration to extend (e.g. +2h, +30m) |

### `petri cleanup`

Clean up expired labs.

```bash
petri cleanup --expired
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--expired` | bool | false | Destroy all labs past their TTL |

Requires `--expired` flag. Uses the grace period from config before destroying.

### `petri export-creds`

Export encrypted credentials bundle for a lab.

```bash
petri export-creds <lab-name> [--output=joe-bundle.enc]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--output` | string | joe-bundle.enc | Output file path for encrypted bundle |

### `petri health`

Check system health.

```bash
petri health
```

Checks:
- Required binaries: `git` (required), `kubectl`, `kind`, `docker`, `terraform` (optional)
- `~/.petri` directory exists
- `~/.petri/master.key` exists with correct permissions (0600)
- `~/.petri/config.yaml` exists

Reports overall status as OK or DEGRADED.

### `petri serve`

Start OASIS environment provider HTTP server.

```bash
petri serve [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--listen` | string | :8090 | Address to listen on |
| `--lab` | string | | Name of an existing local lab to use as base cluster |
| `--audit-log-path` | string | | Path to Kubernetes audit log file |

Implements the OASIS evaluation spec for scenario-based testing of AI agents against live Kubernetes clusters.

### `petri completion`

Generate shell completion scripts.

```bash
petri completion [bash|zsh|fish|powershell]
```
