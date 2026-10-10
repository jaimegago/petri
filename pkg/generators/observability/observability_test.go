package observability_test

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jaimegago/petri/pkg/generators"
	"github.com/jaimegago/petri/pkg/generators/observability"
)

func makeContext(obs []string) generators.TemplateContext {
	return generators.TemplateContext{
		Lab:     generators.LabInfo{Name: "test-lab", ID: "abc-123"},
		Company: generators.CompanyInfo{Name: "acme", CloudProvider: "aws"},
		Level:   generators.LevelInfo{Number: 1, Observability: obs},
		Now:     time.Now(),
	}
}

func makeTestFS() fstest.MapFS {
	return fstest.MapFS{
		"observability/prometheus.yaml.tmpl": &fstest.MapFile{
			Data: []byte(`# prometheus for {{.Lab.Name}}`),
		},
		"observability/grafana.yaml.tmpl": &fstest.MapFile{
			Data: []byte(`# grafana for {{.Lab.Name}}`),
		},
	}
}

func TestGenerate_Prometheus(t *testing.T) {
	g := observability.NewWithFS(makeTestFS())
	ctx := makeContext([]string{"prometheus"})

	files, err := g.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].Path != "observability/prometheus.yaml" {
		t.Errorf("unexpected path %q", files[0].Path)
	}
	if files[0].Content == "" {
		t.Error("expected non-empty content")
	}
}

func TestGenerate_PrometheusAndGrafana(t *testing.T) {
	g := observability.NewWithFS(makeTestFS())
	ctx := makeContext([]string{"prometheus", "grafana"})

	files, err := g.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
}

func TestGenerate_SkipsUnknownTools(t *testing.T) {
	g := observability.NewWithFS(makeTestFS())
	ctx := makeContext([]string{"prometheus", "loki", "tempo", "cloud-monitoring"})

	files, err := g.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Only prometheus has a template; others are skipped silently.
	if len(files) != 1 {
		t.Errorf("expected 1 file (prometheus only), got %d", len(files))
	}
}

func TestGenerate_EmptyObservability(t *testing.T) {
	g := observability.NewWithFS(makeTestFS())
	ctx := makeContext([]string{})

	files, err := g.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files for empty observability, got %d", len(files))
	}
}

func TestGenerate_TemplateContext(t *testing.T) {
	fsys := fstest.MapFS{
		"observability/prometheus.yaml.tmpl": &fstest.MapFile{
			Data: []byte(`lab={{.Lab.Name}} company={{.Company.Name}}`),
		},
	}
	g := observability.NewWithFS(fsys)
	ctx := makeContext([]string{"prometheus"})
	ctx.Lab.Name = "my-lab"
	ctx.Company.Name = "acme"

	files, err := g.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := "lab=my-lab company=acme"
	if files[0].Content != want {
		t.Errorf("got %q, want %q", files[0].Content, want)
	}
}

// A local lab's Prometheus and Grafana build their external URLs from the
// lab's own ingress host port, not a fixed one shared by every lab.
func TestGenerate_LocalURLsUseLabIngressPort(t *testing.T) {
	t.Parallel()
	tmplCtx := makeContext([]string{"prometheus", "grafana"})
	tmplCtx.Lab.LabType = "local"
	tmplCtx.Lab.IngressHTTPPort = 52080

	files, err := observability.New().Generate(context.Background(), tmplCtx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := map[string]string{
		"observability/prometheus.yaml": "--web.external-url=http://localhost:52080/prometheus",
		"observability/grafana.yaml":    "http://localhost:52080/grafana/",
	}
	for _, f := range files {
		w, ok := want[f.Path]
		if !ok {
			continue
		}
		if !strings.Contains(f.Content, w) {
			t.Errorf("%s missing %q", f.Path, w)
		}
		if strings.Contains(f.Content, "30080") {
			t.Errorf("%s still references fixed port 30080", f.Path)
		}
		delete(want, f.Path)
	}
	if len(want) != 0 {
		t.Errorf("files not rendered: %v", want)
	}
}
