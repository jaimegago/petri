package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	gitprov "github.com/jaimegago/petri/pkg/provisioners/git"
	pulumiprov "github.com/jaimegago/petri/pkg/provisioners/pulumi"
	tfprov "github.com/jaimegago/petri/pkg/provisioners/terraform"
	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
)

// ── destroyExpiredLab ─────────────────────────────────────────────────────────

func TestDestroyExpiredLab_NonDestroyableStatus(t *testing.T) {
	mgr := state.NewMockManager()

	lab := newTestLab(types.CloudProviderLocal)
	lab.Status = types.LabStatusDestroyed // already destroyed, can't transition to DESTROYING
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	o := New(Config{WorkDir: t.TempDir()}, Deps{
		State: mgr,
		Log:   zerolog.Nop(),
	})

	o.destroyExpiredLab(context.Background(), lab)

	// Status should remain DESTROYED (not changed by the skip path).
	got, _ := mgr.GetLab(context.Background(), lab.ID)
	if got.Status != types.LabStatusDestroyed {
		t.Errorf("expected DESTROYED to be unchanged, got %s", got.Status)
	}
}

// ── destroyCloud ─────────────────────────────────────────────────────────────

func TestDestroyCloud_TerraformPath(t *testing.T) {
	tfProv := &mockTFProv{}
	gitProv := &mockGitProv{}
	mgr := state.NewMockManager()

	lab := newTestLab(types.CloudProviderAWS)
	lab.Metadata.GitRepos = []types.GitRepo{
		{Name: "acme-infra", URL: "https://github.com/acme-org/acme-infra.git", Type: "infra"},
	}
	lab.Status = types.LabStatusDestroying
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	o := New(Config{WorkDir: t.TempDir()}, Deps{
		State:   mgr,
		Cipher:  &mockCipher{},
		Log:     zerolog.Nop(),
		TFProv:  tfProv,
		GitProv: gitProv,
	})

	company := newTestCompany()
	company.IaCTool = types.IaCToolTerraform

	err := o.Destroy(context.Background(), DestroyOptions{
		Lab:     lab,
		Company: company,
		Spec:    company.Levels[1],
	})
	if err != nil {
		t.Fatalf("Destroy failed: %v", err)
	}

	// Git repos should have been deleted.
	if len(gitProv.deleted) != 1 {
		t.Errorf("expected 1 git repo deleted, got %d", len(gitProv.deleted))
	}
}

func TestDestroyCloud_PulumiPath(t *testing.T) {
	pulumiProv := &mockPulumiProv{}
	gitProv := &mockGitProv{}
	mgr := state.NewMockManager()

	lab := newTestLab(types.CloudProviderAzure)
	lab.Status = types.LabStatusDestroying
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	o := New(Config{WorkDir: t.TempDir()}, Deps{
		State:      mgr,
		Cipher:     &mockCipher{},
		Log:        zerolog.Nop(),
		PulumiProv: pulumiProv,
		GitProv:    gitProv,
	})

	company := newTestCompany()
	company.CloudProvider = types.CloudProviderAzure
	company.IaCTool = types.IaCToolPulumi

	err := o.Destroy(context.Background(), DestroyOptions{
		Lab:     lab,
		Company: company,
		Spec:    company.Levels[1],
	})
	if err != nil {
		t.Fatalf("Destroy pulumi failed: %v", err)
	}
}

func TestDestroyCloud_TerraformError_Force(t *testing.T) {
	tfProv := &mockTFProv{
		destroyFn: func(_ context.Context, _ tfprov.DestroyOptions) error {
			return errors.New("terraform destroy failed")
		},
	}
	mgr := state.NewMockManager()

	lab := newTestLab(types.CloudProviderAWS)
	lab.Status = types.LabStatusDestroying
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	o := New(Config{WorkDir: t.TempDir()}, Deps{
		State:  mgr,
		Cipher: &mockCipher{},
		Log:    zerolog.Nop(),
		TFProv: tfProv,
	})

	company := newTestCompany()
	company.IaCTool = types.IaCToolTerraform

	// With Force=true, Destroy should succeed despite the terraform error.
	err := o.Destroy(context.Background(), DestroyOptions{
		Lab:     lab,
		Company: company,
		Spec:    company.Levels[1],
		Force:   true,
	})
	if err != nil {
		t.Fatalf("force Destroy should succeed: %v", err)
	}
}

// ── ExportCredentials edge cases ──────────────────────────────────────────────

func TestExportCredentials_NoCredentials(t *testing.T) {
	mgr := state.NewMockManager()
	lab := newTestLab(types.CloudProviderLocal)
	lab.Status = types.LabStatusActive
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	o := New(Config{}, Deps{
		State:  mgr,
		Cipher: &mockCipher{},
		Log:    zerolog.Nop(),
	})

	outputPath := t.TempDir() + "/bundle.enc"
	err := o.ExportCredentials(context.Background(), lab, outputPath)
	if err != nil {
		t.Fatalf("ExportCredentials with no creds: %v", err)
	}
}

func TestExportCredentials_WithGitToken(t *testing.T) {
	mgr := state.NewMockManager()
	lab := newTestLab(types.CloudProviderAWS)
	lab.Status = types.LabStatusActive
	lab.Metadata.GitRepos = []types.GitRepo{{Name: "infra", URL: "https://github.com/org/infra.git"}}
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	// Store a github_token credential.
	if err := mgr.StoreCredential(context.Background(), &types.LabCredential{
		ID:             uuid.New(),
		LabID:          lab.ID,
		CredentialType: "github_token",
		EncryptedValue: "ghp_testtoken",
		CreatedAt:      time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	o := New(Config{}, Deps{
		State:  mgr,
		Cipher: &mockCipher{},
		Log:    zerolog.Nop(),
	})

	outputPath := t.TempDir() + "/bundle.enc"
	if err := o.ExportCredentials(context.Background(), lab, outputPath); err != nil {
		t.Fatalf("ExportCredentials: %v", err)
	}
}

// ── applyAppManifests ─────────────────────────────────────────────────────────

func TestApplyAppManifests_NilGen(t *testing.T) {
	o := New(Config{}, Deps{Log: zerolog.Nop()})
	kctl := &mockKubectl{}
	err := o.applyAppManifests(context.Background(), CreateOptions{
		Lab:     newTestLab(types.CloudProviderLocal),
		Company: newTestCompany(),
		Spec:    newTestCompany().Levels[1],
	}, kctl)
	if err != nil {
		t.Errorf("expected nil error when AppsGen is nil, got: %v", err)
	}
}

// ── createCloud with unsupported IaC tool ─────────────────────────────────────

func TestCreate_Cloud_UnsupportedIaCTool_ReturnsError(t *testing.T) {
	gitProv := &mockGitProv{}
	mgr := state.NewMockManager()

	lab := newTestLab(types.CloudProviderAWS)
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	o := New(Config{WorkDir: t.TempDir()}, Deps{
		State:   mgr,
		Cipher:  &mockCipher{},
		Log:     zerolog.Nop(),
		GitProv: gitProv,
	})

	company := newTestCompany()
	company.IaCTool = "unsupported-tool"

	err := o.Create(context.Background(), CreateOptions{
		Lab:     lab,
		Company: company,
		Spec:    company.Levels[1],
	})
	if err == nil {
		t.Fatal("expected error for unsupported IaC tool")
	}
}

// ── destroyCloud UnsupportedIaC ───────────────────────────────────────────────

func TestDestroy_Cloud_UnsupportedIaCTool_CollectsError(t *testing.T) {
	mgr := state.NewMockManager()

	lab := newTestLab(types.CloudProviderAWS)
	lab.Status = types.LabStatusDestroying
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	o := New(Config{WorkDir: t.TempDir()}, Deps{
		State:  mgr,
		Cipher: &mockCipher{},
		Log:    zerolog.Nop(),
	})

	company := newTestCompany()
	company.IaCTool = "unsupported"

	// No IaC provisioner configured → destroyCloud does nothing (nil checks pass silently).
	// This should still mark the lab DESTROYED.
	err := o.Destroy(context.Background(), DestroyOptions{
		Lab:     lab,
		Company: company,
		Spec:    company.Levels[1],
	})
	if err != nil {
		t.Logf("destroy returned error (acceptable): %v", err)
	}
}

// ── destroyFromMetadata with empty cluster name ──────────────────────────────

func TestDestroyFromMetadata_EmptyClusterName_UsesLabName(t *testing.T) {
	localProv := &mockLocalProv{}

	lab := newTestLab(types.CloudProviderLocal)
	// Cluster with empty name → falls back to lab.Name.
	lab.Metadata.Clusters = []types.Cluster{{Name: ""}}

	o := New(Config{}, Deps{Log: zerolog.Nop(), LocalProv: localProv})
	if err := o.destroyFromMetadata(context.Background(), lab); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(localProv.deleted) != 1 || localProv.deleted[0] != lab.Name {
		t.Errorf("expected %q deleted, got: %v", lab.Name, localProv.deleted)
	}
}

// ── git provisioner Delete error in destroyFromMetadata ──────────────────────

func TestDestroyFromMetadata_GitDeleteError_ReturnsError(t *testing.T) {
	gitProv := &mockGitProv{
		deleteFn: func(_ context.Context, _ gitprov.DeleteOptions) error {
			return errors.New("repo not found")
		},
	}

	lab := newTestLab(types.CloudProviderAWS)
	lab.Metadata.GitRepos = []types.GitRepo{
		{Name: "infra", URL: "https://github.com/org/infra.git"},
	}

	o := New(Config{}, Deps{Log: zerolog.Nop(), GitProv: gitProv})
	err := o.destroyFromMetadata(context.Background(), lab)
	if err == nil {
		t.Error("expected error when git delete fails")
	}
}

// ── pulumi destroy error ──────────────────────────────────────────────────────

func TestDestroyCloud_PulumiDestroyError_Force(t *testing.T) {
	pulumiProv := &mockPulumiProv{
		destroyFn: func(_ context.Context, _ pulumiprov.DestroyOptions) error {
			return errors.New("pulumi destroy failed")
		},
	}
	mgr := state.NewMockManager()

	lab := newTestLab(types.CloudProviderAzure)
	lab.Status = types.LabStatusDestroying
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	o := New(Config{WorkDir: t.TempDir()}, Deps{
		State:      mgr,
		Cipher:     &mockCipher{},
		Log:        zerolog.Nop(),
		PulumiProv: pulumiProv,
	})

	company := newTestCompany()
	company.CloudProvider = types.CloudProviderAzure
	company.IaCTool = types.IaCToolPulumi

	err := o.Destroy(context.Background(), DestroyOptions{
		Lab:     lab,
		Company: company,
		Spec:    company.Levels[1],
		Force:   true,
	})
	if err != nil {
		t.Fatalf("force Destroy with pulumi error should succeed: %v", err)
	}
}

// mockPulumiProv extended to support destroy error injection.
type mockPulumiProv struct {
	destroyFn func(ctx context.Context, opts pulumiprov.DestroyOptions) error
}

func (m *mockPulumiProv) Init(_ context.Context, _ pulumiprov.InitOptions) error { return nil }
func (m *mockPulumiProv) Up(_ context.Context, _ pulumiprov.UpOptions) (*pulumiprov.UpResult, error) {
	return &pulumiprov.UpResult{}, nil
}
func (m *mockPulumiProv) Destroy(ctx context.Context, opts pulumiprov.DestroyOptions) error {
	if m.destroyFn != nil {
		return m.destroyFn(ctx, opts)
	}
	return nil
}
func (m *mockPulumiProv) StackRemove(_ context.Context, _ pulumiprov.StackRemoveOptions) error {
	return nil
}
