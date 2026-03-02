package apps_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jaimegago/petri/pkg/generators"
	"github.com/jaimegago/petri/pkg/generators/apps"
)

func baseCtx(level int) generators.TemplateContext {
	return generators.TemplateContext{
		Lab: generators.LabInfo{
			ID:   uuid.New().String(),
			Name: "test-lab",
		},
		Company: generators.CompanyInfo{
			Name:          "acme",
			CloudProvider: "aws",
		},
		Level: generators.LevelInfo{
			Number:       level,
			ClusterNames: []string{"prod"},
			Apps:         []string{"boutique-frontend", "boutique-cart"},
		},
		Now: time.Now(),
	}
}

// minimalAppsFS returns an in-memory FS with stub templates for all app templates.
func minimalAppsFS() fstest.MapFS {
	m := fstest.MapFS{
		"apps/namespace.yaml.tmpl": {
			Data: []byte("# namespace {{ .Namespace }}\n"),
		},
		"apps/deployment.yaml.tmpl": {
			Data: []byte("# deployment {{ .AppName }} ns={{ .Namespace }} replicas={{ .Replicas }}\n"),
		},
		"apps/service.yaml.tmpl": {
			Data: []byte("# service {{ .AppName }}\n"),
		},
		"apps/ingress.yaml.tmpl": {
			Data: []byte("# ingress {{ .AppName }}\n"),
		},
		"apps/hpa.yaml.tmpl": {
			Data: []byte("# hpa {{ .AppName }}\n"),
		},
		"apps/pdb.yaml.tmpl": {
			Data: []byte("# pdb {{ .AppName }}\n"),
		},
		"apps/networkpolicy.yaml.tmpl": {
			Data: []byte("# networkpolicy {{ .AppName }}\n"),
		},
	}
	return m
}

func TestGenerate_Level1(t *testing.T) {
	gen := apps.NewWithFS(minimalAppsFS())
	ctx := baseCtx(1)

	files, err := gen.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Level 1: namespace(default) + deployment + service + ingress(frontend) per app.
	// boutique-frontend → deployment, service, ingress
	// boutique-cart → deployment, service
	// namespaces/default.yaml
	if len(files) == 0 {
		t.Fatal("expected at least one file")
	}

	paths := make(map[string]bool)
	for _, f := range files {
		paths[f.Path] = true
		if f.Content == "" {
			t.Errorf("file %s has empty content", f.Path)
		}
	}

	mustHave := []string{
		"apps/boutique-frontend/deployment.yaml",
		"apps/boutique-frontend/service.yaml",
		"apps/boutique-frontend/ingress.yaml",
		"apps/boutique-cart/deployment.yaml",
		"apps/boutique-cart/service.yaml",
	}
	for _, p := range mustHave {
		if !paths[p] {
			t.Errorf("expected path not found: %s", p)
		}
	}

	// Level 1 should NOT have HPA or PDB.
	if paths["apps/boutique-frontend/hpa.yaml"] {
		t.Error("level 1 should not have HPA")
	}
}

func TestGenerate_Level2_HasHPA_PDB(t *testing.T) {
	gen := apps.NewWithFS(minimalAppsFS())
	ctx := baseCtx(2)
	ctx.Level.Namespaces = []string{"team-app"}

	files, err := gen.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	paths := make(map[string]bool)
	for _, f := range files {
		paths[f.Path] = true
	}

	// Level 2 must have HPA and PDB for each app.
	for _, app := range ctx.Level.Apps {
		hpaPath := "apps/" + app + "/hpa.yaml"
		pdbPath := "apps/" + app + "/pdb.yaml"
		if !paths[hpaPath] {
			t.Errorf("level 2 expected HPA at %s", hpaPath)
		}
		if !paths[pdbPath] {
			t.Errorf("level 2 expected PDB at %s", pdbPath)
		}
	}
}

func TestGenerate_Level3_HasNetworkPolicy(t *testing.T) {
	gen := apps.NewWithFS(minimalAppsFS())
	ctx := baseCtx(3)

	files, err := gen.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	paths := make(map[string]bool)
	for _, f := range files {
		paths[f.Path] = true
	}

	for _, app := range ctx.Level.Apps {
		npPath := "apps/" + app + "/networkpolicy.yaml"
		if !paths[npPath] {
			t.Errorf("level 3 expected NetworkPolicy at %s", npPath)
		}
	}
}

func TestGenerate_NamespacesRendered(t *testing.T) {
	gen := apps.NewWithFS(minimalAppsFS())
	ctx := baseCtx(2)
	ctx.Level.Namespaces = []string{"team-platform", "team-app"}

	files, err := gen.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	paths := make(map[string]bool)
	for _, f := range files {
		paths[f.Path] = true
	}

	for _, ns := range ctx.Level.Namespaces {
		p := "namespaces/" + ns + ".yaml"
		if !paths[p] {
			t.Errorf("expected namespace manifest at %s", p)
		}
	}
}

func TestGenerate_DeploymentContent(t *testing.T) {
	gen := apps.NewWithFS(minimalAppsFS())
	ctx := baseCtx(2)
	ctx.Level.Apps = []string{"api-gateway"}

	files, err := gen.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	var dep *generators.RenderedFile
	for i := range files {
		if files[i].Path == "apps/api-gateway/deployment.yaml" {
			dep = &files[i]
			break
		}
	}
	if dep == nil {
		t.Fatal("deployment.yaml not found")
	}

	if !strings.Contains(dep.Content, "api-gateway") {
		t.Error("deployment content missing app name")
	}
}

func TestGenerate_MissingTemplate(t *testing.T) {
	gen := apps.NewWithFS(fstest.MapFS{}) // empty FS
	ctx := baseCtx(1)
	_, err := gen.Generate(context.Background(), ctx)
	if err == nil {
		t.Fatal("expected error when templates are missing")
	}
}

func TestGenerate_WithEmbeddedFS(t *testing.T) {
	gen := apps.New()

	tests := []struct {
		name  string
		level int
		apps  []string
	}{
		{"level1-basic", 1, []string{"boutique-frontend", "boutique-cart"}},
		{"level2-full", 2, []string{"boutique-frontend", "boutique-cart", "boutique-checkout"}},
		{"level3-prod", 3, []string{"spring-frontend", "spring-catalog"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := baseCtx(tt.level)
			ctx.Level.Apps = tt.apps

			files, err := gen.Generate(context.Background(), ctx)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if len(files) == 0 {
				t.Error("expected at least one rendered file")
			}
			for _, f := range files {
				if f.Content == "" {
					t.Errorf("file %s has empty content", f.Path)
				}
			}
		})
	}
}
