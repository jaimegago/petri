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
| `--oasis` | bool | false | Enable OASIS evaluation mode (audit logging, Calico CNI for NetworkPolicy) |
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
| `--status` | string | | Filter by status (CREATING, ACTIVE, EXPIRED, DESTROYING, DESTROYED, ERROR) |
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

For best results, create the lab with `--oasis` to auto-configure audit logging and Calico CNI (NetworkPolicy enforcement):

```bash
petri create --company=acme --level=1 --local --oasis --name=eval-lab
petri serve --lab=eval-lab
```

### `petri inject`

Inject a single chaos fault into a named, running lab.

```bash
petri inject <fault-type> [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--lab` | string | | Name of the target lab (primary target selector) |
| `--kubeconfig` | string | | Explicit kubeconfig path (overrides `--lab`) |
| `--target` | string | | Target resource as `namespace/kind/name` |
| `--param` | string | | Fault parameter as `key=value` (repeatable) |
| `--dry-run` | bool | false | Resolve and validate everything, print the plan, but do not mutate the cluster |

`petri inject` is the **runtime** counterpart to Petri's **provision-time**
born-into-state capability (`pkg/workloadstate`, documented in
[workload-state.md](workload-state.md)): chaos perturbs a resource that is
*already running*, whereas workloadstate synthesises a workload that *starts* in
a named state. The command looks the fault up in the catalog and executes it
once — it does **not** start the continuous random `ChaosRunner`.

Exactly one of `--lab` or `--kubeconfig` selects the cluster; `--kubeconfig`
wins when both are given. `--lab` is resolved through the same active-lab guard
`petri serve` uses, so an EXPIRED/ERROR/CREATING lab is refused with a non-zero
exit.

**Target grammar.** `--target` is a `namespace/kind/name` triple, e.g.
`apps/Deployment/boutique-frontend`. The kind and name pass through verbatim —
the fault resolves and validates its own target (for pod-targeting faults the
name may be a label selector such as `app=frontend`).

**Accepted fault types** (sourced structurally from `chaos.DefaultFaults()`, so
this list tracks the catalog):

- `kill_pod`
- `restart_deployment`
- `cpu_pressure`
- `memory_pressure`
- `corrupt_configmap`
- `revoke_serviceaccount`
- `network_latency`
- `scale_to_zero`

Per-fault `--param` keys are documented by each fault in `pkg/chaos/faults.go`
(e.g. `cpu_pressure` accepts `duration` and `workers`; `network_latency` accepts
`latency_ms` and `jitter_ms`). Faults that take no parameters ignore `--param`.

```bash
# Roll-restart a Deployment in an active lab
petri inject restart_deployment --lab eval-lab --target apps/Deployment/boutique-frontend

# Apply 30s of CPU pressure with two workers to a pod
petri inject cpu_pressure --lab eval-lab --target apps/Pod/api --param duration=30s --param workers=2

# Preview without mutating the cluster
petri inject kill_pod --kubeconfig ~/.kube/config --target apps/Pod/frontend --dry-run
```

### `petri completion`

Generate shell completion scripts.

```bash
petri completion [bash|zsh|fish|powershell]
```
