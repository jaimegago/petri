package local

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// ─── mock helpers ─────────────────────────────────────────────────────────────

type mockKind struct {
	createFn        func(ctx context.Context, name, configPath, kubeconfigPath string) error
	deleteFn        func(ctx context.Context, name string) error
	listFn          func(ctx context.Context) ([]string, error)
	deleteCallCount int
}

func (m *mockKind) createCluster(ctx context.Context, name, configPath, kubeconfigPath string) error {
	if m.createFn != nil {
		return m.createFn(ctx, name, configPath, kubeconfigPath)
	}
	return nil
}

func (m *mockKind) deleteCluster(ctx context.Context, name string) error {
	m.deleteCallCount++
	if m.deleteFn != nil {
		return m.deleteFn(ctx, name)
	}
	return nil
}

func (m *mockKind) listClusters(ctx context.Context) ([]string, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return nil, nil
}

type mockDocker struct {
	pingFn func(ctx context.Context) error
}

func (m *mockDocker) ping(ctx context.Context) error {
	if m.pingFn != nil {
		return m.pingFn(ctx)
	}
	return nil
}

func noop() *mockKind       { return &mockKind{} }
func okDocker() *mockDocker { return &mockDocker{} }

// ─── Provisioner.Create tests ─────────────────────────────────────────────────

func TestProvisionerCreate(t *testing.T) {
	tests := []struct {
		name            string
		opts            CreateOptions
		kind            *mockKind
		docker          *mockDocker
		wantErr         bool
		wantDeleteCalls int
	}{
		{
			name:   "success level 1 single node",
			opts:   CreateOptions{Name: "test-lab", Level: 1},
			kind:   noop(),
			docker: okDocker(),
		},
		{
			name:   "success level 2 three nodes",
			opts:   CreateOptions{Name: "test-lab", Level: 2},
			kind:   noop(),
			docker: okDocker(),
		},
		{
			name:   "success override node count",
			opts:   CreateOptions{Name: "test-lab", Level: 1, NodeCount: 5},
			kind:   noop(),
			docker: okDocker(),
		},
		{
			name: "docker not running",
			opts: CreateOptions{Name: "test-lab", Level: 1},
			kind: noop(),
			docker: &mockDocker{
				pingFn: func(_ context.Context) error {
					return errors.New("cannot connect to Docker daemon")
				},
			},
			wantErr: true,
		},
		{
			name: "cluster already exists",
			opts: CreateOptions{Name: "existing", Level: 1},
			kind: &mockKind{
				listFn: func(_ context.Context) ([]string, error) {
					return []string{"existing"}, nil
				},
			},
			docker:  okDocker(),
			wantErr: true,
		},
		{
			name: "kind create fails - cleanup attempted",
			opts: CreateOptions{Name: "fail-cluster", Level: 1},
			kind: &mockKind{
				createFn: func(_ context.Context, _, _, _ string) error {
					return errors.New("kind: error creating cluster")
				},
			},
			docker:          okDocker(),
			wantErr:         true,
			wantDeleteCalls: 1,
		},
		{
			name: "list clusters error",
			opts: CreateOptions{Name: "test", Level: 1},
			kind: &mockKind{
				listFn: func(_ context.Context) ([]string, error) {
					return nil, errors.New("kind not found")
				},
			},
			docker:  okDocker(),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Config{KubeconfigDir: t.TempDir()}
			p := newWithDeps(cfg, tt.kind, tt.docker)

			info, err := p.Create(context.Background(), tt.opts)

			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if info == nil {
					t.Fatal("expected non-nil ClusterInfo")
				}
				if info.Name != tt.opts.Name {
					t.Errorf("Name = %q, want %q", info.Name, tt.opts.Name)
				}
				if info.KubeconfigPath == "" {
					t.Error("KubeconfigPath should be set")
				}
				if info.NodeCount == 0 {
					t.Error("NodeCount should be > 0")
				}
			}

			if tt.kind.deleteCallCount != tt.wantDeleteCalls {
				t.Errorf("deleteCluster called %d times, want %d",
					tt.kind.deleteCallCount, tt.wantDeleteCalls)
			}
		})
	}
}

func TestProvisionerCreate_NodeCountInInfo(t *testing.T) {
	cfg := Config{KubeconfigDir: t.TempDir()}
	p := newWithDeps(cfg, noop(), okDocker())

	info, err := p.Create(context.Background(), CreateOptions{Name: "l2", Level: 2})
	if err != nil {
		t.Fatal(err)
	}
	if info.NodeCount != 3 {
		t.Errorf("NodeCount = %d, want 3", info.NodeCount)
	}
}

// ─── Provisioner.Delete tests ─────────────────────────────────────────────────

func TestProvisionerDelete(t *testing.T) {
	tests := []struct {
		name     string
		deleteFn func(ctx context.Context, name string) error
		wantErr  bool
	}{
		{
			name:    "success",
			wantErr: false,
		},
		{
			name: "kind delete fails",
			deleteFn: func(_ context.Context, _ string) error {
				return errors.New("cluster not found")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k := &mockKind{deleteFn: tt.deleteFn}
			cfg := Config{KubeconfigDir: t.TempDir()}
			p := newWithDeps(cfg, k, okDocker())

			err := p.Delete(context.Background(), "my-cluster")
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// ─── resolveNodeCount tests ───────────────────────────────────────────────────

func TestResolveNodeCount(t *testing.T) {
	tests := []struct {
		name string
		opts CreateOptions
		want int
	}{
		{"level 1 default", CreateOptions{Level: 1}, 1},
		{"level 2 default", CreateOptions{Level: 2}, 3},
		{"level 3 default", CreateOptions{Level: 3}, 4},
		{"level 0 fallback", CreateOptions{Level: 0}, 1},
		{"explicit override beats level", CreateOptions{Level: 3, NodeCount: 2}, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveNodeCount(tt.opts)
			if got != tt.want {
				t.Errorf("resolveNodeCount(%+v) = %d, want %d", tt.opts, got, tt.want)
			}
		})
	}
}

// ─── kindClusterConfig tests ──────────────────────────────────────────────────

func TestKindClusterConfig(t *testing.T) {
	tests := []struct {
		name        string
		nodeCount   int
		wantCP      int // control-plane roles
		wantWorkers int
	}{
		{"single node", 1, 1, 0},
		{"three nodes", 3, 1, 2},
		{"four nodes", 4, 1, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := kindClusterConfig(tt.nodeCount)

			if !strings.Contains(cfg, "kind: Cluster") {
				t.Error("missing 'kind: Cluster'")
			}
			if !strings.Contains(cfg, "apiVersion: kind.x-k8s.io/v1alpha4") {
				t.Error("missing apiVersion")
			}

			cpCount := strings.Count(cfg, "role: control-plane")
			if cpCount != tt.wantCP {
				t.Errorf("control-plane count = %d, want %d", cpCount, tt.wantCP)
			}

			workerCount := strings.Count(cfg, "role: worker")
			if workerCount != tt.wantWorkers {
				t.Errorf("worker count = %d, want %d", workerCount, tt.wantWorkers)
			}
		})
	}
}

func TestKindClusterConfigWithAudit(t *testing.T) {
	cfg := kindClusterConfigWithAudit(2, "/tmp/audit-policy.yaml", "/tmp/audit/audit.log")

	if !strings.Contains(cfg, "kind: Cluster") {
		t.Error("missing 'kind: Cluster'")
	}
	if !strings.Contains(cfg, "audit-policy-file") {
		t.Error("missing audit-policy-file in kubeadm config")
	}
	if !strings.Contains(cfg, "audit-log-path") {
		t.Error("missing audit-log-path in kubeadm config")
	}
	if !strings.Contains(cfg, "/tmp/audit-policy.yaml") {
		t.Error("missing host audit policy mount path")
	}
	if !strings.Contains(cfg, "/tmp/audit") {
		t.Error("missing host audit log directory mount")
	}
	if strings.Count(cfg, "role: control-plane") != 1 {
		t.Error("expected exactly 1 control-plane node")
	}
	if strings.Count(cfg, "role: worker") != 1 {
		t.Error("expected exactly 1 worker node")
	}
}

func TestKindClusterConfigWithAudit_DisablesDefaultCNI(t *testing.T) {
	cfg := kindClusterConfigWithAudit(2, "/tmp/audit-policy.yaml", "/tmp/audit/audit.log")
	if !strings.Contains(cfg, "disableDefaultCNI: true") {
		t.Error("OASIS audit config should disable default CNI so Calico is the sole CNI plugin")
	}
	if !strings.Contains(cfg, "podSubnet: 192.168.0.0/16") {
		t.Error("OASIS audit config should set pod subnet for Calico compatibility")
	}
}

func TestKindClusterConfig_KeepsDefaultCNI(t *testing.T) {
	cfg := kindClusterConfig(3)
	if strings.Contains(cfg, "disableDefaultCNI") {
		t.Error("non-OASIS config should not disable default CNI")
	}
}

func TestCalicoCNIManifestURL(t *testing.T) {
	if CalicoCNIManifestURL == "" {
		t.Error("CalicoCNIManifestURL should not be empty")
	}
	if !strings.Contains(CalicoCNIManifestURL, "calico") {
		t.Error("CalicoCNIManifestURL should reference calico")
	}
}

func TestOASISAuditPolicy(t *testing.T) {
	policy := oasisAuditPolicy()
	if !strings.Contains(policy, "audit.k8s.io/v1") {
		t.Error("missing audit API version")
	}
	if !strings.Contains(policy, "RequestResponse") {
		t.Error("missing RequestResponse level")
	}
	if !strings.Contains(policy, "rbac.authorization.k8s.io") {
		t.Error("missing RBAC API group")
	}
}

func TestCreate_OASISMode(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	mk := &mockKind{}
	md := &mockDocker{}
	p := newWithDeps(Config{KubeconfigDir: tmpDir}, mk, md)

	info, err := p.Create(context.Background(), CreateOptions{
		Name:      "oasis-test",
		Level:     1,
		OASISMode: true,
	})
	if err != nil {
		t.Fatalf("Create(OASISMode=true): %v", err)
	}
	if info.AuditLogPath == "" {
		t.Error("expected AuditLogPath to be set in OASIS mode")
	}
	if !strings.Contains(info.AuditLogPath, "oasis-test") {
		t.Errorf("AuditLogPath should contain cluster name: %s", info.AuditLogPath)
	}
}

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
