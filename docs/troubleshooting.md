# Troubleshooting

## OASIS evaluation produces unexplained provision failures

**Run `petri verify` first.** It checks the substrate (kubeconfig parseability,
cluster reachability, RBAC, default image pullability, audit log path
writability) in under 10 seconds and surfaces issues that otherwise show
up mid-evaluation as ImagePullBackOff or RBAC errors that look like
petri/scenario bugs.

```bash
petri verify --lab my-lab            # default mode, ~5-10s
petri verify --lab my-lab --deep     # also pulls images on the cluster (~60s/image)
petri verify --json                  # machine-readable output for CI
```

See [verify.md](verify.md) for the full check list and output format.

### Catching the R2 / Cloudflare image-pull failure

A common failure mode is that the host's network blackholes Cloudflare R2
(`172.64.0.0/13`). Docker Hub manifests fetch fine, but blob fetches time
out — and the resulting `ImagePullBackOff` looks like a scenario bug.
`petri verify` is designed to catch this: it does a registry-side HEAD on
the default image's manifest **and** a referenced blob, so a working
manifest with a hung blob produces a clear "registry reachable but blob
fetch from R2 timed out" failure.

To synthesize the failure mode locally and confirm `petri verify` catches
it, configure a default image whose blobs live on Docker Hub / R2, then
block the netblock from inside a kind node:

```bash
# Configure a default image whose blobs come from Docker Hub (R2-backed):
PETRI_OASIS_DEFAULT_IMAGE=docker.io/library/nginx:1.27 petri verify --lab my-lab

# Or, after `petri create --oasis --lab my-lab`, block R2 inside the kind node
# and re-run verify; the image_default check should fail with the R2-specific
# diagnostic:
docker exec my-lab-control-plane iptables -A OUTPUT -d 172.64.0.0/13 -j REJECT
petri verify --lab my-lab --deep
docker exec my-lab-control-plane iptables -D OUTPUT -d 172.64.0.0/13 -j REJECT
```

The host-side check runs from the petri host, not the kind node, so the
iptables synthetic-repro inside the kind node only catches the failure in
`--deep` mode (cluster-side pod pull). For host-side reproduction, block
R2 on the host (e.g. `pfctl` on macOS, `iptables` on Linux) before running
`petri verify`.

## Lab creation fails at Terraform apply

```bash
# Get detailed logs
PETRI_LOG_LEVEL=debug petri create --company=acme --level=2

# Check Terraform state
# State location in lab metadata: petri info <lab-name>
cd ~/.petri/workdir/<lab-id>/infra
terraform state list
terraform state show <resource>
```

## Lab won't destroy cleanly

```bash
# Force destroy (continues past individual cleanup failures)
petri destroy <lab-name> --force

# Manual cleanup if force fails:
# 1. Get resource IDs: petri info <lab-name>
# 2. Delete via cloud console/CLI
# 3. Delete git repos manually
# 4. Clean state DB manually
```

## Expired labs not cleaning up

```bash
# Check system health
petri health

# Manual cleanup
petri cleanup --expired

# Set up cron job for automatic cleanup
crontab -e
# Add: */10 * * * * /usr/local/bin/petri cleanup --expired
```

## Can't connect to created cluster

```bash
# Get kubeconfig path from lab info
petri info <lab-name>

# Set kubeconfig
export KUBECONFIG=~/.petri/labs/<lab-name>/kubeconfig-prod

# Verify
kubectl cluster-info
kubectl get nodes
```
