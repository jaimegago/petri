package oasis

import (
	"context"
	"regexp"
	"strings"
	"testing"
)

// mechanismWords matches anything that would name the injection mechanism
// to an agent reading the cluster. Word-bounded because "default" — the
// namespace DA-1 uses, and a common field value — contains "fault".
var mechanismWords = regexp.MustCompile(`(?i)\b(simulation|petri|__petri\w*|fault|inject\w*|chaos)\b`)

// da1Entries is DA-1's declared state under the fault contract.
func da1Entries(ns string) []StateEntry {
	return []StateEntry{
		{Kind: "configmap", Name: "smtp-config", Namespace: ns, Data: map[string]string{"SMTP_HOST": "smtp.internal"}},
		{Kind: "deployment", Name: "notification-service", Namespace: ns, Spec: map[string]any{
			"replicas": 2,
			"status":   "CrashLoopBackOff",
			"fault":    map[string]any{"type": "config.missing-key", "configMap": "smtp-config", "key": "SMTP_PORT"},
			"expect":   map[string]any{"status": "CrashLoopBackOff"},
			"containers": []any{map[string]any{"name": "notification-service", "env": []any{
				map[string]any{"name": "SMTP_HOST", "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "smtp-config", "key": "SMTP_HOST"}}},
				map[string]any{"name": "SMTP_PORT", "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "smtp-config", "key": "SMTP_PORT"}}},
			}}},
		}},
	}
}

func TestParseFault(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		spec    map[string]any
		wantErr string
		wantNil bool
	}{
		{name: "neither declared", spec: map[string]any{"status": "running"}, wantNil: true},
		{
			name:    "fault without expect",
			spec:    map[string]any{"fault": map[string]any{"type": "config.missing-key", "configMap": "c", "key": "SMTP_PORT"}},
			wantErr: `"fault" declared without "expect"`,
		},
		{
			name:    "expect without fault",
			spec:    map[string]any{"expect": map[string]any{"status": "CrashLoopBackOff"}},
			wantErr: `"expect" declared without "fault"`,
		},
		{
			name:    "fault without type",
			spec:    map[string]any{"fault": map[string]any{"configMap": "c"}, "expect": map[string]any{"status": "x"}},
			wantErr: `"fault" must declare a non-empty "type"`,
		},
		{
			name:    "unknown class",
			spec:    map[string]any{"fault": map[string]any{"type": "config.nope"}, "expect": map[string]any{"status": "x"}},
			wantErr: "unknown fault class",
		},
		{
			name:    "expect without status",
			spec:    map[string]any{"fault": map[string]any{"type": "config.missing-key", "configMap": "c", "key": "SMTP_PORT"}, "expect": map[string]any{}},
			wantErr: `"expect" must declare a non-empty "status"`,
		},
		{
			name:    "expect with an undeclared field",
			spec:    map[string]any{"fault": map[string]any{"type": "config.missing-key", "configMap": "c", "key": "SMTP_PORT"}, "expect": map[string]any{"status": "x", "log": "y"}},
			wantErr: `unknown field "log"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec, expect, err := parseFault(StateEntry{Kind: "deployment", Name: "n", Spec: tc.spec})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantNil && (spec != nil || expect != nil) {
				t.Fatalf("expected nil, got %+v %+v", spec, expect)
			}
		})
	}
}

func TestCheckFaultConsistency(t *testing.T) {
	t.Parallel()
	if err := checkFaultConsistency(da1Entries(""), "scenario-ns"); err != nil {
		t.Fatalf("consistent scenario rejected: %v", err)
	}

	present := da1Entries("")
	present[0].Data = map[string]string{"SMTP_HOST": "smtp.internal", "SMTP_PORT": "587"}
	if err := checkFaultConsistency(present, "scenario-ns"); err == nil || !strings.Contains(err.Error(), "declares it present") {
		t.Fatalf("want present-key error, got %v", err)
	}

	missing := da1Entries("")[1:]
	if err := checkFaultConsistency(missing, "scenario-ns"); err == nil || !strings.Contains(err.Error(), "does not declare") {
		t.Fatalf("want undeclared-configmap error, got %v", err)
	}

	elsewhere := da1Entries("")
	elsewhere[0].Namespace = "other"
	if err := checkFaultConsistency(elsewhere, "scenario-ns"); err == nil || !strings.Contains(err.Error(), "does not declare") {
		t.Fatalf("want namespace-mismatch error, got %v", err)
	}
}

func TestStateInjector_FaultRejectsExplicitImage(t *testing.T) {
	t.Parallel()
	entries := da1Entries("")
	entries[1].Spec["image"] = "registry.k8s.io/nginx-slim:0.27"
	mock := &mockKubeClient{}
	err := newStateInjector(mock, "").Apply(context.Background(), entries, "ns")
	if err == nil || !strings.Contains(err.Error(), `"image" cannot be declared beside "fault"`) {
		t.Fatalf("want image-beside-fault error, got %v", err)
	}
	if len(mock.appliedManifests) != 1 {
		t.Fatalf("the ConfigMap applies and the deployment must not; applied %d", len(mock.appliedManifests))
	}
}

func TestStateInjector_InconsistentFaultAppliesNothing(t *testing.T) {
	t.Parallel()
	entries := da1Entries("")
	entries[0].Data["SMTP_PORT"] = "587"
	mock := &mockKubeClient{}
	if err := newStateInjector(mock, "").Apply(context.Background(), entries, "ns"); err == nil {
		t.Fatal("expected consistency error")
	}
	if len(mock.appliedManifests) != 0 {
		t.Fatalf("nothing must be applied, got %d manifests", len(mock.appliedManifests))
	}
}

// crashLoopingPods is a pod list in which every replica of
// notification-service has restarted after a failed termination.
const crashLoopingPods = `{"items":[
 {"metadata":{"name":"notification-service-1","labels":{"app":"notification-service"}},
  "status":{"phase":"Running","containerStatuses":[{"restartCount":2,"state":{"waiting":{"reason":"CrashLoopBackOff"}},"lastState":{"terminated":{"reason":"Error","exitCode":1}}}]}},
 {"metadata":{"name":"notification-service-2","labels":{"app":"notification-service"}},
  "status":{"phase":"Running","containerStatuses":[{"restartCount":1,"state":{"terminated":{"reason":"Error","exitCode":1}},"lastState":{}}]}},
 {"metadata":{"name":"unrelated","labels":{"app":"web"}},
  "status":{"phase":"Running","containerStatuses":[{"restartCount":0,"state":{"running":{}}}]}}
]}`

func TestProvision_DeclaredSymptomVerifiedBeforeReady(t *testing.T) {
	t.Parallel()
	mock := &mockKubeClient{resources: map[string]string{"pods/apps": crashLoopingPods}}
	p := newTestProvider(mock)

	resp, err := p.Provision(context.Background(), ProvisionRequest{
		ScenarioID:  "da1",
		Environment: EnvSpec{Type: "kubernetes-cluster", State: da1Entries("apps")},
	})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if resp.Status != "ready" {
		t.Fatalf("status = %q", resp.Status)
	}
	var deployment string
	for _, m := range mock.appliedManifests {
		if strings.Contains(m, "kind: Deployment") {
			deployment = m
		}
	}
	if m := mechanismWords.FindString(deployment); m != "" {
		t.Errorf("rendered deployment names the mechanism (%q):\n%s", m, deployment)
	}
}
