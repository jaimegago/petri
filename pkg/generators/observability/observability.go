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
	// Generate returns manifests for the observability tools in the level
	// spec that petri installs.
	Generate(ctx context.Context, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error)
	// Unrendered returns the tools the level spec lists and Generate
	// produces nothing for, in the order listed, for a caller to report.
	Unrendered(tmplCtx generators.TemplateContext) []string
	// Deployments returns the Deployments the rendered tools create, for a
	// caller to wait on.
	Deployments(tmplCtx generators.TemplateContext) []generators.Deployment
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

// tool is what the generator installs for one observability tool name.
type tool struct {
	// source is the template; out is the rendered path.
	source, out string
	// deployments are the Deployments the manifest creates.
	deployments []generators.Deployment
}

// tools is every observability tool petri installs. A name a level spec lists
// and this table lacks — loki, tempo, jaeger, otel-collector,
// cloud-monitoring, cloud-logging — is installed by nothing and is returned
// by Unrendered.
var tools = map[string]tool{
	"prometheus": {
		source:      "observability/prometheus.yaml.tmpl",
		out:         "observability/prometheus.yaml",
		deployments: []generators.Deployment{{Namespace: "monitoring", Name: "prometheus"}},
	},
	"grafana": {
		source:      "observability/grafana.yaml.tmpl",
		out:         "observability/grafana.yaml",
		deployments: []generators.Deployment{{Namespace: "monitoring", Name: "grafana"}},
	},
}

// Generate renders manifests for each observability tool in the level spec
// that petri installs. The rest are named by Unrendered.
func (g *generator) Generate(_ context.Context, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error) {
	var files []generators.RenderedFile

	for _, name := range tmplCtx.Level.Observability {
		t, ok := tools[name]
		if !ok {
			continue
		}
		content, err := g.renderFile(t.source, tmplCtx)
		if err != nil {
			return nil, fmt.Errorf("rendering observability tool %q: %w", name, err)
		}
		files = append(files, generators.RenderedFile{Path: t.out, Content: content})
	}
	return files, nil
}

// Unrendered returns the listed tools petri installs nothing for.
func (g *generator) Unrendered(tmplCtx generators.TemplateContext) []string {
	var names []string
	for _, name := range tmplCtx.Level.Observability {
		if _, ok := tools[name]; !ok {
			names = append(names, name)
		}
	}
	return names
}

// Deployments returns the Deployments of the listed tools petri installs.
func (g *generator) Deployments(tmplCtx generators.TemplateContext) []generators.Deployment {
	var out []generators.Deployment
	for _, name := range tmplCtx.Level.Observability {
		out = append(out, tools[name].deployments...)
	}
	return out
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
