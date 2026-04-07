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
	LabStatusExpiring   LabStatus = "EXPIRING"
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
}

// IsExpired returns true if the lab has passed its TTL.
func (l *Lab) IsExpired() bool {
	return time.Now().After(l.ExpiresAt)
}

// CanTransitionTo returns true if the given status transition is valid.
func (l *Lab) CanTransitionTo(next LabStatus) bool {
	valid := map[LabStatus][]LabStatus{
		LabStatusCreating:   {LabStatusActive, LabStatusError},
		LabStatusActive:     {LabStatusExpiring, LabStatusDestroying, LabStatusError},
		LabStatusExpiring:   {LabStatusDestroying, LabStatusActive, LabStatusError},
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
