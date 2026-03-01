package cli

import (
	"fmt"

	"github.com/spf13/cobra"
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
	fmt.Printf("Lab %q destroy requested (orchestrator not yet implemented).\n", name)
	return nil
}
