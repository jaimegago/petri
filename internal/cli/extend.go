package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func (c *CLI) newExtendCmd() *cobra.Command {
	var ttl string

	cmd := &cobra.Command{
		Use:   "extend <lab-name>",
		Short: "Extend the TTL of a lab",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return c.runExtend(args[0], ttl)
		},
	}

	cmd.Flags().StringVar(&ttl, "ttl", "+1h", "Duration to extend (e.g. +2h, +30m)")
	_ = cmd.MarkFlagRequired("ttl")

	return cmd
}

func (c *CLI) runExtend(name, ttl string) error {
	fmt.Printf("Extending lab %q by %s (state management not yet implemented).\n", name, ttl)
	return nil
}
