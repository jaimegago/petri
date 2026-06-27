package cli

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaimegago/petri/pkg/chaos"
	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
)

// fakeFault records the arguments Execute was called with so dispatch tests can
// assert exactly one invocation with the expected target and params.
type fakeFault struct {
	ft        chaos.FaultType
	calls     int
	gotTarget chaos.TargetResource
	gotParams map[string]string
	returnErr error
}

func (f *fakeFault) Type() chaos.FaultType { return f.ft }

func (f *fakeFault) Execute(_ context.Context, _ chaos.KubeClient, target chaos.TargetResource, params map[string]string) error {
	f.calls++
	f.gotTarget = target
	f.gotParams = params
	return f.returnErr
}

// fakeKube is an inert chaos.KubeClient; the inject path only ever passes it
// through to Execute, which (in tests) is a fakeFault that ignores it.
type fakeKube struct{ chaos.KubeClient }

func TestParseTarget(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    chaos.TargetResource
		wantErr bool
	}{
		{"valid triple", "apps/Deployment/frontend", chaos.TargetResource{Namespace: "apps", Kind: "Deployment", Name: "frontend"}, false},
		{"empty", "", chaos.TargetResource{}, true},
		{"too few parts", "apps/frontend", chaos.TargetResource{}, true},
		{"too many parts", "apps/Deployment/frontend/extra", chaos.TargetResource{}, true},
		{"empty segment", "apps//frontend", chaos.TargetResource{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseTarget(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("parseTarget(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseParams(t *testing.T) {
	t.Run("valid pairs", func(t *testing.T) {
		got, err := parseParams([]string{"duration=30s", "workers=2"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got["duration"] != "30s" || got["workers"] != "2" {
			t.Errorf("got %+v", got)
		}
	})
	t.Run("value with equals", func(t *testing.T) {
		got, err := parseParams([]string{"selector=app=frontend"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got["selector"] != "app=frontend" {
			t.Errorf("expected first-= split, got %q", got["selector"])
		}
	})
	t.Run("malformed pair", func(t *testing.T) {
		if _, err := parseParams([]string{"bogus"}); err == nil {
			t.Fatal("expected error for pair without '='")
		}
	})
	t.Run("empty key", func(t *testing.T) {
		if _, err := parseParams([]string{"=value"}); err == nil {
			t.Fatal("expected error for empty key")
		}
	})
}

// TestRunInject_UnknownFaultType asserts validation rejects an unrecognized
// type and that the error enumerates the structural accepted set.
func TestRunInject_UnknownFaultType(t *testing.T) {
	c := newTestCLI(state.NewMockManager(), companiesYAML(t))
	opts := &injectOptions{kubeconfigPath: "/tmp/kc", target: "apps/Pod/frontend"}

	err := c.runInject(context.Background(), "not_a_fault", opts,
		chaos.DefaultFaults(), func(string) chaos.KubeClient { return fakeKube{} }, io.Discard)
	if err == nil {
		t.Fatal("expected error for unknown fault type")
	}
	// The error must enumerate the accepted set sourced from DefaultFaults.
	for _, ft := range acceptedFaultTypes(chaos.DefaultFaults()) {
		if !strings.Contains(err.Error(), ft) {
			t.Errorf("error should enumerate accepted type %q; got: %v", ft, err)
		}
	}
}

// TestRunInject_DryRunDoesNotExecute asserts the dry-run path resolves and
// validates everything but never calls Execute or constructs a kube client.
func TestRunInject_DryRunDoesNotExecute(t *testing.T) {
	c := newTestCLI(state.NewMockManager(), companiesYAML(t))
	ff := &fakeFault{ft: chaos.FaultKillPod}
	faults := map[chaos.FaultType]chaos.Fault{chaos.FaultKillPod: ff}

	newKubeCalled := false
	newKube := func(string) chaos.KubeClient {
		newKubeCalled = true
		return fakeKube{}
	}

	opts := &injectOptions{
		kubeconfigPath: "/tmp/kc",
		target:         "apps/Pod/frontend",
		params:         []string{"duration=30s"},
		dryRun:         true,
	}

	var sb strings.Builder
	if err := c.runInject(context.Background(), string(chaos.FaultKillPod), opts, faults, newKube, &sb); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ff.calls != 0 {
		t.Errorf("Execute must not be called on dry-run; calls = %d", ff.calls)
	}
	if newKubeCalled {
		t.Error("kube client must not be constructed on dry-run")
	}
	if !strings.Contains(sb.String(), "DRY RUN") {
		t.Errorf("expected dry-run plan output, got: %q", sb.String())
	}
}

// TestRunInject_DispatchesOnce asserts a successful run calls Execute exactly
// once with the parsed target and params, via the injected fake fault/kube.
func TestRunInject_DispatchesOnce(t *testing.T) {
	c := newTestCLI(state.NewMockManager(), companiesYAML(t))
	ff := &fakeFault{ft: chaos.FaultCPUPressure}
	faults := map[chaos.FaultType]chaos.Fault{chaos.FaultCPUPressure: ff}

	opts := &injectOptions{
		kubeconfigPath: "/tmp/kc",
		target:         "apps/Pod/api",
		params:         []string{"duration=30s", "workers=2"},
	}

	var sb strings.Builder
	if err := c.runInject(context.Background(), string(chaos.FaultCPUPressure), opts,
		faults, func(string) chaos.KubeClient { return fakeKube{} }, &sb); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ff.calls != 1 {
		t.Fatalf("Execute should be called exactly once; calls = %d", ff.calls)
	}
	want := chaos.TargetResource{Namespace: "apps", Kind: "Pod", Name: "api"}
	if ff.gotTarget != want {
		t.Errorf("target = %+v, want %+v", ff.gotTarget, want)
	}
	if ff.gotParams["duration"] != "30s" || ff.gotParams["workers"] != "2" {
		t.Errorf("params = %+v", ff.gotParams)
	}
	if !strings.Contains(sb.String(), "Injected cpu_pressure against apps/api") {
		t.Errorf("expected success confirmation, got: %q", sb.String())
	}
}

// TestRunInject_ActiveLabGuard asserts the shared active-lab guard rejects a
// non-active lab resolved via --lab, before any Execute happens.
func TestRunInject_ActiveLabGuard(t *testing.T) {
	mgr := state.NewMockManager()
	lab := &types.Lab{
		ID:            uuid.New(),
		Name:          "inject-lab",
		Company:       "acme",
		Level:         1,
		CloudProvider: types.CloudProviderLocal,
		Status:        types.LabStatusActive, // lazily transitioned to EXPIRED on read
		CreatedAt:     time.Now().Add(-5 * time.Hour),
		ExpiresAt:     time.Now().Add(-1 * time.Hour),
		TTLHours:      1,
		Metadata: types.LabMetadata{
			Clusters: []types.Cluster{{Name: "inject-lab", KubeconfigPath: "/tmp/kc"}},
		},
	}
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	c := newTestCLI(mgr, companiesYAML(t))
	ff := &fakeFault{ft: chaos.FaultKillPod}
	faults := map[chaos.FaultType]chaos.Fault{chaos.FaultKillPod: ff}

	opts := &injectOptions{lab: "inject-lab", target: "apps/Pod/frontend"}
	err := c.runInject(context.Background(), string(chaos.FaultKillPod), opts,
		faults, func(string) chaos.KubeClient { return fakeKube{} }, io.Discard)
	if err == nil {
		t.Fatal("expected refusal for non-active lab")
	}
	if !strings.Contains(err.Error(), "EXPIRED") {
		t.Errorf("error should mention EXPIRED status; got: %v", err)
	}
	if ff.calls != 0 {
		t.Errorf("Execute must not run when the lab guard fails; calls = %d", ff.calls)
	}
}
