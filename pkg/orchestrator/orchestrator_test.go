package orchestrator

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jaimegago/petri/pkg/logger"

	"github.com/jaimegago/petri/pkg/generators"
	gitprov "github.com/jaimegago/petri/pkg/provisioners/git"
	localprov "github.com/jaimegago/petri/pkg/provisioners/local"
	tfprov "github.com/jaimegago/petri/pkg/provisioners/terraform"
	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
)

// ── Mock implementations ──────────────────────────────────────────────────────

type mockLocalProv struct {
	createFn func(ctx context.Context, opts localprov.CreateOptions) (*localprov.ClusterInfo, error)
	deleteFn func(ctx context.Context, name string) error
	deleted  []string
}

func (m *mockLocalProv) Create(ctx context.Context, opts localprov.CreateOptions) (*localprov.ClusterInfo, error) {
	if m.createFn != nil {
		return m.createFn(ctx, opts)
	}
	return &localprov.ClusterInfo{Name: opts.Name, KubeconfigPath: "/tmp/kubeconfig", NodeCount: 1}, nil
}

func (m *mockLocalProv) Delete(ctx context.Context, name string) error {
	m.deleted = append(m.deleted, name)
	if m.deleteFn != nil {
		return m.deleteFn(ctx, name)
	}
	return nil
}

type mockGitProv struct {
	createFn func(ctx context.Context, opts gitprov.CreateOptions) (*gitprov.RepoInfo, error)
	deleteFn func(ctx context.Context, opts gitprov.DeleteOptions) error
	created  []string
	deleted  []string
}

func (m *mockGitProv) Create(ctx context.Context, opts gitprov.CreateOptions) (*gitprov.RepoInfo, error) {
	m.created = append(m.created, opts.Name)
	if m.createFn != nil {
		return m.createFn(ctx, opts)
	}
	return &gitprov.RepoInfo{Name: opts.Name, FullName: opts.Owner + "/" + opts.Name, CloneURL: "https://github.com/" + opts.Owner + "/" + opts.Name + ".git"}, nil
}

func (m *mockGitProv) Delete(ctx context.Context, opts gitprov.DeleteOptions) error {
	m.deleted = append(m.deleted, opts.Name)
	if m.deleteFn != nil {
		return m.deleteFn(ctx, opts)
	}
	return nil
}

type mockTFProv struct {
	initFn    func(ctx context.Context, opts tfprov.InitOptions) error
	applyFn   func(ctx context.Context, opts tfprov.ApplyOptions) (*tfprov.ApplyResult, error)
	destroyFn func(ctx context.Context, opts tfprov.DestroyOptions) error
	outputFn  func(ctx context.Context, workDir string, env []string) (map[string]tfprov.OutputValue, error)
}

func (m *mockTFProv) Init(ctx context.Context, opts tfprov.InitOptions) error {
	if m.initFn != nil {
		return m.initFn(ctx, opts)
	}
	return nil
}

func (m *mockTFProv) Apply(ctx context.Context, opts tfprov.ApplyOptions) (*tfprov.ApplyResult, error) {
	if m.applyFn != nil {
		return m.applyFn(ctx, opts)
	}
	return &tfprov.ApplyResult{}, nil
}

func (m *mockTFProv) Destroy(ctx context.Context, opts tfprov.DestroyOptions) error {
	if m.destroyFn != nil {
		return m.destroyFn(ctx, opts)
	}
	return nil
}

func (m *mockTFProv) Output(ctx context.Context, workDir string, env []string) (map[string]tfprov.OutputValue, error) {
	if m.outputFn != nil {
		return m.outputFn(ctx, workDir, env)
	}
	return nil, nil
}


type mockKubectl struct {
	applyFn  func(ctx context.Context, manifest string) error
	waitFn   func(ctx context.Context, timeout time.Duration) error
	applied  []string
}

func (m *mockKubectl) ApplyManifest(ctx context.Context, manifest string) error {
	m.applied = append(m.applied, manifest)
	if m.applyFn != nil {
		return m.applyFn(ctx, manifest)
	}
	return nil
}

func (m *mockKubectl) WaitForNodes(ctx context.Context, timeout time.Duration) error {
	if m.waitFn != nil {
		return m.waitFn(ctx, timeout)
	}
	return nil
}

func (m *mockKubectl) WaitForRollout(_ context.Context, _, _ string, _ time.Duration) error {
	return nil
}

func (m *mockKubectl) GetNodeCount(_ context.Context) (int, error) { return 1, nil }

type mockAppsGen struct {
	generateFn func(ctx context.Context, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error)
}

func (m *mockAppsGen) Generate(ctx context.Context, tmplCtx generators.TemplateContext) ([]generators.RenderedFile, error) {
	if m.generateFn != nil {
		return m.generateFn(ctx, tmplCtx)
	}
	return []generators.RenderedFile{{Path: "ns.yaml", Content: "apiVersion: v1\nkind: Namespace"}}, nil
}

type mockCipher struct{}

func (m *mockCipher) Encrypt(plaintext []byte) (string, error) { return string(plaintext), nil }
func (m *mockCipher) Decrypt(ciphertext string) ([]byte, error) { return []byte(ciphertext), nil }

// ── Helpers ───────────────────────────────────────────────────────────────────

func newTestLab(provider types.CloudProvider) *types.Lab {
	now := time.Now()
	return &types.Lab{
		ID:            uuid.New(),
		Name:          "test-lab",
		Company:       "acme",
		Level:         1,
		CloudProvider: provider,
		Status:        types.LabStatusCreating,
		CreatedAt:     now,
		TTLHours:      2,
		ExpiresAt:     now.Add(2 * time.Hour),
	}
}

func newTestCompany() *types.Company {
	return &types.Company{
		Name:          "Acme",
		CloudProvider: types.CloudProviderAWS,
		IaCTool:       types.IaCToolTerraform,
		GitOpsTool:    types.GitOpsToolArgoCD,
		GitProvider:   types.GitProviderGitHub,
		GitHubOrg:     "acme-org",
		Authors: []types.Author{
			{Name: "Test User", Email: "test@acme.com", Role: "engineer"},
		},
		Levels: map[int]types.LevelSpec{
			1: {
				Clusters:        1,
				ClusterNames:    []string{"main"},
				NodesPerCluster: []int{1},
				Apps:            []string{"frontend"},
				Namespaces:      []string{"default"},
				TTLDefaultHours: 2,
			},
		},
	}
}

func newTestOrchestrator(t *testing.T, mgr state.Manager, localProv LocalProvisioner, kctl *mockKubectl) *Orchestrator {
	t.Helper()

	kubeconfigFile := t.TempDir() + "/kubeconfig"
	if err := os.WriteFile(kubeconfigFile, []byte("apiVersion: v1"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Wrap the mock kubectl so the factory always returns it.
	factory := func(_ string) KubectlClient { return kctl }

	return New(Config{WorkDir: t.TempDir()}, Deps{
		State:          mgr,
		Cipher:         &mockCipher{},
		Log:            logger.Nop(),
		LocalProv:      localProv,
		KubectlFactory: factory,
		AppsGen:        &mockAppsGen{},
	})
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestCreate_Local_Success(t *testing.T) {
	kubeconfigPath := t.TempDir() + "/kubeconfig"
	if err := os.WriteFile(kubeconfigPath, []byte("apiVersion: v1"), 0o600); err != nil {
		t.Fatal(err)
	}

	localProv := &mockLocalProv{
		createFn: func(_ context.Context, opts localprov.CreateOptions) (*localprov.ClusterInfo, error) {
			return &localprov.ClusterInfo{
				Name:           opts.Name,
				KubeconfigPath: kubeconfigPath,
				NodeCount:      1,
			}, nil
		},
	}
	kctl := &mockKubectl{}
	mgr := state.NewMockManager()

	lab := newTestLab(types.CloudProviderLocal)
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	orch := newTestOrchestrator(t, mgr, localProv, kctl)

	err := orch.Create(context.Background(), CreateOptions{
		Lab:     lab,
		Company: newTestCompany(),
		Spec:    newTestCompany().Levels[1],
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Verify lab is ACTIVE.
	updated, err := mgr.GetLab(context.Background(), lab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != types.LabStatusActive {
		t.Errorf("expected ACTIVE, got %s", updated.Status)
	}
	if len(updated.Metadata.Clusters) == 0 {
		t.Error("expected cluster metadata to be populated")
	}
}

func TestCreate_Local_ClusterFailure_MarksError(t *testing.T) {
	localProv := &mockLocalProv{
		createFn: func(_ context.Context, _ localprov.CreateOptions) (*localprov.ClusterInfo, error) {
			return nil, errors.New("docker not running")
		},
	}
	kctl := &mockKubectl{}
	mgr := state.NewMockManager()

	lab := newTestLab(types.CloudProviderLocal)
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	orch := newTestOrchestrator(t, mgr, localProv, kctl)
	err := orch.Create(context.Background(), CreateOptions{
		Lab:     lab,
		Company: newTestCompany(),
		Spec:    newTestCompany().Levels[1],
	})
	if err == nil {
		t.Fatal("expected error")
	}

	updated, getErr := mgr.GetLab(context.Background(), lab.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if updated.Status != types.LabStatusError {
		t.Errorf("expected ERROR, got %s", updated.Status)
	}
}

func TestCreate_Local_Rollback_OnKubectlWaitFailure(t *testing.T) {
	kubeconfigPath := t.TempDir() + "/kubeconfig"
	if err := os.WriteFile(kubeconfigPath, []byte("apiVersion: v1"), 0o600); err != nil {
		t.Fatal(err)
	}

	localProv := &mockLocalProv{
		createFn: func(_ context.Context, opts localprov.CreateOptions) (*localprov.ClusterInfo, error) {
			return &localprov.ClusterInfo{Name: opts.Name, KubeconfigPath: kubeconfigPath, NodeCount: 1}, nil
		},
	}
	kctl := &mockKubectl{
		waitFn: func(_ context.Context, _ time.Duration) error {
			return errors.New("timeout waiting for nodes")
		},
	}
	mgr := state.NewMockManager()

	lab := newTestLab(types.CloudProviderLocal)
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	orch := newTestOrchestrator(t, mgr, localProv, kctl)
	err := orch.Create(context.Background(), CreateOptions{
		Lab:     lab,
		Company: newTestCompany(),
		Spec:    newTestCompany().Levels[1],
	})
	if err == nil {
		t.Fatal("expected error")
	}

	// Rollback should have deleted the cluster.
	if len(localProv.deleted) == 0 {
		t.Error("expected rollback to delete the cluster")
	}
}

func TestCreate_Local_NoApps(t *testing.T) {
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

	lab := newTestLab(types.CloudProviderLocal)
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	orch := newTestOrchestrator(t, mgr, localProv, kctl)
	err := orch.Create(context.Background(), CreateOptions{
		Lab:     lab,
		Company: newTestCompany(),
		Spec:    newTestCompany().Levels[1],
		NoApps:  true,
	})
	if err != nil {
		t.Fatalf("Create with --no-apps failed: %v", err)
	}

	// No manifests should have been applied.
	if len(kctl.applied) != 0 {
		t.Errorf("expected no manifests applied with --no-apps, got %d", len(kctl.applied))
	}
}

func TestCreate_UnsupportedProvider(t *testing.T) {
	mgr := state.NewMockManager()
	lab := newTestLab("invalid")
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	orch := New(Config{WorkDir: t.TempDir()}, Deps{
		State:  mgr,
		Cipher: &mockCipher{},
		Log:    logger.Nop(),
	})

	err := orch.Create(context.Background(), CreateOptions{
		Lab:     lab,
		Company: newTestCompany(),
		Spec:    newTestCompany().Levels[1],
	})
	if err == nil {
		t.Fatal("expected error for unsupported provider")
	}
}

func TestDestroy_Local_Success(t *testing.T) {
	localProv := &mockLocalProv{}
	mgr := state.NewMockManager()

	lab := newTestLab(types.CloudProviderLocal)
	lab.Status = types.LabStatusDestroying
	lab.Metadata.Clusters = []types.Cluster{{Name: "test-lab"}}
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	orch := New(Config{WorkDir: t.TempDir()}, Deps{
		State:     mgr,
		Cipher:    &mockCipher{},
		Log:       logger.Nop(),
		LocalProv: localProv,
	})

	err := orch.Destroy(context.Background(), DestroyOptions{
		Lab:     lab,
		Company: newTestCompany(),
		Spec:    newTestCompany().Levels[1],
	})
	if err != nil {
		t.Fatalf("Destroy failed: %v", err)
	}

	updated, err := mgr.GetLab(context.Background(), lab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != types.LabStatusDestroyed {
		t.Errorf("expected DESTROYED, got %s", updated.Status)
	}
	if len(localProv.deleted) == 0 {
		t.Error("expected cluster to be deleted")
	}
}

func TestDestroy_Local_Force_OnError(t *testing.T) {
	localProv := &mockLocalProv{
		deleteFn: func(_ context.Context, _ string) error {
			return errors.New("kind not found")
		},
	}
	mgr := state.NewMockManager()

	lab := newTestLab(types.CloudProviderLocal)
	lab.Status = types.LabStatusDestroying
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	orch := New(Config{WorkDir: t.TempDir()}, Deps{
		State:     mgr,
		Cipher:    &mockCipher{},
		Log:       logger.Nop(),
		LocalProv: localProv,
	})

	err := orch.Destroy(context.Background(), DestroyOptions{
		Lab:     lab,
		Company: newTestCompany(),
		Spec:    newTestCompany().Levels[1],
		Force:   true,
	})
	if err != nil {
		t.Fatalf("Force destroy should not return error, got: %v", err)
	}

	updated, _ := mgr.GetLab(context.Background(), lab.ID)
	if updated.Status != types.LabStatusDestroyed {
		t.Errorf("expected DESTROYED with force, got %s", updated.Status)
	}
}

func TestDestroy_Local_NonForce_OnError(t *testing.T) {
	localProv := &mockLocalProv{
		deleteFn: func(_ context.Context, _ string) error {
			return errors.New("kind not found")
		},
	}
	mgr := state.NewMockManager()

	lab := newTestLab(types.CloudProviderLocal)
	lab.Status = types.LabStatusDestroying
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	orch := New(Config{WorkDir: t.TempDir()}, Deps{
		State:     mgr,
		Cipher:    &mockCipher{},
		Log:       logger.Nop(),
		LocalProv: localProv,
	})

	err := orch.Destroy(context.Background(), DestroyOptions{
		Lab:     lab,
		Company: newTestCompany(),
		Spec:    newTestCompany().Levels[1],
		Force:   false,
	})
	if err == nil {
		t.Fatal("expected error without --force")
	}

	updated, _ := mgr.GetLab(context.Background(), lab.ID)
	if updated.Status != types.LabStatusError {
		t.Errorf("expected ERROR without force, got %s", updated.Status)
	}
}

func TestExportCredentials(t *testing.T) {
	mgr := state.NewMockManager()
	lab := newTestLab(types.CloudProviderLocal)
	lab.Status = types.LabStatusActive
	lab.Metadata.GitRepos = []types.GitRepo{{Name: "acme-test-infra", URL: "https://github.com/acme-org/acme-test-infra.git", Type: "infra"}}
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	// Store a kubeconfig credential.
	cred := &types.LabCredential{
		ID:             uuid.New(),
		LabID:          lab.ID,
		CredentialType: "kubeconfig",
		EncryptedValue: "apiVersion: v1",
		CreatedAt:      time.Now(),
	}
	if err := mgr.StoreCredential(context.Background(), cred); err != nil {
		t.Fatal(err)
	}

	orch := New(Config{}, Deps{
		State:  mgr,
		Cipher: &mockCipher{},
		Log:    logger.Nop(),
	})

	outputPath := t.TempDir() + "/bundle.enc"
	err := orch.ExportCredentials(context.Background(), lab, outputPath)
	if err != nil {
		t.Fatalf("ExportCredentials failed: %v", err)
	}

	// Verify file exists.
	if _, statErr := os.Stat(outputPath); statErr != nil {
		t.Errorf("expected bundle file to exist: %v", statErr)
	}
}

func TestCleanup_RunCleanup(t *testing.T) {
	localProv := &mockLocalProv{}
	mgr := state.NewMockManager()

	// Create an expired lab.
	lab := newTestLab(types.CloudProviderLocal)
	lab.Status = types.LabStatusActive
	lab.ExpiresAt = time.Now().Add(-2 * time.Hour) // expired 2 hours ago
	lab.Metadata.Clusters = []types.Cluster{{Name: lab.Name}}
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	orch := New(Config{WorkDir: t.TempDir()}, Deps{
		State:     mgr,
		Cipher:    &mockCipher{},
		Log:       logger.Nop(),
		LocalProv: localProv,
	})

	// grace period is 0 so the expired lab is found immediately.
	orch.runCleanup(context.Background(), 0)

	updated, err := mgr.GetLab(context.Background(), lab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != types.LabStatusDestroyed {
		t.Errorf("expected DESTROYED after cleanup, got %s", updated.Status)
	}
}

func TestExtractOwnerFromURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected string
	}{
		{"github https", "https://github.com/acme-org/repo.git", "acme-org"},
		{"empty url", "", ""},
		{"non-github", "https://gitlab.com/owner/repo.git", ""},
		{"no owner", "https://github.com/", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractOwnerFromURL(tt.url)
			if got != tt.expected {
				t.Errorf("extractOwnerFromURL(%q) = %q, want %q", tt.url, got, tt.expected)
			}
		})
	}
}
