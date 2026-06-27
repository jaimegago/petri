// Package manifest holds pure helpers for emitting Kubernetes YAML fragments
// by hand. It has no cluster dependency: every function maps Go values to
// indented YAML strings, so it can be unit-tested without kubectl or a live
// API server.
//
// The helpers live here, rather than alongside any single manifest builder,
// because both the OASIS state translator (pkg/oasis) and the workload-state
// capability (pkg/workloadstate) build manifests by hand and need the same
// label/annotation serialisation. See ADR 0015.
//
// All emitted scalar values are quoted (%q) so that label and annotation
// values like "true", "1", or "8080" are not coerced to YAML bools/numbers.
package manifest

import (
	"fmt"
	"strings"
)

// LabelsToYAML renders a string map as indented `key: "value"` lines, one per
// entry, joined by newlines (no trailing newline). It returns the empty string
// for an empty map. Values are always quoted to prevent YAML type coercion.
func LabelsToYAML(labels map[string]string, indent int) string {
	if len(labels) == 0 {
		return ""
	}
	prefix := strings.Repeat(" ", indent)
	var lines []string
	for k, v := range labels {
		lines = append(lines, fmt.Sprintf("%s%s: %q", prefix, k, v))
	}
	return strings.Join(lines, "\n")
}

// MergeLabels returns a new map containing base overlaid with extra. Keys in
// extra win on collision. Neither input is mutated.
func MergeLabels(base, extra map[string]string) map[string]string {
	result := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		result[k] = v
	}
	for k, v := range extra {
		result[k] = v
	}
	return result
}

// MergeAnnotations returns a copy of annotations with an optional
// app.kubernetes.io/managed-by entry appended when managedBy is non-empty.
// The input map is not mutated.
func MergeAnnotations(annotations map[string]string, managedBy string) map[string]string {
	result := make(map[string]string, len(annotations)+1)
	for k, v := range annotations {
		result[k] = v
	}
	if managedBy != "" {
		result["app.kubernetes.io/managed-by"] = managedBy
	}
	return result
}

// WriteAnnotationsYAML appends a `metadata.annotations` block to sb when the
// map is non-empty. It is a no-op for an empty map, so callers can invoke it
// unconditionally.
func WriteAnnotationsYAML(sb *strings.Builder, annotations map[string]string) {
	if len(annotations) > 0 {
		sb.WriteString("  annotations:\n")
		sb.WriteString(LabelsToYAML(annotations, 4))
		sb.WriteString("\n")
	}
}
