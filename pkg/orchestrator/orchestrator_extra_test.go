package orchestrator

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/zerolog"

	"github.com/jaimegago/petri/pkg/generators"
	"github.com/jaimegago/petri/pkg/metrics"
	localprov "github.com/jaimegago/petri/pkg/provisioners/local"
	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
)

// ── mockPlatformGen / mockObsGen ─────────────────────────────────────────────

type mockPlatformGen struct {
	generateFn func(ctx context.Context, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error)
}

func (m *mockPlatformGen) Generate(ctx context.Context, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error) {
	if m.generateFn != nil {
		return m.generateFn(ctx, tmplCtx)
	}
	return []generators.RenderedFile{{Path: "platform.yaml", Content: "apiVersion: v1\nkind: Namespace"}}, nil
}

type mockObsGen struct {
	generateFn func(ctx context.Context, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error)
}

func (m *mockObsGen) Generate(ctx context.Context, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error) {
	if m.generateFn != nil {
		return m.generateFn(ctx, tmplCtx)
	}
	return []generators.RenderedFile{{Path: "obs.yaml", Content: "apiVersion: v1\nkind: Namespace"}}, nil
}

// ── applyPlatformManifests ───────────────────────────────────────────────────

func TestApplyPlatformManifests_NilGen(t *testing.T) {
	o := New(Config{}, Deps{Log: zerolog.Nop()})
	kctl := &mockKubectl{}
	err := o.applyPlatformManifests(context.Background(), CreateOptions{
		Lab:     newTestLab(types.CloudProviderLocal),
		Company: newTestCompany(),
		Spec:    newTestCompany().Levels[1],
	}, kctl)
	if err != nil {
		t.Errorf("expected nil error when PlatformGen is nil, got: %v", err)
	}
	if len(kctl.applied) != 0 {
		t.Error("expected no manifests applied when PlatformGen is nil")
	}
}

func TestApplyPlatformManifests_Success(t *testing.T) {
	kctl := &mockKubectl{}
	o := New(Config{}, Deps{
		Log:         zerolog.Nop(),
		PlatformGen: &mockPlatformGen{},
	})
	err := o.applyPlatformManifests(context.Background(), CreateOptions{
		Lab:     newTestLab(types.CloudProviderLocal),
		Company: newTestCompany(),
		Spec:    newTestCompany().Levels[1],
	}, kctl)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(kctl.applied) == 0 {
		t.Error("expected at least one manifest to be applied")
	}
}

func TestApplyPlatformManifests_GenerateError(t *testing.T) {
	kctl := &mockKubectl{}
	o := New(Config{}, Deps{
		Log: zerolog.Nop(),
		PlatformGen: &mockPlatformGen{
			generateFn: func(_ context.Context, _ generators.TemplateContext) ([]generators.RenderedFile, error) {
				return nil, errors.New("template error")
			},
		},
	})
	err := o.applyPlatformManifests(context.Background(), CreateOptions{
		Lab:     newTestLab(types.CloudProviderLocal),
		Company: newTestCompany(),
		Spec:    newTestCompany().Levels[1],
	}, kctl)
	if err == nil {
		t.Error("expected error from generate failure")
	}
}

func TestApplyPlatformManifests_ApplyError(t *testing.T) {
	kctl := &mockKubectl{
		applyFn: func(_ context.Context, _ string) error {
			return errors.New("kubectl apply failed")
		},
	}
	o := New(Config{}, Deps{
		Log:         zerolog.Nop(),
		PlatformGen: &mockPlatformGen{},
	})
	err := o.applyPlatformManifests(context.Background(), CreateOptions{
		Lab:     newTestLab(types.CloudProviderLocal),
		Company: newTestCompany(),
		Spec:    newTestCompany().Levels[1],
	}, kctl)
	if err == nil {
		t.Error("expected error when apply fails")
	}
}

// ── applyObservabilityManifests ──────────────────────────────────────────────

func TestApplyObservabilityManifests_NilGen(t *testing.T) {
	o := New(Config{}, Deps{Log: zerolog.Nop()})
	kctl := &mockKubectl{}
	err := o.applyObservabilityManifests(context.Background(), CreateOptions{
		Lab:     newTestLab(types.CloudProviderLocal),
		Company: newTestCompany(),
		Spec:    newTestCompany().Levels[1],
	}, kctl)
	if err != nil {
		t.Errorf("expected nil error when ObsGen is nil, got: %v", err)
	}
}

func TestApplyObservabilityManifests_Success(t *testing.T) {
	kctl := &mockKubectl{}
	o := New(Config{}, Deps{
		Log:              zerolog.Nop(),
		ObservabilityGen: &mockObsGen{},
	})
	err := o.applyObservabilityManifests(context.Background(), CreateOptions{
		Lab:     newTestLab(types.CloudProviderLocal),
		Company: newTestCompany(),
		Spec:    newTestCompany().Levels[1],
	}, kctl)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(kctl.applied) == 0 {
		t.Error("expected at least one manifest to be applied")
	}
}

func TestApplyObservabilityManifests_ApplyError(t *testing.T) {
	kctl := &mockKubectl{
		applyFn: func(_ context.Context, _ string) error {
			return errors.New("apply error")
		},
	}
	o := New(Config{}, Deps{
		Log:              zerolog.Nop(),
		ObservabilityGen: &mockObsGen{},
	})
	err := o.applyObservabilityManifests(context.Background(), CreateOptions{
		Lab:     newTestLab(types.CloudProviderLocal),
		Company: newTestCompany(),
		Spec:    newTestCompany().Levels[1],
	}, kctl)
	if err == nil {
		t.Error("expected error when apply fails")
	}
}

// ── joinErrs ─────────────────────────────────────────────────────────────────

func TestJoinErrs(t *testing.T) {
	tests := []struct {
		name     string
		errs     []string
		expected string
	}{
		{"empty", []string{}, ""},
		{"single", []string{"one"}, "one"},
		{"multiple", []string{"one", "two", "three"}, "one; two; three"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := joinErrs(tt.errs)
			if got != tt.expected {
				t.Errorf("joinErrs(%v) = %q, want %q", tt.errs, got, tt.expected)
			}
		})
	}
}

// ── destroyFromMetadata ──────────────────────────────────────────────────────

func TestDestroyFromMetadata_CloudWithGitRepos(t *testing.T) {
	gitProv := &mockGitProv{}
	mgr := state.NewMockManager()

	lab := newTestLab(types.CloudProviderAWS)
	lab.Metadata.GitRepos = []types.GitRepo{
		{Name: "acme-infra", URL: "https://github.com/acme-org/acme-infra.git", Type: "infra"},
		{Name: "acme-apps", URL: "https://github.com/acme-org/acme-apps.git", Type: "apps"},
	}

	o := New(Config{WorkDir: t.TempDir()}, Deps{
		State:   mgr,
		Log:     zerolog.Nop(),
		GitProv: gitProv,
	})

	err := o.destroyFromMetadata(context.Background(), lab)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(gitProv.deleted) != 2 {
		t.Errorf("expected 2 repos deleted, got %d", len(gitProv.deleted))
	}
}

func TestDestroyFromMetadata_CloudNoOwnerSkipped(t *testing.T) {
	gitProv := &mockGitProv{}

	lab := newTestLab(types.CloudProviderAWS)
	lab.Metadata.GitRepos = []types.GitRepo{
		{Name: "repo", URL: "", Type: "infra"}, // no URL → owner can't be resolved
	}

	o := New(Config{}, Deps{Log: zerolog.Nop(), GitProv: gitProv})
	err := o.destroyFromMetadata(context.Background(), lab)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(gitProv.deleted) != 0 {
		t.Error("expected no deletes when owner can't be resolved")
	}
}

func TestDestroyFromMetadata_LocalClusters(t *testing.T) {
	localProv := &mockLocalProv{}

	lab := newTestLab(types.CloudProviderLocal)
	lab.Metadata.Clusters = []types.Cluster{{Name: "my-cluster"}}

	o := New(Config{}, Deps{Log: zerolog.Nop(), LocalProv: localProv})
	err := o.destroyFromMetadata(context.Background(), lab)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(localProv.deleted) != 1 || localProv.deleted[0] != "my-cluster" {
		t.Errorf("expected my-cluster deleted, got: %v", localProv.deleted)
	}
}

func TestDestroyFromMetadata_DeleteError(t *testing.T) {
	localProv := &mockLocalProv{
		deleteFn: func(_ context.Context, _ string) error {
			return errors.New("delete failed")
		},
	}
	lab := newTestLab(types.CloudProviderLocal)
	lab.Metadata.Clusters = []types.Cluster{{Name: "my-cluster"}}

	o := New(Config{}, Deps{Log: zerolog.Nop(), LocalProv: localProv})
	err := o.destroyFromMetadata(context.Background(), lab)
	if err == nil {
		t.Error("expected error when delete fails")
	}
}

// ── StartCleanupLoop ─────────────────────────────────────────────────────────

func TestStartCleanupLoop_StopsOnCancel(t *testing.T) {
	mgr := state.NewMockManager()
	o := New(Config{WorkDir: t.TempDir()}, Deps{
		State: mgr,
		Log:   zerolog.Nop(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		o.StartCleanupLoop(ctx, 100*time.Millisecond, 0)
		close(done)
	}()

	cancel()
	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Error("StartCleanupLoop did not stop after context cancel")
	}
}

func TestStartCleanupLoop_CleansExpiredLab(t *testing.T) {
	localProv := &mockLocalProv{}
	mgr := state.NewMockManager()

	lab := &types.Lab{
		ID:            uuid.New(),
		Name:          "expired-lab",
		Company:       "acme",
		Level:         1,
		CloudProvider: types.CloudProviderLocal,
		Status:        types.LabStatusActive,
		CreatedAt:     time.Now(),
		TTLHours:      1,
		ExpiresAt:     time.Now().Add(-1 * time.Hour),
	}
	lab.Metadata.Clusters = []types.Cluster{{Name: lab.Name}}
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	o := New(Config{WorkDir: t.TempDir()}, Deps{
		State:     mgr,
		Log:       zerolog.Nop(),
		LocalProv: localProv,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go o.StartCleanupLoop(ctx, 50*time.Millisecond, 0)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		updated, err := mgr.GetLab(context.Background(), lab.ID)
		if err == nil && updated.Status == types.LabStatusDestroyed {
			return // success
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("lab was not cleaned up by StartCleanupLoop within timeout")
}

// ── createCloud nil-git guard ─────────────────────────────────────────────────

func TestCreate_Cloud_NoGitProv_ReturnsError(t *testing.T) {
	mgr := state.NewMockManager()
	lab := newTestLab(types.CloudProviderAWS)
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	o := New(Config{WorkDir: t.TempDir()}, Deps{
		State:   mgr,
		Log:     zerolog.Nop(),
		GitProv: nil,
	})

	err := o.Create(context.Background(), CreateOptions{
		Lab:     lab,
		Company: newTestCompany(),
		Spec:    newTestCompany().Levels[1],
	})
	if err == nil {
		t.Fatal("expected error when GitProv is nil for cloud lab")
	}
}

// ── Metrics wiring ───────────────────────────────────────────────────────────

func TestCreate_Local_RecordsMetrics(t *testing.T) {
	kubeconfigPath := t.TempDir() + "/kubeconfig"
	if err := os.WriteFile(kubeconfigPath, []byte("apiVersion: v1"), 0o600); err != nil {
		t.Fatal(err)
	}

	localProv := &mockLocalProv{
		createFn: func(_ context.Context, opts localprov.CreateOptions) (*localprov.ClusterInfo, error) {
			return &localprov.ClusterInfo{Name: opts.Name, KubeconfigPath: kubeconfigPath, NodeCount: 1}, nil
		},
	}
	kctl := &mockKubectl{}
	mgr := state.NewMockManager()

	reg := prometheus.NewRegistry()
	rec := metrics.New(reg)

	lab := newTestLab(types.CloudProviderLocal)
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	factory := func(_ string) KubectlClient { return kctl }
	o := New(Config{WorkDir: t.TempDir()}, Deps{
		State:          mgr,
		Cipher:         &mockCipher{},
		Log:            zerolog.Nop(),
		Metrics:        rec,
		LocalProv:      localProv,
		KubectlFactory: factory,
		AppsGen:        &mockAppsGen{},
	})

	if err := o.Create(context.Background(), CreateOptions{
		Lab:     lab,
		Company: newTestCompany(),
		Spec:    newTestCompany().Levels[1],
	}); err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	mfs, _ := reg.Gather()
	found := false
	for _, mf := range mfs {
		if mf.GetName() == "petri_labs_created_total" {
			found = true
			if mf.GetMetric()[0].GetCounter().GetValue() != 1 {
				t.Errorf("labs_created_total: got %v, want 1", mf.GetMetric()[0].GetCounter().GetValue())
			}
		}
	}
	if !found {
		t.Error("labs_created_total metric not found after Create")
	}
}
