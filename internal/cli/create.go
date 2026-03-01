package cli

import (
	"context"
	"crypto/rand"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/jaimegago/petri/pkg/config"
	"github.com/jaimegago/petri/pkg/types"
)

type createOptions struct {
	company string
	level   int
	name    string
	local   bool
	cloud   string
	ttl     string
	noApps  bool
	dryRun  bool
}

func (c *CLI) newCreateCmd() *cobra.Command {
	opts := &createOptions{}

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new infrastructure lab",
		Long: `Create a complete, ephemeral infrastructure lab for the specified company and complexity level.

Examples:
  petri create --company=acme --level=1 --local --name=my-lab
  petri create --company=acme --level=2 --name=aws-lab --ttl=4h
  petri create --company=techflow --level=3 --name=azure-lab`,
		RunE: func(_ *cobra.Command, _ []string) error {
			return c.runCreate(opts)
		},
	}

	cmd.Flags().StringVar(&opts.company, "company", "", "Company profile (acme, techflow, cloudnative) [required]")
	cmd.Flags().IntVar(&opts.level, "level", 0, "Complexity level 1-3 [required]")
	cmd.Flags().StringVar(&opts.name, "name", "", "Lab name (auto-generated if not set)")
	cmd.Flags().BoolVar(&opts.local, "local", false, "Use local kind/k3s instead of cloud")
	cmd.Flags().StringVar(&opts.cloud, "cloud", "", "Cloud provider override (aws, azure, gcp)")
	cmd.Flags().StringVar(&opts.ttl, "ttl", "", "Time-to-live (e.g. 4h, 30m; default: level-specific)")
	cmd.Flags().BoolVar(&opts.noApps, "no-apps", false, "Skip application deployment (platform only)")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "Print what would be created without creating it")

	_ = cmd.MarkFlagRequired("company")
	_ = cmd.MarkFlagRequired("level")

	return cmd
}

func (c *CLI) runCreate(opts *createOptions) error {
	if opts.level < 1 || opts.level > 3 {
		return fmt.Errorf("level must be 1, 2, or 3 (got %d)", opts.level)
	}

	companiesPath := c.resolveCompaniesFile()
	companies, err := config.LoadCompanies(companiesPath)
	if err != nil {
		return fmt.Errorf("loading companies: %w", err)
	}

	var company *types.Company
	for i := range companies {
		if strings.EqualFold(companies[i].Name, opts.company) {
			company = &companies[i]
			break
		}
	}
	if company == nil {
		names := make([]string, len(companies))
		for i, co := range companies {
			names[i] = co.Name
		}
		return fmt.Errorf("unknown company %q (available: %s)", opts.company, strings.Join(names, ", "))
	}

	spec, err := company.GetLevel(opts.level)
	if err != nil {
		return fmt.Errorf("validating level: %w", err)
	}

	provider := company.CloudProvider
	if opts.local {
		provider = types.CloudProviderLocal
	} else if opts.cloud != "" {
		provider = types.CloudProvider(opts.cloud)
	}

	name := opts.name
	if name == "" {
		name = fmt.Sprintf("%s-l%d-%s", strings.ToLower(opts.company), opts.level, randomSuffix(6))
	}

	if opts.dryRun {
		printDryRun(name, company, opts.level, provider, spec, opts)
		return nil
	}

	// Parse optional TTL override.
	ttlHours := spec.TTLDefaultHours
	if opts.ttl != "" {
		d, parseErr := time.ParseDuration(opts.ttl)
		if parseErr != nil {
			return fmt.Errorf("parsing --ttl %q: %w", opts.ttl, parseErr)
		}
		ttlHours = int(d.Hours())
		if ttlHours < 1 {
			ttlHours = 1
		}
	}

	now := time.Now().UTC()
	lab := &types.Lab{
		ID:            uuid.New(),
		Name:          name,
		Company:       strings.ToLower(opts.company),
		Level:         opts.level,
		CloudProvider: provider,
		Status:        types.LabStatusCreating,
		CreatedAt:     now,
		TTLHours:      ttlHours,
		ExpiresAt:     now.Add(time.Duration(ttlHours) * time.Hour),
	}

	mgr, err := c.stateManager()
	if err != nil {
		return err
	}

	ctx := context.Background()

	// Guard against duplicate lab names.
	if existing, _ := mgr.GetLabByName(ctx, name); existing != nil {
		return fmt.Errorf("lab %q already exists (status: %s)", name, existing.Status)
	}

	if err := mgr.CreateLab(ctx, lab); err != nil {
		return fmt.Errorf("recording lab in state: %w", err)
	}

	c.log.Info().
		Str("lab_id", lab.ID.String()).
		Str("name", name).
		Str("company", lab.Company).
		Int("level", opts.level).
		Str("cloud_provider", string(provider)).
		Msg("Lab record created in state")

	// Mark ACTIVE immediately; actual provisioning will happen in Phase 8.
	lab.Status = types.LabStatusActive
	if err := mgr.UpdateLab(ctx, lab); err != nil {
		return fmt.Errorf("updating lab status: %w", err)
	}

	fmt.Printf("Lab created:\n")
	fmt.Printf("  Name:     %s\n", lab.Name)
	fmt.Printf("  ID:       %s\n", lab.ID)
	fmt.Printf("  Company:  %s (%s)\n", company.Name, company.Description)
	fmt.Printf("  Level:    %d\n", opts.level)
	fmt.Printf("  Provider: %s\n", provider)
	fmt.Printf("  Status:   %s\n", lab.Status)
	fmt.Printf("  Expires:  %s\n", lab.ExpiresAt.Format(time.RFC3339))
	fmt.Println()
	fmt.Println("Note: Provisioning (clusters, apps, IaC) will be available in Phase 8.")
	fmt.Printf("Run 'petri info %s' to view lab details.\n", name)

	return nil
}

func printDryRun(name string, company *types.Company, level int, provider types.CloudProvider, spec types.LevelSpec, opts *createOptions) {
	fmt.Println("=== DRY RUN ===")
	fmt.Printf("Lab Name:      %s\n", name)
	fmt.Printf("Company:       %s (%s)\n", company.Name, company.Description)
	fmt.Printf("Level:         %d\n", level)
	fmt.Printf("Cloud:         %s\n", provider)
	fmt.Printf("IaC Tool:      %s\n", company.IaCTool)
	fmt.Printf("GitOps:        %s\n", company.GitOpsTool)
	fmt.Printf("Clusters:      %d\n", spec.Clusters)
	fmt.Printf("Apps:          %s\n", strings.Join(spec.Apps, ", "))
	fmt.Printf("Platform:      %s\n", strings.Join(spec.Platform, ", "))
	fmt.Printf("Observability: %s\n", strings.Join(spec.Observability, ", "))
	if len(spec.Namespaces) > 0 {
		fmt.Printf("Namespaces:    %s\n", strings.Join(spec.Namespaces, ", "))
	}
	if opts.ttl != "" {
		fmt.Printf("TTL:           %s\n", opts.ttl)
	} else {
		fmt.Printf("TTL:           %dh (default)\n", spec.TTLDefaultHours)
	}
	fmt.Println()
	fmt.Printf("Git repos that would be created:\n")
	fmt.Printf("  %s-%s-infra\n", strings.ToLower(company.Name), name)
	fmt.Printf("  %s-%s-gitops\n", strings.ToLower(company.Name), name)
	fmt.Printf("  %s-%s-apps\n", strings.ToLower(company.Name), name)
}

func (c *CLI) resolveCompaniesFile() string {
	if c.companiesFile != "" {
		return c.companiesFile
	}
	return filepath.Join("configs", "companies.yaml")
}

func randomSuffix(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = chars[b[i]%byte(len(chars))]
	}
	return string(b)
}
