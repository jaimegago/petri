package terraform

import (
	"encoding/json"
	"fmt"
	"strings"
)

// parseOutputs parses the JSON produced by `terraform output -json`.
func parseOutputs(jsonStr string) (map[string]OutputValue, error) {
	if strings.TrimSpace(jsonStr) == "" {
		return map[string]OutputValue{}, nil
	}
	var raw map[string]struct {
		Value     json.RawMessage `json:"value"`
		Type      json.RawMessage `json:"type"`
		Sensitive bool            `json:"sensitive"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
		return nil, fmt.Errorf("parsing outputs JSON: %w", err)
	}
	result := make(map[string]OutputValue, len(raw))
	for k, v := range raw {
		result[k] = OutputValue{
			Value:     v.Value,
			Type:      v.Type,
			Sensitive: v.Sensitive,
		}
	}
	return result, nil
}

// parseResources parses the JSON produced by `terraform show -json` and
// returns a flat list of resources with their cloud IDs.
func parseResources(jsonStr string) ([]ResourceInfo, error) {
	if strings.TrimSpace(jsonStr) == "" {
		return nil, nil
	}
	var show struct {
		Values struct {
			RootModule struct {
				Resources []struct {
					Address string                     `json:"address"`
					Type    string                     `json:"type"`
					Name    string                     `json:"name"`
					Values  map[string]json.RawMessage `json:"values"`
				} `json:"resources"`
			} `json:"root_module"`
		} `json:"values"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &show); err != nil {
		return nil, fmt.Errorf("parsing terraform show JSON: %w", err)
	}
	var resources []ResourceInfo
	for _, r := range show.Values.RootModule.Resources {
		resources = append(resources, ResourceInfo{
			Address:    r.Address,
			Type:       r.Type,
			Name:       r.Name,
			ResourceID: extractResourceID(r.Values),
		})
	}
	return resources, nil
}

// extractResourceID returns the most specific ID value from a resource's
// attribute map. It checks "id", "arn", and "name" in order.
func extractResourceID(values map[string]json.RawMessage) string {
	for _, key := range []string{"id", "arn", "name"} {
		if raw, ok := values[key]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil && s != "" {
				return s
			}
		}
	}
	return ""
}

// extractPlanSummary returns the summary line from `terraform plan` output,
// e.g. "Plan: 3 to add, 0 to change, 0 to destroy." or "No changes."
func extractPlanSummary(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Plan:") || strings.HasPrefix(line, "No changes.") {
			return line
		}
	}
	return ""
}
