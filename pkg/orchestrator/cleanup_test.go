package orchestrator

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaimegago/petri/pkg/logger"
	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
)

// TestStartCleanupLoop_FiresOnInterval verifies the background reaper
// goroutine destroys expired labs on its tick cadence.
func TestStartCleanupLoop_FiresOnInterval(t *testing.T) {
	t.Parallel()

	mgr := state.NewMockManager()
	lab := &types.Lab{
		ID:            uuid.New(),
		Name:          "reaper-lab",
		Company:       "acme",
		Level:         1,
		CloudProvider: types.CloudProviderLocal,
		Status:        types.LabStatusActive,
		CreatedAt:     time.Now().Add(-2 * time.Hour),
		ExpiresAt:     time.Now().Add(-1 * time.Hour),
		TTLHours:      1,
		Metadata:      types.LabMetadata{Clusters: []types.Cluster{{Name: "reaper-lab"}}},
	}
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	localProv := &mockLocalProv{}
	orch := New(Config{WorkDir: t.TempDir()}, Deps{
		State:     mgr,
		Cipher:    &mockCipher{},
		Log:       logger.Nop(),
		LocalProv: localProv,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		orch.StartCleanupLoop(ctx, 50*time.Millisecond, 0)
		close(done)
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, err := mgr.GetLab(context.Background(), lab.ID)
		if err == nil && got.Status == types.LabStatusDestroyed {
			cancel()
			<-done
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	<-done
	got, _ := mgr.GetLab(context.Background(), lab.ID)
	t.Fatalf("expired lab was not reaped within deadline; status=%s", got.Status)
}

// TestStartCleanupLoop_ExitsOnContextCancel verifies the reaper goroutine
// honours context cancellation — this is what petri serve relies on for
// graceful shutdown via the asyncTasks coordinator.
func TestStartCleanupLoop_ExitsOnContextCancel(t *testing.T) {
	t.Parallel()
	mgr := state.NewMockManager()
	orch := New(Config{WorkDir: t.TempDir()}, Deps{
		State:  mgr,
		Cipher: &mockCipher{},
		Log:    logger.Nop(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		orch.StartCleanupLoop(ctx, 10*time.Millisecond, 0)
		close(done)
	}()

	// Let it tick at least once, then cancel.
	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reaper did not exit within 2s of ctx cancel")
	}
}

// TestRunCleanup_StrandedCreating verifies the reaper picks up labs stuck
// in CREATING past the stranded threshold and tears them down.
func TestRunCleanup_StrandedCreating(t *testing.T) {
	t.Parallel()
	mgr := state.NewMockManager()
	lab := &types.Lab{
		ID:            uuid.New(),
		Name:          "stranded-create",
		Company:       "acme",
		Level:         1,
		CloudProvider: types.CloudProviderLocal,
		Status:        types.LabStatusCreating,
		CreatedAt:     time.Now().Add(-StrandedCreatingTimeout - time.Minute),
		ExpiresAt:     time.Now().Add(1 * time.Hour), // TTL not yet elapsed
		TTLHours:      2,
		Metadata:      types.LabMetadata{Clusters: []types.Cluster{{Name: "stranded-create"}}},
	}
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	localProv := &mockLocalProv{}
	orch := New(Config{WorkDir: t.TempDir()}, Deps{
		State:     mgr,
		Cipher:    &mockCipher{},
		Log:       logger.Nop(),
		LocalProv: localProv,
	})

	orch.runCleanup(context.Background(), 0)

	got, err := mgr.GetLab(context.Background(), lab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != types.LabStatusDestroyed {
		t.Errorf("expected stranded CREATING lab to be DESTROYED, got %s", got.Status)
	}
	if len(localProv.deleted) == 0 {
		t.Error("expected mockLocalProv.Delete to have been called for stranded lab")
	}
}

// TestRunCleanup_CreatingWithinThreshold_NotReaped is the defensive guard:
// a CREATING lab younger than the stranded threshold must not be torn down.
func TestRunCleanup_CreatingWithinThreshold_NotReaped(t *testing.T) {
	t.Parallel()
	mgr := state.NewMockManager()
	lab := &types.Lab{
		ID:            uuid.New(),
		Name:          "young-create",
		Company:       "acme",
		Level:         1,
		CloudProvider: types.CloudProviderLocal,
		Status:        types.LabStatusCreating,
		CreatedAt:     time.Now().Add(-1 * time.Minute), // fresh
		ExpiresAt:     time.Now().Add(-1 * time.Hour),   // synthetically past TTL
		TTLHours:      0,
	}
	if err := mgr.CreateLab(context.Background(), lab); err != nil {
		t.Fatal(err)
	}

	var deleteCalled int32
	localProv := &mockLocalProv{
		deleteFn: func(_ context.Context, _ string) error {
			atomic.AddInt32(&deleteCalled, 1)
			return nil
		},
	}
	orch := New(Config{WorkDir: t.TempDir()}, Deps{
		State:     mgr,
		Cipher:    &mockCipher{},
		Log:       logger.Nop(),
		LocalProv: localProv,
	})

	orch.runCleanup(context.Background(), 0)

	got, err := mgr.GetLab(context.Background(), lab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != types.LabStatusCreating {
		t.Errorf("young CREATING lab should still be CREATING, got %s", got.Status)
	}
	if atomic.LoadInt32(&deleteCalled) != 0 {
		t.Errorf("LocalProv.Delete should not have been called for young CREATING lab")
	}
}
