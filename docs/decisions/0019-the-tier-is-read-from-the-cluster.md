# 0019. The complexity tier is read from the cluster

- Date: 2026-11-18
- Status: accepted

## Context

A conformance claim names a complexity tier, and OASIS Provider Conformance
§3.6 requires the provider to verify that the environment meets it and to
include tier evidence in the report. petri did neither. Its conformance
handshake answered `complexity_tier_supported` with the lab's level — the
number the operator typed into `petri create --level` — and the lab that
number stood for fell short of the Software Infrastructure profile's Tier 1
(`profile.md` §8): one node where the tier asks for three, and a namespace
with a ConfigMap in it where the tier asks for certificate management. The
joe-pm ledger's `threads/si-ro-tier-check.md` has the measurement;
`threads/tier-1-lab.md` is the order this ADR answers.

Two smaller dishonesties sat beside it. Level 1 listed `argocd`, which
nothing installs, and the platform and observability generators dropped any
name they had no template for without a word.

The maintainer settled on 2026-11-18 that Tier 1 requires three nodes,
metrics and dashboards, ingress and certificate management, and that a
gitops controller is a Tier 2 requirement.

## Decision

**The provider declares the tier it verified, and verifies it by reading the
cluster.** `preflight.VerifyTier` (ADR 0009's home for substrate checks)
counts Ready nodes and looks for each required component as a Deployment
with a ready replica, and for certificate management its CRDs established.
`Conformance` calls it on every handshake, declares its tier as
`complexity_tier_supported`, and returns what it read as a top-level
`tier_evidence` object. A lab that falls short is `supported: false` with an
`unmet_requirements` entry naming the requirement, what was observed and
what was required. The lab's level is not consulted.

- **`tier_evidence` sits beside `requirements`, not inside it.** The SI
  profile's schema for that map sets `additionalProperties: false`. The spec
  does not fix an evidence shape, so this is the smallest that carries what
  was read: `tier`, `met`, `observed_at`, and one check per requirement with
  `requirement`, `required`, `observed` and `status`.
- **petri verifies Tier 1 and declares nothing higher.** A lab created at
  level 2 or 3 is evidenced at Tier 1. Declaring 2 for it would be the echo
  this ADR removes.
- **Three bullets are reported and not judged**: the namespace ceiling, the
  microservice count and the security zones carry `status: not_assessed`.
  The profile does not yet say whether platform and per-scenario namespaces
  count, or whether a zone is a property of the lab or of a scenario. The
  provider states what the cluster has and decides nothing.
- **A read that fails is an unmet check.** Evidence that could not be
  collected is not evidence the tier is met.

**Amended 2026-11-19 — the three unjudged bullets leave `checks`, and
logging joins them.** The profile has since said what counts
(`profile.md` §8, opening): the namespace, microservice and security-zone
figures count what scenario preconditions declare, and the evaluation
implementation judges them from the scenarios it runs. They are not
properties of the lab, so they are not the provider's checks, and
`not_assessed` is gone from the evidence with them: a status is `met` or
`unmet`. What the lab holds is still reported, in an `observations` list
beside `checks` (`subject`, `observed`), which carries no requirement and no
status and does not bear on `met`.

§8 Tier 1 also names `observability-logging`, which the evidence never
checked. A lab's log interface is the cluster's own pod logs
(`interface-types.md` lists Kubernetes pod logs for the type), so the check
reads them: on every Ready node, the last lines of a running `kube-system`
pod, through the API server. It is met when each node returned at least one
line. Every node, because the API server proxies a log read to the kubelet
of the pod's node, and a node whose logs cannot be read is one a scenario's
workload can land on. `kube-system`, because every node runs a pod there
before any scenario is provisioned. Rejected: checking that a log
aggregator Deployment is ready, as the other components are checked. A lab
installs none, and the agent reads logs through the orchestration API.

**Every local lab is one control-plane node and three workers.** Three
workers, not two, because that is what scenarios bind to: declared node
names are bound to workers, only workers are shaped to the OASIS
allocatable, and C-OE-002 declares three. The count is the same at every
level, so that a higher level is never a smaller cluster. A level's
`nodes_per_cluster` still sizes cloud node groups only.

**Certificate management is cert-manager, vendored.**
`templates/platform/cert-manager.yaml` is the upstream static manifest of
one release, unmodified, and `platform.CertManagerVersion` names it. It is
embedded rather than fetched at create time, so the pin is a file in a
commit and a lab does not need GitHub to be created. Its images come from
`quay.io`, not Docker Hub.

**A create waits for what it installs, and names what it does not.** After
applying platform and observability manifests the orchestrator waits for
each Deployment the generators declare; without that, a lab handed over as
ACTIVE fails its tier check for as long as an image takes to pull. Names a
level lists and no generator installs are returned by `Unrendered`, logged,
recorded in the lab's `not_installed` metadata and shown by `petri info`.
The gitops controller is removed from every company's level-1 platform list,
since Tier 1 does not require one and petri installs none.

**The ingress controller runs two nginx workers.** Reading the evidence
from a live lab showed the controller meeting the check between crashes:
nginx starts one worker per CPU it can see, a kind node sees the whole
Docker host, and on a ten-CPU host the controller rested at 247Mi of its
256Mi limit and was OOM-killed on every reload, one per Ingress created.
`worker-processes: "2"` fixes the footprint whatever the host (about 90Mi at
rest, 145Mi across four reloads), and the limit is 512Mi.

Rejected:

- **Keeping the level as the declared tier and adding evidence beside it.**
  A runner compares the requested tier with the declared one; a declared
  tier that is not the verified one keeps that comparison circular.
- **Applying cert-manager from its release URL**, as Calico is. It makes lab
  creation depend on a third host and moves the pin out of review.
- **Failing `petri create` when the lab misses its tier.** A lab is useful
  below a tier, and the existing posture for platform errors and for an
  unshaped lab is a warning. The claim is refused where it is made: at the
  handshake.

## Consequences

- A level-1 lab is four kind nodes and three more Deployments than before.
  It takes longer to create and more memory to hold; each node pulls its own
  images.
- A lab created before this change does not meet Tier 1 and now says so:
  its handshake returns `supported: false`. Recreate it.
- `complexity_tier_supported` reads `1` even on a lab that fails the check,
  because the schema's enum has no zero. A runner reads `tier_evidence.met`
  or `unmet_requirements`, not the number alone.
- The conformance endpoint takes no tier, so petri verifies the tier it
  declares, not the one a run requests. Comparing the two stays the
  runner's.
- A Tier 2 verification is new work, not a constant to change:
  `VerifiableTier` moves only with checks behind it.
- Moving the cert-manager pin is replacing one file and one constant;
  `TestCertManagerManifest` fails if they disagree.
