# Troubleshooting

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
