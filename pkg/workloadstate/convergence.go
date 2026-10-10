package workloadstate

import (
	"fmt"

	"github.com/jaimegago/petri/pkg/fault"
)

// Convergence is what a workload provisioned from a Spec looks like once it
// exhibits the state it was born into — what a caller waits for before it
// hands the workload over. A workload that has been applied but not yet
// converged is still starting, and reads as neither healthy nor as the
// declared failure. Exactly one field is set.
type Convergence struct {
	// RollsOut means the state is healthy: the Deployment's rollout completes,
	// every replica Ready.
	RollsOut bool
	// Symptom is, for an intentionally unhealthy state, the status every pod
	// exhibits once the state is reached, as fault.WaitForSymptom matches it.
	// A rollout would never complete for these states, which is why they are
	// waited on by symptom instead.
	Symptom string
}

// Convergence returns what the workload Render produces for s looks like once
// it has reached its state. It follows the renderer, not the state's name: a
// crashloopbackoff with a declared environment is stopped by the kubelet on a
// required reference rather than crashing, and converges to that. A faulted
// spec converges to its class's symptom; a caller holding a declared expect
// waits on that instead, as it is at least as strict.
//
// A bare Pod has no rollout: where its Deployment counterpart would roll
// out, it converges when its container is running. A combination Render
// refuses is refused here with the same error.
func (s Spec) Convergence() (Convergence, error) {
	kind, _, err := s.resolve()
	if err != nil {
		return Convergence{}, err
	}
	c, err := s.deploymentConvergence()
	if err != nil {
		return Convergence{}, err
	}
	if kind == KindPod && c.RollsOut {
		return Convergence{Symptom: fault.SymptomRunning}, nil
	}
	return c, nil
}

// deploymentConvergence is Convergence for the Deployment Render produces.
func (s Spec) deploymentConvergence() (Convergence, error) {
	state, err := s.normalizeState()
	if err != nil {
		return Convergence{}, err
	}
	if s.Fault != nil {
		return Convergence{Symptom: s.Fault.Definition().Symptom}, nil
	}
	switch state {
	case StateRunning, StateElevatedErrorRate:
		// elevated_error_rate's pods are Ready: the 5xx is in the responses,
		// not in the pod.
		return Convergence{RollsOut: true}, nil
	case StateCrashLoopBackOff:
		if len(s.Env) > 0 {
			return Convergence{Symptom: fault.SymptomCreateContainerConfigError}, nil
		}
		return Convergence{Symptom: fault.SymptomCrashLoopBackOff}, nil
	case StateOOMKilled:
		return Convergence{Symptom: fault.SymptomOOMKilled}, nil
	case StatePending:
		return Convergence{Symptom: fault.SymptomUnschedulable}, nil
	case StateDegraded:
		// The containers run; the failing readiness probe keeps them out of
		// Ready, so the rollout never completes.
		return Convergence{Symptom: fault.SymptomRunning}, nil
	case StateError:
		return Convergence{Symptom: fault.SymptomImagePullBackOff}, nil
	default:
		// Unreachable: normalizeState only returns recognised states or err.
		return Convergence{}, fmt.Errorf("workload %q: unhandled state %q", s.Name, state)
	}
}
