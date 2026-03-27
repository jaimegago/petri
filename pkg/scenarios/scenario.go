// Package scenarios provides deterministic, scripted fault sequences for Petri labs.
// Unlike the chaos package (random, non-deterministic), scenarios execute an ordered
// list of steps with precise timing and optional expected-diagnosis metadata so that
// Joe's responses can be evaluated for correctness.
//
// Scenarios are defined in YAML files that can be version-controlled and shared.
// Use LoadFile to parse a scenario, Validate to check it against a running lab
// topology, and NewRunner to execute it.
package scenarios

import (
	"context"
	"time"

	"github.com/jaimegago/petri/pkg/chaos"
)

// SteadyStateCheck describes a Kubernetes Deployment rollout to verify before
// executing the next step in a scenario. The step proceeds only when the deployment
// reports a fully rolled-out condition within the given Timeout.
type SteadyStateCheck struct {
	// Deployment is the name of the Deployment to wait for.
	Deployment string `yaml:"deployment" json:"deployment"`
	// Namespace is the Kubernetes namespace containing the Deployment.
	Namespace string `yaml:"namespace" json:"namespace"`
	// Timeout is the maximum time to wait for steady state.
	// Defaults to 2 minutes when zero.
	Timeout time.Duration `yaml:"timeout" json:"timeout"`
}

// Step is a single fault-injection action within a Scenario.
type Step struct {
	// Name is a human-readable identifier for the step, used in log output and
	// in emitted FaultEvents via the ScenarioStep field.
	Name string `yaml:"name" json:"name"`
	// FaultType is the fault to inject. Must be a registered chaos.FaultType.
	FaultType chaos.FaultType `yaml:"fault_type" json:"fault_type"`
	// Target is the Kubernetes resource to inject the fault into.
	Target chaos.TargetResource `yaml:"target" json:"target"`
	// Parameters carries fault-specific configuration (same semantics as chaos.FaultSpec.Parameters).
	Parameters map[string]string `yaml:"parameters,omitempty" json:"parameters,omitempty"`
	// Delay is how long to wait before executing this step. The wait begins
	// after the previous step (or after Run is called for the first step) completes.
	Delay time.Duration `yaml:"delay" json:"delay"`
	// SteadyState is an optional check that must pass before fault injection begins.
	// The check runs after Delay elapses but before the fault is executed.
	SteadyState *SteadyStateCheck `yaml:"steady_state,omitempty" json:"steady_state,omitempty"`
	// ExpectedDiagnosis describes what Joe (the AI copilot) should detect and report
	// after this fault fires. ScenarioRunner preserves this in each emitted FaultEvent
	// without acting on it; it is used downstream by the instrumentation.Correlator.
	ExpectedDiagnosis string `yaml:"expected_diagnosis,omitempty" json:"expected_diagnosis,omitempty"`
}

// Scenario is an ordered collection of fault-injection steps that together represent
// a realistic failure situation for a running lab. Scenarios are version-controlled
// YAML files; see LoadFile and Validate.
type Scenario struct {
	// Name is the unique identifier for this scenario (e.g. "database-cascade-failure").
	Name string `yaml:"name" json:"name"`
	// Description provides human-readable context for the scenario.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	// Steps are executed sequentially in the order they appear.
	Steps []Step `yaml:"steps" json:"steps"`
}

// steadyStateWaiter can wait for a Kubernetes Deployment rollout to complete.
// It is a narrow interface so tests can inject a mock without pulling in the
// full kubectl.Client.
type steadyStateWaiter interface {
	WaitForRollout(ctx context.Context, namespace, deployment string, timeout time.Duration) error
}
