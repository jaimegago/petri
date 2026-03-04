// Package orchestrator coordinates the lab lifecycle across provisioners and generators.
// It implements the create, destroy, and cleanup workflows for Petri labs.
package orchestrator

import (
	"context"
	"time"

	"github.com/rs/zerolog"

	"github.com/jaimegago/petri/pkg/crypto"
	"github.com/jaimegago/petri/pkg/generators"
	"github.com/jaimegago/petri/pkg/generators/commits"
	gitprov "github.com/jaimegago/petri/pkg/provisioners/git"
	localprov "github.com/jaimegago/petri/pkg/provisioners/local"
	pulumiprov "github.com/jaimegago/petri/pkg/provisioners/pulumi"
	tfprov "github.com/jaimegago/petri/pkg/provisioners/terraform"
	"github.com/jaimegago/petri/pkg/state"
)

// ── Interfaces (defined in the consuming package per Go standards) ─────────────

// LocalProvisioner abstracts kind/k3s cluster management.
type LocalProvisioner interface {
	Create(ctx context.Context, opts localprov.CreateOptions) (*localprov.ClusterInfo, error)
	Delete(ctx context.Context, name string) error
}

// GitProvisioner abstracts git repository management.
type GitProvisioner interface {
	Create(ctx context.Context, opts gitprov.CreateOptions) (*gitprov.RepoInfo, error)
	Delete(ctx context.Context, opts gitprov.DeleteOptions) error
}

// TerraformProvisioner abstracts Terraform operations.
type TerraformProvisioner interface {
	Init(ctx context.Context, opts tfprov.InitOptions) error
	Apply(ctx context.Context, opts tfprov.ApplyOptions) (*tfprov.ApplyResult, error)
	Destroy(ctx context.Context, opts tfprov.DestroyOptions) error
	Output(ctx context.Context, workDir string, env []string) (map[string]tfprov.OutputValue, error)
}

// PulumiProvisioner abstracts Pulumi stack operations.
type PulumiProvisioner interface {
	Init(ctx context.Context, opts pulumiprov.InitOptions) error
	Up(ctx context.Context, opts pulumiprov.UpOptions) (*pulumiprov.UpResult, error)
	Destroy(ctx context.Context, opts pulumiprov.DestroyOptions) error
	StackRemove(ctx context.Context, opts pulumiprov.StackRemoveOptions) error
}

// KubectlClient abstracts kubectl operations against a cluster.
type KubectlClient interface {
	ApplyManifest(ctx context.Context, manifest string) error
	WaitForNodes(ctx context.Context, timeout time.Duration) error
	WaitForRollout(ctx context.Context, namespace, deployment string, timeout time.Duration) error
	GetNodeCount(ctx context.Context) (int, error)
}

// KubectlClientFactory creates a KubectlClient for a given kubeconfig path.
type KubectlClientFactory func(kubeconfigPath string) KubectlClient

// IaCGenerator renders IaC templates (Terraform/Pulumi).
type IaCGenerator interface {
	Generate(ctx context.Context, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error)
}

// GitOpsGenerator renders GitOps manifests (ArgoCD/Flux/Anthos).
type GitOpsGenerator interface {
	Generate(ctx context.Context, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error)
}

// AppsGenerator renders Kubernetes application manifests.
type AppsGenerator interface {
	Generate(ctx context.Context, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error)
}

// CommitsGenerator generates realistic git commit history.
type CommitsGenerator interface {
	Generate(ctx context.Context, opts commits.GenerateOptions) ([]commits.CommitSpec, error)
}

// ── Config & Deps ─────────────────────────────────────────────────────────────

// Config holds operational settings for the orchestrator.
type Config struct {
	// WorkDir is the base directory for lab working files (IaC, clones).
	// Defaults to ~/.petri/labs/ when empty.
	WorkDir string
}

// Deps bundles all injectable dependencies for the orchestrator.
// Fields may be nil if the corresponding functionality is not needed
// (e.g. GitProv is nil for local-only labs).
type Deps struct {
	State         state.Manager
	Cipher        crypto.Cipher
	Log           zerolog.Logger
	LocalProv     LocalProvisioner
	GitProv       GitProvisioner
	TFProv        TerraformProvisioner
	PulumiProv    PulumiProvisioner
	KubectlFactory KubectlClientFactory
	IaCGen        IaCGenerator
	GitOpsGen     GitOpsGenerator
	AppsGen       AppsGenerator
	CommitsGen    CommitsGenerator
}

// ── Orchestrator ──────────────────────────────────────────────────────────────

// Orchestrator coordinates all provisioners and generators for lab lifecycle operations.
type Orchestrator struct {
	cfg  Config
	deps Deps
	log  zerolog.Logger
}

// New returns an Orchestrator with the given configuration and dependencies.
func New(cfg Config, deps Deps) *Orchestrator {
	return &Orchestrator{
		cfg:  cfg,
		deps: deps,
		log:  deps.Log,
	}
}
