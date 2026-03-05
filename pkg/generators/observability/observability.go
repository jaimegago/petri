// Package observability generates Kubernetes manifests for the observability
// stack (Prometheus, Grafana, etc.) appropriate for each company and complexity level.
package observability

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"text/template"

	"github.com/jaimegago/petri/pkg/generators"
	petritemplates "github.com/jaimegago/petri/templates"
)

// Generator renders observability stack manifests based on the lab context.
type Generator interface {
	// Generate returns manifests for all observability tools listed in the level spec.
	Generate(ctx context.Context, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error)
}

type generator struct {
	fs fs.FS
}

// New returns a Generator backed by the embedded template filesystem.
func New() Generator {
	return &generator{fs: petritemplates.FS}
}

// NewWithFS returns a Generator backed by a custom filesystem. Intended for tests.
func NewWithFS(fsys fs.FS) Generator {
	return &generator{fs: fsys}
}

// Generate renders manifests for each observability tool in the level spec.
// Unknown tools are silently skipped (not all tools have templates).
func (g *generator) Generate(_ context.Context, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error) {
	var files []generators.RenderedFile

	for _, tool := range tmplCtx.Level.Observability {
		rendered, err := g.renderTool(tool, tmplCtx)
		if err != nil {
			return nil, fmt.Errorf("rendering observability tool %q: %w", tool, err)
		}
		files = append(files, rendered...)
	}
	return files, nil
}

// renderTool renders manifests for a single observability tool.
// Returns an empty slice for tools without templates (not an error).
func (g *generator) renderTool(tool string, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error) {
	tmplPath, outPath := templatePathFor(tool)
	if tmplPath == "" {
		// No template for this tool — skip silently.
		return nil, nil
	}

	content, err := g.renderFile(tmplPath, tmplCtx)
	if err != nil {
		return nil, err
	}
	return []generators.RenderedFile{{Path: outPath, Content: content}}, nil
}

// templatePathFor maps an observability tool name to its template and output paths.
func templatePathFor(tool string) (tmplPath, outPath string) {
	switch tool {
	case "prometheus":
		return "observability/prometheus.yaml.tmpl", "observability/prometheus.yaml"
	case "grafana":
		return "observability/grafana.yaml.tmpl", "observability/grafana.yaml"
	default:
		// Tools like loki, tempo, jaeger, otel-collector, cloud-monitoring, cloud-logging
		// are managed by cloud-native integrations or Helm charts outside Petri templates.
		return "", ""
	}
}

func (g *generator) renderFile(templatePath string, data any) (string, error) {
	raw, err := fs.ReadFile(g.fs, templatePath)
	if err != nil {
		return "", fmt.Errorf("reading template %s: %w", templatePath, err)
	}
	tmpl, err := template.New(templatePath).Funcs(generators.TemplateFuncs()).Parse(string(raw))
	if err != nil {
		return "", fmt.Errorf("parsing template %s: %w", templatePath, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing template %s: %w", templatePath, err)
	}
	return buf.String(), nil
}
