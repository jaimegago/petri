package state_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/jaimegago/petri/pkg/state"
	"github.com/jaimegago/petri/pkg/types"
)

// TestSQLiteConcurrentManagers models several petri processes (e.g. two
// `petri create` runs, or `petri create` alongside `petri serve`'s reaper)
// sharing one state DB file. Each manager owns its own connection, so they
// contend through SQLite's file locks exactly as separate processes do. Every
// open and every write must succeed; none may surface SQLITE_BUSY.
//
// The two cases separate the contention points: opening (schema migration
// against a fresh file) and steady-state writes against an already-migrated
// file.
func TestSQLiteConcurrentManagers(t *testing.T) {
	const (
		managers = 8
		labsEach = 10
	)
	tests := []struct {
		name        string
		openUpFront bool
	}{
		{"concurrent_open_and_write", false},
		{"concurrent_write", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "petri.db")

			opened := make([]state.Manager, managers)
			if tt.openUpFront {
				for i := range opened {
					m, err := state.NewSQLiteManager(ctx, path)
					if err != nil {
						t.Fatalf("opening manager %d: %v", i, err)
					}
					t.Cleanup(func() { _ = m.Close() })
					opened[i] = m
				}
			}

			start := make(chan struct{})
			errs := make(chan error, managers)
			var wg sync.WaitGroup
			for i := range managers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					errs <- runWriter(ctx, opened[i], path, i, labsEach)
				}()
			}
			close(start)
			wg.Wait()
			close(errs)
			for err := range errs {
				if err != nil {
					t.Error(err)
				}
			}
			if t.Failed() {
				return
			}

			m, err := state.NewSQLiteManager(ctx, path)
			if err != nil {
				t.Fatalf("reopening: %v", err)
			}
			t.Cleanup(func() { _ = m.Close() })
			labs, err := m.ListLabs(ctx, state.ListFilter{})
			if err != nil {
				t.Fatalf("ListLabs: %v", err)
			}
			if got, want := len(labs), managers*labsEach; got != want {
				t.Errorf("labs = %d, want %d", got, want)
			}

			// WAL is persistent in the file header, so an independent handle sees it.
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatalf("opening raw handle: %v", err)
			}
			t.Cleanup(func() { _ = raw.Close() })
			var mode string
			if err := raw.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
				t.Fatalf("reading journal_mode: %v", err)
			}
			if mode != "wal" {
				t.Errorf("journal_mode = %q, want wal", mode)
			}
		})
	}
}

// runWriter performs a create, resource insert and update per lab — the
// write shape of `petri create`. A nil m is opened on path here, so the open
// itself races the other writers.
func runWriter(ctx context.Context, m state.Manager, path string, id, n int) error {
	if m == nil {
		var err error
		m, err = state.NewSQLiteManager(ctx, path)
		if err != nil {
			return fmt.Errorf("writer %d: opening: %w", id, err)
		}
		defer func() { _ = m.Close() }()
	}

	for j := range n {
		lab := testLab(fmt.Sprintf("lab-%d-%d", id, j))
		if err := m.CreateLab(ctx, lab); err != nil {
			return fmt.Errorf("writer %d: CreateLab: %w", id, err)
		}
		res := &types.LabResource{
			ID:           uuid.New(),
			LabID:        lab.ID,
			ResourceType: "kind_cluster",
			ResourceID:   lab.Name,
		}
		if err := m.CreateResource(ctx, res); err != nil {
			return fmt.Errorf("writer %d: CreateResource: %w", id, err)
		}
		lab.Status = types.LabStatusActive
		if err := m.UpdateLab(ctx, lab); err != nil {
			return fmt.Errorf("writer %d: UpdateLab: %w", id, err)
		}
	}
	return nil
}
