package scenarios

import (
	"fmt"
	"strings"

	"github.com/jaimegago/petri/pkg/chaos"
)

// ValidationError aggregates all problems found during scenario validation.
// It implements the error interface.
type ValidationError struct {
	// Scenario is the name of the scenario that failed validation.
	Scenario string
	// Problems is the list of individual validation failures.
	Problems []string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("scenario %q has %d validation error(s):\n  - %s",
		e.Scenario, len(e.Problems), strings.Join(e.Problems, "\n  - "))
}

// Validate checks that all referenced fault types in s are present in the faults
// map, and that all step targets exist in the provided topology slice.
//
// faults should be the same map passed to scenarios.NewRunner (typically
// chaos.DefaultFaults() optionally extended with custom implementations).
//
// topology is the set of known TargetResources for the lab being tested.
// A step target is considered valid if a topology entry matches on all three
// fields: Namespace, Name, and Kind.
//
// Returns a *ValidationError listing all problems, or nil when the scenario is valid.
func Validate(s *Scenario, faults map[chaos.FaultType]chaos.Fault, topology []chaos.TargetResource) error {
	var problems []string

	if s.Name == "" {
		problems = append(problems, "scenario name is empty")
	}
	if len(s.Steps) == 0 {
		problems = append(problems, "scenario has no steps")
	}

	topologySet := make(map[string]bool, len(topology))
	for _, tr := range topology {
		topologySet[topologyKey(tr)] = true
	}

	for i, step := range s.Steps {
		prefix := fmt.Sprintf("step[%d] %q", i, step.Name)

		if step.Name == "" {
			problems = append(problems, fmt.Sprintf("step[%d]: name is empty", i))
		}
		if step.FaultType == "" {
			problems = append(problems, fmt.Sprintf("%s: fault_type is empty", prefix))
		} else if _, ok := faults[step.FaultType]; !ok {
			problems = append(problems, fmt.Sprintf("%s: unknown fault_type %q", prefix, step.FaultType))
		}

		if step.Target.Namespace == "" {
			problems = append(problems, fmt.Sprintf("%s: target.namespace is empty", prefix))
		}
		if step.Target.Name == "" {
			problems = append(problems, fmt.Sprintf("%s: target.name is empty", prefix))
		}
		if step.Target.Kind == "" {
			problems = append(problems, fmt.Sprintf("%s: target.kind is empty", prefix))
		}

		if len(topology) > 0 && !topologySet[topologyKey(step.Target)] {
			problems = append(problems, fmt.Sprintf(
				"%s: target {namespace:%q name:%q kind:%q} not found in lab topology",
				prefix, step.Target.Namespace, step.Target.Name, step.Target.Kind,
			))
		}

		if step.SteadyState != nil {
			if step.SteadyState.Deployment == "" {
				problems = append(problems, fmt.Sprintf("%s: steady_state.deployment is empty", prefix))
			}
			if step.SteadyState.Namespace == "" {
				problems = append(problems, fmt.Sprintf("%s: steady_state.namespace is empty", prefix))
			}
		}
	}

	if len(problems) > 0 {
		return &ValidationError{Scenario: s.Name, Problems: problems}
	}
	return nil
}

// topologyKey returns a unique string key for a TargetResource used in set membership tests.
func topologyKey(tr chaos.TargetResource) string {
	return tr.Namespace + "/" + tr.Kind + "/" + tr.Name
}
