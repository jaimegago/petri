ministack-aws-lab
Status: open

Petri's AWS path (`templates/terraform/aws/*.tf.tmpl` through
`pkg/provisioners/terraform`) runs only against a real account — VPC, EKS, RDS
and ElastiCache at real cost (`docs/configuration.md` prices a lab at
$0.15–0.60/hr) — so it has never been exercised end to end. The local path
skips terraform entirely and hand-builds a kind cluster with no RDS/Redis
analogue.

[ministack](https://ministack.org/) (`github.com/ministackorg/ministack`, MIT)
emulates the AWS API locally on one Docker container, and its emulation is
material rather than stubbed: `CreateCluster` in `services/eks.py` spawns a
real `rancher/k3s` container whose live kube-apiserver endpoint
`DescribeCluster` advertises, so `aws eks update-kubeconfig` and kubectl work;
RDS backs onto real Postgres/MySQL and ElastiCache onto real Redis/Memcached.
Terraform reaches it through the standard provider `endpoints {}` override.

The proposition: a **ministack AWS lab** — petri's existing AWS terraform,
rendered as-is, applied against a local ministack instead of a real account.
That would make the AWS cloud path testable for free, and would put a lab's
databases and cache closer to the cloud shape than the kind path gets today.
This is a third provider posture, not a replacement for the kind path, which
local labs, e2e and the OASIS provider all sit on.

Investigated 2026-08-22; recorded in
`joe-pm/threads/petri-ministack-investigation.md`. LocalStack was ruled out
there: EKS/RDS/ElastiCache were Pro-tier, and the open-source repo was archived
2026-03-23.

## First step: a bounded spike

Before any petri code changes: render `templates/terraform/aws` for `acme`,
point the provider at a pinned `ministackorg/ministack` image, and report which
resources apply, which fail, and whether the resulting kubeconfig reaches a
cluster petri's platform install can use. The spike's result decides between
adopting ministack as the AWS local provider and re-measuring later.

## Spike result (2026-10-10)

Run in `joe-pm/threads/ministack-aws-spike.md`. `acme` level 2 (VPC, EKS
`prod` + `staging`, RDS Postgres 15, ElastiCache Redis 7.0) rendered through
`pkg/generators/iac` unchanged, applied with terraform 1.12.2 / AWS provider
5.100.0 / eks module 20.37.2 / rds 6.13.1 against `ministackorg/ministack:1.5.25`
(`sha256:1754efa3…dfd9b1`). Only override files were added: provider
`endpoints {}` + dummy credentials, and the template's own S3/DynamoDB backend
pointed at ministack (it works).

- **Apply: 102 of 104 resources created** in ~3 minutes, including both EKS
  clusters (real k3s), managed node groups, KMS keys, IAM roles/policies,
  CloudWatch log groups, launch templates, all SG rules, NAT/route tables, RDS
  (real Postgres) and ElastiCache (real Redis).
- **The one failure:** the eks module's `data "tls_certificate"` rejects the OIDC
  issuer ministack advertises (`http://localhost:4566/oidc/id/…`; needs
  `https`), so `aws_iam_openid_connect_provider` (IRSA) is never created. An
  override setting `enable_irsa = false` converges the apply.
- **Not idempotent:** every subsequent plan proposes 3 replacements + 2 updates —
  ElastiCache reports its host-mapped port (16379 vs 6379, forces replacement),
  node groups read back without `launch_template` (forces replacement), the
  ElastiCache subnet group reads back without subnets/tags. `terraform destroy`
  is clean and removes every backing container.
- **Cluster reach: yes.** `aws eks update-kubeconfig` (the aws CLI shipped in the
  ministack image) + `kubectl` reach a Ready k3s v1.31.4 node in each cluster
  via the `aws eks get-token` webhook. petri's level-2 platform, observability and
  app manifests applied with `kubectl apply` and all pods reached Running; pods
  reach RDS and Redis on their Docker-bridge IPs.
- **Fiction the lab would carry:** one node per cluster whatever the node group
  says (desired 4 → 1 node, no nodegroup labels); k3s v1.31 while
  `DescribeCluster` reports the requested 1.29; metrics-server disabled (HPA
  targets `<unknown>`); flannel + k3s's own NetworkPolicy controller, no Calico,
  so `pkg/oasis` network-policy detection would report unenforced; ElastiCache
  advertises `localhost:16379` (host-reachable, not pod-reachable) while RDS
  advertises a bridge IP (pod-reachable, not host-reachable).

Pre-existing gaps in petri's AWS path that the spike surfaced, independent of
ministack: `runTerraform` reads a `kubeconfig` output the AWS template never
emits (only `eks_*_kubeconfig_command`), so the cloud path never gets a
kubeconfig and skips app deploy and node wait; `createCloud` never applies
platform or observability manifests; the RDS module takes
`module.vpc.database_subnet_group_name`, which is null because the VPC declares
no database subnets (the instance lands in `default`).

**Recommendation: not yet as a drop-in provider.** The core proposition holds —
the existing terraform applies nearly unchanged and yields usable clusters and
real datastores. What blocks adoption is petri-side (the kubeconfig gap) plus
two ministack-side defects (http OIDC issuer, non-idempotent reads). Re-measure
when ministack serves an https issuer and stable reads, after the kubeconfig
gap is fixed on its own merits.

## What the design has to decide

- **Module coverage is unverified.** `terraform-aws-modules/eks` and `/vpc`
  touch far more API surface than the four headline resources — IAM roles and
  policies, OIDC provider, KMS, CloudWatch log groups, launch templates,
  security-group rules. Whether ministack satisfies every call at the pinned
  module versions is the spike's question.
- **k3s is not EKS.** The `--oasis` path installs Calico for NetworkPolicy;
  k3s ships flannel and its own defaults. Nodegroups are modelled, not real
  nodes, so per-node behaviour (node-state entries, chaos injection) needs
  re-checking against a single-container k3s.
- **How the lab is wired.** A new cloud-provider value, a flag on the existing
  `aws` path that swaps in endpoint overrides, or a config-level endpoint field
  the company profile carries — and where the ministack container's own
  lifecycle (start, pin, teardown) lives relative to the lab's.
- **Maturity.** The project is five months old (created 2026-03-24), one org,
  releasing every few days. Pin an image tag; decide who bumps it and on what
  trigger.

## Related

- `docs/backlog/node-state-entry-kind.md` — per-node realism questions that a
  k3s-backed lab would inherit.
- `docs/backlog/realistic-failure-injection.md` — the same "consistent story
  vs labelled fiction" bar applies to an emulated AWS.
