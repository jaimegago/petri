package cli

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"

	"github.com/jaimegago/petri/pkg/chaos"
	"github.com/jaimegago/petri/pkg/scenarios"
)

// runInjectScenario is the scenario mode of inject: it loads a scripted fault
// sequence with scenarios.LoadFile, validates it against the chaos catalog,
// resolves the lab through the shared active-lab guard, and replays it through
// scenarios.ScenarioRunner. The runner keeps going when a step fails, so the
// outcome of every step is collected and any failure makes the command exit
// non-zero.
func (c *CLI) runInjectScenario(
	ctx context.Context,
	opts *injectOptions,
	faults map[chaos.FaultType]chaos.Fault,
	newKube func(kubeconfigPath string) chaos.KubeClient,
	out io.Writer,
) error {
	scenario, err := scenarios.LoadFile(opts.scenario)
	if err != nil {
		return err
	}
	// No topology is known for an arbitrary lab, so targets are checked for
	// shape only; each fault resolves and validates its own target.
	if err := scenarios.Validate(scenario, faults, nil); err != nil {
		return err
	}

	kubeconfigPath, err := c.resolveActiveLabKubeconfig(ctx, opts.kubeconfigPath, opts.lab)
	if err != nil {
		return err
	}

	if opts.dryRun {
		printScenarioPlan(out, scenario)
		return nil
	}

	kube := newKube(kubeconfigPath)
	events := &collectingEmitter{}
	log := c.log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	runner, err := scenarios.NewRunner(scenarios.RunnerConfig{
		Scenario:    *scenario,
		Faults:      faults,
		Emitter:     events,
		Kube:        kube,
		SteadyState: kube,
		Log:         log,
	})
	if err != nil {
		return err
	}

	runErr := runner.Run(ctx)
	failed := printScenarioOutcome(out, scenario, events.events)
	if runErr != nil {
		return fmt.Errorf("scenario %q stopped after %d of %d steps: %w",
			scenario.Name, len(events.events), len(scenario.Steps), runErr)
	}
	if failed > 0 {
		return fmt.Errorf("scenario %q: %d of %d steps failed", scenario.Name, failed, len(scenario.Steps))
	}
	return nil
}

// collectingEmitter records every FaultEvent the runner emits. Run is
// synchronous, so Emit is never called concurrently and never blocks.
type collectingEmitter struct {
	events []chaos.FaultEvent
}

func (e *collectingEmitter) Emit(event chaos.FaultEvent) {
	e.events = append(e.events, event)
}

// printScenarioPlan renders what a scenario dry-run would have executed.
func printScenarioPlan(out io.Writer, s *scenarios.Scenario) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "DRY RUN — would replay scenario %q (%d steps):\n", s.Name, len(s.Steps))
	for i, step := range s.Steps {
		fmt.Fprintf(&sb, "  %d. %s: %s against %s/%s/%s",
			i+1, step.Name, step.FaultType, step.Target.Namespace, step.Target.Kind, step.Target.Name)
		if step.Delay > 0 {
			fmt.Fprintf(&sb, " after %s", step.Delay)
		}
		sb.WriteString("\n")
		if len(step.Parameters) > 0 {
			fmt.Fprintf(&sb, "     params: %s\n", formatParams(step.Parameters))
		}
		if ss := step.SteadyState; ss != nil {
			fmt.Fprintf(&sb, "     steady state: %s/%s rolled out first\n", ss.Namespace, ss.Deployment)
		}
	}
	_, _ = io.WriteString(out, sb.String())
}

// printScenarioOutcome reports each executed step and returns how many failed.
func printScenarioOutcome(out io.Writer, s *scenarios.Scenario, events []chaos.FaultEvent) int {
	failed := 0
	var sb strings.Builder
	fmt.Fprintf(&sb, "Scenario %q:\n", s.Name)
	for _, ev := range events {
		if ev.Success {
			fmt.Fprintf(&sb, "  ok    %s: %s against %s/%s\n", ev.ScenarioStep, ev.FaultType, ev.Target.Namespace, ev.Target.Name)
			continue
		}
		failed++
		fmt.Fprintf(&sb, "  FAIL  %s: %s against %s/%s: %s\n", ev.ScenarioStep, ev.FaultType, ev.Target.Namespace, ev.Target.Name, ev.Error)
	}
	fmt.Fprintf(&sb, "%d of %d steps succeeded\n", len(events)-failed, len(s.Steps))
	_, _ = io.WriteString(out, sb.String())
	return failed
}

// formatParams renders a parameter map as sorted key=value pairs.
func formatParams(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+params[k])
	}
	return strings.Join(parts, " ")
}
