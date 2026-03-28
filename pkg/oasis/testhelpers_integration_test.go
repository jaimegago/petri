//go:build integration

package oasis

import (
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jaimegago/petri/pkg/chaos"
)

// testEnv holds shared state for integration tests.
type testEnv struct {
	server   *httptest.Server
	provider OASISProvider
	kube     KubeClient
	baseURL  string
}

// setupTestEnv creates a test environment with a real kind cluster.
// It skips the test if kind or kubectl are not available, or if no cluster is reachable.
func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	kubeconfigPath := detectKubeconfig(t)
	checkClusterReachable(t, kubeconfigPath)

	kube := chaos.NewKubeClient(kubeconfigPath)
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))

	provider := New(ProviderConfig{KubeconfigPath: kubeconfigPath}, kube, log)
	srv := NewServer(provider, log)
	ts := httptest.NewServer(srv.Handler())

	t.Cleanup(func() {
		ts.Close()
	})

	return &testEnv{
		server:   ts,
		provider: provider,
		kube:     kube,
		baseURL:  ts.URL,
	}
}

// detectKubeconfig finds a kubeconfig path. It checks KUBECONFIG env, then
// the kind default, then ~/.kube/config. Skips the test if none found.
func detectKubeconfig(t *testing.T) string {
	t.Helper()

	if v := os.Getenv("KUBECONFIG"); v != "" {
		if _, err := os.Stat(v); err == nil {
			return v
		}
	}

	// kind default location.
	home, _ := os.UserHomeDir()
	kindDefault := filepath.Join(home, ".kube", "config")
	if _, err := os.Stat(kindDefault); err == nil {
		return kindDefault
	}

	t.Skip("no kubeconfig found; skipping integration test")
	return ""
}

// checkClusterReachable verifies that kubectl can reach the cluster.
func checkClusterReachable(t *testing.T, kubeconfigPath string) {
	t.Helper()

	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skip("kubectl not found; skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfigPath, "cluster-info")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Skipf("cluster not reachable: %v; skipping integration test", err)
	}
}

// cleanupNamespace registers a t.Cleanup that deletes the given namespace.
func cleanupNamespace(t *testing.T, kube KubeClient, namespace string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = kube.DeleteNamespace(ctx, namespace)
	})
}

// provisionAndCleanup provisions an environment and registers cleanup for the namespace.
// Returns the ProvisionResponse and the environment namespace.
func provisionAndCleanup(t *testing.T, env *testEnv, req ProvisionRequest) ProvisionResponse {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := env.provider.Provision(ctx, req)
	if err != nil {
		t.Fatalf("Provision() error: %v", err)
	}
	if resp.Status != "ready" {
		t.Fatalf("Provision() status = %q, want %q", resp.Status, "ready")
	}

	// Get the namespace for cleanup.
	p := env.provider.(*petriProvider)
	e, err := p.store.get(resp.EnvironmentID)
	if err != nil {
		t.Fatalf("environment not found after provision: %v", err)
	}
	cleanupNamespace(t, env.kube, e.Namespace)

	return resp
}

// waitForResource polls until a resource exists or timeout.
func waitForResource(t *testing.T, kube KubeClient, kind, namespace, name string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		raw, err := kube.GetResource(ctx, kind, namespace, name)
		cancel()
		if err == nil && raw != "" {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("timed out waiting for %s/%s/%s", kind, namespace, name)
}

// getNamespace returns the namespace for a given environment ID.
func getNamespace(t *testing.T, env *testEnv, envID string) string {
	t.Helper()
	p := env.provider.(*petriProvider)
	e, err := p.store.get(envID)
	if err != nil {
		t.Fatalf("environment %q not found: %v", envID, err)
	}
	return e.Namespace
}

// namespaceExists checks if a namespace exists.
func namespaceExists(kube KubeClient, namespace string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := kube.GetResource(ctx, "namespace", "", namespace)
	return err == nil && raw != ""
}

// uniqueScenarioID returns a unique scenario ID for test isolation.
func uniqueScenarioID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()%100000)
}
