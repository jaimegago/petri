package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/jaimegago/petri/pkg/types"
)

func (c *CLI) newDestroyCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "destroy <lab-name>",
		Short: "Destroy a lab and all its resources",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return c.runDestroy(args[0], force)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Force destroy even if some cleanup steps fail")

	return cmd
}

func (c *CLI) runDestroy(name string, _ bool) error {
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

	c.log.Info().
		Str("lab_id", lab.ID.String()).
		Str("name", name).
		Msg("Lab marked for destruction")

	// Full infrastructure teardown happens in Phase 8 (orchestrator).
	// Remove tracked records and mark destroyed.
	if err := mgr.DeleteResources(ctx, lab.ID); err != nil {
		c.log.Warn().Err(err).Str("name", name).Msg("Failed to delete resource records")
	}
	if err := mgr.DeleteCredentials(ctx, lab.ID); err != nil {
		c.log.Warn().Err(err).Str("name", name).Msg("Failed to delete credential records")
	}

	lab.Status = types.LabStatusDestroyed
	if err := mgr.UpdateLab(ctx, lab); err != nil {
		return fmt.Errorf("marking lab destroyed: %w", err)
	}

	fmt.Printf("Lab %q destroyed.\n", name)
	fmt.Println("Note: Actual infrastructure teardown (clusters, repos, IaC) will be available in Phase 8.")

	return nil
}
