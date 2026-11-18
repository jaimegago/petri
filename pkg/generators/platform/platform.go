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

// CertManagerVersion is the cert-manager release vendored at
// templates/platform/cert-manager.yaml. The file is the upstream static
// manifest, unmodified: the controller, the CA injector, the webhook and the
// six CRDs. Moving the pin is replacing the file with the same asset of
// another release and changing this constant; TestCertManagerManifest holds
// the two together.
const CertManagerVersion = "v1.21.3"

// Generator renders platform component manifests based on the lab context.
type Generator interface {
	// Generate returns manifests for the platform components in the level
	// spec that petri installs.
	Generate(ctx context.Context, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error)
	// Unrendered returns the components the level spec lists and Generate
	// produces nothing for, in the order listed. A caller reports them: a
	// component a lab lists and does not have is a fact about the lab.
	Unrendered(tmplCtx generators.TemplateContext) []string
	// Deployments returns the Deployments the rendered components create,
	// for a caller to wait on.
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

// component is what the generator installs for one platform component name.
type component struct {
	// source is the file in the template filesystem; out is the rendered path.
	source, out string
	// verbatim marks an upstream manifest served as shipped rather than
	// executed as a template.
	verbatim bool
	// deployments are the Deployments the manifest creates.
	deployments []generators.Deployment
}

// components is every platform component petri installs. A name a level spec
// lists and this table lacks — argocd, flux, anthos-config-mgmt, vault,
// external-dns, crossplane, istio, velero, kyverno, dapr, keda,
// azure-keyvault-csi, secret-manager-csi, anthos-service-mesh,
// config-connector — is installed by nothing, in a local lab or a cloud one,
// and is returned by Unrendered.
var components = map[string]component{
	"cert-manager": {
		source:   "platform/cert-manager.yaml",
		out:      "platform/cert-manager.yaml",
		verbatim: true,
		deployments: []generators.Deployment{
			{Namespace: "cert-manager", Name: "cert-manager"},
			{Namespace: "cert-manager", Name: "cert-manager-cainjector"},
			{Namespace: "cert-manager", Name: "cert-manager-webhook"},
		},
	},
	"ingress-nginx": {
		source: "platform/ingress-nginx.yaml.tmpl",
		out:    "platform/ingress-nginx.yaml",
		deployments: []generators.Deployment{
			{Namespace: "ingress-nginx", Name: "ingress-nginx-controller"},
		},
	},
}

// Generate renders manifests for each platform component listed in the level
// spec that petri installs. The rest are named by Unrendered.
func (g *generator) Generate(_ context.Context, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error) {
	var files []generators.RenderedFile

	for _, name := range tmplCtx.Level.Platform {
		c, ok := components[name]
		if !ok {
			continue
		}
		content, err := g.render(c, tmplCtx)
		if err != nil {
			return nil, fmt.Errorf("rendering platform component %q: %w", name, err)
		}
		files = append(files, generators.RenderedFile{Path: c.out, Content: content})
	}
	return files, nil
}

// Unrendered returns the listed components petri installs nothing for.
func (g *generator) Unrendered(tmplCtx generators.TemplateContext) []string {
	var names []string
	for _, name := range tmplCtx.Level.Platform {
		if _, ok := components[name]; !ok {
			names = append(names, name)
		}
	}
	return names
}

// Deployments returns the Deployments of the listed components petri installs.
func (g *generator) Deployments(tmplCtx generators.TemplateContext) []generators.Deployment {
	var out []generators.Deployment
	for _, name := range tmplCtx.Level.Platform {
		out = append(out, components[name].deployments...)
	}
	return out
}

// render returns a component's manifest: executed as a template, or read as
// shipped when the component is a vendored upstream manifest.
func (g *generator) render(c component, tmplCtx generators.TemplateContext) (string, error) {
	if !c.verbatim {
		return g.renderFile(c.source, tmplCtx)
	}
	raw, err := fs.ReadFile(g.fs, c.source)
	if err != nil {
		return "", fmt.Errorf("reading manifest %s: %w", c.source, err)
	}
	return string(raw), nil
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
