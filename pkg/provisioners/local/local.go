// Package local provisions kind clusters on the developer machine.
package local

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Config holds settings for the local provisioner.
type Config struct {
	// KubeconfigDir is the directory where kubeconfigs are written.
	// Defaults to ~/.petri/kubeconfigs/ when empty.
	KubeconfigDir string
}

// CreateOptions configures a new local cluster.
type CreateOptions struct {
	// Name is the cluster name (also used as the kind cluster name).
	Name string
	// Level is the complexity level (1–3); controls default node count.
	Level int
	// NodeCount overrides the level default when > 0.
	NodeCount int
}

// ClusterInfo describes a successfully created cluster.
type ClusterInfo struct {
	Name           string `json:"name"`
	KubeconfigPath string `json:"kubeconfig_path"`
	NodeCount      int    `json:"node_count"`
}

// Provisioner manages local Kubernetes clusters via kind.
type Provisioner interface {
	// Create provisions a kind cluster and returns its connection details.
	Create(ctx context.Context, opts CreateOptions) (*ClusterInfo, error)
	// Delete removes a kind cluster and its kubeconfig.
	Delete(ctx context.Context, name string) error
}

// kindOps abstracts kind CLI calls so tests can inject a fake.
type kindOps interface {
	createCluster(ctx context.Context, name, configPath, kubeconfigPath string) error
	deleteCluster(ctx context.Context, name string) error
	listClusters(ctx context.Context) ([]string, error)
}

// dockerOps abstracts Docker daemon checks so tests can inject a fake.
type dockerOps interface {
	ping(ctx context.Context) error
}

type provisioner struct {
	cfg    Config
	kind   kindOps
	docker dockerOps
}

// New returns a Provisioner using the real kind CLI and Docker daemon.
func New(cfg Config) Provisioner {
	return &provisioner{
		cfg:    cfg,
		kind:   &cliKind{},
		docker: &cliDocker{},
	}
}

// newWithDeps injects kind and docker dependencies; used in tests.
func newWithDeps(cfg Config, k kindOps, d dockerOps) Provisioner {
	return &provisioner{cfg: cfg, kind: k, docker: d}
}

// Create provisions a kind cluster with the appropriate configuration for the
// requested level, then returns the cluster connection details.
func (p *provisioner) Create(ctx context.Context, opts CreateOptions) (*ClusterInfo, error) {
	// Verify Docker is reachable before attempting anything.
	if err := p.docker.ping(ctx); err != nil {
		return nil, fmt.Errorf("Docker daemon not reachable (is Docker running?): %w", err)
	}

	// Idempotency: refuse to create if a cluster with this name already exists.
	existing, err := p.kind.listClusters(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing kind clusters: %w", err)
	}
	for _, c := range existing {
		if c == opts.Name {
			return nil, fmt.Errorf("kind cluster %q already exists", opts.Name)
		}
	}

	nodeCount := resolveNodeCount(opts)

	kubeconfigPath, err := p.resolveKubeconfigPath(opts.Name)
	if err != nil {
		return nil, fmt.Errorf("resolving kubeconfig path: %w", err)
	}

	// Write kind cluster config to a temp file.
	configFile, err := os.CreateTemp("", "petri-kind-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("creating kind config file: %w", err)
	}
	defer os.Remove(configFile.Name()) //nolint:errcheck

	if _, err := configFile.WriteString(kindClusterConfig(nodeCount)); err != nil {
		return nil, fmt.Errorf("writing kind config: %w", err)
	}
	_ = configFile.Close()

	// Create the cluster; attempt cleanup on failure.
	if err := p.kind.createCluster(ctx, opts.Name, configFile.Name(), kubeconfigPath); err != nil {
		_ = p.kind.deleteCluster(ctx, opts.Name)
		_ = os.Remove(kubeconfigPath)
		return nil, fmt.Errorf("creating kind cluster %q: %w", opts.Name, err)
	}

	return &ClusterInfo{
		Name:           opts.Name,
		KubeconfigPath: kubeconfigPath,
		NodeCount:      nodeCount,
	}, nil
}

// Delete removes a kind cluster and its associated kubeconfig file.
func (p *provisioner) Delete(ctx context.Context, name string) error {
	if err := p.kind.deleteCluster(ctx, name); err != nil {
		return fmt.Errorf("deleting kind cluster %q: %w", name, err)
	}
	// Best-effort kubeconfig removal.
	if path, _ := p.resolveKubeconfigPath(name); path != "" {
		_ = os.Remove(path)
	}
	return nil
}

// resolveKubeconfigPath returns the full path for a cluster's kubeconfig file,
// creating the parent directory if needed.
func (p *provisioner) resolveKubeconfigPath(clusterName string) (string, error) {
	dir := p.cfg.KubeconfigDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving home dir: %w", err)
		}
		dir = filepath.Join(home, ".petri", "kubeconfigs")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating kubeconfig dir %s: %w", dir, err)
	}
	return filepath.Join(dir, clusterName+".kubeconfig"), nil
}

// resolveNodeCount returns the effective node count for the cluster.
// Level 1 → 1 node, Level 2 → 3 nodes, Level 3 → 4 nodes.
// A non-zero NodeCount in opts always takes precedence.
func resolveNodeCount(opts CreateOptions) int {
	if opts.NodeCount > 0 {
		return opts.NodeCount
	}
	switch opts.Level {
	case 2:
		return 3 // 1 control-plane + 2 workers
	case 3:
		return 4 // 1 control-plane + 3 workers
	default:
		return 1 // Level 1: single-node
	}
}

// ── shared exec helpers ────────────────────────────────────────────────────────

func runCmd(ctx context.Context, name string, args ...string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("%s %v: %w: %s", name, args, err, msg)
		}
		return fmt.Errorf("%s %v: %w", name, args, err)
	}
	return nil
}

func runOutput(ctx context.Context, name string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return "", fmt.Errorf("%s %v: %w: %s", name, args, err, msg)
		}
		return "", fmt.Errorf("%s %v: %w", name, args, err)
	}
	return strings.TrimSpace(stdout.String()), nil
}
