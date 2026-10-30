# lab-repo-instances
Status: open

Filed 2026-10-30 from the design session in
`joe-pm/threads/lab-app-family-design.md`. The whole design, which spans
repositories, is `joe-pm/queue/lab-app-family.md`; this item is the part only
petri can hold. It has open design questions below, so it gets a design session
before an order.

## What it is

Petri holds access to a set of source repositories, applications and IaC, and
creates a lab-specific repository instance from them, carrying the content that
lab or test needs, git history included. It is a capability of petri and knows
nothing about any one agent or about OASIS: a `petri create` lab uses it the same
way a scenario does.

An application's defective versions and their fixes live on branches of its
source repository. The instance is why that is safe: what an agent reads is the
instance, not the source.

## What an instance guarantees

- **History runs up to the deployed version, and later objects are absent.** Not
  hidden behind a tag or a pin: joe's `git_read` answers at any commit named, from
  whatever its clone contains (`joe:internal/tools/core/gitread.go:34-38`).
- **It is the only copy in the lab.** An agent's token may be bound to cluster-admin
  (`joe-oasis-e2e:internal/labcreds/labcreds.go:166-170`), so a fuller copy anywhere
  in the lab, including its Secrets, is exposed.
- **Nothing deployed names the source repository**: not the instance's remote
  configuration, image labels or chart metadata.
- **It matches what is running.** Its tag and the deployed image come from one
  commit. Petri pins tags and image digests, never branches, because a branch head
  moves.
- **It carries no classification.** Which release is defective, of what class, with
  what symptom lives in petri's catalog (`pkg/fault`), never in the instance.

**This does not claim network isolation.** An agent with open web access can still
find a public source repository. That limit is accepted and stated in
`joe-pm/queue/lab-app-family.md` § The accepted gap.

## Where it starts from

- Local labs already get per-lab on-disk repositories with `file://` URLs
  (`pkg/orchestrator/create.go:237-241`, `pkg/provisioners/git/local.go:66`); cloud
  labs use GitHub (`create.go:625`).
- There is no in-lab git server; a search for gitea, gogs, git-daemon, soft-serve and
  gitolite found none.
- Every GitOps template points at github.com
  (`templates/gitops/argocd/application.yaml.tmpl:20`,
  `templates/gitops/flux/gotk-sync.yaml.tmpl:17`,
  `templates/gitops/anthos/config-management.yaml.tmpl:15`).
- Under OASIS, `gitops-application` is a ConfigMap with a `source_repo` string, not a
  repository (`pkg/oasis/translate.go:1122-1124`).

## Open questions

- **Where is an instance served from?** A host path works for an agent on the host,
  which is where the harness runs joe today. An in-lab server works for an agent
  anywhere and needs building. UNVERIFIED: whether joe's git adapter accepts an
  in-lab or `file://` URL; it takes HTTPS-token or no credentials
  (`joe:docs/project/DECISIONS.md:1150`).
- **Is history preserved or rewritten?** Preserved commit hashes can be searched for
  on the open web, and rewritten ones no longer match the source repository. Tags and
  image provenance have to agree with whichever is chosen.
- **What does the source set look like?** How it is declared, and how a lab or
  scenario names the version it wants.
