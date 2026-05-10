package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/jaimegago/petri/pkg/preflight"
)

// utilImage is the OCI image used internally to construct intentionally
// unhealthy or behavioural pod states. It must match
// pkg/oasis.defaultUtilImage. Kept as a constant here (not imported) because
// pkg/oasis keeps it package-private; preflight needs the value to verify
// pullability without taking a dependency on pkg/oasis.
const utilImage = "registry.k8s.io/e2e-test-images/busybox:1.37.0-2"

type verifyOptions struct {
	deep           bool
	json           bool
	lab            string
	kubeconfigPath string
	auditLogPath   string
}

func (c *CLI) newVerifyCmd() *cobra.Command {
	opts := &verifyOptions{}
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Run substrate-readiness preflight checks",
		Long: `Run preflight checks against the substrate before starting an OASIS
evaluation. Verifies kubeconfig parseability, cluster reachability, RBAC,
default image pullability, and audit log path writability.

Use --deep to additionally verify image pullability from inside the cluster
(creates a throwaway namespace and pod; costs ~30-60s per image).

Use --json to emit a machine-readable report. Exit code is 0 if every check
passes, non-zero if any fails.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			return c.runVerify(cmd.Context(), opts)
		},
	}
	cmd.Flags().BoolVar(&opts.deep, "deep", false, "additionally pull each verified image on the cluster (slower)")
	cmd.Flags().BoolVar(&opts.json, "json", false, "emit a machine-readable JSON report instead of human-readable text")
	cmd.Flags().StringVar(&opts.lab, "lab", "", "petri lab whose kubeconfig should be verified (defaults to the host's KUBECONFIG)")
	cmd.Flags().StringVar(&opts.kubeconfigPath, "kubeconfig", "", "explicit kubeconfig path (overrides --lab and KUBECONFIG)")
	cmd.Flags().StringVar(&opts.auditLogPath, "audit-log-path", "", "audit log path to verify (overrides oasis.audit_log_path)")
	return cmd
}

func (c *CLI) runVerify(ctx context.Context, opts *verifyOptions) error {
	preflightOpts, err := c.buildPreflightOptions(ctx, verifyResolverInput{
		lab:            opts.lab,
		kubeconfigPath: opts.kubeconfigPath,
		auditLogPath:   opts.auditLogPath,
		deep:           opts.deep,
	})
	if err != nil {
		return err
	}

	report, err := preflight.Run(ctx, preflightOpts)
	if err != nil {
		return fmt.Errorf("running preflight: %w", err)
	}

	if opts.json {
		if err := preflight.RenderJSON(os.Stdout, report); err != nil {
			return fmt.Errorf("encoding report: %w", err)
		}
	} else {
		preflight.Render(os.Stdout, report)
	}

	if report.Failed() {
		// Returning a non-nil error makes the root command exit non-zero.
		// The detailed report is already on stdout; keep the error short
		// to avoid duplicating it.
		return fmt.Errorf("preflight failed: %d check(s) did not pass", failureCount(report))
	}
	return nil
}

// failureCount counts failed checks for the short top-line error.
func failureCount(r *preflight.Report) int {
	n := 0
	for _, c := range r.Checks {
		if c.Status == preflight.StatusFail {
			n++
		}
	}
	return n
}

// verifyResolverInput captures every flag that influences how preflight
// Options are built. Both `petri verify` and `petri serve --verify` go
// through buildPreflightOptions so the resolution rules are shared.
type verifyResolverInput struct {
	// lab, when non-empty, is the petri lab whose kubeconfig should be
	// verified.
	lab string
	// kubeconfigPath, when non-empty, takes precedence over lab and the
	// environment's default kubeconfig.
	kubeconfigPath string
	// auditLogPath, when non-empty, overrides cfg.OASIS.AuditLogPath.
	auditLogPath string
	// deep is a passthrough.
	deep bool
}

// buildPreflightOptions resolves a preflight.Options from the loaded config,
// the input flags, and (optionally) lab metadata. It is called by both
// `petri verify` and `petri serve --verify`.
func (c *CLI) buildPreflightOptions(ctx context.Context, in verifyResolverInput) (preflight.Options, error) {
	kubeconfig, err := c.resolveKubeconfig(ctx, in.kubeconfigPath, in.lab)
	if err != nil {
		return preflight.Options{}, err
	}

	auditPath := in.auditLogPath
	if auditPath == "" && c.cfg != nil {
		auditPath = c.cfg.OASIS.AuditLogPath
	}

	defaultImage := ""
	if c.cfg != nil {
		defaultImage = c.cfg.OASIS.DefaultImage
	}

	util := utilImage
	if c.preflightUtilImage != "" {
		util = c.preflightUtilImage
	}

	return preflight.Options{
		KubeconfigPath: kubeconfig,
		AuditLogPath:   auditPath,
		DefaultImage:   defaultImage,
		UtilImage:      util,
		Deep:           in.deep,
		KubeClient:     c.preflightKube,
		HTTPClient:     c.preflightHTTP,
	}, nil
}
