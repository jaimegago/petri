package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
)

func (c *CLI) newListCmd() *cobra.Command {
	var (
		filterCompany string
		filterLevel   int
		filterStatus  string
		aliveOnly     bool
		format        string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List labs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return c.runList(filterCompany, filterLevel, filterStatus, aliveOnly, format)
		},
	}

	cmd.Flags().StringVar(&filterCompany, "company", "", "Filter by company")
	cmd.Flags().IntVar(&filterLevel, "level", 0, "Filter by level (1-3)")
	cmd.Flags().StringVar(&filterStatus, "status", "", "Filter by status (CREATING, ACTIVE, EXPIRED, DESTROYING, DESTROYED, ERROR)")
	cmd.Flags().BoolVar(&aliveOnly, "alive", false, "Show only non-expired labs")
	cmd.Flags().StringVar(&format, "format", "table", "Output format: table or json")

	return cmd
}

func (c *CLI) runList(company string, level int, status string, aliveOnly bool, format string) error {
	mgr, err := c.stateManager()
	if err != nil {
		return err
	}

	filter := state.ListFilter{
		Company:        company,
		Level:          level,
		Status:         types.LabStatus(status),
		IncludeExpired: !aliveOnly,
	}

	ctx := context.Background()
	labs, err := mgr.ListLabs(ctx, filter)
	if err != nil {
		return fmt.Errorf("listing labs: %w", err)
	}

	for _, lab := range labs {
		if _, terr := state.TransitionIfExpired(ctx, mgr, lab); terr != nil {
			c.log.Warn("failed to lazily transition lab to EXPIRED", "lab", lab.Name, "error", terr)
		}
	}

	if len(labs) == 0 {
		fmt.Println("No labs found.")
		fmt.Println("Run 'petri create' to create a lab.")
		return nil
	}

	switch format {
	case "json":
		return printLabsJSON(labs)
	default:
		printLabsTable(labs)
		return nil
	}
}

func printLabsTable(labs []*types.Lab) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tCOMPANY\tLEVEL\tPROVIDER\tSTATUS\tEXPIRES")
	for _, lab := range labs {
		expires := lab.ExpiresAt.Format(time.RFC3339)
		if lab.IsExpired() {
			expires += " (expired)"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\n",
			lab.Name, lab.Company, lab.Level,
			lab.CloudProvider, lab.Status, expires,
		)
	}
	_ = w.Flush()
}

func printLabsJSON(labs []*types.Lab) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(labs); err != nil {
		return fmt.Errorf("encoding labs as JSON: %w", err)
	}
	return nil
}
