package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func (c *CLI) newExportCredsCmd() *cobra.Command {
	var output string

	cmd := &cobra.Command{
		Use:   "export-creds <lab-name>",
		Short: "Export encrypted credentials bundle for Joe",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return c.runExportCreds(args[0], output)
		},
	}

	cmd.Flags().StringVar(&output, "output", "joe-bundle.enc", "Output file path for encrypted bundle")

	return cmd
}

func (c *CLI) runExportCreds(name, _ string) error {
	fmt.Printf("Credential export for lab %q will be available in Phase 2 (state management).\n", name)
	return nil
}
