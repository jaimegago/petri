package cli

import (
	"fmt"

	"github.com/spf13/cobra"
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
	if expired {
		fmt.Println("Checking for expired labs (state management not yet implemented).")
		return nil
	}
	fmt.Println("Cleanup requires --expired flag.")
	return nil
}
