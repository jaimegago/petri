package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jaimegago/petri/pkg/orchestrator"
	"github.com/jaimegago/petri/pkg/types"
)

func (c *CLI) newCleanupCmd() *cobra.Command {
	var expired bool

	cmd := &cobra.Command{
		Use:   "cleanup",
		Short: "Clean up expired or orphaned labs",
		RunE: func(_ *cobra.Command, _ []string) error {
			return c.runCleanup(expired)
		},
	}

	cmd.Flags().BoolVar(&expired, "expired", false, "Destroy all labs past their TTL")

	return cmd
}

func (c *CLI) runCleanup(expired bool) error {
	if !expired {
		fmt.Println("Specify --expired to destroy all labs past their TTL.")
		return nil
	}

	mgr, err := c.stateManager()
	if err != nil {
		return err
	}

	ctx := context.Background()
	gracePeriod := c.cfg.Cleanup.GracePeriod

	labs, err := mgr.FindExpiredLabs(ctx, gracePeriod)
	if err != nil {
		return fmt.Errorf("finding expired labs: %w", err)
	}

	if len(labs) == 0 {
		fmt.Println("No expired labs found.")
		return nil
	}

	fmt.Printf("Found %d expired lab(s):\n", len(labs))
	for _, lab := range labs {
		fmt.Printf("  %s (expired %s)\n", lab.Name, lab.ExpiresAt.Format("2006-01-02 15:04:05 UTC"))
	}
	fmt.Println()

	orch, err := c.buildOrchestrator(githubToken())
	if err != nil {
		return fmt.Errorf("initializing orchestrator: %w", err)
	}

	var destroyed, failed int
	for _, lab := range labs {
		if !lab.CanTransitionTo(types.LabStatusDestroying) {
			c.log.Warn("Skipping lab; cannot transition to DESTROYING", "name", lab.Name, "status", string(lab.Status))
			continue
		}

		lab.Status = types.LabStatusDestroying
		if err := mgr.UpdateLab(ctx, lab); err != nil {
			c.log.Warn("Failed to mark lab as DESTROYING", "error", err, "name", lab.Name)
			failed++
			continue
		}

		// Resolve company (best-effort).
		company, spec, _ := c.resolveCompanyForLab(lab)
		destroyOpts := orchestrator.DestroyOptions{
			Lab:   lab,
			Force: true, // auto-cleanup always uses force
		}
		if company != nil {
			destroyOpts.Company = company
			destroyOpts.Spec = spec
		} else {
			destroyOpts.Company = &types.Company{
				Name:          lab.Company,
				CloudProvider: lab.CloudProvider,
				GitHubOrg:     extractGitHubOrgFromRepos(lab),
			}
		}

		if err := orch.Destroy(ctx, destroyOpts); err != nil {
			c.log.Warn("Cleanup: destroy failed", "error", err, "name", lab.Name)
			failed++
			continue
		}

		c.log.Info("Expired lab destroyed", "name", lab.Name)
		destroyed++
	}

	fmt.Printf("Cleanup complete: %d destroyed", destroyed)
	if failed > 0 {
		fmt.Printf(", %d failed", failed)
	}
	fmt.Println()
	return nil
}
