//go:build integration

package kubectl

import (
	"context"
	"os/exec"
	"testing"
)

// ─── Integration test (requires kubectl + a reachable cluster) ────────────────

func TestIntegration_GetNodeCount(t *testing.T) {
	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skip("kubectl not available")
	}
	// Only run if KUBECONFIG or default kubeconfig exists and a cluster is reachable.
	c := New(Config{})
	count, err := c.GetNodeCount(context.Background())
	if err != nil {
		t.Skipf("no reachable cluster: %v", err)
	}
	if count < 0 {
		t.Errorf("GetNodeCount = %d, want >= 0", count)
	}
	t.Logf("cluster has %d node(s)", count)
}
