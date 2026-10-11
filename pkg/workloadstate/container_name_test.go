package workloadstate

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/jaimegago/petri/pkg/fault"
)

// renderedContainerNames reads back the container names of a rendered
// Deployment or Pod.
func renderedContainerNames(t *testing.T, manifest string) []string {
	t.Helper()
	type containers struct {
		Containers []struct {
			Name string `yaml:"name"`
		} `yaml:"containers"`
	}
	var m struct {
		Kind string `yaml:"kind"`
		Spec struct {
			containers `yaml:",inline"`
			Template   struct {
				Spec containers `yaml:"spec"`
			} `yaml:"template"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(manifest), &m); err != nil {
		t.Fatalf("rendered manifest is not valid YAML: %v\n%s", err, manifest)
	}
	cs := m.Spec.Containers
	if m.Kind == "Deployment" {
		cs = m.Spec.Template.Spec.Containers
	}
	names := make([]string, len(cs))
	for i, c := range cs {
		names[i] = c.Name
	}
	return names
}

// A declared container name is the name the cluster shows, in every state and
// for both kinds; the default applies only when none is declared.
func TestRender_DeclaredContainerName(t *testing.T) {
	t.Parallel()

	for _, kind := range []Kind{KindDeployment, KindPod} {
		for _, state := range AcceptedStates() {
			if kind == KindPod && state == StateDegraded {
				continue // a bare pod cannot be degraded
			}
			for _, declared := range []string{"", "notification-service"} {
				want := declared
				if want == "" {
					want = DefaultContainerName
				}
				t.Run(string(kind)+"/"+string(state)+"/"+want, func(t *testing.T) {
					t.Parallel()
					s := baseSpec("notification-service", string(state))
					s.Kind = kind
					s.ContainerName = declared
					got, err := Render(s)
					if err != nil {
						t.Fatalf("Render() error = %v", err)
					}
					names := renderedContainerNames(t, got)
					if len(names) != 1 || names[0] != want {
						t.Errorf("container names = %v, want [%s]:\n%s", names, want, got)
					}
				})
			}
		}
	}
}

// DA-1's shape: a faulted workload runs the application, and its container
// still carries the declared name.
func TestRender_DeclaredContainerNameOnFaultedWorkload(t *testing.T) {
	t.Parallel()

	s := baseSpec("notification-service", "crashloopbackoff")
	s.ContainerName = "notification-service"
	s.Fault = &fault.Spec{Class: fault.ClassConfigMissingKey, ConfigMap: "smtp-config", Key: "SMTP_PORT"}
	s.Env = []EnvVar{{Name: "SMTP_PORT", ConfigMapKeyRef: &ConfigMapKeySelector{Name: "smtp-config", Key: "SMTP_PORT"}}}
	got, err := Render(s)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if names := renderedContainerNames(t, got); len(names) != 1 || names[0] != "notification-service" {
		t.Errorf("container names = %v, want [notification-service]:\n%s", names, got)
	}
	if strings.Contains(got, "name: "+DefaultContainerName+"\n") {
		t.Errorf("declared name must replace the default, not sit beside it:\n%s", got)
	}
}

// A declared name Kubernetes would reject is refused before anything renders.
func TestRender_InvalidContainerNameIsError(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"Notification-Service",
		"-leading",
		"trailing-",
		"under_score",
		"dotted.name",
		"with space",
		"x: y\n        image: evil",
		strings.Repeat("a", 64),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := baseSpec("svc", "running")
			s.ContainerName = name
			got, err := Render(s)
			if err == nil {
				t.Fatalf("Render() accepted container name %q:\n%s", name, got)
			}
			if !strings.Contains(err.Error(), "not a valid Kubernetes container name") {
				t.Errorf("error = %v, want it to name the invalid container name", err)
			}
		})
	}

	s := baseSpec("svc", "running")
	s.ContainerName = strings.Repeat("a", 63)
	if _, err := Render(s); err != nil {
		t.Errorf("a 63-character name is valid: %v", err)
	}
}
