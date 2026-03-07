// Package cli implements the Petri command-line interface.
package cli

import (
	"context"
	"fmt"
	"os"

	"log/slog"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/cobra"

	"github.com/jaimegago/petri/pkg/config"
	"github.com/jaimegago/petri/pkg/crypto"
	appsgen "github.com/jaimegago/petri/pkg/generators/apps"
	commitsgen "github.com/jaimegago/petri/pkg/generators/commits"
	gitopsgen "github.com/jaimegago/petri/pkg/generators/gitops"
	iacgen "github.com/jaimegago/petri/pkg/generators/iac"
	obsgen "github.com/jaimegago/petri/pkg/generators/observability"
	platformgen "github.com/jaimegago/petri/pkg/generators/platform"
	"github.com/jaimegago/petri/pkg/logger"
	"github.com/jaimegago/petri/pkg/metrics"
	"github.com/jaimegago/petri/pkg/orchestrator"
	gitprov "github.com/jaimegago/petri/pkg/provisioners/git"
	"github.com/jaimegago/petri/pkg/provisioners/kubectl"
	localprov "github.com/jaimegago/petri/pkg/provisioners/local"
	pulumiprov "github.com/jaimegago/petri/pkg/provisioners/pulumi"
	tfprov "github.com/jaimegago/petri/pkg/provisioners/terraform"
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
	log           *slog.Logger
	stateMgr      state.Manager
	cipher        crypto.Cipher
	metricsReg    *prometheus.Registry
	metricsRec    *metrics.Recorder
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
		c.newCompletionCmd(),
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

// metricsRecorder returns the shared metrics recorder, initialising it on first call.
// Registers with a dedicated prometheus.Registry (not the global default)
// to avoid conflicts across commands.
func (c *CLI) metricsRecorder() *metrics.Recorder {
	if c.metricsRec != nil {
		return c.metricsRec
	}
	c.metricsReg = prometheus.NewRegistry()
	c.metricsRec = metrics.New(c.metricsReg)
	return c.metricsRec
}

// startMetricsServer starts the Prometheus HTTP server in the background if
// metrics are enabled in config. The server stops when ctx is cancelled.
func (c *CLI) startMetricsServer(ctx context.Context) {
	if c.cfg == nil || !c.cfg.Observability.MetricsEnabled {
		return
	}
	port := c.cfg.Observability.MetricsPort
	if port == 0 {
		port = 9090
	}
	addr := fmt.Sprintf(":%d", port)
	go func() {
		if err := metrics.StartServer(ctx, addr, c.metricsReg, c.log); err != nil {
			c.log.Warn("Metrics server stopped", "error", err)
		}
	}()
}

// buildOrchestrator constructs an Orchestrator wired with all real provisioners
// and generators. gitHubToken may be empty for local-only labs.
func (c *CLI) buildOrchestrator(gitHubToken string) (*orchestrator.Orchestrator, error) {
	mgr, err := c.stateManager()
	if err != nil {
		return nil, err
	}
	ciph, err := c.encryptionCipher()
	if err != nil {
		return nil, err
	}

	deps := orchestrator.Deps{
		State:   mgr,
		Cipher:  ciph,
		Log:     c.log,
		Metrics: c.metricsRecorder(),

		// Local provisioner (kind).
		LocalProv: localprov.New(localprov.Config{}),

		// Kubectl client factory.
		KubectlFactory: func(kubeconfigPath string) orchestrator.KubectlClient {
			return kubectl.New(kubectl.Config{KubeconfigPath: kubeconfigPath})
		},

		// Generators (always wired; they use embedded templates).
		IaCGen:           iacgen.New(),
		GitOpsGen:        gitopsgen.New(),
		AppsGen:          appsgen.New(),
		ObservabilityGen: obsgen.New(),
		PlatformGen:      platformgen.New(),
		CommitsGen:       commitsgen.New(),

		// Terraform provisioner.
		TFProv: tfprov.New(tfprov.Config{}),

		// Pulumi provisioner.
		PulumiProv: pulumiprov.New(pulumiprov.Config{}),
	}

	// Wire git provisioner only when a token is available.
	if gitHubToken != "" {
		deps.GitProv = gitprov.New(gitprov.Config{Token: gitHubToken})
	}

	return orchestrator.New(orchestrator.Config{}, deps), nil
}

// githubToken returns the GitHub PAT from the GITHUB_TOKEN environment variable.
func githubToken() string {
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return t
	}
	return os.Getenv("PETRI_GITHUB_TOKEN")
}
