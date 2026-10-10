package oasis

import (
	"context"
	"strings"
	"testing"

	"github.com/jaimegago/petri/pkg/workloadstate"
)

// onDockerHub reports whether an image reference resolves to Docker Hub: a
// first path segment that is not a registry host (no "." or ":", not
// localhost) means Docker Hub, as does a Docker Hub host named outright.
func onDockerHub(ref string) bool {
	first, _, found := strings.Cut(ref, "/")
	if !found || (!strings.ContainsAny(first, ".:") && first != "localhost") {
		return true
	}
	switch first {
	case "docker.io", "index.docker.io", "registry-1.docker.io":
		return true
	}
	return false
}

// TestNoDefaultImageIsOnDockerHub renders every state entry kind that runs a
// container, with no image declared, and holds every image the provider
// chose to a registry other than Docker Hub, whose blob storage some
// networks null-route. Deployments and bare pods are rendered in every state
// they accept, so a new state cannot bring a Docker Hub image back.
func TestNoDefaultImageIsOnDockerHub(t *testing.T) {
	t.Parallel()

	var entries []StateEntry
	for _, st := range workloadstate.AcceptedStates() {
		entries = append(entries, StateEntry{Kind: "deployment", Name: "d-" + string(st), Spec: map[string]any{"status": string(st)}})
		if st != workloadstate.StateDegraded { // a bare pod refuses degraded
			entries = append(entries, StateEntry{Kind: "pod", Name: "p-" + string(st), Spec: map[string]any{"status": string(st)}})
		}
	}
	entries = append(entries,
		StateEntry{Kind: "logs", Name: "logs", Spec: map[string]any{"entries": []any{"line"}}},
		StateEntry{Kind: "metrics", Name: "metrics", Spec: map[string]any{"metric_name": "up", "value": float64(1)}},
		StateEntry{Kind: "traces", Name: "traces", Spec: map[string]any{"trace_id": "abc"}},
		StateEntry{Kind: "alert", Name: "alert", Spec: map[string]any{"alertname": "High"}},
	)

	mock := newMockKube()
	si := newStateInjector(mock, defaultOASISImage)
	if err := si.Apply(context.Background(), resolveEnvironment(entries, AgentScope{}, "ns")); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	containers := 0
	for _, m := range mock.appliedManifests {
		for _, line := range strings.Split(m, "\n") {
			ref, ok := strings.CutPrefix(strings.TrimSpace(line), "image:")
			if !ok {
				continue
			}
			containers++
			if ref = strings.TrimSpace(ref); onDockerHub(ref) {
				t.Errorf("rendered Docker Hub image %q in:\n%s", ref, m)
			}
		}
	}
	// Every deployment, pod, logs deployment and the three mock servers.
	if want := 2*len(workloadstate.AcceptedStates()) - 1 + 4; containers != want {
		t.Errorf("checked %d images, want %d — an entry kind stopped rendering, or the set grew", containers, want)
	}
}

func TestOnDockerHub(t *testing.T) {
	t.Parallel()
	for ref, want := range map[string]bool{
		"python:3-alpine":                 true,
		"grafana/grafana:10.2.3":          true,
		"docker.io/library/nginx:1.27":    true,
		"registry.k8s.io/nginx-slim:0.27": false,
		"quay.io/prometheus/prometheus:1": false,
	} {
		if got := onDockerHub(ref); got != want {
			t.Errorf("onDockerHub(%q) = %v, want %v", ref, got, want)
		}
	}
}
