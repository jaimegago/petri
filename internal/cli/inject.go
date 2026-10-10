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
	"github.com/jaimegago/petri/pkg/fault"
)

type injectOptions struct {
	lab            string
	kubeconfigPath string
	target         string
	params         []string
	dryRun         bool
	// timeout bounds the symptom wait of a catalog fault (pkg/fault). Chaos
	// faults return when their perturbation is applied and ignore it.
	timeout time.Duration
	// timeoutSet records whether --timeout was given explicitly, so a mode
	// that does not use it can refuse it rather than silently ignore it.
	timeoutSet bool
	// scenario is the path of a scripted scenario file (pkg/scenarios). It
	// selects the scenario mode and is exclusive with every other mode.
	scenario string
	// state is a pkg/workloadstate state. It selects the born-into-state
	// mode and is exclusive with every other mode.
	state string
	// image is the container image for the born-into-state mode. Empty
	// resolves to the configured OASIS default image.
	image string
}

// defaultInjectTimeout bounds how long a catalog fault waits for its declared
// symptom after the misconfiguration is applied. The application image is
// already on the node in the runtime case, so the wait is the application's
// own restart backoff, not a pull.
const defaultInjectTimeout = 2 * time.Minute

// expectParam is the reserved --param key that overrides the symptom a
// catalog fault waits for. It is not a fault parameter, so it is taken out
// before the fault's own parameters are parsed.
const expectParam = "expect"

// newInjectCmd builds the `petri inject` command: a one-shot trigger that
// injects exactly one chaos fault into a running lab. It is the runtime
// counterpart to the provision-time pkg/workloadstate capability — chaos
// perturbs an already-running resource, workloadstate synthesises a workload
// born into a named state. Two further modes share the command: a scripted
// scenario file (--scenario) replays an ordered fault sequence through the
// pkg/scenarios ScenarioRunner, and --state provisions a workload born into a
// pkg/workloadstate state. Continuous/random injection (chaos.ChaosRunner) is
// deliberately out of scope here.
func (c *CLI) newInjectCmd() *cobra.Command {
	opts := &injectOptions{}

	// Accepted fault types are sourced structurally from both catalogs so
	// the help text can never drift from either.
	accepted := strings.Join(acceptedFaultTypes(chaos.DefaultFaults()), ", ")
	causes := strings.Join(fault.Classes(), ", ")
	states := strings.Join(acceptedWorkloadStates(), ", ")

	cmd := &cobra.Command{
		Use:   "inject <fault-type> | --scenario <file.yaml> | --state <state>",
		Short: "Inject a chaos fault, a cause, a scripted fault sequence, or a born-into-state workload into a running lab",
		Long: fmt.Sprintf(`Inject exactly one chaos fault into a named, running lab, replay a
scripted fault sequence from a scenario file, or provision a workload born
into a named state.

This is the runtime counterpart to Petri's provision-time born-into-state
capability (pkg/workloadstate): chaos perturbs an already-running resource,
whereas workloadstate synthesises a workload that starts in a named state.

The fault is looked up in the catalog and executed once; this command does not
start the continuous random ChaosRunner.

Accepted chaos fault types (perturb a healthy resource):
  %s

Each fault documents its own supported --param keys (e.g. cpu_pressure accepts
duration and workers; network_latency accepts latency_ms and jitter_ms). Faults
that take no parameters ignore --param.

Accepted cause classes (pkg/fault — a misconfiguration the application
genuinely fails on, applied to a running Deployment of the application image;
the symptom is verified before the command returns):
  %s

A cause takes its class parameters as --param (config.missing-key: configMap,
key; config.unbounded-cache: env) and optionally --param expect=<status> to
override the symptom waited for. --target must name a Deployment. Only the second trigger of the same
catalog petri serve materialises at provision time.

Scenario mode (--scenario <file.yaml>) replays the ordered steps of a
pkg/scenarios file — each step a chaos fault type, target, parameters, delay
and optional steady-state check — against the lab. It is exclusive with the
other modes, and refuses --target, --param, --timeout and --image because
every step carries its own. Every step runs even if an earlier one fails; the
command exits non-zero if any step failed.

Born-into-state mode (--state <state>) provisions a Deployment that starts in
the named pkg/workloadstate state, with no healthy intermediate. --target names
the Deployment to create (namespace/Deployment/name) in an existing namespace;
--image sets the container image for the states that run one (default: the
configured OASIS default image). This is the symptom half only: a cause under
diagnosis goes through a cause class above, not through --state.

Accepted states:
  %s

Examples:
  petri inject restart_deployment --lab eval-lab --target apps/Deployment/boutique-frontend
  petri inject cpu_pressure --lab eval-lab --target apps/Pod/api --param duration=30s --param workers=2
  petri inject kill_pod --kubeconfig ~/.kube/config --target apps/Pod/frontend --dry-run
  petri inject config.missing-key --lab eval-lab --target default/Deployment/notification-service --param configMap=smtp-config --param key=SMTP_PORT
  petri inject config.unbounded-cache --lab eval-lab --target default/Deployment/user-service --param env=CACHE_MAX_ENTRIES
  petri inject --scenario scenarios/db-cascade.yaml --lab eval-lab --dry-run
  petri inject --state oomkilled --lab eval-lab --target apps/Deployment/batch-worker`, accepted, causes, states),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			opts.timeoutSet = cmd.Flags().Changed("timeout")
			return c.runInjectMode(cmd.Context(), args, opts, chaos.DefaultFaults(), chaos.NewKubeClient, os.Stdout)
		},
	}

	cmd.Flags().StringVar(&opts.lab, "lab", "", "name of the target lab (primary target selector)")
	cmd.Flags().StringVar(&opts.kubeconfigPath, "kubeconfig", "", "explicit kubeconfig path (overrides --lab)")
	cmd.Flags().StringVar(&opts.target, "target", "", "target resource as namespace/kind/name (e.g. apps/Deployment/frontend)")
	cmd.Flags().StringArrayVar(&opts.params, "param", nil, "fault parameter as key=value (repeatable)")
	cmd.Flags().BoolVar(&opts.dryRun, "dry-run", false, "resolve and validate everything, print the plan, but do not mutate the cluster")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", defaultInjectTimeout, "how long a cause class waits for its symptom after applying the misconfiguration")
	cmd.Flags().StringVar(&opts.scenario, "scenario", "", "replay the scripted fault sequence in this scenario YAML file (exclusive with a positional fault type and --state)")
	cmd.Flags().StringVar(&opts.state, "state", "", "provision a workload born into this state at --target (exclusive with a positional fault type and --scenario)")
	cmd.Flags().StringVar(&opts.image, "image", "", "container image for --state (default: the configured OASIS default image)")
	return cmd
}

// runInjectMode selects exactly one inject mode from the positional argument
// and the mode flags, refusing ambiguous combinations before anything is
// resolved: a positional fault type (chaos fault or cause class), --scenario,
// or --state — exactly one of them — and no flag the selected mode would
// ignore.
func (c *CLI) runInjectMode(
	ctx context.Context,
	args []string,
	opts *injectOptions,
	faults map[chaos.FaultType]chaos.Fault,
	newKube func(kubeconfigPath string) chaos.KubeClient,
	out io.Writer,
) error {
	var modes []string
	if len(args) > 0 {
		modes = append(modes, fmt.Sprintf("positional fault type %q", args[0]))
	}
	if opts.scenario != "" {
		modes = append(modes, "--scenario")
	}
	if opts.state != "" {
		modes = append(modes, "--state")
	}
	switch {
	case len(modes) == 0:
		return fmt.Errorf("one of a fault type, --scenario <file.yaml>, or --state <state> is required")
	case len(modes) > 1:
		return fmt.Errorf("%s are mutually exclusive; give exactly one", strings.Join(modes, " and "))
	}

	target := modeFlag{"--target", opts.target != ""}
	params := modeFlag{"--param", len(opts.params) > 0}
	timeout := modeFlag{"--timeout", opts.timeoutSet}
	image := modeFlag{"--image", opts.image != ""}

	switch {
	case opts.scenario != "":
		if err := refuseFlagsForMode("--scenario", "every step carries its own", target, params, timeout, image); err != nil {
			return err
		}
		return c.runInjectScenario(ctx, opts, faults, newKube, out)
	case opts.state != "":
		if err := refuseFlagsForMode("--state", "a born-into-state workload takes only --target and --image", params, timeout); err != nil {
			return err
		}
		return c.runInjectState(ctx, opts, newKube, out)
	default:
		if err := refuseFlagsForMode("a fault type", "--image applies only to --state", image); err != nil {
			return err
		}
		return c.runInject(ctx, args[0], opts, faults, newKube, out)
	}
}

// modeFlag names a flag and whether it was given.
type modeFlag struct {
	name string
	set  bool
}

// refuseFlagsForMode rejects the given flags that were set, so a flag the
// selected mode does not use fails loudly instead of being silently dropped.
// why completes "<mode> does not take <flags>: <why>".
func refuseFlagsForMode(mode, why string, flags ...modeFlag) error {
	var set []string
	for _, f := range flags {
		if f.set {
			set = append(set, f.name)
		}
	}
	if len(set) > 0 {
		return fmt.Errorf("%s does not take %s: %s", mode, strings.Join(set, ", "), why)
	}
	return nil
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
	// 1. Validate the fault type against the structural catalogs: chaos
	// first, then the cause catalog, which has its own dispatch.
	chaosFault, ok := faults[chaos.FaultType(faultTypeArg)]
	if !ok {
		if _, isCause := fault.Catalog()[fault.Class(faultTypeArg)]; isCause {
			return c.runInjectCause(ctx, faultTypeArg, opts, newKube, out)
		}
		return fmt.Errorf("unknown fault type %q; accepted types: %s",
			faultTypeArg, strings.Join(append(acceptedFaultTypes(faults), fault.Classes()...), ", "))
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
		printInjectPlan(out, chaosFault.Type(), target, params)
		return nil
	}

	// 5. Execute the single fault, mirroring how ChaosRunner logs an injection.
	kube := newKube(kubeconfigPath)
	return dispatchInject(ctx, c.log, out, chaosFault, kube, target, params)
}

// runInjectCause is the cause-catalog branch of inject: the misconfiguration
// a class names is applied to a running Deployment of the application image
// and the declared symptom is waited for, exactly as petri serve verifies it
// at provision time. The same fault.Parse resolves the parameters, so a
// scenario block and a --param list are held to the same rules.
func (c *CLI) runInjectCause(
	ctx context.Context,
	class string,
	opts *injectOptions,
	newKube func(kubeconfigPath string) chaos.KubeClient,
	out io.Writer,
) error {
	target, err := parseTarget(opts.target)
	if err != nil {
		return err
	}
	if !strings.EqualFold(target.Kind, "deployment") {
		return fmt.Errorf("cause %s targets a Deployment; got kind %q", class, target.Kind)
	}
	params, err := parseParams(opts.params)
	if err != nil {
		return err
	}
	expect := fault.Expect{Status: params[expectParam]}
	delete(params, expectParam)
	spec, err := fault.Parse(class, params)
	if err != nil {
		return err
	}
	if expect.Status == "" {
		expect.Status = spec.Definition().Symptom
	}

	kubeconfigPath, err := c.resolveActiveLabKubeconfig(ctx, opts.kubeconfigPath, opts.lab)
	if err != nil {
		return err
	}

	if opts.dryRun {
		printInjectPlan(out, chaos.FaultType(spec.String()), target, map[string]string{expectParam: expect.Status})
		return nil
	}

	timeout := opts.timeout
	if timeout <= 0 {
		timeout = defaultInjectTimeout
	}
	if c.log != nil {
		c.log.Info("applying cause", "class", class, "namespace", target.Namespace, "target", target.Name, "expect", expect.Status)
	}
	started := time.Now()
	err = fault.Inject(ctx, newKube(kubeconfigPath), fault.Target{Namespace: target.Namespace, Name: target.Name}, spec, expect, timeout)
	if err != nil {
		if c.log != nil {
			c.log.Warn("cause not reached", "class", class, "namespace", target.Namespace, "target", target.Name, "error", err)
		}
		return fmt.Errorf("injecting %s into %s/%s: %w", class, target.Namespace, target.Name, err)
	}
	if c.log != nil {
		c.log.Info("symptom reached", "class", class, "namespace", target.Namespace, "target", target.Name, "expect", expect.Status, "duration_ms", time.Since(started).Milliseconds())
	}
	_, _ = fmt.Fprintf(out, "Injected %s against %s/%s; symptom %s reached\n", spec, target.Namespace, target.Name, expect.Status)
	return nil
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
		fmt.Fprintf(&sb, "  params:    %s\n", formatParams(params))
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
