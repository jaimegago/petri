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

	kubeconfigPath, err := c.resolveServeKubeconfig(ctx, opts.lab)
	if err != nil {
		return err
	}

	c.log.Info("starting OASIS provider server",
		"listen", opts.listen,
		"lab", opts.lab,
		"kubeconfig", kubeconfigPath,
		"audit_log_path", opts.auditLogPath,
	)

	kube := chaos.NewKubeClient(kubeconfigPath)
	provider := oasis.New(oasis.ProviderConfig{
		KubeconfigPath: kubeconfigPath,
		AuditLogPath:   opts.auditLogPath,
	}, kube, c.log)

	srv := oasis.NewServer(provider, c.log)
	if err := srv.ListenAndServe(ctx, opts.listen); err != nil {
		return fmt.Errorf("OASIS server stopped: %w", err)
	}
	return nil
}

// resolveServeKubeconfig returns the kubeconfig path for the named lab.
// If labName is empty, returns empty string (uses the default kubeconfig).
func (c *CLI) resolveServeKubeconfig(ctx context.Context, labName string) (string, error) {
	if labName == "" {
		c.log.Warn("no --lab flag provided; using default kubeconfig — all scenarios share the same cluster")
		return "", nil
	}

	mgr, err := c.stateManager()
	if err != nil {
		return "", err
	}

	lab, err := mgr.GetLabByName(ctx, labName)
	if err != nil {
		return "", fmt.Errorf("lab %q not found: %w", labName, err)
	}
	if lab.Status != types.LabStatusActive {
		return "", fmt.Errorf("lab %q is not active (status: %s); only active labs can serve OASIS scenarios", labName, lab.Status)
	}
	if lab.CloudProvider != types.CloudProviderLocal {
		return "", fmt.Errorf("lab %q uses provider %q; petri serve currently requires a local (kind) lab", labName, lab.CloudProvider)
	}

	kubeconfigPath := localKubeconfigPath(lab)
	if kubeconfigPath == "" {
		return "", fmt.Errorf("lab %q has no kubeconfig path in metadata; was it created successfully?", labName)
	}

	c.log.Info("using lab cluster for OASIS scenarios", "lab", labName, "kubeconfig", kubeconfigPath)
	return kubeconfigPath, nil
}
