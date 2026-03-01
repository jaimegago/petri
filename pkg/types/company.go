package types

import "fmt"

// IaCTool identifies the infrastructure-as-code tool used by a company.
type IaCTool string

const (
	IaCToolTerraform      IaCTool = "terraform"
	IaCToolPulumi         IaCTool = "pulumi"
	IaCToolCloudFormation IaCTool = "cloudformation"
)

// GitOpsTool identifies the GitOps tool used by a company.
type GitOpsTool string

const (
	GitOpsToolArgoCD GitOpsTool = "argocd"
	GitOpsToolFlux   GitOpsTool = "flux"
	GitOpsToolAnthos GitOpsTool = "anthos"
)

// GitProvider identifies where repositories are hosted.
type GitProvider string

const (
	GitProviderGitHub GitProvider = "github"
	GitProviderGitLab GitProvider = "gitlab"
)

// Author represents a team member persona for realistic git history.
type Author struct {
	Name  string `yaml:"name"  json:"name"`
	Email string `yaml:"email" json:"email"`
	Role  string `yaml:"role"  json:"role"`
}

// Database describes a managed database to provision.
type Database struct {
	Type          string `yaml:"type"           json:"type"`
	EngineVersion string `yaml:"engine_version" json:"engine_version"`
	InstanceClass string `yaml:"instance_class" json:"instance_class"`
	NodeType      string `yaml:"node_type"      json:"node_type,omitempty"`
}

// LevelSpec defines the resources and components for a complexity level.
type LevelSpec struct {
	Clusters          int        `yaml:"clusters"`
	ClusterNames      []string   `yaml:"cluster_names"`
	NodesPerCluster   []int      `yaml:"nodes_per_cluster"`
	NodeInstanceTypes []string   `yaml:"node_instance_types"`
	NodeInstanceType  string     `yaml:"node_instance_type"`
	Apps              []string   `yaml:"apps"`
	Platform          []string   `yaml:"platform"`
	Observability     []string   `yaml:"observability"`
	Databases         []Database `yaml:"databases"`
	Namespaces        []string   `yaml:"namespaces"`
	TTLDefaultHours   int        `yaml:"ttl_default_hours"`
}

// Company defines a simulated company profile.
type Company struct {
	Name          string            `yaml:"name"           json:"name"`
	Description   string            `yaml:"description"    json:"description"`
	CloudProvider CloudProvider     `yaml:"cloud_provider" json:"cloud_provider"`
	IaCTool       IaCTool           `yaml:"iac_tool"       json:"iac_tool"`
	GitOpsTool    GitOpsTool        `yaml:"gitops_tool"    json:"gitops_tool"`
	GitProvider   GitProvider       `yaml:"git_provider"   json:"git_provider"`
	GitHubOrg     string            `yaml:"github_org"     json:"github_org,omitempty"`
	GitLabGroup   string            `yaml:"gitlab_group"   json:"gitlab_group,omitempty"`
	CICD          string            `yaml:"ci_cd"          json:"ci_cd,omitempty"`
	Authors       []Author          `yaml:"authors"        json:"authors"`
	Levels        map[int]LevelSpec `yaml:"levels"    json:"levels"`
}

// LevelSpec returns the specification for the given complexity level.
func (c *Company) GetLevel(level int) (LevelSpec, error) {
	spec, ok := c.Levels[level]
	if !ok {
		return LevelSpec{}, fmt.Errorf("company %q has no definition for level %d", c.Name, level)
	}
	return spec, nil
}

// Validate checks required fields on a Company.
func (c *Company) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("company name is required")
	}
	if c.CloudProvider == "" {
		return fmt.Errorf("company %q: cloud_provider is required", c.Name)
	}
	if c.IaCTool == "" {
		return fmt.Errorf("company %q: iac_tool is required", c.Name)
	}
	if len(c.Levels) == 0 {
		return fmt.Errorf("company %q: at least one level must be defined", c.Name)
	}
	return nil
}
