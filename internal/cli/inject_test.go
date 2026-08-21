package cli

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaimegago/petri/pkg/chaos"
	"github.com/jaimegago/petri/pkg/fault"
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

// TestRunInject_CauseCatalog covers the second trigger into pkg/fault: the
// accepted set enumerates both catalogs, a cause validates its parameters
// and target shape before touching the cluster, and dry-run prints the plan
// without constructing a kube client.
func TestRunInject_CauseCatalog(t *testing.T) {
	c := newTestCLI(state.NewMockManager(), companiesYAML(t))
	noKube := func(string) chaos.KubeClient {
		t.Fatal("kube client must not be constructed")
		return nil
	}

	t.Run("unknown type enumerates cause classes too", func(t *testing.T) {
		err := c.runInject(context.Background(), "nope", &injectOptions{kubeconfigPath: "/tmp/kc", target: "a/Deployment/b"},
			chaos.DefaultFaults(), noKube, io.Discard)
		if err == nil || !strings.Contains(err.Error(), string(fault.ClassConfigMissingKey)) {
			t.Fatalf("error should enumerate cause classes; got %v", err)
		}
	})

	t.Run("cause requires a Deployment target", func(t *testing.T) {
		opts := &injectOptions{kubeconfigPath: "/tmp/kc", target: "a/Pod/b", params: []string{"configMap=c", "key=SMTP_PORT"}}
		err := c.runInject(context.Background(), string(fault.ClassConfigMissingKey), opts, chaos.DefaultFaults(), noKube, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "targets a Deployment") {
			t.Fatalf("want Deployment-target error, got %v", err)
		}
	})

	t.Run("cause parameters are validated by the catalog", func(t *testing.T) {
		opts := &injectOptions{kubeconfigPath: "/tmp/kc", target: "a/Deployment/b", params: []string{"configMap=c", "key=SMTP_USER"}}
		err := c.runInject(context.Background(), string(fault.ClassConfigMissingKey), opts, chaos.DefaultFaults(), noKube, io.Discard)
		if err == nil || !strings.Contains(err.Error(), `key "SMTP_USER" is not one`) {
			t.Fatalf("want coverage error, got %v", err)
		}
	})

	t.Run("dry-run prints the plan with the default symptom", func(t *testing.T) {
		var sb strings.Builder
		opts := &injectOptions{kubeconfigPath: "/tmp/kc", target: "default/Deployment/notification-service", params: []string{"configMap=smtp-config", "key=SMTP_PORT"}, dryRun: true}
		if err := c.runInject(context.Background(), string(fault.ClassConfigMissingKey), opts, chaos.DefaultFaults(), noKube, &sb); err != nil {
			t.Fatalf("dry-run: %v", err)
		}
		for _, want := range []string{"DRY RUN", "config.missing-key configMap=smtp-config key=SMTP_PORT", "expect=CrashLoopBackOff", "notification-service"} {
			if !strings.Contains(sb.String(), want) {
				t.Errorf("plan missing %q:\n%s", want, sb.String())
			}
		}
	})

	t.Run("expect param overrides the symptom and is not a fault parameter", func(t *testing.T) {
		var sb strings.Builder
		opts := &injectOptions{kubeconfigPath: "/tmp/kc", target: "default/Deployment/n", params: []string{"configMap=smtp-config", "key=SMTP_PORT", "expect=Error"}, dryRun: true}
		if err := c.runInject(context.Background(), string(fault.ClassConfigMissingKey), opts, chaos.DefaultFaults(), noKube, &sb); err != nil {
			t.Fatalf("dry-run: %v", err)
		}
		if !strings.Contains(sb.String(), "expect=Error") {
			t.Errorf("plan should carry the overridden symptom:\n%s", sb.String())
		}
	})
}

// TestInjectCatalogsAreDisjoint guards the single CLI over two catalogs: a
// name in both would be dispatched as chaos and never reach pkg/fault.
func TestInjectCatalogsAreDisjoint(t *testing.T) {
	chaosTypes := chaos.DefaultFaults()
	for _, class := range fault.Classes() {
		if _, clash := chaosTypes[chaos.FaultType(class)]; clash {
			t.Errorf("%q is both a chaos fault type and a cause class", class)
		}
	}
}
