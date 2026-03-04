package pulumi

import (
	"encoding/json"
	"fmt"
	"strings"
)

// parseOutputs parses the JSON produced by `pulumi stack output --json`.
// The format is a flat object: {"key": value, ...} where values are raw JSON.
func parseOutputs(jsonStr string) (map[string]OutputValue, error) {
	if strings.TrimSpace(jsonStr) == "" {
		return map[string]OutputValue{}, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
		return nil, fmt.Errorf("parsing pulumi outputs JSON: %w", err)
	}
	result := make(map[string]OutputValue, len(raw))
	for k, v := range raw {
		result[k] = OutputValue{Value: v}
	}
	return result, nil
}

// parseResources parses the JSON produced by `pulumi stack export` and returns
// a flat list of managed cloud resources, excluding the internal Stack resource.
//
// The checkpoint format is:
//
//	{"version":3,"deployment":{"resources":[{"urn":...,"type":...,"id":...}]}}
func parseResources(jsonStr string) ([]ResourceInfo, error) {
	if strings.TrimSpace(jsonStr) == "" {
		return nil, nil
	}
	var checkpoint struct {
		Deployment struct {
			Resources []struct {
				URN  string `json:"urn"`
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"resources"`
		} `json:"deployment"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &checkpoint); err != nil {
		return nil, fmt.Errorf("parsing pulumi stack export JSON: %w", err)
	}
	var resources []ResourceInfo
	for _, r := range checkpoint.Deployment.Resources {
		// Skip the pulumi meta-resource (the Stack bookkeeping entry itself).
		if r.Type == "pulumi:pulumi:Stack" {
			continue
		}
		resources = append(resources, ResourceInfo{
			URN:        r.URN,
			Type:       r.Type,
			Name:       extractResourceName(r.URN),
			ResourceID: r.ID,
		})
	}
	return resources, nil
}

// extractResourceName parses the logical resource name from a Pulumi URN.
// URN format: urn:pulumi:<stack>::<project>::<parent$type>::<name>
func extractResourceName(urn string) string {
	parts := strings.Split(urn, "::")
	if len(parts) >= 4 {
		return parts[len(parts)-1]
	}
	return urn
}

// extractHasChanges reports whether a pulumi preview output contains resource changes.
func extractHasChanges(output string) bool {
	lower := strings.ToLower(output)
	if strings.Contains(lower, "no changes") {
		return false
	}
	// Presence of create/update/delete markers indicates pending changes.
	return strings.Contains(output, " + ") ||
		strings.Contains(output, " - ") ||
		strings.Contains(output, " ~ ") ||
		strings.Contains(lower, "to create") ||
		strings.Contains(lower, "to update") ||
		strings.Contains(lower, "to delete") ||
		strings.Contains(lower, "to replace")
}

// extractPreviewSummary returns the resource summary lines from pulumi preview
// output. It locates the "Resources:" section and collects the lines that follow.
func extractPreviewSummary(output string) string {
	lines := strings.Split(output, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Resources:") {
			var summary []string
			for j := i + 1; j < len(lines) && j < i+5; j++ {
				s := strings.TrimSpace(lines[j])
				if s == "" || strings.HasPrefix(s, "Duration:") || strings.HasPrefix(s, "View Live:") {
					break
				}
				summary = append(summary, s)
			}
			if len(summary) > 0 {
				return strings.Join(summary, ", ")
			}
			return trimmed
		}
	}
	return ""
}
