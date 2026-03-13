// Package generators provides shared types, context construction, and template
// utilities used by all Petri code generators (IaC, GitOps, Apps, Commits).
package generators

import (
	"strings"
	"text/template"
	"time"

	"github.com/jaimegago/petri/pkg/types"
)

// TemplateContext holds all data required to render any Petri template.
type TemplateContext struct {
	Lab     LabInfo
	Company CompanyInfo
	Level   LevelInfo
	Now     time.Time
}

// LabInfo holds lab-level data for templates.
type LabInfo struct {
	ID        string
	Name      string
	LabType   string
	CreatedAt string
}

// CompanyInfo holds company-level data for templates.
type CompanyInfo struct {
	Name          string
	CloudProvider string
	IaCTool       string
	GitOpsTool    string
	GitProvider   string
	GitHubOrg     string
	CICD          string
}

// LevelInfo holds complexity-level data for templates.
type LevelInfo struct {
	Number        int
	ClusterNames  []string
	NodeCounts    []int
	InstanceTypes []string
	Apps          []string
	Platform      []string
	Observability []string
	Databases     []DatabaseInfo
	Namespaces    []string
}

// DatabaseInfo describes a managed database for template rendering.
type DatabaseInfo struct {
	Type          string
	EngineVersion string
	InstanceClass string
	NodeType      string
}

// RenderedFile pairs a relative output path with its rendered content.
type RenderedFile struct {
	Path    string
	Content string
}

// AppTemplateContext extends TemplateContext with per-application fields.
// It is used by the apps and gitops generators when rendering per-app templates.
type AppTemplateContext struct {
	TemplateContext
	AppName         string
	Namespace       string
	Replicas        int
	Port            int
	ImageRepository string
	ImageTag        string
	CPURequest      string
	MemoryRequest   string
	CPULimit        string
	MemoryLimit     string
}

// NewTemplateContext builds a TemplateContext from Petri domain types.
func NewTemplateContext(lab *types.Lab, company *types.Company, level types.LevelSpec) TemplateContext {
	databases := make([]DatabaseInfo, len(level.Databases))
	for i, db := range level.Databases {
		databases[i] = DatabaseInfo{
			Type:          db.Type,
			EngineVersion: db.EngineVersion,
			InstanceClass: db.InstanceClass,
			NodeType:      db.NodeType,
		}
	}
	return TemplateContext{
		Lab: LabInfo{
			ID:        lab.ID.String(),
			Name:      lab.Name,
			LabType:   string(lab.CloudProvider),
			CreatedAt: lab.CreatedAt.Format(time.RFC3339),
		},
		Company: CompanyInfo{
			Name:          company.Name,
			CloudProvider: string(company.CloudProvider),
			IaCTool:       string(company.IaCTool),
			GitOpsTool:    string(company.GitOpsTool),
			GitProvider:   string(company.GitProvider),
			GitHubOrg:     company.GitHubOrg,
			CICD:          company.CICD,
		},
		Level: LevelInfo{
			Number:        lab.Level,
			ClusterNames:  level.ClusterNames,
			NodeCounts:    level.NodesPerCluster,
			InstanceTypes: level.NodeInstanceTypes,
			Apps:          level.Apps,
			Platform:      level.Platform,
			Observability: level.Observability,
			Databases:     databases,
			Namespaces:    level.Namespaces,
		},
		Now: time.Now(),
	}
}

// TemplateFuncs returns the standard function map injected into all Petri templates.
func TemplateFuncs() template.FuncMap {
	return template.FuncMap{
		// Numeric comparisons for level-gating template blocks.
		"levelGte": func(current, threshold int) bool { return current >= threshold },
		"levelEq":  func(current, target int) bool { return current == target },

		// Slice membership check.
		"contains": func(items []string, item string) bool {
			for _, i := range items {
				if i == item {
					return true
				}
			}
			return false
		},

		// String utilities.
		"join":    strings.Join,
		"replace": strings.ReplaceAll,
		"upper":   strings.ToUpper,
		"lower":   strings.ToLower,
		"title":   strings.Title, //nolint:staticcheck // used for template identifier casing only
		"quote":   func(s string) string { return `"` + s + `"` },

		// Indentation helper for nested YAML/HCL blocks.
		"indent": func(n int, s string) string {
			pad := strings.Repeat(" ", n)
			return pad + strings.ReplaceAll(s, "\n", "\n"+pad)
		},

		// Basic arithmetic.
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		"mul": func(a, b int) int { return a * b },
	}
}
