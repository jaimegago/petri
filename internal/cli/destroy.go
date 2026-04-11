package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jaimegago/petri/pkg/config"
	"github.com/jaimegago/petri/pkg/orchestrator"
	"github.com/jaimegago/petri/pkg/types"
)

func (c *CLI) newDestroyCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "destroy <lab-name>",
		Short: "Destroy a lab and all its resources",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return c.runDestroy(args[0], force)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Force destroy even if some cleanup steps fail")

	return cmd
}

func (c *CLI) runDestroy(name string, force bool) error {
	mgr, err := c.stateManager()
	if err != nil {
		return err
	}

	ctx := context.Background()

	lab, err := mgr.GetLabByName(ctx, name)
	if err != nil {
		return fmt.Errorf("lab %q not found", name)
	}

	if !lab.CanTransitionTo(types.LabStatusDestroying) {
		return fmt.Errorf("cannot destroy lab in status %q", lab.Status)
	}

	lab.Status = types.LabStatusDestroying
	if err := mgr.UpdateLab(ctx, lab); err != nil {
		return fmt.Errorf("updating lab status: %w", err)
	}

	c.log.Info("Starting lab destruction", "lab_id", lab.ID.String(), "name", name)

	// Resolve company for cloud teardown (best-effort; non-fatal if missing).
	company, spec, companyErr := c.resolveCompanyForLab(lab)
	if companyErr != nil {
		c.log.Warn("Could not resolve company profile; skipping IaC teardown", "error", companyErr)
	}

	// Resolve GitHub token for cloud labs.
	token := ""
	if lab.CloudProvider != types.CloudProviderLocal {
		token = githubToken()
	}

	orch, orchErr := c.buildOrchestrator(token)
	if orchErr != nil {
		return fmt.Errorf("initializing orchestrator: %w", orchErr)
	}

	destroyOpts := orchestrator.DestroyOptions{
		Lab:   lab,
		Force: force,
	}
	if company != nil {
		destroyOpts.Company = company
		destroyOpts.Spec = spec
	} else {
		// Minimal stub so the orchestrator can still clean up metadata-tracked resources.
		destroyOpts.Company = &types.Company{
			Name:          lab.Company,
			CloudProvider: lab.CloudProvider,
			GitHubOrg:     extractGitHubOrgFromRepos(lab),
		}
	}

	return orch.Destroy(ctx, destroyOpts)
}

// resolveCompanyForLab loads the company profile for a lab from the companies YAML.
func (c *CLI) resolveCompanyForLab(lab *types.Lab) (*types.Company, types.LevelSpec, error) {
	companies, err := config.LoadCompanies(c.resolveCompaniesFile())
	if err != nil {
		return nil, types.LevelSpec{}, fmt.Errorf("loading companies: %w", err)
	}
	for i := range companies {
		if strings.EqualFold(companies[i].Name, lab.Company) {
			spec, specErr := companies[i].GetLevel(lab.Level)
			if specErr != nil {
				return &companies[i], types.LevelSpec{}, specErr
			}
			return &companies[i], spec, nil
		}
	}
	return nil, types.LevelSpec{}, fmt.Errorf("company %q not found", lab.Company)
}

// extractGitHubOrgFromRepos extracts the GitHub org from lab git repo URLs.
func extractGitHubOrgFromRepos(lab *types.Lab) string {
	for _, repo := range lab.Metadata.GitRepos {
		const prefix = "https://github.com/"
		if len(repo.URL) > len(prefix) {
			rest := repo.URL[len(prefix):]
			for i, ch := range rest {
				if ch == '/' {
					return rest[:i]
				}
			}
		}
	}
	return ""
}
