package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"log/slog"

	"github.com/google/uuid"

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
	// OASISMode enables audit logging and Calico CNI (for NetworkPolicy support)
	// on the kind cluster. Use this when the lab will be used with petri serve.
	OASISMode bool
	// GitHubToken is the PAT used to create GitHub repositories.
	// Required for cloud labs; ignored for local labs.
	GitHubToken string
	// CloudEnv carries cloud provider credential environment variables
	// forwarded to Terraform/Pulumi (e.g. AWS_ACCESS_KEY_ID=…).
	CloudEnv []string
	// GCPProjectID is the GCP project a GCP lab is created in. Required for
	// GCP labs; recorded in lab metadata so teardown targets the same project.
	GCPProjectID string
}

// rollback accumulates cleanup functions executed in reverse on error.
type rollback struct {
	fns []func(context.Context) error
	log *slog.Logger
}

func newRollback(log *slog.Logger) *rollback {
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
			r.log.Error("rollback step failed", "error", err, "step", i)
		}
	}
}

// Create runs the full lab creation workflow, routing to local or cloud paths.
func (o *Orchestrator) Create(ctx context.Context, opts CreateOptions) error {
	rb := newRollback(o.log)
	start := time.Now()

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
		o.log.Error("Lab creation failed; rolling back", "error", err, "lab", opts.Lab.Name)
		rb.execute(ctx)

		// Mark lab as ERROR in state (best-effort).
		opts.Lab.Status = types.LabStatusError
		opts.Lab.Metadata.ErrorMessage = err.Error()
		if updateErr := o.deps.State.UpdateLab(ctx, opts.Lab); updateErr != nil {
			o.log.Error("Failed to mark lab as ERROR after rollback", "error", updateErr)
		}
		return err
	}

	if o.deps.Metrics != nil {
		provider := string(opts.Lab.CloudProvider)
		o.deps.Metrics.LabCreated(opts.Company.Name, opts.Lab.Level, provider)
		o.deps.Metrics.ObserveCreate(opts.Company.Name, opts.Lab.Level, provider, time.Since(start))
	}

	return nil
}

// ── Local workflow ─────────────────────────────────────────────────────────────

// createLocal provisions a kind cluster, creates git repositories with app manifests,
// and deploys application manifests to the cluster.
func (o *Orchestrator) createLocal(ctx context.Context, opts CreateOptions, rb *rollback) error {
	if o.deps.LocalProv == nil {
		return fmt.Errorf("local provisioner not configured")
	}

	totalSteps := 7
	if opts.NoApps {
		totalSteps = 6
	}
	prog := newProgress(totalSteps, o.log)

	// ── Step 1: Create kind cluster ──────────────────────────────────────────
	prog.Step("Creating kind cluster")
	cluster, err := o.deps.LocalProv.Create(ctx, localprov.CreateOptions{
		Name:      opts.Lab.Name,
		Level:     opts.Lab.Level,
		OASISMode: opts.OASISMode,
	})
	if err != nil {
		return fmt.Errorf("creating kind cluster: %w", err)
	}
	rb.push(func(ctx context.Context) error {
		o.log.Info("Rollback: deleting kind cluster", "cluster", cluster.Name)
		return o.deps.LocalProv.Delete(ctx, cluster.Name)
	})
	if cluster.NodeShape != nil && !cluster.NodeShape.Shaped {
		o.log.Warn("OASIS lab created UNSHAPED: worker allocatable does not match the OASIS target; scenarios declaring allocatable_cpu/allocatable_memory will be refused at provision",
			"lab", opts.Lab.Name, "reason", cluster.NodeShape.Reason)
	}
	// Record the cluster now, not only at ACTIVE: the observability
	// templates render the lab's allocated ingress host port into
	// Prometheus's and Grafana's external URLs.
	opts.Lab.Metadata.Clusters = []types.Cluster{localClusterRecord(cluster, opts.OASISMode)}

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

	// ── Step 3: Create git repositories with app/gitops manifests ───────────
	prog.Step("Creating git repositories")
	gitProv := o.resolveLocalGitProvisioner(opts.Lab.ID.String())
	appFiles, gitopsFiles := o.generateLocalManifests(ctx, opts)
	repos, err := o.createLocalGitRepos(ctx, opts, appFiles, gitopsFiles, gitProv, rb)
	if err != nil {
		return fmt.Errorf("creating git repositories: %w", err)
	}
	opts.Lab.Metadata.GitRepos = repos
	if err := o.deps.State.UpdateLab(ctx, opts.Lab); err != nil {
		o.log.Warn("Failed to save git repos to lab metadata", "error", err)
	}

	// ── Step 4: Wait for nodes ready ────────────────────────────────────────
	prog.Step("Waiting for cluster nodes to be ready")
	kctl := o.deps.KubectlFactory(cluster.KubeconfigPath)
	if err := kctl.WaitForNodes(ctx, 5*time.Minute); err != nil {
		return fmt.Errorf("waiting for nodes: %w", err)
	}

	// ── Step 5: Deploy platform components ──────────────────────────────────
	prog.Step("Deploying platform components")
	if err := o.applyPlatformManifests(ctx, opts, kctl); err != nil {
		o.log.Warn("Platform manifest deployment had errors; continuing", "error", err)
	}

	// ── Step 6: Deploy observability stack ──────────────────────────────────
	prog.Step("Deploying observability stack")
	if err := o.applyObservabilityManifests(ctx, opts, kctl); err != nil {
		o.log.Warn("Observability manifest deployment had errors; continuing", "error", err)
	}

	// ── Step 7 (optional): Deploy application manifests ─────────────────────
	if !opts.NoApps {
		prog.Step("Deploying application manifests")
		if err := o.applyAppManifests(ctx, opts, kctl); err != nil {
			// Non-fatal: log and continue so the lab is still marked ACTIVE.
			o.log.Warn("App manifest deployment had errors; lab will still be marked ACTIVE", "error", err)
		}
	}

	// ── Update lab metadata and mark ACTIVE ─────────────────────────────────
	opts.Lab.Metadata.Clusters = []types.Cluster{localClusterRecord(cluster, opts.OASISMode)}
	opts.Lab.Metadata.WorkDir = filepath.Dir(cluster.KubeconfigPath)
	opts.Lab.Status = types.LabStatusActive

	if err := o.deps.State.UpdateLab(ctx, opts.Lab); err != nil {
		return fmt.Errorf("updating lab to ACTIVE: %w", err)
	}

	prog.Done(fmt.Sprintf("Lab %q is ACTIVE", opts.Lab.Name))
	printLocalConnectionInfo(opts.Lab, cluster)
	return nil
}

// localClusterRecord is the lab-state record of a created kind cluster.
func localClusterRecord(cluster *localprov.ClusterInfo, oasisMode bool) types.Cluster {
	return types.Cluster{
		Name:             cluster.Name,
		KubeconfigPath:   cluster.KubeconfigPath,
		NodeCount:        cluster.NodeCount,
		AuditLogPath:     cluster.AuditLogPath,
		OASISMode:        oasisMode,
		NodeShape:        cluster.NodeShape,
		IngressHostPorts: cluster.IngressHostPorts,
	}
}

// resolveLocalGitProvisioner returns a filesystem git provisioner for local labs.
// Local labs always use on-disk repos under the lab's work directory — GitHub
// is only used for cloud labs.
func (o *Orchestrator) resolveLocalGitProvisioner(labID string) GitProvisioner {
	return gitprov.NewLocalFS(o.labWorkDir(labID, "repos"))
}

// generateLocalManifests pre-renders app and gitops manifests so they can be
// committed to git repos and applied to the cluster in a single generation pass.
func (o *Orchestrator) generateLocalManifests(ctx context.Context, opts CreateOptions) (appFiles, gitopsFiles []generators.RenderedFile) {
	tmplCtx := generators.NewTemplateContext(opts.Lab, opts.Company, opts.Spec)

	if o.deps.AppsGen != nil {
		files, err := o.deps.AppsGen.Generate(ctx, tmplCtx)
		if err != nil {
			o.log.Warn("App manifest generation failed for git commit", "error", err)
		} else {
			appFiles = files
		}
	}

	if o.deps.GitOpsGen != nil {
		files, err := o.deps.GitOpsGen.Generate(ctx, tmplCtx)
		if err != nil {
			o.log.Warn("GitOps manifest generation failed for git commit", "error", err)
		} else {
			gitopsFiles = files
		}
	}

	return appFiles, gitopsFiles
}

// createLocalGitRepos creates apps and (optionally) gitops git repositories for a
// local lab and populates them with generated manifests appended after realistic
// commit history.
func (o *Orchestrator) createLocalGitRepos(
	ctx context.Context,
	opts CreateOptions,
	appFiles []generators.RenderedFile,
	gitopsFiles []generators.RenderedFile,
	gitProv GitProvisioner,
	rb *rollback,
) ([]types.GitRepo, error) {
	companyLower := strings.ToLower(opts.Company.Name)

	type repoSpec struct {
		suffix string
		kind   string
		files  []generators.RenderedFile
	}

	specs := []repoSpec{
		{"apps", "apps", appFiles},
	}
	if len(gitopsFiles) > 0 {
		specs = append(specs, repoSpec{"gitops", "gitops", gitopsFiles})
	}

	var gitRepos []types.GitRepo
	for _, rs := range specs {
		repoName := fmt.Sprintf("%s-%s-%s", companyLower, opts.Lab.Name, rs.suffix)

		var commitSpecs []commits.CommitSpec
		if o.deps.CommitsGen != nil {
			generated, err := o.deps.CommitsGen.Generate(ctx, commits.GenerateOptions{
				RepoType: commits.RepoType(rs.kind),
				Company:  opts.Company,
				Level:    opts.Lab.Level,
			})
			if err != nil {
				o.log.Warn("Commit generation failed; using empty history", "error", err, "repo", repoName)
			} else {
				commitSpecs = generated
			}
		}

		// Append a final commit containing the actual rendered manifest files.
		if len(rs.files) > 0 {
			fileMap := make(map[string]string, len(rs.files))
			for _, f := range rs.files {
				fileMap[f.Path] = f.Content
			}
			commitSpecs = append(commitSpecs, commits.CommitSpec{
				Message:   fmt.Sprintf("deploy: add %s manifests", rs.kind),
				Author:    types.Author{Name: "Petri Automation", Email: "petri@internal"},
				Timestamp: time.Now().UTC().Add(-30 * time.Minute),
				Files:     fileMap,
			})
		}

		info, err := gitProv.Create(ctx, gitprov.CreateOptions{
			Name:    repoName,
			Commits: commitSpecs,
		})
		if err != nil {
			return nil, fmt.Errorf("creating %s repo: %w", rs.kind, err)
		}

		capturedName := repoName
		rb.push(func(ctx context.Context) error {
			o.log.Info("Rollback: deleting git repo", "repo", capturedName)
			return gitProv.Delete(ctx, gitprov.DeleteOptions{Name: capturedName})
		})

		gitRepos = append(gitRepos, types.GitRepo{
			Name: repoName,
			URL:  info.CloneURL,
			Type: rs.kind,
		})
	}

	return gitRepos, nil
}

// applyPlatformManifests generates and applies platform component manifests.
func (o *Orchestrator) applyPlatformManifests(ctx context.Context, opts CreateOptions, kctl KubectlClient) error {
	if o.deps.PlatformGen == nil {
		o.log.Warn("Platform generator not configured; skipping platform deployment")
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
		o.log.Warn("Observability generator not configured; skipping observability deployment")
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
		o.log.Warn("Apps generator not configured; skipping manifest deployment")
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
	if cluster.NodeShape != nil {
		fmt.Printf("  Shape:   %s\n", cluster.NodeShape.Summary())
	}
	fmt.Printf("  Access:  export KUBECONFIG=%s\n", cluster.KubeconfigPath)
	if p := cluster.IngressHostPorts; p != nil {
		fmt.Printf("  Ingress: http://localhost:%d  https://localhost:%d\n", p.HTTP, p.HTTPS)
	}
	fmt.Printf("  Expires: %s\n\n", lab.ExpiresAt.Format("2006-01-02 15:04 UTC"))
}

// ── Cloud workflow ─────────────────────────────────────────────────────────────

// createCloud provisions git repositories, runs IaC, and deploys applications
// on a cloud-hosted Kubernetes cluster.
func (o *Orchestrator) createCloud(ctx context.Context, opts CreateOptions, rb *rollback) error {
	if opts.Lab.CloudProvider == types.CloudProviderGCP {
		if opts.GCPProjectID == "" {
			return ErrNoGCPProject
		}
		opts.Lab.Metadata.GCPProjectID = opts.GCPProjectID
	}
	if o.deps.GitProv == nil {
		return fmt.Errorf("git provisioner not configured; provide GITHUB_TOKEN")
	}

	totalSteps := 10
	if opts.NoApps {
		totalSteps = 9
	}
	prog := newProgress(totalSteps, o.log)
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
	// The teardown is registered before the run so a partial apply, or a
	// failure to reach the clusters it created, still rolls back.
	prog.Step("Provisioning infrastructure")
	workDir := o.labWorkDir(opts.Lab.ID.String(), "infra")
	var clusters []provisionedCluster

	switch opts.Company.IaCTool {
	case types.IaCToolTerraform:
		rb.push(func(ctx context.Context) error {
			return o.destroyTerraform(ctx, opts.Lab, opts.CloudEnv, workDir)
		})
		clusters, err = o.runTerraform(ctx, opts, workDir)
	case types.IaCToolPulumi:
		rb.push(func(ctx context.Context) error {
			return o.destroyPulumiStack(ctx, opts, workDir)
		})
		clusters, err = o.runPulumi(ctx, opts, workDir)
	default:
		return fmt.Errorf("unsupported IaC tool: %s", opts.Company.IaCTool)
	}
	if err != nil {
		return err
	}

	// ── Step 4: Persist kubeconfig as encrypted credential ──────────────────
	prog.Step("Storing kubeconfig credential")
	if err := o.storeCloudKubeconfigs(ctx, opts, clusters); err != nil {
		return err
	}

	// ── Step 5: Generate and commit GitOps manifests ─────────────────────────
	prog.Step("Generating GitOps manifests")
	if err := o.generateAndCommitGitOps(ctx, opts); err != nil {
		// Non-fatal for GitOps (cluster already exists).
		o.log.Warn("GitOps manifest generation failed; continuing", "error", err)
	}

	// Steps 6–9 mirror the local path, once per cluster. A failure here is
	// non-fatal: the infrastructure exists and the lab is still marked ACTIVE.
	kctls := make([]KubectlClient, len(opts.Lab.Metadata.Clusters))
	for i, c := range opts.Lab.Metadata.Clusters {
		kctls[i] = o.deps.KubectlFactory(c.KubeconfigPath)
	}

	// ── Step 6: Wait for nodes ───────────────────────────────────────────────
	prog.Step("Waiting for cluster nodes")
	for i, kctl := range kctls {
		if err := kctl.WaitForNodes(ctx, 10*time.Minute); err != nil {
			o.log.Warn("Node wait timed out; lab will still be marked ACTIVE", "error", err, "cluster", opts.Lab.Metadata.Clusters[i].Name)
		}
	}

	// ── Step 7: Deploy platform components ──────────────────────────────────
	prog.Step("Deploying platform components")
	for i, kctl := range kctls {
		if err := o.applyPlatformManifests(ctx, opts, kctl); err != nil {
			o.log.Warn("Platform manifest deployment had errors; continuing", "error", err, "cluster", opts.Lab.Metadata.Clusters[i].Name)
		}
	}

	// ── Step 8: Deploy observability stack ──────────────────────────────────
	prog.Step("Deploying observability stack")
	for i, kctl := range kctls {
		if err := o.applyObservabilityManifests(ctx, opts, kctl); err != nil {
			o.log.Warn("Observability manifest deployment had errors; continuing", "error", err, "cluster", opts.Lab.Metadata.Clusters[i].Name)
		}
	}

	// ── Step 9 (optional): Deploy application manifests ─────────────────────
	if !opts.NoApps {
		prog.Step("Deploying application manifests")
		for i, kctl := range kctls {
			if err := o.applyAppManifests(ctx, opts, kctl); err != nil {
				o.log.Warn("App deployment errors; lab will still be marked ACTIVE", "error", err, "cluster", opts.Lab.Metadata.Clusters[i].Name)
			}
		}
	}

	// ── Step 10: Mark ACTIVE ─────────────────────────────────────────────────
	prog.Step("Finalising lab")
	opts.Lab.Status = types.LabStatusActive
	if err := o.deps.State.UpdateLab(ctx, opts.Lab); err != nil {
		return fmt.Errorf("updating lab to ACTIVE: %w", err)
	}

	prog.Done(fmt.Sprintf("Lab %q is ACTIVE", opts.Lab.Name))
	printCloudConnectionInfo(opts.Lab, repos)
	return nil
}

// storeCloudKubeconfigs stores the clusters' merged kubeconfig as the lab's
// encrypted `kubeconfig` credential, writes one single-context kubeconfig per
// cluster (0600, the same posture as a kind kubeconfig) for kubectl, and
// records the clusters in lab metadata. No clusters is not an error: an IaC
// path that cannot yet reach its clusters leaves the lab without cluster
// access, and the cluster steps that follow find nothing to do.
func (o *Orchestrator) storeCloudKubeconfigs(ctx context.Context, opts CreateOptions, clusters []provisionedCluster) error {
	if len(clusters) == 0 {
		o.log.Warn("No cluster kubeconfig obtained; skipping cluster steps", "provider", opts.Lab.CloudProvider, "iac", opts.Company.IaCTool)
		return nil
	}

	merged, err := mergeKubeconfigs(clusters)
	if err != nil {
		return err
	}
	enc, err := o.deps.Cipher.Encrypt(merged)
	if err != nil {
		return fmt.Errorf("encrypting kubeconfig: %w", err)
	}
	if err := o.deps.State.StoreCredential(ctx, &types.LabCredential{
		ID:             uuid.New(),
		LabID:          opts.Lab.ID,
		CredentialType: "kubeconfig",
		EncryptedValue: enc,
		CreatedAt:      time.Now().UTC(),
	}); err != nil {
		return fmt.Errorf("storing kubeconfig credential: %w", err)
	}

	dir := o.labWorkDir(opts.Lab.ID.String(), "kubeconfigs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating kubeconfig dir: %w", err)
	}
	meta := make([]types.Cluster, 0, len(clusters))
	for _, c := range clusters {
		path := filepath.Join(dir, c.Name)
		if err := os.WriteFile(path, c.Kubeconfig, 0o600); err != nil {
			return fmt.Errorf("writing kubeconfig for cluster %s: %w", c.Name, err)
		}
		meta = append(meta, types.Cluster{
			Name:           c.Name,
			KubeconfigPath: path,
			Endpoint:       c.Endpoint,
			NodeCount:      c.NodeCount,
		})
	}
	opts.Lab.Metadata.Clusters = meta
	if err := o.deps.State.UpdateLab(ctx, opts.Lab); err != nil {
		return fmt.Errorf("saving clusters to lab metadata: %w", err)
	}
	return nil
}

// createGitRepos creates the infra, gitops, and apps repositories on GitHub.
func (o *Orchestrator) createGitRepos(ctx context.Context, opts CreateOptions, companyLower string, rb *rollback) ([]types.GitRepo, error) {
	repoTypes := []struct {
		suffix   string
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
				o.log.Warn("Commit generation failed; using empty history", "error", err, "repo", repoName)
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
			o.log.Info("Rollback: deleting git repo", "repo", repoName)
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
		o.log.Warn("IaC generator not configured; skipping")
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
	o.log.Info("IaC files written", "dir", workDir, "files", len(files))
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
	o.log.Info("GitOps files written", "dir", workDir, "files", len(files))
	return nil
}

// runTerraform runs terraform init+apply and returns the clusters it created
// with their kubeconfigs. It returns no clusters, without error, where the
// provider has no kubeconfig assembly yet.
func (o *Orchestrator) runTerraform(ctx context.Context, opts CreateOptions, workDir string) ([]provisionedCluster, error) {
	if o.deps.TFProv == nil {
		o.log.Warn("Terraform provisioner not configured; skipping IaC provisioning")
		return nil, nil
	}

	if err := o.deps.TFProv.Init(ctx, tfprov.InitOptions{
		WorkDir: workDir,
		Env:     opts.CloudEnv,
	}); err != nil {
		return nil, fmt.Errorf("terraform init: %w", err)
	}

	vars, err := terraformVars(opts.Lab)
	if err != nil {
		return nil, err
	}
	result, err := o.deps.TFProv.Apply(ctx, tfprov.ApplyOptions{
		WorkDir: workDir,
		Vars:    vars,
		Env:     opts.CloudEnv,
	})
	if err != nil {
		return nil, fmt.Errorf("terraform apply: %w", err)
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
			o.log.Warn("Failed to track resource", "error", err, "resource", res.Address)
		}
	}

	var clusters []provisionedCluster
	switch opts.Lab.CloudProvider {
	case types.CloudProviderAWS:
		clusters, err = eksClustersFromOutputs(result.Outputs, opts.Spec.ClusterNames, opts.Spec.NodesPerCluster)
	case types.CloudProviderGCP:
		clusters, err = gkeClustersFromOutputs(result.Outputs, opts.Spec.ClusterNames, opts.Spec.NodesPerCluster)
	default:
		o.log.Warn("No terraform kubeconfig assembly for this provider", "provider", opts.Lab.CloudProvider)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("obtaining cluster kubeconfig: %w", err)
	}
	return clusters, nil
}

// ErrNoGCPProject refuses a GCP lab with no project, before anything is
// provisioned: the GCP template's project_id has no default.
var ErrNoGCPProject = errors.New("GCP labs need a GCP project: set gcp.project_id in ~/.petri/config.yaml or pass petri create --gcp-project")

// terraformVars returns the input variables a lab's terraform apply and
// destroy both pass. The GCP template's project_id has no default, so a GCP
// lab without one is refused rather than left to an interactive prompt.
func terraformVars(lab *types.Lab) (map[string]string, error) {
	if lab.CloudProvider != types.CloudProviderGCP {
		return nil, nil
	}
	if lab.Metadata.GCPProjectID == "" {
		return nil, ErrNoGCPProject
	}
	return map[string]string{"project_id": lab.Metadata.GCPProjectID}, nil
}

// destroyTerraform runs terraform destroy for rollback or teardown, with the
// same input variables the apply passed.
func (o *Orchestrator) destroyTerraform(ctx context.Context, lab *types.Lab, env []string, workDir string) error {
	if o.deps.TFProv == nil {
		return nil
	}
	vars, err := terraformVars(lab)
	if err != nil {
		return err
	}
	return o.deps.TFProv.Destroy(ctx, tfprov.DestroyOptions{
		WorkDir: workDir,
		Vars:    vars,
		Env:     env,
	})
}

// runPulumi runs pulumi login+init+up and returns the clusters it created with
// their kubeconfigs. It returns no clusters, without error, where the provider
// has no kubeconfig assembly.
func (o *Orchestrator) runPulumi(ctx context.Context, opts CreateOptions, workDir string) ([]provisionedCluster, error) {
	if o.deps.PulumiProv == nil {
		o.log.Warn("Pulumi provisioner not configured; skipping IaC provisioning")
		return nil, nil
	}

	stackName := opts.Lab.Name

	if err := o.deps.PulumiProv.Init(ctx, pulumiprov.InitOptions{
		WorkDir:   workDir,
		StackName: stackName,
		Env:       opts.CloudEnv,
	}); err != nil {
		return nil, fmt.Errorf("pulumi init: %w", err)
	}

	result, err := o.deps.PulumiProv.Up(ctx, pulumiprov.UpOptions{
		WorkDir:   workDir,
		StackName: stackName,
		Env:       opts.CloudEnv,
	})
	if err != nil {
		return nil, fmt.Errorf("pulumi up: %w", err)
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
			o.log.Warn("Failed to track resource", "error", err, "resource", res.URN)
		}
	}

	if opts.Lab.CloudProvider != types.CloudProviderAzure {
		o.log.Warn("No pulumi kubeconfig assembly for this provider", "provider", opts.Lab.CloudProvider)
		return nil, nil
	}
	clusters, err := aksClustersFromOutputs(result.Outputs, opts.Spec.ClusterNames, opts.Spec.NodesPerCluster)
	if err != nil {
		return nil, fmt.Errorf("obtaining cluster kubeconfig: %w", err)
	}
	return clusters, nil
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
	for _, c := range lab.Metadata.Clusters {
		fmt.Printf("  Cluster:  %s (export KUBECONFIG=%s)\n", c.Name, c.KubeconfigPath)
	}
	for _, r := range repos {
		fmt.Printf("  %s repo: %s\n", r.Type, r.URL)
	}
	fmt.Printf("  Expires: %s\n\n", lab.ExpiresAt.Format("2006-01-02 15:04 UTC"))
}
