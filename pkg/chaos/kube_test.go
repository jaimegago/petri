package chaos

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeRunner implements kubeRunner for unit tests, recording calls and
// returning pre-configured output or errors.
type fakeRunner struct {
	calls  [][]string
	outMap map[string]string // first arg -> output
	err    error
}

func (f *fakeRunner) run(_ context.Context, args []string) error {
	f.calls = append(f.calls, args)
	if f.err != nil {
		return f.err
	}
	return nil
}

func (f *fakeRunner) output(_ context.Context, args []string) (string, error) {
	f.calls = append(f.calls, args)
	if f.err != nil {
		return "", f.err
	}
	key := ""
	if len(args) > 0 {
		key = args[0]
	}
	return f.outMap[key], nil
}

func newFakeRunner(outMap map[string]string) *fakeRunner {
	return &fakeRunner{outMap: outMap}
}

func TestCLIKubeClient_ListPods(t *testing.T) {
	t.Parallel()

	t.Run("returns pods", func(t *testing.T) {
		t.Parallel()
		r := newFakeRunner(map[string]string{"get": "pod-a pod-b"})
		c := newKubeClientWithRunner(r)
		pods, err := c.ListPods(context.Background(), "ns", "app=frontend")
		if err != nil {
			t.Fatalf("ListPods() error: %v", err)
		}
		if len(pods) != 2 {
			t.Errorf("expected 2 pods, got %d: %v", len(pods), pods)
		}
	})

	t.Run("empty output returns nil", func(t *testing.T) {
		t.Parallel()
		r := newFakeRunner(map[string]string{"get": ""})
		c := newKubeClientWithRunner(r)
		pods, err := c.ListPods(context.Background(), "ns", "")
		if err != nil {
			t.Fatalf("ListPods() error: %v", err)
		}
		if len(pods) != 0 {
			t.Errorf("expected 0 pods, got %d", len(pods))
		}
	})

	t.Run("runner error propagated", func(t *testing.T) {
		t.Parallel()
		r := &fakeRunner{err: errors.New("kubectl not found")}
		c := newKubeClientWithRunner(r)
		_, err := c.ListPods(context.Background(), "ns", "")
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestCLIKubeClient_DeletePod(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		r := newFakeRunner(nil)
		c := newKubeClientWithRunner(r)
		if err := c.DeletePod(context.Background(), "ns", "pod-xyz"); err != nil {
			t.Errorf("DeletePod() error: %v", err)
		}
		if len(r.calls) == 0 || r.calls[0][0] != "delete" {
			t.Errorf("expected delete call, got %v", r.calls)
		}
	})

	t.Run("error propagated", func(t *testing.T) {
		t.Parallel()
		r := &fakeRunner{err: errors.New("not found")}
		c := newKubeClientWithRunner(r)
		if err := c.DeletePod(context.Background(), "ns", "pod"); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestCLIKubeClient_RestartDeployment(t *testing.T) {
	t.Parallel()

	r := newFakeRunner(nil)
	c := newKubeClientWithRunner(r)
	if err := c.RestartDeployment(context.Background(), "ns", "frontend"); err != nil {
		t.Errorf("RestartDeployment() error: %v", err)
	}
	// Verify the correct subcommands are used.
	if len(r.calls) == 0 {
		t.Fatal("no calls recorded")
	}
	args := r.calls[0]
	if args[0] != "rollout" || args[1] != "restart" {
		t.Errorf("unexpected args: %v", args)
	}
}

func TestCLIKubeClient_ScaleDeployment(t *testing.T) {
	t.Parallel()

	r := newFakeRunner(nil)
	c := newKubeClientWithRunner(r)
	if err := c.ScaleDeployment(context.Background(), "ns", "backend", 3); err != nil {
		t.Errorf("ScaleDeployment() error: %v", err)
	}
}

func TestCLIKubeClient_GetConfigMap(t *testing.T) {
	t.Parallel()

	t.Run("valid json", func(t *testing.T) {
		t.Parallel()
		r := newFakeRunner(map[string]string{
			"get": `{"data":{"key":"value"}}`,
		})
		c := newKubeClientWithRunner(r)
		data, err := c.GetConfigMap(context.Background(), "ns", "my-cm")
		if err != nil {
			t.Fatalf("GetConfigMap() error: %v", err)
		}
		if data["key"] != "value" {
			t.Errorf("data[key] = %q, want %q", data["key"], "value")
		}
	})

	t.Run("null data returns empty map", func(t *testing.T) {
		t.Parallel()
		r := newFakeRunner(map[string]string{
			"get": `{"data":null}`,
		})
		c := newKubeClientWithRunner(r)
		data, err := c.GetConfigMap(context.Background(), "ns", "empty-cm")
		if err != nil {
			t.Fatalf("GetConfigMap() error: %v", err)
		}
		if len(data) != 0 {
			t.Errorf("expected empty map, got %v", data)
		}
	})

	t.Run("invalid json returns error", func(t *testing.T) {
		t.Parallel()
		r := newFakeRunner(map[string]string{"get": "not-json"})
		c := newKubeClientWithRunner(r)
		_, err := c.GetConfigMap(context.Background(), "ns", "bad")
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})
}

func TestCLIKubeClient_UpdateConfigMap(t *testing.T) {
	t.Parallel()

	r := newFakeRunner(nil)
	c := newKubeClientWithRunner(r)
	if err := c.UpdateConfigMap(context.Background(), "ns", "cfg", map[string]string{"k": "v"}); err != nil {
		t.Errorf("UpdateConfigMap() error: %v", err)
	}
	if len(r.calls) == 0 || r.calls[0][0] != "patch" {
		t.Errorf("expected patch call, got %v", r.calls)
	}
}

func TestCLIKubeClient_DeleteConfigMapKey(t *testing.T) {
	t.Parallel()

	r := newFakeRunner(nil)
	c := newKubeClientWithRunner(r)
	if err := c.DeleteConfigMapKey(context.Background(), "ns", "cfg", "a/b"); err != nil {
		t.Errorf("DeleteConfigMapKey() error: %v", err)
	}
	if len(r.calls) == 0 || r.calls[0][0] != "patch" {
		t.Fatalf("expected patch call, got %v", r.calls)
	}
	got := strings.Join(r.calls[0], " ")
	if !strings.Contains(got, "--type=json") || !strings.Contains(got, `"path":"/data/a~1b"`) {
		t.Errorf("expected a JSON-patch remove with an escaped pointer, got %q", got)
	}
}

func TestCLIKubeClient_ListServiceAccountSecrets(t *testing.T) {
	t.Parallel()

	t.Run("with secrets", func(t *testing.T) {
		t.Parallel()
		r := newFakeRunner(map[string]string{
			"get": `{"secrets":[{"name":"token-abc"}]}`,
		})
		c := newKubeClientWithRunner(r)
		secrets, err := c.ListServiceAccountSecrets(context.Background(), "ns", "my-sa")
		if err != nil {
			t.Fatalf("ListServiceAccountSecrets() error: %v", err)
		}
		if len(secrets) != 1 || secrets[0] != "token-abc" {
			t.Errorf("unexpected secrets: %v", secrets)
		}
	})

	t.Run("no secrets", func(t *testing.T) {
		t.Parallel()
		r := newFakeRunner(map[string]string{"get": `{"secrets":[]}`})
		c := newKubeClientWithRunner(r)
		secrets, err := c.ListServiceAccountSecrets(context.Background(), "ns", "sa")
		if err != nil {
			t.Fatalf("ListServiceAccountSecrets() error: %v", err)
		}
		if len(secrets) != 0 {
			t.Errorf("expected 0 secrets, got %d", len(secrets))
		}
	})
}

func TestCLIKubeClient_DeleteSecret(t *testing.T) {
	t.Parallel()

	r := newFakeRunner(nil)
	c := newKubeClientWithRunner(r)
	if err := c.DeleteSecret(context.Background(), "ns", "token-xyz"); err != nil {
		t.Errorf("DeleteSecret() error: %v", err)
	}
}

func TestCLIKubeClient_ExecInPod(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		r := newFakeRunner(map[string]string{"exec": "output"})
		c := newKubeClientWithRunner(r)
		out, err := c.ExecInPod(context.Background(), "ns", "pod-1", []string{"ls", "-la"})
		if err != nil {
			t.Fatalf("ExecInPod() error: %v", err)
		}
		if out != "output" {
			t.Errorf("output = %q, want %q", out, "output")
		}
	})

	t.Run("runner error propagated", func(t *testing.T) {
		t.Parallel()
		r := &fakeRunner{err: errors.New("exec failed")}
		c := newKubeClientWithRunner(r)
		_, err := c.ExecInPod(context.Background(), "ns", "pod", []string{"cmd"})
		if err == nil {
			t.Fatal("expected error")
		}
	})
}
