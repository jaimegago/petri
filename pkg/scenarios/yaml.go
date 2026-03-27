package scenarios

import (
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadFile parses a scenario definition from a YAML file at path.
// It returns an error if the file cannot be read or the YAML is malformed.
// The returned Scenario is not validated; call Validate to check fault types
// and targets against a running lab topology.
func LoadFile(path string) (*Scenario, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening scenario file %q: %w", path, err)
	}
	defer f.Close() //nolint:errcheck

	return LoadReader(f)
}

// LoadReader parses a scenario definition from r.
// It returns an error if the YAML is malformed or the scenario has no name.
func LoadReader(r io.Reader) (*Scenario, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reading scenario: %w", err)
	}

	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing scenario YAML: %w", err)
	}

	if s.Name == "" {
		return nil, fmt.Errorf("scenario YAML is missing required field \"name\"")
	}

	return &s, nil
}
