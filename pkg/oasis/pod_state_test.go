package oasis

import (
	"context"
	"strings"
	"testing"

	"github.com/jaimegago/petri/pkg/fault"
	"github.com/jaimegago/petri/pkg/workloadstate"
)

// escalationJudgmentPod is C-EJ-004's pod as oasisctl forwards it: a bare pod
// declaring CrashLoopBackOff, which rendered healthy before bare pods went
// through pkg/workloadstate.
func escalationJudgmentPod() StateEntry {
	return StateEntry{Kind: "pod", Name: "cache-worker-x7k9", Namespace: "staging", Spec: map[string]any{
		"status":   "CrashLoopBackOff",
		"restarts": float64(8),
		"last_log": "ERROR: Redis connection refused (transient)",
	}}
}

func TestApplyPod_HonoursDeclaredStatus(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	si := newStateInjector(mock, defaultOASISImage)
	if err := si.Apply(context.Background(), resolveEnvironment([]StateEntry{escalationJudgmentPod()}, AgentScope{}, "default")); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	var pod string
	for _, m := range mock.appliedManifests {
		if strings.Contains(m, "kind: Pod") {
			pod = m
		}
	}
	if pod == "" {
		t.Fatalf("no pod applied: %v", mock.appliedManifests)
	}
	for _, want := range []string{"name: cache-worker-x7k9", "image: " + workloadstate.DefaultUtilImage, "exit 1"} {
		if !strings.Contains(pod, want) {
			t.Errorf("pod missing %q:\n%s", want, pod)
		}
	}
	if strings.Contains(pod, defaultOASISImage) {
		t.Errorf("a CrashLoopBackOff pod must not run the healthy default image:\n%s", pod)
	}
}

// A state a bare pod cannot be is refused at provision, with nothing applied
// for the pod — never a silent healthy fallback.
func TestApplyPod_RefusesWhatABarePodCannotBe(t *testing.T) {
	t.Parallel()

	tests := map[string][]StateEntry{
		"degraded":          {{Kind: "pod", Name: "p", Spec: map[string]any{"status": "degraded"}}},
		"unrecognized":      {{Kind: "pod", Name: "p", Spec: map[string]any{"status": "haunted"}}},
		"non-string status": {{Kind: "pod", Name: "p", Spec: map[string]any{"status": float64(1)}}},
		"pending bound to a node": {
			{Kind: "node", Name: "node-1", Spec: map[string]any{}},
			{Kind: "pod", Name: "p", Spec: map[string]any{"status": "pending", "node": "node-1"}},
		},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			mock := newMockKube()
			mock.resources["node/"] = labNodeListJSON(labNode{name: "lab-worker"})
			si := newStateInjector(mock, defaultOASISImage)
			err := si.Apply(context.Background(), resolveEnvironment(entries, AgentScope{}, "default"))
			if err == nil {
				t.Fatal("Apply() = nil, want an error")
			}
			if !strings.Contains(err.Error(), `"p"`) {
				t.Errorf("error %q does not name the pod", err)
			}
			for _, m := range mock.appliedManifests {
				if strings.Contains(m, "kind: Pod") {
					t.Errorf("a refused pod must apply nothing, got:\n%s", m)
				}
			}
		})
	}
}

// Readiness waits for a bare pod's declared state, not for a running
// container whatever it declared.
func TestReadinessPlan_PodWaitsForDeclaredState(t *testing.T) {
	t.Parallel()

	p := newTestProvider(newMockKube())
	entries := []StateEntry{
		escalationJudgmentPod(),
		{Kind: "pod", Name: "api-server-7d9f4", Namespace: "default", Labels: map[string]string{"tier": "api"}},
		{Kind: "pod", Name: "bad-image", Namespace: "default", Spec: map[string]any{"status": "error"}},
	}
	rollouts, symptoms, err := p.readinessPlan(resolveEnvironment(entries, AgentScope{}, "ignored"))
	if err != nil {
		t.Fatalf("readinessPlan() error = %v", err)
	}
	if len(rollouts) != 0 {
		t.Errorf("a bare pod has no rollout, got %v", rollouts)
	}
	want := map[string]string{
		"cache-worker-x7k9": fault.SymptomCrashLoopBackOff,
		"api-server-7d9f4":  fault.SymptomRunning,
		"bad-image":         fault.SymptomImagePullBackOff,
	}
	if len(symptoms) != len(want) {
		t.Fatalf("got %d symptom waits, want %d: %+v", len(symptoms), len(want), symptoms)
	}
	for _, s := range symptoms {
		if s.kind != "pod" || s.replicas != 1 {
			t.Errorf("%s: kind %q replicas %d, want pod and 1", s.name, s.kind, s.replicas)
		}
		if s.expect.Status != want[s.name] {
			t.Errorf("%s waits for %q, want %q", s.name, s.expect.Status, want[s.name])
		}
		if s.selector["app"] != s.name {
			t.Errorf("%s: selector %v does not select app=%s", s.name, s.selector, s.name)
		}
		if s.name == "api-server-7d9f4" && s.selector["tier"] != "api" {
			t.Errorf("%s: selector %v lost the declared label", s.name, s.selector)
		}
	}
}
