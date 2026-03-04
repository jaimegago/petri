// Package kubectl wraps the kubectl CLI for Kubernetes operations.
package kubectl

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Config holds settings for the kubectl client.
type Config struct {
	// KubeconfigPath is the path to the kubeconfig file to use.
	KubeconfigPath string
	// Binary overrides the kubectl binary name. Defaults to "kubectl".
	Binary string
}

// NodeInfo describes a Kubernetes node.
type NodeInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Client provides kubectl-based Kubernetes operations.
type Client interface {
	// ApplyManifest applies a Kubernetes manifest provided as a YAML string.
	ApplyManifest(ctx context.Context, manifest string) error
	// WaitForNodes waits until all nodes in the cluster report Ready.
	WaitForNodes(ctx context.Context, timeout time.Duration) error
	// WaitForRollout waits for a Deployment to fully roll out.
	WaitForRollout(ctx context.Context, namespace, deployment string, timeout time.Duration) error
	// GetNodeCount returns the current number of nodes in the cluster.
	GetNodeCount(ctx context.Context) (int, error)
}

// runner abstracts kubectl command execution so tests can inject a fake.
type runner interface {
	run(ctx context.Context, args []string) error
	output(ctx context.Context, args []string) (string, error)
}

type cliClient struct {
	runner runner
}

// New returns a Client that shells out to the real kubectl CLI.
func New(cfg Config) Client {
	bin := cfg.Binary
	if bin == "" {
		bin = "kubectl"
	}
	return &cliClient{
		runner: &cliRunner{binary: bin, kubeconfig: cfg.KubeconfigPath},
	}
}

// newWithRunner injects a runner dependency; used in tests.
func newWithRunner(r runner) Client {
	return &cliClient{runner: r}
}

// ApplyManifest writes the manifest to a temp file and runs `kubectl apply -f`.
func (c *cliClient) ApplyManifest(ctx context.Context, manifest string) error {
	f, err := os.CreateTemp("", "petri-manifest-*.yaml")
	if err != nil {
		return fmt.Errorf("creating manifest temp file: %w", err)
	}
	defer os.Remove(f.Name()) //nolint:errcheck

	if _, err := f.WriteString(manifest); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}
	_ = f.Close()

	if err := c.runner.run(ctx, []string{"apply", "-f", f.Name()}); err != nil {
		return fmt.Errorf("kubectl apply: %w", err)
	}
	return nil
}

// WaitForNodes waits until all cluster nodes satisfy the Ready condition.
func (c *cliClient) WaitForNodes(ctx context.Context, timeout time.Duration) error {
	args := []string{
		"wait", "--for=condition=Ready",
		"nodes", "--all",
		"--timeout", formatDuration(timeout),
	}
	if err := c.runner.run(ctx, args); err != nil {
		return fmt.Errorf("waiting for nodes ready: %w", err)
	}
	return nil
}

// WaitForRollout waits for a Deployment to complete its rollout.
func (c *cliClient) WaitForRollout(ctx context.Context, namespace, deployment string, timeout time.Duration) error {
	args := []string{
		"rollout", "status",
		"deployment/" + deployment,
		"-n", namespace,
		"--timeout", formatDuration(timeout),
	}
	if err := c.runner.run(ctx, args); err != nil {
		return fmt.Errorf("rollout status %s/%s: %w", namespace, deployment, err)
	}
	return nil
}

// GetNodeCount returns the number of nodes currently registered in the cluster.
func (c *cliClient) GetNodeCount(ctx context.Context) (int, error) {
	out, err := c.runner.output(ctx, []string{"get", "nodes", "--no-headers"})
	if err != nil {
		return 0, fmt.Errorf("getting nodes: %w", err)
	}
	if strings.TrimSpace(out) == "" {
		return 0, nil
	}
	return len(strings.Split(strings.TrimSpace(out), "\n")), nil
}

// ── cliRunner ──────────────────────────────────────────────────────────────────

// cliRunner implements runner by executing the real kubectl binary.
type cliRunner struct {
	binary     string
	kubeconfig string
}

// baseArgs returns the --kubeconfig flag when a path is configured.
func (r *cliRunner) baseArgs() []string {
	if r.kubeconfig != "" {
		return []string{"--kubeconfig", r.kubeconfig}
	}
	return nil
}

func (r *cliRunner) run(ctx context.Context, args []string) error {
	_, err := r.output(ctx, args)
	return err
}

func (r *cliRunner) output(ctx context.Context, args []string) (string, error) {
	full := append(r.baseArgs(), args...) //nolint:gocritic // new slice each call
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, r.binary, full...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("%s %v: %w: %s", r.binary, full, err, msg)
		}
		return "", fmt.Errorf("%s %v: %w", r.binary, full, err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// ── helpers ────────────────────────────────────────────────────────────────────

// formatDuration converts a time.Duration to a kubectl-compatible string
// (e.g. "5m" or "300s").
func formatDuration(d time.Duration) string {
	total := int(d.Seconds())
	if total >= 60 && total%60 == 0 {
		return strconv.Itoa(total/60) + "m"
	}
	return strconv.Itoa(total) + "s"
}
