package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/jaimegago/petri/pkg/chaos"
	"github.com/jaimegago/petri/pkg/config"
	"github.com/jaimegago/petri/pkg/workloadstate"
)

// runInjectState is the born-into-state mode of inject: it provisions a
// Deployment at --target that starts in the requested pkg/workloadstate state,
// through workloadstate.Provision. The state vocabulary and its validation
// belong to workloadstate — the spec is rendered before the lab is resolved,
// so an unrecognised state fails with the capability's own error, which
// enumerates the accepted set, and nothing is applied.
func (c *CLI) runInjectState(
	ctx context.Context,
	opts *injectOptions,
	newKube func(kubeconfigPath string) chaos.KubeClient,
	out io.Writer,
) error {
	target, err := parseTarget(opts.target)
	if err != nil {
		return err
	}
	if !strings.EqualFold(target.Kind, "deployment") {
		return fmt.Errorf("--state provisions a Deployment; got kind %q", target.Kind)
	}

	spec := workloadstate.Spec{
		Name:      target.Name,
		Namespace: target.Namespace,
		Image:     c.workloadImage(opts.image),
		State:     opts.state,
	}
	if _, err := workloadstate.Render(spec); err != nil {
		return err
	}

	kubeconfigPath, err := c.resolveActiveLabKubeconfig(ctx, opts.kubeconfigPath, opts.lab)
	if err != nil {
		return err
	}

	if opts.dryRun {
		printStatePlan(out, spec)
		return nil
	}

	if c.log != nil {
		c.log.Info("provisioning born-into-state workload", "state", spec.State, "namespace", spec.Namespace, "target", spec.Name)
	}
	if err := workloadstate.Provision(ctx, newKube(kubeconfigPath), spec); err != nil {
		return fmt.Errorf("provisioning %s/%s in state %s: %w", spec.Namespace, spec.Name, spec.State, err)
	}
	_, _ = fmt.Fprintf(out, "Provisioned %s/%s born into state %s\n", spec.Namespace, spec.Name, spec.State)
	return nil
}

// workloadImage resolves --image, defaulting to the image OASIS Deployment
// entries use when they omit one, so both callers of workloadstate land on the
// same registry-safe default.
func (c *CLI) workloadImage(flag string) string {
	if flag != "" {
		return flag
	}
	if c.cfg != nil && c.cfg.OASIS.DefaultImage != "" {
		return c.cfg.OASIS.DefaultImage
	}
	return config.DefaultOASISImage
}

// printStatePlan renders what a born-into-state dry-run would have provisioned.
func printStatePlan(out io.Writer, spec workloadstate.Spec) {
	var sb strings.Builder
	sb.WriteString("DRY RUN — would provision born-into-state workload:\n")
	fmt.Fprintf(&sb, "  state:     %s\n", spec.State)
	fmt.Fprintf(&sb, "  namespace: %s\n", spec.Namespace)
	fmt.Fprintf(&sb, "  kind:      Deployment\n")
	fmt.Fprintf(&sb, "  name:      %s\n", spec.Name)
	fmt.Fprintf(&sb, "  image:     %s\n", spec.Image)
	_, _ = io.WriteString(out, sb.String())
}

// acceptedWorkloadStates returns the state vocabulary for help text, sourced
// structurally from workloadstate.AcceptedStates so it cannot drift.
func acceptedWorkloadStates() []string {
	states := workloadstate.AcceptedStates()
	names := make([]string, len(states))
	for i, st := range states {
		names[i] = string(st)
	}
	return names
}
