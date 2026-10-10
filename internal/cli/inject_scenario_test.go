package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jaimegago/petri/pkg/chaos"
	"github.com/jaimegago/petri/pkg/state"
)

// rolloutKube is an inert chaos.KubeClient that records steady-state waits,
// the one kube call the ScenarioRunner itself makes.
type rolloutKube struct {
	chaos.KubeClient
	rollouts []string
}

func (k *rolloutKube) WaitForRollout(_ context.Context, namespace, deployment string, _ time.Duration) error {
	k.rollouts = append(k.rollouts, namespace+"/"+deployment)
	return nil
}

const twoStepScenario = `name: db-cascade
steps:
  - name: kill-db
    fault_type: kill_pod
    target: {namespace: data, kind: Pod, name: postgres}
  - name: restart-api
    fault_type: restart_deployment
    target: {namespace: apps, kind: Deployment, name: api}
    parameters: {reason: drill}
    steady_state: {namespace: apps, deployment: api, timeout: 1s}
`

func writeScenario(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scenario.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func noKubeAllowed(t *testing.T) func(string) chaos.KubeClient {
	return func(string) chaos.KubeClient {
		t.Fatal("kube client must not be constructed")
		return nil
	}
}

// TestRunInjectMode_Exclusivity asserts ambiguous mode combinations are
// refused before any lab resolution or cluster access.
func TestRunInjectMode_Exclusivity(t *testing.T) {
	c := newTestCLI(state.NewMockManager(), companiesYAML(t))
	path := writeScenario(t, twoStepScenario)

	tests := []struct {
		name    string
		args    []string
		opts    injectOptions
		wantErr string
	}{
		{"no mode", nil, injectOptions{kubeconfigPath: "/tmp/kc"}, "one of a fault type, --scenario"},
		{"positional and scenario", []string{"kill_pod"}, injectOptions{kubeconfigPath: "/tmp/kc", scenario: path}, "mutually exclusive"},
		{"scenario with target", nil, injectOptions{kubeconfigPath: "/tmp/kc", scenario: path, target: "a/Pod/b"}, "--scenario does not take --target"},
		{"scenario with param", nil, injectOptions{kubeconfigPath: "/tmp/kc", scenario: path, params: []string{"k=v"}}, "--param"},
		{"scenario with explicit timeout", nil, injectOptions{kubeconfigPath: "/tmp/kc", scenario: path, timeoutSet: true}, "--timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := tt.opts
			err := c.runInjectMode(context.Background(), tt.args, &opts, chaos.DefaultFaults(), noKubeAllowed(t), io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

// TestRunInjectScenario_DryRun asserts the scenario is loaded, validated and
// the lab resolved, then the plan printed without constructing a kube client
// or executing any fault.
func TestRunInjectScenario_DryRun(t *testing.T) {
	c := newTestCLI(state.NewMockManager(), companiesYAML(t))
	kill := &fakeFault{ft: chaos.FaultKillPod}
	restart := &fakeFault{ft: chaos.FaultRestartDeployment}
	faults := map[chaos.FaultType]chaos.Fault{kill.ft: kill, restart.ft: restart}

	var sb strings.Builder
	opts := &injectOptions{kubeconfigPath: "/tmp/kc", scenario: writeScenario(t, twoStepScenario), dryRun: true}
	if err := c.runInjectMode(context.Background(), nil, opts, faults, noKubeAllowed(t), &sb); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if kill.calls+restart.calls != 0 {
		t.Errorf("no fault may execute on dry-run")
	}
	for _, want := range []string{"DRY RUN", `"db-cascade" (2 steps)`, "kill-db: kill_pod against data/Pod/postgres", "params: reason=drill", "steady state: apps/api"} {
		if !strings.Contains(sb.String(), want) {
			t.Errorf("plan missing %q:\n%s", want, sb.String())
		}
	}
}

// TestRunInjectScenario_Invalid asserts load and validation failures stop the
// mode before the cluster is touched.
func TestRunInjectScenario_Invalid(t *testing.T) {
	c := newTestCLI(state.NewMockManager(), companiesYAML(t))

	t.Run("missing file", func(t *testing.T) {
		opts := &injectOptions{kubeconfigPath: "/tmp/kc", scenario: filepath.Join(t.TempDir(), "absent.yaml")}
		if err := c.runInjectMode(context.Background(), nil, opts, chaos.DefaultFaults(), noKubeAllowed(t), io.Discard); err == nil {
			t.Fatal("expected error for a missing scenario file")
		}
	})

	t.Run("unknown fault type", func(t *testing.T) {
		body := "name: bad\nsteps:\n  - name: s\n    fault_type: not_a_fault\n    target: {namespace: a, kind: Pod, name: b}\n"
		opts := &injectOptions{kubeconfigPath: "/tmp/kc", scenario: writeScenario(t, body)}
		err := c.runInjectMode(context.Background(), nil, opts, chaos.DefaultFaults(), noKubeAllowed(t), io.Discard)
		if err == nil || !strings.Contains(err.Error(), `unknown fault_type "not_a_fault"`) {
			t.Fatalf("want validation error, got %v", err)
		}
	})

	t.Run("active-lab guard", func(t *testing.T) {
		opts := &injectOptions{lab: "no-such-lab", scenario: writeScenario(t, twoStepScenario)}
		if err := c.runInjectMode(context.Background(), nil, opts, chaos.DefaultFaults(), noKubeAllowed(t), io.Discard); err == nil {
			t.Fatal("expected the lab guard to refuse an unknown lab")
		}
	})
}

// TestRunInjectScenario_Executes asserts every step runs in order through the
// ScenarioRunner, the steady-state check uses the lab's kube client, and a
// failed step makes the command exit non-zero without stopping the sequence.
func TestRunInjectScenario_Executes(t *testing.T) {
	c := newTestCLI(state.NewMockManager(), companiesYAML(t))
	kill := &fakeFault{ft: chaos.FaultKillPod, returnErr: errors.New("boom")}
	restart := &fakeFault{ft: chaos.FaultRestartDeployment}
	faults := map[chaos.FaultType]chaos.Fault{kill.ft: kill, restart.ft: restart}
	kube := &rolloutKube{}

	var gotKubeconfig string
	newKube := func(path string) chaos.KubeClient {
		gotKubeconfig = path
		return kube
	}

	var sb strings.Builder
	opts := &injectOptions{kubeconfigPath: "/tmp/kc", scenario: writeScenario(t, twoStepScenario)}
	err := c.runInjectMode(context.Background(), nil, opts, faults, newKube, &sb)
	if err == nil || !strings.Contains(err.Error(), "1 of 2 steps failed") {
		t.Fatalf("want a step-failure error, got %v", err)
	}
	if gotKubeconfig != "/tmp/kc" {
		t.Errorf("kube client built for %q, want /tmp/kc", gotKubeconfig)
	}
	if kill.calls != 1 || restart.calls != 1 {
		t.Errorf("each step should execute once; kill=%d restart=%d", kill.calls, restart.calls)
	}
	if restart.gotParams["reason"] != "drill" {
		t.Errorf("step parameters not passed through: %+v", restart.gotParams)
	}
	if len(kube.rollouts) != 1 || kube.rollouts[0] != "apps/api" {
		t.Errorf("steady-state check should wait on apps/api; got %v", kube.rollouts)
	}
	for _, want := range []string{"FAIL  kill-db", "boom", "ok    restart-api", "1 of 2 steps succeeded"} {
		if !strings.Contains(sb.String(), want) {
			t.Errorf("outcome missing %q:\n%s", want, sb.String())
		}
	}
}
