package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func (c *CLI) newListCmd() *cobra.Command {
	var (
		filterCompany string
		filterLevel   int
		showExpired   bool
		format        string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List labs",
		RunE: func(_ *cobra.Command, _ []string) error {
			return c.runList(filterCompany, filterLevel, showExpired, format)
		},
	}

	cmd.Flags().StringVar(&filterCompany, "company", "", "Filter by company")
	cmd.Flags().IntVar(&filterLevel, "level", 0, "Filter by level (1-3)")
	cmd.Flags().BoolVar(&showExpired, "expired", false, "Show expired labs")
	cmd.Flags().StringVar(&format, "format", "table", "Output format: table or json")

	return cmd
}

func (c *CLI) runList(_ string, _ int, _ bool, _ string) error {
	fmt.Println("No labs found. State management will be available in Phase 2.")
	fmt.Println("Run 'petri create' to create a lab.")
	return nil
}
