package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	gitprov "github.com/jaimegago/petri/pkg/provisioners/git"
	pulumiprov "github.com/jaimegago/petri/pkg/provisioners/pulumi"
	tfprov "github.com/jaimegago/petri/pkg/provisioners/terraform"
	"github.com/jaimegago/petri/pkg/types"
)

// DestroyOptions parameterises a lab destruction workflow.
type DestroyOptions struct {
	// Lab is the lab to destroy (must be in DESTROYING status).
	Lab *types.Lab
	// Company is the resolved company profile.
	Company *types.Company
	// Spec is the level specification for the lab.
	Spec types.LevelSpec
	// GitHubToken is the PAT used to delete GitHub repositories.
	GitHubToken string
	// CloudEnv carries cloud credential environment variables for IaC teardown.
	CloudEnv []string
	// Force continues teardown even if individual steps fail.
	Force bool
}

// Destroy tears down all resources associated with a lab and marks it DESTROYED.
func (o *Orchestrator) Destroy(ctx context.Context, opts DestroyOptions) error {
	lab := opts.Lab
	start := time.Now()

	var errs []string
	collectErr := func(step string, err error) {
		if err == nil {
			return
		}
		errs = append(errs, fmt.Sprintf("%s: %v", step, err))
		o.log.Error().Err(err).Str("step", step).Str("lab", lab.Name).Msg("Destroy step failed")
	}

	switch lab.CloudProvider {
	case types.CloudProviderLocal:
		o.destroyLocal(ctx, opts, collectErr)
	case types.CloudProviderAWS, types.CloudProviderGCP, types.CloudProviderAzure:
		o.destroyCloud(ctx, opts, collectErr)
	default:
		collectErr("provider", fmt.Errorf("unsupported cloud provider: %s", lab.CloudProvider))
	}

	// Always clean up state records.
	if err := o.deps.State.DeleteResources(ctx, lab.ID); err != nil {
		o.log.Warn().Err(err).Msg("Failed to delete resource records")
	}
	if err := o.deps.State.DeleteCredentials(ctx, lab.ID); err != nil {
		o.log.Warn().Err(err).Msg("Failed to delete credential records")
	}

	// Remove lab working directory (best-effort).
	o.removeLabWorkDir(lab.ID.String())

	// In non-force mode, report errors without marking DESTROYED.
	if len(errs) > 0 && !opts.Force {
		lab.Status = types.LabStatusError
		lab.Metadata.ErrorMessage = strings.Join(errs, "; ")
		_ = o.deps.State.UpdateLab(ctx, lab)
		return fmt.Errorf("destroy failed: %s", strings.Join(errs, "; "))
	}

	lab.Status = types.LabStatusDestroyed
	if err := o.deps.State.UpdateLab(ctx, lab); err != nil {
		return fmt.Errorf("marking lab destroyed: %w", err)
	}

	if o.deps.Metrics != nil && opts.Company != nil {
		provider := string(lab.CloudProvider)
		o.deps.Metrics.LabDestroyed(opts.Company.Name, lab.Level, provider, "manual")
		o.deps.Metrics.ObserveDestroy(opts.Company.Name, lab.Level, provider, time.Since(start))
	}

	fmt.Printf("Lab %q destroyed.\n", lab.Name)
	return nil
}

// destroyLocal deletes the kind cluster for a local lab.
func (o *Orchestrator) destroyLocal(ctx context.Context, opts DestroyOptions, collectErr func(string, error)) {
	lab := opts.Lab
	if o.deps.LocalProv == nil {
		o.log.Warn().Msg("Local provisioner not configured; skipping cluster deletion")
		return
	}

	clusterName := lab.Name
	if len(lab.Metadata.Clusters) > 0 {
		clusterName = lab.Metadata.Clusters[0].Name
	}

	collectErr("delete kind cluster", o.deps.LocalProv.Delete(ctx, clusterName))
}

// destroyCloud tears down cloud infrastructure and deletes git repositories.
func (o *Orchestrator) destroyCloud(ctx context.Context, opts DestroyOptions, collectErr func(string, error)) {
	lab := opts.Lab
	workDir := o.labWorkDir(lab.ID.String(), "infra")

	switch opts.Company.IaCTool {
	case types.IaCToolTerraform:
		if o.deps.TFProv != nil {
			collectErr("terraform destroy", o.deps.TFProv.Destroy(ctx, tfprov.DestroyOptions{
				WorkDir: workDir,
				Env:     opts.CloudEnv,
			}))
		}
	case types.IaCToolPulumi:
		if o.deps.PulumiProv != nil {
			stackName := lab.Name
			if err := o.deps.PulumiProv.Destroy(ctx, pulumiprov.DestroyOptions{
				WorkDir:   workDir,
				StackName: stackName,
				Env:       opts.CloudEnv,
			}); err != nil {
				collectErr("pulumi destroy", err)
			} else {
				collectErr("pulumi stack rm", o.deps.PulumiProv.StackRemove(ctx, pulumiprov.StackRemoveOptions{
					WorkDir:   workDir,
					StackName: stackName,
					Force:     true,
				}))
			}
		}
	}

	// Delete git repositories.
	if o.deps.GitProv == nil {
		return
	}
	owner := opts.Company.GitHubOrg
	for _, repo := range lab.Metadata.GitRepos {
		collectErr(
			fmt.Sprintf("delete repo %s", repo.Name),
			o.deps.GitProv.Delete(ctx, gitprov.DeleteOptions{Owner: owner, Name: repo.Name}),
		)
	}
}

// removeLabWorkDir removes the lab's working directory tree (best-effort).
func (o *Orchestrator) removeLabWorkDir(labID string) {
	base := o.cfg.WorkDir
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".petri", "labs")
	}
	labDir := filepath.Join(base, labID)
	if err := os.RemoveAll(labDir); err != nil {
		o.log.Warn().Err(err).Str("dir", labDir).Msg("Failed to remove lab work dir")
	}
}
