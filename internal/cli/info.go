package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func (c *CLI) newInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info <lab-name>",
		Short: "Show detailed information about a lab",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return c.runInfo(args[0])
		},
	}
}

func (c *CLI) runInfo(name string) error {
	fmt.Printf("Lab %q not found. State management will be available in Phase 2.\n", name)
	return nil
}
