ministack-aws-lab
open

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
