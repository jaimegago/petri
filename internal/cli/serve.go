package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/jaimegago/petri/pkg/chaos"
	"github.com/jaimegago/petri/pkg/oasis"
	"github.com/jaimegago/petri/pkg/types"
)

type serveOptions struct {
	listen       string
	lab          string
	auditLogPath string
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
		RunE: func(_ *cobra.Command, _ []string) error {
			return c.runServe(opts)
		},
	}
	cmd.Flags().StringVar(&opts.listen, "listen", ":8090", "address to listen on")
	cmd.Flags().StringVar(&opts.lab, "lab", "", "name of an existing local lab to use as the base cluster (recommended)")
	cmd.Flags().StringVar(&opts.auditLogPath, "audit-log-path", "", "path to Kubernetes audit log file for audit_log observations")
	return cmd
}

func (c *CLI) runServe(opts *serveOptions) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	labInfo, err := c.resolveServeLabInfo(ctx, opts.lab)
	if err != nil {
		return err
	}

	// --audit-log-path flag takes precedence; fall back to lab metadata.
	auditLogPath := opts.auditLogPath
	if auditLogPath == "" && labInfo.auditLogPath != "" {
		auditLogPath = labInfo.auditLogPath
		c.log.Info("auto-detected audit log path from lab metadata", "path", auditLogPath)
	}

	c.log.Info("starting OASIS provider server",
		"listen", opts.listen,
		"lab", opts.lab,
		"kubeconfig", labInfo.kubeconfigPath,
		"audit_log_path", auditLogPath,
	)

	kube := chaos.NewKubeClient(labInfo.kubeconfigPath)
	provider := oasis.New(oasis.ProviderConfig{
		KubeconfigPath: labInfo.kubeconfigPath,
		AuditLogPath:   auditLogPath,
	}, kube, c.log)

	srv := oasis.NewServer(provider, c.log)
	if err := srv.ListenAndServe(ctx, opts.listen); err != nil {
		return fmt.Errorf("OASIS server stopped: %w", err)
	}
	return nil
}

// serveLabInfo holds resolved lab connection details for the serve command.
type serveLabInfo struct {
	kubeconfigPath string
	auditLogPath   string
}

// resolveServeLabInfo returns the kubeconfig and audit log paths for the named lab.
func (c *CLI) resolveServeLabInfo(ctx context.Context, labName string) (serveLabInfo, error) {
	if labName == "" {
		c.log.Warn("no --lab flag provided; using default kubeconfig — all scenarios share the same cluster")
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
	if len(lab.Metadata.Clusters) > 0 {
		auditLogPath = lab.Metadata.Clusters[0].AuditLogPath
	}

	c.log.Info("using lab cluster for OASIS scenarios", "lab", labName, "kubeconfig", kubeconfigPath)
	return serveLabInfo{
		kubeconfigPath: kubeconfigPath,
		auditLogPath:   auditLogPath,
	}, nil
}
