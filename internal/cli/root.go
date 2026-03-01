// Package cli implements the Petri command-line interface.
package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/rs/zerolog"
	"github.com/spf13/cobra"

	"github.com/jaimegago/petri/pkg/config"
	"github.com/jaimegago/petri/pkg/crypto"
	"github.com/jaimegago/petri/pkg/logger"
	"github.com/jaimegago/petri/pkg/state"
)

const version = "0.1.0"

// CLI holds injected dependencies shared across all commands.
// All state that would otherwise be package-level globals lives here,
// enabling proper dependency injection and testability.
type CLI struct {
	cfgFile       string
	companiesFile string
	logLevel      string
	cfg           *config.Config
	log           zerolog.Logger
	stateMgr      state.Manager
	cipher        crypto.Cipher
}

// NewCLI creates a CLI with zero-value dependencies.
func NewCLI() *CLI {
	return &CLI{}
}

// Execute is the entry point for the CLI.
func (c *CLI) Execute() {
	if err := c.newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func (c *CLI) newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "petri",
		Short: "Infrastructure lab framework for testing Joe",
		Long: `Petri spawns complete, realistic company infrastructures for testing Joe,
an LLM-based infrastructure copilot. Each lab includes Kubernetes clusters,
applications, IaC repositories with realistic git history, and observability stacks.`,
		Version:           version,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error { return c.initialize(cmd.Name()) },
	}

	cmd.PersistentFlags().StringVar(&c.cfgFile, "config", "", "config file (default ~/.petri/config.yaml)")
	cmd.PersistentFlags().StringVar(&c.companiesFile, "companies", "", "companies YAML file (default configs/companies.yaml)")
	cmd.PersistentFlags().StringVar(&c.logLevel, "log-level", "", "log level: debug, info, warn, error (default: info)")

	cmd.AddCommand(
		c.newInitCmd(),
		c.newCreateCmd(),
		c.newListCmd(),
		c.newInfoCmd(),
		c.newDestroyCmd(),
		c.newHealthCmd(),
		c.newExportCredsCmd(),
		c.newExtendCmd(),
		c.newCleanupCmd(),
	)

	return cmd
}

// initialize loads config and sets up logging. Skipped for the init command
// since config may not exist yet.
func (c *CLI) initialize(cmdName string) error {
	if cmdName == "init" {
		c.log = logger.NewConsole("info", os.Stdout)
		return nil
	}

	var err error
	c.cfg, err = config.Load(c.cfgFile)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if c.logLevel != "" {
		c.cfg.Observability.LogLevel = c.logLevel
	}

	c.log = logger.NewConsole(c.cfg.Observability.LogLevel, os.Stdout)
	return nil
}

// stateManager returns the shared state manager, initialising it on first call.
// Commands that don't need state (health, init, version) never call this.
func (c *CLI) stateManager() (state.Manager, error) {
	if c.stateMgr != nil {
		return c.stateMgr, nil
	}
	if c.cfg == nil {
		return nil, fmt.Errorf("config not loaded; run 'petri init' first")
	}
	mgr, err := state.New(context.Background(), c.cfg.State)
	if err != nil {
		return nil, fmt.Errorf("connecting to state backend (%s): %w", c.cfg.State.Backend, err)
	}
	c.stateMgr = mgr
	return mgr, nil
}

// encryptionCipher returns the shared AES-256-GCM cipher, initialising it on first call.
func (c *CLI) encryptionCipher() (crypto.Cipher, error) {
	if c.cipher != nil {
		return c.cipher, nil
	}
	if c.cfg == nil {
		return nil, fmt.Errorf("config not loaded; run 'petri init' first")
	}
	ciph, err := crypto.NewAESCipher(c.cfg.Credentials.MasterKeyPath)
	if err != nil {
		return nil, fmt.Errorf("loading encryption key: %w", err)
	}
	c.cipher = ciph
	return ciph, nil
}
