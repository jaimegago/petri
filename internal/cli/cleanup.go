package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

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

	var destroyed, failed int
	for _, lab := range labs {
		lab.Status = types.LabStatusDestroying
		if err := mgr.UpdateLab(ctx, lab); err != nil {
			c.log.Warn().Err(err).Str("name", lab.Name).Msg("Failed to mark lab as destroying")
			failed++
			continue
		}

		_ = mgr.DeleteResources(ctx, lab.ID)
		_ = mgr.DeleteCredentials(ctx, lab.ID)

		lab.Status = types.LabStatusDestroyed
		if err := mgr.UpdateLab(ctx, lab); err != nil {
			c.log.Warn().Err(err).Str("name", lab.Name).Msg("Failed to mark lab as destroyed")
			failed++
			continue
		}

		c.log.Info().Str("name", lab.Name).Msg("Expired lab destroyed")
		destroyed++
	}

	fmt.Printf("Cleanup complete: %d destroyed", destroyed)
	if failed > 0 {
		fmt.Printf(", %d failed", failed)
	}
	fmt.Println()
	fmt.Println("Note: Actual infrastructure teardown will be available in Phase 8.")

	return nil
}
