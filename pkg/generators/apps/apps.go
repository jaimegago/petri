// Package apps generates Kubernetes application manifests (Deployments, Services,
// Ingresses, HPAs, PDBs, NetworkPolicies) from embedded templates.
// Each app in the level spec gets a full set of manifests appropriate for the
// complexity level.
package apps

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"strings"
	"text/template"

	"github.com/jaimegago/petri/pkg/generators"
	petritemplates "github.com/jaimegago/petri/templates"
)

// Generator renders Kubernetes manifests for all apps in a lab context.
type Generator interface {
	// Generate produces per-app manifest files for every app in the level spec.
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

// Generate renders namespace manifests and per-app Kubernetes resources.
func (g *generator) Generate(_ context.Context, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error) {
	var files []generators.RenderedFile

	// Render namespace manifests.
	namespaces := tmplCtx.Level.Namespaces
	if len(namespaces) == 0 {
		namespaces = []string{"default"}
	}
	for _, ns := range namespaces {
		nsCtx := struct {
			generators.TemplateContext
			Namespace string
		}{tmplCtx, ns}
		content, err := g.renderData("apps/namespace.yaml.tmpl", nsCtx)
		if err != nil {
			return nil, fmt.Errorf("rendering namespace %s: %w", ns, err)
		}
		files = append(files, generators.RenderedFile{
			Path:    "namespaces/" + ns + ".yaml",
			Content: content,
		})
	}

	// Render per-app manifests.
	for _, app := range tmplCtx.Level.Apps {
		appFiles, err := g.generateApp(app, tmplCtx)
		if err != nil {
			return nil, fmt.Errorf("generating app %s: %w", app, err)
		}
		files = append(files, appFiles...)
	}
	return files, nil
}

// generateApp renders all Kubernetes manifest templates for a single application.
func (g *generator) generateApp(appName string, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error) {
	ns := appNamespace(appName, tmplCtx)
	appCtx := generators.AppTemplateContext{
		TemplateContext: tmplCtx,
		AppName:         appName,
		Namespace:       ns,
		Replicas:        replicasForLevel(tmplCtx.Level.Number),
		Port:            defaultPort(appName),
		ImageRepository: defaultImage(appName, tmplCtx.Company.Name),
		CPURequest:      cpuRequest(tmplCtx.Level.Number),
		MemoryRequest:   memRequest(tmplCtx.Level.Number),
		CPULimit:        cpuLimit(tmplCtx.Level.Number),
		MemoryLimit:     memLimit(tmplCtx.Level.Number),
	}

	// Core templates always rendered.
	coreTemplates := []struct {
		tmpl string
		path string
	}{
		{"apps/deployment.yaml.tmpl", "apps/" + appName + "/deployment.yaml"},
		{"apps/service.yaml.tmpl", "apps/" + appName + "/service.yaml"},
	}

	// Ingress for frontend/gateway apps or when explicitly named.
	if isFrontendApp(appName) {
		coreTemplates = append(coreTemplates, struct {
			tmpl string
			path string
		}{"apps/ingress.yaml.tmpl", "apps/" + appName + "/ingress.yaml"})
	}

	// Level 2+ gets HPA and PDB.
	if tmplCtx.Level.Number >= 2 {
		coreTemplates = append(coreTemplates,
			struct {
				tmpl string
				path string
			}{"apps/hpa.yaml.tmpl", "apps/" + appName + "/hpa.yaml"},
			struct {
				tmpl string
				path string
			}{"apps/pdb.yaml.tmpl", "apps/" + appName + "/pdb.yaml"},
		)
	}

	// Level 3 adds NetworkPolicy.
	if tmplCtx.Level.Number >= 3 {
		coreTemplates = append(coreTemplates, struct {
			tmpl string
			path string
		}{"apps/networkpolicy.yaml.tmpl", "apps/" + appName + "/networkpolicy.yaml"})
	}

	files := make([]generators.RenderedFile, 0, len(coreTemplates))
	for _, t := range coreTemplates {
		content, err := g.renderData(t.tmpl, appCtx)
		if err != nil {
			return nil, fmt.Errorf("rendering %s for app %s: %w", t.tmpl, appName, err)
		}
		files = append(files, generators.RenderedFile{Path: t.path, Content: content})
	}
	return files, nil
}

// renderData parses and executes a template with any data type.
func (g *generator) renderData(templatePath string, data any) (string, error) {
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

// appNamespace returns the namespace for an application. When level namespaces
// are defined the first one is used; otherwise "default".
func appNamespace(app string, tmplCtx generators.TemplateContext) string {
	if len(tmplCtx.Level.Namespaces) > 0 {
		return tmplCtx.Level.Namespaces[0]
	}
	_ = app
	return "default"
}

// isFrontendApp returns true for apps that should receive an Ingress resource.
func isFrontendApp(name string) bool {
	frontends := []string{"frontend", "api-gateway", "spring-frontend", "boutique-frontend"}
	for _, f := range frontends {
		if strings.EqualFold(name, f) {
			return true
		}
	}
	return false
}

// defaultPort returns a sensible port for common app names, falling back to 8080.
func defaultPort(app string) int {
	portMap := map[string]int{
		"api-gateway":       8080,
		"auth-service":      8081,
		"user-service":      8082,
		"order-service":     8083,
		"product-service":   8084,
		"boutique-frontend": 8080,
		"boutique-cart":     8080,
		"boutique-checkout": 8080,
		"spring-frontend":   8080,
		"spring-catalog":    8081,
		"spring-cart":       8082,
		"spring-orders":     8083,
		"spring-payments":   8084,
	}
	if p, ok := portMap[app]; ok {
		return p
	}
	return 8080
}

// defaultImage returns a placeholder container image for an app.
func defaultImage(app, company string) string {
	return "ghcr.io/" + company + "/" + app
}

// replicasForLevel returns the baseline replica count for a level.
func replicasForLevel(level int) int {
	switch level {
	case 3:
		return 3
	case 2:
		return 2
	default:
		return 1
	}
}

func cpuRequest(level int) string {
	if level >= 2 {
		return "100m"
	}
	return "50m"
}

func memRequest(level int) string {
	if level >= 2 {
		return "128Mi"
	}
	return "64Mi"
}

func cpuLimit(level int) string {
	if level >= 3 {
		return "1000m"
	}
	return "500m"
}

func memLimit(level int) string {
	if level >= 3 {
		return "1Gi"
	}
	return "512Mi"
}
