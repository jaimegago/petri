package cli

import (
	"context"
	"fmt"
	"time"

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
	mgr, err := c.stateManager()
	if err != nil {
		return err
	}

	lab, err := mgr.GetLabByName(context.Background(), name)
	if err != nil {
		return fmt.Errorf("lab %q not found", name)
	}

	resources, err := mgr.ListResources(context.Background(), lab.ID)
	if err != nil {
		return fmt.Errorf("listing resources: %w", err)
	}

	fmt.Printf("Lab: %s\n", lab.Name)
	fmt.Printf("  ID:       %s\n", lab.ID)
	fmt.Printf("  Company:  %s\n", lab.Company)
	fmt.Printf("  Level:    %d\n", lab.Level)
	fmt.Printf("  Provider: %s\n", lab.CloudProvider)
	fmt.Printf("  Status:   %s\n", lab.Status)
	fmt.Printf("  Created:  %s\n", lab.CreatedAt.Format(time.RFC3339))
	fmt.Printf("  Expires:  %s", lab.ExpiresAt.Format(time.RFC3339))
	if lab.IsExpired() {
		fmt.Print(" (EXPIRED)")
	}
	fmt.Println()

	if lab.Metadata.WorkDir != "" {
		fmt.Printf("  Work Dir: %s\n", lab.Metadata.WorkDir)
	}
	if lab.Metadata.ErrorMessage != "" {
		fmt.Printf("  Error:    %s\n", lab.Metadata.ErrorMessage)
	}

	if len(lab.Metadata.GitRepos) > 0 {
		fmt.Println("  Git Repos:")
		for _, repo := range lab.Metadata.GitRepos {
			fmt.Printf("    [%s] %s — %s\n", repo.Type, repo.Name, repo.URL)
		}
	}

	if len(lab.Metadata.Clusters) > 0 {
		fmt.Println("  Clusters:")
		for _, cl := range lab.Metadata.Clusters {
			fmt.Printf("    %s (%d nodes)", cl.Name, cl.NodeCount)
			if cl.Endpoint != "" {
				fmt.Printf(" — %s", cl.Endpoint)
			}
			fmt.Println()
		}
	}

	if len(lab.Metadata.ObservabilityURLs) > 0 {
		fmt.Println("  Observability:")
		for tool, url := range lab.Metadata.ObservabilityURLs {
			fmt.Printf("    %s: %s\n", tool, url)
		}
	}

	if len(resources) > 0 {
		fmt.Printf("  Resources: %d tracked\n", len(resources))
		for _, r := range resources {
			fmt.Printf("    [%s] %s", r.ResourceType, r.ResourceID)
			if r.CloudResourceID != "" {
				fmt.Printf(" (%s)", r.CloudResourceID)
			}
			fmt.Println()
		}
	}

	return nil
}
