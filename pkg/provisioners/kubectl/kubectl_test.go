package kubectl

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// ─── mock runner ──────────────────────────────────────────────────────────────

type mockRunner struct {
	runFn    func(ctx context.Context, args []string) error
	outputFn func(ctx context.Context, args []string) (string, error)
	// track last call for assertion
	lastArgs []string
}

func (m *mockRunner) run(ctx context.Context, args []string) error {
	m.lastArgs = args
	if m.runFn != nil {
		return m.runFn(ctx, args)
	}
	return nil
}

func (m *mockRunner) output(ctx context.Context, args []string) (string, error) {
	m.lastArgs = args
	if m.outputFn != nil {
		return m.outputFn(ctx, args)
	}
	return "", nil
}

// ─── ApplyManifest tests ──────────────────────────────────────────────────────

func TestApplyManifest(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		runFn    func(ctx context.Context, args []string) error
		wantErr  bool
		wantArg  string // substring expected in args
	}{
		{
			name:     "success",
			manifest: "apiVersion: v1\nkind: Namespace\n",
			wantArg:  "apply",
		},
		{
			name:     "kubectl error propagated",
			manifest: "invalid yaml",
			runFn: func(_ context.Context, _ []string) error {
				return errors.New("error validating data")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &mockRunner{runFn: tt.runFn}
			c := newWithRunner(r)

			err := c.ApplyManifest(context.Background(), tt.manifest)

			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tt.wantArg != "" && !containsArg(r.lastArgs, tt.wantArg) {
					t.Errorf("args %v missing %q", r.lastArgs, tt.wantArg)
				}
			}
		})
	}
}

// ─── WaitForNodes tests ───────────────────────────────────────────────────────

func TestWaitForNodes(t *testing.T) {
	tests := []struct {
		name    string
		runFn   func(ctx context.Context, args []string) error
		timeout time.Duration
		wantErr bool
	}{
		{
			name:    "success",
			timeout: 2 * time.Minute,
			wantErr: false,
		},
		{
			name: "timeout error propagated",
			runFn: func(_ context.Context, _ []string) error {
				return errors.New("timed out waiting for the condition")
			},
			timeout: time.Second,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &mockRunner{runFn: tt.runFn}
			c := newWithRunner(r)

			err := c.WaitForNodes(context.Background(), tt.timeout)

			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				// Verify --for=condition=Ready and --all are included.
				if !containsArg(r.lastArgs, "--for=condition=Ready") {
					t.Errorf("args missing --for=condition=Ready: %v", r.lastArgs)
				}
				if !containsArg(r.lastArgs, "--all") {
					t.Errorf("args missing --all: %v", r.lastArgs)
				}
				if !containsArg(r.lastArgs, "--timeout") {
					t.Errorf("args missing --timeout: %v", r.lastArgs)
				}
			}
		})
	}
}

// ─── WaitForRollout tests ─────────────────────────────────────────────────────

func TestWaitForRollout(t *testing.T) {
	var capturedArgs []string
	r := &mockRunner{
		runFn: func(_ context.Context, args []string) error {
			capturedArgs = args
			return nil
		},
	}
	c := newWithRunner(r)

	if err := c.WaitForRollout(context.Background(), "production", "frontend", 3*time.Minute); err != nil {
		t.Fatalf("WaitForRollout: %v", err)
	}

	if !containsArg(capturedArgs, "deployment/frontend") {
		t.Errorf("args missing deployment/frontend: %v", capturedArgs)
	}
	if !containsArg(capturedArgs, "production") {
		t.Errorf("args missing namespace: %v", capturedArgs)
	}
	if !containsArg(capturedArgs, "3m") {
		t.Errorf("args missing 3m timeout: %v", capturedArgs)
	}
}

func TestWaitForRollout_Error(t *testing.T) {
	r := &mockRunner{
		runFn: func(_ context.Context, _ []string) error {
			return errors.New("deployment not progressing")
		},
	}
	err := newWithRunner(r).WaitForRollout(context.Background(), "ns", "svc", time.Minute)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "rollout status") {
		t.Errorf("unexpected error format: %v", err)
	}
}

// ─── GetNodeCount tests ───────────────────────────────────────────────────────

func TestGetNodeCount(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		outputFn func(ctx context.Context, args []string) (string, error)
		want     int
		wantErr  bool
	}{
		{
			name: "three nodes",
			output: "node1   Ready    control-plane   5m   v1.27.0\n" +
				"node2   Ready    <none>          5m   v1.27.0\n" +
				"node3   Ready    <none>          5m   v1.27.0",
			want: 3,
		},
		{
			name:   "single node",
			output: "node1   Ready    control-plane   5m   v1.27.0",
			want:   1,
		},
		{
			name:   "empty cluster",
			output: "",
			want:   0,
		},
		{
			name: "kubectl error",
			outputFn: func(_ context.Context, _ []string) (string, error) {
				return "", errors.New("connection refused")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &mockRunner{
				outputFn: func(_ context.Context, _ []string) (string, error) {
					if tt.outputFn != nil {
						return tt.outputFn(context.Background(), nil)
					}
					return tt.output, nil
				},
			}
			c := newWithRunner(r)

			count, err := c.GetNodeCount(context.Background())
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if count != tt.want {
					t.Errorf("GetNodeCount = %d, want %d", count, tt.want)
				}
			}
		})
	}
}

// ─── formatDuration tests ─────────────────────────────────────────────────────

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{60 * time.Second, "1m"},
		{5 * time.Minute, "5m"},
		{90 * time.Second, "90s"},
		{2*time.Minute + 30*time.Second, "150s"},
		{10 * time.Minute, "10m"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatDuration(tt.d)
			if got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

// ─── cliRunner baseArgs test ──────────────────────────────────────────────────

func TestCLIRunnerBaseArgs(t *testing.T) {
	t.Run("with kubeconfig", func(t *testing.T) {
		r := &cliRunner{binary: "kubectl", kubeconfig: "/tmp/kube.yaml"}
		args := r.baseArgs()
		if len(args) != 2 || args[0] != "--kubeconfig" || args[1] != "/tmp/kube.yaml" {
			t.Errorf("baseArgs = %v, want [--kubeconfig /tmp/kube.yaml]", args)
		}
	})
	t.Run("without kubeconfig", func(t *testing.T) {
		r := &cliRunner{binary: "kubectl"}
		args := r.baseArgs()
		if len(args) != 0 {
			t.Errorf("baseArgs = %v, want []", args)
		}
	})
}

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

// ─── helper ───────────────────────────────────────────────────────────────────

func containsArg(args []string, target string) bool {
	for _, a := range args {
		if a == target {
			return true
		}
	}
	return false
}
