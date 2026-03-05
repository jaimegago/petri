// Package platform generates Kubernetes manifests for platform components
// (cert-manager, ingress-nginx, vault, etc.) required by each company level.
package platform

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"text/template"

	"github.com/jaimegago/petri/pkg/generators"
	petritemplates "github.com/jaimegago/petri/templates"
)

// Generator renders platform component manifests based on the lab context.
type Generator interface {
	// Generate returns manifests for all platform components in the level spec.
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

// Generate renders manifests for each platform component listed in the level spec.
// Components without dedicated templates are silently skipped.
func (g *generator) Generate(_ context.Context, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error) {
	var files []generators.RenderedFile

	for _, component := range tmplCtx.Level.Platform {
		rendered, err := g.renderComponent(component, tmplCtx)
		if err != nil {
			return nil, fmt.Errorf("rendering platform component %q: %w", component, err)
		}
		files = append(files, rendered...)
	}
	return files, nil
}

// renderComponent renders manifests for a single platform component.
func (g *generator) renderComponent(component string, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error) {
	tmplPath, outPath := templatePathFor(component)
	if tmplPath == "" {
		return nil, nil
	}

	content, err := g.renderFile(tmplPath, tmplCtx)
	if err != nil {
		return nil, err
	}
	return []generators.RenderedFile{{Path: outPath, Content: content}}, nil
}

// templatePathFor maps a platform component name to its template and output paths.
func templatePathFor(component string) (tmplPath, outPath string) {
	switch component {
	case "cert-manager":
		return "platform/cert-manager.yaml.tmpl", "platform/cert-manager.yaml"
	case "ingress-nginx":
		return "platform/ingress-nginx.yaml.tmpl", "platform/ingress-nginx.yaml"
	default:
		// Components like argocd, flux, anthos-config-mgmt, vault, external-dns,
		// crossplane, istio, velero, kyverno, dapr, keda, azure-keyvault-csi,
		// secret-manager-csi, anthos-service-mesh, config-connector are managed
		// via Helm/cloud-native tooling in cloud labs.
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
