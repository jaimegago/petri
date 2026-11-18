package platform_test

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"gopkg.in/yaml.v3"

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
		"platform/cert-manager.yaml": &fstest.MapFile{
			Data: []byte(`# cert-manager, as shipped`),
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
	// argocd, vault and external-dns are installed by nothing.
	ctx := makeContext([]string{"argocd", "cert-manager", "vault", "external-dns"})

	files, err := g.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Only cert-manager is installed.
	if len(files) != 1 {
		t.Errorf("expected 1 file (cert-manager only), got %d", len(files))
	}
}

// A component the level lists and petri does not install is named, in the
// order listed, so the caller can report it rather than drop it.
func TestUnrendered_NamesWhatGenerateSkips(t *testing.T) {
	g := platform.NewWithFS(makeTestFS())
	ctx := makeContext([]string{"argocd", "cert-manager", "vault", "ingress-nginx", "external-dns"})

	got := g.Unrendered(ctx)
	want := []string{"argocd", "vault", "external-dns"}
	if !slices.Equal(got, want) {
		t.Errorf("Unrendered = %v, want %v", got, want)
	}
	if got := g.Unrendered(makeContext([]string{"cert-manager", "ingress-nginx"})); len(got) != 0 {
		t.Errorf("Unrendered = %v for a list petri installs in full, want none", got)
	}
}

// Every Deployment the generator tells a caller to wait on is one the
// embedded manifests create, in that namespace.
func TestDeployments_AreCreatedByTheEmbeddedManifests(t *testing.T) {
	g := platform.New()
	ctx := makeContext([]string{"argocd", "cert-manager", "ingress-nginx"})

	files, err := g.Generate(context.Background(), ctx)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	created := map[generators.Deployment]bool{}
	for _, f := range files {
		for _, d := range manifestDeployments(t, f.Content) {
			created[d] = true
		}
	}
	deployments := g.Deployments(ctx)
	if len(deployments) != 4 {
		t.Errorf("Deployments = %v, want cert-manager's three and ingress-nginx's one", deployments)
	}
	for _, d := range deployments {
		if !created[d] {
			t.Errorf("Deployments names %s/%s, which no rendered manifest creates (created: %v)", d.Namespace, d.Name, created)
		}
	}
}

// The embedded cert-manager manifest is the real thing at the pinned
// release: three workloads and the six CRDs, not a namespace and a note.
func TestCertManagerManifest(t *testing.T) {
	files, err := platform.New().Generate(context.Background(), makeContext([]string{"cert-manager"}))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	manifest := files[0].Content

	for _, image := range []string{"cert-manager-controller", "cert-manager-cainjector", "cert-manager-webhook"} {
		want := "quay.io/jetstack/" + image + ":" + platform.CertManagerVersion
		if !strings.Contains(manifest, want) {
			t.Errorf("manifest does not run %s: CertManagerVersion and templates/platform/cert-manager.yaml disagree", want)
		}
	}
	for _, crd := range []string{
		"certificates.cert-manager.io",
		"certificaterequests.cert-manager.io",
		"issuers.cert-manager.io",
		"clusterissuers.cert-manager.io",
		"orders.acme.cert-manager.io",
		"challenges.acme.cert-manager.io",
	} {
		if !strings.Contains(manifest, `name: "`+crd+`"`) {
			t.Errorf("manifest defines no CRD %s", crd)
		}
	}
	if strings.Contains(manifest, "docker.io/") {
		t.Error("manifest pulls from Docker Hub")
	}
}

// manifestDeployments returns the Deployments a multi-document manifest
// declares. A document with no namespace is skipped: neither embedded
// manifest declares a Deployment without one.
func manifestDeployments(t *testing.T, manifest string) []generators.Deployment {
	t.Helper()
	var out []generators.Deployment
	dec := yaml.NewDecoder(strings.NewReader(manifest))
	for {
		var doc struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
		}
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("parsing manifest: %v", err)
		}
		if doc.Kind == "Deployment" && doc.Metadata.Namespace != "" {
			out = append(out, generators.Deployment{Namespace: doc.Metadata.Namespace, Name: doc.Metadata.Name})
		}
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
		"platform/ingress-nginx.yaml.tmpl": &fstest.MapFile{
			Data: []byte(`lab={{.Lab.Name}}`),
		},
	}
	g := platform.NewWithFS(fsys)
	ctx := makeContext([]string{"ingress-nginx"})
	ctx.Lab.Name = "my-lab"
	ctx.Now = time.Now()

	_, _ = g.Generate(context.Background(), ctx)
}

// The ingress controller's worker count is fixed, not left to the number of
// CPUs the Docker host has: at one worker per CPU a ten-CPU host put the
// controller at its memory limit, and every reload was an OOM kill.
func TestIngressNginxManifest_FixesWorkerCount(t *testing.T) {
	files, err := platform.New().Generate(context.Background(), makeContext([]string{"ingress-nginx"}))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if !strings.Contains(files[0].Content, `worker-processes: "2"`) {
		t.Error("ingress-nginx ConfigMap does not fix worker-processes")
	}
}
