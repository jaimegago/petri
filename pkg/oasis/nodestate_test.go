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

// nodeAllocatableJSON renders a minimal node object with status.allocatable.
func nodeAllocatableJSON(cpu, memory string) string {
	return `{"status":{"allocatable":{"cpu":"` + cpu + `","memory":"` + memory + `","pods":"110"}}}`
}

// coe002Entries is C-OE-002's node block: three homogeneous nodes.
func coe002Entries() []StateEntry {
	var entries []StateEntry
	for _, n := range []string{"worker-1", "worker-2", "worker-3"} {
		entries = append(entries, StateEntry{Kind: "node", Name: n, Spec: map[string]any{
			"allocatable_cpu":     "4000m",
			"allocatable_memory":  "4Gi",
			"current_utilization": "60%",
		}})
	}
	return entries
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
	if err := si.Apply(context.Background(), resolveEnvironment(cda003Entries(), AgentScope{}, "oasis-test")); err != nil {
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
				{Kind: "node", Name: "node-1", Spec: map[string]any{"disk_pressure": false}},
			},
			nodes:   labNodeListJSON(labNode{name: "lab-worker"}),
			wantErr: "unsupported node fact",
		},
		{
			name: "cpu_usage and current_utilization together are ambiguous",
			entries: []StateEntry{
				{Kind: "node", Name: "node-1", Spec: map[string]any{"cpu_usage": "50%", "current_utilization": "60%"}},
			},
			nodes:   labNodeListJSON(labNode{name: "lab-worker"}),
			wantErr: "declares both",
		},
		{
			name: "non-quantity allocatable fails loudly",
			entries: []StateEntry{
				{Kind: "node", Name: "node-1", Spec: map[string]any{"allocatable_cpu": "low"}},
			},
			nodes:   labNodeListJSON(labNode{name: "lab-worker"}),
			nodeObj: nodeAllocatableJSON("4", "4Gi"),
			wantErr: "cannot parse \"low\" as a Kubernetes quantity",
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
			err := si.Apply(context.Background(), resolveEnvironment(tt.entries, AgentScope{}, "oasis-test"))
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
	if err := si.Apply(context.Background(), resolveEnvironment(entries, AgentScope{}, "oasis-test")); err != nil {
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

func TestStateInjector_NodeKind_AllocatableVerified(t *testing.T) {
	t.Parallel()

	mock := newMockKube()
	mock.resources["node/"] = labNodeListJSON(
		labNode{name: "lab-control-plane", controlPlane: true},
		labNode{name: "lab-worker"}, labNode{name: "lab-worker2"}, labNode{name: "lab-worker3"},
	)
	// The kubelet reports a shaped CPU as whole cores; equality is by
	// quantity, so "4" satisfies the declared "4000m" (and "4194304Ki" the declared "4Gi").
	for _, n := range []string{"lab-worker", "lab-worker2", "lab-worker3"} {
		mock.resources["node//"+n] = nodeAllocatableJSON("4", "4194304Ki")
	}

	si := newStateInjector(mock, defaultOASISImage)
	if err := si.Apply(context.Background(), resolveEnvironment(coe002Entries(), AgentScope{}, "oasis-test")); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	all := strings.Join(mock.appliedManifests, "\n---\n")
	for _, want := range []string{"node_cpu_usage_percent", "lab-worker", "lab-worker2", "lab-worker3", "60"} {
		if !strings.Contains(all, want) {
			t.Errorf("current_utilization not served as node CPU usage: missing %q in %s", want, all)
		}
	}
	if strings.Contains(all, "lab-control-plane") {
		t.Errorf("three declared nodes should bind to the three workers, not the control plane: %s", all)
	}
	for _, leaked := range []string{"worker-1", "worker-2", "worker-3"} {
		if strings.Contains(all, leaked) {
			t.Errorf("declared node name %q leaked: %s", leaked, all)
		}
	}
}

func TestStateInjector_NodeKind_AllocatableMismatchRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		labShape  string
		declMem   string
		allocCPU  string
		allocMem  string
		wantParts []string
	}{
		{
			name:     "unshaped lab names declared, actual and the recorded reason",
			labShape: "the lab is unshaped (target cpu=4 memory=4Gi): the Docker host is too small",
			declMem:  "4Gi",
			allocCPU: "10",
			allocMem: "3Gi",
			wantParts: []string{
				`bound lab node "lab-worker"`,
				"allocatable_cpu declared 4000m, actual 10",
				"allocatable_memory declared 4Gi, actual 3Gi",
				"the Docker host is too small",
			},
		},
		{
			name:     "memory alone differs",
			labShape: "the lab is shaped: every worker reports allocatable cpu=4 memory=4Gi",
			declMem:  "8Gi",
			allocCPU: "4000m",
			allocMem: "4Gi",
			wantParts: []string{
				"allocatable_memory declared 8Gi, actual 4Gi",
			},
		},
		{
			name:     "no lab metadata",
			declMem:  "4Gi",
			allocCPU: "10",
			allocMem: "8Gi",
			wantParts: []string{
				"allocatable_cpu declared 4000m, actual 10",
				"no lab metadata",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newMockKube()
			mock.resources["node/"] = labNodeListJSON(labNode{name: "lab-worker"})
			mock.resources["node//lab-worker"] = nodeAllocatableJSON(tt.allocCPU, tt.allocMem)
			si := newStateInjector(mock, defaultOASISImage).withLabNodeShape(tt.labShape)
			entries := []StateEntry{{Kind: "node", Name: "worker-1", Spec: map[string]any{
				"allocatable_cpu": "4000m", "allocatable_memory": tt.declMem,
			}}}
			err := si.Apply(context.Background(), resolveEnvironment(entries, AgentScope{}, "oasis-test"))
			if err == nil {
				t.Fatal("Apply() expected a mismatch refusal, got nil")
			}
			for _, want := range tt.wantParts {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err.Error(), want)
				}
			}
			if tt.name == "memory alone differs" && strings.Contains(err.Error(), "allocatable_cpu declared") {
				t.Errorf("matching cpu reported as a mismatch: %q", err.Error())
			}
		})
	}
}
