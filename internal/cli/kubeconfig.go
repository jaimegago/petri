package cli

import (
	"context"
	"fmt"

	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
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

// resolveActiveLabKubeconfig resolves a kubeconfig path for commands that act
// against a live lab cluster (e.g. `petri inject`), enforcing the same
// active-lab guard `petri serve` uses. Precedence mirrors resolveKubeconfig:
// --kubeconfig wins over --lab. Unlike resolveKubeconfig it rejects a
// non-active lab via the shared serveLabStatusError, so a caller never mutates
// an EXPIRED/ERROR/CREATING substrate. Exactly one of kubeconfigFlag/labName
// must be supplied.
func (c *CLI) resolveActiveLabKubeconfig(ctx context.Context, kubeconfigFlag, labName string) (string, error) {
	if kubeconfigFlag != "" {
		if labName != "" && c.log != nil {
			c.log.Info("--kubeconfig provided; --lab ignored", "lab", labName)
		}
		return kubeconfigFlag, nil
	}
	if labName == "" {
		return "", fmt.Errorf("either --lab or --kubeconfig is required")
	}

	mgr, err := c.stateManager()
	if err != nil {
		return "", err
	}
	lab, err := mgr.GetLabByName(ctx, labName)
	if err != nil {
		return "", fmt.Errorf("lab %q not found: %w", labName, err)
	}

	// Apply the lazy on-read expiry transition before checking Status so an
	// elapsed-TTL lab is reported as EXPIRED rather than silently ACTIVE.
	if _, terr := state.TransitionIfExpired(ctx, mgr, lab); terr != nil {
		c.log.Warn("failed to lazily transition lab to EXPIRED", "lab", lab.Name, "error", terr)
	}
	if lab.Status != types.LabStatusActive {
		return "", serveLabStatusError(lab)
	}

	kc := localKubeconfigPath(lab)
	if kc == "" {
		return "", fmt.Errorf("lab %q has no kubeconfig path in metadata; was it created successfully?", labName)
	}
	return kc, nil
}
