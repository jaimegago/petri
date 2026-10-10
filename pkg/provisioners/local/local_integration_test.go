//go:build integration

package local

import (
	"context"
	"os/exec"
	"testing"
)

// Creates a real kind cluster: run with `go test -tags integration`, and on
// a shared host only while holding whatever lab lock the host uses.

// ─── Integration tests (require kind + docker) ────────────────────────────────

func TestIntegration_CreateDeleteCluster(t *testing.T) {
	if _, err := exec.LookPath("kind"); err != nil {
		t.Skip("kind CLI not available")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker CLI not available")
	}

	// Quick Docker reachability check before spending time on cluster creation.
	if err := (&cliDocker{}).ping(context.Background()); err != nil {
		t.Skipf("Docker daemon not reachable: %v", err)
	}

	clusterName := "petri-test-integration"
	cfg := Config{KubeconfigDir: t.TempDir()}
	p := New(cfg)
	ctx := context.Background()

	// Ensure cleanup even on test failure.
	t.Cleanup(func() {
		_ = p.Delete(ctx, clusterName)
	})

	info, err := p.Create(ctx, CreateOptions{Name: clusterName, Level: 1})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.KubeconfigPath == "" {
		t.Error("KubeconfigPath should be set")
	}
	if info.NodeCount != 1 {
		t.Errorf("NodeCount = %d, want 1", info.NodeCount)
	}

	// Verify kubeconfig file exists.
	if _, err := exec.CommandContext(ctx,
		"kubectl", "--kubeconfig", info.KubeconfigPath,
		"get", "nodes", "--no-headers",
	).Output(); err != nil {
		t.Fatalf("kubectl get nodes: %v", err)
	}

	// Delete cluster.
	if err := p.Delete(ctx, clusterName); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
