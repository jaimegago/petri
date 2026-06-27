package workloadstate

import (
	"context"
	"strings"
	"testing"
)

// baseSpec returns a minimal valid spec for the given state.
func baseSpec(name, state string) Spec {
	return Spec{
		Name:      name,
		Namespace: "test-ns",
		Replicas:  1,
		Image:     "registry.k8s.io/nginx-slim:0.27",
		State:     state,
		UtilImage: DefaultUtilImage,
	}
}

func TestRender_RecognizedStates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		spec    Spec
		want    []string // substrings that must appear
		notWant []string // substrings that must not appear
	}{
		{
			name: "running is healthy with image and port 80",
			spec: func() Spec {
				s := baseSpec("frontend", "running")
				s.Replicas = 2
				s.Image = "nginx:1.25"
				return s
			}(),
			want:    []string{"nginx:1.25", "replicas: 2", "containerPort: 80"},
			notWant: []string{"exit 1", "readinessProbe"},
		},
		{
			name:    "empty state resolves to running",
			spec:    baseSpec("web", ""),
			want:    []string{"registry.k8s.io/nginx-slim:0.27", "containerPort: 80"},
			notWant: []string{"exit 1", "memory: 4Mi"},
		},
		{
			name:    "whitespace-only state resolves to running",
			spec:    baseSpec("web", "   "),
			want:    []string{"containerPort: 80"},
			notWant: []string{"exit 1"},
		},
		{
			name:    "crashloopbackoff exit-1 on util image",
			spec:    baseSpec("broken", "CrashLoopBackOff"),
			want:    []string{"exit 1", "registry.k8s.io/e2e-test-images/busybox"},
			notWant: []string{"containerPort: 80"},
		},
		{
			name: "crashloopbackoff with configMapRef references missing key",
			spec: func() Spec {
				s := baseSpec("api", "crashloopbackoff")
				s.ConfigMapRef = "smtp-config"
				return s
			}(),
			want:    []string{"smtp-config", "__petri_missing_key__", "optional: false"},
			notWant: []string{"exit 1"},
		},
		{
			name:    "oomkilled has low memory limit on util image",
			spec:    baseSpec("oom", "OOMKilled"),
			want:    []string{"memory: 4Mi", "dd if=/dev/zero", "registry.k8s.io/e2e-test-images/busybox"},
			notWant: []string{"containerPort: 80"},
		},
		{
			name:    "pending has impossible cpu request",
			spec:    baseSpec("stuck", "pending"),
			want:    []string{"cpu: \"100\"", "requests:"},
			notWant: []string{"readinessProbe"},
		},
		{
			name: "degraded has readiness probe and respects 3 replicas",
			spec: func() Spec {
				s := baseSpec("flaky", "degraded")
				s.Replicas = 3
				return s
			}(),
			want:    []string{"readinessProbe", "/healthz", "replicas: 3"},
			notWant: nil,
		},
		{
			name:    "degraded enforces minimum 2 replicas",
			spec:    baseSpec("flaky-single", "degraded"),
			want:    []string{"replicas: 2"},
			notWant: []string{"replicas: 1"},
		},
		{
			name: "elevated_error_rate runs python server with configured rate",
			spec: func() Spec {
				s := baseSpec("errs", "elevated_error_rate")
				s.ErrorRate = 30
				return s
			}(),
			want:    []string{"python:3-alpine", "<=30", "send_response(500)", "containerPort: 8080"},
			notWant: nil,
		},
		{
			name:    "elevated-error-rate alias is accepted",
			spec:    baseSpec("errs", "elevated-error-rate"),
			want:    []string{"python:3-alpine", "send_response(500)"},
			notWant: nil,
		},
		{
			name:    "elevated_error_rate defaults rate to 50 when unset",
			spec:    baseSpec("errs", "elevated_error_rate"),
			want:    []string{"<=50"},
			notWant: nil,
		},
		{
			name:    "error uses invalid image",
			spec:    baseSpec("bad", "error"),
			want:    []string{"invalid-registry.example.com/does-not-exist"},
			notWant: []string{"containerPort: 80"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Render(tt.spec)
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			// Every state produces a Deployment in the requested namespace.
			if !strings.Contains(got, "kind: Deployment") {
				t.Errorf("manifest is not a Deployment:\n%s", got)
			}
			if !strings.Contains(got, "namespace: test-ns") {
				t.Errorf("manifest missing namespace:\n%s", got)
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("manifest missing %q:\n%s", w, got)
				}
			}
			for _, nw := range tt.notWant {
				if strings.Contains(got, nw) {
					t.Errorf("manifest must not contain %q:\n%s", nw, got)
				}
			}
		})
	}
}

func TestRender_UnrecognizedStateIsError(t *testing.T) {
	t.Parallel()

	_, err := Render(baseSpec("mystery-svc", "haunted"))
	if err == nil {
		t.Fatal("expected error for unrecognized state, got nil")
	}
	// The error must name the resource, the offending value, and enumerate the
	// accepted states.
	for _, want := range []string{"mystery-svc", "haunted", "running", "crashloopbackoff", "oomkilled"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q", err.Error(), want)
		}
	}
}

func TestRender_OmittedStateYieldsRunning(t *testing.T) {
	t.Parallel()

	// An omitted state must produce a healthy deployment identical to an
	// explicit "running" request (behavior unchanged from the OASIS default).
	omitted, err := Render(baseSpec("svc", ""))
	if err != nil {
		t.Fatalf("Render(omitted) error = %v", err)
	}
	explicit, err := Render(baseSpec("svc", "running"))
	if err != nil {
		t.Fatalf("Render(running) error = %v", err)
	}
	if omitted != explicit {
		t.Errorf("omitted state should render identically to running:\nomitted:\n%s\nexplicit:\n%s", omitted, explicit)
	}
	if strings.Contains(omitted, "exit 1") || strings.Contains(omitted, "memory: 4Mi") {
		t.Errorf("omitted-state deployment must be healthy:\n%s", omitted)
	}
}

func TestRender_DefaultMatchLabels(t *testing.T) {
	t.Parallel()

	got, err := Render(Spec{Name: "svc", Namespace: "ns", Image: "img:1"})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if !strings.Contains(got, `app: "svc"`) {
		t.Errorf("default selector should be app=svc:\n%s", got)
	}
}

func TestAcceptedStates(t *testing.T) {
	t.Parallel()

	states := AcceptedStates()
	if len(states) == 0 {
		t.Fatal("AcceptedStates() returned empty set")
	}
	// Every accepted state must round-trip through Render without error.
	for _, st := range states {
		s := baseSpec("svc", string(st))
		if _, err := Render(s); err != nil {
			t.Errorf("accepted state %q failed to render: %v", st, err)
		}
	}
	// Mutating the returned slice must not affect the package's source of truth.
	states[0] = "tampered"
	if AcceptedStates()[0] == "tampered" {
		t.Error("AcceptedStates() must return a defensive copy")
	}
}

// recordingKube captures applied manifests for Provision tests.
type recordingKube struct {
	applied []string
	err     error
}

func (r *recordingKube) ApplyYAML(_ context.Context, manifest string) error {
	if r.err != nil {
		return r.err
	}
	r.applied = append(r.applied, manifest)
	return nil
}

func TestProvision_AppliesRenderedManifest(t *testing.T) {
	t.Parallel()

	kube := &recordingKube{}
	if err := Provision(context.Background(), kube, baseSpec("web", "running")); err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	if len(kube.applied) != 1 {
		t.Fatalf("expected 1 applied manifest, got %d", len(kube.applied))
	}
	if !strings.Contains(kube.applied[0], "kind: Deployment") {
		t.Errorf("applied manifest is not a Deployment:\n%s", kube.applied[0])
	}
}

func TestProvision_UnrecognizedStateDoesNotApply(t *testing.T) {
	t.Parallel()

	kube := &recordingKube{}
	err := Provision(context.Background(), kube, baseSpec("web", "nonsense"))
	if err == nil {
		t.Fatal("expected error for unrecognized state, got nil")
	}
	if len(kube.applied) != 0 {
		t.Errorf("nothing should be applied on validation failure, got %d manifests", len(kube.applied))
	}
}
