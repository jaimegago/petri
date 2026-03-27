package scenarios

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jaimegago/petri/pkg/chaos"
)

// RunnerConfig holds all dependencies required to create a ScenarioRunner.
type RunnerConfig struct {
	// Scenario is the scripted fault sequence to execute.
	Scenario Scenario
	// Faults maps FaultType to implementation. When nil, chaos.DefaultFaults() is used.
	Faults map[chaos.FaultType]chaos.Fault
	// Emitter receives a FaultEvent for every step executed, whether it succeeded or failed.
	Emitter chaos.EventEmitter
	// Kube provides Kubernetes operations for fault execution.
	Kube chaos.KubeClient
	// SteadyState is the client used to wait for Deployment rollouts between steps.
	// If nil, steady-state checks are skipped rather than failing.
	SteadyState steadyStateWaiter
	// Log is the structured logger.
	Log *slog.Logger
}

// ScenarioRunner executes a Scenario step-by-step with the defined timing,
// emitting a FaultEvent for each step through the shared EventEmitter interface.
// Stop the runner by cancelling the context passed to Run.
type ScenarioRunner struct {
	scenario    Scenario
	faults      map[chaos.FaultType]chaos.Fault
	emitter     chaos.EventEmitter
	kube        chaos.KubeClient
	steadyState steadyStateWaiter
	log         *slog.Logger
}

// NewRunner constructs a ScenarioRunner, validating that all fault types referenced
// in the scenario exist in the faults map. It does not validate targets against a
// topology; call Validate separately for that.
func NewRunner(cfg RunnerConfig) (*ScenarioRunner, error) {
	if cfg.Faults == nil {
		cfg.Faults = chaos.DefaultFaults()
	}

	for i, step := range cfg.Scenario.Steps {
		if _, ok := cfg.Faults[step.FaultType]; !ok {
			return nil, fmt.Errorf(
				"scenario %q step[%d] %q references unknown fault type %q",
				cfg.Scenario.Name, i, step.Name, step.FaultType,
			)
		}
	}

	return &ScenarioRunner{
		scenario:    cfg.Scenario,
		faults:      cfg.Faults,
		emitter:     cfg.Emitter,
		kube:        cfg.Kube,
		steadyState: cfg.SteadyState,
		log:         cfg.Log,
	}, nil
}

// Run executes all scenario steps in order and blocks until all steps complete,
// the context is cancelled, or a non-recoverable error occurs. A fault execution
// failure is recorded in the emitted FaultEvent but does not stop the sequence —
// subsequent steps still run. Context cancellation stops the runner immediately
// after the current in-flight step completes.
func (r *ScenarioRunner) Run(ctx context.Context) error {
	r.log.Info("scenario started",
		"scenario", r.scenario.Name,
		"steps", len(r.scenario.Steps),
	)

	for i, step := range r.scenario.Steps {
		if ctx.Err() != nil {
			r.log.Info("scenario cancelled before step",
				"scenario", r.scenario.Name,
				"step", i,
				"step_name", step.Name,
			)
			return ctx.Err()
		}

		r.log.Info("scenario step waiting",
			"scenario", r.scenario.Name,
			"step", i,
			"step_name", step.Name,
			"delay", step.Delay,
		)

		if step.Delay > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(step.Delay):
			}
		}

		if step.SteadyState != nil {
			if err := r.checkSteadyState(ctx, step); err != nil {
				r.log.Warn("steady state check failed; proceeding anyway",
					"scenario", r.scenario.Name,
					"step", i,
					"step_name", step.Name,
					"error", err,
				)
			}
		}

		r.executeStep(ctx, i, step)
	}

	r.log.Info("scenario completed",
		"scenario", r.scenario.Name,
		"steps", len(r.scenario.Steps),
	)
	return nil
}

// executeStep runs a single scenario step and emits the resulting FaultEvent.
func (r *ScenarioRunner) executeStep(ctx context.Context, index int, step Step) {
	fault := r.faults[step.FaultType]

	event := chaos.FaultEvent{
		ID:                uuid.New().String(),
		FaultType:         step.FaultType,
		Target:            step.Target,
		StartedAt:         time.Now(),
		Metadata:          make(map[string]string),
		ScenarioStep:      step.Name,
		ExpectedDiagnosis: step.ExpectedDiagnosis,
	}

	r.log.Info("executing scenario step",
		"scenario", r.scenario.Name,
		"step", index,
		"step_name", step.Name,
		"fault_type", string(step.FaultType),
		"namespace", step.Target.Namespace,
		"target", step.Target.Name,
	)

	params := step.Parameters
	if params == nil {
		params = map[string]string{}
	}

	err := fault.Execute(ctx, r.kube, step.Target, params)
	event.EndedAt = time.Now()

	if err != nil {
		event.Success = false
		event.Error = err.Error()
		r.log.Warn("scenario step fault failed",
			"scenario", r.scenario.Name,
			"step_name", step.Name,
			"fault_type", string(step.FaultType),
			"error", err,
		)
	} else {
		event.Success = true
		r.log.Info("scenario step fault succeeded",
			"scenario", r.scenario.Name,
			"step_name", step.Name,
			"fault_type", string(step.FaultType),
			"duration_ms", event.EndedAt.Sub(event.StartedAt).Milliseconds(),
		)
	}

	r.emitter.Emit(event)
}

// checkSteadyState waits for the Deployment referenced in check to complete its rollout.
// Returns an error if the wait fails or the SteadyState client is nil.
func (r *ScenarioRunner) checkSteadyState(ctx context.Context, step Step) error {
	if r.steadyState == nil {
		return fmt.Errorf("no SteadyState client configured; skipping check for step %q", step.Name)
	}
	check := step.SteadyState
	timeout := check.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	if err := r.steadyState.WaitForRollout(ctx, check.Namespace, check.Deployment, timeout); err != nil {
		return fmt.Errorf("steady state check for %s/%s: %w", check.Namespace, check.Deployment, err)
	}
	return nil
}
