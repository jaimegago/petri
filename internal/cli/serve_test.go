package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaimegago/petri/pkg/asynctasks"
	"github.com/jaimegago/petri/pkg/config"
	"github.com/jaimegago/petri/pkg/logger"
	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
)

func newServeTestCLI(t *testing.T, mgr state.Manager) *CLI {
	t.Helper()
	c := newTestCLI(mgr, companiesYAML(t))
	c.cfg = config.DefaultConfig()
	c.log = logger.Nop()
	return c
}

// TestResolveServeLabInfo_ExpiredLab_RefusesAndDoesNotResolveKubeconfig
// verifies serve --lab refuses an EXPIRED lab with an actionable error and
// never returns a kubeconfig path that the caller would bind to.
func TestResolveServeLabInfo_ExpiredLab_RefusesAndDoesNotResolveKubeconfig(t *testing.T) {
	mgr := state.NewMockManager()
	lab := &types.Lab{
		ID:            uuid.New(),
		Name:          "verify-smoke",
		Company:       "acme",
		Level:         1,
		CloudProvider: types.CloudProviderLocal,
		Status:        types.LabStatusActive, // will be lazily transitioned
		CreatedAt:     time.Now().Add(-5 * time.Hour),
		ExpiresAt:     time.Now().Add(-1 * time.Hour),
		TTLHours:      1,
		Metadata: types.LabMetadata{
			Clusters: []types.Cluster{{Name: "verify-smoke", KubeconfigPath: "/tmp/fake"}},
		},
	}
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	c := newServeTestCLI(t, mgr)
	_, err := c.resolveServeLabInfo(context.Background(), "", "verify-smoke")
	if err == nil {
		t.Fatal("expected refusal for EXPIRED lab")
	}
	if !strings.Contains(err.Error(), "EXPIRED") {
		t.Errorf("error message should mention EXPIRED status; got: %v", err)
	}
	if !strings.Contains(err.Error(), "petri destroy") || !strings.Contains(err.Error(), "petri cleanup --expired") {
		t.Errorf("error message should include actionable next-step commands; got: %v", err)
	}

	// Underlying DB record must have been lazily transitioned.
	got, err := mgr.GetLab(context.Background(), lab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != types.LabStatusExpired {
		t.Errorf("expected lab to be transitioned to EXPIRED, got %s", got.Status)
	}
}

// TestResolveServeLabInfo_MissingKubeconfig_RefusesAndMarksError verifies
// the substrate-precondition: an ACTIVE record with a missing kubeconfig
// file is refused, the record is marked ERROR, and a follow-up command is
// suggested.
func TestResolveServeLabInfo_MissingKubeconfig_RefusesAndMarksError(t *testing.T) {
	mgr := state.NewMockManager()
	missingPath := filepath.Join(t.TempDir(), "kubeconfig-does-not-exist")
	lab := &types.Lab{
		ID:            uuid.New(),
		Name:          "verify-smoke",
		Company:       "acme",
		Level:         1,
		CloudProvider: types.CloudProviderLocal,
		Status:        types.LabStatusActive,
		CreatedAt:     time.Now(),
		ExpiresAt:     time.Now().Add(1 * time.Hour),
		TTLHours:      1,
		Metadata: types.LabMetadata{
			Clusters: []types.Cluster{{Name: "verify-smoke", KubeconfigPath: missingPath}},
		},
	}
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	c := newServeTestCLI(t, mgr)
	_, err := c.resolveServeLabInfo(context.Background(), "", "verify-smoke")
	if err == nil {
		t.Fatal("expected refusal for missing kubeconfig file")
	}
	if !strings.Contains(err.Error(), "kubeconfig file") || !strings.Contains(err.Error(), "missing") {
		t.Errorf("error should call out missing kubeconfig file; got: %v", err)
	}
	if !strings.Contains(err.Error(), "ERROR") {
		t.Errorf("error should mention lab record was marked ERROR; got: %v", err)
	}

	got, err := mgr.GetLab(context.Background(), lab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != types.LabStatusError {
		t.Errorf("expected lab marked ERROR, got %s", got.Status)
	}
}

// TestResolveServeLabInfo_HappyPath_KubeconfigExists verifies that the
// precondition gates do not interfere with the normal ACTIVE path when
// the kubeconfig file exists on disk.
func TestResolveServeLabInfo_HappyPath_KubeconfigExists(t *testing.T) {
	mgr := state.NewMockManager()
	tmp := t.TempDir()
	kubeconfigPath := filepath.Join(tmp, "kubeconfig")
	if err := os.WriteFile(kubeconfigPath, []byte("apiVersion: v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	lab := &types.Lab{
		ID:            uuid.New(),
		Name:          "verify-smoke",
		Company:       "acme",
		Level:         1,
		CloudProvider: types.CloudProviderLocal,
		Status:        types.LabStatusActive,
		CreatedAt:     time.Now(),
		ExpiresAt:     time.Now().Add(1 * time.Hour),
		TTLHours:      1,
		Metadata: types.LabMetadata{
			Clusters: []types.Cluster{{Name: "verify-smoke", KubeconfigPath: kubeconfigPath}},
		},
	}
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	c := newServeTestCLI(t, mgr)
	info, err := c.resolveServeLabInfo(context.Background(), "", "verify-smoke")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.kubeconfigPath != kubeconfigPath {
		t.Errorf("kubeconfigPath = %q, want %q", info.kubeconfigPath, kubeconfigPath)
	}
}

// TestStartLabReaper_DisabledByFlag asserts the --no-reaper flag prevents
// the reaper goroutine from being scheduled at all.
func TestStartLabReaper_DisabledByFlag(t *testing.T) {
	t.Parallel()
	mgr := state.NewMockManager()
	c := newServeTestCLI(t, mgr)
	tasks := asynctasks.New(logger.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c.startLabReaper(ctx, tasks, true /* noReaperFlag */)

	// No tasks registered → Wait returns immediately.
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer waitCancel()
	if !tasks.Wait(waitCtx) {
		t.Fatal("Wait should return immediately when no reaper task was scheduled")
	}
}

// TestStartLabReaper_DisabledByConfig asserts oasis.disable_lab_reaper
// behaves the same as --no-reaper.
func TestStartLabReaper_DisabledByConfig(t *testing.T) {
	t.Parallel()
	mgr := state.NewMockManager()
	c := newServeTestCLI(t, mgr)
	c.cfg.OASIS.DisableLabReaper = true
	tasks := asynctasks.New(logger.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c.startLabReaper(ctx, tasks, false)

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer waitCancel()
	if !tasks.Wait(waitCtx) {
		t.Fatal("Wait should return immediately when reaper is config-disabled")
	}
}

// TestStartLabReaper_RegistersWithAsyncTasksAndExitsOnCancel verifies the
// reaper goroutine is tracked by the asynctasks coordinator and exits on
// ctx cancellation. This is the graceful-shutdown contract.
func TestStartLabReaper_RegistersWithAsyncTasksAndExitsOnCancel(t *testing.T) {
	t.Parallel()
	mgr := state.NewMockManager()

	// Cipher etc. is exercised by buildOrchestrator; point at a fresh
	// master key file so the orchestrator constructor succeeds.
	masterKey := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(masterKey, []byte(strings.Repeat("x", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	c := newServeTestCLI(t, mgr)
	c.cfg.Credentials.MasterKeyPath = masterKey
	c.cfg.OASIS.LabReaperInterval = 20 * time.Millisecond

	tasks := asynctasks.New(logger.Nop())
	ctx, cancel := context.WithCancel(context.Background())

	c.startLabReaper(ctx, tasks, false)

	// Cancel; the goroutine must exit on ctx.Done.
	cancel()

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer waitCancel()
	if !tasks.Wait(waitCtx) {
		t.Fatal("reaper goroutine did not exit within 2s of context cancellation")
	}
}

// TestRunInfo_LazilyTransitionsExpired verifies that calling petri info on a
// lab whose TTL has elapsed transitions the underlying record to EXPIRED.
func TestRunInfo_LazilyTransitionsExpired(t *testing.T) {
	mgr := state.NewMockManager()
	lab := &types.Lab{
		ID:            uuid.New(),
		Name:          "lazy-expired",
		Company:       "acme",
		Level:         1,
		CloudProvider: types.CloudProviderLocal,
		Status:        types.LabStatusActive,
		CreatedAt:     time.Now().Add(-2 * time.Hour),
		ExpiresAt:     time.Now().Add(-1 * time.Hour),
		TTLHours:      1,
	}
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	c := newTestCLI(mgr, companiesYAML(t))
	out := string(runWithCapturedStdout(t, func() {
		if err := c.runInfo("lazy-expired"); err != nil {
			t.Fatalf("runInfo: %v", err)
		}
	}))
	if !strings.Contains(out, "Status:   EXPIRED") {
		t.Errorf("expected rendered status to be EXPIRED, got:\n%s", out)
	}

	got, err := mgr.GetLab(context.Background(), lab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != types.LabStatusExpired {
		t.Errorf("DB record should have been transitioned to EXPIRED; got %s", got.Status)
	}
}
