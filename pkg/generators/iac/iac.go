// Package iac generates IaC code (Terraform/Pulumi) from embedded templates.
// Templates are selected based on the company's cloud provider and IaC tool.
package iac

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"text/template"

	"github.com/jaimegago/petri/pkg/generators"
	petritemplates "github.com/jaimegago/petri/templates"
)

// Generator renders IaC templates for a given lab context.
type Generator interface {
	// Generate produces a set of rendered files for the provided template context.
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

// Generate selects the appropriate template set and renders all IaC files.
func (g *generator) Generate(_ context.Context, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error) {
	switch tmplCtx.Company.IaCTool {
	case "terraform":
		return g.generateTerraform(tmplCtx)
	case "pulumi":
		return g.generatePulumi(tmplCtx)
	default:
		return nil, fmt.Errorf("unsupported IaC tool: %s", tmplCtx.Company.IaCTool)
	}
}

// terraformFiles lists the template filenames rendered for every Terraform provider.
var terraformFiles = []string{
	"provider.tf.tmpl",
	"variables.tf.tmpl",
	"main.tf.tmpl",
	"outputs.tf.tmpl",
}

func (g *generator) generateTerraform(tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error) {
	dir := "terraform/" + tmplCtx.Company.CloudProvider
	files := make([]generators.RenderedFile, 0, len(terraformFiles))

	for _, entry := range terraformFiles {
		path := dir + "/" + entry
		content, err := g.render(path, tmplCtx)
		if err != nil {
			return nil, fmt.Errorf("rendering %s: %w", path, err)
		}
		files = append(files, generators.RenderedFile{Path: stripTmplExt(entry), Content: content})
	}
	return files, nil
}

// pulumiFiles lists the template filenames rendered for every Pulumi provider.
var pulumiFiles = []string{
	"Pulumi.yaml.tmpl",
	"index.ts.tmpl",
	"package.json.tmpl",
}

func (g *generator) generatePulumi(tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error) {
	dir := "pulumi/" + tmplCtx.Company.CloudProvider
	files := make([]generators.RenderedFile, 0, len(pulumiFiles))

	for _, entry := range pulumiFiles {
		path := dir + "/" + entry
		content, err := g.render(path, tmplCtx)
		if err != nil {
			return nil, fmt.Errorf("rendering %s: %w", path, err)
		}
		files = append(files, generators.RenderedFile{Path: stripTmplExt(entry), Content: content})
	}
	return files, nil
}

// render reads a template from the filesystem, parses it, and executes it against tmplCtx.
func (g *generator) render(templatePath string, tmplCtx generators.TemplateContext) (string, error) {
	data, err := fs.ReadFile(g.fs, templatePath)
	if err != nil {
		return "", fmt.Errorf("reading template: %w", err)
	}

	tmpl, err := template.New(templatePath).
		Funcs(generators.TemplateFuncs()).
		Parse(string(data))
	if err != nil {
		return "", fmt.Errorf("parsing template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, tmplCtx); err != nil {
		return "", fmt.Errorf("executing template: %w", err)
	}
	return buf.String(), nil
}

// stripTmplExt removes the trailing ".tmpl" extension from a filename.
func stripTmplExt(name string) string {
	const ext = ".tmpl"
	if len(name) > len(ext) && name[len(name)-len(ext):] == ext {
		return name[:len(name)-len(ext)]
	}
	return name
}
