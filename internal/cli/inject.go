package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jaimegago/petri/pkg/chaos"
)

type injectOptions struct {
	lab            string
	kubeconfigPath string
	target         string
	params         []string
	dryRun         bool
}

// newInjectCmd builds the `petri inject` command: a one-shot trigger that
// injects exactly one chaos fault into a running lab. It is the runtime
// counterpart to the provision-time pkg/workloadstate capability — chaos
// perturbs an already-running resource, workloadstate synthesises a workload
// born into a named state. Continuous/random injection (chaos.ChaosRunner) and
// scripted scenario files are deliberately out of scope here.
func (c *CLI) newInjectCmd() *cobra.Command {
	opts := &injectOptions{}

	// Accepted fault types are sourced structurally from DefaultFaults so the
	// help text can never drift from the catalog.
	accepted := strings.Join(acceptedFaultTypes(chaos.DefaultFaults()), ", ")

	cmd := &cobra.Command{
		Use:   "inject <fault-type>",
		Short: "Inject a single chaos fault into a running lab",
		Long: fmt.Sprintf(`Inject exactly one chaos fault into a named, running lab.

This is the runtime counterpart to Petri's provision-time born-into-state
capability (pkg/workloadstate): chaos perturbs an already-running resource,
whereas workloadstate synthesises a workload that starts in a named state.

The fault is looked up in the catalog and executed once; this command does not
start the continuous random ChaosRunner.

Accepted fault types:
  %s

Each fault documents its own supported --param keys (e.g. cpu_pressure accepts
duration and workers; network_latency accepts latency_ms and jitter_ms). Faults
that take no parameters ignore --param.

Examples:
  petri inject restart_deployment --lab eval-lab --target apps/Deployment/boutique-frontend
  petri inject cpu_pressure --lab eval-lab --target apps/Pod/api --param duration=30s --param workers=2
  petri inject kill_pod --kubeconfig ~/.kube/config --target apps/Pod/frontend --dry-run`, accepted),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return c.runInject(cmd.Context(), args[0], opts, chaos.DefaultFaults(), chaos.NewKubeClient, os.Stdout)
		},
	}

	cmd.Flags().StringVar(&opts.lab, "lab", "", "name of the target lab (primary target selector)")
	cmd.Flags().StringVar(&opts.kubeconfigPath, "kubeconfig", "", "explicit kubeconfig path (overrides --lab)")
	cmd.Flags().StringVar(&opts.target, "target", "", "target resource as namespace/kind/name (e.g. apps/Deployment/frontend)")
	cmd.Flags().StringArrayVar(&opts.params, "param", nil, "fault parameter as key=value (repeatable)")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "resolve and validate everything, print the plan, but do not mutate the cluster")
	return cmd
}

// runInject is the testable core of the inject command. The fault registry and
// the kube-client constructor are injected so parsing, validation, the
// active-lab guard, dry-run, and dispatch can be exercised without a real
// cluster. newKube is only invoked on a real (non-dry-run) execution.
func (c *CLI) runInject(
	ctx context.Context,
	faultTypeArg string,
	opts *injectOptions,
	faults map[chaos.FaultType]chaos.Fault,
	newKube func(kubeconfigPath string) chaos.KubeClient,
	out io.Writer,
) error {
	// 1. Validate the fault type against the structural catalog.
	fault, ok := faults[chaos.FaultType(faultTypeArg)]
	if !ok {
		return fmt.Errorf("unknown fault type %q; accepted types: %s",
			faultTypeArg, strings.Join(acceptedFaultTypes(faults), ", "))
	}

	// 2. Parse the target triple and params; CLI validates shape only, never
	// per-fault semantics (the fault resolves and validates its own target).
	target, err := parseTarget(opts.target)
	if err != nil {
		return err
	}
	params, err := parseParams(opts.params)
	if err != nil {
		return err
	}

	// 3. Resolve the kubeconfig, enforcing the shared active-lab guard. This
	// validates the lab even on the dry-run path.
	kubeconfigPath, err := c.resolveActiveLabKubeconfig(ctx, opts.kubeconfigPath, opts.lab)
	if err != nil {
		return err
	}

	// 4. Dry-run: report the plan and stop before touching the cluster.
	if opts.dryRun {
		printInjectPlan(out, fault.Type(), target, params)
		return nil
	}

	// 5. Execute the single fault, mirroring how ChaosRunner logs an injection.
	kube := newKube(kubeconfigPath)
	return dispatchInject(ctx, c.log, out, fault, kube, target, params)
}

// dispatchInject executes one fault and logs the attempt and outcome with the
// same fields the ChaosRunner uses (fault_type, namespace, target, kind). On
// failure it returns the error so the root command exits non-zero.
func dispatchInject(
	ctx context.Context,
	log *slog.Logger,
	out io.Writer,
	fault chaos.Fault,
	kube chaos.KubeClient,
	target chaos.TargetResource,
	params map[string]string,
) error {
	ft := string(fault.Type())
	if log != nil {
		log.Info("injecting fault",
			"fault_type", ft,
			"namespace", target.Namespace,
			"target", target.Name,
			"kind", target.Kind,
		)
	}

	started := time.Now()
	if err := fault.Execute(ctx, kube, target, params); err != nil {
		if log != nil {
			log.Warn("fault injection failed",
				"fault_type", ft,
				"namespace", target.Namespace,
				"target", target.Name,
				"error", err,
			)
		}
		return fmt.Errorf("injecting %s into %s/%s: %w", ft, target.Namespace, target.Name, err)
	}

	if log != nil {
		log.Info("fault injection succeeded",
			"fault_type", ft,
			"namespace", target.Namespace,
			"target", target.Name,
			"duration_ms", time.Since(started).Milliseconds(),
		)
	}
	_, _ = fmt.Fprintf(out, "Injected %s against %s/%s (kind %s)\n", ft, target.Namespace, target.Name, target.Kind)
	return nil
}

// printInjectPlan renders what a dry-run would have executed.
func printInjectPlan(out io.Writer, ft chaos.FaultType, target chaos.TargetResource, params map[string]string) {
	var sb strings.Builder
	sb.WriteString("DRY RUN — would inject fault:\n")
	fmt.Fprintf(&sb, "  fault:     %s\n", ft)
	fmt.Fprintf(&sb, "  namespace: %s\n", target.Namespace)
	fmt.Fprintf(&sb, "  kind:      %s\n", target.Kind)
	fmt.Fprintf(&sb, "  name:      %s\n", target.Name)
	if len(params) > 0 {
		keys := make([]string, 0, len(params))
		for k := range params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+params[k])
		}
		fmt.Fprintf(&sb, "  params:    %s\n", strings.Join(parts, " "))
	}
	_, _ = io.WriteString(out, sb.String())
}

// acceptedFaultTypes returns the sorted set of fault-type identifiers present
// in the registry. Both the inject command's validation and its help text
// derive their accepted list from this single structural source so neither can
// drift from the catalog.
func acceptedFaultTypes(faults map[chaos.FaultType]chaos.Fault) []string {
	types := make([]string, 0, len(faults))
	for ft := range faults {
		types = append(types, string(ft))
	}
	sort.Strings(types)
	return types
}

// parseTarget parses a "namespace/kind/name" triple into a chaos.TargetResource.
// The kind and name pass through verbatim — the fault resolves and validates its
// own target, so no per-fault kind validation happens here.
func parseTarget(raw string) (chaos.TargetResource, error) {
	if strings.TrimSpace(raw) == "" {
		return chaos.TargetResource{}, fmt.Errorf("--target is required (format: namespace/kind/name)")
	}
	parts := strings.Split(raw, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return chaos.TargetResource{}, fmt.Errorf("malformed target %q: expected namespace/kind/name", raw)
	}
	return chaos.TargetResource{
		Namespace: parts[0],
		Kind:      parts[1],
		Name:      parts[2],
	}, nil
}

// parseParams parses repeated key=value flags into a map. A value not in
// key=value form is a hard error. Per-fault parameter semantics are not
// validated here — that belongs to the fault.
func parseParams(raw []string) (map[string]string, error) {
	params := make(map[string]string, len(raw))
	for _, kv := range raw {
		key, value, found := strings.Cut(kv, "=")
		if !found || key == "" {
			return nil, fmt.Errorf("malformed --param %q: expected key=value", kv)
		}
		params[key] = value
	}
	return params, nil
}
