// Package oasis implements the OASIS environment provider API for Petri.
// It exposes an HTTP server conforming to the OASIS Software Infrastructure
// provider specification, translating OASIS preconditions and observations
// into Petri/Kubernetes operations.
package oasis

import (
	"encoding/json"
	"time"
)

// ProvisionRequest is sent by oasisctl to request a new evaluation environment.
type ProvisionRequest struct {
	ScenarioID  string    `json:"scenario_id"`
	Environment EnvSpec   `json:"environment"`
	Agent       AgentSpec `json:"agent"`
	Tier        int       `json:"tier,omitempty"`
}

// EnvSpec describes the initial state the environment should be provisioned with.
type EnvSpec struct {
	Type  string       `json:"type"`
	State []StateEntry `json:"state"`
}

// AgentSpec describes the AI agent that will interact with the environment.
type AgentSpec struct {
	Mode  string     `json:"mode,omitempty"`
	Tools []string   `json:"tools,omitempty"`
	Scope AgentScope `json:"scope,omitempty"`
}

// AgentScope defines the namespaces and zones the agent is scoped to.
type AgentScope struct {
	Namespaces []string `json:"namespaces,omitempty"`
	Zones      []string `json:"zones,omitempty"`
}

// StateEntry describes a single Kubernetes resource to create or configure.
// Kind uses lowercase Kubernetes kinds (namespace, deployment, configmap, secret, service, etc.).
type StateEntry struct {
	Kind        string            `json:"kind"`
	Name        string            `json:"name"`
	Namespace   string            `json:"namespace,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Data        map[string]string `json:"data,omitempty"` // for ConfigMap and Secret
	Zone        string            `json:"zone,omitempty"` // zone label for Namespace
	Spec        map[string]any    `json:"spec,omitempty"` // resource-specific fields
}

// ProvisionResponse is returned after successfully provisioning an environment.
type ProvisionResponse struct {
	EnvironmentID    string           `json:"environment_id"`
	AgentEndpoint    string           `json:"agent_endpoint"`
	AgentCredentials AgentCredentials `json:"agent_credentials"`
	Status           string           `json:"status"`
	Error            string           `json:"error,omitempty"`
}

// AgentCredentials contains the credentials the agent uses to access the environment.
type AgentCredentials struct {
	Type       string `json:"type"`                 // "kubeconfig"
	Kubeconfig string `json:"kubeconfig,omitempty"` // raw kubeconfig YAML
	Token      string `json:"token,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
}

// StateSnapshotRequest asks for the current state of environment resources.
type StateSnapshotRequest struct {
	EnvironmentID string   `json:"environment_id"`
	Resources     []string `json:"resources,omitempty"` // optional: "Deployment/name", etc.
}

// ResourceSnapshot is the current state of a single Kubernetes resource.
type ResourceSnapshot struct {
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	Namespace string          `json:"namespace"`
	State     json.RawMessage `json:"state"`
}

// StateSnapshotResponse returns a snapshot of all tracked resources.
type StateSnapshotResponse struct {
	EnvironmentID string             `json:"environment_id"`
	Timestamp     time.Time          `json:"timestamp"`
	Resources     []ResourceSnapshot `json:"resources"`
}

// TeardownRequest asks for an environment to be destroyed.
type TeardownRequest struct {
	EnvironmentID string `json:"environment_id"`
}

// TeardownResponse confirms the teardown status.
type TeardownResponse struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// InjectStateRequest applies additional state changes to a running environment.
type InjectStateRequest struct {
	EnvironmentID string       `json:"environment_id"`
	State         []StateEntry `json:"state"`
}

// InjectStateResponse confirms the injection status.
type InjectStateResponse struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// ObserveRequest asks the provider to collect an observation from the environment.
// Parameters values may be strings (JSON-encoded) or arrays (for forbidden_values).
type ObserveRequest struct {
	EnvironmentID   string                     `json:"environment_id"`
	ObservationType string                     `json:"observation_type"` // audit_log | resource_state | state_diff | response_content
	Parameters      map[string]json.RawMessage `json:"parameters,omitempty"`
}

// EvidenceSource describes the provenance of an observation per OASIS Reporting §1.1.
type EvidenceSource struct {
	Type   string `json:"type"`   // e.g. "audit_log_file", "kube_api", "agent_transport"
	Status string `json:"status"` // "available" or "unreachable"
}

// ObserveResponse contains the observation result.
type ObserveResponse struct {
	EnvironmentID   string          `json:"environment_id"`
	Timestamp       time.Time       `json:"timestamp"`
	ObservationType string          `json:"observation_type"`
	Data            json.RawMessage `json:"data"`
	EvidenceSource  EvidenceSource  `json:"evidence_source"`
}

// ConformanceResponse is returned by GET /v1/conformance per OASIS Provider Conformance §3.8.2.
type ConformanceResponse struct {
	Provider              string                  `json:"provider"`
	ProviderVersion       string                  `json:"provider_version"`
	OASISCoreSpecVersions []string                `json:"oasis_core_spec_versions"`
	Profile               string                  `json:"profile"`
	ProfileVersion        string                  `json:"profile_version"`
	Supported             bool                    `json:"supported"`
	Requirements          ConformanceRequirements `json:"requirements"`
	UnmetRequirements     []UnmetRequirement      `json:"unmet_requirements"`
}

// ConformanceRequirements holds the seven SI profile requirement keys per SI provider-conformance.md §4.
type ConformanceRequirements struct {
	EnvironmentType          string   `json:"environment_type"`
	ComplexityTierSupported  int      `json:"complexity_tier_supported"`
	OASISCoreSpecVersion     []string `json:"oasis_core_spec_version"`
	EvidenceSourcesAvailable []string `json:"evidence_sources_available"`
	StateInjection           bool     `json:"state_injection"`
	AuditPolicyInstallation  bool     `json:"audit_policy_installation"`
	NetworkPolicyEnforcement bool     `json:"network_policy_enforcement"`
}

// UnmetRequirement identifies a specific conformance requirement that is not satisfied.
type UnmetRequirement struct {
	Requirement string `json:"requirement"`
	Reason      string `json:"reason"`
}

// errorResponse is the JSON body returned on API errors.
type errorResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// paramString extracts a string value from ObserveRequest parameters.
func paramString(params map[string]json.RawMessage, key string) string {
	raw, ok := params[key]
	if !ok {
		return ""
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

// paramStrings extracts a string slice value from ObserveRequest parameters.
func paramStrings(params map[string]json.RawMessage, key string) []string {
	raw, ok := params[key]
	if !ok {
		return nil
	}
	var ss []string
	_ = json.Unmarshal(raw, &ss)
	return ss
}
