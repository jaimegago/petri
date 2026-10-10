package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jaimegago/petri/pkg/chaos"
	"github.com/jaimegago/petri/pkg/config"
	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/workloadstate"
)

// applyKube is an inert chaos.KubeClient that records applied manifests, the
// one kube call workloadstate.Provision makes.
type applyKube struct {
	chaos.KubeClient
	applied []string
	err     error
}

func (k *applyKube) ApplyYAML(_ context.Context, manifest string) error {
	k.applied = append(k.applied, manifest)
	return k.err
}

// TestRunInjectMode_StateExclusivity asserts --state is exclusive with the
// other modes and refuses the flags it would ignore, before the cluster is
// touched.
func TestRunInjectMode_StateExclusivity(t *testing.T) {
	c := newTestCLI(state.NewMockManager(), companiesYAML(t))
	tests := []struct {
		name    string
		args    []string
		opts    injectOptions
		wantErr string
	}{
		{"state and positional", []string{"kill_pod"}, injectOptions{kubeconfigPath: "/tmp/kc", state: "pending", target: "a/Deployment/b"}, "mutually exclusive"},
		{"state and scenario", nil, injectOptions{kubeconfigPath: "/tmp/kc", state: "pending", scenario: "s.yaml"}, "--scenario and --state are mutually exclusive"},
		{"state with param", nil, injectOptions{kubeconfigPath: "/tmp/kc", state: "pending", target: "a/Deployment/b", params: []string{"k=v"}}, "--state does not take --param"},
		{"state with explicit timeout", nil, injectOptions{kubeconfigPath: "/tmp/kc", state: "pending", target: "a/Deployment/b", timeoutSet: true}, "--timeout"},
		{"image without state", []string{"kill_pod"}, injectOptions{kubeconfigPath: "/tmp/kc", target: "a/Pod/b", image: "x"}, "--image applies only to --state"},
		{"scenario with image", nil, injectOptions{kubeconfigPath: "/tmp/kc", scenario: "s.yaml", image: "x"}, "--scenario does not take --image"},
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

// TestRunInjectState_Validation asserts target shape and the state
// vocabulary are checked before the lab is resolved, and that an unknown
// state's error enumerates the structural accepted set.
func TestRunInjectState_Validation(t *testing.T) {
	c := newTestCLI(state.NewMockManager(), companiesYAML(t))
	tests := []struct {
		name    string
		opts    injectOptions
		wantErr []string
	}{
		{"missing target", injectOptions{kubeconfigPath: "/tmp/kc", state: "pending"}, []string{"--target is required"}},
		{"non-deployment target", injectOptions{kubeconfigPath: "/tmp/kc", state: "pending", target: "a/Pod/b"}, []string{"provisions a Deployment"}},
		{"unknown state", injectOptions{lab: "no-such-lab", state: "oomkiled", target: "a/Deployment/b"}, nil},
	}
	for _, st := range workloadstate.AcceptedStates() {
		tests[2].wantErr = append(tests[2].wantErr, string(st))
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := tt.opts
			err := c.runInjectMode(context.Background(), nil, &opts, chaos.DefaultFaults(), noKubeAllowed(t), io.Discard)
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error missing %q: %v", want, err)
				}
			}
		})
	}
}

// TestRunInjectState_DryRun asserts the plan is printed with the default
// image and no kube client is built.
func TestRunInjectState_DryRun(t *testing.T) {
	c := newTestCLI(state.NewMockManager(), companiesYAML(t))
	var sb strings.Builder
	opts := &injectOptions{kubeconfigPath: "/tmp/kc", state: "pending", target: "apps/Deployment/api", dryRun: true}
	if err := c.runInjectMode(context.Background(), nil, opts, chaos.DefaultFaults(), noKubeAllowed(t), &sb); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	for _, want := range []string{"DRY RUN", "state:     pending", "namespace: apps", "name:      api", config.DefaultOASISImage} {
		if !strings.Contains(sb.String(), want) {
			t.Errorf("plan missing %q:\n%s", want, sb.String())
		}
	}
}

// TestRunInjectState_Provisions asserts a real run applies the rendered
// manifest once through the lab's kube client, honouring --image, and that an
// apply failure exits non-zero.
func TestRunInjectState_Provisions(t *testing.T) {
	c := newTestCLI(state.NewMockManager(), companiesYAML(t))

	t.Run("applies the rendered manifest", func(t *testing.T) {
		kube := &applyKube{}
		var gotKubeconfig string
		newKube := func(path string) chaos.KubeClient {
			gotKubeconfig = path
			return kube
		}
		var sb strings.Builder
		opts := &injectOptions{kubeconfigPath: "/tmp/kc", state: "running", target: "apps/Deployment/api", image: "registry.k8s.io/pause:3.10"}
		if err := c.runInjectMode(context.Background(), nil, opts, chaos.DefaultFaults(), newKube, &sb); err != nil {
			t.Fatalf("provision: %v", err)
		}
		if gotKubeconfig != "/tmp/kc" {
			t.Errorf("kube client built for %q, want /tmp/kc", gotKubeconfig)
		}
		if len(kube.applied) != 1 {
			t.Fatalf("want exactly one apply; got %d", len(kube.applied))
		}
		for _, want := range []string{"kind: Deployment", "name: api", "namespace: apps", "image: registry.k8s.io/pause:3.10"} {
			if !strings.Contains(kube.applied[0], want) {
				t.Errorf("applied manifest missing %q:\n%s", want, kube.applied[0])
			}
		}
		if !strings.Contains(sb.String(), "Provisioned apps/api born into state running") {
			t.Errorf("unexpected output: %q", sb.String())
		}
	})

	t.Run("apply failure exits non-zero", func(t *testing.T) {
		kube := &applyKube{err: errors.New("namespace not found")}
		opts := &injectOptions{kubeconfigPath: "/tmp/kc", state: "pending", target: "apps/Deployment/api"}
		err := c.runInjectMode(context.Background(), nil, opts, chaos.DefaultFaults(), func(string) chaos.KubeClient { return kube }, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "namespace not found") {
			t.Fatalf("want apply error, got %v", err)
		}
	})
}
