package platform_test

import (
	"context"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jaimegago/petri/pkg/generators"
	"github.com/jaimegago/petri/pkg/generators/platform"
)

func makeContext(components []string) generators.TemplateContext {
	return generators.TemplateContext{
		Lab:     generators.LabInfo{Name: "test-lab", ID: "abc-123"},
		Company: generators.CompanyInfo{Name: "acme", CloudProvider: "aws"},
		Level:   generators.LevelInfo{Number: 1, Platform: components},
		Now:     time.Now(),
	}
}

func makeTestFS() fstest.MapFS {
	return fstest.MapFS{
		"platform/cert-manager.yaml.tmpl": &fstest.MapFile{
			Data: []byte(`# cert-manager for {{.Lab.Name}}`),
		},
		"platform/ingress-nginx.yaml.tmpl": &fstest.MapFile{
			Data: []byte(`# ingress-nginx for {{.Lab.Name}}`),
		},
	}
}

func TestGenerate_CertManager(t *testing.T) {
	g := platform.NewWithFS(makeTestFS())
	ctx := makeContext([]string{"cert-manager"})

	files, err := g.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Path != "platform/cert-manager.yaml" {
		t.Errorf("unexpected path %q", files[0].Path)
	}
}

func TestGenerate_IngressNginx(t *testing.T) {
	g := platform.NewWithFS(makeTestFS())
	ctx := makeContext([]string{"ingress-nginx"})

	files, err := g.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Path != "platform/ingress-nginx.yaml" {
		t.Errorf("unexpected path %q", files[0].Path)
	}
}

func TestGenerate_MultipleComponents(t *testing.T) {
	g := platform.NewWithFS(makeTestFS())
	ctx := makeContext([]string{"cert-manager", "ingress-nginx"})

	files, err := g.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
}

func TestGenerate_SkipsUnknownComponents(t *testing.T) {
	g := platform.NewWithFS(makeTestFS())
	// argocd, flux, vault are managed via Helm/cloud tooling — no local templates.
	ctx := makeContext([]string{"argocd", "cert-manager", "vault", "external-dns"})

	files, err := g.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Only cert-manager has a template.
	if len(files) != 1 {
		t.Errorf("expected 1 file (cert-manager only), got %d", len(files))
	}
}

func TestGenerate_EmptyPlatform(t *testing.T) {
	g := platform.NewWithFS(makeTestFS())
	ctx := makeContext([]string{})

	files, err := g.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files, got %d", len(files))
	}
}

func TestGenerate_TemplateContext(_ *testing.T) {
	fsys := fstest.MapFS{
		"platform/cert-manager.yaml.tmpl": &fstest.MapFile{
			Data: []byte(`lab={{.Lab.Name}}`),
		},
	}
	g := platform.NewWithFS(fsys)
	ctx := makeContext([]string{"cert-manager"})
	ctx.Lab.Name = "my-lab"
	ctx.Now = time.Now()

	_, _ = g.Generate(context.Background(), ctx)
}
