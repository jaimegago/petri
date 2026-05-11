package state

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/jaimegago/petri/pkg/types"
)

func TestTransitionIfExpired(t *testing.T) {
	tests := []struct {
		name       string
		status     types.LabStatus
		expiresAt  time.Time
		wantStatus types.LabStatus
	}{
		{
			name:       "ACTIVE past TTL transitions to EXPIRED",
			status:     types.LabStatusActive,
			expiresAt:  time.Now().Add(-1 * time.Hour),
			wantStatus: types.LabStatusExpired,
		},
		{
			name:       "ACTIVE within TTL is unchanged",
			status:     types.LabStatusActive,
			expiresAt:  time.Now().Add(1 * time.Hour),
			wantStatus: types.LabStatusActive,
		},
		{
			name:       "EXPIRED is unchanged",
			status:     types.LabStatusExpired,
			expiresAt:  time.Now().Add(-1 * time.Hour),
			wantStatus: types.LabStatusExpired,
		},
		{
			name:       "DESTROYED is unchanged even when past TTL",
			status:     types.LabStatusDestroyed,
			expiresAt:  time.Now().Add(-1 * time.Hour),
			wantStatus: types.LabStatusDestroyed,
		},
		{
			name:       "ERROR is unchanged even when past TTL",
			status:     types.LabStatusError,
			expiresAt:  time.Now().Add(-1 * time.Hour),
			wantStatus: types.LabStatusError,
		},
		{
			name:       "CREATING is unchanged",
			status:     types.LabStatusCreating,
			expiresAt:  time.Now().Add(-1 * time.Hour),
			wantStatus: types.LabStatusCreating,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mgr := NewMockManager()
			lab := &types.Lab{
				ID:        uuid.New(),
				Name:      "t-lab",
				Company:   "acme",
				Level:     1,
				Status:    tc.status,
				CreatedAt: time.Now().Add(-2 * time.Hour),
				ExpiresAt: tc.expiresAt,
				TTLHours:  1,
			}
			if err := mgr.CreateLab(context.Background(), lab); err != nil {
				t.Fatalf("seed: %v", err)
			}

			updated, err := TransitionIfExpired(context.Background(), mgr, lab)
			if err != nil {
				t.Fatalf("TransitionIfExpired: %v", err)
			}
			if updated.Status != tc.wantStatus {
				t.Errorf("returned lab status = %s, want %s", updated.Status, tc.wantStatus)
			}

			// Also verify the persisted record matches.
			persisted, err := mgr.GetLab(context.Background(), lab.ID)
			if err != nil {
				t.Fatalf("GetLab: %v", err)
			}
			if persisted.Status != tc.wantStatus {
				t.Errorf("persisted lab status = %s, want %s", persisted.Status, tc.wantStatus)
			}
		})
	}
}

func TestTransitionIfExpired_NilLab(t *testing.T) {
	mgr := NewMockManager()
	got, err := TransitionIfExpired(context.Background(), mgr, nil)
	if err != nil {
		t.Fatalf("unexpected error on nil: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil result, got %+v", got)
	}
}
