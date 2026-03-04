package cli

import (
	"context"
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

func (c *CLI) runExportCreds(name, outputPath string) error {
	mgr, err := c.stateManager()
	if err != nil {
		return err
	}

	ctx := context.Background()
	lab, err := mgr.GetLabByName(ctx, name)
	if err != nil {
		return fmt.Errorf("lab %q not found", name)
	}

	orch, err := c.buildOrchestrator("")
	if err != nil {
		return fmt.Errorf("initializing orchestrator: %w", err)
	}

	return orch.ExportCredentials(ctx, lab, outputPath)
}
