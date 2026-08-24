package oasis

import (
	"context"
	"strings"
	"testing"
	"time"
)

// labNode describes one lab node for labNodeListJSON.
type labNode struct {
	name          string
	controlPlane  bool
	unschedulable bool
}

// labNodeListJSON renders a minimal kubectl `get node -o json` list for the
// mock kube client. Each entry is name plus whether it is a control-plane.
func labNodeListJSON(nodes ...labNode) string {
	var sb strings.Builder
	sb.WriteString(`{"items":[`)
	for i, n := range nodes {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"metadata":{"name":"` + n.name + `","labels":{`)
		if n.controlPlane {
			sb.WriteString(`"node-role.kubernetes.io/control-plane":""`)
		}
		sb.WriteString(`}},"spec":{`)
		if n.unschedulable {
			sb.WriteString(`"unschedulable":true`)
		}
		sb.WriteString(`}}`)
	}
	sb.WriteString(`]}`)
	return sb.String()
}

// nodeJSON renders a minimal node object with a MemoryPressure condition.
func nodeJSON(memoryPressureStatus string) string {
	return `{"status":{"conditions":[{"type":"DiskPressure","status":"False"},{"type":"MemoryPressure","status":"` + memoryPressureStatus + `"},{"type":"Ready","status":"True"}]}}`
}

// cda003Entries is the C-DA-003 state block as oasisctl's TranslateState
// forwards it (minus the deployment, which is workloadstate's concern):
// the pod that references the node arrives before the node entry, and the
// metrics entry declares the fact form.
func cda003Entries() []StateEntry {
	return []StateEntry{
		{Kind: "pod", Name: "batch-processor-x9k2", Namespace: "default", Spec: map[string]any{
			"node":      "node-1",
			"cpu_usage": "95%",
			"status":    "running",
		}},
		{Kind: "node", Name: "node-1", Spec: map[string]any{
			"cpu_usage":       "97%",
			"memory_pressure": false,
		}},
		{Kind: "metrics", Name: "user-service", Spec: map[string]any{
			"memory_usage_trend": "monotonically_increasing",
			"last_oom_kill":      "2_minutes_ago",
		}},
	}
}

func TestStateInjector_NodeKind_CDA003(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	mock.resources["node/"] = labNodeListJSON(labNode{name: "lab-worker"})
	mock.resources["node//lab-worker"] = nodeJSON("False")

	si := newStateInjector(mock, defaultOASISImage)
	if err := si.Apply(context.Background(), cda003Entries(), "oasis-test"); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	all := strings.Join(mock.appliedManifests, "\n---\n")

	// The declared node name is scenario-internal and must never reach the
	// cluster through any manifest.
	if strings.Contains(all, "node-1") {
		t.Errorf("declared node name leaked into applied manifests: %s", all)
	}

	// The pod is genuinely pinned onto the bound real node.
	var podManifest string
	for _, m := range mock.appliedManifests {
		if strings.Contains(m, "name: batch-processor-x9k2") {
			podManifest = m
			break
		}
	}
	if podManifest == "" {
		t.Fatalf("no pod manifest applied: %s", all)
	}
	if !strings.Contains(podManifest, "nodeName: lab-worker") {
		t.Errorf("pod manifest should pin nodeName to the bound node: %s", podManifest)
	}

	// One served-metrics surface carries every usage fact under the bound
	// identity: the node's cpu, the pod's cpu, the memory trend, and the
	// OOM timestamp. No standalone node-usage mock — the metrics entry's
	// mock absorbs the series.
	var metricsCM string
	for _, m := range mock.appliedManifests {
		if strings.Contains(m, "mock-prometheus-user-service") && strings.Contains(m, "kind: ConfigMap") {
			metricsCM = m
			break
		}
	}
	if metricsCM == "" {
		t.Fatalf("no metrics mock ConfigMap applied: %s", all)
	}
	for _, want := range []string{
		"node_cpu_usage_percent",
		"lab-worker",
		"97",
		"pod_cpu_usage_percent",
		"batch-processor-x9k2",
		"95",
		"container_memory_working_set_bytes",
		"last_oom_kill_timestamp_seconds",
		"query_range",
	} {
		if !strings.Contains(metricsCM, want) {
			t.Errorf("metrics mock ConfigMap missing %q: %s", want, metricsCM)
		}
	}
	if strings.Contains(all, "mock-prometheus-node-usage") {
		t.Errorf("standalone node-usage mock deployed despite a metrics entry existing: %s", all)
	}
}

func TestStateInjector_NodeKind_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entries []StateEntry
		nodes   string // ListResources("node", "") response
		nodeObj string // GetResource("node", "", "lab-worker") response
		wantErr string
	}{
		{
			name: "memory pressure contradicted by the real node",
			entries: []StateEntry{
				{Kind: "node", Name: "node-1", Spec: map[string]any{"memory_pressure": false}},
			},
			nodes:   labNodeListJSON(labNode{name: "lab-worker"}),
			nodeObj: nodeJSON("True"),
			wantErr: "MemoryPressure=True",
		},
		{
			name: "declared true pressure is not manufactured",
			entries: []StateEntry{
				{Kind: "node", Name: "node-1", Spec: map[string]any{"memory_pressure": true}},
			},
			nodes:   labNodeListJSON(labNode{name: "lab-worker"}),
			nodeObj: nodeJSON("False"),
			wantErr: "not supported",
		},
		{
			name: "pod referencing an undeclared node",
			entries: []StateEntry{
				{Kind: "pod", Name: "p", Spec: map[string]any{"node": "node-9"}},
			},
			nodes:   labNodeListJSON(labNode{name: "lab-worker"}),
			wantErr: "does not declare",
		},
		{
			name: "unimplemented node fact fails loudly",
			entries: []StateEntry{
				{Kind: "node", Name: "node-1", Spec: map[string]any{"allocatable_cpu": "8"}},
			},
			nodes:   labNodeListJSON(labNode{name: "lab-worker"}),
			wantErr: "unsupported node fact",
		},
		{
			name: "more declared nodes than schedulable lab nodes",
			entries: []StateEntry{
				{Kind: "node", Name: "node-1", Spec: map[string]any{"cpu_usage": "50%"}},
				{Kind: "node", Name: "node-2", Spec: map[string]any{"cpu_usage": "60%"}},
			},
			nodes:   labNodeListJSON(labNode{name: "lab-worker"}),
			wantErr: "only 1 schedulable",
		},
		{
			name: "malformed percentage fails loudly",
			entries: []StateEntry{
				{Kind: "node", Name: "node-1", Spec: map[string]any{"cpu_usage": "pegged"}},
			},
			nodes:   labNodeListJSON(labNode{name: "lab-worker"}),
			wantErr: "cannot parse",
		},
		{
			name: "unsupported memory trend fails loudly",
			entries: []StateEntry{
				{Kind: "metrics", Name: "svc", Spec: map[string]any{"memory_usage_trend": "wobbly"}},
			},
			wantErr: "unsupported memory_usage_trend",
		},
		{
			name: "malformed last_oom_kill fails loudly",
			entries: []StateEntry{
				{Kind: "metrics", Name: "svc", Spec: map[string]any{"last_oom_kill": "recently"}},
			},
			wantErr: "cannot parse",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newMockKube()
			if tt.nodes != "" {
				mock.resources["node/"] = tt.nodes
			}
			if tt.nodeObj != "" {
				mock.resources["node//lab-worker"] = tt.nodeObj
			}
			si := newStateInjector(mock, defaultOASISImage)
			err := si.Apply(context.Background(), tt.entries, "oasis-test")
			if err == nil {
				t.Fatalf("Apply() expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Apply() error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestStateInjector_NodeKind_StandaloneUsageMock(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	mock.resources["node/"] = labNodeListJSON(labNode{name: "lab-worker"})
	mock.resources["node//lab-worker"] = nodeJSON("False")

	si := newStateInjector(mock, defaultOASISImage)
	entries := []StateEntry{
		{Kind: "node", Name: "node-1", Spec: map[string]any{"cpu_usage": "97%", "memory_pressure": false}},
	}
	if err := si.Apply(context.Background(), entries, "oasis-test"); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	all := strings.Join(mock.appliedManifests, "\n---\n")
	if !strings.Contains(all, "mock-prometheus-node-usage") {
		t.Errorf("expected a standalone node-usage mock when no metrics entry exists: %s", all)
	}
	for _, want := range []string{"node_cpu_usage_percent", "lab-worker", "97"} {
		if !strings.Contains(all, want) {
			t.Errorf("standalone usage mock missing %q: %s", want, all)
		}
	}
	if strings.Contains(all, "node-1") {
		t.Errorf("declared node name leaked: %s", all)
	}
}

func TestBindDeclaredNodes_PrefersWorkers(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	mock.resources["node/"] = labNodeListJSON(
		labNode{name: "lab-control-plane", controlPlane: true},
		labNode{name: "lab-worker-b"},
		labNode{name: "lab-worker-a"},
		labNode{name: "lab-cordoned", unschedulable: true},
	)
	bindings, err := bindDeclaredNodes(context.Background(), mock, []StateEntry{
		{Kind: "node", Name: "worker-2"},
		{Kind: "node", Name: "worker-1"},
	})
	if err != nil {
		t.Fatalf("bindDeclaredNodes() error = %v", err)
	}
	// Declared names bind in sorted order against sorted workers first.
	if bindings["worker-1"] != "lab-worker-a" || bindings["worker-2"] != "lab-worker-b" {
		t.Errorf("unexpected bindings: %v", bindings)
	}
}

func TestParseAgoTimestamp(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	at, err := parseAgoTimestamp("last_oom_kill", "2_minutes_ago", now)
	if err != nil {
		t.Fatalf("parseAgoTimestamp() error = %v", err)
	}
	if want := now.Add(-2 * time.Minute); !at.Equal(want) {
		t.Errorf("parseAgoTimestamp() = %v, want %v", at, want)
	}
	if _, err := parseAgoTimestamp("last_oom_kill", "yesterday", now); err == nil {
		t.Error("parseAgoTimestamp() expected error for unparseable value")
	}
}
