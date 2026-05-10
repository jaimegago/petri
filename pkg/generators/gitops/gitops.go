// Package gitops generates ArgoCD, Flux, and Anthos Config Management manifests
// from embedded templates, keyed on the company's gitops_tool setting.
package gitops

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"text/template"

	"github.com/jaimegago/petri/pkg/generators"
	petritemplates "github.com/jaimegago/petri/templates"
)

// Generator renders GitOps manifests for a given lab context.
type Generator interface {
	// Generate produces a set of rendered GitOps manifest files.
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

// Generate selects the appropriate GitOps tool templates and renders all manifests.
func (g *generator) Generate(_ context.Context, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error) {
	switch tmplCtx.Company.GitOpsTool {
	case "argocd":
		return g.generateArgoCD(tmplCtx)
	case "flux":
		return g.generateFlux(tmplCtx)
	case "anthos":
		return g.generateAnthos(tmplCtx)
	default:
		return nil, fmt.Errorf("unsupported GitOps tool: %s", tmplCtx.Company.GitOpsTool)
	}
}

// generateArgoCD renders an app-of-apps root Application plus one Application per app.
func (g *generator) generateArgoCD(tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error) {
	var files []generators.RenderedFile

	// Root app-of-apps manifest.
	root, err := g.render("gitops/argocd/app-of-apps.yaml.tmpl", tmplCtx)
	if err != nil {
		return nil, fmt.Errorf("rendering app-of-apps: %w", err)
	}
	files = append(files, generators.RenderedFile{
		Path:    "clusters/" + tmplCtx.Level.ClusterNames[0] + "/applications/app-of-apps.yaml",
		Content: root,
	})

	// One Application per app, placed in the correct namespace.
	for _, app := range tmplCtx.Level.Apps {
		ns := appNamespace(app, tmplCtx)
		appCtx := generators.AppTemplateContext{
			TemplateContext: tmplCtx,
			AppName:         app,
			Namespace:       ns,
		}
		content, err := g.renderApp("gitops/argocd/application.yaml.tmpl", appCtx)
		if err != nil {
			return nil, fmt.Errorf("rendering application %s: %w", app, err)
		}
		files = append(files, generators.RenderedFile{
			Path:    "clusters/" + tmplCtx.Level.ClusterNames[0] + "/applications/" + app + ".yaml",
			Content: content,
		})
	}
	return files, nil
}

// generateFlux renders a gotk-sync manifest plus one HelmRelease per app.
func (g *generator) generateFlux(tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error) {
	var files []generators.RenderedFile

	sync, err := g.render("gitops/flux/gotk-sync.yaml.tmpl", tmplCtx)
	if err != nil {
		return nil, fmt.Errorf("rendering gotk-sync: %w", err)
	}
	files = append(files, generators.RenderedFile{
		Path:    "clusters/" + tmplCtx.Level.ClusterNames[0] + "/flux-system/gotk-sync.yaml",
		Content: sync,
	})

	for _, app := range tmplCtx.Level.Apps {
		ns := appNamespace(app, tmplCtx)
		appCtx := generators.AppTemplateContext{
			TemplateContext: tmplCtx,
			AppName:         app,
			Namespace:       ns,
			Replicas:        defaultReplicas(tmplCtx.Level.Number),
			Port:            defaultPort(app),
			ImageRepository: defaultImage(app, tmplCtx.Company.Name),
		}
		content, err := g.renderApp("gitops/flux/helmrelease.yaml.tmpl", appCtx)
		if err != nil {
			return nil, fmt.Errorf("rendering helmrelease %s: %w", app, err)
		}
		files = append(files, generators.RenderedFile{
			Path:    "apps/" + app + "/helmrelease.yaml",
			Content: content,
		})
	}
	return files, nil
}

// generateAnthos renders the ConfigManagement + RootSync manifest.
func (g *generator) generateAnthos(tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error) {
	content, err := g.render("gitops/anthos/config-management.yaml.tmpl", tmplCtx)
	if err != nil {
		return nil, fmt.Errorf("rendering config-management: %w", err)
	}
	return []generators.RenderedFile{
		{Path: "config-management.yaml", Content: content},
	}, nil
}

// render executes a standard TemplateContext against a template file.
func (g *generator) render(templatePath string, tmplCtx generators.TemplateContext) (string, error) {
	data, err := fs.ReadFile(g.fs, templatePath)
	if err != nil {
		return "", fmt.Errorf("reading template %s: %w", templatePath, err)
	}
	tmpl, err := template.New(templatePath).Funcs(generators.TemplateFuncs()).Parse(string(data))
	if err != nil {
		return "", fmt.Errorf("parsing template %s: %w", templatePath, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, tmplCtx); err != nil {
		return "", fmt.Errorf("executing template %s: %w", templatePath, err)
	}
	return buf.String(), nil
}

// renderApp executes an AppTemplateContext against a template file.
func (g *generator) renderApp(templatePath string, appCtx generators.AppTemplateContext) (string, error) {
	data, err := fs.ReadFile(g.fs, templatePath)
	if err != nil {
		return "", fmt.Errorf("reading template %s: %w", templatePath, err)
	}
	tmpl, err := template.New(templatePath).Funcs(generators.TemplateFuncs()).Parse(string(data))
	if err != nil {
		return "", fmt.Errorf("parsing template %s: %w", templatePath, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, appCtx); err != nil {
		return "", fmt.Errorf("executing template %s: %w", templatePath, err)
	}
	return buf.String(), nil
}

// appNamespace returns the Kubernetes namespace for an app, preferring the first
// non-platform namespace when namespaces are defined, otherwise "default".
func appNamespace(app string, tmplCtx generators.TemplateContext) string {
	if len(tmplCtx.Level.Namespaces) > 0 {
		return tmplCtx.Level.Namespaces[0]
	}
	_ = app
	return "default"
}

// defaultReplicas returns a sensible replica count based on complexity level.
func defaultReplicas(level int) int {
	if level >= 2 {
		return 2
	}
	return 1
}

// defaultPort returns a conventional port for well-known app names.
func defaultPort(app string) int {
	portMap := map[string]int{
		"api-gateway":       8080,
		"auth-service":      8081,
		"user-service":      8082,
		"order-service":     8083,
		"boutique-frontend": 8080,
	}
	if p, ok := portMap[app]; ok {
		return p
	}
	return 8080
}

// defaultImage returns a placeholder image repository for an app.
func defaultImage(app, company string) string {
	return "ghcr.io/" + company + "/" + app
}
