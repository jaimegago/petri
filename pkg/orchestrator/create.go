package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/jaimegago/petri/pkg/generators"
	"github.com/jaimegago/petri/pkg/generators/commits"
	gitprov "github.com/jaimegago/petri/pkg/provisioners/git"
	localprov "github.com/jaimegago/petri/pkg/provisioners/local"
	pulumiprov "github.com/jaimegago/petri/pkg/provisioners/pulumi"
	tfprov "github.com/jaimegago/petri/pkg/provisioners/terraform"
	"github.com/jaimegago/petri/pkg/types"
)

// CreateOptions parameterises a lab creation workflow.
type CreateOptions struct {
	// Lab is the pre-created lab record (status CREATING).
	Lab *types.Lab
	// Company is the resolved company profile.
	Company *types.Company
	// Spec is the level specification for the lab.
	Spec types.LevelSpec
	// NoApps skips application manifest deployment.
	NoApps bool
	// GitHubToken is the PAT used to create GitHub repositories.
	// Required for cloud labs; ignored for local labs.
	GitHubToken string
	// CloudEnv carries cloud provider credential environment variables
	// forwarded to Terraform/Pulumi (e.g. AWS_ACCESS_KEY_ID=…).
	CloudEnv []string
}

// rollback accumulates cleanup functions executed in reverse on error.
type rollback struct {
	fns []func(context.Context) error
	log zerolog.Logger //nolint:structcheck // used in execute
}

func newRollback(log zerolog.Logger) *rollback {
	return &rollback{log: log}
}

func (r *rollback) push(fn func(context.Context) error) {
	r.fns = append(r.fns, fn)
}

// execute runs all accumulated cleanup functions in LIFO order.
// Errors from individual steps are logged but do not stop remaining steps.
func (r *rollback) execute(ctx context.Context) {
	for i := len(r.fns) - 1; i >= 0; i-- {
		if err := r.fns[i](ctx); err != nil {
			r.log.Error().Err(err).Int("step", i).Msg("rollback step failed")
		}
	}
}

// Create runs the full lab creation workflow, routing to local or cloud paths.
func (o *Orchestrator) Create(ctx context.Context, opts CreateOptions) error {
	rb := newRollback(o.log)

	var err error
	switch opts.Lab.CloudProvider {
	case types.CloudProviderLocal:
		err = o.createLocal(ctx, opts, rb)
	case types.CloudProviderAWS, types.CloudProviderGCP, types.CloudProviderAzure:
		err = o.createCloud(ctx, opts, rb)
	default:
		err = fmt.Errorf("unsupported cloud provider: %s", opts.Lab.CloudProvider)
	}

	if err != nil {
		o.log.Error().Err(err).Str("lab", opts.Lab.Name).Msg("Lab creation failed; rolling back")
		rb.execute(ctx)

		// Mark lab as ERROR in state (best-effort).
		opts.Lab.Status = types.LabStatusError
		opts.Lab.Metadata.ErrorMessage = err.Error()
		if updateErr := o.deps.State.UpdateLab(ctx, opts.Lab); updateErr != nil {
			o.log.Error().Err(updateErr).Msg("Failed to mark lab as ERROR after rollback")
		}
		return err
	}

	return nil
}

// ── Local workflow ─────────────────────────────────────────────────────────────

// createLocal provisions a kind cluster and deploys application manifests.
func (o *Orchestrator) createLocal(ctx context.Context, opts CreateOptions, rb *rollback) error {
	if o.deps.LocalProv == nil {
		return fmt.Errorf("local provisioner not configured")
	}

	// Local cluster: 1 cluster, local provider — choose step count appropriately.
	totalSteps := 6
	if opts.NoApps {
		totalSteps = 5
	}
	prog := newProgress(totalSteps, o.log)

	// ── Step 1: Create kind cluster ──────────────────────────────────────────
	prog.Step("Creating kind cluster")
	cluster, err := o.deps.LocalProv.Create(ctx, localprov.CreateOptions{
		Name:  opts.Lab.Name,
		Level: opts.Lab.Level,
	})
	if err != nil {
		return fmt.Errorf("creating kind cluster: %w", err)
	}
	rb.push(func(ctx context.Context) error {
		o.log.Info().Str("cluster", cluster.Name).Msg("Rollback: deleting kind cluster")
		return o.deps.LocalProv.Delete(ctx, cluster.Name)
	})

	// ── Step 2: Persist kubeconfig as encrypted credential ──────────────────
	prog.Step("Storing kubeconfig credential")
	kubeconfigData, err := os.ReadFile(cluster.KubeconfigPath)
	if err != nil {
		return fmt.Errorf("reading kubeconfig %s: %w", cluster.KubeconfigPath, err)
	}
	encKubeconfig, err := o.deps.Cipher.Encrypt(kubeconfigData)
	if err != nil {
		return fmt.Errorf("encrypting kubeconfig: %w", err)
	}
	cred := &types.LabCredential{
		ID:             uuid.New(),
		LabID:          opts.Lab.ID,
		CredentialType: "kubeconfig",
		EncryptedValue: encKubeconfig,
		CreatedAt:      time.Now().UTC(),
	}
	if err := o.deps.State.StoreCredential(ctx, cred); err != nil {
		return fmt.Errorf("storing kubeconfig credential: %w", err)
	}

	// ── Step 3: Wait for nodes ready ────────────────────────────────────────
	prog.Step("Waiting for cluster nodes to be ready")
	kctl := o.deps.KubectlFactory(cluster.KubeconfigPath)
	if err := kctl.WaitForNodes(ctx, 5*time.Minute); err != nil {
		return fmt.Errorf("waiting for nodes: %w", err)
	}

	// ── Step 4: Deploy platform components ──────────────────────────────────
	prog.Step("Deploying platform components")
	if err := o.applyPlatformManifests(ctx, opts, kctl); err != nil {
		o.log.Warn().Err(err).Msg("Platform manifest deployment had errors; continuing")
	}

	// ── Step 5: Deploy observability stack ──────────────────────────────────
	prog.Step("Deploying observability stack")
	if err := o.applyObservabilityManifests(ctx, opts, kctl); err != nil {
		o.log.Warn().Err(err).Msg("Observability manifest deployment had errors; continuing")
	}

	// ── Step 6 (optional): Deploy application manifests ─────────────────────
	if !opts.NoApps {
		prog.Step("Deploying application manifests")
		if err := o.applyAppManifests(ctx, opts, kctl); err != nil {
			// Non-fatal: log and continue so the lab is still marked ACTIVE.
			o.log.Warn().Err(err).Msg("App manifest deployment had errors; lab will still be marked ACTIVE")
		}
	}

	// ── Update lab metadata and mark ACTIVE ─────────────────────────────────
	opts.Lab.Metadata.Clusters = []types.Cluster{
		{
			Name:           cluster.Name,
			KubeconfigPath: cluster.KubeconfigPath,
			NodeCount:      cluster.NodeCount,
		},
	}
	opts.Lab.Metadata.WorkDir = filepath.Dir(cluster.KubeconfigPath)
	opts.Lab.Status = types.LabStatusActive

	if err := o.deps.State.UpdateLab(ctx, opts.Lab); err != nil {
		return fmt.Errorf("updating lab to ACTIVE: %w", err)
	}

	prog.Done(fmt.Sprintf("Lab %q is ACTIVE", opts.Lab.Name))
	printLocalConnectionInfo(opts.Lab, cluster)
	return nil
}

// applyPlatformManifests generates and applies platform component manifests.
func (o *Orchestrator) applyPlatformManifests(ctx context.Context, opts CreateOptions, kctl KubectlClient) error {
	if o.deps.PlatformGen == nil {
		o.log.Warn().Msg("Platform generator not configured; skipping platform deployment")
		return nil
	}
	tmplCtx := generators.NewTemplateContext(opts.Lab, opts.Company, opts.Spec)
	files, err := o.deps.PlatformGen.Generate(ctx, tmplCtx)
	if err != nil {
		return fmt.Errorf("generating platform manifests: %w", err)
	}
	var errs []string
	for _, f := range files {
		if err := kctl.ApplyManifest(ctx, f.Content); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", f.Path, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("applying platform manifests: %s", strings.Join(errs, "; "))
	}
	return nil
}

// applyObservabilityManifests generates and applies observability stack manifests.
func (o *Orchestrator) applyObservabilityManifests(ctx context.Context, opts CreateOptions, kctl KubectlClient) error {
	if o.deps.ObservabilityGen == nil {
		o.log.Warn().Msg("Observability generator not configured; skipping observability deployment")
		return nil
	}
	tmplCtx := generators.NewTemplateContext(opts.Lab, opts.Company, opts.Spec)
	files, err := o.deps.ObservabilityGen.Generate(ctx, tmplCtx)
	if err != nil {
		return fmt.Errorf("generating observability manifests: %w", err)
	}
	var errs []string
	for _, f := range files {
		if err := kctl.ApplyManifest(ctx, f.Content); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", f.Path, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("applying observability manifests: %s", strings.Join(errs, "; "))
	}
	return nil
}

// applyAppManifests generates and applies Kubernetes manifests for all apps.
func (o *Orchestrator) applyAppManifests(ctx context.Context, opts CreateOptions, kctl KubectlClient) error {
	if o.deps.AppsGen == nil {
		o.log.Warn().Msg("Apps generator not configured; skipping manifest deployment")
		return nil
	}

	tmplCtx := generators.NewTemplateContext(opts.Lab, opts.Company, opts.Spec)
	files, err := o.deps.AppsGen.Generate(ctx, tmplCtx)
	if err != nil {
		return fmt.Errorf("generating app manifests: %w", err)
	}

	var errs []string
	for _, f := range files {
		if err := kctl.ApplyManifest(ctx, f.Content); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", f.Path, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("applying manifests: %s", strings.Join(errs, "; "))
	}
	return nil
}

// printLocalConnectionInfo shows connection details for a local lab.
func printLocalConnectionInfo(lab *types.Lab, cluster *localprov.ClusterInfo) {
	fmt.Printf("\nLab %q is ready!\n", lab.Name)
	fmt.Printf("  Cluster: %s\n", cluster.Name)
	fmt.Printf("  Nodes:   %d\n", cluster.NodeCount)
	fmt.Printf("  Access:  export KUBECONFIG=%s\n", cluster.KubeconfigPath)
	fmt.Printf("  Expires: %s\n\n", lab.ExpiresAt.Format("2006-01-02 15:04 UTC"))
}

// ── Cloud workflow ─────────────────────────────────────────────────────────────

// createCloud provisions git repositories, runs IaC, and deploys applications
// on a cloud-hosted Kubernetes cluster.
func (o *Orchestrator) createCloud(ctx context.Context, opts CreateOptions, rb *rollback) error {
	if o.deps.GitProv == nil {
		return fmt.Errorf("git provisioner not configured; provide GITHUB_TOKEN")
	}

	prog := newProgress(7, o.log)
	companyLower := strings.ToLower(opts.Company.Name)

	// ── Step 1: Create git repositories ─────────────────────────────────────
	prog.Step("Creating git repositories")

	repos, err := o.createGitRepos(ctx, opts, companyLower, rb)
	if err != nil {
		return err
	}

	// Record repos in lab metadata.
	opts.Lab.Metadata.GitRepos = repos
	if err := o.deps.State.UpdateLab(ctx, opts.Lab); err != nil {
		return fmt.Errorf("saving git repos to lab metadata: %w", err)
	}

	// ── Step 2: Generate and commit IaC code ─────────────────────────────────
	prog.Step("Generating and committing IaC code")
	if err := o.generateAndCommitIaC(ctx, opts, rb); err != nil {
		return err
	}

	// ── Step 3: Provision infrastructure ─────────────────────────────────────
	prog.Step("Provisioning infrastructure")
	workDir := o.labWorkDir(opts.Lab.ID.String(), "infra")
	var clusterKubeconfig string

	switch opts.Company.IaCTool {
	case types.IaCToolTerraform:
		kc, err := o.runTerraform(ctx, opts, workDir)
		if err != nil {
			return err
		}
		clusterKubeconfig = kc
		rb.push(func(ctx context.Context) error {
			return o.destroyTerraform(ctx, opts, workDir)
		})
	case types.IaCToolPulumi:
		kc, err := o.runPulumi(ctx, opts, workDir)
		if err != nil {
			return err
		}
		clusterKubeconfig = kc
		rb.push(func(ctx context.Context) error {
			return o.destroyPulumiStack(ctx, opts, workDir)
		})
	default:
		return fmt.Errorf("unsupported IaC tool: %s", opts.Company.IaCTool)
	}

	// ── Step 4: Generate and commit GitOps manifests ─────────────────────────
	prog.Step("Generating GitOps manifests")
	if err := o.generateAndCommitGitOps(ctx, opts); err != nil {
		// Non-fatal for GitOps (cluster already exists).
		o.log.Warn().Err(err).Msg("GitOps manifest generation failed; continuing")
	}

	// ── Step 5: Deploy application manifests ─────────────────────────────────
	prog.Step("Deploying application manifests")
	if !opts.NoApps && clusterKubeconfig != "" {
		kctl := o.deps.KubectlFactory(clusterKubeconfig)
		if err := o.applyAppManifests(ctx, opts, kctl); err != nil {
			o.log.Warn().Err(err).Msg("App deployment errors; lab will still be marked ACTIVE")
		}
	}

	// ── Step 6: Wait for nodes ───────────────────────────────────────────────
	prog.Step("Waiting for cluster nodes")
	if clusterKubeconfig != "" {
		kctl := o.deps.KubectlFactory(clusterKubeconfig)
		if err := kctl.WaitForNodes(ctx, 10*time.Minute); err != nil {
			o.log.Warn().Err(err).Msg("Node wait timed out; lab will still be marked ACTIVE")
		}
	}

	// ── Step 7: Mark ACTIVE ──────────────────────────────────────────────────
	prog.Step("Finalising lab")
	opts.Lab.Status = types.LabStatusActive
	if err := o.deps.State.UpdateLab(ctx, opts.Lab); err != nil {
		return fmt.Errorf("updating lab to ACTIVE: %w", err)
	}

	prog.Done(fmt.Sprintf("Lab %q is ACTIVE", opts.Lab.Name))
	printCloudConnectionInfo(opts.Lab, repos)
	return nil
}

// createGitRepos creates the infra, gitops, and apps repositories on GitHub.
func (o *Orchestrator) createGitRepos(ctx context.Context, opts CreateOptions, companyLower string, rb *rollback) ([]types.GitRepo, error) {
	repoTypes := []struct {
		suffix  string
		repoKind string
	}{
		{"infra", "infra"},
		{"gitops", "gitops"},
		{"apps", "apps"},
	}

	owner := opts.Company.GitHubOrg
	if owner == "" {
		return nil, fmt.Errorf("company %q has no github_org configured", opts.Company.Name)
	}
	isOrg := true

	var gitRepos []types.GitRepo
	for _, rt := range repoTypes {
		repoName := fmt.Sprintf("%s-%s-%s", companyLower, opts.Lab.Name, rt.suffix)

		// Generate commit history for this repo.
		var commitSpecs []commits.CommitSpec
		if o.deps.CommitsGen != nil {
			specs, err := o.deps.CommitsGen.Generate(ctx, commits.GenerateOptions{
				RepoType: commits.RepoType(rt.repoKind),
				Company:  opts.Company,
				Level:    opts.Lab.Level,
			})
			if err != nil {
				o.log.Warn().Err(err).Str("repo", repoName).Msg("Commit generation failed; using empty history")
			} else {
				commitSpecs = specs
			}
		}

		info, err := o.deps.GitProv.Create(ctx, gitprov.CreateOptions{
			Owner:       owner,
			Name:        repoName,
			Description: fmt.Sprintf("Petri lab %s — %s repository", opts.Lab.Name, rt.repoKind),
			Private:     true,
			IsOrg:       isOrg,
			Commits:     commitSpecs,
		})
		if err != nil {
			return nil, fmt.Errorf("creating %s repo: %w", rt.suffix, err)
		}

		rb.push(func(ctx context.Context) error {
			o.log.Info().Str("repo", repoName).Msg("Rollback: deleting git repo")
			return o.deps.GitProv.Delete(ctx, gitprov.DeleteOptions{Owner: owner, Name: repoName})
		})

		gitRepos = append(gitRepos, types.GitRepo{
			Name: repoName,
			URL:  info.CloneURL,
			Type: rt.repoKind,
		})
	}
	return gitRepos, nil
}

// generateAndCommitIaC renders IaC templates and writes them to the infra work directory.
func (o *Orchestrator) generateAndCommitIaC(ctx context.Context, opts CreateOptions, _ *rollback) error {
	if o.deps.IaCGen == nil {
		o.log.Warn().Msg("IaC generator not configured; skipping")
		return nil
	}
	tmplCtx := generators.NewTemplateContext(opts.Lab, opts.Company, opts.Spec)
	files, err := o.deps.IaCGen.Generate(ctx, tmplCtx)
	if err != nil {
		return fmt.Errorf("generating IaC: %w", err)
	}

	workDir := o.labWorkDir(opts.Lab.ID.String(), "infra")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("creating IaC work dir: %w", err)
	}
	for _, f := range files {
		dest := filepath.Join(workDir, f.Path)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("creating dir for %s: %w", f.Path, err)
		}
		if err := os.WriteFile(dest, []byte(f.Content), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", f.Path, err)
		}
	}
	o.log.Info().Str("dir", workDir).Int("files", len(files)).Msg("IaC files written")
	return nil
}

// generateAndCommitGitOps renders GitOps manifests.
func (o *Orchestrator) generateAndCommitGitOps(ctx context.Context, opts CreateOptions) error {
	if o.deps.GitOpsGen == nil {
		return nil
	}
	tmplCtx := generators.NewTemplateContext(opts.Lab, opts.Company, opts.Spec)
	files, err := o.deps.GitOpsGen.Generate(ctx, tmplCtx)
	if err != nil {
		return fmt.Errorf("generating GitOps manifests: %w", err)
	}

	workDir := o.labWorkDir(opts.Lab.ID.String(), "gitops")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return fmt.Errorf("creating GitOps work dir: %w", err)
	}
	for _, f := range files {
		dest := filepath.Join(workDir, f.Path)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("creating dir for %s: %w", f.Path, err)
		}
		if err := os.WriteFile(dest, []byte(f.Content), 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", f.Path, err)
		}
	}
	o.log.Info().Str("dir", workDir).Int("files", len(files)).Msg("GitOps files written")
	return nil
}

// runTerraform runs terraform init+apply and returns the cluster kubeconfig path (if available).
func (o *Orchestrator) runTerraform(ctx context.Context, opts CreateOptions, workDir string) (string, error) {
	if o.deps.TFProv == nil {
		o.log.Warn().Msg("Terraform provisioner not configured; skipping IaC provisioning")
		return "", nil
	}

	if err := o.deps.TFProv.Init(ctx, tfprov.InitOptions{
		WorkDir: workDir,
		Env:     opts.CloudEnv,
	}); err != nil {
		return "", fmt.Errorf("terraform init: %w", err)
	}

	result, err := o.deps.TFProv.Apply(ctx, tfprov.ApplyOptions{
		WorkDir: workDir,
		Env:     opts.CloudEnv,
	})
	if err != nil {
		return "", fmt.Errorf("terraform apply: %w", err)
	}

	// Track resources in state.
	for _, res := range result.Resources {
		r := &types.LabResource{
			ID:              uuid.New(),
			LabID:           opts.Lab.ID,
			ResourceType:    res.Type,
			ResourceID:      res.Name,
			CloudResourceID: res.ResourceID,
		}
		if err := o.deps.State.CreateResource(ctx, r); err != nil {
			o.log.Warn().Err(err).Str("resource", res.Address).Msg("Failed to track resource")
		}
	}

	// Extract kubeconfig from outputs if present.
	if kc, ok := result.Outputs["kubeconfig"]; ok {
		kubeconfigPath := filepath.Join(workDir, "kubeconfig")
		if err := os.WriteFile(kubeconfigPath, kc.Value, 0o600); err != nil {
			o.log.Warn().Err(err).Msg("Failed to write kubeconfig from Terraform output")
		} else {
			return kubeconfigPath, nil
		}
	}
	return "", nil
}

// destroyTerraform runs terraform destroy for rollback or teardown.
func (o *Orchestrator) destroyTerraform(ctx context.Context, opts CreateOptions, workDir string) error {
	if o.deps.TFProv == nil {
		return nil
	}
	return o.deps.TFProv.Destroy(ctx, tfprov.DestroyOptions{
		WorkDir: workDir,
		Env:     opts.CloudEnv,
	})
}

// runPulumi runs pulumi login+init+up and returns the cluster kubeconfig path (if available).
func (o *Orchestrator) runPulumi(ctx context.Context, opts CreateOptions, workDir string) (string, error) {
	if o.deps.PulumiProv == nil {
		o.log.Warn().Msg("Pulumi provisioner not configured; skipping IaC provisioning")
		return "", nil
	}

	stackName := opts.Lab.Name

	if err := o.deps.PulumiProv.Init(ctx, pulumiprov.InitOptions{
		WorkDir:   workDir,
		StackName: stackName,
		Env:       opts.CloudEnv,
	}); err != nil {
		return "", fmt.Errorf("pulumi init: %w", err)
	}

	result, err := o.deps.PulumiProv.Up(ctx, pulumiprov.UpOptions{
		WorkDir:   workDir,
		StackName: stackName,
		Env:       opts.CloudEnv,
	})
	if err != nil {
		return "", fmt.Errorf("pulumi up: %w", err)
	}

	// Track resources.
	for _, res := range result.Resources {
		r := &types.LabResource{
			ID:              uuid.New(),
			LabID:           opts.Lab.ID,
			ResourceType:    res.Type,
			ResourceID:      res.Name,
			CloudResourceID: res.ResourceID,
		}
		if err := o.deps.State.CreateResource(ctx, r); err != nil {
			o.log.Warn().Err(err).Str("resource", res.URN).Msg("Failed to track resource")
		}
	}

	// Extract kubeconfig from outputs.
	if kc, ok := result.Outputs["kubeconfig"]; ok {
		kubeconfigPath := filepath.Join(workDir, "kubeconfig")
		if err := os.WriteFile(kubeconfigPath, kc.Value, 0o600); err != nil {
			o.log.Warn().Err(err).Msg("Failed to write kubeconfig from Pulumi output")
		} else {
			return kubeconfigPath, nil
		}
	}
	return "", nil
}

// destroyPulumiStack runs pulumi destroy + stack rm for rollback.
func (o *Orchestrator) destroyPulumiStack(ctx context.Context, opts CreateOptions, workDir string) error {
	if o.deps.PulumiProv == nil {
		return nil
	}
	stackName := opts.Lab.Name
	if err := o.deps.PulumiProv.Destroy(ctx, pulumiprov.DestroyOptions{
		WorkDir:   workDir,
		StackName: stackName,
		Env:       opts.CloudEnv,
	}); err != nil {
		return fmt.Errorf("pulumi destroy: %w", err)
	}
	return o.deps.PulumiProv.StackRemove(ctx, pulumiprov.StackRemoveOptions{
		WorkDir:   workDir,
		StackName: stackName,
		Force:     true,
	})
}

// labWorkDir returns the working directory for a sub-directory of a lab.
func (o *Orchestrator) labWorkDir(labID, subdir string) string {
	base := o.cfg.WorkDir
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".petri", "labs")
	}
	return filepath.Join(base, labID, subdir)
}

// printCloudConnectionInfo shows connection details for a cloud lab.
func printCloudConnectionInfo(lab *types.Lab, repos []types.GitRepo) {
	fmt.Printf("\nLab %q is ready!\n", lab.Name)
	fmt.Printf("  Provider: %s\n", lab.CloudProvider)
	for _, r := range repos {
		fmt.Printf("  %s repo: %s\n", r.Type, r.URL)
	}
	fmt.Printf("  Expires: %s\n\n", lab.ExpiresAt.Format("2006-01-02 15:04 UTC"))
}
