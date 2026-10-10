package workloadstate

import (
	"testing"

	"github.com/jaimegago/petri/pkg/fault"
)

// Every recognised state must say what it converges to, or provision has
// nothing to wait for and hands the workload over while it is still starting.
func TestConvergence_EveryAcceptedStateConverges(t *testing.T) {
	t.Parallel()
	for _, st := range AcceptedStates() {
		c, err := baseSpec("svc", string(st)).Convergence()
		if err != nil {
			t.Errorf("state %q: %v", st, err)
			continue
		}
		if c.RollsOut == (c.Symptom != "") {
			t.Errorf("state %q: exactly one of RollsOut and Symptom must be set, got %+v", st, c)
		}
	}
}

func TestConvergence(t *testing.T) {
	t.Parallel()
	withEnv := baseSpec("svc", "CrashLoopBackOff")
	withEnv.Env = []EnvVar{{Name: "SMTP_PORT", ConfigMapKeyRef: &ConfigMapKeySelector{Name: "cfg", Key: "SMTP_PORT"}}}
	faulted := baseSpec("svc", "oomkilled")
	faulted.Fault = &fault.Spec{Class: fault.ClassConfigUnboundedCache}

	tests := []struct {
		name string
		spec Spec
		want Convergence
	}{
		// The omitted state renders healthy, so it must be waited on as one.
		{"omitted", baseSpec("svc", ""), Convergence{RollsOut: true}},
		{"running, any case", baseSpec("svc", " Running "), Convergence{RollsOut: true}},
		{"elevated error rate alias", baseSpec("svc", "elevated-error-rate"), Convergence{RollsOut: true}},
		{"crashloop", baseSpec("svc", "CrashLoopBackOff"), Convergence{Symptom: fault.SymptomCrashLoopBackOff}},
		{"crashloop with declared env", withEnv, Convergence{Symptom: fault.SymptomCreateContainerConfigError}},
		{"oomkilled", baseSpec("svc", "OOMKilled"), Convergence{Symptom: fault.SymptomOOMKilled}},
		{"pending", baseSpec("svc", "pending"), Convergence{Symptom: fault.SymptomUnschedulable}},
		{"degraded", baseSpec("svc", "degraded"), Convergence{Symptom: fault.SymptomRunning}},
		{"error", baseSpec("svc", "error"), Convergence{Symptom: fault.SymptomImagePullBackOff}},
		{"faulted", faulted, Convergence{Symptom: fault.SymptomOOMKilled}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := tt.spec.Convergence()
			if err != nil {
				t.Fatalf("Convergence() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Convergence() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestConvergence_UnrecognizedStateIsError(t *testing.T) {
	t.Parallel()
	if _, err := baseSpec("svc", "haunted").Convergence(); err == nil {
		t.Fatal("expected an error for an unrecognized state")
	}
}
