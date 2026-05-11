package types

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLabValidate(t *testing.T) {
	tests := []struct {
		name    string
		lab     Lab
		wantErr bool
	}{
		{
			name:    "valid lab",
			lab:     Lab{ID: uuid.New(), Name: "test", Company: "acme", Level: 1},
			wantErr: false,
		},
		{
			name:    "missing name",
			lab:     Lab{Company: "acme", Level: 1},
			wantErr: true,
		},
		{
			name:    "missing company",
			lab:     Lab{Name: "test", Level: 1},
			wantErr: true,
		},
		{
			name:    "level zero",
			lab:     Lab{Name: "test", Company: "acme", Level: 0},
			wantErr: true,
		},
		{
			name:    "level too high",
			lab:     Lab{Name: "test", Company: "acme", Level: 4},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.lab.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestLabIsExpired(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{"past expiry", time.Now().Add(-1 * time.Hour), true},
		{"future expiry", time.Now().Add(1 * time.Hour), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lab := &Lab{ExpiresAt: tc.expiresAt}
			if got := lab.IsExpired(); got != tc.want {
				t.Errorf("IsExpired() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLabCanTransitionTo(t *testing.T) {
	tests := []struct {
		from LabStatus
		to   LabStatus
		want bool
	}{
		{LabStatusCreating, LabStatusActive, true},
		{LabStatusCreating, LabStatusError, true},
		{LabStatusCreating, LabStatusDestroying, true},
		{LabStatusCreating, LabStatusDestroyed, false},
		{LabStatusActive, LabStatusExpired, true},
		{LabStatusActive, LabStatusDestroying, true},
		{LabStatusActive, LabStatusCreating, false},
		{LabStatusExpired, LabStatusDestroying, true},
		{LabStatusExpired, LabStatusActive, false},
		{LabStatusDestroyed, LabStatusActive, false},
		{LabStatusError, LabStatusDestroying, true},
		{LabStatusError, LabStatusActive, false},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%s->%s", tc.from, tc.to), func(t *testing.T) {
			lab := &Lab{Status: tc.from}
			if got := lab.CanTransitionTo(tc.to); got != tc.want {
				t.Errorf("CanTransitionTo() = %v, want %v", got, tc.want)
			}
		})
	}
}
