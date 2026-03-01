package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

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

func (c *CLI) runExtend(name, ttlStr string) error {
	// Strip leading '+' if present.
	durStr := strings.TrimPrefix(ttlStr, "+")
	d, err := time.ParseDuration(durStr)
	if err != nil {
		return fmt.Errorf("parsing --ttl %q: %w", ttlStr, err)
	}
	if d <= 0 {
		return fmt.Errorf("--ttl must be a positive duration, got %s", ttlStr)
	}

	mgr, err := c.stateManager()
	if err != nil {
		return err
	}

	ctx := context.Background()

	lab, err := mgr.GetLabByName(ctx, name)
	if err != nil {
		return fmt.Errorf("lab %q not found", name)
	}

	oldExpiry := lab.ExpiresAt
	lab.ExpiresAt = lab.ExpiresAt.Add(d)
	lab.TTLHours += int(d.Hours())

	if err := mgr.UpdateLab(ctx, lab); err != nil {
		return fmt.Errorf("updating lab TTL: %w", err)
	}

	c.log.Info().
		Str("name", name).
		Str("old_expiry", oldExpiry.Format(time.RFC3339)).
		Str("new_expiry", lab.ExpiresAt.Format(time.RFC3339)).
		Msg("Lab TTL extended")

	fmt.Printf("Lab %q extended by %s.\n", name, d)
	fmt.Printf("  Old expiry: %s\n", oldExpiry.Format(time.RFC3339))
	fmt.Printf("  New expiry: %s\n", lab.ExpiresAt.Format(time.RFC3339))

	return nil
}
