package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

func (c *CLI) newHealthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Check Petri dependencies and connectivity",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return c.runHealth()
		},
	}
}

func (c *CLI) runHealth() error {
	allOK := true

	fmt.Println("Petri Health Check")
	fmt.Println("==================")

	binaries := []struct {
		name     string
		required bool
	}{
		{"kubectl", false},
		{"kind", false},
		{"docker", false},
		{"terraform", false},
		{"git", true},
	}

	fmt.Println("\nBinaries:")
	for _, b := range binaries {
		path, err := exec.LookPath(b.name)
		if err != nil {
			if b.required {
				fmt.Printf("  ✗ %-12s NOT FOUND (required)\n", b.name)
				allOK = false
			} else {
				fmt.Printf("  - %-12s not found (optional)\n", b.name)
			}
		} else {
			fmt.Printf("  ✓ %-12s %s\n", b.name, path)
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home directory: %w", err)
	}

	petriDir := filepath.Join(home, ".petri")

	fmt.Println("\nPetri Setup:")
	if _, err := os.Stat(petriDir); err == nil {
		fmt.Printf("  ✓ %-20s exists\n", "~/.petri")
	} else {
		fmt.Printf("  ✗ %-20s missing (run: petri init)\n", "~/.petri")
		allOK = false
	}

	masterKey := filepath.Join(petriDir, "master.key")
	if fi, err := os.Stat(masterKey); err == nil {
		if fi.Mode().Perm() == 0600 {
			fmt.Printf("  ✓ %-20s exists (permissions OK)\n", "master.key")
		} else {
			fmt.Printf("  ! %-20s exists but permissions are %v (should be 0600)\n", "master.key", fi.Mode().Perm())
		}
	} else {
		fmt.Printf("  ✗ %-20s missing (run: petri init)\n", "master.key")
		allOK = false
	}

	configPath := filepath.Join(petriDir, "config.yaml")
	if _, err := os.Stat(configPath); err == nil {
		fmt.Printf("  ✓ %-20s loaded\n", "config.yaml")
	} else {
		fmt.Printf("  - %-20s missing (using defaults)\n", "config.yaml")
	}

	fmt.Println()
	if allOK {
		fmt.Println("Status: OK")
	} else {
		fmt.Println("Status: DEGRADED (see issues above)")
	}

	return nil
}
