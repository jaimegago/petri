package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/jaimegago/petri/pkg/asynctasks"
	"github.com/jaimegago/petri/pkg/chaos"
	"github.com/jaimegago/petri/pkg/oasis"
	"github.com/jaimegago/petri/pkg/preflight"
	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
)

type serveOptions struct {
	listen           string
	lab              string
	kubeconfigPath   string
	auditLogPath     string
	imagePullTimeout time.Duration
	verify           bool
	deep             bool
	noReaper         bool
}

// reaperShutdownTimeout bounds how long petri serve will wait for the lab
// reaper goroutine to exit after SIGTERM before forcing exit. Matches the
// existing OASIS async-task shutdown discipline.
const reaperShutdownTimeout = 30 * time.Second

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
	cmd.Flags().DurationVar(&opts.imagePullTimeout, "image-pull-timeout", 0,
		"budget for pulling scenario images, separate from the 60s rollout budget (default from oasis.image_pull_timeout)")
	cmd.Flags().BoolVar(&opts.verify, "verify", false, "run preflight checks before binding the listener; abort on failure")
	cmd.Flags().BoolVar(&opts.deep, "deep", false, "with --verify, also pull each verified image on the cluster (slower)")
	cmd.Flags().BoolVar(&opts.noReaper, "no-reaper", false, "disable the background lab reaper goroutine (overrides oasis.disable_lab_reaper=false)")
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

	// --image-pull-timeout takes precedence; fall back to config.
	imagePullTimeout := opts.imagePullTimeout
	if imagePullTimeout <= 0 {
		imagePullTimeout = c.cfg.OASIS.ImagePullTimeout
	}

	tasks := asynctasks.New(c.log)
	c.startLabReaper(ctx, tasks, opts.noReaper)

	kube := chaos.NewKubeClient(labInfo.kubeconfigPath)
	provider := oasis.New(oasis.ProviderConfig{
		KubeconfigPath:   labInfo.kubeconfigPath,
		AuditLogPath:     auditLogPath,
		LabLevel:         labInfo.labLevel,
		OASISModeEnabled: labInfo.oasisMode,
		DefaultImage:     c.cfg.OASIS.DefaultImage,
		ImagePullTimeout: imagePullTimeout,
	}, kube, c.log)

	srv := oasis.NewServer(provider, c.log)
	serveErr := srv.ListenAndServe(ctx, opts.listen)

	// Wait for the reaper goroutine to honour ctx cancellation. We're past
	// the HTTP listener at this point — the only remaining task to drain is
	// the reaper itself.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), reaperShutdownTimeout)
	defer drainCancel()
	if !tasks.Wait(drainCtx) {
		c.log.Warn("lab reaper did not exit within shutdown budget; abandoning",
			"timeout", reaperShutdownTimeout.String(),
		)
	}

	if serveErr != nil {
		return fmt.Errorf("OASIS server stopped: %w", serveErr)
	}
	return nil
}

// startLabReaper spawns the background reaper goroutine when enabled. The
// reaper runs StartCleanupLoop on cfg.OASIS.LabReaperInterval and exits on
// ctx cancellation; its goroutine is tracked by `tasks` so runServe can
// drain it on graceful shutdown.
func (c *CLI) startLabReaper(ctx context.Context, tasks *asynctasks.Tasks, noReaperFlag bool) {
	if noReaperFlag || c.cfg.OASIS.DisableLabReaper {
		c.log.Info("lab reaper disabled by config/flag; expired labs will not be reaped during this serve session")
		return
	}
	interval := c.cfg.OASIS.LabReaperInterval
	if interval <= 0 {
		// config.Load applies the default, but defend against direct callers
		// who hand-craft a Config and forget to set it.
		return
	}
	gracePeriod := c.cfg.Cleanup.GracePeriod

	orch, err := c.buildOrchestrator(githubToken())
	if err != nil {
		c.log.Warn("lab reaper not started: orchestrator init failed", "error", err)
		return
	}

	tasks.Go("lab-reaper", func() {
		orch.StartCleanupLoop(ctx, interval, gracePeriod)
	})
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
		_ = preflight.Render(c.serveVerifyReportWriter(), report)
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

	// Apply the lazy on-read expiry transition before checking Status, so
	// EXPIRED ends up in the DB and the message we render reflects truth.
	if _, terr := state.TransitionIfExpired(ctx, mgr, lab); terr != nil {
		c.log.Warn("failed to lazily transition lab to EXPIRED", "lab", lab.Name, "error", terr)
	}

	if lab.Status != types.LabStatusActive {
		return serveLabInfo{}, serveLabStatusError(lab)
	}
	if lab.CloudProvider != types.CloudProviderLocal {
		return serveLabInfo{}, fmt.Errorf("lab %q uses provider %q; petri serve currently requires a local (kind) lab", labName, lab.CloudProvider)
	}

	kubeconfigPath := localKubeconfigPath(lab)
	if kubeconfigPath == "" {
		return serveLabInfo{}, fmt.Errorf("lab %q has no kubeconfig path in metadata; was it created successfully?", labName)
	}

	// Verify the substrate is reachable on disk. A missing kubeconfig file
	// means the cluster was deleted out-of-band (e.g. `kind delete cluster`
	// or a manual rm) while the lab record still claims ACTIVE — serving
	// would silently hand the OASIS client a dead substrate. Refuse and
	// mark the lab ERROR so future reads reflect the divergence.
	if _, err := os.Stat(kubeconfigPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			lab.Status = types.LabStatusError
			if lab.Metadata.ErrorMessage == "" {
				lab.Metadata.ErrorMessage = fmt.Sprintf("kubeconfig file %q missing; cluster may have been deleted out-of-band", kubeconfigPath)
			}
			if uerr := mgr.UpdateLab(ctx, lab); uerr != nil {
				c.log.Warn("failed to mark lab ERROR after kubeconfig miss", "lab", labName, "error", uerr)
			}
			return serveLabInfo{}, fmt.Errorf("lab %q is ACTIVE but kubeconfig file %q is missing. The cluster may have been deleted out-of-band. Lab record marked ERROR. Run 'petri destroy %s' to clean up the record", labName, kubeconfigPath, labName)
		}
		return serveLabInfo{}, fmt.Errorf("statting kubeconfig %q for lab %q: %w", kubeconfigPath, labName, err)
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

// serveLabStatusError returns a refusal error tailored to the lab's current
// non-ACTIVE status, including a concrete next-step command the operator
// can run. Serve must refuse rather than warn-and-continue: serving against
// a non-ACTIVE substrate silently miscompares scenarios against a lab the
// operator believes is healthy.
func serveLabStatusError(lab *types.Lab) error {
	switch lab.Status {
	case types.LabStatusExpired:
		return fmt.Errorf("lab %q is EXPIRED (past TTL since %s). Run 'petri destroy %s && petri create --name %s ...' to recreate, or 'petri cleanup --expired' to clean up all expired labs",
			lab.Name, lab.ExpiresAt.UTC().Format(time.RFC3339), lab.Name, lab.Name)
	case types.LabStatusDestroyed:
		return fmt.Errorf("lab %q is DESTROYED. Run 'petri create --name %s ...' to create a fresh lab", lab.Name, lab.Name)
	case types.LabStatusError:
		msg := lab.Metadata.ErrorMessage
		if msg == "" {
			msg = "(no error detail recorded)"
		}
		return fmt.Errorf("lab %q is in ERROR state: %s. Run 'petri destroy %s' to clean up the record and 'petri create --name %s ...' to recreate", lab.Name, msg, lab.Name, lab.Name)
	case types.LabStatusCreating:
		return fmt.Errorf("lab %q is still CREATING (started %s). Wait for create to complete, then re-run 'petri serve --lab %s'",
			lab.Name, lab.CreatedAt.UTC().Format(time.RFC3339), lab.Name)
	case types.LabStatusDestroying:
		return fmt.Errorf("lab %q is currently DESTROYING. Wait for destroy to complete, then create a fresh lab with 'petri create --name %s ...'", lab.Name, lab.Name)
	default:
		return fmt.Errorf("lab %q is not active (status: %s); only active labs can serve OASIS scenarios", lab.Name, lab.Status)
	}
}
