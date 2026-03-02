package gitops_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jaimegago/petri/pkg/generators"
	"github.com/jaimegago/petri/pkg/generators/gitops"
)

func baseCtx(gitopsTool string, level int) generators.TemplateContext {
	return generators.TemplateContext{
		Lab: generators.LabInfo{
			ID:      uuid.New().String(),
			Name:    "test-lab",
			LabType: "aws",
		},
		Company: generators.CompanyInfo{
			Name:          "acme",
			CloudProvider: "aws",
			GitOpsTool:    gitopsTool,
			GitHubOrg:     "acme-org",
		},
		Level: generators.LevelInfo{
			Number:       level,
			ClusterNames: []string{"prod"},
			NodeCounts:   []int{3},
			Apps:         []string{"frontend", "backend"},
			Namespaces:   []string{"team-app"},
		},
		Now: time.Now(),
	}
}

// argoCDFS builds a minimal in-memory FS for ArgoCD tests.
func argoCDFS() fstest.MapFS {
	return fstest.MapFS{
		"gitops/argocd/app-of-apps.yaml.tmpl": {
			Data: []byte("# app-of-apps {{ .Company.Name }} {{ .Lab.Name }}\n"),
		},
		"gitops/argocd/application.yaml.tmpl": {
			Data: []byte("# application {{ .AppName }} ns={{ .Namespace }}\n"),
		},
	}
}

// fluxFS builds a minimal in-memory FS for Flux tests.
func fluxFS() fstest.MapFS {
	return fstest.MapFS{
		"gitops/flux/gotk-sync.yaml.tmpl": {
			Data: []byte("# gotk-sync {{ .Company.Name }} {{ .Lab.Name }}\n"),
		},
		"gitops/flux/helmrelease.yaml.tmpl": {
			Data: []byte("# helmrelease {{ .AppName }} ns={{ .Namespace }}\n"),
		},
	}
}

// anthosFS builds a minimal in-memory FS for Anthos tests.
func anthosFS() fstest.MapFS {
	return fstest.MapFS{
		"gitops/anthos/config-management.yaml.tmpl": {
			Data: []byte("# config-management {{ .Company.Name }}\n"),
		},
	}
}

func TestGenerate_ArgoCD(t *testing.T) {
	gen := gitops.NewWithFS(argoCDFS())
	ctx := baseCtx("argocd", 1)

	files, err := gen.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// expect: 1 app-of-apps + 2 app manifests
	if len(files) != 3 {
		t.Errorf("expected 3 files, got %d", len(files))
	}

	// Root app-of-apps should be present.
	var found bool
	for _, f := range files {
		if strings.Contains(f.Path, "app-of-apps") {
			found = true
			if !strings.Contains(f.Content, "acme") {
				t.Error("app-of-apps content missing company name")
			}
		}
	}
	if !found {
		t.Error("app-of-apps file not found in output")
	}
}

func TestGenerate_ArgoCD_AppManifests(t *testing.T) {
	gen := gitops.NewWithFS(argoCDFS())
	ctx := baseCtx("argocd", 1)

	files, err := gen.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// Check that each app from Level.Apps gets a manifest.
	appPaths := make(map[string]bool)
	for _, f := range files {
		appPaths[f.Path] = true
	}
	for _, app := range ctx.Level.Apps {
		key := "clusters/prod/applications/" + app + ".yaml"
		if !appPaths[key] {
			t.Errorf("missing application manifest for %s (path %s)", app, key)
		}
	}
}

func TestGenerate_Flux(t *testing.T) {
	gen := gitops.NewWithFS(fluxFS())
	ctx := baseCtx("flux", 2)
	ctx.Level.Apps = []string{"api-gateway", "auth-service"}

	files, err := gen.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// expect: 1 gotk-sync + 2 helmreleases
	if len(files) != 3 {
		t.Errorf("expected 3 files, got %d", len(files))
	}

	var gotSync bool
	for _, f := range files {
		if strings.Contains(f.Path, "gotk-sync") {
			gotSync = true
		}
		if f.Content == "" {
			t.Errorf("file %s has empty content", f.Path)
		}
	}
	if !gotSync {
		t.Error("gotk-sync file not found in output")
	}
}

func TestGenerate_Anthos(t *testing.T) {
	gen := gitops.NewWithFS(anthosFS())
	ctx := baseCtx("anthos", 1)

	files, err := gen.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("expected 1 file, got %d", len(files))
	}
	if files[0].Path != "config-management.yaml" {
		t.Errorf("unexpected path: %s", files[0].Path)
	}
}

func TestGenerate_UnsupportedTool(t *testing.T) {
	gen := gitops.NewWithFS(fstest.MapFS{})
	ctx := baseCtx("unknown", 1)
	_, err := gen.Generate(context.Background(), ctx)
	if err == nil {
		t.Fatal("expected error for unsupported GitOps tool")
	}
	if !strings.Contains(err.Error(), "unsupported GitOps tool") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGenerate_WithEmbeddedFS(t *testing.T) {
	gen := gitops.New()

	tests := []struct {
		name    string
		tool    string
		level   int
		minFiles int
	}{
		{"argocd-level1", "argocd", 1, 1},
		{"flux-level2", "flux", 2, 1},
		{"anthos-level1", "anthos", 1, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := baseCtx(tt.tool, tt.level)
			files, err := gen.Generate(context.Background(), ctx)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if len(files) < tt.minFiles {
				t.Errorf("expected at least %d files, got %d", tt.minFiles, len(files))
			}
			for _, f := range files {
				if f.Content == "" {
					t.Errorf("file %s has empty content", f.Path)
				}
			}
		})
	}
}
