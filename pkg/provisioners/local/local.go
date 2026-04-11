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
	"time"
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
	// OASISMode enables audit logging on the API server for OASIS evaluation.
	OASISMode bool
}

// ClusterInfo describes a successfully created cluster.
type ClusterInfo struct {
	Name           string `json:"name"`
	KubeconfigPath string `json:"kubeconfig_path"`
	NodeCount      int    `json:"node_count"`
	AuditLogPath   string `json:"audit_log_path,omitempty"`
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
		return nil, fmt.Errorf("docker daemon not reachable (is Docker running?): %w", err)
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

	// Determine audit paths for OASIS mode.
	//
	// Each lab gets its own subdirectory under audit/ so that concurrent labs
	// (and labs that share the same audit/ root over time) cannot read each
	// other's audit events. The kube-apiserver inside the kind node always
	// writes to /var/log/kubernetes/audit.log; per-lab isolation comes from
	// the host-side bind-mount being a unique per-lab directory.
	//
	// Layout:
	//   ~/.petri/kubeconfigs/audit/<lab>/
	//     audit-policy.yaml   (read-only mount, projected to /etc/kubernetes/audit/audit-policy.yaml)
	//     audit.log           (read-write, projected to /var/log/kubernetes/audit.log)
	//
	// The directory MUST exist on the host before kind create cluster runs,
	// otherwise kind silently mounts an empty tmpfs over the missing path and
	// the apiserver's audit writes vanish.
	var auditLogPath string
	var auditPolicyPath string
	var auditDir string
	if opts.OASISMode {
		auditRoot := filepath.Join(filepath.Dir(kubeconfigPath), "audit")
		auditDir = filepath.Join(auditRoot, opts.Name)
		if err := os.MkdirAll(auditDir, 0o700); err != nil {
			return nil, fmt.Errorf("creating per-lab audit dir %s: %w", auditDir, err)
		}

		auditLogPath = filepath.Join(auditDir, "audit.log")
		auditPolicyPath = filepath.Join(auditDir, "audit-policy.yaml")

		if err := os.WriteFile(auditPolicyPath, []byte(oasisAuditPolicy()), 0o600); err != nil {
			return nil, fmt.Errorf("writing audit policy: %w", err)
		}
	}

	// Write kind cluster config to a temp file.
	configFile, err := os.CreateTemp("", "petri-kind-*.yaml")
	if err != nil {
		return nil, fmt.Errorf("creating kind config file: %w", err)
	}
	defer os.Remove(configFile.Name()) //nolint:errcheck

	kindCfg := kindClusterConfig(nodeCount)
	if opts.OASISMode {
		// Pass the per-lab audit DIRECTORY (not the file path) to the kind config
		// so that the bind-mount targets the per-lab subdirectory and the
		// apiserver's audit.log lands inside it.
		kindCfg = kindClusterConfigWithAudit(nodeCount, auditPolicyPath, auditDir)
	}
	if _, err := configFile.WriteString(kindCfg); err != nil {
		return nil, fmt.Errorf("writing kind config: %w", err)
	}
	_ = configFile.Close()

	// Create the cluster; attempt cleanup on failure.
	if err := p.kind.createCluster(ctx, opts.Name, configFile.Name(), kubeconfigPath); err != nil {
		_ = p.kind.deleteCluster(ctx, opts.Name)
		_ = os.Remove(kubeconfigPath)
		return nil, fmt.Errorf("creating kind cluster %q: %w", opts.Name, err)
	}

	// In OASIS mode the kind default CNI (kindnet) is disabled and Calico
	// is installed as the sole CNI plugin to provide NetworkPolicy
	// enforcement. Apply the Calico manifest and wait for readiness.
	if opts.OASISMode {
		if _, statErr := os.Stat(kubeconfigPath); statErr == nil {
			if err := runCmd(ctx, "kubectl", "--kubeconfig", kubeconfigPath,
				"apply", "-f", CalicoCNIManifestURL); err != nil {
				p.cleanupFailedCluster(ctx, opts.Name, kubeconfigPath)
				return nil, fmt.Errorf("installing Calico CNI: %w", err)
			}
			if err := waitForCalico(ctx, kubeconfigPath); err != nil {
				p.cleanupFailedCluster(ctx, opts.Name, kubeconfigPath)
				return nil, fmt.Errorf("waiting for Calico CNI readiness: %w", err)
			}
		}
	}

	return &ClusterInfo{
		Name:           opts.Name,
		KubeconfigPath: kubeconfigPath,
		NodeCount:      nodeCount,
		AuditLogPath:   auditLogPath,
	}, nil
}

// cleanupFailedCluster deletes the kind cluster and kubeconfig after a
// post-creation step (e.g. Calico install) fails. Errors are logged but
// not returned so the original error is preserved for the caller.
func (p *provisioner) cleanupFailedCluster(ctx context.Context, name, kubeconfigPath string) {
	if err := p.kind.deleteCluster(ctx, name); err != nil {
		fmt.Fprintf(os.Stderr, "petri: cleanup: failed to delete kind cluster %q: %v\n", name, err)
	}
	_ = os.Remove(kubeconfigPath)
}

// Delete removes a kind cluster and its associated kubeconfig file.
func (p *provisioner) Delete(ctx context.Context, name string) error {
	if err := p.kind.deleteCluster(ctx, name); err != nil {
		return fmt.Errorf("deleting kind cluster %q: %w", name, err)
	}
	// Best-effort kubeconfig removal.
	if path, _ := p.resolveKubeconfigPath(name); path != "" {
		_ = os.Remove(path)
		// Best-effort per-lab audit directory removal. Safe even if the lab
		// was not created with --oasis (the directory simply will not exist).
		auditDir := filepath.Join(filepath.Dir(path), "audit", name)
		_ = os.RemoveAll(auditDir)
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

// waitForCalico blocks until the Calico CNI is fully operational. It waits for
// the calico-node daemonset and calico-kube-controllers deployment to roll out,
// then runs a smoke-test pod to verify that the CNI can actually configure pod
// networking (the rollout check alone doesn't guarantee /var/lib/calico/ is
// mounted and functional on every node).
func waitForCalico(ctx context.Context, kubeconfigPath string) error {
	kube := func(args ...string) error {
		return runCmd(ctx, "kubectl", append([]string{"--kubeconfig", kubeconfigPath}, args...)...)
	}

	// Wait for calico-node daemonset pods to be Running+Ready on all nodes.
	if err := kube("rollout", "status", "daemonset/calico-node",
		"-n", "kube-system", "--timeout", "3m"); err != nil {
		return fmt.Errorf("calico-node daemonset not ready: %w", err)
	}

	// Wait for calico-kube-controllers deployment.
	if err := kube("rollout", "status", "deployment/calico-kube-controllers",
		"-n", "kube-system", "--timeout", "2m"); err != nil {
		return fmt.Errorf("calico-kube-controllers not ready: %w", err)
	}

	// Brief stabilization delay: Calico's networking plumbing (IPAM, Felix
	// route programming) can take a few seconds to fully converge after the
	// daemonset reports Ready. This pause lets Calico finish setting up CNI
	// configuration before the smoke test.
	time.Sleep(5 * time.Second)

	// Smoke-test: create a throwaway pod and verify it reaches Running.
	// This confirms the CNI plugin can actually set up pod networking.
	const smokeNS = "kube-system"
	const smokePod = "petri-cni-smoke"

	_ = kube("delete", "pod", smokePod, "-n", smokeNS, "--ignore-not-found")

	if err := kube("run", smokePod, "-n", smokeNS,
		"--image", "registry.k8s.io/pause:3.10", "--restart=Never"); err != nil {
		return fmt.Errorf("creating CNI smoke-test pod: %w", err)
	}
	defer func() {
		_ = kube("delete", "pod", smokePod, "-n", smokeNS, "--ignore-not-found")
	}()

	if err := kube("wait", "--for=condition=Ready",
		"pod/"+smokePod, "-n", smokeNS, "--timeout", "120s"); err != nil {
		return fmt.Errorf("CNI smoke-test pod did not reach Ready: %w", err)
	}

	return nil
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
