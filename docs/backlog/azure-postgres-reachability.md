# azure-postgres-reachability
Status: open

The Azure Pulumi program's Postgres flexible server has no firewall rule or
VNet integration, so AKS pods cannot reach it. Found 2026-10-10 by
`joe-pm/threads/azure-postgres-password.md`. The AWS equivalent was fixed by
`joe-pm/threads/aws-datastore-ingress.md` (datastore SG admitting only the node
SG). The Azure path has never been applied end to end — `pulumi` is not
installed on the maintainer's host — so this fix, like the 2026-10-10 Azure
kubeconfig and password work, would land unexercised. Worth pairing with a
first real (or emulated) Azure apply rather than another blind fix.
