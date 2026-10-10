package workloadstate

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/jaimegago/petri/pkg/fault"
)

// podSpec returns a minimal bare-pod spec for the given state.
func podSpec(name, state string) Spec {
	s := baseSpec(name, state)
	s.Kind = KindPod
	return s
}

// renderedPod is the part of a rendered Pod the tests read back.
type renderedPod struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name      string            `yaml:"name"`
		Namespace string            `yaml:"namespace"`
		Labels    map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	Spec struct {
		NodeName   string `yaml:"nodeName"`
		Containers []struct {
			Name      string   `yaml:"name"`
			Image     string   `yaml:"image"`
			Command   []string `yaml:"command"`
			Resources struct {
				Limits   map[string]string `yaml:"limits"`
				Requests map[string]string `yaml:"requests"`
			} `yaml:"resources"`
			Env []struct {
				Name      string `yaml:"name"`
				ValueFrom struct {
					SecretKeyRef struct {
						Name string `yaml:"name"`
						Key  string `yaml:"key"`
					} `yaml:"secretKeyRef"`
				} `yaml:"valueFrom"`
			} `yaml:"env"`
		} `yaml:"containers"`
	} `yaml:"spec"`
}

func parsePod(t *testing.T, manifest string) renderedPod {
	t.Helper()
	var p renderedPod
	if err := yaml.Unmarshal([]byte(manifest), &p); err != nil {
		t.Fatalf("rendered pod is not valid YAML: %v\n%s", err, manifest)
	}
	if p.APIVersion != "v1" || p.Kind != "Pod" {
		t.Fatalf("rendered %s/%s, want v1/Pod:\n%s", p.APIVersion, p.Kind, manifest)
	}
	if len(p.Spec.Containers) != 1 || p.Spec.Containers[0].Name != "app" {
		t.Fatalf("want exactly one container named app:\n%s", manifest)
	}
	return p
}

// A bare pod is born into its declared state exactly as a Deployment's pods
// are: the container is the same one the Deployment would run.
func TestRender_PodHonoursDeclaredState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		state     string
		image     string
		wantInCmd string
	}{
		{state: "", image: "registry.k8s.io/nginx-slim:0.27"},
		{state: "running", image: "registry.k8s.io/nginx-slim:0.27"},
		{state: "CrashLoopBackOff", image: DefaultUtilImage, wantInCmd: "exit 1"},
		{state: "OOMKilled", image: DefaultUtilImage, wantInCmd: "tail /dev/zero"},
		{state: "pending", image: "registry.k8s.io/nginx-slim:0.27"},
		{state: "elevated_error_rate", image: HTTPServerImage, wantInCmd: "return 500;"},
		{state: "error", image: "invalid-registry.example.com/does-not-exist:v0.0.0"},
	}
	for _, tt := range tests {
		t.Run("state "+tt.state, func(t *testing.T) {
			t.Parallel()
			got, err := Render(podSpec("cache-worker-x7k9", tt.state))
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			p := parsePod(t, got)
			if p.Metadata.Name != "cache-worker-x7k9" || p.Metadata.Namespace != "test-ns" {
				t.Errorf("identity = %s/%s", p.Metadata.Namespace, p.Metadata.Name)
			}
			if p.Metadata.Labels["app"] != "cache-worker-x7k9" {
				t.Errorf("labels = %v, want app=cache-worker-x7k9", p.Metadata.Labels)
			}
			c := p.Spec.Containers[0]
			if c.Image != tt.image {
				t.Errorf("image = %q, want %q", c.Image, tt.image)
			}
			if tt.wantInCmd != "" && !strings.Contains(strings.Join(c.Command, " "), tt.wantInCmd) {
				t.Errorf("command %q does not contain %q", c.Command, tt.wantInCmd)
			}
			if strings.Contains(got, "replicas:") {
				t.Errorf("a bare pod has no replica count:\n%s", got)
			}
		})
	}
}

func TestRender_PodStructure(t *testing.T) {
	t.Parallel()

	oom := podSpec("oom", "oomkilled")
	if p := parsePod(t, mustRender(t, oom)); p.Spec.Containers[0].Resources.Limits["memory"] != OOMKilledMemoryLimit {
		t.Errorf("oomkilled pod limits = %v, want memory %s", p.Spec.Containers[0].Resources.Limits, OOMKilledMemoryLimit)
	}

	bound := podSpec("batch-processor-x9k2", "running")
	bound.NodeName = "lab-worker"
	bound.Env = []EnvVar{{Name: "DB_PASSWORD", SecretKeyRef: &SecretKeySelector{Name: "db-credentials", Key: "DB_PASSWORD"}}}
	p := parsePod(t, mustRender(t, bound))
	if p.Spec.NodeName != "lab-worker" {
		t.Errorf("nodeName = %q, want lab-worker", p.Spec.NodeName)
	}
	env := p.Spec.Containers[0].Env
	if len(env) != 1 || env[0].ValueFrom.SecretKeyRef.Name != "db-credentials" || env[0].ValueFrom.SecretKeyRef.Key != "DB_PASSWORD" {
		t.Errorf("env = %+v, want DB_PASSWORD from secret db-credentials", env)
	}
}

func mustRender(t *testing.T, s Spec) string {
	t.Helper()
	got, err := Render(s)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	return got
}

// A state a bare pod cannot be, or a combination that would materialise as
// some other state, is refused — never rendered as a healthy pod.
func TestRender_PodRefusesWhatItCannotBe(t *testing.T) {
	t.Parallel()

	boundPending := podSpec("p", "pending")
	boundPending.NodeName = "lab-worker"
	twoReplicas := podSpec("p", "running")
	twoReplicas.Replicas = 2
	faulted := podSpec("p", "oomkilled")
	faulted.Fault = &fault.Spec{Class: fault.ClassConfigUnboundedCache, Key: "CACHE_MAX_ENTRIES"}
	faulted.Env = []EnvVar{{Name: "CACHE_MAX_ENTRIES", Value: "0"}}
	boundDeployment := baseSpec("d", "running")
	boundDeployment.NodeName = "lab-worker"
	otherKind := baseSpec("s", "running")
	otherKind.Kind = "StatefulSet"
	envOnSynthesised := podSpec("p", "error")
	envOnSynthesised.Env = []EnvVar{{Name: "A", Value: "b"}}

	// resolved marks the refusals Spec.resolve makes, which Convergence
	// shares; the env refusal is Render's alone, as it is for a Deployment.
	tests := map[string]struct {
		spec     Spec
		want     string
		resolved bool
	}{
		"degraded pod":              {podSpec("p", "degraded"), "bare Pod cannot be", true},
		"pending pod bound to node": {boundPending, "bypasses", true},
		"pod with two replicas":     {twoReplicas, "one replica", true},
		"pod with a fault":          {faulted, "fault", true},
		"deployment bound to node":  {boundDeployment, "node binding", true},
		"unsupported kind":          {otherKind, "unsupported kind", true},
		"env on synthesised pod":    {envOnSynthesised, "synthesises its own container", false},
		"unrecognized pod state":    {podSpec("p", "haunted"), "unrecognized state", true},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := Render(tt.spec)
			if err == nil {
				t.Fatalf("Render() = nil error, manifest:\n%s", got)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
			if got != "" {
				t.Errorf("a refused spec must render nothing, got:\n%s", got)
			}
			if _, cerr := tt.spec.Convergence(); tt.resolved && cerr == nil {
				t.Errorf("Convergence() accepted a spec Render refuses")
			}
		})
	}
}

// A bare pod has no rollout: a healthy state converges on its container
// running, and an unhealthy one on the symptom its Deployment's pods would.
func TestConvergence_Pod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		state string
		want  Convergence
	}{
		{"", Convergence{Symptom: fault.SymptomRunning}},
		{"running", Convergence{Symptom: fault.SymptomRunning}},
		{"elevated_error_rate", Convergence{Symptom: fault.SymptomRunning}},
		{"CrashLoopBackOff", Convergence{Symptom: fault.SymptomCrashLoopBackOff}},
		{"OOMKilled", Convergence{Symptom: fault.SymptomOOMKilled}},
		{"pending", Convergence{Symptom: fault.SymptomUnschedulable}},
		{"error", Convergence{Symptom: fault.SymptomImagePullBackOff}},
	}
	for _, tt := range tests {
		got, err := podSpec("p", tt.state).Convergence()
		if err != nil {
			t.Errorf("state %q: Convergence() error = %v", tt.state, err)
			continue
		}
		if got != tt.want {
			t.Errorf("state %q: Convergence() = %+v, want %+v", tt.state, got, tt.want)
		}
	}
	if _, err := podSpec("p", "degraded").Convergence(); err == nil {
		t.Error("degraded pod: Convergence() must refuse what Render refuses")
	}
}
