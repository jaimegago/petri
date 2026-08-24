package oasis

// Node state entries and the fact-routing they introduce.
//
// A declared node name (C-DA-003's "node-1") is scenario-internal: the lab's
// nodes exist before the scenario does, so the name is bound to a real lab
// node at provision and every channel the agent can read presents the bound
// identity consistently. The declared name never reaches the cluster — not in
// a manifest, not in a label, not in a served metric.
//
// Declared facts route by what can genuinely observe them:
//
//   - Usage facts (a node's cpu_usage, a pod's declared cpu_usage) have no
//     physical reader in the lab — nothing installs metrics-server or a real
//     Prometheus pipeline — so they are served through the existing
//     mock-Prometheus machinery under the bound identity, and nowhere else.
//     No physical CPU is burned: pegging a real node proves nothing to an
//     agent that cannot read it, and converts a scoring fixture into a flake
//     generator.
//   - Condition facts (memory_pressure) have a real reader: the agent reads
//     node objects through its generic Kubernetes tooling. They are therefore
//     verified against the real node's conditions at provision — the provider
//     verifies rather than manufactures, and a declared condition the real
//     node contradicts fails the provision loudly.
//
// Scope is deliberately minimal: the facts C-DA-003 declares (cpu_usage,
// memory_pressure) are implemented; anything else on a node entry is a loud
// error rather than a silently dropped fact. Allocatable-resource shaping and
// the manufacturing of true pressure conditions are recorded in petri's
// backlog, not built here.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// nodeRoleControlPlaneLabel is the label kubeadm (and kind) stamp on
// control-plane nodes. Binding prefers worker nodes so a pinned pod lands
// where a scheduled workload plausibly would; control-plane nodes are used
// only when no worker exists (a level-1 lab is single-node).
const nodeRoleControlPlaneLabel = "node-role.kubernetes.io/control-plane"

// metricSample is one (timestamp, value) point of a served range series.
// Values are strings because that is what the Prometheus HTTP API carries.
type metricSample struct {
	at    time.Time
	value string
}

// metricSeries is one series on the scenario's served-Prometheus surface.
type metricSeries struct {
	// labels carries the full label set including __name__.
	labels map[string]string
	// value is the instant-query value.
	value string
	// rangeSamples, when non-nil, is the series' range-query shape (e.g. a
	// monotonically increasing memory trend). Series without samples render
	// as constants over the range window.
	rangeSamples []metricSample
}

// bindDeclaredNodes maps every declared node name to a real, schedulable lab
// node. Deterministic: declared names are bound in sorted order against a
// sorted pool (workers first), so re-running the same Apply — including the
// /v1/inject-state path — reproduces the same binding.
func bindDeclaredNodes(ctx context.Context, kube KubeClient, entries []StateEntry) (map[string]string, error) {
	var declared []string
	seen := map[string]bool{}
	for _, e := range entries {
		if strings.ToLower(e.Kind) != "node" {
			continue
		}
		if seen[e.Name] {
			return nil, fmt.Errorf("node %q is declared more than once", e.Name)
		}
		seen[e.Name] = true
		declared = append(declared, e.Name)
	}
	if len(declared) == 0 {
		return nil, nil
	}
	sort.Strings(declared)

	raw, err := kube.ListResources(ctx, "node", "")
	if err != nil {
		return nil, fmt.Errorf("listing lab nodes: %w", err)
	}
	pool, err := schedulableNodeNames(raw)
	if err != nil {
		return nil, err
	}
	if len(pool) < len(declared) {
		return nil, fmt.Errorf("scenario declares %d node(s) but the lab has only %d schedulable node(s)", len(declared), len(pool))
	}
	bindings := make(map[string]string, len(declared))
	for i, name := range declared {
		bindings[name] = pool[i]
	}
	return bindings, nil
}

// schedulableNodeNames extracts the bindable node pool from a kubectl
// `get node -o json` list: schedulable nodes only, workers before
// control-planes, each group sorted by name.
func schedulableNodeNames(rawList string) ([]string, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Spec struct {
				Unschedulable bool `json:"unschedulable"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(rawList), &list); err != nil {
		return nil, fmt.Errorf("parsing lab node list: %w", err)
	}
	var workers, controlPlanes []string
	for _, n := range list.Items {
		if n.Spec.Unschedulable {
			continue
		}
		if _, cp := n.Metadata.Labels[nodeRoleControlPlaneLabel]; cp {
			controlPlanes = append(controlPlanes, n.Metadata.Name)
		} else {
			workers = append(workers, n.Metadata.Name)
		}
	}
	sort.Strings(workers)
	sort.Strings(controlPlanes)
	return append(workers, controlPlanes...), nil
}

// collectNodeUsageSeries walks the entries once and returns every usage fact
// that routes to the served metrics surface: a node entry's cpu_usage and a
// pod entry's declared cpu_usage, labeled with the bound (real) identity.
// It is also where a node entry's fact vocabulary is enforced: an
// unimplemented fact is a loud provision error, never a silent drop.
func collectNodeUsageSeries(entries []StateEntry, bindings map[string]string, defaultNamespace string) ([]metricSeries, error) {
	var series []metricSeries
	for _, e := range entries {
		switch strings.ToLower(e.Kind) {
		case "node":
			for key := range e.Spec {
				switch key {
				case "cpu_usage", "memory_pressure":
				default:
					return nil, fmt.Errorf("node %q: unsupported node fact %q; implemented facts are %q (served via metrics) and %q (verified against the real node)", e.Name, key, "cpu_usage", "memory_pressure")
				}
			}
			if v, ok := e.Spec["cpu_usage"]; ok {
				pct, err := parsePercent("cpu_usage", v)
				if err != nil {
					return nil, fmt.Errorf("node %q: %w", e.Name, err)
				}
				real := bindings[e.Name]
				series = append(series, metricSeries{
					labels: map[string]string{
						"__name__": "node_cpu_usage_percent",
						"node":     real,
						"instance": real,
					},
					value: formatFloat(pct),
				})
			}
		case "pod":
			v, ok := e.Spec["cpu_usage"]
			if !ok {
				continue
			}
			pct, err := parsePercent("cpu_usage", v)
			if err != nil {
				return nil, fmt.Errorf("pod %q: %w", e.Name, err)
			}
			ns := e.Namespace
			if ns == "" {
				ns = defaultNamespace
			}
			labels := map[string]string{
				"__name__":  "pod_cpu_usage_percent",
				"namespace": ns,
				"pod":       e.Name,
			}
			if ref, ok := e.Spec["node"].(string); ok && ref != "" {
				if real, bound := bindings[ref]; bound {
					labels["node"] = real
				}
			}
			series = append(series, metricSeries{labels: labels, value: formatFloat(pct)})
		}
	}
	return series, nil
}

// applyNode handles a node state entry. The node itself is never created —
// the binding pass already mapped the declared name onto a real lab node —
// so what remains is the condition-fact half of the routing: verifying that
// every declared condition genuinely holds on the bound node.
func (si *stateInjector) applyNode(ctx context.Context, e StateEntry, pass *applyPass) error {
	realName, ok := pass.nodeBindings[e.Name]
	if !ok {
		// Unreachable: the binding pass covers every node entry.
		return fmt.Errorf("node %q has no lab node binding", e.Name)
	}
	if v, ok := e.Spec["memory_pressure"]; ok {
		declared, err := parseBoolFact("memory_pressure", v)
		if err != nil {
			return fmt.Errorf("node %q: %w", e.Name, err)
		}
		if err := si.verifyNodeMemoryPressure(ctx, e.Name, realName, declared); err != nil {
			return err
		}
	}
	return nil
}

// verifyNodeMemoryPressure checks a declared memory_pressure fact against the
// bound node's real conditions. Only memory_pressure: false is verifiable —
// manufacturing true pressure on a lab node is the unbuilt half of the node
// operation (see docs/backlog/node-state-entry-kind.md). A contradiction
// fails the provision loudly: the agent must never be scored against a
// cluster that disagrees with the scenario's declared world.
func (si *stateInjector) verifyNodeMemoryPressure(ctx context.Context, declaredName, realName string, declared bool) error {
	if declared {
		return fmt.Errorf("node %q: %q is not supported: petri verifies condition facts against the real node and does not manufacture pressure", declaredName, "memory_pressure: true")
	}
	raw, err := si.kube.GetResource(ctx, "node", "", realName)
	if err != nil {
		return fmt.Errorf("node %q: reading bound lab node %q: %w", declaredName, realName, err)
	}
	var node struct {
		Status struct {
			Conditions []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(raw), &node); err != nil {
		return fmt.Errorf("node %q: parsing bound lab node %q: %w", declaredName, realName, err)
	}
	for _, c := range node.Status.Conditions {
		if c.Type != "MemoryPressure" {
			continue
		}
		if strings.EqualFold(c.Status, "False") {
			return nil
		}
		return fmt.Errorf("node %q declares memory_pressure: false, but bound lab node %q reports MemoryPressure=%s; refusing to provision a cluster that contradicts the declared state", declaredName, realName, c.Status)
	}
	return fmt.Errorf("node %q: bound lab node %q reports no MemoryPressure condition, so the declared memory_pressure: false cannot be verified", declaredName, realName)
}

// parsePercent reads a declared percentage fact: "97%", "97", or a YAML
// number. Malformed values are loud errors — a usage fact that quietly
// materialises as nothing is the defect the fact routing exists to remove.
func parsePercent(field string, v any) (float64, error) {
	switch p := v.(type) {
	case string:
		s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(p), "%"))
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, fmt.Errorf("%s: cannot parse %q as a percentage", field, p)
		}
		return f, nil
	case float64:
		return p, nil
	case int:
		return float64(p), nil
	default:
		return 0, fmt.Errorf("%s: unsupported type %T for a percentage", field, v)
	}
}

// parseBoolFact reads a declared boolean fact (YAML bool or its string form).
func parseBoolFact(field string, v any) (bool, error) {
	switch b := v.(type) {
	case bool:
		return b, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(b)) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		return false, fmt.Errorf("%s: cannot parse %q as a boolean", field, b)
	default:
		return false, fmt.Errorf("%s: unsupported type %T for a boolean", field, v)
	}
}

// formatFloat renders a metric value the way the legacy builder did ("%g").
func formatFloat(f float64) string {
	return fmt.Sprintf("%g", f)
}

// parseAgoTimestamp reads the scenario vocabulary "<N>_<unit>_ago"
// (seconds, minutes, hours — singular or plural) into an absolute time
// relative to now. C-DA-003 declares last_oom_kill: 2_minutes_ago.
func parseAgoTimestamp(field, v string, now time.Time) (time.Time, error) {
	parts := strings.Split(strings.TrimSpace(v), "_")
	if len(parts) != 3 || parts[2] != "ago" {
		return time.Time{}, fmt.Errorf("%s: cannot parse %q; expected \"<N>_<seconds|minutes|hours>_ago\"", field, v)
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil || n < 0 {
		return time.Time{}, fmt.Errorf("%s: cannot parse %q; expected \"<N>_<seconds|minutes|hours>_ago\"", field, v)
	}
	var unit time.Duration
	switch parts[1] {
	case "second", "seconds":
		unit = time.Second
	case "minute", "minutes":
		unit = time.Minute
	case "hour", "hours":
		unit = time.Hour
	default:
		return time.Time{}, fmt.Errorf("%s: cannot parse %q; expected \"<N>_<seconds|minutes|hours>_ago\"", field, v)
	}
	return now.Add(-time.Duration(n) * unit), nil
}

// renderPromResponses serializes the series set into the responses.json the
// mock Prometheus serves: an instant-query vector always, and a range-query
// matrix whenever any series declares a range shape (constant series are
// rendered flat across the same window so every channel of the mock tells
// the same story).
func renderPromResponses(series []metricSeries, now time.Time) (string, error) {
	instant := make([]any, 0, len(series))
	for _, s := range series {
		instant = append(instant, map[string]any{
			"metric": s.labels,
			"value":  []any{now.Unix(), s.value},
		})
	}
	resp := map[string]any{
		"query": map[string]any{
			"status": "success",
			"data":   map[string]any{"resultType": "vector", "result": instant},
		},
	}

	windowStart := now
	hasRange := false
	for _, s := range series {
		if len(s.rangeSamples) > 0 {
			hasRange = true
			if s.rangeSamples[0].at.Before(windowStart) {
				windowStart = s.rangeSamples[0].at
			}
		}
	}
	if hasRange {
		matrix := make([]any, 0, len(series))
		for _, s := range series {
			var values []any
			if len(s.rangeSamples) > 0 {
				for _, p := range s.rangeSamples {
					values = append(values, []any{p.at.Unix(), p.value})
				}
			} else {
				values = []any{
					[]any{windowStart.Unix(), s.value},
					[]any{now.Unix(), s.value},
				}
			}
			matrix = append(matrix, map[string]any{
				"metric": s.labels,
				"values": values,
			})
		}
		resp["query_range"] = map[string]any{
			"status": "success",
			"data":   map[string]any{"resultType": "matrix", "result": matrix},
		}
	}

	b, err := json.Marshal(resp)
	if err != nil {
		return "", fmt.Errorf("marshaling metrics responses: %w", err)
	}
	return string(b), nil
}
