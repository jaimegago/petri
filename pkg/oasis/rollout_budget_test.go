package oasis

import (
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// TestProviderBudgets_FallBackOnZeroOrNegative pins the provider half of the
// budget configuration: a configured value is used as-is, and zero or
// negative falls back to the package default rather than meaning "no budget".
func TestProviderBudgets_FallBackOnZeroOrNegative(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         ProviderConfig
		wantPull    time.Duration
		wantRollout time.Duration
	}{
		{
			name:        "unset uses defaults",
			cfg:         ProviderConfig{},
			wantPull:    defaultImagePullTimeout,
			wantRollout: defaultRolloutTimeout,
		},
		{
			name:        "configured values are honored",
			cfg:         ProviderConfig{ImagePullTimeout: 10 * time.Minute, RolloutTimeout: 2 * time.Minute},
			wantPull:    10 * time.Minute,
			wantRollout: 2 * time.Minute,
		},
		{
			name:        "negative falls back to defaults",
			cfg:         ProviderConfig{ImagePullTimeout: -time.Second, RolloutTimeout: -time.Second},
			wantPull:    defaultImagePullTimeout,
			wantRollout: defaultRolloutTimeout,
		},
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := New(tt.cfg, newMockKube(), log).(*petriProvider)
			if got := p.pullBudget(); got != tt.wantPull {
				t.Errorf("pullBudget() = %s, want %s", got, tt.wantPull)
			}
			if got := p.rolloutBudget(); got != tt.wantRollout {
				t.Errorf("rolloutBudget() = %s, want %s", got, tt.wantRollout)
			}
		})
	}
}

// TestDefaultRolloutTimeout_Unchanged pins the settled default. The value
// reaches log parsers through ErrRolloutTimeout's historical phrasing, so a
// change here is a change to what they see.
func TestDefaultRolloutTimeout_Unchanged(t *testing.T) {
	t.Parallel()

	if defaultRolloutTimeout != 60*time.Second {
		t.Fatalf("defaultRolloutTimeout = %s, want 60s", defaultRolloutTimeout)
	}
}

// TestErrRolloutTimeout_InterpolatesConfiguredBudget confirms a configured
// rollout budget keeps the historical phrasing and carries the configured
// value, not the 60s default.
func TestErrRolloutTimeout_InterpolatesConfiguredBudget(t *testing.T) {
	t.Parallel()

	e := &ErrRolloutTimeout{Timeout: 2 * time.Minute, Deployments: []string{"frontend/web-app"}}
	if got, want := e.Error(), "deployments did not become ready within 2m0s: frontend/web-app"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if strings.Contains(e.Error(), "1m0s") {
		t.Errorf("Error() = %q, leaked the default budget", e.Error())
	}
}
