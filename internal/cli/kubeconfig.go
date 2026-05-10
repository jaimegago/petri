package cli

import (
	"context"
	"fmt"
)

// resolveKubeconfig resolves the kubeconfig path for subcommands that accept
// both --kubeconfig and --lab. Precedence is --kubeconfig > --lab > "" (the
// caller falls back to KUBECONFIG / default loading rules). When both flags
// are supplied, --kubeconfig wins and an INFO log line records that --lab was
// ignored. Both `petri verify` and `petri serve` go through this helper so
// the resolution rules are shared.
func (c *CLI) resolveKubeconfig(ctx context.Context, kubeconfigFlag, labFlag string) (string, error) {
	if kubeconfigFlag != "" {
		if labFlag != "" && c.log != nil {
			c.log.Info("--kubeconfig provided; --lab ignored", "lab", labFlag)
		}
		return kubeconfigFlag, nil
	}
	if labFlag == "" {
		return "", nil
	}
	mgr, err := c.stateManager()
	if err != nil {
		return "", err
	}
	lab, err := mgr.GetLabByName(ctx, labFlag)
	if err != nil {
		return "", fmt.Errorf("lab %q not found: %w", labFlag, err)
	}
	kc := localKubeconfigPath(lab)
	if kc == "" {
		return "", fmt.Errorf("lab %q has no kubeconfig path in metadata", labFlag)
	}
	return kc, nil
}
