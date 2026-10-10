# cloud-kubeconfig-gcp-azure
Status: open

The AWS cloud path now assembles a kubeconfig from the template's per-cluster
outputs (`pkg/orchestrator/cloudkubeconfig.go`, `eksClustersFromOutputs`) and
gives each cluster the local path's treatment: node wait, platform,
observability, apps. GCP and Azure still obtain no kubeconfig, so a lab on
either is created with no cluster access and skips every cluster step
(`runTerraform` logs "No kubeconfig assembly for this provider yet"; `runPulumi`
logs that the stack exports no `kubeconfig`). Found 2026-10-10 in
`joe-pm/threads/aws-cloud-path-kubeconfig-gap.md`.

## GCP (`templates/terraform/gcp`)

- No CA output: the template emits `gke_<name>_endpoint` (a bare IP, no scheme)
  and `gke_<name>_kubeconfig_command`, but not
  `master_auth[0].cluster_ca_certificate`.
- Auth would be the `gke-gcloud-auth-plugin` exec plugin, the GKE equivalent of
  `aws eks get-token`.
- Independently, `variable "project_id"` has no default and petri passes no
  `-var`/`-var-file`, so a GCP apply cannot run non-interactively today. That
  has to be fixed first, and how petri learns the project is a design choice
  (company profile, config, environment).

## Azure (`templates/pulumi/azure`)

- The stack exports only `aks<Cluster>Name` and the resource group.
- The AKS kubeconfig (`listManagedClusterUserCredentials`) carries a real
  credential (client certificate or an AAD exec config, depending on the
  cluster's auth mode), unlike the EKS one. It must be exported as a Pulumi
  secret and land only in the encrypted credential store; the choice of local
  accounts vs AAD + `kubelogin` is part of the design.
- `runPulumi` already reads a `kubeconfig` string output as a single cluster;
  a multi-cluster level needs a per-cluster output shape like AWS's.
