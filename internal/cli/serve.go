package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/jaimegago/petri/pkg/chaos"
	"github.com/jaimegago/petri/pkg/oasis"
	"github.com/jaimegago/petri/pkg/preflight"
	"github.com/jaimegago/petri/pkg/types"
)

type serveOptions struct {
	listen         string
	lab            string
	kubeconfigPath string
	auditLogPath   string
	verify         bool
	deep           bool
}

func (c *CLI) newServeCmd() *cobra.Command {
	opts := &serveOptions{}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the OASIS environment provider HTTP server",
		Long: `Start an HTTP server implementing the OASIS environment provider API.
oasisctl connects to this server to provision and tear down evaluation
environments for AI infrastructure copilot assessment.

Typical workflow:
  petri create --name oasis-lab --company acme --level 1 --local
  petri serve --lab oasis-lab --listen :8090`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return c.runServe(opts)
		},
	}
	cmd.Flags().StringVar(&opts.listen, "listen", ":8090", "address to listen on")
	cmd.Flags().StringVar(&opts.lab, "lab", "", "name of an existing local lab to use as the base cluster (recommended)")
	cmd.Flags().StringVar(&opts.kubeconfigPath, "kubeconfig", "", "explicit kubeconfig path (overrides --lab and KUBECONFIG)")
	cmd.Flags().StringVar(&opts.auditLogPath, "audit-log-path", "", "path to Kubernetes audit log file for audit_log observations")
	cmd.Flags().BoolVar(&opts.verify, "verify", false, "run preflight checks before binding the listener; abort on failure")
	cmd.Flags().BoolVar(&opts.deep, "deep", false, "with --verify, also pull each verified image on the cluster (slower)")
	return cmd
}

func (c *CLI) runServe(opts *serveOptions) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	labInfo, err := c.resolveServeLabInfo(ctx, opts.kubeconfigPath, opts.lab)
	if err != nil {
		return err
	}

	// --audit-log-path flag takes precedence; fall back to lab metadata.
	auditLogPath := opts.auditLogPath
	if auditLogPath == "" && labInfo.auditLogPath != "" {
		auditLogPath = labInfo.auditLogPath
		c.log.Info("auto-detected audit log path from lab metadata", "path", auditLogPath)
	}

	if opts.verify {
		if err := c.runServeVerify(ctx, opts, labInfo, auditLogPath); err != nil {
			return err
		}
	}

	c.log.Info("starting OASIS provider server",
		"listen", opts.listen,
		"lab", opts.lab,
		"kubeconfig", labInfo.kubeconfigPath,
		"audit_log_path", auditLogPath,
	)

	kube := chaos.NewKubeClient(labInfo.kubeconfigPath)
	provider := oasis.New(oasis.ProviderConfig{
		KubeconfigPath:   labInfo.kubeconfigPath,
		AuditLogPath:     auditLogPath,
		LabLevel:         labInfo.labLevel,
		OASISModeEnabled: labInfo.oasisMode,
		DefaultImage:     c.cfg.OASIS.DefaultImage,
	}, kube, c.log)

	srv := oasis.NewServer(provider, c.log)
	if err := srv.ListenAndServe(ctx, opts.listen); err != nil {
		return fmt.Errorf("OASIS server stopped: %w", err)
	}
	return nil
}

// runServeVerify runs preflight checks before binding the HTTP listener.
// On failure it renders the human-readable report directly to stderr (so
// it stays readable for the operator — no slog timestamp prefix per line,
// no empty WARN entries), emits a single structured WARN summarising the
// failure (so process supervisors capturing structured logs see one
// actionable event), and returns an error so runServe aborts before any
// port is opened.
func (c *CLI) runServeVerify(ctx context.Context, opts *serveOptions, labInfo serveLabInfo, auditLogPath string) error {
	preflightOpts, err := c.buildPreflightOptions(ctx, verifyResolverInput{
		// labInfo.kubeconfigPath is already resolved (lab metadata or
		// default); pass it directly so we don't re-query the state DB.
		kubeconfigPath: labInfo.kubeconfigPath,
		auditLogPath:   auditLogPath,
		deep:           opts.deep,
	})
	if err != nil {
		return fmt.Errorf("building preflight options: %w", err)
	}

	report, err := preflight.Run(ctx, preflightOpts)
	if err != nil {
		return fmt.Errorf("running preflight: %w", err)
	}

	if report.Failed() {
		preflight.Render(c.serveVerifyReportWriter(), report)
		c.log.Warn("preflight failed",
			"failed_checks", failureCount(report),
			"total_checks", len(report.Checks),
			"duration_ms", report.Duration.Milliseconds(),
		)
		return fmt.Errorf("preflight failed: refusing to start the OASIS server")
	}
	c.log.Info("verify checks passed",
		"total_checks", len(report.Checks),
		"duration_ms", report.Duration.Milliseconds(),
	)
	return nil
}

// serveVerifyReportWriter returns the destination for the human-readable
// preflight report when serve --verify fails. Production writes to
// os.Stderr; tests inject a buffer via c.serveVerifyOut.
func (c *CLI) serveVerifyReportWriter() io.Writer {
	if c.serveVerifyOut != nil {
		return c.serveVerifyOut
	}
	return os.Stderr
}

// serveLabInfo holds resolved lab connection details for the serve command.
type serveLabInfo struct {
	kubeconfigPath string
	auditLogPath   string
	labLevel       int
	oasisMode      bool
}

// resolveServeLabInfo returns the kubeconfig and (when available) audit-log
// metadata for the lab the operator chose. Resolution precedence matches
// `petri verify`: an explicit --kubeconfig takes priority over --lab. If
// both are supplied, the lab is ignored (and an INFO log line records that
// the lab metadata for audit-log/level/oasisMode is therefore not consulted).
func (c *CLI) resolveServeLabInfo(ctx context.Context, kubeconfigFlag, labName string) (serveLabInfo, error) {
	if kubeconfigFlag != "" {
		if labName != "" {
			c.log.Info("--kubeconfig provided; --lab ignored", "lab", labName)
		}
		return serveLabInfo{kubeconfigPath: kubeconfigFlag}, nil
	}
	if labName == "" {
		c.log.Warn("no --lab or --kubeconfig flag provided; using default kubeconfig — all scenarios share the same cluster")
		return serveLabInfo{}, nil
	}

	mgr, err := c.stateManager()
	if err != nil {
		return serveLabInfo{}, err
	}

	lab, err := mgr.GetLabByName(ctx, labName)
	if err != nil {
		return serveLabInfo{}, fmt.Errorf("lab %q not found: %w", labName, err)
	}
	if lab.Status != types.LabStatusActive {
		return serveLabInfo{}, fmt.Errorf("lab %q is not active (status: %s); only active labs can serve OASIS scenarios", labName, lab.Status)
	}
	if lab.CloudProvider != types.CloudProviderLocal {
		return serveLabInfo{}, fmt.Errorf("lab %q uses provider %q; petri serve currently requires a local (kind) lab", labName, lab.CloudProvider)
	}

	kubeconfigPath := localKubeconfigPath(lab)
	if kubeconfigPath == "" {
		return serveLabInfo{}, fmt.Errorf("lab %q has no kubeconfig path in metadata; was it created successfully?", labName)
	}

	var auditLogPath string
	var oasisMode bool
	if len(lab.Metadata.Clusters) > 0 {
		auditLogPath = lab.Metadata.Clusters[0].AuditLogPath
		oasisMode = lab.Metadata.Clusters[0].OASISMode
	}

	c.log.Info("using lab cluster for OASIS scenarios", "lab", labName, "kubeconfig", kubeconfigPath)
	return serveLabInfo{
		kubeconfigPath: kubeconfigPath,
		auditLogPath:   auditLogPath,
		labLevel:       lab.Level,
		oasisMode:      oasisMode,
	}, nil
}
