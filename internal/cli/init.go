package cli

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func (c *CLI) newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize Petri (create ~/.petri directory, config, and master key)",
		RunE:  func(_ *cobra.Command, _ []string) error { return c.runInit() },
	}
}

func (c *CLI) runInit() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home directory: %w", err)
	}

	petriDir := filepath.Join(home, ".petri")

	if err := os.MkdirAll(petriDir, 0700); err != nil {
		return fmt.Errorf("creating %s: %w", petriDir, err)
	}
	fmt.Printf("✓ Created %s\n", petriDir)

	cfgPath := filepath.Join(petriDir, "config.yaml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		if err := os.WriteFile(cfgPath, []byte(defaultConfigYAML), 0600); err != nil {
			return fmt.Errorf("writing config: %w", err)
		}
		fmt.Printf("✓ Created %s\n", cfgPath)
	} else {
		fmt.Printf("  Config already exists: %s\n", cfgPath)
	}

	keyPath := filepath.Join(petriDir, "master.key")
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		key := make([]byte, 32) // AES-256
		if _, err := rand.Read(key); err != nil {
			return fmt.Errorf("generating master key: %w", err)
		}
		if err := os.WriteFile(keyPath, key, 0600); err != nil {
			return fmt.Errorf("writing master key: %w", err)
		}
		fmt.Printf("✓ Generated master encryption key: %s\n", keyPath)
	} else {
		fmt.Printf("  Master key already exists: %s\n", keyPath)
	}

	labsDir := filepath.Join(petriDir, "labs")
	if err := os.MkdirAll(labsDir, 0700); err != nil {
		return fmt.Errorf("creating labs dir: %w", err)
	}
	fmt.Printf("✓ Created %s\n", labsDir)

	fmt.Println()
	fmt.Println("Petri initialized successfully.")
	fmt.Printf("Edit %s to configure your state backend and credentials.\n", cfgPath)
	fmt.Println("Run 'petri health' to verify your setup.")

	return nil
}

const defaultConfigYAML = `state:
  backend: postgresql
  connection_string: "postgres://localhost/petri?sslmode=disable"

credentials:
  master_key_path: ~/.petri/master.key

observability:
  metrics_enabled: true
  metrics_port: 9090
  tracing_enabled: false
  log_level: info

git:
  default_provider: github

cloud:
  terraform_version: 1.7.0
  pulumi_version: 3.100.0

cleanup:
  check_interval: 5m
  grace_period: 30m
`
