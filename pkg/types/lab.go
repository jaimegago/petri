// Package types defines the core domain types for Petri labs, companies, and credentials.
package types

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// LabStatus represents the lifecycle state of a lab.
type LabStatus string

const (
	LabStatusCreating   LabStatus = "CREATING"
	LabStatusActive     LabStatus = "ACTIVE"
	LabStatusExpired    LabStatus = "EXPIRED"
	LabStatusDestroying LabStatus = "DESTROYING"
	LabStatusDestroyed  LabStatus = "DESTROYED"
	LabStatusError      LabStatus = "ERROR"
)

// CloudProvider identifies the cloud platform for a lab.
type CloudProvider string

const (
	CloudProviderLocal CloudProvider = "local"
	CloudProviderAWS   CloudProvider = "aws"
	CloudProviderAzure CloudProvider = "azure"
	CloudProviderGCP   CloudProvider = "gcp"
)

// Lab represents a running or historical lab instance.
type Lab struct {
	ID            uuid.UUID     `json:"id"`
	Name          string        `json:"name"`
	Company       string        `json:"company"`
	Level         int           `json:"level"`
	CloudProvider CloudProvider `json:"cloud_provider"`
	Status        LabStatus     `json:"status"`
	CreatedAt     time.Time     `json:"created_at"`
	TTLHours      int           `json:"ttl_hours"`
	ExpiresAt     time.Time     `json:"expires_at"`
	Metadata      LabMetadata   `json:"metadata"`
}

// LabMetadata holds supplementary data for a lab.
type LabMetadata struct {
	GitRepos          []GitRepo         `json:"git_repos,omitempty"`
	Clusters          []Cluster         `json:"clusters,omitempty"`
	ObservabilityURLs map[string]string `json:"observability_urls,omitempty"`
	WorkDir           string            `json:"work_dir,omitempty"`
	ErrorMessage      string            `json:"error_message,omitempty"`
	// GCPProjectID is the GCP project a GCP lab was created in. Recorded at
	// create so teardown targets the same project whatever the config says
	// by then.
	GCPProjectID string `json:"gcp_project_id,omitempty"`
}

// GitRepo tracks a created git repository.
type GitRepo struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Type string `json:"type"` // infra, gitops, apps
}

// Cluster tracks a Kubernetes cluster within a lab.
type Cluster struct {
	Name            string `json:"name"`
	CloudResourceID string `json:"cloud_resource_id,omitempty"`
	KubeconfigPath  string `json:"kubeconfig_path,omitempty"`
	Endpoint        string `json:"endpoint,omitempty"`
	NodeCount       int    `json:"node_count"`
	AuditLogPath    string `json:"audit_log_path,omitempty"`
	OASISMode       bool   `json:"oasis_mode,omitempty"`
	// NodeShape records the allocatable shaping applied to the cluster's
	// worker nodes at OASIS lab creation. Nil for labs that predate shaping
	// or were not created with --oasis on the local path.
	NodeShape *NodeShape `json:"node_shape,omitempty"`
	// IngressHostPorts records the host ports a local kind cluster's ingress
	// NodePorts are published on. Allocated per lab so labs can coexist on
	// one host. Nil for cloud clusters and for local labs that predate
	// per-lab allocation (those used the Legacy*HostPort constants).
	IngressHostPorts *IngressHostPorts `json:"ingress_host_ports,omitempty"`
}

// IngressHostPorts is the pair of host ports on which a local lab's ingress
// (HTTP and HTTPS NodePorts on the kind control-plane node) is reachable at
// localhost.
type IngressHostPorts struct {
	HTTP  int `json:"http"`
	HTTPS int `json:"https"`
}

// The fixed host ports every local lab published its ingress on before
// per-lab allocation. They remain the answer for a local lab whose state
// records no IngressHostPorts, because that is what its kind cluster was
// created with.
const (
	LegacyIngressHTTPHostPort  = 30080
	LegacyIngressHTTPSHostPort = 30443
)

// LocalIngressHostPorts returns the host ports a local lab's ingress is
// published on, and false for a lab that is not local or records no
// cluster. A local lab created before per-lab allocation reports the
// legacy fixed ports.
func (l *Lab) LocalIngressHostPorts() (IngressHostPorts, bool) {
	if l.CloudProvider != CloudProviderLocal || len(l.Metadata.Clusters) == 0 {
		return IngressHostPorts{}, false
	}
	if p := l.Metadata.Clusters[0].IngressHostPorts; p != nil {
		return *p, true
	}
	return IngressHostPorts{HTTP: LegacyIngressHTTPHostPort, HTTPS: LegacyIngressHTTPSHostPort}, true
}

// NodeShape is the outcome of shaping an OASIS lab's worker nodes to a
// homogeneous allocatable target. Allocatable is a lab property, shaped once
// at creation; scenarios only verify it. When the host cannot reach the
// target the lab is still created, unshaped, and Reason says why — provision
// quotes it when a declared allocatable fact is refused.
type NodeShape struct {
	// Shaped is true when every worker reports exactly the target allocatable.
	Shaped bool `json:"shaped"`
	// TargetCPU and TargetMemory are the allocatable quantities shaping aims at.
	TargetCPU    string `json:"target_cpu"`
	TargetMemory string `json:"target_memory"`
	// Reason explains why the lab is unshaped. Empty when Shaped.
	Reason string `json:"reason,omitempty"`
	// Nodes is the status.allocatable of every worker node after shaping
	// (or as found, when unshaped).
	Nodes []NodeAllocatable `json:"nodes,omitempty"`
}

// NodeAllocatable is one node's observed status.allocatable.
type NodeAllocatable struct {
	Name   string `json:"name"`
	CPU    string `json:"cpu"`
	Memory string `json:"memory"`
}

// Summary renders the shape as one line for logs and provision errors.
func (s *NodeShape) Summary() string {
	if s == nil {
		return "the lab records no node shaping (created before allocatable shaping existed, or not via petri create --oasis on the local path)"
	}
	if s.Shaped {
		return fmt.Sprintf("the lab is shaped: every worker reports allocatable cpu=%s memory=%s", s.TargetCPU, s.TargetMemory)
	}
	return fmt.Sprintf("the lab is unshaped (target cpu=%s memory=%s): %s", s.TargetCPU, s.TargetMemory, s.Reason)
}

// IsExpired returns true if the lab has passed its TTL.
func (l *Lab) IsExpired() bool {
	return time.Now().After(l.ExpiresAt)
}

// IsStrandedCreating returns true when the lab is still in CREATING and was
// created more than threshold ago — the heuristic the lab reaper and CLI
// destroy command share to recover from a crashed petri-create without
// accidentally yanking a lab that is still mid-create. See ADR 0013.
func (l *Lab) IsStrandedCreating(threshold time.Duration) bool {
	return l.Status == LabStatusCreating && time.Since(l.CreatedAt) > threshold
}

// CanTransitionTo returns true if the given status transition is valid.
//
// Transition table:
//
//	CREATING   → ACTIVE, ERROR, DESTROYING
//	ACTIVE     → EXPIRED, DESTROYING, ERROR
//	EXPIRED    → DESTROYING, ERROR
//	DESTROYING → DESTROYED, ERROR
//	DESTROYED  → (terminal)
//	ERROR      → DESTROYING
//
// CREATING → DESTROYING is permitted only for labs stranded in CREATING
// longer than the stranded-create timeout, to recover from petri-create
// crashes (see pkg/orchestrator/cleanup.go and ADR 0013). Callers must
// gate on the lab's age before using this transition.
func (l *Lab) CanTransitionTo(next LabStatus) bool {
	valid := map[LabStatus][]LabStatus{
		LabStatusCreating:   {LabStatusActive, LabStatusError, LabStatusDestroying},
		LabStatusActive:     {LabStatusExpired, LabStatusDestroying, LabStatusError},
		LabStatusExpired:    {LabStatusDestroying, LabStatusError},
		LabStatusDestroying: {LabStatusDestroyed, LabStatusError},
		LabStatusDestroyed:  {},
		LabStatusError:      {LabStatusDestroying},
	}
	for _, s := range valid[l.Status] {
		if s == next {
			return true
		}
	}
	return false
}

// Validate checks required fields on a Lab.
func (l *Lab) Validate() error {
	if l.Name == "" {
		return fmt.Errorf("lab name is required")
	}
	if l.Company == "" {
		return fmt.Errorf("company is required")
	}
	if l.Level < 1 || l.Level > 3 {
		return fmt.Errorf("level must be 1, 2, or 3")
	}
	return nil
}
